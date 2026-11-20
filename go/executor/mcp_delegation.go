package main

// `executor mcp delegation|design-rules|connection`: newline JSON-RPC MCP servers the native agents start.
// fleet-stack L4: the delegation server shares the executor's durable journal and makes no native or model calls.
// Behaviour reference: the stdio exchanges in tests/executor/fixtures/mcp.json (docs/designs/fleet-stack.md L4).

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"fleet/contract"
	"fleet/mcpserve"
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
	agentTools    = []string{"session_start", "session_presets", "session_deliver", "session_transition", "session_fork", "session_resume", "session_handoffs", "session_rename", "message_send", "queue_list", "ask", "reply", "questions", "worker_done", "execution_status"}
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
			schema := jAt(o, "inputSchema")
			if schema == nil {
				if generated := contract.Operations[n].InputSchema; len(generated) > 0 {
					schema = generated
				}
			}
			if schema == nil {
				schema = M{"type": "object"}
			}
			out = append(out, M{"name": n, "description": jAt(o, "description"), "inputSchema": schema})
		}
	}
	return M{"tools": out}
}

// Internal launch selection comes from the same schema/context as actual discovery, including recovery tools.
func mcpToolNames(discovery any) []any {
	out := []any{}
	for _, tool := range jArr(discovery, "tools") {
		out = append(out, jStr(tool, "name"))
	}
	return out
}

// The context goes in a file (@path), never in argv: a director's operation list with schemas outgrows Linux's
// 128 KiB limit for one argument (MAX_ARG_STRLEN) once the MCP wrappers escape it, and the harness then cannot start.
func delegationServer(ctx any, journal, contextFile string) (M, error) {
	if err := os.WriteFile(contextFile, []byte(enc(ctx)), 0o600); err != nil {
		return nil, err
	}
	return M{"command": exe(), "args": []any{"mcp", "delegation", journal, "@" + contextFile}, "tools": mcpToolNames(mcpTools(ctx))}, nil
}

// unreadReply marks a stored host reply that does not decode: the request was journaled, so its outcome is unknown.
type unreadReply struct{ error }

// dbError marks journal failures: they end the call with a JSON-RPC error, never a tool result.
type dbError struct{ error }

func dbe(err error) error {
	if err == nil {
		return nil
	}
	return dbError{err}
}

// wait returns the host's reply for key once the executor settles it (wake.go), nil after seconds.
func wait(db *sql.DB, table, key string, seconds int, artifact bool) (any, error) {
	end := time.Now().Add(time.Duration(seconds) * time.Second)
	w := listen(db, key) // before the first read: a reply written after it signals this FIFO
	defer w.close()
	for {
		v, ok, err := reply(db, table, key, artifact)
		if ok || err != nil {
			return v, err
		}
		if !w.wait(end) {
			v, _, err := reply(db, table, key, artifact) // the last look at the deadline: a lost signal costs only latency
			return v, err
		}
	}
}

// reply reads key's stored reply: ok once it is the one the caller waits for.
func reply(db *sql.DB, table, key string, artifact bool) (any, bool, error) {
	q := "SELECT response,'' FROM delegation_requests WHERE id=?"
	if table == "transfers" {
		q = "SELECT response,state FROM transfers WHERE id=?"
	}
	var raw sql.NullString
	var state string
	err := db.QueryRow(q, key).Scan(&raw, &state)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, false, dbe(err)
	}
	if err != nil || raw.String == "" {
		return nil, false, nil
	}
	v, err := dec(raw.String)
	if err != nil {
		return nil, false, unreadReply{err} // the stored reply does not decode: the request was made, its outcome unknown
	}
	if table != "transfers" || state == "failed" || (artifact && has(v, "artifactId")) || (!artifact && state == "done") {
		return v, true, nil
	}
	return nil, false, nil
}

