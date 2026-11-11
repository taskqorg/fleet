package main

// `executor mcp delegation|design-rules|connection`: newline JSON-RPC MCP servers the native agents start.
// fleet-stack L4: the delegation server shares the executor's durable journal and makes no native or model calls.
// Behaviour reference: the stdio exchanges in tests/executor/fixtures/mcp.json (docs/designs/fleet-stack.md L4).

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"fleet/contract"
)

//go:embed mcp-tools.json
var mcpToolsJSON []byte

var (
	projectTools = func() (out []string) { // the contract's ProjectOperation
		for _, o := range contract.ProjectOperationValues {
			out = append(out, string(o))
		}
		return out
	}()
	agentTools    = []string{"session_start", "session_deliver", "session_transition", "session_fork", "session_resume", "session_handoffs", "message_send", "queue_list", "ask", "reply", "questions", "worker_done", "execution_status"}
	artifactTools = []string{"artifact_save", "artifact_list", "artifact_get"}
)

func init() { runMCP = mcpRun }

// toolSchemas: the inputSchema of every tool in mcp-tools.json, by name.
var toolSchemas = func() map[string]any {
	all, _ := dec(string(mcpToolsJSON))
	out := map[string]any{}
	for _, t := range jArr(all) {
		out[jStr(t, "name")] = jAt(t, "inputSchema")
	}
	return out
}()

// argumentsFit checks a project tool call against the tool's own inputSchema: only its properties, each a string within
// minLength and maxLength (characters), the required ones present and not blank.
func argumentsFit(name string, args M) error {
	schema := toolSchemas[name]
	for k, v := range args {
		p, ok := jAt(schema, "properties", k).(M)
		if !ok {
			return fmt.Errorf("Invalid project request: %s takes no %s", name, k)
		}
		s, isStr := v.(string)
		n := float64(utf8.RuneCountInString(s))
		if min, ok := p["minLength"].(float64); !isStr || ok && n < min {
			return fmt.Errorf("Invalid project request: %s must be a non-empty string", k)
		}
		if max, ok := p["maxLength"].(float64); ok && n > max {
			return fmt.Errorf("Invalid project request: %s is longer than %d characters", k, int(max))
		}
	}
	for _, k := range jArr(schema, "required") {
		if s, _ := str(args[jStr(k)]); strings.TrimSpace(s) == "" {
			return fmt.Errorf("Invalid project request: %s needs %s", name, jStr(k))
		}
	}
	return nil
}

// newID is a random UUID v4.
func newID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func mcpText(v any, isError *bool) M {
	r := M{"content": []any{M{"type": "text", "text": enc(v)}}}
	if isError != nil {
		r["isError"] = *isError
	}
	return r
}

var yes, no = true, false

func mcpTools(ctx any) M {
	all, _ := dec(string(mcpToolsJSON))
	out := []any{}
	for _, t := range all.([]any) {
		n := jStr(jAt(t, "name"))
		if n == "request_child" {
			if !truth(jAt(ctx, "profiles")) {
				continue
			}
			jAt(t, "inputSchema", "properties", "profileId").(M)["enum"] = jAt(ctx, "profiles")
		} else if slices.Contains(projectTools, n) && !truth(jAt(ctx, "projectId")) || slices.Contains(artifactTools, n) && !truth(jAt(ctx, "workdir")) {
			continue
		}
		out = append(out, t)
	}
	for _, o := range operationsOf(ctx) { // the director's operations from the start command (profile fleetOperations)
		if n := jStr(jAt(o, "name")); !slices.ContainsFunc(out, func(t any) bool { return jStr(jAt(t, "name")) == n }) {
			out = append(out, M{"name": n, "description": jAt(o, "description"), "inputSchema": M{"type": "object"}})
		}
	}
	return M{"tools": out}
}

// dbError marks journal failures: they end the call with a JSON-RPC error, never a tool result.
type dbError struct{ error }

func dbe(err error) error {
	if err == nil {
		return nil
	}
	return dbError{err}
}

// wait polls the host's reply for key; nil after seconds.
func wait(db *sql.DB, table, key string, seconds int, artifact bool) (any, error) {
	end := time.Now().Add(time.Duration(seconds) * time.Second)
	q := "SELECT response,'' FROM delegation_requests WHERE id=?"
	pause := 50 * time.Millisecond
	if table == "transfers" {
		q, pause = "SELECT response,state FROM transfers WHERE id=?", 100*time.Millisecond
	}
	for time.Now().Before(end) {
		var raw sql.NullString
		var state string
		err := db.QueryRow(q, key).Scan(&raw, &state)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, dbe(err)
		}
		if err == nil && raw.String != "" {
			v, err := dec(raw.String)
			if err != nil {
				return nil, err
			}
			if table != "transfers" || state == "failed" || (artifact && has(v, "artifactId")) || (!artifact && state == "done") {
				return v, nil
			}
		}
		time.Sleep(pause)
	}
	return nil, nil
}

