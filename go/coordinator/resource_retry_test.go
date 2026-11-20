package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func withResourceRetry(t *testing.T, base, maximum time.Duration) {
	t.Helper()
	b, m := kitRetryBase, kitRetryMax
	kitRetryBase, kitRetryMax = base, maximum
	t.Cleanup(func() { kitRetryBase, kitRetryMax = b, m })
}

// Requirement: a failing item pauses exponentially with jitter up to the maximum, reproducibly for a seed; risk: equal
// failures retry in step, or the pause grows without a ceiling.
func TestResourceRetryDelay(t *testing.T) {
	withResourceRetry(t, 100*time.Millisecond, 5*time.Minute)
	k := newWorkKit(&App{}, time.Now, 7)
	for n := 0; n < 80; n++ {
		step := min(5*time.Minute, 100*time.Millisecond<<min(n, 30))
		for range 20 {
			low := step
			if step == 5*time.Minute {
				low = min(step, 5*time.Minute*3/4) // at the maximum the jitter goes below it
			}
			if d := k.backoff(n); d < low || d > min(5*time.Minute, step*3/2) {
				t.Fatalf("delay(%d) = %v, step %v", n, d, step)
			}
		}
	}
	a, b := newWorkKit(&App{}, time.Now, 9), newWorkKit(&App{}, time.Now, 9)
	for n := 0; n < 20; n++ {
		if x, y := a.backoff(n), b.backoff(n); x != y {
			t.Fatalf("same seed, different jitter: %v %v", x, y)
		}
	}
	if n := kitFailingAttempts(); n != 13 { // 100 ms * 2^12 is the first step at 5 minutes
		t.Fatalf("failing attempts %d", n)
	}
	for d, want := range map[time.Duration]string{time.Minute: "1 minute", 90 * time.Second: "1.5 minutes", 5 * time.Minute: "5 minutes", time.Second: "1 second", 800 * time.Millisecond: "0.8 seconds"} {
		withResourceRetry(t, time.Millisecond, d)
		if got := kitRetryEvery(); got != want {
			t.Fatalf("every %v = %q", d, got)
		}
	}
	withResourceRetry(t, 100*time.Millisecond, 110*time.Millisecond)
	for range 20 {
		if d := k.backoff(0); d < 100*time.Millisecond || d > 110*time.Millisecond {
			t.Fatalf("uncapped step jitter exceeded its bounds: %v", d)
		}
	}
	withResourceRetry(t, time.Second, time.Second)
	if n, d := kitFailingAttempts(), k.backoff(5); n != 1 || d < 750*time.Millisecond || d > time.Second {
		t.Fatalf("base at the maximum: failing attempts %d, delay %v", n, d)
	}
}

