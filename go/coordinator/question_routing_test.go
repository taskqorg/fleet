package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: when a worker execution on a bot-managed task calls question_ask instead of ask, the coordinator binds
// its task, session, execution and agent asker so addressToManagingBot routes the question to that managing bot
// rather than surfacing an unaddressed operator_decision in Home WAITING FOR YOU.
func TestAgentQuestionAskRoutesToManagingBot(t *testing.T) {
	e := openTestCoordinator(t, false)
	var created map[string]any
	e.opInto(t, "project_create", map[string]any{"requestId": "qr-proj-1", "id": "fleet", "content": map[string]any{
		"settings": map[string]any{"id": "fleet", "name": "Fleet", "contextFiles": []any{"rules.md"}},
		"contextFiles": []any{map[string]any{"path": "rules.md", "text": "Rules."}}}}, e.ownerHeaders(), &created)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	botID := "hal0-routing-bot"
	exec(`INSERT INTO bot (id, project_id, director, revision) VALUES ($1, 'fleet', $2, 1)`, botID, testOwnerEmail)
	exec(`INSERT INTO bot_revision (bot_id, revision, name, purpose, profile_id, instructions, soul, schedule_time, schedule_zone, wake_on_needs_you) VALUES ($1, 1, 'HAL0', 'Ops', 'hal0', '', '', '', 'UTC', true)`, botID)
	exec(`INSERT INTO task (id, project_id, title, state) VALUES ('t-managed', 'fleet', 'Managed worker task', 'in_progress')`)
	exec(`INSERT INTO bot_task (bot_id, task_id, project_id, link_kind) VALUES ($1, 't-managed', 'fleet', 'created')`, botID)
	exec(`INSERT INTO session (id, project_id, task_id, directed_by) VALUES ('s-managed-worker', 'fleet', 't-managed', $1)`, testOwnerEmail)
	exec(`INSERT INTO execution (id, kind, state, session_id, task_id, harness, data, observation, config_ref) VALUES ('e-managed-worker', 'agent_run', 'running', 's-managed-worker', 't-managed', 'codex', '{}', 'confirmed', '{}')`)

	var reply contract.QuestionAskReply
	if err := e.app.store.Write(e.ctx, nil, nil, func(q *Request) error {
		q.person = &Person{Email: testOwnerEmail, Owner: true}
		q.agent = "execution:e-managed-worker"
		bound, err := agentBoundArguments(q, "question_ask", nil, "e-managed-worker")
		if err != nil {
			return err
		}
		if string(bound["projectId"]) != `"fleet"` {
			t.Fatalf("bound projectId = %s, want \"fleet\"", bound["projectId"])
		}
		out, err := e.app.runOp(q, "question_ask", jsonBytes(map[string]any{
			"projectId": "fleet",
			"requestId": "req-route-review-1",
			"text":      "Need independent code review on T-FLT-111",
		}), nil)
		if err != nil {
			return err
		}
		reply = out.(contract.QuestionAskReply)
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	var taskID, sessionID, executionID, askedBy, addressedTo string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT coalesce(task_id,''), coalesce(session_id,''), coalesce(execution_id,''), coalesce(asked_by,''), coalesce(addressed_to,'') FROM question WHERE id=$1`, reply.Id).Scan(&taskID, &sessionID, &executionID, &askedBy, &addressedTo); err != nil {
		t.Fatal(err)
	}
	if taskID != "t-managed" || sessionID != "s-managed-worker" || executionID != "e-managed-worker" || askedBy != "execution:e-managed-worker" || addressedTo != "bot:"+botID {
		t.Fatalf("question routing = (%q, %q, %q, %q, %q), want managed bot routing", taskID, sessionID, executionID, askedBy, addressedTo)
	}

	var home contract.HomeViewReply
	e.opInto(t, "home_view", map[string]any{}, e.ownerHeaders(), &home)
	for _, q := range home.Questions {
		if q.Id == reply.Id {
			t.Fatalf("managed bot question %s leaked into home_view.questions", reply.Id)
		}
	}
}
