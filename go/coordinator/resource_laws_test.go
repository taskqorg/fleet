package main

// Executable laws of the resource projection (DESIGN_RULES.md, R1-R8). A seeded generator drives the real engine on
// real PostgreSQL through random source changes, faults, restarts and clock moves; after every pass and at quiescence
// the laws L1-L7 must hold. The harness reaches the engine only through resource_laws_adapter_test.go.
// FLEET_LAW_SEEDS=n runs n seeds (default 20), FLEET_LAW_SEED=k replays one seed; a violation prints its trace.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"net"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"fleet/contract"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	lawBase = time.Second      // retry base the laws run with
	lawMax  = 16 * time.Second // retry maximum the laws run with
)

var lawProjects = []string{"LA", "LB", "LC"}

// lawFaultSQL: armed faults, read by triggers on the tables a step writes. A fault fires on the nth..nth+repeat-1 write of
// its target (a sequence counts writes outside any transaction, so a rolled-back write still counts). The harness's own
// clock moves set law.harness and never fire one.
const lawFaultSQL = `
CREATE TABLE law_fault(id int PRIMARY KEY, tbl text NOT NULL, target text NOT NULL, nth int NOT NULL, mode text NOT NULL, repeat int NOT NULL);
DO $$ BEGIN FOR i IN 1..64 LOOP EXECUTE format('CREATE SEQUENCE law_hit_%s', i); END LOOP; END $$;
CREATE FUNCTION law_fault_fire() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE k text; f record; h bigint;
BEGIN
 IF current_setting('law.harness', true)='on' THEN IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW; END IF;
 IF TG_OP='DELETE' THEN k := to_jsonb(OLD)->>TG_ARGV[0]; ELSE k := to_jsonb(NEW)->>TG_ARGV[0]; END IF;
 FOR f IN SELECT * FROM law_fault WHERE tbl=TG_TABLE_NAME AND (target=k OR target='*') ORDER BY id LOOP
  h := nextval(format('law_hit_%s', f.id));
  IF h >= f.nth AND h < f.nth + f.repeat THEN
   IF f.mode='40001' THEN RAISE EXCEPTION 'law fault % % 40001', TG_TABLE_NAME, k USING ERRCODE='40001'; END IF;
   IF f.mode='terminate' THEN PERFORM pg_terminate_backend(pg_backend_pid()); PERFORM pg_sleep(5); END IF;
   RAISE EXCEPTION 'law fault % % %', TG_TABLE_NAME, k, f.mode;
  END IF;
 END LOOP;
 IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END $$;
CREATE TRIGGER law_fault BEFORE INSERT OR UPDATE OR DELETE ON resource_contribution FOR EACH ROW EXECUTE FUNCTION law_fault_fire('execution_id');
CREATE TRIGGER law_fault BEFORE INSERT OR UPDATE OR DELETE ON resource_metric FOR EACH ROW EXECUTE FUNCTION law_fault_fire('project_id');
CREATE TRIGGER law_fault BEFORE UPDATE ON resource_project_summary FOR EACH ROW EXECUTE FUNCTION law_fault_fire('project_id');
CREATE TRIGGER law_fault BEFORE UPDATE ON resource_build FOR EACH ROW EXECUTE FUNCTION law_fault_fire('project_id');
CREATE TRIGGER law_fault BEFORE UPDATE ON resource_work FOR EACH ROW EXECUTE FUNCTION law_fault_fire('execution_id');
SELECT set_config('fleet.project_ids','*',false);
INSERT INTO definition(kind,id,path,head,project_id) VALUES('project','LA','la',0,'LA'),('project','LB','lb',0,'LB'),('project','LC','lc',0,'LC');
INSERT INTO price_entry(id,subject,unit,price,currency,effective_from,observed_at,source) VALUES('law-price','m','input_tokens',0.01,'USD','2020-01-01','2020-01-01','owner');
`

var lawTemplate struct {
	once      sync.Once
	name, err string
	sock, bin string
	port      int
}

// lawTemplateDB migrates one database per test process; every seed clones it.
func lawTemplateDB(t *testing.T) string {
	t.Helper()
	sock, port, bin := ensureTestPostgres(t)
	lawTemplate.once.Do(func() {
		lawTemplate.sock, lawTemplate.port, lawTemplate.bin = sock, port, bin
		name := fmt.Sprintf("fleet_law_tmpl_%d", os.Getpid())
		raw, err := os.ReadFile("../../deploy/infrastructure/fleet-postgres.sql")
		if err != nil {
			lawTemplate.err = err.Error()
			return
		}
		runTestPSQL(t, bin, sock, port, "postgres", "postgres", strings.NewReplacer("DATABASE fleet", "DATABASE "+name,
			"datname = 'fleet'", "datname = '"+name+"'", "\\connect fleet", "\\connect "+name).Replace(string(raw)))
		ctx := context.Background()
		url := lawURL("fleet", name)
		if err := migrate(ctx, url); err != nil {
			lawTemplate.err = "migrate: " + err.Error()
			return
		}
		conn, err := pgx.Connect(ctx, url)
		if err != nil {
			lawTemplate.err = err.Error()
			return
		}
		defer conn.Close(ctx)
		if _, err := conn.Exec(ctx, lawFaultSQL); err != nil {
			lawTemplate.err = "law fixture: " + err.Error()
			return
		}
		lawTemplate.name = name
	})
	if lawTemplate.err != "" {
		t.Fatal(lawTemplate.err)
	}
	return lawTemplate.name
}