// delegationCall answers one tools/call of the delegation server.
func delegationCall(name string, q, ctx any, db *sql.DB) (any, error) {
	if !truth(q) {
		q = M{}
	}
	if name == "agent_tool_result" {
		key := jStr(q, "requestKey")
		var body string
		var raw sql.NullString
		if !strings.HasPrefix(key, jStr(ctx, "commandId")+":") {
			return mcpText(M{"error": "Request belongs to another command"}, &yes), nil
		}
		err := db.QueryRow("SELECT body,response FROM delegation_requests WHERE id=?", key).Scan(&body, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return mcpText(M{"error": "Unknown request"}, &yes), nil
		}
		if err != nil {
			return nil, dbe(err)
		}
		stored, err := dec(body)
		if err != nil {
			return nil, dbe(err)
		}
		if jStr(stored, "commandId") != jStr(ctx, "commandId") {
			return mcpText(M{"error": "Request belongs to another command"}, &yes), nil
		}
		if jStr(stored, "executionId") != jStr(ctx, "executionId") {
			return mcpText(M{"error": "Request belongs to another execution"}, &yes), nil
		}
		if raw.String == "" {
			return mcpText(M{"status": "pending", "requestKey": key, "instruction": "Outcome unknown; inspect this request again. Do not repeat the mutation."}, &yes), nil
		}
		result, err := dec(raw.String)
		if err != nil {
			return nil, dbe(err)
		}
		if has(stored, "request") {
			if has(result, "result") {
				return jAt(result, "result"), nil
			}
			return mcpText(result, &yes), nil
		}
		isErr := has(result, "error")
		return mcpText(result, &isErr), nil
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
	var body any
	route := contract.RouteAgentTools.Path // one route per envelope (contracts/host-protocol.schema.json routes)
	if agent {
		cmd, err := need("commandId")
		if err != nil {
			return nil, err
		}
		key = cmd + ":agent:" + newID()
		host := jStr(ctx, "hostId") // the coordinator derives the host from its token
		var execution *string
		if s, ok := str(jAt(ctx, "executionId")); ok {
			execution = &s
		}
		body = contract.AgentToolsRequest{HostId: host, CommandId: cmd, ExecutionId: execution, AgentToolKey: &key,
			Request: json.RawMessage(enc(M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": M{"name": name, "arguments": q}}))}
	} else {
		child := name == "request_child"
		route = map[bool]string{true: contract.RouteDelegations.Path, false: contract.RouteProjectOperations.Path}[child]
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
			return nil, errors.New(map[bool]string{true: "Invalid child request", false: "Invalid project request"}[child])
		}
		key = cmd + ":" + rid
		// The typed receipt (contracts ChildReceipt / ProjectReceipt): every identity must be a string.
		ids := map[string]string{}
		for _, k := range []string{"hostId", "commandId", "executionId"} {
			s, err := need(k)
			if err != nil {
				return nil, err
			}
			ids[k] = s
		}
		if child {
			profile, ok := str(d["profileId"])
			if !ok {
				return nil, errors.New("Invalid child request")
			}
			body = contract.ChildReceipt{HostId: ids["hostId"], CommandId: ids["commandId"], ExecutionId: ids["executionId"],
				Delegation: contract.ChildRequest{RequestId: rid, ProfileId: profile, Input: jStr(d, "input")}}
		} else {
			project, err := need("projectId")
			if err != nil {
				return nil, err
			}
			args := contract.ProjectArguments{}
			for k, v := range qo {
				args[k] = json.RawMessage(enc(v))
			}
			body = contract.ProjectReceipt{HostId: ids["hostId"], CommandId: ids["commandId"], ExecutionId: ids["executionId"],
				Delegation: contract.ProjectRequest{Operation: contract.ProjectOperation(name), Arguments: args, RequestId: rid, ProjectId: project}}
		}
	}
	raw := enc(body)
	// Acquire the writer before reading: a deferred WAL read cannot upgrade after a concurrent host reply.
	if err := journalOnce(db, key, raw, route); err != nil {
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
	if r == nil && name != "request_child" {
		return mcpText(M{"status": "pending", "requestKey": key, "instruction": "Outcome unknown; use agent_tool_result with this requestKey. Do not repeat the mutation."}, &yes), nil
	}
	if agent {
		if has(r, "result") {
			return jAt(r, "result"), nil
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

var designRulesTools = []any{M{"name": "get_design_rules", "description": "Read project website design rules.",
	"inputSchema": M{"type": "object", "properties": M{}, "additionalProperties": false},
	"annotations": M{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true}}}

// designRulesCall answers get_design_rules with no arguments: the server's context.
func designRulesCall(name string, args, ctx any) (any, error) {
	if a, ok := args.(M); name == "get_design_rules" && (args == nil || ok && len(a) == 0) {
		return mcpText(ctx, &no), nil
	}
	return nil, errors.New("Unsupported tool or arguments")
}

func mcpRun(args []string) error {
	if len(args) == 0 {
		return errors.New("MCP server required")
	}
	server := args[0]
	if server == "stdio" {
		if len(args) != 4 {
			return errors.New("stdio MCP needs owner, trusted spec and root")
		}
		spec, err := dec(args[2])
		if err != nil {
			return err
		}
		return mcpStdio(args[1], spec, os.Stdin, os.Stdout, args[3], jStr(spec, "pathPolicy"))
	}

	if server == "project-browser" {
		if len(args) != 2 {
			return errors.New("browser context required")
		}
		cfg, err := dec(args[1])
		if err != nil {
			return err
		}
		cmd := browserCommand(cfg, os.Getenv("HOME"), false)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}

	arg := func(i int, what string) (string, error) {
		if i >= len(args) {
			return "", errors.New(what + " required")
		}
		return args[i], nil
	}
	var ctx any
	var db *sql.DB
	switch server {
	case "design-rules", "project-forge":
		s, err := arg(1, "context")
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
		// The context file delegationServer writes; inline JSON stays for a harness an earlier executor started, which
		// restarts this server from the updated binary with its original argv.
		if path, ok := strings.CutPrefix(s, "@"); ok {
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			s = string(b)
		}
		if ctx, err = dec(s); err != nil {
			return err
		}
	case "code":
		path, err := arg(1, "spec file")
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		spec, err := dec(string(b))
		if err != nil {
			return err
		}
		return codeMCP(spec, os.Stdin, os.Stdout)
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
		// The executor passes the run token, gateway URL, Access file path and the run's work and input folders in a
		// private run file (argument 3), never in the harness environment; they become this server's own environment.
		if f, err := arg(3, "run file"); err == nil {
			if err := loadRunFile(f); err != nil {
				return err
			}
		}
		return connectionMCP(conn, spec, os.Stdin, os.Stdout)
	default:
		return errors.New("unknown MCP server")
	}
	srv := mcpserve.Stdio{Name: "fleet-design-rules", Version: "1.0.0", Tools: func() any { return designRulesTools },
		Call: func(name string, args json.RawMessage) (any, error) { return designRulesCall(name, decRaw(args), ctx) }}
	switch server {
	case "delegation":
		srv = mcpserve.Stdio{Name: "fleet-delegation", Version: "1", Tools: func() any { return mcpTools(ctx)["tools"] },
			Call: func(name string, args json.RawMessage) (any, error) {
				return delegationCall(name, decRaw(args), ctx, db)
			}}
	case "project-forge":
		srv = mcpserve.Stdio{Name: "fleet-project-forge", Version: "1", Tools: func() any { return forgeTools },
			Call: func(name string, args json.RawMessage) (any, error) { return forgeCall(name, decRaw(args), ctx) }}
	}
	return srv.Serve(os.Stdin, os.Stdout)
}

// operationsOf: the start command's fleetOperations list carried in the server context.
func operationsOf(ctx any) []any {
	xs, _ := jAt(ctx, "operations").([]any)
	return xs
}
