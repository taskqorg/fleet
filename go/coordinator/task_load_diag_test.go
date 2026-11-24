package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"fleet/contract"
	"fleet/obs"
)

// Requirement: fleet.md Observability & T-FLT-20: taskLoad 404s emit structured diagnostic logs and span attributes
// recording task_id, effective person/actor, project_ids scope, deleted/tombstone visibility, row existence outside RLS,
// and err without logging credentials or adding noise to normal 200 responses, while preserving normal 404s and tx scope.
// Risk: intermittent task_get 404s remain undiagnosable in production telemetry, diagnostic logging leaks secrets or
// fires on 200s, or probing outside RLS leaves widened scope/deleted visibility active on the caller's transaction.
func TestTaskLoad404Diagnostics(t *testing.T) {
	e := openTestCoordinator(t, false)
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES
 ('project','diag-hidden','config/projects/diag-hidden/project.json',1,'diag-hidden'),
 ('project','diag-del','config/projects/diag-del/project.json',1,'diag-del');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','diag-hidden',1,'{"settings":{"id":"diag-hidden","name":"Hidden","contextFiles":[]},"contextFiles":[]}','h1','editor','person','t'),
 ('project','diag-del',1,'{"settings":{"id":"diag-del","name":"Deleted","contextFiles":[]},"contextFiles":[]}','x1','editor','person','t');
UPDATE definition SET deleted_at = now(), retired_at = now() WHERE kind = 'project' AND id = 'diag-del';
INSERT INTO task(id,title,project_id,state) VALUES
 ('t-visible','Visible task','desiign','in_progress'),
 ('t-hidden','Hidden project task','diag-hidden','in_progress'),
 ('t-del-proj','Deleted project task','diag-del','in_progress');
INSERT INTO task(id,title,project_id,state,tombstoned_at) VALUES
 ('t-tomb','Tombstoned task','desiign','in_progress',now());
`)

	var mu sync.Mutex
	var logBuf bytes.Buffer
	var spans []*obs.Span
	oldLogger := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logBuf, nil)))
	obs.SetSink(func(s *obs.Span) {
		mu.Lock()
		spans = append(spans, s)
		mu.Unlock()
	})
	t.Cleanup(func() {
		obs.SetSink(nil)
		slog.SetDefault(oldLogger)
	})

	obsSrv := httptest.NewServer(obs.Handler(e.app.routes(), spanName))
	t.Cleanup(obsSrv.Close)

	callTaskGet := func(personEmail, taskID string) (int, map[string]any, *obs.Span, string) {
		t.Helper()
		mu.Lock()
		logBuf.Reset()
		spans = nil
		mu.Unlock()

		raw, err := json.Marshal(contract.TaskGetRequest{TaskId: taskID})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequestWithContext(e.ctx, "POST", obsSrv.URL+"/api/ops/task_get", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+e.browserSecret)
		req.Header.Set("X-Fleet-Person", personEmail)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST task_get: %v", err)
		}
		defer resp.Body.Close()
		var parsed map[string]any
		_ = json.NewDecoder(resp.Body).Decode(&parsed)

		mu.Lock()
		var span *obs.Span
		if len(spans) > 0 {
			span = spans[len(spans)-1]
		}
		logs := logBuf.String()
		mu.Unlock()
		return resp.StatusCode, parsed, span, logs
	}

	parseDiagLog := func(t *testing.T, logs string) map[string]any {
		t.Helper()
		if strings.Contains(logs, e.browserSecret) {
			t.Fatalf("diagnostic log leaked browser secret")
		}
		for _, line := range strings.Split(strings.TrimSpace(logs), "\n") {
			if line == "" {
				continue
			}
			var rec map[string]any
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatalf("unmarshal slog line %q: %v", line, err)
			}
			if rec["msg"] == "taskLoad: not found" {
				return rec
			}
		}
		t.Fatalf("expected taskLoad: not found log in %q", logs)
		return nil
	}

	// 1) Normal 200 path must not log diagnostic warnings or attach 404 attributes.
	code, body, span, logs := callTaskGet("member@trial.test", "t-visible")
	if code != 200 || body["id"] != "t-visible" {
		t.Fatalf("200 task_get = %d %v", code, body)
	}
	if strings.Contains(logs, "taskLoad: not found") {
		t.Fatalf("unexpected diagnostic log on 200: %s", logs)
	}
	if span == nil {
		t.Fatal("expected span on 200 request")
	}
	for k := range span.Attrs {
		if strings.HasPrefix(k, "fleet.task.not_found.") {
			t.Fatalf("unexpected not_found span attr %q on 200", k)
		}
	}

	// 2) Hidden by RLS (row exists live in another project): 404 + diagnostic attrs.
	code, body, span, logs = callTaskGet("member@trial.test", "t-hidden")
	if code != 404 || body["statusCode"] != float64(404) {
		t.Fatalf("rls-hidden task_get = %d %v", code, body)
	}
	rec := parseDiagLog(t, logs)
	if rec["task_id"] != "t-hidden" || rec["person"] != "member@trial.test" || rec["actor"] != "member@trial.test" ||
		rec["project_ids"] != "desiign" || rec["deleted_visible"] != false ||
		rec["exists"] != true || rec["row_project_id"] != "diag-hidden" ||
		rec["tombstoned"] != false || rec["project_deleted"] != false ||
		rec["err"] != "no rows in result set" {
		t.Fatalf("unexpected rls-hidden log record: %#v", rec)
	}
	if span == nil || span.Attrs["fleet.task.not_found.task_id"] != "t-hidden" ||
		span.Attrs["fleet.task.not_found.person"] != "member@trial.test" ||
		span.Attrs["fleet.task.not_found.project_ids"] != "desiign" ||
		span.Attrs["fleet.task.not_found.deleted_visible"] != "false" ||
		span.Attrs["fleet.task.not_found.exists"] != "true" ||
		span.Attrs["fleet.task.not_found.row_project_id"] != "diag-hidden" ||
		span.Attrs["fleet.task.not_found.tombstoned"] != "false" ||
		span.Attrs["fleet.task.not_found.project_deleted"] != "false" ||
		span.Attrs["fleet.task.not_found.err"] != "no rows in result set" {
		t.Fatalf("unexpected rls-hidden span attrs: %#v", span)
	}
	for k, v := range span.Attrs {
		if strings.Contains(v, e.browserSecret) {
			t.Fatalf("span attr %s leaked browser secret", k)
		}
	}

	// 3) Tombstoned task requested by owner: 404 with exists=true, tombstoned=true.
	code, _, span, logs = callTaskGet(testOwnerEmail, "t-tomb")
	if code != 404 {
		t.Fatalf("tombstoned task_get = %d, want 404", code)
	}
	rec = parseDiagLog(t, logs)
	if rec["person"] != testOwnerEmail || rec["project_ids"] != "*" ||
		rec["exists"] != true || rec["tombstoned"] != true || rec["project_deleted"] != false {
		t.Fatalf("unexpected tombstoned log record: %#v", rec)
	}
	if span == nil || span.Attrs["fleet.task.not_found.tombstoned"] != "true" || span.Attrs["fleet.task.not_found.exists"] != "true" {
		t.Fatalf("unexpected tombstoned span attrs: %#v", span)
	}

	// 4) Deleted project task requested by owner: 404 with exists=true, project_deleted=true.
	code, _, span, logs = callTaskGet(testOwnerEmail, "t-del-proj")
	if code != 404 {
		t.Fatalf("deleted-project task_get = %d, want 404", code)
	}
	rec = parseDiagLog(t, logs)
	if rec["exists"] != true || rec["tombstoned"] != false || rec["project_deleted"] != true || rec["row_project_id"] != "diag-del" {
		t.Fatalf("unexpected deleted-project log record: %#v", rec)
	}
	if span == nil || span.Attrs["fleet.task.not_found.project_deleted"] != "true" {
		t.Fatalf("unexpected deleted-project span attrs: %#v", span)
	}

	// 5) Truly missing task: 404 with exists=false.
	code, _, span, logs = callTaskGet(testOwnerEmail, "t-missing")
	if code != 404 {
		t.Fatalf("missing task_get = %d, want 404", code)
	}
	rec = parseDiagLog(t, logs)
	if rec["exists"] != false || rec["tombstoned"] != false || rec["project_deleted"] != false {
		t.Fatalf("unexpected missing log record: %#v", rec)
	}
	if span == nil || span.Attrs["fleet.task.not_found.exists"] != "false" {
		t.Fatalf("unexpected missing span attrs: %#v", span)
	}

	// 6) Inspecting 404 visibility inside an active transaction restores the caller's scope and deleted visibility.
	member := e.app.peopleSnapshot().person("member@trial.test")
	if member == nil {
		t.Fatal("missing member@trial.test in people snapshot")
	}
	if err := e.app.store.Read(e.ctx, scopedRequest(e.ctx, e.app, member), func(q *Request) error {
		if err := refreshRequestAccess(q); err != nil {
			return err
		}
		if _, err := taskLoad(q, "t-hidden"); !errors.Is(err, errNotFound) {
			t.Fatalf("taskLoad(t-hidden) err = %v, want errNotFound", err)
		}
		vis, err := q.db().TxVisibility(q.ctx())
		if err != nil {
			return err
		}
		if vis.ProjectIds != "desiign" || vis.Person != "member@trial.test" || vis.Deleted != "" {
			t.Fatalf("scope/deleted leaked after taskLoad 404: %+v", vis)
		}
		if _, err := q.db().TaskGet(q.ctx(), "t-tomb"); err == nil {
			t.Fatal("tombstoned task became visible after taskLoad 404")
		}
		return nil
	}); err != nil {
		t.Fatalf("read transaction failed: %v", err)
	}
}