func lawURL(role, db string) string {
	return fmt.Sprintf("postgresql://%s@/%s?host=%s&port=%d", role, db, lawTemplate.sock, lawTemplate.port)
}

var lawDBSeq atomic.Int64

// lawClone creates a database from the template for one seed and drops it when the test ends.
func lawClone(t *testing.T) string {
	t.Helper()
	tmpl := lawTemplateDB(t)
	name := fmt.Sprintf("fleet_law_%d_%d", os.Getpid(), lawDBSeq.Add(1))
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, lawURL("postgres", "postgres"))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for _, s := range []string{`CREATE DATABASE ` + name + ` TEMPLATE ` + tmpl + ` OWNER fleet STRATEGY FILE_COPY`, `REVOKE ALL ON DATABASE ` + name + ` FROM PUBLIC`, `GRANT CONNECT ON DATABASE ` + name + ` TO fleet_app`} {
		if _, err := admin.Exec(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		if c, err := pgx.Connect(context.Background(), lawURL("postgres", "postgres")); err == nil {
			_, _ = c.Exec(context.Background(), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
			_ = c.Close(context.Background())
		}
	})
	return name
}

type lawEnv struct {
	t        *testing.T
	ctx      context.Context
	seed     int64
	rng      *rand.Rand
	db       string
	ownerDB  *pgxpool.Pool
	st       *Store
	a        *App
	lost     *App
	lostPool *pgxpool.Pool
	drop     atomic.Bool
	lostNext bool
	off      bool
	offFor   int
	faults   int
	now      time.Duration // fake time the harness moved the clock by
	trace    []string
	values   map[string]string    // L7: execution -> frozen method_sha
	versions map[string][2]string // L4: project -> last ready (epoch, revision)
	rebuilt  map[string]bool      // L4: projects rebuilt since their epoch was last seen
}

func newLawEnv(t *testing.T, ctx context.Context, seed int64) *lawEnv {
	t.Helper()
	e := &lawEnv{t: t, ctx: ctx, seed: seed, rng: rand.New(rand.NewSource(seed)), db: lawClone(t),
		values: map[string]string{}, versions: map[string][2]string{}, rebuilt: map[string]bool{}}
	cfg, err := pgxpool.ParseConfig(lawURL("fleet", e.db))
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 4
	cfg.ConnConfig.RuntimeParams["options"] = "-c fleet.project_ids=* -c law.harness=on"
	if e.ownerDB, err = pgxpool.NewWithConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	e.open()
	t.Cleanup(func() { e.close(); e.ownerDB.Close() })
	return e
}

// open starts a new engine process view: a store, an App, and an App whose first COMMIT response can be lost.
func (e *lawEnv) open() {
	st, err := openStore(e.ctx, lawURL("fleet_app", e.db))
	if err != nil {
		e.t.Fatal(err)
	}
	e.st, e.a = st, &App{store: st}
	cfg := st.pool.Config()
	dial := cfg.ConnConfig.DialFunc
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		c, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &resourceLostCommitConn{Conn: c, reader: bufio.NewReader(c), drop: &e.drop}, nil
	}
	if e.lostPool, err = pgxpool.NewWithConfig(e.ctx, cfg); err != nil {
		e.t.Fatal(err)
	}
	e.lost = &App{store: &Store{pool: e.lostPool, readPool: st.readPool}}
	lawAttach(e, e.a)
	lawAttach(e, e.lost)
}

func (e *lawEnv) close() {
	e.lostPool.Close()
	e.st.Close()
}

func (e *lawEnv) log(format string, args ...any) {
	e.trace = append(e.trace, fmt.Sprintf(format, args...))
}

// violate fails the test with the law, the seed and the replay trace.
func (e *lawEnv) violate(law, format string, args ...any) {
	e.t.Helper()
	tail := e.trace
	if len(tail) > 80 {
		tail = tail[len(tail)-80:]
	}
	e.t.Fatalf("LAW VIOLATION %s (seed %d, replay with FLEET_LAW_SEED=%d): %s\ntrace (last %d of %d):\n  %s", law, e.seed, e.seed,
		fmt.Sprintf(format, args...), len(tail), len(e.trace), strings.Join(tail, "\n  "))
}

func (e *lawEnv) owner(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.ownerDB.Exec(e.ctx, sql, args...); err != nil {
		e.t.Fatalf("seed %d: %s: %v", e.seed, sql, err)
	}
}

func (e *lawEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var v *string
	if err := e.ownerDB.QueryRow(e.ctx, sql, args...).Scan(&v); err != nil {
		e.t.Fatalf("seed %d: %s: %v", e.seed, sql, err)
	}
	if v == nil {
		return ""
	}
	return *v
}

