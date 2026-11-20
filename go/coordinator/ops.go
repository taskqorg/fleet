// Operation plumbing: runOp, the one dispatcher of operations, the declared auth rule of the contract, and the person gate
// of legacy registrations.
package main

import (
	"encoding/json"
	"strings"

	"fleet/contract"

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

// toolReceipt: a host agent's tool call identity (agent_receipts.go); its write commits with its acknowledgment.
type toolReceipt struct {
	key       *string
	execution string
	payload   any // what the receipt hashes: the arguments as the caller bound them (nil: the body)
}

// runOp is the one dispatcher of operations: POST /api/ops/{name}, /api/project-operations/{name} and the MCP servers
// (/api/tools, /api/project-tools, /api/hosts/agent-tools). A long operation runs without a surrounding transaction (it
// opens its own); any other runs in its own transaction in the person's scope. Every call refreshes the person's access
// and passes the person gate (guardOp); an operation registered with op also passes its declared contract auth rule
// (declaredAuth) before its body is decoded. rc, when set, records a host agent's write with its receipt.
func (a *App) runOp(q *Request, name string, b json.RawMessage, rc *toolReceipt) (any, error) {
	reg, ok := operations[name]
	if !ok {
		return nil, fail(404, "Unknown operation")
	}
	sub := *q
	sub.body = b
	if h, ok := longOperations[name]; ok {
		sub.tx = nil
		if reg.declared {
			if err := a.authorizeOp(&sub, name); err != nil {
				return nil, err
			}
		}
		return h(&sub)
	}
	var out any
	err := a.store.Tx(q.ctx(), scopeOf(q.person), !reg.read, "", func(tx pgx.Tx) error {
		sub.tx = tx
		err := a.authorizeOp(&sub, name)
		if err != nil {
			return err
		}
		if rc != nil && !reg.read {
			if _, err = getExecution(&sub, rc.execution); err != nil {
				return err
			}
			payload := rc.payload
			if payload == nil {
				payload = b
			}
			out, err = agentReceipt(&sub, rc.key, rc.execution, name, payload, func() (any, error) { return reg.h(&sub) })
		} else {
			out, err = reg.h(&sub)
		}
		return err
	})
	if err == nil && !reg.read {
		a.wakeDispatch()
	}
	return out, err
}

// authorizeOp: the access refresh, the person gate and, for an operation registered with op, its declared auth rule.
func (a *App) authorizeOp(q *Request, name string) error {
	if err := refreshRequestAccess(q); err != nil {
		return err
	}
	if err := a.guardOp(q, name, q.body); err != nil {
		return err
	}
	if operations[name].declared {
		return declaredAuth(q, name)
	}
	return nil
}

// declaredAuth applies an operation's contract auth (contracts/ops-*.schema.json): architect needs the architect role,
// owner full access, holder:account the architect role or the caller's own subscription, project:<path> a grant of the
// project the request names at path (not visible: 404). holder:<kind> of another kind is checked by the handler, which
// reads the object. Full access passes every rule; a call without a person (hosts, timers) keeps full rights until
// stage 2 of docs/designs/fleet-standard.md refuses it unless the operation is system.
func declaredAuth(q *Request, name string) error {
	p := q.person
	if p == nil || fullAccess(p) {
		return nil
	}
	auth := contract.Operations[name].Auth
	switch {
	case auth == "architect":
		// A project's own definitions are read with its grant, which guardOp checked (fleet-definition-editors.md).
		if !hasRole(p, "architect") && !(projectDefinitionRead(name) && jsonPathString(q.body, "kind") == "project") {
			return fail(403, "Architect role required")
		}
	case auth == "owner":
		return fail(403, "Only the owner may call "+name)
	case auth == "holder:account":
		if !hasRole(p, "architect") && !q.app.ownAccount(q, name, q.body) {
			return fail(403, "Architect role required")
		}
	case strings.HasPrefix(auth, "project:"):
		if id := jsonPathString(q.body, strings.TrimPrefix(auth, "project:")); id != "" && !mayAccessProjectID(p, id) {
			return fail(404, "Unknown project")
		}
	}
	return nil
}

// jsonPathString: the string at a dotted path of a JSON object; "" when absent or not a string.
func jsonPathString(body json.RawMessage, path string) string {
	cur := body
	for _, k := range strings.Split(path, ".") {
		var m map[string]json.RawMessage
		if json.Unmarshal(cur, &m) != nil {
			return ""
		}
		cur = m[k]
	}
	var s string
	if json.Unmarshal(cur, &s) != nil {
		return ""
	}
	return s
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