func delegation(req, ctx any, db *sql.DB) (any, error) {
	method := jStr(jAt(req, "method"))
	p := jAt(req, "params")
	switch method {
	case "initialize":
		pv := any("2024-11-05")
		if has(p, "protocolVersion") {
			pv = jAt(p, "protocolVersion")
		}
		return M{"protocolVersion": pv, "capabilities": M{"tools": M{}}, "serverInfo": M{"name": "fleet-delegation", "version": "1"}}, nil
	case "ping":
		return M{}, nil
	case "tools/list":
		return mcpTools(ctx), nil
	}
	name := jStr(jAt(p, "name"))
	if method != "tools/call" {
		return nil, errors.New("Unsupported method")
	}
	q := any(M{})
	if truth(jAt(p, "arguments")) {
		q = jAt(p, "arguments")
	}
	if slices.Contains(artifactTools, name) && truth(jAt(ctx, "workdir")) {
		return artifactCall(ctx, name, q, dbExec{db})
	}
	need := func(k string) (string, error) {
		s, ok := str(jAt(ctx, k))
		if !ok {
			return "", fmt.Errorf("'%s'", k)
		}
		return s, nil
	}
	agent := slices.Contains(agentTools, name) || slices.ContainsFunc(operationsOf(ctx), func(o any) bool { return jStr(jAt(o, "name")) == name })
	var key string
	var body M
	if agent {
		cmd, err := need("commandId")
		if err != nil {
			return nil, err
		}
		key = cmd + ":agent:" + newID()
		body = M{"hostId": jAt(ctx, "hostId"), "commandId": jAt(ctx, "commandId"), "executionId": jAt(ctx, "executionId"), "agentToolKey": key,
			"request": M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": M{"name": name, "arguments": q}}}
	} else {
		child := name == "request_child"
		if !child && !(truth(jAt(ctx, "projectId")) && slices.Contains(projectTools, name)) {
			return nil, errors.New("Unsupported method")
		}
		var d M
		qo, isObj := q.(M)
		if child {
			profiles, _ := jAt(ctx, "profiles").([]any)
			bad := !isObj || len(qo) != 3 || !has(qo, "requestId") || !has(qo, "profileId") || !has(qo, "input") ||
				!slices.ContainsFunc(profiles, func(x any) bool { return enc(x) == enc(qo["profileId"]) })
			for k, n := range map[string]int{"requestId": 200, "input": 32000} {
				s, ok := str(jAt(q, k))
				bad = bad || !ok || s == "" || utf8.RuneCountInString(s) > n
			}
			if bad {
				return nil, errors.New("Invalid child request")
			}
			d = qo
		} else {
			if !isObj {
				return nil, errors.New("Invalid project request")
			}
			if err := argumentsFit(name, qo); err != nil {
				return nil, err
			}
			// An effect (its schema requires requestId) keeps the caller's requestId, so a retry is the same request.
			rid := any(name + "-" + newID())
			if slices.Contains(jArr(toolSchemas[name], "required"), any("requestId")) {
				rid = qo["requestId"]
			}
			d = M{"operation": name, "arguments": q, "requestId": rid, "projectId": jAt(ctx, "projectId")}
		}
		cmd, err := need("commandId")
		if err != nil {
			return nil, err
		}
		rid, ok := str(d["requestId"])
		if !ok {
			return nil, errors.New("Invalid child request")
		}
		key = cmd + ":" + rid
		// The typed receipt (contracts ChildReceipt / ProjectReceipt): every identity must be a string.
		ids := M{}
		for _, k := range []string{"hostId", "commandId", "executionId"} {
			s, err := need(k)
			if err != nil {
				return nil, err
			}
			ids[k] = s
		}
		if child {
			d = M{"requestId": d["requestId"], "profileId": d["profileId"], "input": d["input"]}
			if _, ok := str(d["profileId"]); !ok {
				return nil, errors.New("Invalid child request")
			}
		} else if _, err := need("projectId"); err != nil {
			return nil, err
		}
		body = M{"hostId": ids["hostId"], "commandId": ids["commandId"], "executionId": ids["executionId"], "delegation": d}
	}
	raw := enc(body)
	// Acquire the writer before reading: a deferred WAL read cannot upgrade after a concurrent host reply.
	if err := journalOnce(db, key, raw, map[bool]string{true: "/api/hosts/agent-tools", false: "/api/hosts/receipts"}[agent]); err != nil {
		return nil, err
	}
	seconds := 8
	if agent {
		seconds = 20
	}
	r, err := wait(db, "delegation_requests", key, seconds, false)
	if err != nil {
		return nil, err
	}
	if agent {
		if has(r, "result") {
			return jAt(r, "result"), nil
		}
		if r == nil {
			r = M{"status": "pending", "instruction": "Outcome unknown; inspect with execution_status before repeating effects."}
		}
		return mcpText(r, &yes), nil
	}
	isErr := truth(r) && has(r, "error")
	if name == "request_child" {
		status := "pending-or-rejected"
		if truth(r) && has(r, "childExecutionId") {
			status = "accepted"
		}
		r = M{"status": status, "response": r, "instruction": "Finish this turn. Child work runs after your completed receipt. Retry only with the same requestId; submission is not completion."}
	}
	return mcpText(r, &isErr), nil
}

// journalOnce records the request and its outbox row in one immediate transaction; a repeat with the same body is a no-op.
func journalOnce(db *sql.DB, key, body, route string) error {
	ctx := context.Background()
	c, err := db.Conn(ctx)
	if err != nil {
		return dbe(err)
	}
	defer c.Close()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return dbe(err)
	}
	fail := func(err error) error { c.ExecContext(ctx, "ROLLBACK"); return err }
	var old string
	switch err := c.QueryRowContext(ctx, "SELECT body FROM delegation_requests WHERE id=?", key).Scan(&old); {
	case err == nil:
		o, _ := dec(old)
		b, _ := dec(body)
		if enc(o) != enc(b) {
			return fail(errors.New("Request identity conflict"))
		}
	case errors.Is(err, sql.ErrNoRows):
		if _, err := c.ExecContext(ctx, "INSERT INTO delegation_requests(id,body) VALUES(?,?)", key, body); err != nil {
			return fail(dbe(err))
		}
		if _, err := c.ExecContext(ctx, "INSERT INTO outbox(route,body) VALUES(?,?)", route, body); err != nil {
			return fail(dbe(err))
		}
	default:
		return fail(dbe(err))
	}
	_, err = c.ExecContext(ctx, "COMMIT")
	return dbe(err)
}