func (e *lawEnv) strings(sql string, args ...any) []string {
	e.t.Helper()
	rs, err := e.ownerDB.Query(e.ctx, sql, args...)
	if err != nil {
		e.t.Fatalf("seed %d: %s: %v", e.seed, sql, err)
	}
	out, err := pgx.CollectRows(rs, pgx.RowTo[string])
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func short(err error) string {
	if err == nil {
		return "ok"
	}
	s := strings.ReplaceAll(err.Error(), "\n", " | ")
	if len(s) > 160 {
		s = s[:160] + "..."
	}
	return s
}

func (e *lawEnv) pick(xs []string) string { return xs[e.rng.Intn(len(xs))] }

func (e *lawEnv) project() *string {
	if e.rng.Intn(5) == 0 {
		return nil
	}
	p := e.pick(lawProjects)
	return &p
}

func deref2(p *string) string {
	if p == nil {
		return "NULL"
	}
	return *p
}

// pass runs one processor pass and checks the laws that hold after every pass.
func (e *lawEnv) pass() {
	e.t.Helper()
	if e.off {
		e.log("pass skipped: processor off")
		return
	}
	a := e.a
	if e.lostNext {
		a, e.lostNext = e.lost, false
		e.drop.Store(true)
	}
	err := lawPass(e.ctx, e, a)
	lost := a == e.lost && !e.drop.Load()
	e.drop.Store(false)
	e.log("pass lost=%v: %s", lost, short(err))
	e.checkProgress()
	e.checkProblems()
	e.checkFrozen()
}

// L4 (during the run): pending iff scoped work or repairs; revision monotone within an epoch; epoch changes only on rebuild.
func (e *lawEnv) checkProgress() {
	e.t.Helper()
	for _, p := range e.strings(`SELECT project_id FROM resource_project_summary ORDER BY 1`) {
		var s resourcePreparedState
		err := e.st.Tx(e.ctx, nil, false, NOLOCK, func(tx pgx.Tx) error {
			var err error
			s, err = resourceReadPrepared(backgroundRequest(e.ctx, e.a, tx), p)
			return err
		})
		if err != nil {
			e.violate("L4", "read of %s: %v", p, err)
		}
		want := e.scalar(`SELECT (EXISTS(SELECT 1 FROM resource_work_scope WHERE project_id=$1) OR EXISTS(SELECT 1 FROM resource_repair_scope WHERE project_id=$1)
		 OR EXISTS(SELECT 1 FROM resource_metadata_repair WHERE NOT done))::text`, p) == "true"
		// The read and the check are two snapshots; only work the harness itself did not add can differ, and it adds none here.
		if s.Pending != want {
			e.violate("L4", "project %s pending=%v, scoped work or repairs exist=%v", p, s.Pending, want)
		}
		if !s.Ready {
			continue
		}
		last, seen := e.versions[p]
		if seen && last[0] != s.Epoch && !e.rebuilt[p] {
			e.violate("L4", "project %s epoch changed %s -> %s without a rebuild", p, last[0], s.Epoch)
		}
		if seen && last[0] == s.Epoch {
			a, _ := strconv.ParseInt(last[1], 10, 64)
			b, _ := strconv.ParseInt(s.Revision, 10, 64)
			if b < a {
				e.violate("L4", "project %s revision went back %d -> %d in epoch %s", p, a, b, s.Epoch)
			}
		}
		if !seen || last[0] != s.Epoch {
			delete(e.rebuilt, p)
		}
		e.versions[p] = [2]string{s.Epoch, s.Revision}
	}
}

const lawFailingSQL = `SELECT (EXISTS(SELECT 1 FROM resource_work WHERE last_error IS NOT NULL AND attempts>=$1)
 OR EXISTS(SELECT 1 FROM resource_build WHERE NOT ready AND attempts>=$1)
 OR EXISTS(SELECT 1 FROM resource_metadata_repair WHERE attempts>=$1))::text`

// L5 (during the run): resource-failing is reported iff some item's attempts reached kitFailingAttempts().
func (e *lawEnv) checkProblems() {
	e.t.Helper()
	want := e.scalar(lawFailingSQL, kitFailingAttempts()) == "true" || lawMemoryFailing() > 0
	fs, _, err := stateRules(e.ctx, e.a)
	if err != nil {
		e.violate("L5", "stateRules: %v", err)
	}
	got := false
	for _, f := range fs {
		got = got || f.Rule == "resource-failing"
	}
	if got != want {
		e.violate("L5", "resource-failing reported=%v, an item at %d attempts exists=%v", got, kitFailingAttempts(), want)
	}
}

// L7: a frozen valuation never changes once written.
func (e *lawEnv) checkFrozen() {
	e.t.Helper()
	rs, err := e.ownerDB.Query(e.ctx, `SELECT execution_id,method_sha FROM resource_valuation`)
	if err != nil {
		e.t.Fatal(err)
	}
	type row struct{ id, sha string }
	rows, err := pgx.CollectRows(rs, func(r pgx.CollectableRow) (row, error) { var x row; err := r.Scan(&x.id, &x.sha); return x, err })
	if err != nil {
		e.t.Fatal(err)
	}
	for _, r := range rows {
		if old, ok := e.values[r.id]; ok && old != r.sha {
			e.violate("L7", "valuation of %s changed %s -> %s", r.id, old, r.sha)
		}
		e.values[r.id] = r.sha
	}
}

// The generator's operations.
func (e *lawEnv) executions() []string { return e.strings(`SELECT id FROM execution ORDER BY id`) }

func (e *lawEnv) opInsert() {
	id := fmt.Sprintf("x%02d", e.rng.Intn(24))
	if e.scalar(`SELECT count(*)::text FROM execution WHERE id=$1`, id) != "0" {
		return
	}
	p := e.project()
	start := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(e.rng.Intn(1000)) * time.Minute)
	data := `{}`
	if e.rng.Intn(2) == 0 {
		data = `{"connection":"cA"}`
	}
	if e.rng.Intn(3) > 0 {
		e.owner(`INSERT INTO execution(id,kind,state,project_id,model,started_at,ended_at,data) VALUES($1,'agent_run','succeeded',$2,'m',$3,$4,$5)`, id, p, start, start.Add(time.Duration(1+e.rng.Intn(90))*time.Minute), data)
		calls := e.rng.Intn(4)
		e.owner(`INSERT INTO execution_totals(execution_id,project_id,tokens_input,tokens_input_basis,tokens_output,tokens_output_basis,wall_ms,wall_ms_basis,tool_calls,tool_calls_basis)
		 VALUES($1,$2,$3,'measured',$4,'measured',$5,'measured',$6,'measured')`, id, p, 10+e.rng.Intn(1000), e.rng.Intn(100), 100+e.rng.Intn(10000),
			fmt.Sprintf(`{"byTool":{"a":{"calls":%d,"errors":%d}},"other":{"calls":1,"errors":0}}`, calls, e.rng.Intn(calls+1)))
		e.log("insert agent_run %s project=%s data=%s", id, deref2(p), data)
		return
	}
	state := e.pick([]string{"succeeded", "failed"})
	e.owner(`INSERT INTO execution(id,kind,state,project_id,started_at,ended_at,data) VALUES($1,'tool_run',$2,$3,$4,$5,$6::jsonb || '{"tool":"t1"}')`, id, state, p, start, start.Add(time.Minute), data)
	e.log("insert tool_run %s %s project=%s data=%s", id, state, deref2(p), data)
}

