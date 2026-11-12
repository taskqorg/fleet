package main

import (
	"context"
	_ "embed"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store: one pool of the coordinator role with the operator scope '*' as a startup option (docs/designs/fleet-data-foundation.md §12).
// A transaction of a person other than the owner sets fleet.project_ids and fleet.person for itself (SET LOCAL).
type Store struct {
	pool  *pgxpool.Pool
	locks sync.Map // scope key -> *sync.Mutex
}

// Scope: nil is the coordinator's own '*' scope (the owner, hosts).
type Scope struct{ ProjectIDs, Person string }

// actorKey: the context value naming the agent execution of a call (execution:<id>); every transaction of that call sets
// fleet.actor from it, which fleet_change() records as change.actor (docs/designs/fleet-pm-agent.md §1).
type actorKey struct{}

// Scope keys. Lock order: GLOBAL before any execution scope; an execution-scope writer never takes GLOBAL.
const GLOBAL = "fleet:write"

type HTTPError struct {
	Code  int
	Msg   string
	Extra map[string]any // more fields of the error body (a 409's DefinitionConflict)
	// Refused marks a refusal by design (a removed host): its span outcome is refused, not error.
	Refused bool
}

func (e *HTTPError) Error() string    { return e.Msg }
func fail(code int, msg string) error { return &HTTPError{Code: code, Msg: msg} }

var errNotFound = fail(404, "Not found")

func openStore(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.ConnConfig.RuntimeParams["options"] = "-c fleet.project_ids=*"
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return &Store{pool: p}, nil
}

func (s *Store) mutex(key string) *sync.Mutex {
	// ponytail: one mutex per scope key, never freed; prune by execution end if the map ever shows in memory.
	m, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	return m.(*sync.Mutex)
}

func retryable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01")
}

// Tx runs fn in one transaction. write: READ COMMITTED holding the scope lock (in-process queue, then
// pg_advisory_xact_lock on the same key for other processes); read: a READ ONLY REPEATABLE READ snapshot.
// Serialization failures and deadlocks are retried with a short randomized backoff (5 ms .. 200 ms).
func (s *Store) Tx(ctx context.Context, sc *Scope, write bool, lock string, fn func(pgx.Tx) error) error {
	if write {
		if lock == "" {
			lock = GLOBAL
		}
		m := s.mutex(lock)
		m.Lock()
		defer m.Unlock()
	}
	for attempt := 0; ; attempt++ {
		err := s.once(ctx, sc, write, lock, fn)
		if err == nil || !retryable(err) {
			return err
		}
		time.Sleep(time.Duration(rand.Float64() * float64(min(200, 5<<min(attempt, 6))) * float64(time.Millisecond)))
	}
}

func (s *Store) once(ctx context.Context, sc *Scope, write bool, lock string, fn func(pgx.Tx) error) error {
	opts := pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	if !write {
		opts = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	}
	var t pgx.Tx
	err := pgx.BeginTxFunc(ctx, s.pool, opts, func(tx pgx.Tx) error {
		t = tx
		if a, _ := ctx.Value(actorKey{}).(string); a != "" {
			if _, err := tx.Exec(ctx, "SELECT set_config('fleet.actor',$1,true)", a); err != nil {
				return err
			}
		}
		if sc != nil {
			if _, err := tx.Exec(ctx, "SELECT set_config('fleet.project_ids',$1,true),set_config('fleet.person',$2,true)", sc.ProjectIDs, sc.Person); err != nil {
				return err
			}
		}
		if write {
			if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", lock); err != nil {
				return err
			}
		}
		return fn(tx)
	})
	if hooks, _ := endHooks.LoadAndDelete(t); hooks != nil {
		for _, h := range *hooks.(*[]func(bool)) {
			h(err == nil)
		}
	}
	return err
}

// endHooks: effects outside the database (files) registered by onEnd, keyed by their transaction.
var endHooks sync.Map

