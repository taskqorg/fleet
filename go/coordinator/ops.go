// Operation plumbing: the person gate on every operation and runOp for the MCP tool servers.
package main

import (
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// gatedObjOp registers an operation behind the architect gate and the project grant check.
func gatedObjOp(name string, read bool, h handler) {
	operation(name, read, func(q *Request) (any, error) {
		if err := refreshRequestAccess(q); err != nil {
			return nil, err
		}
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
	return a.runAgentOp(q, name, b, nil, "")
}

func (a *App) runAgentOp(q *Request, name string, b json.RawMessage, key *string, execution string) (any, error) {
	if h, ok := longOperations[name]; ok { // opens its own transactions
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
		if err = refreshRequestAccess(&sub); err != nil {
			return err
		}
		if err = a.guardOp(&sub, name, b); err != nil {
			return err
		}
		if key != nil && !op.read {
			if _, err = getExecution(&sub, execution); err != nil {
				return err
			}
			out, err = agentReceipt(&sub, key, execution, name, b, func() (any, error) { return op.h(&sub) })
		} else {
			out, err = op.h(&sub)
		}
		return err
	})
	if err == nil && !op.read {
		a.wakeDispatch()
	}
	return out, err
}

// gatedRoute, gatedLongRoute: route and longRoute behind the person gate of routes (architect routes, named project grants).
func gatedRoute(pattern string, h handler) {
	route(pattern, func(q *Request) (any, error) {
		if err := refreshRequestAccess(q); err != nil {
			return nil, err
		}
		if err := q.app.guardRoute(q); err != nil {
			return nil, err
		}
		return h(q)
	})
}

func gatedLongRoute(pattern string, h handler) {
	longRoute(pattern, func(q *Request) (any, error) {
		if err := refreshRequestAccess(q); err != nil {
			return nil, err
		}
		if err := q.app.guardRoute(q); err != nil {
			return nil, err
		}
		return h(q)
	})
}
