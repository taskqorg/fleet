package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: a project app (docs/designs/fleet-project-apps.md) is asked for with an owner card; Approve makes it
// active with one key, taken once; on /api/apps a request runs only with the fleet-apps edge token's client id and a
// live app key, reaches only the app operations and only its project, and a revoke ends the key at once. Risk: an app
// key opens Fleet beyond its project's pipelines, a request without the edge token gets in, or a revoked app keeps
// calling.
func TestProjectAppKeyAndRoute(t *testing.T) {
	e := openTestCoordinator(t, false)
	secrets := t.TempDir()
	t.Setenv("FLEET_SECRET_DIR", secrets)
	if err := os.MkdirAll(filepath.Join(secrets, "apps"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secrets, "apps", "edge.env"), []byte("CF_ACCESS_CLIENT_ID=edge-cid.access\nCF_ACCESS_CLIENT_SECRET=s\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := e.ownerHeaders()

	var asked contract.ProjectAppView
	e.opInto(t, "project_app_ask", doc{"projectId": "desiign", "requestId": "r1", "id": "desiign-site", "name": "Desiign site",
		"pipelines": []string{"doc-recognize-agent"}, "callbackOrigins": []string{"https://example.com"}, "reason": "checks uploads"}, owner, &asked)
	if asked.State != "asked" || asked.QuestionId == nil || len(asked.Keys) != 0 {
		t.Fatalf("asked app %+v, want asked with a card and no key", asked)
	}
	var again contract.ProjectAppView
	e.opInto(t, "project_app_ask", doc{"projectId": "desiign", "requestId": "r1", "id": "desiign-site", "name": "Desiign site",
		"pipelines": []string{"doc-recognize-agent"}, "callbackOrigins": []string{"https://example.com"}, "reason": "checks uploads"}, owner, &again)
	if deref(again.QuestionId) != deref(asked.QuestionId) {
		t.Fatalf("the same ask made a new card")
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/project_app_key_take", doc{"appId": "desiign-site"}, owner); code != 409 {
		t.Fatalf("key of an asked app: %d, want 409", code)
	}
	e.opInto(t, "question_answer", doc{"questionId": *asked.QuestionId, "answer": approvalYes}, owner, &map[string]any{})
	var take contract.ProjectAppKeyTakeReply
	e.opInto(t, "project_app_key_take", doc{"appId": "desiign-site"}, owner, &take)
	if take.Key == "" || deref(take.EdgeClientId) != "edge-cid.access" {
		t.Fatalf("take %+v, want the key and the edge token", take.KeyId)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/project_app_key_take", doc{"appId": "desiign-site"}, owner); code != 409 {
		t.Fatalf("second take: %d, want 409", code)
	}

	app := func(edge, key string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": edge, "X-Fleet-App-Key": key}
	}
	ok := app("edge-cid.access", take.Key)
	if code, body := e.callJSON(t, "POST", "/api/apps/ops/apps_ping", doc{}, ok); code != 200 || body["app"] != "desiign-site" || body["projectId"] != "desiign" {
		t.Fatalf("ping %d %v", code, body)
	}
	for _, c := range []struct {
		name    string
		headers map[string]string
		op      string
		body    doc
		want    int
	}{
		{"other edge name", app(testOwnerEmail, take.Key), "apps_ping", doc{}, 403},
		{"no key", app("edge-cid.access", ""), "apps_ping", doc{}, 401},
		{"wrong key", app("edge-cid.access", "fla_nope"), "apps_ping", doc{}, 401},
		{"not an app operation", ok, "project_list", doc{}, 403},
		{"not an app operation (ask)", ok, "project_app_ask", doc{"projectId": "desiign"}, 403},
		{"other project", ok, "pipeline_history", doc{"projectId": "hello-world"}, 404},
	} {
		if code, body := e.callJSON(t, "POST", "/api/apps/ops/"+c.op, c.body, c.headers); code != c.want {
			t.Errorf("%s: %d %v, want %d", c.name, code, body["message"], c.want)
		}
	}
	if code, _ := e.callJSON(t, "POST", "/api/apps/ops/pipeline_history", doc{"projectId": "desiign"}, ok); code != 200 {
		t.Errorf("own project's history: %d, want 200", code)
	}

	if deref(take.CallbackSecret) == "" {
		t.Fatalf("take has no callback secret")
	}

	// Inputs by reference, callbacks and limits are refused before the run is planned or any file is read.
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO access_server (id, address) VALUES ('pds9', '10.0.0.9')`)
	exec(`INSERT INTO access_grant (id, project_id, server_id, kind, level, state, request_id, asked_by, payload)
	 VALUES ('ag-sql', 'desiign', 'pds9', 'mysql', 'read', 'ready', 'g1', 'agent:worker', '{"connection": "desiign-mysql"}')`)
	run := func(rid string, extra doc) doc {
		b := doc{"projectId": "desiign", "requestId": rid, "query": "read", "wait": false}
		for k, v := range extra {
			b[k] = v
		}
		return b
	}
	ref := func(conn string) doc {
		return doc{"inputs": []doc{{"path": "a.pdf", "ref": doc{"connection": conn, "path": "/srv/a.pdf"}}}}
	}
	for _, c := range []struct {
		name    string
		headers map[string]string
		body    doc
		want    int
		message string
	}{
		{"unknown connection", ok, run("ref1", ref("nope-files")), 400, "input:a.pdf: unknown connection nope-files"},
		{"not a files grant", ok, run("ref2", ref("desiign-mysql")), 400, "input:a.pdf: desiign-mysql is a mysql grant, not a files grant"},
		{"artifact and ref", ok, run("ref3", doc{"inputs": []doc{{"path": "a.pdf", "artifactId": "x", "ref": doc{"connection": "c", "path": "/a"}}}}), 400, "input:a.pdf: give either artifactId or ref"},
		{"callback off its origins", ok, run("cb1", doc{"notify": doc{"url": "https://other.example/hook"}}), 400, ""},
		{"callback over http", ok, run("cb2", doc{"notify": doc{"url": "http://example.com/hook"}}), 400, ""},
		{"callback of a person", owner, run("cb3", doc{"notify": doc{"url": "https://example.com/hook"}}), 400, ""},
	} {
		path := "/api/apps/ops/pipeline_run"
		if c.headers["X-Fleet-App-Key"] == "" {
			path = "/api/ops/pipeline_run"
		}
		if code, body := e.callJSON(t, "POST", path, c.body, c.headers); code != c.want || !strings.HasPrefix(fmt.Sprint(body["message"]), c.message) {
			t.Errorf("%s: %d %v, want %d %s", c.name, code, body["message"], c.want, c.message)
		}
	}
	e.opInto(t, "project_app_set", doc{"appId": "desiign-site", "limits": doc{"runsPerDay": 1, "concurrentRuns": 5}}, owner, &map[string]any{})
	exec(`INSERT INTO execution (id, kind, state, project_id, started_at, app_id, data, observation, config_ref)
	 VALUES ('app-run-1', 'pipeline_run', 'succeeded', 'desiign', now(), 'desiign-site', '{}', 'confirmed', '{}')`)
	if code, body := e.callJSON(t, "POST", "/api/apps/ops/pipeline_run", run("lim1", nil), ok); code != 429 || body["message"] != "limit:runs-per-day" {
		t.Errorf("over runsPerDay: %d %v, want 429 limit:runs-per-day", code, body["message"])
	}
	e.opInto(t, "project_app_set", doc{"appId": "desiign-site", "limits": doc{"concurrentRuns": 1}}, owner, &map[string]any{})
	exec(`INSERT INTO execution (id, kind, state, project_id, started_at, app_id, data, observation, config_ref)
	 VALUES ('app-run-2', 'pipeline_run', 'running', 'desiign', now() - interval '2 days', 'desiign-site', '{}', 'confirmed', '{}')`)
	if code, body := e.callJSON(t, "POST", "/api/apps/ops/pipeline_run", run("lim2", nil), ok); code != 429 || body["message"] != "limit:concurrent-runs" {
		t.Errorf("over concurrentRuns: %d %v, want 429 limit:concurrent-runs", code, body["message"])
	}

	var apps contract.ProjectAppsReply
	e.opInto(t, "project_apps", doc{"projectId": "desiign"}, owner, &apps)
	if len(apps.Apps) != 1 || apps.Apps[0].State != "active" || apps.Apps[0].LastUsedAt == nil || len(apps.Apps[0].Keys) != 1 || apps.Apps[0].Keys[0].TakenAt == nil {
		t.Fatalf("apps %+v", apps.Apps)
	}
	e.opInto(t, "project_app_revoke", doc{"appId": "desiign-site"}, owner, &map[string]any{})
	if code, _ := e.callJSON(t, "POST", "/api/apps/ops/apps_ping", doc{}, ok); code != 401 {
		t.Fatalf("revoked app: %d, want 401", code)
	}
}

// Requirement: a callback's X-Fleet-Signature is the hex HMAC-SHA256 of the exact body with the app's callback secret,
// and the body names the run and its end, never its result. Risk: an app cannot verify a callback, or one leaks
// personal result fields.
func TestAppCallbackSignature(t *testing.T) {
	end := int64(1764200000000)
	body := jsonBytes(appCallbackBody{RunID: "run-1", RequestID: "r-1", State: "succeeded", FinishedAt: &end})
	if string(body) != `{"runId":"run-1","requestId":"r-1","state":"succeeded","finishedAt":1764200000000}` {
		t.Fatalf("body %s", body)
	}
	m := hmac.New(sha256.New, []byte("fcs_secret"))
	m.Write(body)
	if got, want := appCallbackSign("fcs_secret", body), "sha256="+hex.EncodeToString(m.Sum(nil)); got != want {
		t.Fatalf("signature %s, want %s", got, want)
	}
	if appCallbackSign("fcs_other", body) == appCallbackSign("fcs_secret", body) {
		t.Fatalf("the signature does not depend on the secret")
	}
}

// Requirement: an app's 30-day use (project_apps use30d) and a pipeline's callers (project_pipeline_view use) count the
// pipeline runs and failed runs of the window and the cost of their leaf executions only, each leaf once; the pipeline
// page leaves Lab runs out and puts agents and people after the apps. Risk: a container's spend counts, an older or Lab
// run counts, or app runs are mixed with people's.
func TestProjectAppUse(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`INSERT INTO project_app (id, project_id, name, state, request_id, asked_by, payload) VALUES ('use-site', 'desiign', 'Use site', 'active', 'u1', 'owner', '{}')`)
	now := time.Now().UTC()
	run := func(id, kind, state, parent, app, experiment string, ago time.Duration, usd float64) {
		t.Helper()
		at := now.Add(-ago)
		exec(`INSERT INTO execution (id, kind, state, project_id, started_at, ended_at, data, observation, config_ref, parent_execution_id, app_id, experiment_id, pipeline_id)
		 VALUES ($1, $2::execution_kind, $3::execution_state, 'desiign', $4, $4, '{}', 'confirmed', '{}', nullif($5, ''), nullif($6, ''), nullif($7, ''),
		  CASE WHEN $2 = 'pipeline_run' THEN 'use-pipe' END)`, id, kind, state, at, parent, app, experiment)
		if usd > 0 {
			exec(`INSERT INTO execution_spend (execution_id, project_id, started_at, kind, state, purpose, cost_currency, cost_basis, cost_value)
			 VALUES ($1, 'desiign', $2, $3::execution_kind, $4::execution_state, 'other', 'USD', 'estimated', $5)
			 ON CONFLICT (execution_id) DO UPDATE SET cost_currency = 'USD', cost_basis = 'estimated', cost_value = $5, stale = false, payload = NULL, cost_unpriced = 0`,
				id, at, kind, state, usd)
		}
	}
	// App: two runs in the window (one failed) with a step, a leaf under the step and a tool call under the leaf; the
	// run's and the step's own spend must not count.
	run("use-r1", "pipeline_run", "succeeded", "", "use-site", "", time.Hour, 100)
	run("use-s1", "pipeline_step", "succeeded", "use-r1", "", "", time.Hour, 50)
	run("use-a1", "agent_run", "succeeded", "use-s1", "", "", time.Hour, 1.5)
	run("use-t1", "tool_run", "succeeded", "use-a1", "", "", time.Hour, 0.25)
	run("use-r2", "pipeline_run", "failed", "", "use-site", "", 2*time.Hour, 0)
	run("use-a2", "agent_run", "failed", "use-r2", "", "", 2*time.Hour, 0.5)
	run("use-r3", "pipeline_run", "succeeded", "", "use-site", "", 40*24*time.Hour, 0) // older than 30 days
	run("use-a3", "agent_run", "succeeded", "use-r3", "", "", 40*24*time.Hour, 9)
	// Agents and people: one run. Lab: one run, left out.
	run("use-r4", "pipeline_run", "succeeded", "", "", "", 3*time.Hour, 0)
	run("use-a4", "agent_run", "succeeded", "use-r4", "", "", 3*time.Hour, 2)
	run("use-r5", "pipeline_run", "succeeded", "", "", "use-x", 3*time.Hour, 0)
	run("use-a5", "agent_run", "succeeded", "use-r5", "", "", 3*time.Hour, 7)

	usd := func(cs []contract.CostFigure) float64 {
		for _, c := range cs {
			if deref(c.Currency) == "USD" && c.Value != nil {
				return *c.Value
			}
		}
		return -1
	}
	var apps contract.ProjectAppsReply
	e.opInto(t, "project_apps", doc{"projectId": "desiign"}, e.ownerHeaders(), &apps)
	if len(apps.Apps) != 1 {
		t.Fatalf("apps %+v", apps.Apps)
	}
	if u := apps.Apps[0].Use30d; u.Runs != 2 || u.Failed != 1 || usd(u.Cost) != 2.25 {
		t.Fatalf("use30d %+v (USD %v), want 2 runs, 1 failed, $2.25", u, usd(u.Cost))
	}
	var use []contract.ProjectPipelineUse
	if err := e.app.store.Read(e.ctx, nil, func(q *Request) (err error) {
		use, err = projectPipelineUse(q, "desiign", "use-pipe")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if len(use) != 2 || use[0].Caller.Kind != "app" || deref(use[0].Caller.Name) != "Use site" || use[0].Runs != 2 || use[0].Failed != 1 ||
		usd(use[0].Cost) != 2.25 || use[1].Caller.Kind != "people" || use[1].Runs != 1 || usd(use[1].Cost) != 2 {
		t.Fatalf("pipeline use %+v", use)
	}
}
