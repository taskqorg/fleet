package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

func TestBotTasksPagePlan(t *testing.T) {
	e := openTestCoordinator(t, false)
	if _, err := e.ownerStore.pool.Exec(e.ctx, `
		INSERT INTO bot (id, project_id, director, revision) VALUES ('bp-bot', 'a', 'owner@example.com', 1);
		INSERT INTO bot_revision (bot_id, revision, name, purpose, profile_id, instructions, soul)
		 VALUES ('bp-bot', 1, 'Bot', '', 'test', '', '');
		INSERT INTO task (id, code, project_id, title, state, pause_reason) VALUES
		 ('bp-older', 'T-BP-1', 'a', 'Older', 'paused', 'held'),
		 ('bp-newer', 'T-BP-2', 'a', 'Newer', 'paused', 'held'),
		 ('bp-hidden', 'T-BP-3', 'b', 'Hidden', 'paused', 'held');
		INSERT INTO bot_task (bot_id, task_id, project_id, seq) VALUES
		 ('bp-bot', 'bp-older', 'a', 100), ('bp-bot', 'bp-newer', 'a', 100), ('bp-bot', 'bp-hidden', 'b', 101);
		INSERT INTO execution (id, kind, state, task_id, project_id, tombstoned_at) VALUES
		 ('bp-run-1', 'agent_run', 'succeeded', 'bp-older', 'a', NULL),
		 ('bp-run-2', 'agent_run', 'failed', 'bp-older', 'a', NULL),
		 ('bp-run-3', 'agent_run', 'failed', 'bp-older', 'b', NULL),
		 ('bp-run-4', 'agent_run', 'failed', 'bp-older', 'a', now());
		INSERT INTO execution (id, kind, state, task_id, project_id)
		 SELECT 'bp-tool-' || i, 'tool_run', 'succeeded', 'bp-older', 'a' FROM generate_series(1, 1000) i;
		INSERT INTO execution (id, kind, state, project_id, started_at, ended_at)
		 SELECT 'bp-unrelated-' || i, 'agent_run', 'succeeded', 'a', now()-interval '2 days', now()-interval '2 days'
		 FROM generate_series(1, 1000) i;
		INSERT INTO question (id, kind, project_id, task_id, text) VALUES
		 ('bp-q1', 'operator_decision', 'a', 'bp-older', 'Visible'),
		 ('bp-q2', 'operator_decision', 'b', 'bp-older', 'Hidden');
		ANALYZE execution;
	`); err != nil {
		t.Fatal(err)
	}
	p := &Person{Email: "operator@example.com", Grants: []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{{Kind: "project", ID: "a"}}}
	request := scopedRequest(e.ctx, e.app, p)
	err := e.appStore.Read(e.ctx, request, func(q *Request) error {
		rec := &recordingTx{Tx: q.tx, by: map[string]*recordedStatement{}}
		sub := *q
		sub.tx = rec
		first, err := botTasksOp(&sub, &contract.BotTasksRequest{BotId: "bp-bot", Limit: ptr(int64(1)), State: ptr("held")})
		if err != nil {
			t.Fatal(err)
		}
		if len(first.Tasks) != 1 || first.Tasks[0].TaskId != "bp-older" || !first.HasMore || first.NextAfter == nil {
			t.Fatalf("first page: %+v", first)
		}
		item := first.Tasks[0]
		if item.ExecutionCount != 2 || item.ExecutionsByState["failed"] != 1 || item.ExecutionsByState["succeeded"] != 1 || item.OpenQuestions != 1 {
			t.Fatalf("visible agent runs and questions: %+v", item)
		}
		next, err := botTasksOp(&sub, &contract.BotTasksRequest{BotId: "bp-bot", Limit: ptr(int64(1)), After: first.NextAfter, State: ptr("held")})
		if err != nil || len(next.Tasks) != 1 || next.Tasks[0].TaskId != "bp-newer" || next.Tasks[0].ExecutionCount != 0 || next.HasMore {
			t.Fatalf("second page: %+v, %v", next, err)
		}
		if len(rec.order) != 2 {
			t.Fatalf("expected a bot check and page query, got %d statements", len(rec.order))
		}
		s := rec.by[rec.order[1]]
		plan, err := explainOne(q, s.sql, s.args)
		if err != nil {
			return err
		}
		if !strings.Contains(plan, "CTE page") || strings.Count(plan, " on execution ") != 1 ||
			strings.Contains(plan, "Seq Scan on execution") || strings.Contains(plan, "temp written") {
			t.Fatalf("bot task page must probe live agent runs once, after paging:\n%s", plan)
		}
		t.Log(plan[strings.LastIndex(plan, "Execution Time:"):])
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
