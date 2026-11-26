package main

import (
	"context"
	"crypto/sha256"
	"embed"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store: one pool of the coordinator role with the operator scope '*' as a startup option (docs/designs/fleet-data-foundation.md §12).
// A transaction of a person other than the owner sets fleet.project_ids and fleet.person for itself (SET LOCAL).
type Store struct {
	pool           *pgxpool.Pool
	readPool       *pgxpool.Pool
	app            *App
	authMu         sync.Mutex
	authWrites     int
	authGeneration uint64
	authPeople     map[string]*Person
	authHosts      map[string]authHost
}

// Scope: nil is the coordinator's own '*' scope (the owner, hosts).
type Scope struct{ ProjectIDs, Person string }

// actorKey: the context value naming the agent execution of a call (execution:<id>); every transaction of that call sets
// fleet.actor from it, which fleet_change() records as change.actor (docs/designs/fleet-pm-agent.md §1).
type actorKey struct{}

// showDeletedKey: the context value of an executor-internal host call (true); its transactions set fleet.deleted=on
// (migration 67), so the stop and teardown of deleted work are still delivered and their results land.
type showDeletedKey struct{}

// deletedHostRoutes: the host routes that see deleted rows. Every other host route (agent tools, project operations,
// child runs, artifact reads and transfers) reads as everyone does: a late upload of deleted work gets 404 and the
// executor sets it aside.
var deletedHostRoutes = map[string]bool{"/api/hosts/poll": true, "/api/hosts/receipts": true, "/api/hosts/events": true, "/api/hosts/spans": true}

// Canonical advisory-lock hierarchy. Within any transaction, requested locks are sorted and deduplicated by (rank, key),
// and dynamic lock acquisition rejects any lock ordered before or equal to a lock already held in the transaction:
//
//	GLOBAL < DISPATCH < project:* < bot:* < task:* < bot-subject:*, session-subject:* < session:* < experiment:* < execution:* < host:* < usageKey:* < person-setting:* < budget:* < problems:* < board:*
const (
	GLOBAL   = "fleet:write"
	DISPATCH = "dispatch"
	NOLOCK   = "-"
)

func lockRank(key string) (int, error) {
	switch {
	case key == GLOBAL:
		return 0, nil
	case key == DISPATCH:
		return 1, nil
	case strings.HasPrefix(key, "project:") && len(key) > len("project:"):
		return 2, nil
	case strings.HasPrefix(key, "bot:") && len(key) > len("bot:"):
		return 3, nil
	case strings.HasPrefix(key, "task:") && len(key) > len("task:"):
		return 4, nil
	case strings.HasPrefix(key, "bot-subject:") && len(key) > len("bot-subject:"):
		// The one conversation per bot and subject (botConversation): taken before the session it creates.
		return 5, nil
	case strings.HasPrefix(key, "session-subject:") && len(key) > len("session-subject:"):
		// The one open conversation per agent and subject (sessionStart): taken before the session it creates.
		return 5, nil
	case strings.HasPrefix(key, "session:") && len(key) > len("session:"):
		return 6, nil
	case strings.HasPrefix(key, "experiment:") && len(key) > len("experiment:"):
		return 7, nil
	case strings.HasPrefix(key, "execution:") && len(key) > len("execution:"):
		return 8, nil
	case strings.HasPrefix(key, "host:") && len(key) > len("host:"):
		return 9, nil
	case strings.HasPrefix(key, "usageKey:") && len(key) > len("usageKey:"):
		return 10, nil
	case strings.HasPrefix(key, "person-setting:") && len(key) > len("person-setting:"):
		return 11, nil
	case strings.HasPrefix(key, "budget:") && len(key) > len("budget:"):
		return 12, nil
	case strings.HasPrefix(key, "problems:") && len(key) > len("problems:"):
		return 13, nil
	case strings.HasPrefix(key, "board:") && len(key) > len("board:"):
		return 14, nil
	case strings.HasPrefix(key, "token-calls:") && len(key) > len("token-calls:"):
		return 15, nil
	default:
		return -1, fmt.Errorf("unknown advisory lock scope %q", key)
	}
}

func sortLocks(keys []string) ([]string, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	type ranked struct {
		rank int
		key  string
	}
	seen := make(map[string]struct{}, len(keys))
	items := make([]ranked, 0, len(keys))
	for _, k := range keys {
		if k == "" || k == NOLOCK {
			continue
		}
		if _, ok := seen[k]; ok {
			continue
		}
		r, err := lockRank(k)
		if err != nil {
			return nil, err
		}
		seen[k] = struct{}{}
		items = append(items, ranked{rank: r, key: k})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].rank != items[j].rank {
			return items[i].rank < items[j].rank
		}
		return items[i].key < items[j].key
	})
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.key
	}
	return out, nil
}

