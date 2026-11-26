package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

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

	// A source with its own tally (agent gaps): the count is the source's, not one per raise; seenAt is its last one.
	last := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	for range 3 {
		if _, err := problemRaiseID(q, "agent-gaps", Problem{Cause: "gap:sandbox-write", Text: "Sandbox blocks writes", Count: 626, SeenAt: &last}); err != nil {
			t.Fatal(err)
		}
	}
	g := get("gap:sandbox-write")
	if g.Count != 626 || !g.SeenAt.Equal(last) {
		t.Fatalf("tally: count %d seen %v, want 626 at %v", g.Count, g.SeenAt, last)
	}
	// The owner never started it: it is unstarted until the owner has its problem conversation.
	ids, err := q.db().ProblemsUnstarted(ctx, "hal")
	if err != nil || !slices.Contains(ids, g.ID) {
		t.Fatalf("unstarted %v (%v), want %s", ids, err, g.ID)
	}

	// The run-rate breaker: 25 runs in the last hour against no usual pauses the bot and raises one problem without an
	// owner; after the cool time the bot resumes on probation; a person's question is never asked.
	if _, err := tx.Exec(ctx, "UPDATE execution SET started_at = now() - interval '2 days' WHERE bot_id = 'hal'"); err != nil {
		t.Fatal(err)
	}
	for i := range 25 {
		if _, err := tx.Exec(ctx, "INSERT INTO execution(id, kind, state, bot_id, started_at) VALUES ($1, 'agent_run', 'succeeded', 'hal', now() - interval '10 minutes')", fmt.Sprintf("burst-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	if err := botBreaker(q, now); err != nil {
		t.Fatal(err)
	}
	var paused bool
	if err := tx.QueryRow(ctx, "SELECT paused FROM bot WHERE id = 'hal'").Scan(&paused); err != nil || !paused {
		t.Fatalf("bot paused %v (%v), want paused", paused, err)
	}
	burst := get("burst:hal")
	if burst.OwnerBotID != nil || burst.QuestionID != nil {
		t.Fatalf("burst problem: %+v", burst)
	}
	if err := botBreaker(q, now.Add(31*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, "SELECT paused FROM bot WHERE id = 'hal'").Scan(&paused); err != nil || paused {
		t.Fatalf("bot paused %v after the cool time (%v), want resumed on probation", paused, err)
	}
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM question WHERE state = 'open'").Scan(&questions); err != nil || questions != 0 {
		t.Fatalf("the breaker asked %d questions (%v)", questions, err)
	}
	// On probation only runs since the resume count: the burst it paused for does not pause it again.
	if err := botBreaker(q, now.Add(32*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, "SELECT paused FROM bot WHERE id = 'hal'").Scan(&paused); err != nil || paused {
		t.Fatalf("bot paused again on probation by the old burst (%v)", err)
	}

	// Whatever closes a problem withdraws the question it asked: a stale "HAL0 is not working" never stays in Needs you.
	esc, err := problemRaiseID(q, "direct", Problem{Cause: "problem:asked", Text: "Needs the owner"})
	if err != nil {
		t.Fatal(err)
	}
	fleet := "fleet"
	qid, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", Text: "Only you can do this", AskedBy: &fleet})
	if err != nil {
		t.Fatal(err)
	}
	if err := q.db().ProblemUpdate(ctx, db.ProblemUpdateParams{ID: esc, Phase: ptr("escalated"), QuestionID: &qid}); err != nil {
		t.Fatal(err)
	}
	if err := problemClose(q, "problem:asked"); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := tx.QueryRow(ctx, "SELECT state::text FROM question WHERE id = $1", qid).Scan(&state); err != nil || state != "withdrawn" {
		t.Fatalf("question of a closed problem: %s (%v), want withdrawn", state, err)
	}
}

// Requirement: a machine identity's change never asks a holder to acknowledge it (OR-71); site-agents' row has no
// Access common name, only a name that is not an e-mail (live 2026-11-26: Q-ALL-1437..1439 from hosts_update).
func TestMachineIdentity(t *testing.T) {
	for _, c := range []struct {
		p    Person
		want bool
	}{{Person{Email: "site-agents"}, true}, {Person{Email: "x@y.z", AccessCommonName: "abc"}, true}, {Person{Email: "x@y.z"}, false}} {
		if got := c.p.machine(); got != c.want {
			t.Errorf("%+v: machine %v, want %v", c.p, got, c.want)
		}
	}
}
