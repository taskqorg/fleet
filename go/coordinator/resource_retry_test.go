package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

func withResourceRetry(t *testing.T, base, maximum time.Duration) {
	t.Helper()
	b, m := resourceRetryBase, resourceRetryMax
	resourceRetryBase, resourceRetryMax = base, maximum
	t.Cleanup(func() { resourceRetryBase, resourceRetryMax = b, m })
}

// Requirement: a failing item pauses exponentially with jitter up to the maximum; risk: equal failures retry in
// step, or the pause grows without a ceiling.
func TestResourceRetryDelay(t *testing.T) {
	withResourceRetry(t, 100*time.Millisecond, 5*time.Minute)
	for n := 0; n < 80; n++ {
		step := min(5*time.Minute, 100*time.Millisecond<<min(n, 30))
		for range 20 {
			low := step
			if step == 5*time.Minute {
				low = min(step, 5*time.Minute*3/4) // at the maximum the jitter goes below it
			}
			if d := resourceRetryDelay(n); d < low || d > min(5*time.Minute, step*3/2) {
				t.Fatalf("delay(%d) = %v, step %v", n, d, step)
			}
		}
	}
	if n := resourceFailingAttempts(); n != 13 { // 100 ms * 2^12 is the first step at 5 minutes
		t.Fatalf("failing attempts %d", n)
	}
	h := &resourceHolds{items: map[string]resourceHold{}}
	for range 3 {
		h.fail("project:p", 0)
	}
	before := time.Now()
	h.fail("execution:e", 12) // continues a durable pause at the maximum
	if h.items["execution:e"].failures != 13 || h.items["execution:e"].until.Sub(before) < 225*time.Second {
		t.Fatal("hold did not continue the durable pause", h.items["execution:e"])
	}
	if strings.Join(h.ids("project:", true), ",") != "p" || h.count("project:q") != 0 || strings.Join(h.failing(4), ",") != "execution:e" || strings.Join(h.failing(3), ",") != "execution:e,project:p" {
		t.Fatal("hold not recorded", h.items)
	}
	if strings.Join(h.ids("project:", true), ",") != "p" || strings.Join(h.ids("execution:", true), ",") != "e" {
		t.Fatal("hold ids", h.items)
	}
	h.forget("project:p")
	if len(h.ids("execution:", false)) != 0 {
		t.Fatal("held execution ended", h.items)
	}
	h.items["execution:e"] = resourceHold{failures: 13}
	if strings.Join(h.ids("execution:", false), ",") != "e" {
		t.Fatal("ended hold not listed", h.items)
	}
	h.forget("execution:e")
	if len(h.items) != 0 {
		t.Fatal("hold not released", h.items)
	}
	for d, want := range map[time.Duration]string{time.Minute: "1 minute", 90 * time.Second: "1.5 minutes", 5 * time.Minute: "5 minutes", time.Second: "1 second", 800 * time.Millisecond: "0.8 seconds"} {
		withResourceRetry(t, time.Millisecond, d)
		if got := resourceRetryEvery(); got != want {
			t.Fatalf("every %v = %q", d, got)
		}
	}
	withResourceRetry(t, 100*time.Millisecond, 110*time.Millisecond)
	for range 20 {
		if d := resourceRetryDelay(0); d < 100*time.Millisecond || d > 110*time.Millisecond {
			t.Fatalf("uncapped step jitter exceeded its bounds: %v", d)
		}
	}
	withResourceRetry(t, time.Second, time.Second)
	if n, d := resourceFailingAttempts(), resourceRetryDelay(5); n != 1 || d < 750*time.Millisecond || d > time.Second {
		t.Fatalf("base at the maximum: failing attempts %d, delay %v", n, d)
	}
}

