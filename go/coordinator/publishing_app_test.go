package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/projectpublisher"
)

// A normal dispatch writes its project into the execution and start payload, not the command column. The
// App route must authorize the exact keyed workspace and reject legacy login readiness before contacting GitHub.
func TestFleetAppPublishingStartAuthority(t *testing.T) {
	env := openTestCoordinator(t, false)
	const host = projectpublisher.Executor
	var project projectWriteResult
	env.opInto(t, "project_create", doc{"requestId": "app-publishing-project", "id": "fleet", "content": doc{
		"settings": doc{"id": "fleet", "name": "Fleet", "contextFiles": []string{"rules.md"},
			"execution":   doc{"forge": true, "profiles": []string{"coding-worker"}},
			"repos":       []doc{{"id": "main", "remote": "https://github.com/" + projectpublisher.Repo}},
			"integration": doc{"mode": "pr", "autoMerge": true}},
		"contextFiles": []doc{{"path": "rules.md", "text": "Invented publishing fixture."}},
	}}, env.ownerHeaders(), &project)
	var task contract.TaskDetail
	env.opInto(t, "task_create", doc{"title": "Publish an invented nonvisual fix", "projectId": "fleet"}, env.ownerHeaders(), &task)
	env.opInto(t, "task_update", doc{"taskId": task.Id, "visualImpact": "nonvisual"}, env.ownerHeaders(), &task)
	session, source, cmd := newID(), newID(), newID()
	now := time.Now()
	write := func(fn func(*Request) error) {
		t.Helper()
		if err := env.app.store.Write(env.ctx, nil, []string{GLOBAL}, fn); err != nil {
			t.Fatal(err)
		}
	}
	observe := func(q *Request, cap string) error {
		return q.db().HostObservationPut(q.ctx(), db.HostObservationPutParams{ID: host, Holder: ptr(testOwnerEmail),
			LastSeenAt: &now, Payload: jsonBytes(hostPollObservationPayload{Capabilities: &[]string{cap}})})
	}
	const key = "invented-transition-workspace"
	write(func(q *Request) error {
		if err := observe(q, projectpublisher.Capability); err != nil {
			return err
		}
		profile := jsonBytes(agentProfile{ID: "coding-worker", Harness: "codex"})
		if err := sessionPut(q, &agentSession{ID: session, ProjectID: "fleet", TaskID: task.Id, Access: "write", Branch: ptr(true), HostID: host,
			DirectedBy: testOwnerEmail, AgentConfig: profile}); err != nil {
			return err
		}
		if err := executionWrite(q, &agentExecution{ID: source, SessionID: session, ProjectID: "fleet", TaskID: task.Id, HostID: ptr(host),
			State: "running", CreatedAt: now.UnixMilli(), DirectedBy: testOwnerEmail, AgentConfig: profile,
			Dispatch: &agentDispatch{ProjectID: ptr("fleet"), WorkspaceKey: ptr(key)}}); err != nil {
			return err
		}
		return commandPut(q, &agentCommand{ID: cmd, ExecutionID: source, SessionID: session, HostID: host, Kind: "start", Status: "accepted",
			Payload: jsonBytes(contract.StartPayload{ProjectId: ptr("fleet"), WorkspaceKey: ptr(key), Branch: ptr(true),
				Project: rawObject{"execution": jsonBytes(contract.ProjectExecution{Forge: ptr(true)})}})})
	})
	q := &Request{app: env.app, host: &authHost{HostID: host, Holder: testOwnerEmail}}
	b := contract.PublishingAppRequest{HostId: host, ExecutionId: source, CommandId: cmd, AgentToolKey: "invented-request",
		Action: "publish_draft", Title: ptr("fixture"), Body: ptr(""), ExpectedHeadSha: ptr(strings.Repeat("a", 40))}
	hits := 0
	authorize := func(_ context.Context, op projectpublisher.Operation) (projectpublisher.Reply, error) {
		hits++
		if op.Branch != "fleet/"+contract.WorkspaceSession(session, key) || op.Base != "main" || op.Head != *b.ExpectedHeadSha {
			t.Fatalf("incorrect scoped operation: %+v", op)
		}
		return projectpublisher.Reply{Ticket: "invented-single-use-ticket"}, nil
	}
	got, err := publishApp(q, &b, authorize)
	if err != nil || deref(got.Ticket) != "invented-single-use-ticket" || hits != 1 {
		t.Fatalf("normal start command could not publish: %+v, %v", got, err)
	}
	// With forge removed from the dispatched start, only the branch-scoped read is eligible.
	write(func(q *Request) error {
		c, err := commandGet(q, cmd)
		if err != nil {
			return err
		}
		c.Payload = jsonBytes(contract.StartPayload{ProjectId: ptr("fleet"), WorkspaceKey: ptr(key), Branch: ptr(true), ReadOnly: ptr(true)})
		return commandPut(q, &c)
	})
	if _, err := publishApp(q, &b, authorize); err == nil || hits != 1 {
		t.Fatal("read-only start submitted a write")
	}
	b.Action = "status"
	if _, err := publishApp(q, &b, authorize); err != nil || hits != 2 {
		t.Fatalf("read-only status failed: %v", err)
	}

	write(func(q *Request) error {
		return observe(q, contract.PublishingHostCapability("github", "github.com", projectpublisher.Repo))
	})
	if _, err := publishApp(q, &b, authorize); err == nil || hits != 2 {
		t.Fatal("legacy GitHub capability reached App provider")
	}
}

