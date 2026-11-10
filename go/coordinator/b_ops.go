// Operation plumbing of area B: the person gate on every operation (app.ts preHandler) and runOp for the MCP tool servers.
package main

import (
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// bop registers an operation behind the architect gate and the project grant check.
func bop(name string, read bool, h handler) {
	operation(name, read, func(q *Request) (any, error) {
		if err := q.app.guardOp(q, name, q.body); err != nil {
			return nil, err
		}
		if len(q.body) == 0 || string(q.body) == "null" {
			q.body = json.RawMessage("{}")
		}
		return h(q)
	})
}

// runOp runs a named operation in its own transaction in the person's scope (q.tx is nil: called from a longRoute).
func (a *App) runOp(q *Request, name string, b json.RawMessage) (any, error) {
	if h, ok := longOperations[name]; ok { // area C: opens its own transactions
		sub := *q
		sub.body, sub.tx = b, nil
		return h(&sub)
	}
	op, ok := operations[name]
	if !ok {
		return nil, fail(404, "Unknown operation")
	}
	sub := *q
	sub.body = b
	var out any
	err := a.store.Tx(q.ctx(), scopeOf(q.person), !op.read, "", func(tx pgx.Tx) error {
		sub.tx = tx
		var err error
		out, err = op.h(&sub)
		return err
	})
	return out, err
}

// broute, blongRoute: route and longRoute behind the person gate of routes (architect routes, named project grants).
func broute(pattern string, h handler) {
	route(pattern, func(q *Request) (any, error) {
		if err := q.app.guardRoute(q); err != nil {
			return nil, err
		}
		return h(q)
	})
}

func blongRoute(pattern string, h handler) {
	longRoute(pattern, func(q *Request) (any, error) {
		if err := q.app.guardRoute(q); err != nil {
			return nil, err
		}
		return h(q)
	})
}