func (e *lawEnv) opUpdate() {
	xs := e.executions()
	if len(xs) == 0 {
		return
	}
	id := e.pick(xs)
	if e.scalar(`SELECT count(*)::text FROM execution_totals WHERE execution_id=$1`, id) == "1" && e.rng.Intn(3) > 0 {
		n := 1 + e.rng.Intn(50)
		e.owner(`UPDATE execution_totals SET tokens_input=tokens_input+$2 WHERE execution_id=$1`, id, n)
		e.log("update %s tokens_input+%d", id, n)
		return
	}
	e.owner(`UPDATE execution SET state=CASE WHEN state='succeeded' THEN 'failed'::execution_state ELSE 'succeeded'::execution_state END WHERE id=$1`, id)
	e.log("update %s state", id)
}

func (e *lawEnv) opMove() {
	xs := e.executions()
	if len(xs) == 0 {
		return
	}
	id, p := e.pick(xs), e.project()
	e.owner(`UPDATE execution SET project_id=$2 WHERE id=$1`, id, p)
	e.log("move %s -> %s", id, deref2(p))
}

func (e *lawEnv) opDelete() {
	xs := e.executions()
	if len(xs) == 0 {
		return
	}
	id := e.pick(xs)
	tx, err := e.ownerDB.Begin(e.ctx)
	if err != nil {
		e.t.Fatal(err)
	}
	defer tx.Rollback(e.ctx)
	for _, s := range []string{`DELETE FROM execution_totals WHERE execution_id=$1`, `DELETE FROM execution WHERE id=$1`} {
		if _, err := tx.Exec(e.ctx, s, id); err != nil {
			e.t.Fatal(err)
		}
	}
	if err := tx.Commit(e.ctx); err != nil {
		e.t.Fatal(err)
	}
	e.log("delete %s", id)
}

// opFault arms one fault: a table a step writes, a target (or every row), the write it fires on and how it fails.
func (e *lawEnv) opFault() {
	if e.faults >= 64 {
		return
	}
	tbl := e.pick([]string{"resource_contribution", "resource_metric", "resource_project_summary", "resource_build", "resource_work"})
	target := "*"
	switch tbl {
	case "resource_contribution", "resource_work":
		if xs := e.executions(); len(xs) > 0 && e.rng.Intn(4) > 0 {
			target = e.pick(xs)
		}
	default:
		if e.rng.Intn(4) > 0 {
			target = e.pick(lawProjects)
		}
	}
	mode := e.pick([]string{"error", "error", "40001", "terminate", "poison"})
	repeat := 1
	if mode == "poison" {
		mode, repeat = "error", 3+e.rng.Intn(4)
	}
	nth := 1 + e.rng.Intn(3)
	e.faults++
	e.owner(`INSERT INTO law_fault VALUES($1,$2,$3,$4,$5,$6)`, e.faults, tbl, target, nth, mode, repeat)
	e.log("fault %d %s target=%s nth=%d mode=%s repeat=%d", e.faults, tbl, target, nth, mode, repeat)
}

// opBurst changes several executions of one project at once and arms a fault on one of them, so a cohort fails.
func (e *lawEnv) opBurst() {
	p := e.pick(lawProjects)
	xs := e.strings(`SELECT id FROM execution WHERE project_id=$1 ORDER BY id`, p)
	if len(xs) < 2 || e.faults >= 64 {
		return
	}
	for _, id := range xs {
		e.owner(`UPDATE execution SET state=CASE WHEN state='succeeded' THEN 'failed'::execution_state ELSE 'succeeded'::execution_state END WHERE id=$1`, id)
	}
	e.faults++
	target := e.pick(xs)
	e.owner(`INSERT INTO law_fault VALUES($1,'resource_contribution',$2,1,'error',1)`, e.faults, target)
	e.log("burst %s %v, fault %d on %s", p, xs, e.faults, target)
	e.pass()
}

func (e *lawEnv) opRebuild() {
	p := e.pick(lawProjects)
	err := resourceRebuild(e.ctx, e.a, p)
	if err == nil {
		e.rebuilt[p] = true
	}
	e.log("rebuild %s: %s", p, short(err))
}

func (e *lawEnv) opProvider() {
	provider := e.pick([]string{"openai", "anthropic"})
	e.owner(`INSERT INTO api_connection(id,name,provider) VALUES('connection:cA','cA',$1) ON CONFLICT(id) DO UPDATE SET provider=excluded.provider`, provider)
	e.log("connection cA provider=%s", provider)
}

