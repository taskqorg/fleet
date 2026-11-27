package main

import (
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: home_view (pages/home.md, docs/designs/fleet-data-layer.md) returns rows only for Home's cards and counts
// the rest on the server: done today and since the last visit, failed runs that need a look by cause and host, problem
// questions as blockers. Risk: a finished session joins the cards, a failure loses its cause, or a count drifts from
// what the old client counted from 200 finished rows.
func TestHomeView(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC()
	visit := now.Add(-2 * time.Hour)
	exec(`INSERT INTO person_visit (email, page, visited_at) VALUES ($1, 'home', $2)`, testOwnerEmail, visit)
	for _, s := range []struct {
		id    string
		state string
		at    time.Time
	}{
		{"hv-run", "running", now.Add(-time.Minute)},
		{"hv-fail", "failed", now.Add(-10 * time.Minute)},
		{"hv-done-new", "succeeded", now.Add(-time.Hour)},
		{"hv-done-old", "succeeded", now.Add(-3 * time.Hour)},
		{"hv-done-past", "succeeded", now.Add(-72 * time.Hour)},
	} {
		exec(`INSERT INTO session (id, created_at, updated_at) VALUES ($1, $2, $2)`, s.id, s.at)
		exec(`INSERT INTO execution (id, kind, state, session_id, harness, started_at, ended_at, data, observation, config_ref, problem)
		 VALUES ($1, 'agent_run', $2::execution_state, $3, 'codex', $4, CASE WHEN $2::text = 'running' THEN NULL ELSE $4::timestamptz END, '{}', 'confirmed', '{}', CASE WHEN $2::text = 'failed' THEN 'step:provider-429' END)`,
			s.id+"-r", s.state, s.id, s.at)
	}
	// A failed run that needs a look on a host, and a second one of the same cause there: one blocker of two.
	for i, id := range []string{"hv-p1", "hv-p2"} {
		exec(`INSERT INTO execution (id, kind, state, host_id, started_at, last_seen_at, data, observation, config_ref, problem)
		 VALUES ($1, 'agent_run', 'failed', 'mac', $2, $2, '{}', 'confirmed', '{}', 'infra:http-503')`, id, now.Add(-time.Duration(i+1)*time.Minute))
	}
	exec(`INSERT INTO question (id, kind, session_id, execution_id, text, asked_at) VALUES ('hv-q', 'agent_question', 'hv-run', 'hv-run-r', 'Merge it?', $1)`, now.Add(-5*time.Minute))
	exec(`INSERT INTO problem (id, cause, text) VALUES ('hv-prob', 'problem:host:mac:saturated:1', 'Host mac is saturated.')`)

	// Two pull requests merged, one since the visit.
	deliveryMu.Lock()
	saved := deliveryLast
	deliveryLast = deliveryState{source: contract.DeliverySourceKey, repos: map[string]*deliveryRepo{
		"o/a": {projects: []string{"fleet"}, items: []contract.DeliveryItem{
			{ProjectId: "fleet", Repo: "o/a", Number: 1, Merged: ptr(timeText(now.Add(-time.Hour)))},
			{ProjectId: "fleet", Repo: "o/a", Number: 2, Merged: ptr(timeText(now.Add(-3 * time.Hour)))},
		}},
	}}
	deliveryMu.Unlock()
	t.Cleanup(func() { deliveryMu.Lock(); deliveryLast = saved; deliveryMu.Unlock() })

	var got contract.HomeViewReply
	e.opInto(t, "home_view", map[string]any{"dayStart": now.Add(-12 * time.Hour).UnixMilli()}, e.ownerHeaders(), &got)

	ids := map[string]string{}
	for _, s := range got.Sessions {
		ids[s.Id] = s.State
	}
	if ids["hv-run"] != "running" || ids["hv-fail"] != "failed" || len(ids) != 2 {
		t.Fatalf("cards: the running and the failed session only: %v", ids)
	}
	if len(got.Failures) != 1 || got.Failures[0].SessionId != "hv-fail" || got.Failures[0].Cause != "provider-limit" {
		t.Fatalf("the failed session's cause: %+v", got.Failures)
	}
	if got.Counts.DoneToday != 2 || got.Counts.DoneSince != 1 || got.Counts.VisitAt == nil || *got.Counts.VisitAt != visit.UnixMilli() {
		t.Fatalf("counts: done today 2 and 1 since the visit: %+v", got.Counts)
	}
	// The test store has no project definitions, so the merged count is checked for an allowed project directly.
	if n, other := homeMerged(map[string]bool{"fleet": true}, visit), homeMerged(map[string]bool{"cs": true}, visit); n != 1 || other != 0 {
		t.Fatalf("merged since the visit: %d (want 1), other project %d (want 0)", n, other)
	}
	// The failed session's run needs a look too.
	if got.Counts.FailedSince != 3 {
		t.Fatalf("failed runs since the visit: %d, want 3", got.Counts.FailedSince)
	}
	if len(got.Questions) != 1 || got.Questions[0].Id != "hv-q" {
		t.Fatalf("questions: the agent question only: %+v", got.Questions)
	}
	var run *contract.HomeBlocker
	for i, b := range got.Blockers {
		if b.Kind == contract.HomeBlockerKindRun && deref(b.HostId) == "mac" {
			run = &got.Blockers[i]
		}
		if b.Kind == contract.HomeBlockerKindProblem && (b.Cause != "problem:host:mac:saturated" || deref(b.HostId) != "mac") {
			t.Fatalf("problem blocker by host and kind: %+v", b)
		}
	}
	if run == nil || run.N != 2 || run.Cause != "provider-down" || len(got.Blockers) != 3 {
		t.Fatalf("run blocker: two provider-down failures on mac: %+v", got.Blockers)
	}
	// A problem is the pulse line's count, owned by no bot here; never a question.
	if got.Problems == nil || got.Problems.Open != 1 || got.Problems.Unowned != 1 || len(got.Problems.Owners) != 0 {
		t.Fatalf("problems: one open without an owner: %+v", got.Problems)
	}

	// Scoped to a project no row is in: no cards, no counts.
	var scoped contract.HomeViewReply
	e.opInto(t, "home_view", map[string]any{"projectIds": []string{"elsewhere"}}, e.ownerHeaders(), &scoped)
	if len(scoped.Sessions) != 0 || scoped.Counts.DoneToday != 0 || len(scoped.Blockers) != 0 || len(scoped.Questions) != 0 {
		t.Fatalf("scope: %+v", scoped)
	}
}

// Requirement: Home (pages/home.md) hides raw host/infra execution failures and HAL0/bot-owned problems and runs from
// Home FAILED and operator blockers, surfacing only human product questions, escalated/unowned problems, and
// operator-initiated product failures.
func TestHomeViewHidesHal0AndInfraNoise(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC()
	botID := "hal0-test-bot"
	exec(`INSERT INTO bot (id, project_id, director, revision) VALUES ($1, 'fleet', $2, 1)`, botID, testOwnerEmail)
	exec(`INSERT INTO bot_revision (bot_id, revision, name, purpose, profile_id, instructions, soul, schedule_time, schedule_zone, wake_on_needs_you) VALUES ($1, 1, 'HAL0', 'Ops', 'hal0', '', '', '', 'UTC', true)`, botID)

	// 1. Bot-owned task & session failing with a product cause: hidden from FAILED, Failures, Blockers, FailedSince.
	exec(`INSERT INTO task (id, project_id, title, state) VALUES ('t-bot', 'fleet', 'Bot task', 'in_progress')`)
	exec(`INSERT INTO bot_task (bot_id, task_id, project_id, link_kind) VALUES ($1, 't-bot', 'fleet', 'created')`, botID)
	exec(`INSERT INTO session (id, project_id, task_id, created_at, updated_at) VALUES ('s-bot-task', 'fleet', 't-bot', $1, $1)`, now.Add(-2*time.Minute))
	exec(`INSERT INTO execution (id, kind, state, session_id, task_id, host_id, harness, started_at, ended_at, data, observation, config_ref, problem)
	 VALUES ('e-bot-task', 'agent_run', 'failed', 's-bot-task', 't-bot', 'mac', 'codex', $1, $1, '{}', 'confirmed', '{}', 'step:provider-429')`, now.Add(-2*time.Minute))

	// 2. Operator session failing with raw host/infra cause (preparation / unreported / host-not-ready): hidden.
	for i, tc := range []struct {
		sid     string
		problem string
	}{
		{"s-infra-prep", "preparation failed: readiness-check-failed"},
		{"s-infra-unrep", "unknown:unreported"},
	} {
		at := now.Add(-time.Duration(i+3) * time.Minute)
		exec(`INSERT INTO session (id, project_id, directed_by, created_at, updated_at) VALUES ($1, 'fleet', $2, $3, $3)`, tc.sid, testOwnerEmail, at)
		exec(`INSERT INTO execution (id, kind, state, session_id, host_id, harness, started_at, ended_at, data, observation, config_ref, problem)
		 VALUES ($1, 'agent_run', 'failed', $2, 'mac', 'codex', $3, $3, '{}', 'confirmed', '{}', $4)`, tc.sid+"-r", tc.sid, at, tc.problem)
	}

	// 3. Raw host_job unreported failure: hidden from Blockers and FailedSince.
	exec(`INSERT INTO execution (id, kind, state, host_id, started_at, ended_at, data, observation, config_ref, problem)
	 VALUES ('e-host-job', 'host_job', 'failed', 'mac', $1, $1, '{}', 'confirmed', '{}', 'unknown:unreported')`, now.Add(-time.Minute))

	// 4. Operator-initiated product failure: stays visible in Sessions, Failures, Blockers, FailedSince.
	exec(`INSERT INTO session (id, project_id, directed_by, created_at, updated_at) VALUES ('s-op-prod', 'fleet', $1, $2, $2)`, testOwnerEmail, now.Add(-time.Minute))
	exec(`INSERT INTO execution (id, kind, state, session_id, host_id, harness, started_at, ended_at, data, observation, config_ref, problem)
	 VALUES ('e-op-prod', 'agent_run', 'failed', 's-op-prod', 'mac', 'codex', $1, $1, '{}', 'confirmed', '{}', 'turn.failed: bad query')`, now.Add(-time.Minute))

	// 5. Problems: HAL0-owned stuck agent-gap (hidden), HAL0-owned escalated problem (visible), unowned problem (visible).
	exec(`INSERT INTO problem (id, cause, source, text, owner_bot_id, phase) VALUES ('p-gap', 'gap:fleet-refused:task_page', 'agent-gaps', 'Agents lack task_page', $1, 'stuck')`, botID)
	exec(`INSERT INTO problem (id, cause, source, text, owner_bot_id, phase) VALUES ('p-esc', 'problem:delivery:slow', 'delivery', 'Delivery escalated', $1, 'escalated')`, botID)
	exec(`INSERT INTO problem (id, cause, source, text, owner_bot_id, phase) VALUES ('p-unowned', 'burst:hal0', 'breaker', 'HAL0 burst', NULL, 'new')`)

	var got contract.HomeViewReply
	e.opInto(t, "home_view", map[string]any{"dayStart": now.Add(-12 * time.Hour).UnixMilli()}, e.ownerHeaders(), &got)

	if len(got.Sessions) != 1 || got.Sessions[0].Id != "s-op-prod" {
		t.Fatalf("sessions: want only s-op-prod, got %+v", got.Sessions)
	}
	if len(got.Failures) != 1 || got.Failures[0].SessionId != "s-op-prod" || got.Failures[0].Cause != "agent-turn" {
		t.Fatalf("failures: want only s-op-prod agent-turn, got %+v", got.Failures)
	}
	if got.Counts.FailedSince != 1 {
		t.Fatalf("failedSince: want 1, got %d", got.Counts.FailedSince)
	}
	if got.Problems == nil || got.Problems.Open != 2 || got.Problems.Unowned != 1 || got.Problems.Stuck != 0 {
		t.Fatalf("problems: want 2 open (escalated + unowned), 1 unowned, 0 stuck, got %+v", got.Problems)
	}
	causes := map[string]bool{}
	for _, b := range got.Blockers {
		causes[b.Cause] = true
	}
	if !causes["agent-turn"] || !causes["problem:delivery"] || !causes["problem:burst"] || causes["problem:gap"] || causes["preparation"] || causes["unreported"] {
		t.Fatalf("blockers: unexpected causes %v", causes)
	}
}
