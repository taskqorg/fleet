package main

// db_explain (docs/designs/fleet-data-layer.md, "Verify under RLS"): the plans of a view operation's statements as the
// app runs them. The view runs as the caller inside the operation's read-only transaction, under row level security,
// through a transaction that records each statement it sends; then each distinct statement is explained with
// ANALYZE and BUFFERS in a savepoint of the same transaction. The caller names a view, never SQL.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func init() {
	op("db_explain", dbExplain)
}

// dbExplainMax: the distinct statements explained; dbExplainTimeout: each EXPLAIN's statement_timeout.
const (
	dbExplainMax     = 60
	dbExplainTimeout = "20s"
)

type recordedStatement struct {
	sql   string
	args  []any
	calls int64
}

// recordingTx: a transaction that notes every statement sent through it, in order of first use, with its first
// arguments; batches and copies pass through unrecorded (views send neither).
type recordingTx struct {
	pgx.Tx
	mu    sync.Mutex
	order []string
	by    map[string]*recordedStatement
}

func (t *recordingTx) note(sql string, args []any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.by[sql]; s != nil {
		s.calls++
		return
	}
	t.by[sql] = &recordedStatement{sql: sql, args: append([]any(nil), args...), calls: 1}
	t.order = append(t.order, sql)
}

func (t *recordingTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	t.note(sql, args)
	return t.Tx.Query(ctx, sql, args...)
}

func (t *recordingTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	t.note(sql, args)
	return t.Tx.QueryRow(ctx, sql, args...)
}

func (t *recordingTx) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	t.note(sql, args)
	return t.Tx.Exec(ctx, sql, args...)
}

func dbExplain(q *Request, r *contract.DbExplainRequest) (contract.DbExplainReply, error) {
	out := contract.DbExplainReply{View: r.View, Statements: []contract.DbExplainStatement{}}
	if !contract.Operations[r.View].View || r.View == "db_explain" {
		return out, fail(400, "view: a view operation (contract view: true)")
	}
	reg, ok := operations[r.View]
	if !ok || !reg.read {
		return out, fail(400, "view: a view operation (contract view: true)")
	}
	body := json.RawMessage("{}")
	if r.Args != nil {
		body = jsonBytes(r.Args)
	}
	rec := &recordingTx{Tx: q.tx, by: map[string]*recordedStatement{}}
	// The recording transaction is the request's transaction to the store: the same lock state (read-only), and no
	// after-commit effects of its own (a diagnostic read drops any a view would register).
	if st, ok := txLockStates.Load(q.tx); ok {
		txLockStates.Store(rec, st)
	}
	defer func() { txLockStates.Delete(rec); endHooks.Delete(rec) }()
	sub := *q
	sub.body, sub.tx = body, rec
	if err := authorizeOp(&sub, r.View); err != nil {
		return out, err
	}
	start := time.Now()
	_, err := reg.h(&sub)
	out.HandlerMs = time.Since(start).Milliseconds()
	if err != nil {
		return out, err
	}
	if _, err := q.tx.Exec(q.ctx(), "SET LOCAL statement_timeout = '"+dbExplainTimeout+"'"); err != nil {
		return out, err
	}
	for i, sql := range rec.order {
		if i == dbExplainMax {
			break
		}
		s := rec.by[sql]
		st := contract.DbExplainStatement{Sql: sql, Calls: s.calls}
		st.Plan, err = explainOne(q, sql, s.args)
		if err != nil {
			st.Error = ptr(err.Error())
		}
		out.Statements = append(out.Statements, st)
	}
	return out, nil
}

// explainOne: EXPLAIN (ANALYZE, BUFFERS) of one recorded statement in a savepoint, rolled back, so a failure or a
// timeout leaves the transaction usable for the next one.
func explainOne(q *Request, sql string, args []any) (string, error) {
	sp, err := q.tx.Begin(q.ctx())
	if err != nil {
		return "", err
	}
	defer sp.Rollback(q.ctx())
	rows, err := sp.Query(q.ctx(), "EXPLAIN (ANALYZE, BUFFERS) "+sql, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var l string
		if err := rows.Scan(&l); err != nil {
			return "", err
		}
		lines = append(lines, l)
	}
	return strings.Join(lines, "\n"), rows.Err()
}
