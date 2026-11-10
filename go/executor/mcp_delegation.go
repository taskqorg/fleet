package main

// `executor mcp delegation|design-rules|connection`: newline JSON-RPC MCP servers the native agents start.
// fleet-stack L4: the delegation server shares the executor's durable journal and makes no native or model calls.
// Behaviour reference: crates/executor/src/mcp.rs and crates/executor/connection_call.py (mcp mode).

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
)

//go:embed mcp-tools.json
var mcpToolsJSON []byte

var (
	projectTools  = []string{"project_context", "catalogue", "pipeline_run", "direct_search", "pipeline_history", "pipeline_get", "question_ask", "question_history", "account_list"}
	agentTools    = []string{"session_start", "session_deliver", "session_transition", "session_fork", "session_resume", "session_handoffs", "message_send", "queue_list", "ask", "reply", "questions", "worker_done", "execution_status"}
	artifactTools = []string{"artifact_save", "artifact_list", "artifact_get"}
)

func init() { runMCP = mcpRun }

// newID is a random UUID v4.
func newID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func nowMS() int64 { return time.Now().UnixMilli() }

func mcpText(v any, isError *bool) obj {
	r := obj{"content": []any{obj{"type": "text", "text": encode(v)}}}
	if isError != nil {
		r["isError"] = *isError
	}
	return r
}

var yes, no = true, false

