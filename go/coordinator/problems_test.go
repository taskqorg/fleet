package main

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// Requirement: an anomaly is a problem row, never a question (docs/designs/fleet-system-management.md#problems-anomalies-
// with-a-bot-owner): one open row per cause, a repeat counts, a cleared cause closes, a cause back within a day reopens
// its row, and the bot whose duty names the problem owns it. Risk: anomalies reach Needs you as investigate/dismiss
// questions nobody answers (34 on prod, 2026-11-26), or a returning cause loses its owner's work.
func TestProblemLifecycle(t *testing.T) {
	sockDir, port, _ := ensureTestPostgres(t)
	name := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	cloneTestDB(t, name)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", name, sockDir, port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	for _, sql := range []string{
		"SET LOCAL fleet.project_ids = '*'",
		`INSERT INTO bot(id, project_id, director, revision, memory_revision, paused, avatar_glow) VALUES ('hal', 'fleet', 'owner@x', 1, 0, false, '')`,
		`INSERT INTO bot_revision(bot_id, revision, name, purpose, profile_id, instructions, soul, schedule_time, schedule_zone, wake_on_needs_you, created_at, duty)
		 VALUES ('hal', 1, 'HAL0', '', 'pm', '', '', '', '', false, now(), '{"problems":{"system":true,"projects":["fleet"]}}')`,
	} {
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	q := &Request{tx: tx}
	get := func(cause string) db.Problem {
		t.Helper()
		p, err := q.db().ProblemOpenGet(ctx, cause)
		if err != nil {
			t.Fatalf("open problem %s: %v", cause, err)
		}
		return p
	}

	id, err := problemRaiseID(q, "direct", Problem{Cause: "problem:bugsink:1", Text: "A new error"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := problemRaiseID(q, "direct", Problem{Cause: "problem:bugsink:1", Text: "A new error"}); err != nil {
		t.Fatal(err)
	}
	p := get("problem:bugsink:1")
	if p.ID != id || p.Count != 2 || p.OwnerBotID == nil || *p.OwnerBotID != "hal" || p.Phase != "new" {
		t.Fatalf("raised twice: %+v", p)
	}
	var questions int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM question").Scan(&questions); err != nil || questions != 0 {
		t.Fatalf("a problem asked %d questions (%v)", questions, err)
	}

	if err := q.db().ProblemUpdate(ctx, db.ProblemUpdateParams{ID: id, Phase: ptr("working"), Work: ptr("T-FLT-9")}); err != nil {
		t.Fatal(err)
	}
	if err := problemClose(q, "problem:bugsink:1"); err != nil {
		t.Fatal(err)
	}
	if _, err := q.db().ProblemOpenGet(ctx, "problem:bugsink:1"); err == nil {
		t.Fatal("closed problem still open")
	}
	again, err := problemRaiseID(q, "direct", Problem{Cause: "problem:bugsink:1", Text: "It regressed"})
	if err != nil {
		t.Fatal(err)
	}
	p = get("problem:bugsink:1")
	if again != id || p.Reopened != 1 || p.Work != "T-FLT-9" || p.Phase != "new" || p.Text != "It regressed" {
		t.Fatalf("reopened: %+v", p)
	}

	// A sync source: a cause absent for problemCleanRuns passes closes.
	sync := []Problem{{Cause: "problem:delivery:ui:p50", Text: "Delivery got slower", ProjectID: "fleet"}}
	if err := problemsSync(q, "delivery", sync); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < problemCleanRuns; i++ {
		if err := problemsSync(q, "delivery", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := q.db().ProblemOpenGet(ctx, "problem:delivery:ui:p50"); err == nil {
		t.Fatal("a cause absent for the clean runs stayed open")
	}

	// A set source: an absent cause closes at once.
	if err := problemsSet(q, "", "^host:", []Problem{{Cause: "host:windows:offline", Text: "Work is waiting"}}); err != nil {
		t.Fatal(err)
	}
	get("host:windows:offline")
	if err := problemsSet(q, "", "^host:", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.db().ProblemOpenGet(ctx, "host:windows:offline"); err == nil {
		t.Fatal("an absent set cause stayed open")
	}

	// The watchdog: HAL0 owns a new problem an hour old and has not run since: one problem without an owner and one
	// only-you question; a run of HAL0 closes both.
	if _, err := tx.Exec(ctx, "UPDATE problem SET opened_at = now() - interval '1 hour' WHERE cause = 'problem:bugsink:1'"); err != nil {
		t.Fatal(err)
	}
	if err := problemsWatch(q); err != nil {
		t.Fatal(err)
	}
	down := get("duty:hal:down")
	if down.OwnerBotID != nil || down.QuestionID == nil || down.Phase != "escalated" {
		t.Fatalf("duty down: %+v", down)
	}
	if err := problemsWatch(q); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM question WHERE state = 'open'").Scan(&questions); err != nil || questions != 1 {
		t.Fatalf("watchdog asked %d open questions, want 1 (%v)", questions, err)
	}
	if _, err := tx.Exec(ctx, "INSERT INTO execution(id, kind, state, bot_id, started_at) VALUES ('hal-run', 'agent_run', 'running', 'hal', now())"); err != nil {
		t.Fatal(err)
	}
	if err := problemsWatch(q); err != nil {
		t.Fatal(err)
	}
	if _, err := q.db().ProblemOpenGet(ctx, "duty:hal:down"); err == nil {
		t.Fatal("duty down stayed open after the bot ran")
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM question WHERE state = 'open'").Scan(&questions); err != nil || questions != 0 {
		t.Fatalf("watchdog question still open: %d (%v)", questions, err)
	}
}
