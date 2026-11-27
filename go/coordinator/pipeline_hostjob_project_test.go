package main

import (
	"encoding/json"
	"testing"

	"fleet/contract"
)

// Requirements: script jobs belong to the run's project and dispatch its normalized data class.
// Risk: sessionless hosts misclassify personal staging or refuse to pass a script's artifacts to the next step.
func TestPipelineScriptHostJobKeepsRunProject(t *testing.T) {
	e := openTestCoordinator(t, false)
	const project, host = "hello-world", "script-fixture"
	headers := map[string]string{"Authorization": "Bearer " + e.registerHost(t, host)}
	for _, tc := range []struct {
		name, class string
		snapshot    json.RawMessage
		reserved    bool
	}{
		{"default", "internal", nil, false},
		{"personal", "personal", json.RawMessage(`{"data":"public","settings":{"data":"personal","repos":[{"id":"fixture"}]}}`), false},
		{"legacy snapshot", "personal", json.RawMessage(`{"data":"personal"}`), true},
		{"public", "public", json.RawMessage(`{"settings":{"data":"public"}}`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := &runRecord{ID: "hostjob-project-run-" + tc.name, ProjectID: new(project), ProjectSnapshot: tc.snapshot}
			var id, commandID string
			if err := e.app.store.Write(e.ctx, nil, nil, func(q *Request) error {
				var err error
				id, err = run.startScript(q, run.ID+":step:0", "fixture-route", nil, host, nil)
				if err != nil {
					return err
				}
				cmd, err := q.db().PipelineScriptCommand(q.ctx(), &id)
				if err != nil {
					return err
				}
				commandID = cmd.ID
				row, err := q.db().ArtCommandOf(q.ctx(), commandID)
				if err != nil {
					return err
				}
				var payload contract.ScriptPayload
				if err := json.Unmarshal(row.Payload, &payload); err != nil {
					return err
				}
				if got := rawObject(payload.Project).text("data"); got != tc.class || len(payload.Project) != 1 {
					t.Fatalf("script project data = %q, settings count = %d", got, len(payload.Project))
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			info, err := execGet(e.ctx, e.app, id)
			if err != nil {
				t.Fatal(err)
			}
			if deref(info.ProjectID) != project {
				t.Fatalf("host job project %q, want %q", deref(info.ProjectID), project)
			}

			// A permanent failure before artifact reservation must still appear
			// under the personal-output rule once this run reaches its end.
			for _, status := range []contract.ReceiptStatus{contract.ReceiptStatusStarted, contract.ReceiptStatusCompleted} {
				r := contract.Receipt{HostId: host, CommandId: commandID, ExecutionId: id, Status: status, Artifacts: []string{}}
				if code, body := e.callJSON(t, "POST", contract.RouteReceipts.Path, r, headers); code != 200 {
					t.Fatalf("receipt %s: HTTP %d %v", status, code, body)
				}
			}
			r := contract.ArtifactFailedRequest{HostId: host, ExecutionId: &id, ArtifactId: newID(), Name: new("fixture.txt"), Error: "invented transfer failure"}
			if tc.reserved {
				// A failed artifact row is the evidence after a successful reservation.
				if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO artifact
					(id, project_id, scope, content_hash, size, media_type, name, execution_id, state)
					VALUES ($1, $2, 'prod', repeat('a', 64), 10, 'text/plain', 'fixture.txt', $3, 'failed')`,
					r.ArtifactId, project, id); err != nil {
					t.Fatal(err)
				}
			} else if code, body := e.callJSON(t, "POST", contract.RouteArtifactsFailed.Path, r, headers); code != 200 {
				t.Fatalf("transfer failure: HTTP %d %v", code, body)
			}
		})
	}
	if err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
		rows, err := q.db().RulesPersonalScriptFailures(q.ctx())
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ProjectID != project || rows[0].N != 2 {
			t.Fatalf("personal script failure rows: %+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