func mcpTools(ctx any) obj {
	all, _ := decode(mcpToolsJSON)
	out := []any{}
	for _, t := range all.([]any) {
		n := sv(get(t, "name"))
		if n == "request_child" {
			if !truth(get(ctx, "profiles")) {
				continue
			}
			get(t, "inputSchema", "properties", "profileId").(obj)["enum"] = get(ctx, "profiles")
		} else if slices.Contains(projectTools, n) && !truth(get(ctx, "projectId")) || slices.Contains(artifactTools, n) && !truth(get(ctx, "workdir")) {
			continue
		}
		out = append(out, t)
	}
	return obj{"tools": out}
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
			v, err := decode([]byte(raw.String))
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
	method := sv(get(req, "method"))
	p := get(req, "params")
	switch method {
	case "initialize":
		pv := any("2024-11-05")
		if has(p, "protocolVersion") {
			pv = get(p, "protocolVersion")
		}
		return obj{"protocolVersion": pv, "capabilities": obj{"tools": obj{}}, "serverInfo": obj{"name": "fleet-delegation", "version": "1"}}, nil
	case "ping":
		return obj{}, nil
	case "tools/list":
		return mcpTools(ctx), nil
	}
	name := sv(get(p, "name"))
	if method != "tools/call" {
		return nil, errors.New("Unsupported method")
	}
	q := any(obj{})
	if truth(get(p, "arguments")) {
		q = get(p, "arguments")
	}
	if slices.Contains(artifactTools, name) && truth(get(ctx, "workdir")) {
		return artifactCall(ctx, name, q, dbExec{db})
	}
	need := func(k string) (string, error) {
		s, ok := str(get(ctx, k))
		if !ok {
			return "", fmt.Errorf("'%s'", k)
		}
		return s, nil
	}
	agent := slices.Contains(agentTools, name)
	var key string
	var body obj
	if agent {
		cmd, err := need("commandId")
		if err != nil {
			return nil, err
		}
		key = cmd + ":agent:" + newID()
		body = obj{"hostId": get(ctx, "hostId"), "commandId": get(ctx, "commandId"), "executionId": get(ctx, "executionId"), "agentToolKey": key,
			"request": obj{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": obj{"name": name, "arguments": q}}}
	} else {
		child := name == "request_child"
		if !child && !(truth(get(ctx, "projectId")) && slices.Contains(projectTools, name)) {
			return nil, errors.New("Unsupported method")
		}
		var d obj
		qo, isObj := q.(obj)
		if child {
			profiles, _ := get(ctx, "profiles").([]any)
			bad := !isObj || len(qo) != 3 || !has(qo, "requestId") || !has(qo, "profileId") || !has(qo, "input") ||
				!slices.ContainsFunc(profiles, func(x any) bool { return encode(x) == encode(qo["profileId"]) })
			for k, n := range map[string]int{"requestId": 200, "input": 32000} {
				s, ok := str(get(q, k))
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
			for k := range qo {
				if !slices.Contains([]string{"requestId", "query", "methodId", "text", "runId"}, k) {
					return nil, errors.New("Invalid project request")
				}
			}
			effect := slices.Contains([]string{"pipeline_run", "direct_search", "question_ask"}, name)
			rid := any(name + "-" + newID())
			if effect {
				if _, ok := str(qo["requestId"]); !ok {
					return nil, errors.New("A stable requestId is required for effects")
				}
				rid = qo["requestId"]
			}
			d = obj{"operation": name, "arguments": q, "requestId": rid, "projectId": get(ctx, "projectId")}
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
		ids := obj{}
		for _, k := range []string{"hostId", "commandId", "executionId"} {
			s, err := need(k)
			if err != nil {
				return nil, err
			}
			ids[k] = s
		}
		if child {
			d = obj{"requestId": d["requestId"], "profileId": d["profileId"], "input": d["input"]}
			if _, ok := str(d["profileId"]); !ok {
				return nil, errors.New("Invalid child request")
			}
		} else if _, err := need("projectId"); err != nil {
			return nil, err
		}
		body = obj{"hostId": ids["hostId"], "commandId": ids["commandId"], "executionId": ids["executionId"], "delegation": d}
	}
	raw := encode(body)
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
			return get(r, "result"), nil
		}
		if r == nil {
			r = obj{"status": "pending", "instruction": "Outcome unknown; inspect with execution_status before repeating effects."}
		}
		return mcpText(r, &yes), nil
	}
	isErr := truth(r) && has(r, "error")
	if name == "request_child" {
		status := "pending-or-rejected"
		if truth(r) && has(r, "childExecutionId") {
			status = "accepted"
		}
		r = obj{"status": status, "response": r, "instruction": "Finish this turn. Child work runs after your completed receipt. Retry only with the same requestId; submission is not completion."}
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
		o, _ := decode([]byte(old))
		b, _ := decode([]byte(body))
		if encode(o) != encode(b) {
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
	p := get(req, "params")
	switch sv(get(req, "method")) {
	case "initialize":
		pv := any("2024-11-05")
		if has(p, "protocolVersion") {
			pv = get(p, "protocolVersion")
		}
		return obj{"protocolVersion": pv, "capabilities": obj{"tools": obj{}}, "serverInfo": obj{"name": "fleet-design-rules", "version": "1.0.0"}}, nil
	case "ping":
		return obj{}, nil
	case "tools/list":
		return obj{"tools": []any{obj{"name": "get_design_rules", "description": "Read project website design rules.",
			"inputSchema": obj{"type": "object", "properties": obj{}, "additionalProperties": false},
			"annotations": obj{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true}}}}, nil
	case "tools/call":
		if a, ok := get(p, "arguments").(obj); get(p, "name") == "get_design_rules" && (!has(p, "arguments") || ok && len(a) == 0) {
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
		if ctx, err = decode([]byte(s)); err != nil {
			return err
		}
	case "delegation":
		path, err := arg(1, "journal")
		if err != nil {
			return err
		}
		db, err = sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(5000)&_pragma=synchronous(FULL)")
		if err != nil {
			return err
		}
		db.SetMaxOpenConns(1)
		defer db.Close()
		s, err := arg(2, "context")
		if err != nil {
			return err
		}
		if ctx, err = decode([]byte(s)); err != nil {
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
		spec, err := decode([]byte(s))
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
		req, perr := decode([]byte(raw))
		if perr != nil {
			code, msg := -32602, pyJSONError(raw)
			if server == "design-rules" {
				code, msg = -32700, "Invalid request"
			}
			if msg == "" {
				msg = "Expecting value: line 1 column 1 (char 0)"
			}
			fmt.Fprintln(out, encode(obj{"jsonrpc": "2.0", "id": nil, "error": obj{"code": code, "message": msg}}))
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
		reply := obj{"jsonrpc": "2.0", "id": get(req, "id")}
		if err != nil {
			code := -32602
			if server == "design-rules" {
				code = -32601
			}
			reply["error"] = obj{"code": code, "message": strings.TrimSpace(err.Error())}
		} else {
			reply["result"] = result
		}
		fmt.Fprintln(out, encode(reply))
		out.Flush()
	}
}