// onEnd runs fn when tx has ended, in the caller's goroutine: committed true only after a confirmed commit. A retried
// attempt ends with committed false before the next attempt runs.
func onEnd(tx pgx.Tx, fn func(committed bool)) {
	v, _ := endHooks.LoadOrStore(tx, &[]func(bool){})
	hooks := v.(*[]func(bool))
	*hooks = append(*hooks, fn)
}

var migrationSplit = regexp.MustCompile(`(?m)^-- migration (\d+)`)

// migrate applies schema.sql as the owner role: each "-- migration N" section once, in order, in one transaction that takes
// the migration lock before it checks schema_migration, so concurrent starts never run a section twice.
// Migration 6's JavaScript document move is a no-op on an empty database and is not ported.
func migrate(ctx context.Context, url string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migration(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	text := []byte(schemaSQL)
	idx := migrationSplit.FindAllSubmatchIndex(text, -1)
	for i, m := range idx {
		version, _ := strconv.Atoi(string(text[m[2]:m[3]]))
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		body := string(text[m[0]:end])
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", GLOBAL); err != nil {
				return err
			}
			// Decided under the lock: another coordinator may have applied this version while this one waited for it.
			var applied bool
			if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM schema_migration WHERE version=$1)", version).Scan(&applied); err != nil || applied {
				return err
			}
			// Simple protocol: the whole section runs as one multi-statement query inside the transaction.
			if _, err := tx.Exec(ctx, body, pgx.QueryExecModeSimpleProtocol); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migration(version) VALUES($1)", version)
			return err
		})
		if err != nil {
			return errors.New("migration " + strconv.Itoa(version) + ": " + err.Error())
		}
	}
	return nil
}

// unscoped runs fn with the transaction's row scope widened to '*' and restores the person's scope after it:
// the coordinator's own host and account choice inside a person's request sees every row,
// and filters in code (people-access §2).
func unscoped[T any](q *Request, fn func() (T, error)) (T, error) {
	var zero T
	if q.tx == nil || q.person == nil || fullAccess(q.person) {
		return fn()
	}
	var ids, person string
	if err := q.tx.QueryRow(q.ctx(), "SELECT coalesce(current_setting('fleet.project_ids',true),''),coalesce(current_setting('fleet.person',true),'')").Scan(&ids, &person); err != nil {
		return zero, err
	}
	if _, err := q.tx.Exec(q.ctx(), "SELECT set_config('fleet.project_ids','*',true),set_config('fleet.person','',true)"); err != nil {
		return zero, err
	}
	out, err := fn()
	if _, rerr := q.tx.Exec(q.ctx(), "SELECT set_config('fleet.project_ids',$1,true),set_config('fleet.person',$2,true)", ids, person); rerr != nil && err == nil {
		err = rerr
	}
	return out, err
}

// Helpers of connections, pipelines and Lab: background requests, transactions, typed rows.

// runningApp: the running coordinator, set at start (background work and snapshot publishing need the store).
var runningApp *App

func labDir() string { return envDir("FLEET_LAB_DIRECTORY", "config/lab") }

// numOK: a JSON number or a SQL integer column (pgx scans integer as int32, smallint as int16).
func numOK(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case float32:
		return float64(x), true
	case int64:
		return float64(x), true
	case int32:
		return float64(x), true
	case int16:
		return float64(x), true
	case int:
		return float64(x), true
	}
	return 0, false
}