func TestFleetAppPublishingOperatorVeto(t *testing.T) {
	env := openTestCoordinator(t, false)
	const host = projectpublisher.Executor
	var project projectWriteResult
	env.opInto(t, "project_create", doc{"requestId": "app-veto-project", "id": "fleet", "content": doc{
		"settings": doc{"id": "fleet", "name": "Fleet", "contextFiles": []string{"rules.md"}, "execution": doc{"forge": true},
			"repos": []doc{{"id": "main", "remote": "https://github.com/" + projectpublisher.Repo}}},
		"contextFiles": []doc{{"path": "rules.md", "text": "Invented publishing fixture."}},
	}}, env.ownerHeaders(), &project)
	var cmd, source string
	if err := env.app.store.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		now := time.Now()
		if err := q.db().HostObservationPut(q.ctx(), db.HostObservationPutParams{ID: host, Holder: ptr(testOwnerEmail),
			LastSeenAt: &now, Payload: jsonBytes(hostPollObservationPayload{Capabilities: &[]string{projectpublisher.Capability}})}); err != nil {
			return err
		}
		source, cmd = newID(), newID()
		if err := executionWrite(q, &agentExecution{ID: source, ProjectID: "fleet", HostID: ptr(host), DirectedBy: testOwnerEmail, State: "running", CreatedAt: now.UnixMilli()}); err != nil {
			return err
		}
		return commandPut(q, &agentCommand{ID: cmd, ExecutionID: source, ProjectID: "fleet", HostID: host, Kind: "publishing", Status: "accepted",
			Payload: jsonBytes(contract.PublishingCommandPayload{Action: "cancel_merge_request", ProjectId: "fleet", Repo: projectpublisher.Repo,
				Provider: "github", Host: projectpublisher.Host, BaseBranch: "main", Number: ptr(int64(42)), MergeMethod: "squash"})})
	}); err != nil {
		t.Fatal(err)
	}
	q := &Request{app: env.app, host: &authHost{HostID: host, Holder: testOwnerEmail}}
	b := contract.PublishingAppRequest{HostId: host, ExecutionId: source, CommandId: cmd, AgentToolKey: "invented-request", Action: "cancel_merge_request"}
	got, err := publishApp(q, &b, func(_ context.Context, op projectpublisher.Operation) (projectpublisher.Reply, error) {
		if op.Action != "cancel_merge_request" || op.Number != 42 || op.Branch != "" {
			t.Fatalf("unexpected veto: %+v", op)
		}
		return projectpublisher.Reply{Result: json.RawMessage(`{"cancelled":true}`)}, nil
	})
	if err != nil || string(got.Result) != `{"cancelled":true}` {
		t.Fatalf("operator veto needs no live worker: %+v, %v", got, err)
	}
}