// Requirement: a resource item that fails, before or after its claim, pauses on its own while the healthy items keep
// flowing, surfaces once as the resource-failing problem and resets on success; a queue whose own bookkeeping fails
// backs off as a whole and surfaces as resource-stalled; risk: the 2026-11-17 hot loop (a failure before the claim
// recorded nothing, so every 100 ms pass failed it again for hours).
func TestResourceRetryPausePostgres(t *testing.T) {
	env := openTestCoordinator(t, false)
	a, ctx := env.app, env.ctx
	withResourceRetry(t, 2*time.Second, 8*time.Second) // failing from 3 attempts on
	var offset time.Duration
	k := newWorkKit(a, func() time.Time { return time.Now().Add(offset) }, 1)
	kitDefault.Store(a, k)
	t.Cleanup(func() { kitDefault.Delete(a) })
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
	finding := func(rule string) string {
		t.Helper()
		fs, _, err := stateRules(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if f.Rule == rule {
				return f.Cause + " " + *f.Text
			}
		}
		return ""
	}
	pass := func() error { t.Helper(); return k.pass(ctx) }
	paused := func(id string) string {
		t.Helper()
		return scalar(`SELECT max(attempts)::text||' '||coalesce(max(last_error),'-')||' '||(max(next_attempt_at)>$2)::text FROM resource_work WHERE execution_id=$1`, id, k.now())
	}
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
	if err := pass(); err == nil || !strings.Contains(err.Error(), `unknown advisory lock scope "project:"`) {
		t.Fatal("poison pass", err)
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-healthy-1'`, "1")
	if got := paused("pause-poison"); got != "1 projection_failed true" {
		t.Fatal("poison not paused", got)
	}
	if h := k.kitHealth(); len(h) != 0 {
		t.Fatal("an item's failure made its queue sick", h)
	}
	// A failure written after its claims were consumed (a COMMIT whose response was lost) pauses nothing later.
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('pause-later','tool_run','succeeded','PAUSE')`)
	if err := k.fail(ctx, k.queue("resource-work"), k.now(), &kitClaim{Key: "pause-later", Guard: []any{[]string{"00000000-0000-0000-0000-000000000001"}}}, context.DeadlineExceeded); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT attempts::text FROM resource_work WHERE execution_id='pause-later'`, "0")
	// A busy lock skips the execution this pass without counting a failure; the others keep flowing.
	busy, err := env.ownerStore.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Rollback(context.Background())
	if _, err := busy.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('execution:pause-later',0))"); err != nil {
		t.Fatal(err)
	}
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('lock-healthy','tool_run','succeeded','PAUSE2')`)
	busyCtx, cancelBusy := context.WithTimeout(ctx, 5*time.Second)
	defer cancelBusy()
	if err := k.pass(busyCtx); err != nil || busyCtx.Err() != nil { // nothing waits
		t.Fatal("busy pass", err)
	}
	equal(`SELECT attempts::text||' '||count(*)::text FROM resource_work WHERE execution_id='pause-later' GROUP BY attempts`, "0 1")
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='lock-healthy'`, "1")
	if err := busy.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	// Paused: the next passes leave it alone; a new source row (due at once) neither makes it a candidate nor restarts
	// its count.
	owner(`SELECT set_config('fleet.project_ids','*',false); SELECT resource_enqueue_source('pause-poison','','')`)
	if err := pass(); err != nil {
		t.Fatal("a new row bypassed the pause", err)
	}
	equal(`SELECT string_agg(attempts::text,',' ORDER BY attempts) FROM resource_work WHERE execution_id='pause-poison'`, "0,1")
	// Each further failure doubles the pause up to the maximum; then it is failing.
	for i, low := range []float64{4, 8} {
		offset += 9 * time.Second
		if err := pass(); err == nil {
			t.Fatal("poison passed")
		}
		var rows, attempts int
		var left float64
		if err := runTx(ctx, a, nil, false, NOLOCK, func(q *Request) error {
			return q.tx.QueryRow(q.ctx(), `SELECT count(DISTINCT (attempts,next_attempt_at)),max(attempts),extract(epoch FROM max(next_attempt_at)-$1)::float8 FROM resource_work WHERE execution_id='pause-poison'`, k.now()).Scan(&rows, &attempts, &left)
		}); err != nil {
			t.Fatal(err)
		}
		if rows != 1 || attempts != i+2 || left > 8 || left < min(low, 6)-0.5 {
			t.Fatalf("failure %d: %d distinct pauses, attempts %d, pause left %.3fs", i+2, rows, attempts, left)
		}
	}
	if f := finding("resource-failing"); !strings.HasPrefix(f, "problem:resource:failing ") || !strings.Contains(f, "execution pause-poison") {
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
	offset += 9 * time.Second
	if err := pass(); err != nil {
		t.Fatal("fixed pass", err)
	}
	equal(`SELECT count(*)::text FROM resource_work WHERE execution_id='pause-poison'`, "0")
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-poison'`, "1")
	if f := finding("resource-failing"); f != "" {
		t.Fatal("problem stays after success", f)
	}

	// A failing build page pauses only that build; a success sets its attempts back to 0.
	owner(`CREATE FUNCTION pause_build_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
	 IF NEW.project_id='PAUSE_BUILD' AND NEW.attempts=0 AND EXISTS(SELECT 1 FROM pg_class WHERE relname='pause_build_on') THEN RAISE EXCEPTION 'build fault'; END IF; RETURN NEW; END $$;
	 CREATE TABLE pause_build_on(); CREATE TRIGGER pause_build_fault BEFORE UPDATE ON resource_build FOR EACH ROW EXECUTE FUNCTION pause_build_fault()`)
	sql(`INSERT INTO resource_build(project_id) VALUES('PAUSE_BUILD')`)
	if err := pass(); err == nil || !strings.Contains(err.Error(), "build fault") {
		t.Fatal("build fault", err)
	}
	equal(`SELECT attempts::text||' '||last_error||' '||(next_attempt_at>$1)::text FROM resource_build WHERE project_id='PAUSE_BUILD'`, "1 build_failed true", k.now())
	if err := pass(); err != nil {
		t.Fatal("pass while the build is paused", err)
	}
	equal(`SELECT attempts::text FROM resource_build WHERE project_id='PAUSE_BUILD'`, "1")
	owner(`DROP TABLE pause_build_on`)
	offset += 9 * time.Second
	for i := 0; i < 20 && scalar(`SELECT ready::text FROM resource_build WHERE project_id='PAUSE_BUILD'`) != "true"; i++ {
		if err := pass(); err != nil {
			t.Fatal(err)
		}
	}
	equal(`SELECT ready::text||' '||attempts::text||' '||coalesce(last_error,'-') FROM resource_build WHERE project_id='PAUSE_BUILD'`, "true 0 -")

	// A failure that cannot be written makes the work queue back off as a whole; repeated, the processor is stalled.
	owner(`SELECT set_config('fleet.project_ids','*',false); INSERT INTO execution(id,kind,state,project_id) VALUES('pause-stall','tool_run','succeeded','');
	 CREATE FUNCTION pause_work_fault() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'pause fault' USING ERRCODE = '40001'; END $$;
	 CREATE TRIGGER pause_work_fault BEFORE UPDATE ON resource_work FOR EACH ROW WHEN (NEW.execution_id='pause-stall') EXECUTE FUNCTION pause_work_fault()`)
	for i := 0; i < kitFailingAttempts(); i++ {
		offset += 9 * time.Second
		if err := pass(); err == nil || !strings.Contains(err.Error(), "not recorded") {
			t.Fatal("unwritable failure", err)
		}
	}
	if h := k.kitHealth(); len(h) != 1 || h[0].Queue != "resource-work" || h[0].Failures != kitFailingAttempts() {
		t.Fatal("queue health", h)
	}
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('pause-healthy-4','tool_run','succeeded','PAUSE')`)
	if err := pass(); err != nil {
		t.Fatal("a backing-off queue ran", err)
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-healthy-4'`, "0")
	if f := finding("work-stalled"); !strings.HasPrefix(f, "problem:work:stalled ") || !strings.Contains(f, "resource-work") {
		t.Fatal("stalled queue not surfaced", f)
	}
	owner(`DROP TRIGGER pause_work_fault ON resource_work; SELECT set_config('fleet.project_ids','*',false); DELETE FROM resource_work WHERE execution_id='pause-stall'`)
	offset += 9 * time.Second
	if err := pass(); err != nil {
		t.Fatal(err)
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-healthy-4'`, "1")
	if f := finding("work-stalled"); f != "" || len(k.kitHealth()) != 0 {
		t.Fatal("stall stays after recovery", f, k.kitHealth())
	}

	// A queue that cannot select its work backs off alone; the other queues keep their pace.
	owner(`ALTER TABLE resource_metadata_repair RENAME TO resource_metadata_repair_away`)
	sql(`INSERT INTO execution(id,kind,state,project_id) VALUES('pause-healthy-5','tool_run','succeeded','PAUSE')`)
	if err := pass(); err == nil || !strings.Contains(err.Error(), "repair selection") {
		t.Fatal("selection failure", err)
	}
	equal(`SELECT count(*)::text FROM resource_contribution WHERE execution_id='pause-healthy-5'`, "1")
	owner(`ALTER TABLE resource_metadata_repair_away RENAME TO resource_metadata_repair`)
	offset += 9 * time.Second
	if err := pass(); err != nil {
		t.Fatal(err)
	}
	if h := k.kitHealth(); len(h) != 0 {
		t.Fatal("queue did not recover", h)
	}
}