func (e *lawEnv) advance(d time.Duration) {
	lawAdvance(e, d)
	e.now += d
}

func (e *lawEnv) opRestart() {
	e.close()
	lawRestart(e)
	e.open()
	e.log("restart")
}

func (e *lawEnv) opRules() {
	err := rulesPass(e.ctx, e.a)
	e.log("rules pass: %s", short(err))
}

// lawDerived: every derived row of the projection, as text, for byte comparison.
func (e *lawEnv) derived() string {
	e.t.Helper()
	var b strings.Builder
	for _, s := range []string{
		`SELECT coalesce(string_agg(to_jsonb(c)::text,E'\n' ORDER BY execution_id),'') FROM resource_contribution c`,
		`SELECT coalesce(string_agg(to_jsonb(m)::text,E'\n' ORDER BY project_id,metric,basis),'') FROM resource_metric m`,
		`SELECT coalesce(string_agg(to_jsonb(x)::text,E'\n' ORDER BY project_id,metric,value),'') FROM resource_extreme x`,
		`SELECT coalesce(string_agg(to_jsonb(x)::text,E'\n' ORDER BY project_id,connection,name),'') FROM resource_tool x`,
		`SELECT coalesce(string_agg(to_jsonb(x)::text,E'\n' ORDER BY project_id,currency,basis),'') FROM resource_cost x`,
		`SELECT coalesce(string_agg(to_jsonb(x)::text,E'\n' ORDER BY project_id,currency,basis,price_id,observed_at),'') FROM resource_price_ref x`,
		`SELECT coalesce(string_agg(to_jsonb(x)::text,E'\n' ORDER BY project_id),'') FROM resource_project_summary x`,
		`SELECT coalesce(string_agg(to_jsonb(x)::text,E'\n' ORDER BY execution_id),'') FROM resource_valuation x`,
	} {
		b.WriteString(e.scalar(s))
		b.WriteString("\n--\n")
	}
	return b.String()
}