func designRules(req, ctx any) (any, error) {
	p := jAt(req, "params")
	switch jStr(jAt(req, "method")) {
	case "initialize":
		pv := any("2024-11-05")
		if has(p, "protocolVersion") {
			pv = jAt(p, "protocolVersion")
		}
		return M{"protocolVersion": pv, "capabilities": M{"tools": M{}}, "serverInfo": M{"name": "fleet-design-rules", "version": "1.0.0"}}, nil
	case "ping":
		return M{}, nil
	case "tools/list":
		return M{"tools": []any{M{"name": "get_design_rules", "description": "Read project website design rules.",
			"inputSchema": M{"type": "object", "properties": M{}, "additionalProperties": false},
			"annotations": M{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true}}}}, nil
	case "tools/call":
		if a, ok := jAt(p, "arguments").(M); jAt(p, "name") == "get_design_rules" && (!has(p, "arguments") || ok && len(a) == 0) {
			return mcpText(ctx, &no), nil
		}
	}
	return nil, errors.New("Unsupported method, tool or arguments")
}

func mcpRun(args []string) error {
	if len(args) == 0 {
		return errors.New("MCP server required")
	}
	server := args[0]
	arg := func(i int, what string) (string, error) {
		if i >= len(args) {
			return "", errors.New(what + " required")
		}
		return args[i], nil
	}
	var ctx any
	var db *sql.DB
	switch server {
	case "design-rules":
		s, err := arg(1, "rules")
		if err != nil {
			return err
		}
		if ctx, err = dec(s); err != nil {
			return err
		}
	case "delegation":
		path, err := arg(1, "journal")
		if err != nil {
			return err
		}
		db, err = openJournal(path)
		if err != nil {
			return err
		}
		defer db.Close()
		s, err := arg(2, "context")
		if err != nil {
			return err
		}
		if ctx, err = dec(s); err != nil {
			return err
		}
	case "connection":
		conn, err := arg(1, "connection")
		if err != nil {
			return err
		}
		s, err := arg(2, "tools")
		if err != nil {
			return err
		}
		spec, err := dec(s)
		if err != nil {
			return err
		}
		// The executor passes the run token, gateway URL and Access file path in a private run file (argument 3), never in
		// the harness environment; they become this server's own environment.
		if f, err := arg(3, "run file"); err == nil {
			if err := loadRunFile(f); err != nil {
				return err
			}
		}
		return connectionMCP(conn, spec, os.Stdin, os.Stdout)
	default:
		return errors.New("unknown MCP server")
	}
	in := bufio.NewReader(os.Stdin)
	out := bufio.NewWriter(os.Stdout)
	for {
		raw, err := in.ReadString('\n')
		if raw == "" && err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		req, perr := dec(raw)
		if perr != nil {
			fmt.Fprintln(out, enc(M{"jsonrpc": "2.0", "id": nil, "error": M{"code": -32700, "message": "Parse error"}}))
			out.Flush()
			continue
		}
		if !has(req, "id") {
			continue
		}
		var result any
		if db != nil {
			result, err = delegation(req, ctx, db)
		} else {
			result, err = designRules(req, ctx)
		}
		reply := M{"jsonrpc": "2.0", "id": jAt(req, "id")}
		if err != nil {
			code := -32602
			if server == "design-rules" {
				code = -32601
			}
			reply["error"] = M{"code": code, "message": strings.TrimSpace(err.Error())}
		} else {
			reply["result"] = result
		}
		fmt.Fprintln(out, enc(reply))
		out.Flush()
	}
}

// operationsOf: the start command's fleetOperations list carried in the server context.
func operationsOf(ctx any) []any {
	xs, _ := jAt(ctx, "operations").([]any)
	return xs
}