// num: a finite number of any JSON or SQL integer or float type, or a numeric text (a ->> read); 0 otherwise. numOK
// refuses text. A SQL numeric column needs ::float8 (pgx scans it as pgtype.Numeric).
func num(v any) float64 {
	f, ok := numOK(v)
	if s, isText := v.(string); isText {
		f, ok = parseNum(s)
	}
	if !ok || math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

func parseNum(s string) (float64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil
}

// msTime: a millisecond time (JSON or SQL number) as a timestamp, or nil.
func msTime(v any) any {
	f, ok := numOK(v)
	if !ok || math.IsInf(f, 0) || math.IsNaN(f) {
		return nil
	}
	return time.UnixMilli(int64(f)).UTC()
}

func intOK(v any) (int64, bool) {
	f, ok := numOK(v)
	if !ok || f != float64(int64(f)) || f > 1<<53 || f < -(1<<53) {
		return 0, false
	}
	return int64(f), true
}

func keysOnly(b doc, allowed ...string) bool {
	for k := range b {
		ok := false
		for _, a := range allowed {
			if k == a {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// queryRows: rows as maps (column name -> value); jsonb decodes to JSON values, timestamps to time.Time.
func queryRows(q *Request, sql string, args ...any) ([]doc, error) {
	rs, err := q.tx.Query(q.ctx(), sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

func ownerEmailOf(a *App) string {
	for _, p := range a.peopleSnapshot().list() {
		if p.Owner {
			return p.Email
		}
	}
	return "owner"
}

// director: the request person, else the owner (timers, hosts).
func directorOf(q *Request) string {
	if q.person != nil {
		return q.person.Email
	}
	return ownerEmailOf(q.app)
}

// runTx: fn in one store transaction as a request: as person q (its scope; q.tx set for fn), or as the coordinator's
// background work when q is nil. A write takes lock (GLOBAL when empty).
func runTx(ctx context.Context, a *App, q *Request, write bool, lock string, fn func(q *Request) error) error {
	if q == nil {
		return a.store.Tx(ctx, nil, write, lock, func(tx pgx.Tx) error { return fn(backgroundRequest(ctx, a, tx)) })
	}
	return a.store.Tx(ctx, scopeOf(q.person), write, lock, func(tx pgx.Tx) error {
		q.tx = tx
		defer func() { q.tx = nil }()
		return fn(q)
	})
}

// backgroundRequest: a request value for background work (no person: the coordinator's own scope).
func backgroundRequest(ctx context.Context, a *App, tx pgx.Tx) *Request {
	hr, _ := http.NewRequestWithContext(ctx, "POST", "/", nil)
	return &Request{r: hr, tx: tx, app: a}
}

// askRow inserts one question; returns its id.
func askRow(q *Request, kind, text string, projectID any, options any, askedBy string, data doc) (string, error) {
	id := newID()
	var opts any
	if options != nil {
		opts = jsonBytes(options)
	}
	if data == nil {
		data = doc{}
	}
	err := exec(q, "INSERT INTO question(id,kind,project_id,text,options,asked_by,data) VALUES($1,$2,$3,$4,$5,$6,$7)", id, kind, projectID, text, opts, askedBy, jsonBytes(data))
	return id, err
}

// insertExecution inserts a typed execution row unless its id or request_id exists.
func insertExecution(q *Request, v doc) (bool, error) {
	state := astr(v["state"])
	if state == "" {
		state = "preparing"
	}
	var cfg any
	if v["configRef"] != nil {
		cfg = jsonBytes(v["configRef"])
	}
	data := v["data"]
	if data == nil {
		data = doc{}
	}
	var id string
	err := q.tx.QueryRow(q.ctx(), `INSERT INTO execution(id,kind,state,project_id,parent_execution_id,config_ref,data,experiment_id,variant,request_id)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING RETURNING id`,
		v["id"], v["kind"], state, v["projectId"], v["parentExecutionId"], cfg, jsonBytes(data), v["experimentId"], v["variant"], v["requestId"]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// writeConnectionCost upserts the connection_cost total of one tool_run.
func writeConnectionCost(q *Request, id string, projectID any, cost any) error {
	basis := "measured"
	var c any
	if cost == nil {
		basis = "unavailable"
	} else {
		c = jsonBytes(cost)
	}
	return exec(q, `INSERT INTO execution_totals(execution_id,project_id,connection_cost,connection_cost_basis) VALUES($1,$2,$3,$4)
	 ON CONFLICT(execution_id) DO UPDATE SET connection_cost=excluded.connection_cost,connection_cost_basis=excluded.connection_cost_basis,computed_at=now()`, id, projectID, c, basis)
}

func sortedKeysOf(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

func msOf(t any) any {
	if tt, ok := t.(time.Time); ok {
		return tt.UnixMilli()
	}
	return nil
}

// The schema is part of the binary: schema.sql next to this file is the schema source (committed).

//go:embed schema.sql
var schemaSQL string