type txLockState struct {
	mu      sync.Mutex
	write   bool
	held    map[string]bool
	hasMax  bool
	maxRank int
	maxKey  string
}

var txLockStates sync.Map // pgx.Tx -> *txLockState

// acquireTxLocks acquires keys in canonical sorted order within tx using 64-bit hashtextextended($1, 0),
// skipping already-held keys and failing explicitly on any out-of-order re-locking attempt.
func acquireTxLocks(ctx context.Context, tx pgx.Tx, keys ...string) error {
	return acquireTxLocksMode(ctx, tx, false, keys...)
}

var errAdvisoryBusy = errors.New("advisory lock busy")

type advisoryBusyError string

func (e advisoryBusyError) Error() string { return fmt.Sprintf("%s: %q", errAdvisoryBusy, string(e)) }
func (e advisoryBusyError) Unwrap() error { return errAdvisoryBusy }

func tryAdvisoryLock(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	return db.New(tx).TryAdvisoryXactLock(ctx, key)
}

func acquireTxLocksMode(ctx context.Context, tx pgx.Tx, noWait bool, keys ...string) error {
	sorted, err := sortLocks(keys)
	if err != nil {
		return err
	}
	if len(sorted) == 0 {
		return nil
	}
	v, ok := txLockStates.Load(tx)
	if !ok {
		return errors.New("acquireTxLocks: transaction has no registered lock state")
	}
	st := v.(*txLockState)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.write {
		return fmt.Errorf("advisory lock %q cannot be acquired inside a read-only transaction", sorted[0])
	}
	pending := make([]string, 0, len(sorted))
	for _, k := range sorted {
		if !st.held[k] {
			pending = append(pending, k)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	firstRank, _ := lockRank(pending[0])
	if st.hasMax && (firstRank < st.maxRank || (firstRank == st.maxRank && pending[0] <= st.maxKey)) {
		return fmt.Errorf("advisory lock order violation: cannot acquire %q after %q", pending[0], st.maxKey)
	}
	for _, k := range pending {
		if noWait {
			held, err := tryAdvisoryLock(ctx, tx, k)
			if err != nil {
				return err
			}
			if !held {
				return advisoryBusyError(k)
			}
		} else if err := db.New(tx).AdvisoryXactLock(ctx, k); err != nil {
			return err
		}
		r, _ := lockRank(k)
		st.held[k] = true
		st.maxRank = r
		st.maxKey = k
		st.hasMax = true
	}
	return nil
}

// acquireNewObjectLock takes the lock of an object this transaction has just created (a new session's id): nobody
// else can know the key before the commit, so it cannot deadlock and skips the order check. A later acquire of it in
// the transaction is a no-op; the order of the other locks is unchanged.
func acquireNewObjectLock(q *Request, key string) error {
	if _, err := lockRank(key); err != nil {
		return err
	}
	v, ok := txLockStates.Load(q.tx)
	if !ok {
		return errors.New("acquireNewObjectLock: transaction has no registered lock state")
	}
	st := v.(*txLockState)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.held[key] {
		return nil
	}
	held, err := tryAdvisoryLock(q.ctx(), q.tx, key)
	if err != nil {
		return err
	}
	if !held {
		return advisoryBusyError(key)
	}
	st.held[key] = true
	return nil
}

func acquireTxLock(q *Request, keys ...string) error {
	return acquireTxLocks(q.ctx(), q.tx, keys...)
}

func txHasLock(tx pgx.Tx, key string) bool {
	if tx == nil {
		return false
	}
	v, ok := txLockStates.Load(tx)
	if !ok {
		return false
	}
	st := v.(*txLockState)
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.held[key]
}

func canAcquireTxLock(tx pgx.Tx, key string) bool {
	if tx == nil {
		return false
	}
	r, err := lockRank(key)
	if err != nil {
		return false
	}
	v, ok := txLockStates.Load(tx)
	if !ok {
		return false
	}
	st := v.(*txLockState)
	st.mu.Lock()
	defer st.mu.Unlock()
	if !st.write {
		return false
	}
	if st.held[key] {
		return true
	}
	return !st.hasMax || r > st.maxRank || (r == st.maxRank && key > st.maxKey)
}

func (e *HTTPError) Error() string    { return e.Msg }
func fail(code int, msg string) error { return &HTTPError{Code: code, Msg: msg} }

var errNotFound = fail(404, "Not found")

func openStore(ctx context.Context, url string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.MaxConns = 16
	// JIT compiles a plan to machine code when its estimated cost is high. Coordinator statements are short indexed
	// lookups whose estimates can still be large (FORCE RLS filters, LIMIT over big tables): measured on 1m-history data,
	// the processor's candidate selection ran in 1.2 ms and then spent 124 ms compiling, on every call. Off for every
	// connection of both Store pools; heavy analytics belong to ClickHouse.
	// Appended, so options given in the connection URL still apply.
	cfg.ConnConfig.RuntimeParams["options"] = strings.TrimSpace(cfg.ConnConfig.RuntimeParams["options"] + " -c fleet.project_ids=* -c jit=off")
	p, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	readCfg := cfg.Copy()
	readCfg.MaxConns = 16
	r, err := pgxpool.NewWithConfig(ctx, readCfg)
	if err != nil {
		p.Close()
		return nil, err
	}
	return &Store{pool: p, readPool: r}, nil
}

func (s *Store) Close() {
	if s.readPool != nil {
		s.readPool.Close()
	}
	if s.pool != nil {
		s.pool.Close()
	}
}

func retryable(err error) bool {
	var pe *pgconn.PgError
	return errors.As(err, &pe) && (pe.Code == "40001" || pe.Code == "40P01")
}

// txLocks runs fn in a transaction holding sorted locks. Every production write to person, person_grant,
// project initial_member or host holds GLOBAL, so authWrites/authGeneration fencing and authPeople/authHosts
// invalidation apply only to writes holding GLOBAL; scoped writes (host polls, messages, receipts, tasks) leave
// the auth cache and shared stream generations untouched.
func (s *Store) txLocks(ctx context.Context, sc *Scope, write bool, locks []string, retry bool, fn func(pgx.Tx) error) error {
	var sorted []string
	if write {
		var err error
		sorted, err = sortLocks(locks)
		if err != nil {
			return err
		}
		if slices.Contains(sorted, GLOBAL) {
			s.authMu.Lock()
			s.authWrites++
			s.authGeneration++
			s.authPeople = nil
			s.authHosts = nil
			s.authMu.Unlock()
			defer func() {
				s.authMu.Lock()
				s.authGeneration++
				s.authPeople = nil
				s.authHosts = nil
				s.authWrites--
				s.authMu.Unlock()
			}()
		}
	}
	for attempt := 0; ; attempt++ {
		err := s.once(ctx, sc, write, sorted, fn)
		if err == nil || !retry || !retryable(err) {
			return err
		}
		delay := time.NewTimer(time.Duration(rand.Float64() * float64(min(200, 5<<min(attempt, 6))) * float64(time.Millisecond)))
		select {
		case <-ctx.Done():
			delay.Stop()
			return ctx.Err()
		case <-delay.C:
		}
	}
}

func (s *Store) once(ctx context.Context, sc *Scope, write bool, sortedLocks []string, fn func(pgx.Tx) error) error {
	opts := pgx.TxOptions{IsoLevel: pgx.ReadCommitted}
	if !write {
		opts = pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	}
	var t pgx.Tx
	pool := s.pool
	if !write && s.readPool != nil {
		pool = s.readPool
	}
	err := pgx.BeginTxFunc(ctx, pool, opts, func(tx pgx.Tx) error {
		t = tx
		txLockStates.Store(tx, &txLockState{write: write, held: map[string]bool{}})
		q := db.New(tx)
		if a, _ := ctx.Value(actorKey{}).(string); a != "" {
			if err := q.TxSetActor(ctx, a); err != nil {
				return err
			}
		}
		if sc != nil {
			if err := q.TxSetScope(ctx, db.TxSetScopeParams{ProjectIds: sc.ProjectIDs, Person: sc.Person}); err != nil {
				return err
			}
		}
		if show, _ := ctx.Value(showDeletedKey{}).(bool); show {
			if err := q.TxShowDeleted(ctx, "on"); err != nil {
				return err
			}
		}
		if write && len(sortedLocks) > 0 {
			if err := acquireTxLocks(ctx, tx, sortedLocks...); err != nil {
				return err
			}
		}
		return fn(tx)
	})
	if t != nil {
		txLockStates.Delete(t)
	}
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

// migrateLocked runs fn in one transaction holding the migration lock (GLOBAL), so concurrent starts apply each step once.
func migrateLocked(ctx context.Context, conn *pgx.Conn, fn func(q *db.Queries, tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := q.AdvisoryXactLock(ctx, GLOBAL); err != nil {
			return err
		}
		return fn(q, tx)
	})
}

// The current definitions of functions, triggers and policies (S1): functions first, which the triggers and policies call.
//
//go:embed schema/*.sql
var schemaFS embed.FS

var schemaFileOrder = []string{"functions.sql", "triggers.sql", "policies.sql"}

// applySchemaFiles runs each schema/*.sql file whose content schema_file does not record yet, in one transaction under
// the migration lock, and records its hash: an unchanged file is not run again, so a start takes no table lock.
func applySchemaFiles(ctx context.Context, url string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_file(name text PRIMARY KEY,sha256 text NOT NULL,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	for _, name := range schemaFileOrder {
		body, err := schemaFS.ReadFile("schema/" + name)
		if err != nil {
			return err
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(body))
		err = migrateLocked(ctx, conn, func(q *db.Queries, tx pgx.Tx) error {
			if have, err := q.SchemaFileHash(ctx, name); err == nil && have == sum || err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
			// A policy is replaced under its table's exclusive lock: a start waits for no long reader past this; it fails
			// and the next start applies the file.
			if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '30s'"); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, string(body), pgx.QueryExecModeSimpleProtocol); err != nil {
				return err
			}
			return q.SchemaFileRecord(ctx, db.SchemaFileRecordParams{Name: name, Sha256: sum})
		})
		if err != nil {
			return errors.New("schema/" + name + ": " + err.Error())
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
	was, err := q.db().TxScope(q.ctx())
	if err != nil {
		return zero, err
	}
	if err := q.db().TxSetScope(q.ctx(), db.TxSetScopeParams{ProjectIds: "*", Person: ""}); err != nil {
		return zero, err
	}
	out, err := fn()
	if rerr := q.db().TxSetScope(q.ctx(), db.TxSetScopeParams{ProjectIds: was.ProjectIds, Person: was.Person}); rerr != nil && err == nil {
		err = rerr
	}
	return out, err
}

// Helpers of connections, pipelines and Lab: background requests, transactions, typed rows.

// runningApp: the running coordinator, set at start (background work and snapshot publishing need the store).
var runningApp *App

func labDir() string { return envDir("FLEET_LAB_DIRECTORY", "config/lab") }

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

// backgroundRequest: a request value for background work (no person: the coordinator's own scope).
func backgroundRequest(ctx context.Context, a *App, tx pgx.Tx) *Request {
	hr, _ := http.NewRequestWithContext(ctx, "POST", "/", nil)
	return &Request{r: hr, tx: tx, app: a}
}

// scopedRequest: a request value for non-HTTP callers acting under p's scope (stream, uploads).
func scopedRequest(ctx context.Context, a *App, p *Person) *Request {
	q := backgroundRequest(ctx, a, nil)
	q.person = p
	return q
}

// askRow inserts one question; returns its id. data is a struct of the question's data fields (nil: none).
func askRow(q *Request, kind, text string, projectID any, options any, askedBy string, data any) (string, error) {
	id := newID()
	var opts []byte
	if options != nil {
		opts = jsonBytes(options)
	}
	d := []byte("{}")
	if data != nil {
		d = jsonBytes(data)
	}
	var project *string
	if p, ok := projectID.(string); ok {
		project = &p
	}
	err := q.db().QuestionAsk(q.ctx(), db.QuestionAskParams{ID: id, Kind: db.QuestionKind(kind), ProjectID: project, Text: text, Options: opts, AskedBy: &askedBy, Data: d})
	return id, err
}

// executionTime accepts a time.Time directly or converts millisecond epochs via msTime.
func executionTime(v any) any {
	if t, ok := v.(time.Time); ok {
		return t.UTC()
	}
	return msTime(v)
}

// insertExecution inserts a typed execution row unless its id or request_id exists. v is the caller's record of it
// (executionNew names the fields read).
func insertExecution(q *Request, v any) (bool, error) {
	x, err := asT[executionNew](v)
	if err != nil {
		return false, err
	}
	parent := firstString(x.ParentExecutionID, x.PipelineStepID, x.PipelineRunID)
	if x.BotID == "" {
		if x.BotID, err = executionBotAttr(q, parent, x.SessionID); err != nil {
			return false, err
		}
	}
	data, traceErr := executionTraceData(q, parent, x.Data)
	if traceErr != nil {
		return false, traceErr
	}
	row := executionRow(x)
	row.Data = jsonBytes(data)
	return executionInsert(q, row)
}

// writeConnectionCost upserts the connection_cost total and measured duration of one tool_run.
func writeConnectionCost(q *Request, id string, cost any) error {
	if kind, err := lockExecutionForTotals(q, id); err != nil || kind == "" {
		return err
	}
	basis := "measured"
	var c []byte
	if cost == nil {
		basis = "unavailable"
	} else {
		c = jsonBytes(cost)
	}
	return q.db().ConnectionCostWrite(q.ctx(), db.ConnectionCostWriteParams{Cost: c, Basis: basis, ID: id})
}

func sortedKeysOf[V any](m map[string]V) []string {
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
