package main

import (
	"context"
	"errors"
	"math/rand/v2"
	"regexp"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store: one pool of the coordinator role with the operator scope '*' as a startup option (packages/coordinator/store.ts).
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

func executionScope(id string) string { return "execution:" + id }

type HTTPError struct {
	Code int
	Msg  string
}

func (e *HTTPError) Error() string    { return e.Msg }
func fail(code int, msg string) error { return &HTTPError{code, msg} }

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
	return pgx.BeginTxFunc(ctx, s.pool, opts, func(tx pgx.Tx) error {
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
}

var migrationSplit = regexp.MustCompile(`(?m)^-- migration (\d+)`)

// migrate applies schema.sql as the owner role: each "-- migration N" section once, in order, in one transaction, recorded
// in schema_migration. Migration 6's JavaScript document move is a no-op on an empty database and is not ported.
func migrate(ctx context.Context, url string) error {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE TABLE IF NOT EXISTS schema_migration(version int PRIMARY KEY,applied_at timestamptz NOT NULL DEFAULT now())"); err != nil {
		return err
	}
	done := map[int]bool{}
	rows, _ := conn.Query(ctx, "SELECT version FROM schema_migration")
	vs, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return err
	}
	for _, v := range vs {
		done[int(v)] = true
	}
	text := []byte(schemaSQL)
	idx := migrationSplit.FindAllSubmatchIndex(text, -1)
	for i, m := range idx {
		version, _ := strconv.Atoi(string(text[m[2]:m[3]]))
		if done[version] {
			continue
		}
		end := len(text)
		if i+1 < len(idx) {
			end = idx[i+1][0]
		}
		body := string(text[m[0]:end])
		err := pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext($1))", GLOBAL); err != nil {
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
