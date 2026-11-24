// Operation plumbing: runOp, the one dispatcher of operations, and the declared auth rule of the contract (the only
// authorization rule of an operation).
package main

import (
	"encoding/json"
	"strings"

	"fleet/contract"
)

// toolReceipt: a host agent's tool call identity (agent_receipts.go); its write commits with its acknowledgment.
type toolReceipt struct {
	key       *string
	execution string
	payload   any // what the receipt hashes: the arguments as the caller bound them (nil: the body)
}

// runOp is the one dispatcher of operations: POST /api/ops/{name} and the MCP servers (/api/tools, /api/project-tools,
// /api/recovery-tools, /api/hosts/agent-tools). A long operation runs without a surrounding transaction (it opens its
// own); any other runs in its own transaction in the person's scope. Every call refreshes the person's access and passes
// the operation's declared auth rule (declaredAuth) before its body is decoded. rc, when set, records a host agent's
// write with its receipt.
func (a *App) runOp(q *Request, name string, b json.RawMessage, rc *toolReceipt) (any, error) {
	reg, ok := operations[name]
	if !ok {
		return nil, fail(404, "Unknown operation")
	}
	sub := *q
	sub.body = b
	if h, ok := longOperations[name]; ok {
		sub.tx = nil
		sub.accessTx = nil
		if err := authorizeOp(&sub, name); err != nil {
			return nil, err
		}
		return h(&sub)
	}
	if !reg.read || sub.tx == nil {
		sub.tx = nil
		sub.accessTx = nil
	}
	var out any
	run := func(sub *Request) error {
		err := authorizeOp(sub, name)
		if err != nil {
			return err
		}
		if rc != nil && !reg.read {
			if _, err = getExecution(sub, rc.execution); err != nil {
				return err
			}
			payload := rc.payload
			if payload == nil {
				payload = b
			}
			out, err = agentReceipt(sub, rc.key, rc.execution, name, payload, func() (any, error) { return reg.h(sub) })
		} else {
			out, err = reg.h(sub)
		}
		return err
	}
	var err error
	if reg.read {
		err = a.store.Read(q.ctx(), &sub, run)
	} else {
		lock := GLOBAL
		if l := opLocks[name]; l != nil {
			if v := l(b); v != "" {
				lock = v
			}
		}
		err = a.store.Write(q.ctx(), &sub, []string{lock}, run)
	}
	if err == nil && !reg.read {
		a.wakeDispatch()
	}
	return out, err
}

// opLocks: the entity lock of a write operation that does not take GLOBAL (lockRank). message_send takes its session's
// lock, so messages, host polls and receipts of other sessions run in parallel; an empty session id keeps GLOBAL and
// the operation's own check refuses it.
var opLocks = map[string]func(b json.RawMessage) string{
	"message_send": func(b json.RawMessage) string {
		if sid := jsonPathString(b, "sessionId"); sid != "" {
			return "session:" + sid
		}
		return ""
	},
	"work_plan_set": func(b json.RawMessage) string {
		if k, id := jsonPathString(b, "scopeKind"), jsonPathString(b, "scopeId"); k != "" && id != "" {
			return "board:" + k + ":" + id
		}
		return ""
	},
	"board_post": func(b json.RawMessage) string {
		if k, id := jsonPathString(b, "scopeKind"), jsonPathString(b, "scopeId"); k != "" && id != "" {
			return "board:" + k + ":" + id
		}
		return ""
	},
	"bot_settings_save": func(b json.RawMessage) string {
		if id := jsonPathString(b, "botId"); id != "" {
			return "bot:" + id
		}
		return ""
	},
	"bot_memory_save": func(b json.RawMessage) string {
		if id := jsonPathString(b, "botId"); id != "" {
			return "bot:" + id
		}
		return ""
	},
	"bot_run": func(b json.RawMessage) string {
		if id := jsonPathString(b, "botId"); id != "" {
			return "bot:" + id
		}
		return ""
	},
	"bot_pause": func(b json.RawMessage) string {
		if id := jsonPathString(b, "botId"); id != "" {
			return "bot:" + id
		}
		return ""
	},
	"bot_stop": func(b json.RawMessage) string {
		if id := jsonPathString(b, "botId"); id != "" {
			return "bot:" + id
		}
		return ""
	},
}

// opGates: a check of an operation that runs after its auth and before its body is decoded (prepared reads off: 503).
var opGates = map[string]func(q *Request) error{}

