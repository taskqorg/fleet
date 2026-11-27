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
		class    string
		snapshot json.RawMessage
	}{
		{"internal", nil},
		{"personal", json.RawMessage(`{"data":"public","settings":{"data":"personal","repos":[{"id":"fixture"}]}}`)},
		{"public", json.RawMessage(`{"settings":{"data":"public"}}`)},
	} {
		t.Run(tc.class, func(t *testing.T) {
			run := &runRecord{ID: "hostjob-project-run-" + tc.class, ProjectID: new(project), ProjectSnapshot: tc.snapshot}
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
			if code, body := e.callJSON(t, "POST", contract.RouteArtifactsFailed.Path, r, headers); code != 200 {
				t.Fatalf("transfer failure: HTTP %d %v", code, body)
			}
		})
	}
	if err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
		rows, err := q.db().RulesPersonalScriptFailures(q.ctx())
		if err != nil {
			return err
		}
		if len(rows) != 1 || rows[0].ProjectID != project || rows[0].N != 1 {
			t.Fatalf("personal script failure rows: %+v", rows)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