// Requirement: a resource item that fails, before or after its claim, pauses on its own while the healthy items keep
// flowing, surfaces once as the resource-failing problem and resets on success; risk: the 2026-11-17 hot loop (a
// failure before the claim recorded nothing, so every 100 ms pass failed it again for hours).
func TestResourceRetryPausePostgres(t *testing.T) {
	env := openTestCoordinator(t, false)
	a, ctx := env.app, env.ctx
	withResourceRetry(t, 2*time.Second, 8*time.Second) // failing from 3 attempts on; long enough for a slow machine
	resetHolds := func() {
		resourceHeld.mu.Lock()
		resourceHeld.items = map[string]resourceHold{}
		resourceHeld.mu.Unlock()
	}
	resetHolds()
	t.Cleanup(resetHolds)
	sql := func(s string, args ...any) {
		t.Helper()
		if err := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error { return exec(q, s, args...) }); err != nil {
			t.Fatal(err)
		}
	}
	owner := func(s string) {
		t.Helper()
		if _, err := env.ownerStore.pool.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	scalar := func(s string, args ...any) string {
		t.Helper()
		var v string
		if err := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error { return q.tx.QueryRow(q.ctx(), s, args...).Scan(&v) }); err != nil {
			t.Fatal(err)
		}
		return v
	}
	equal := func(s, want string, args ...any) {
		t.Helper()
		if got := scalar(s, args...); got != want {
			t.Fatalf("%s = %q want %q", s, got, want)
		}
	}
	finding := func() string {
		t.Helper()
		fs, _, err := stateRules(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if f.Rule == "resource-failing" {
				return f.Cause + " " + *f.Text
			}
		}
		return ""
	}
	pass := func() error { t.Helper(); return resourceProcessorPass(ctx, a) }
	// Let the shipped projects' builds settle, so the passes below fail only on purpose.
	for i := 0; i < 200 && scalar(`SELECT count(*)::text FROM resource_build WHERE NOT ready`) != "0"; i++ {
		if err := pass(); err != nil {
			t.Fatal(err)
		}
	}

	// The incident's item: a '' project makes the scope lock refuse it before the claim.
	owner(`ALTER TABLE execution DROP CONSTRAINT execution_project_id_not_empty;
	 SELECT set_config('fleet.project_ids','*',false); INSERT INTO execution(id,kind,state,project_id) VALUES('pause-poison','tool_run','succeeded','')`)
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('pause-healthy-1','tool_run','succeeded','PAUSE')`)
	err := pass()
	if err == nil || !strings.Contains(err.Error(), `unknown advisory lock scope "project:"`) || errors.Is(err, errResourceStall) {
		t.Fatal("poison pass", err)
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-healthy-1'`, "1")
	equal(`SELECT attempts::text||' '||last_error_class||' '||(next_attempt_at>clock_timestamp())::text FROM resource_work WHERE execution_id='pause-poison'`, "1 projection_failed true")
	// Losing a COMMIT acknowledgement after its claimed rows were deleted leaves later work due.
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('pause-later','tool_run','succeeded','PAUSE')`)
	if err := resourceReplaceFailed(ctx, a, "pause-later", []string{"00000000-0000-0000-0000-000000000001"}, errors.New("commit response lost")); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT attempts::text||' '||(next_attempt_at<=clock_timestamp())::text FROM resource_work WHERE execution_id='pause-later'`, "0 true")
	// Paused: the next passes leave it alone and keep serving new healthy work at full pace.
	for _, id := range []string{"pause-healthy-2", "pause-healthy-3"} {
		sql(`INSERT INTO execution(id,kind,state,project_id) VALUES($1,'tool_run','succeeded','PAUSE')`, id)
		if err := pass(); err != nil {
			t.Fatal("pass while paused", err)
		}
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id LIKE 'pause-healthy-%'`, "3")
	equal(`SELECT attempts::text FROM resource_work WHERE execution_id='pause-poison'`, "1")
	if f := finding(); f != "" {
		t.Fatal("one failure is not persistent", f)
	}
	// The execution is the item: a new source row (due at once) neither makes it a candidate before its pause ends nor
	// restarts its count; every row gets the same pause.
	owner(`SELECT set_config('fleet.project_ids','*',false); SELECT resource_enqueue_source('pause-poison','','')`)
	if err := pass(); err != nil {
		t.Fatal("a new row bypassed the pause", err)
	}
	equal(`SELECT string_agg(attempts::text,',' ORDER BY attempts) FROM resource_work WHERE execution_id='pause-poison'`, "0,1")
	// Each further failure doubles the pause up to the maximum; then it is persistent and asks once.
	for i, low := range []float64{4, 8} {
		sql(`UPDATE resource_work SET next_attempt_at=clock_timestamp() WHERE execution_id='pause-poison'`)
		if err := pass(); err == nil {
			t.Fatal("poison passed")
		}
		var rows, attempts int
		var left float64
		if err := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error {
			return q.tx.QueryRow(q.ctx(), `SELECT count(DISTINCT (attempts,next_attempt_at)),max(attempts),extract(epoch FROM max(next_attempt_at)-clock_timestamp())::float8 FROM resource_work WHERE execution_id='pause-poison'`).Scan(&rows, &attempts, &left)
		}); err != nil {
			t.Fatal(err)
		}
		if rows != 1 || attempts != i+2 || left > 8 || left < min(low, 6)-1 {
			t.Fatalf("failure %d: %d distinct pauses, attempts %d, pause left %.3fs", i+2, rows, attempts, left)
		}
	}
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='pause-poison'`, "2")
	if f := finding(); !strings.HasPrefix(f, "problem:resource:failing ") || !strings.Contains(f, "execution pause-poison") {
		t.Fatal("persistent failure not surfaced", f)
	}
	// One open question tracks the current failure without asking again or rewriting an answered question.
	syncFinding := func(text string) {
		t.Helper()
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
			return problemsSync(q, "rules", []Problem{{Cause: "problem:resource:failing", Text: text, Rule: "resource-failing", RefreshText: true}})
		}); err != nil {
			t.Fatal(err)
		}
	}
	syncFinding("execution pause-poison")
	syncFinding("execution another-poison")
	equal(`SELECT count(*)::text||' '||max(text) FROM question WHERE cause='problem:resource:failing'`, "1 execution another-poison")
	sql(`UPDATE question SET state='answered' WHERE cause='problem:resource:failing'`)
	syncFinding("execution third-poison")
	equal(`SELECT count(*)::text||' '||max(text) FROM question WHERE cause='problem:resource:failing'`, "1 execution another-poison")
	// The fix (migration 68's move) lets it succeed: its work is consumed and the problem goes.
	owner(`SELECT set_config('fleet.project_ids','*',false); UPDATE execution SET project_id='PAUSE' WHERE id='pause-poison'; DELETE FROM resource_work_scope WHERE project_id=''`)
	sql(`UPDATE resource_work SET next_attempt_at=clock_timestamp() WHERE execution_id='pause-poison'`)
	if err := pass(); err != nil {
		t.Fatal("fixed pass", err)
	}
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='pause-poison'`, "0")
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-poison'`, "1")
	if f := finding(); f != "" {
		t.Fatal("problem stays after success", f)
	}

	// A failing build page pauses only that build; a success sets its attempts back to 0.
	owner(`CREATE FUNCTION pause_build_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.project_id='PAUSE_BUILD' AND NEW.attempts=0 AND EXISTS(SELECT 1 FROM pg_class WHERE relname='pause_build_on') THEN RAISE EXCEPTION 'build fault'; END IF; RETURN NEW; END $$;
	 CREATE TABLE pause_build_on(); CREATE TRIGGER pause_build_fault BEFORE UPDATE ON resource_build FOR EACH ROW EXECUTE FUNCTION pause_build_fault()`)
	for i := 0; i < 200 && scalar(`SELECT count(*)::text FROM resource_build WHERE NOT ready`) != "0"; i++ {
		if err := pass(); err != nil {
			t.Fatal(err)
		}
	}
	sql(`INSERT INTO resource_build(project_id) VALUES('PAUSE_BUILD')`)
	if err := pass(); err == nil || !strings.Contains(err.Error(), "build fault") {
		t.Fatal("build fault", err)
	}
	equal(`SELECT attempts::text||' '||last_error||' '||(next_attempt_at>clock_timestamp())::text FROM resource_build WHERE project_id='PAUSE_BUILD'`, "1 build_failed true")
	if err := pass(); err != nil {
		t.Fatal("pass while the build is paused", err)
	}
	equal(`SELECT attempts::text FROM resource_build WHERE project_id='PAUSE_BUILD'`, "1")
	owner(`DROP TABLE pause_build_on`)
	sql(`UPDATE resource_build SET next_attempt_at=clock_timestamp() WHERE project_id='PAUSE_BUILD'`)
	for i := 0; i < 20 && scalar(`SELECT ready::text FROM resource_build WHERE project_id='PAUSE_BUILD'`) != "true"; i++ {
		if err := pass(); err != nil {
			t.Fatal(err)
		}
	}
	equal(`SELECT ready::text||' '||attempts::text FROM resource_build WHERE project_id='PAUSE_BUILD'`, "true 0")

	// A pause that cannot be written holds the execution in memory: it neither spins nor stalls the others.
	owner(`SELECT set_config('fleet.project_ids','*',false); INSERT INTO execution(id,kind,state,project_id) VALUES('pause-stall','tool_run','succeeded','');
	 CREATE FUNCTION pause_work_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'pause fault' USING ERRCODE = '40001'; END $$;
	 CREATE TRIGGER pause_work_fault BEFORE UPDATE ON resource_work FOR EACH ROW WHEN (NEW.execution_id='pause-stall') EXECUTE FUNCTION pause_work_fault()`)
	pauseCtx, cancelPause := context.WithTimeout(ctx, 5*time.Second)
	defer cancelPause()
	if err := resourceProcessorPass(pauseCtx, a); errors.Is(err, errResourceStall) || err == nil || !strings.Contains(err.Error(), "pause fault") || !slices.Contains(resourceHeld.ids("execution:", true), "pause-stall") {
		t.Fatal("unwritable pause", err)
	}
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('pause-healthy-4','tool_run','succeeded','PAUSE')`)
	if err := pass(); err != nil {
		t.Fatal("held execution retried or blocked the others", err)
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-healthy-4'`, "1")
	resourceHeld.fail("execution:pause-stall", 0)
	resourceHeld.fail("execution:pause-stall", 0)
	if f := finding(); !strings.Contains(f, "execution pause-stall (held: its pause cannot be written)") {
		t.Fatal("held execution not surfaced", f)
	}
	owner(`DROP TRIGGER pause_work_fault ON resource_work; SELECT set_config('fleet.project_ids','*',false); DELETE FROM resource_work WHERE execution_id='pause-stall'`)
	resourceHeld.forget("execution:pause-stall")

	// A processor that keeps stalling asks once (problem:resource:stalled) and withdraws the question when it recovers.
	stalled := func() string {
		t.Helper()
		return scalar(`SELECT count(*)::text FROM question WHERE cause='problem:resource:stalled' AND state='open'`)
	}
	guard, err := env.ownerStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer guard.Rollback(context.Background())
	if _, err := guard.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", GLOBAL); err != nil {
		t.Fatal(err)
	}
	notifyCtx, cancelNotify := context.WithTimeout(ctx, 5*time.Second)
	defer cancelNotify()
	if resourceStallProblem(notifyCtx, a, true) || notifyCtx.Err() != nil {
		t.Fatal("the resource processor waited for the notification lock")
	}
	if err := guard.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	resourceStallProblem(ctx, a, true)
	resourceStallProblem(ctx, a, true)
	if stalled() != "1" {
		t.Fatal("stall not asked once", stalled())
	}
	resourceStallProblem(ctx, a, false)
	if stalled() != "0" {
		t.Fatal("stall question stays", stalled())
	}

	// A broken scope table stalls all reads. A scope error with a successful empty-key probe is local to its data.
	hintErr := errors.New("candidate scope read failed")
	if got := resourceHintFailure(ctx, a, hintErr); got != hintErr {
		t.Fatal("an isolated scope failure stalled the processor", got)
	}
	owner(`ALTER TABLE resource_work_scope RENAME TO resource_work_scope_away`)
	if got := resourceHintFailure(ctx, a, hintErr); !errors.Is(got, errResourceStall) || !errors.Is(got, hintErr) {
		t.Fatal("a broken scope read did not stall the processor", got)
	}
	owner(`ALTER TABLE resource_work_scope_away RENAME TO resource_work_scope`)

	// A pass that cannot select its work is a processor-wide failure: the loop pauses, not the items.
	owner(`ALTER TABLE resource_metadata_repair RENAME TO resource_metadata_repair_away`)
	if err := pass(); !errors.Is(err, errResourceStall) {
		t.Fatal("selection failure", err)
	}
	owner(`ALTER TABLE resource_metadata_repair_away RENAME TO resource_metadata_repair`)
	if err := pass(); err != nil {
		t.Fatal(err)
	}
}
