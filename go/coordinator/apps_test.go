package main

import (
	"os"
	"path/filepath"
	"testing"

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