// opProbe (L3): one execution's step fails late (or loses its COMMIT response); unless the commit is known to have
// happened, its work rows remain and every derived row is byte-equal to before.
func (e *lawEnv) opProbe() {
	ids := e.strings(`SELECT DISTINCT execution_id FROM resource_work w WHERE next_attempt_at<=$1
	 AND NOT EXISTS(SELECT 1 FROM resource_work p WHERE p.execution_id=w.execution_id AND p.next_attempt_at>$1) ORDER BY 1`, lawNow(e))
	if len(ids) == 0 {
		return
	}
	id := e.pick(ids)
	work := e.scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id=$1`, id)
	before := e.derived()
	a, how := e.a, e.pick([]string{"error", "terminate", "lost"})
	fault := 0
	if how == "lost" {
		a = e.lost
		e.drop.Store(true)
	} else if e.faults < 64 {
		e.faults++
		fault = e.faults
		e.owner(`INSERT INTO law_fault VALUES($1,'resource_project_summary','*',1,$2,1)`, fault, how)
	} else {
		return
	}
	err := lawStep(e.ctx, e, a, id)
	committed := how == "lost" && !e.drop.Load()
	e.drop.Store(false)
	if fault > 0 {
		e.owner(`DELETE FROM law_fault WHERE id=$1`, fault)
	}
	e.log("probe %s %s committed=%v: %s", id, how, committed, short(err))
	if err == nil || committed {
		return
	}
	if after := e.derived(); after != before {
		e.violate("L3", "failed step of %s changed derived rows:\nbefore %s\nafter  %s", id, before, after)
	}
	left := e.scalar(`SELECT string_agg(work_id::text,',' ORDER BY work_id) FROM resource_work WHERE execution_id=$1 AND work_id::text=ANY(string_to_array($2,','))`, id, work)
	if left != work {
		e.violate("L3", "failed step of %s consumed work: had %s, left %s", id, work, left)
	}
}

// run drives n random operations.
func (e *lawEnv) run(n int) {
	e.t.Helper()
	ops := []struct {
		w  int
		fn func()
	}{
		{18, e.opInsert}, {10, e.opUpdate}, {6, e.opMove}, {4, e.opDelete}, {30, e.pass}, {6, e.opFault},
		{2, func() { e.owner(`DELETE FROM law_fault`); e.log("disarm") }},
		{3, func() { e.lostNext = true; e.log("lose next COMMIT") }},
		{2, e.opRestart}, {2, func() { e.offFor = 3 + e.rng.Intn(12); e.log("processor off for %d operations", e.offFor) }},
		{2, e.opRebuild}, {2, e.opProvider}, {6, func() { d := time.Duration(e.rng.Int63n(int64(2 * lawMax))); e.advance(d); e.log("advance %s", d) }},
		{4, e.opProbe}, {1, e.opRules}, {4, e.opBurst},
	}
	total := 0
	for _, o := range ops {
		total += o.w
	}
	for i := 0; i < n; i++ {
		e.off = e.offFor > 0
		if e.offFor > 0 {
			e.offFor--
		}
		r := e.rng.Intn(total)
		for _, o := range ops {
			if r < o.w {
				o.fn()
				break
			}
			r -= o.w
		}
	}
}

// summary logs what the seed exercised.
func (e *lawEnv) summary() {
	counts := map[string]int{}
	for _, l := range e.trace {
		k, _, _ := strings.Cut(l, " ")
		if strings.HasPrefix(l, "pass lost") && !strings.HasSuffix(l, ": ok") {
			k = "failed-pass"
		}
		counts[k]++
	}
	e.t.Logf("seed %d: %v; contributions %s", e.seed, counts, e.scalar(`SELECT count(*)::text FROM resource_contribution`))
	if os.Getenv("FLEET_LAW_TRACE") != "" {
		e.t.Log(strings.Join(e.trace, "\n"))
	}
}

func (e *lawEnv) drained() bool {
	return e.scalar(`SELECT (NOT EXISTS(SELECT 1 FROM resource_work) AND NOT EXISTS(SELECT 1 FROM resource_metadata_repair)
	 AND NOT EXISTS(SELECT 1 FROM resource_build WHERE NOT ready)
	 AND NOT EXISTS(SELECT 1 FROM definition d WHERE kind='project' AND NOT EXISTS(SELECT 1 FROM resource_build b WHERE b.project_id=d.id)))::text`) == "true"
}

// quiesce disarms every fault, turns the processor on and runs passes past every backoff until nothing is due.
func (e *lawEnv) quiesce() {
	e.t.Helper()
	e.owner(`DELETE FROM law_fault`)
	e.lostNext, e.off, e.offFor = false, false, 0
	e.log("quiesce")
	for i := 0; i < 200; i++ {
		if e.drained() {
			return
		}
		e.advance(2 * lawMax)
		e.pass()
	}
	if !e.drained() {
		e.violate("L4", "livelock: not drained after 200 passes (work %s, repairs %s, unready builds %s)",
			e.scalar(`SELECT count(*)::text FROM resource_work`), e.scalar(`SELECT count(*)::text FROM resource_metadata_repair`),
			e.scalar(`SELECT coalesce(string_agg(project_id||':'||phase||':'||attempts,','),'') FROM resource_build WHERE NOT ready`))
	}
}

// final checks the laws that hold at quiescence.
func (e *lawEnv) final() {
	e.t.Helper()
	e.checkProgress()
	e.checkFrozen()
	e.checkExactlyOnce()
	e.checkSupport()
	// L5 at quiescence: the rules pass closes every resource question.
	for i := 0; i < problemCleanRuns+1; i++ {
		if err := rulesPass(e.ctx, e.a); err != nil {
			e.violate("L5", "rules pass: %v", err)
		}
	}
	if n := e.scalar(`SELECT count(*)::text FROM question WHERE cause LIKE 'problem:resource:%' AND state='open'`); n != "0" {
		e.violate("L5", "%s resource problems open at quiescence", n)
	}
	e.checkProblems()
}

// L1: one contribution per source execution, matching the source's fingerprint and frozen method; per-project headers
// equal the independent rollup projectResources.
func (e *lawEnv) checkExactlyOnce() {
	e.t.Helper()
	if n := e.scalar(`SELECT count(*)::text FROM resource_contribution c WHERE NOT EXISTS(SELECT 1 FROM execution x WHERE x.id=c.execution_id)`); n != "0" {
		e.violate("L1", "%s contributions of deleted executions", n)
	}
	for _, id := range e.executions() {
		var raw []byte
		var stored, method, frozen *string
		err := e.st.Tx(e.ctx, nil, false, NOLOCK, func(tx pgx.Tx) error {
			var err error
			if raw, err = lawSource(e.ctx, tx, id); err != nil {
				return err
			}
			err = tx.QueryRow(e.ctx, `SELECT c.source_sha,c.method_sha,v.method_sha FROM resource_contribution c LEFT JOIN resource_valuation v USING(execution_id) WHERE c.execution_id=$1`, id).Scan(&stored, &method, &frozen)
			if errors.Is(err, pgx.ErrNoRows) {
				return nil
			}
			return err
		})
		if err != nil {
			e.t.Fatal(err)
		}
		if stored == nil {
			e.violate("L1", "execution %s has no contribution", id)
		}
		var s resourceSnapshot
		if err := json.Unmarshal(raw, &s); err != nil {
			e.t.Fatal(err)
		}
		if sha := resourceSHA(s.Fingerprint); sha != *stored {
			e.violate("L1", "contribution of %s is stale: source %s, contribution %s", id, sha, *stored)
		}
		if frozen != nil && *frozen != *method {
			e.violate("L1", "contribution of %s uses method %s, frozen %s", id, *method, *frozen)
		}
	}
	for _, p := range lawProjects {
		var h contract.PreparedHeaderV1
		var stored []byte
		var roll contract.ResourceSummary
		err := e.st.Tx(e.ctx, nil, false, NOLOCK, func(tx pgx.Tx) error {
			q := backgroundRequest(e.ctx, e.a, tx)
			var err error
			if h, err = resourcePreparedHeader(q, p); err != nil {
				return err
			}
			if err = tx.QueryRow(e.ctx, `SELECT header FROM resource_project_summary WHERE project_id=$1`, p).Scan(&stored); err != nil {
				return err
			}
			roll, err = projectResources(q, p)
			return err
		})
		if err != nil {
			e.t.Fatal(err)
		}
		var published contract.PreparedHeaderV1
		if err := json.Unmarshal(stored, &published); err != nil || !reflect.DeepEqual(published, h) {
			e.violate("L1", "project %s published header %s differs from its support %s (%v)", p, stored, jsonBytes(h), err)
		}
		type cmp struct {
			Executions          int64
			Tokens              contract.TokenBreakdown
			Calls, Failed, Wall any
		}
		got := cmp{h.Executions, h.Tokens, h.Tools.TotalCalls, h.Tools.FailedCalls, h.Durations.ExecutionWallMs}
		want := cmp{roll.Executions, roll.Tokens, roll.Tools.TotalCalls, roll.Tools.FailedCalls, roll.Durations.ExecutionWallMs}
		if g, w := string(jsonBytes(got)), string(jsonBytes(want)); g != w {
			e.violate("L1", "project %s header %s, independent rollup %s", p, g, w)
		}
	}
}

var errLawRollback = errors.New("law replay rolled back")

// L2: support tables equal resourceDelta replayed over the stored contributions.
func (e *lawEnv) checkSupport() {
	e.t.Helper()
	support := func(q *Request, p string) (string, error) {
		var out string
		err := q.tx.QueryRow(q.ctx(), `SELECT concat_ws(E'\n',
		 (SELECT string_agg(concat_ws(' ',metric,basis,present_count,expected_count,trim_scale(sum_value),reasons),E'\n' ORDER BY metric,basis) FROM resource_metric WHERE project_id=$1 AND (present_count>0 OR expected_count>0 OR sum_value<>0 OR reasons<>'{}')),
		 (SELECT string_agg(concat_ws(' ',metric,value,multiplicity),E'\n' ORDER BY metric,value) FROM resource_extreme WHERE project_id=$1),
		 (SELECT string_agg(concat_ws(' ',connection,name,calls,errors,references_count),E'\n' ORDER BY connection,name) FROM resource_tool WHERE project_id=$1),
		 (SELECT string_agg(concat_ws(' ',currency,basis,trim_scale(value),usages,references_count),E'\n' ORDER BY currency,basis) FROM resource_cost WHERE project_id=$1),
		 (SELECT string_agg(concat_ws(' ',currency,basis,price_id,observed_at,references_count),E'\n' ORDER BY currency,basis,price_id,observed_at) FROM resource_price_ref WHERE project_id=$1),
		 (SELECT concat_ws(' ',executions,history_incomplete,other_calls,other_errors,tool_count,cost_count,price_count) FROM resource_project_summary WHERE project_id=$1))`, p).Scan(&out)
		return out, err
	}
	for _, p := range lawProjects {
		var actual, replayed string
		err := runTxLocksOnce(e.ctx, e.a, nil, true, []string{"project:" + p}, func(q *Request) error {
			var err error
			if actual, err = support(q, p); err != nil {
				return err
			}
			for _, s := range []string{`DELETE FROM resource_metric WHERE project_id=$1`, `DELETE FROM resource_extreme WHERE project_id=$1`, `DELETE FROM resource_tool WHERE project_id=$1`,
				`DELETE FROM resource_cost WHERE project_id=$1`, `DELETE FROM resource_price_ref WHERE project_id=$1`,
				`UPDATE resource_project_summary SET executions=0,history_incomplete=0,other_calls=0,other_errors=0,tool_count=0,cost_count=0,price_count=0 WHERE project_id=$1`} {
				if err := exec(q, s, p); err != nil {
					return err
				}
			}
			rs, err := q.tx.Query(q.ctx(), `SELECT facts FROM resource_contribution WHERE project_id=$1 ORDER BY execution_id`, p)
			if err != nil {
				return err
			}
			facts, err := pgx.CollectRows(rs, pgx.RowTo[[]byte])
			if err != nil {
				return err
			}
			d := newResourceDelta(p)
			for _, f := range facts {
				var c contract.ResourceContributionV1
				if err := json.Unmarshal(f, &c); err != nil {
					return err
				}
				if err := d.add(c, 1); err != nil {
					return err
				}
			}
			if err := d.flush(q); err != nil {
				return err
			}
			if replayed, err = support(q, p); err != nil {
				return err
			}
			return errLawRollback
		})
		if !errors.Is(err, errLawRollback) {
			e.t.Fatalf("seed %d: support replay of %s: %v", e.seed, p, err)
		}
		if actual != replayed {
			e.violate("L2", "project %s support differs from its contributions replayed:\nstored   %s\nreplayed %s", p, actual, replayed)
		}
	}
}

func lawSeeds() []int64 {
	if s := os.Getenv("FLEET_LAW_SEED"); s != "" {
		n, _ := strconv.ParseInt(s, 10, 64)
		return []int64{n}
	}
	n := 20
	if s := os.Getenv("FLEET_LAW_SEEDS"); s != "" {
		n, _ = strconv.Atoi(s)
	}
	out := make([]int64, n)
	for i := range out {
		out[i] = int64(i + 1)
	}
	return out
}

// TestLaw: every seed's random run keeps L1-L5 and L7; L6 bounds a poisoned item's retries in fake time.
// Requirement: R1-R8 of DESIGN_RULES.md hold on the real engine; risk: a change passes its own tests but breaks
// exactly-once, atomicity, progress or bounded retry somewhere a hand-written test does not look.
func TestLaw(t *testing.T) {
	defer lawConfigure(lawBase, lawMax)()
	start := time.Now()
	// Seeds run in parallel: each has its own database, kit and clock.
	t.Run("seeds", func(t *testing.T) {
		for _, seed := range lawSeeds() {
			t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				e := newLawEnv(t, ctx, seed)
				e.run(150)
				e.quiesce()
				e.final()
				e.summary()
			})
		}
		for _, failWrite := range []bool{false, true} {
			t.Run(fmt.Sprintf("L6/failWrite=%v", failWrite), func(t *testing.T) {
				t.Parallel()
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
				defer cancel()
				e := newLawEnv(t, ctx, 6)
				lawBoundedRetry(e, failWrite)
				e.quiesce()
				e.final()
			})
		}
	})
	t.Logf("laws took %s", time.Since(start).Round(time.Millisecond))
}

// L6: a poisoned item steps at most log2(max/base) + T/(0.75*max) + 1 times in fake time T, each pause within its
// backoff bounds; with failWrite its failure write fails too.
func lawBoundedRetry(e *lawEnv, failWrite bool) {
	e.t.Helper()
	e.owner(`INSERT INTO execution(id,kind,state,project_id,data) VALUES('poison','tool_run','succeeded','LA','{"tool":"t1"}')`)
	e.owner(`INSERT INTO law_fault VALUES(1,'resource_contribution','poison',1,'error',1000000)`)
	if failWrite {
		e.owner(`INSERT INTO law_fault VALUES(2,'resource_work','poison',1,'error',1000000)`)
	}
	e.faults = 2
	hits := func() int {
		n, _ := strconv.Atoi(e.scalar(`SELECT CASE WHEN is_called THEN last_value ELSE 0 END::text FROM law_hit_1`))
		return n
	}
	// T is the processor clock's own elapsed time, so a slow machine (real time also moves that clock) cannot widen the bound.
	start := lawNow(e)
	steps := 0
	for elapsed := time.Duration(0); elapsed < 20*lawMax; elapsed += lawBase {
		e.advance(lawBase)
		e.pass()
		if h := hits(); h > steps {
			steps = h
			if failWrite {
				continue
			}
			var attempts int
			var left float64
			if err := e.ownerDB.QueryRow(e.ctx, `SELECT max(attempts),extract(epoch FROM max(next_attempt_at)-$1)::float8 FROM resource_work WHERE execution_id='poison'`, lawNow(e)).Scan(&attempts, &left); err != nil {
				e.t.Fatal(err)
			}
			if attempts < 1 {
				e.violate("L6", "failure %d of the poisoned item recorded %d attempts", steps, attempts)
			}
			step := math.Min(float64(lawBase)*math.Pow(2, float64(attempts-1)), float64(lawMax)) / float64(time.Second)
			low, high := step, math.Min(step*1.5, float64(lawMax)/float64(time.Second))
			if step >= float64(lawMax)/float64(time.Second) {
				low = step * 0.75
			}
			if left < low-0.3 || left > high+0.3 {
				e.violate("L6", "failure %d (attempts %d) paused %.3fs, bounds [%.3f, %.3f]", steps, attempts, left, low, high)
			}
		}
	}
	T := lawNow(e).Sub(start)
	bound := math.Log2(float64(lawMax)/float64(lawBase)) + float64(T)/(0.75*float64(lawMax)) + 1
	e.log("L6 failWrite=%v: %d steps in %s, bound %.1f", failWrite, steps, T, bound)
	e.t.Logf("L6 failWrite=%v: %d steps in %s of fake time, bound %.1f", failWrite, steps, T, bound)
	if float64(steps) > bound {
		e.violate("L6", "poisoned item stepped %d times in %s of fake time, bound %.1f", steps, T, bound)
	}
	if steps < 2 {
		e.violate("L6", "poisoned item stepped only %d times", steps)
	}
}

// lawMutant: a planted defect TestLawMutants applies with go test -overlay; some law must fail on it.
type lawMutant struct {
	ID, Defect, File, Find, Replace string
}

// TestLawMutants: each planted defect in testdata/mutants fails TestLaw (the tree stays unchanged).
func TestLawMutants(t *testing.T) {
	if os.Getenv("FLEET_LAW_MUTANT") != "" {
		t.Skip("inside a mutant run")
	}
	ensureTestPostgres(t)
	files, err := filepathGlob("testdata/mutants/*.json")
	if err != nil || len(files) == 0 {
		t.Fatal("no mutants", err)
	}
	sort.Strings(files)
	var wg sync.WaitGroup
	results := make([]string, len(files))
	for i, f := range files {
		var m lawMutant
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			t.Fatal(f, err)
		}
		src, err := os.ReadFile(m.File)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(src), m.Find); n != 1 {
			t.Fatalf("mutant %s is stale: its text occurs %d times in %s", m.ID, n, m.File)
		}
		dir := t.TempDir()
		mutated := dir + "/" + m.File
		if err := os.WriteFile(mutated, []byte(strings.Replace(string(src), m.Find, m.Replace, 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		abs, _ := filepathAbs(m.File)
		overlay := dir + "/overlay.json"
		if err := os.WriteFile(overlay, jsonBytes(map[string]any{"Replace": map[string]string{abs: mutated}}), 0o644); err != nil {
			t.Fatal(err)
		}
		wg.Add(1)
		go func(i int, m lawMutant) {
			defer wg.Done()
			out, err := runGoTest(overlay, m.ID)
			switch {
			case err == nil:
				results[i] = fmt.Sprintf("%s survived (%s)", m.ID, m.Defect)
			case !strings.Contains(out, "LAW VIOLATION"):
				results[i] = fmt.Sprintf("%s failed without a law violation:\n%s", m.ID, outputTail(out))
			default:
				law := out[strings.Index(out, "LAW VIOLATION")+len("LAW VIOLATION "):]
				t.Logf("%s killed by %s", m.ID, strings.SplitN(law, ":", 2)[0])
			}
		}(i, m)
	}
	wg.Wait()
	for _, r := range results {
		if r != "" {
			t.Error(r)
		}
	}
}