// authorizeOp: the access refresh, the declared auth rule, then the operation's gate.
func authorizeOp(q *Request, name string) error {
	if err := refreshRequestAccess(q); err != nil {
		return err
	}
	if err := declaredAuth(q, name); err != nil {
		return err
	}
	if g := opGates[name]; g != nil {
		return g(q)
	}
	return nil
}

// declaredAuth is the one authorization rule of an operation: its contract auth (contracts/ops-*.schema.json) and the
// fixed answers of docs/designs/fleet-standard.md S4.
//   - No person (a timer, a host): refused unless auth is system.
//   - An object the person cannot see is 404: a top-level projectId outside the person's grants, a project definition
//     of an ungranted project, the named object of a holder rule that does not exist.
//   - A missing role is 403, checked first: architect needs the architect role, owner full access, holder:connection
//     the architect role too (holderRole). Then the object: holder:<kind> needs full access or the named object's
//     holder (holder:account also passes the architect role); project:<path> a grant of the project at path (404).
//
// Full access passes every role rule. A project's own definitions are read with its grant (fleet-definition-editors.md).
func declaredAuth(q *Request, name string) error {
	auth := contract.Operations[name].Auth
	if kind := jsonPathString(q.body, "kind"); kind == "project" && strings.HasPrefix(name, "definition_") && !projectDefinitionRead(name) {
		return fail(403, "Use project_create or project_update")
	}
	p := q.person
	if p == nil {
		if auth == "system" {
			return nil
		}
		return fail(403, "Person required for "+name)
	}
	if fullAccess(p) {
		return nil
	}
	// A role the person lacks is 403 before any object is looked at, so the answer says nothing about the object.
	kind := strings.TrimPrefix(auth, "holder:")
	switch {
	case auth == "architect":
		if !hasRole(p, "architect") && !(projectDefinitionRead(name) && jsonPathString(q.body, "kind") == "project") {
			return fail(403, "Architect role required")
		}
	case auth == "owner", auth == "system":
		return fail(403, "Only the owner may call "+name)
	case strings.HasPrefix(auth, "holder:") && holderRole[kind] != "" && !hasRole(p, holderRole[kind]):
		return fail(403, "Architect role required")
	}
	if err := namedVisible(q, name); err != nil {
		return err
	}
	switch {
	case strings.HasPrefix(auth, "holder:"):
		if kind == "account" && hasRole(p, "architect") {
			return nil
		}
		h, ok := holders[kind]
		if !ok {
			return fail(500, "No holder rule for "+auth)
		}
		holder, found, err := h(q)
		if err != nil {
			return err
		}
		if !found {
			return fail(404, "Unknown "+kind)
		}
		if holder != p.Email {
			return fail(403, "Only the holder or the owner may call "+name)
		}
	case strings.HasPrefix(auth, "project:"):
		if id := jsonPathString(q.body, strings.TrimPrefix(auth, "project:")); id != "" && !mayAccessProjectID(p, id) {
			return fail(404, "Unknown project")
		}
	}
	return nil
}

// holderRole: the role a holder rule needs besides holding the object (connections are the architect's area).
var holderRole = map[string]string{"connection": "architect"}

// namedVisible: the 404 answer for a named object outside the person's grants: a top-level projectId (an empty one of a
// prepared resource read is the handler's 400), and the project of a project definition.
func namedVisible(q *Request, name string) error {
	p := q.person
	var b struct {
		ProjectID any    `json:"projectId"`
		Kind      string `json:"kind"`
		ID        string `json:"id"`
		Keys      []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"keys"`
	}
	_ = json.Unmarshal(q.body, &b)
	if id, ok := b.ProjectID.(string); ok && !(isResourceRead(name) && strings.TrimSpace(id) == "") && !mayAccessProjectID(p, id) {
		return fail(404, "Unknown project")
	}
	if strings.HasPrefix(name, "definition_") {
		if b.Kind == "project" && name != "definition_list" && !mayAccessProjectID(p, b.ID) {
			return fail(404, "Unknown project")
		}
		if name == "definition_export" {
			for _, k := range b.Keys {
				if k.Kind == "project" && !mayAccessProjectID(p, k.ID) {
					return fail(404, "Unknown project")
				}
			}
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

// body: the request body as its decoded JSON value, {} when it is none.
func body(q *Request) any {
	var b any
	json.Unmarshal(q.body, &b)
	if b == nil {
		return struct{}{}
	}
	return b
}
