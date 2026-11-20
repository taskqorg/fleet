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

// delegationContext: the delegation server's context (executor.go writes it at the start of a Fleet tools run).
type delegationContext struct {
	HostId      *string          `json:"hostId"`
	CommandId   *string          `json:"commandId"`
	ExecutionId jsonValue        `json:"executionId"` // a string names the execution; requests carry the value as written
	ProjectId   *string          `json:"projectId"`
	Workdir     *string          `json:"workdir"`
	InputDir    jsonValue        `json:"inputDir"`
	Spool       *string          `json:"spool"`
	Profiles    jsonValue        `json:"profiles"` // read through profiles()
	Operations  []fleetOperation `json:"operations"`
}

// execution: the context's execution id, and whether it is a string.
func (c *delegationContext) execution() (string, bool) { return valStr(c.ExecutionId) }

// profiles: the delegation profiles when the context names a list of strings, else none (a list with any other item
// counts as no list).
func (c *delegationContext) profiles() []string {
	items, ok := c.Profiles.V.([]any)
	if !ok {
		return nil
	}
	out := []string{}
	for _, it := range items {
		s, ok := it.(string)
		if !ok {
			return nil
		}
		out = append(out, s)
	}
	return out
}

// fleetOperation: one of the director's operations (the start command's fleetOperations).
type fleetOperation struct {
	Name        string    `json:"name"`
	Description jsonValue `json:"description"`
	InputSchema jsonValue `json:"inputSchema"`
}

// contextOf: the delegation context of ctx, a *delegationContext or its JSON value (in memory: jClone gives it JSON form).
func contextOf(ctx any) *delegationContext {
	if c, ok := ctx.(*delegationContext); ok {
		return c
	}
	c := typed[delegationContext](ctx)
	return &c
}

// toolSchema: the part of a tool's inputSchema in mcp-tools.json that a call is checked against.
type toolSchema struct {
	Properties map[string]struct {
		MinLength *float64 `json:"minLength"`
		MaxLength *float64 `json:"maxLength"`
	} `json:"properties"`
	Required []string `json:"required"`
}

// mcpToolEntry: one entry of mcp-tools.json: its name, its schema and the entry as written (the listing publishes it).
type mcpToolEntry struct {
	Name        string     `json:"name"`
	InputSchema toolSchema `json:"inputSchema"`
	raw         string
}

var mcpToolEntries = func() []mcpToolEntry {
	var raws []json.RawMessage
	var out []mcpToolEntry
	json.Unmarshal(mcpToolsJSON, &raws)
	json.Unmarshal(mcpToolsJSON, &out)
	for i := range out {
		out[i].raw = string(raws[i])
	}
	return out
}()

// toolSchemas: the inputSchema of every tool in mcp-tools.json, by name.
var toolSchemas = func() map[string]toolSchema {
	out := map[string]toolSchema{}
	for _, t := range mcpToolEntries {
		out[t.Name] = t.InputSchema
	}
	return out
}()

// argumentsFit checks a project tool call against the tool's own inputSchema: only its properties, each a string within
// minLength and maxLength (characters), the required ones present and not blank.
func argumentsFit(name string, args M) error {
	schema := toolSchemas[name]
	for k, v := range args {
		p, ok := schema.Properties[k]
		if !ok {
			return fmt.Errorf("Invalid project request: %s takes no %s", name, k)
		}
		s, ok := v.(string)
		n := float64(utf8.RuneCountInString(s))
		if !ok || p.MinLength != nil && n < *p.MinLength {
			return fmt.Errorf("Invalid project request: %s must be a non-empty string", k)
		}
		if p.MaxLength != nil && n > *p.MaxLength {
			return fmt.Errorf("Invalid project request: %s is longer than %d characters", k, int(*p.MaxLength))
		}
	}
	for _, k := range schema.Required {
		if s, _ := args[k].(string); strings.TrimSpace(s) == "" {
			return fmt.Errorf("Invalid project request: %s needs %s", name, k)
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

// mcpTools: the delegation server's tools/list for its context, each entry a JSON object as published.
func mcpTools(ctx any) M {
	c := contextOf(ctx)
	out := []any{}
	names := []string{}
	for _, t := range mcpToolEntries {
		entry, _ := dec(t.raw)
		if t.Name == "request_child" {
			if len(c.profiles()) == 0 {
				continue
			}
			jAt(entry, "inputSchema", "properties", "profileId").(M)["enum"] = c.profiles() // the static entry's own shape
		} else if slices.Contains(projectTools, t.Name) && str0(c.ProjectId) == "" || slices.Contains(artifactTools, t.Name) && str0(c.Workdir) == "" {
			continue
		}
		out, names = append(out, entry), append(names, t.Name)
	}
	for _, o := range c.Operations { // the director's operations from the start command (profile fleetOperations)
		if n := o.Name; !slices.Contains(names, n) {
			schema := o.InputSchema.V
			if schema == nil {
				if generated := contract.Operations[n].InputSchema; len(generated) > 0 {
					schema = generated
				}
			}
			if schema == nil {
				schema = M{"type": "object"}
			}
			out, names = append(out, M{"name": n, "description": o.Description.V, "inputSchema": schema}), append(names, n)
		}
	}
	return M{"tools": out}
}

// Internal launch selection comes from the same schema/context as actual discovery, including recovery tools.
func mcpToolNames(discovery any) []any {
	d := typed[struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}](discovery)
	out := []any{}
	for _, tool := range d.Tools {
		out = append(out, tool.Name)
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

// hostReply: a stored host reply. Value is the reply as decoded (the agent reads it as is); Keys are the members the
// executor looks at, each Set when the reply is an object with that key.
type hostReply struct {
	Value any
	Keys  struct {
		Result           jsonValue `json:"result"`
		Error            jsonValue `json:"error"`
		ArtifactId       jsonValue `json:"artifactId"`
		ChildExecutionId jsonValue `json:"childExecutionId"`
	}
}

// valueObject: the reply when it is a JSON object (the caller adds its own keys to it).
func (r *hostReply) valueObject() (M, bool) {
	if r == nil {
		return nil, false
	}
	m, ok := r.Value.(M)
	return m, ok
}

func readReply(raw string) (*hostReply, error) {
	v, err := dec(raw)
	if err != nil {
		return nil, err
	}
	r := &hostReply{Value: v}
	r.Keys = typed[struct {
		Result           jsonValue `json:"result"`
		Error            jsonValue `json:"error"`
		ArtifactId       jsonValue `json:"artifactId"`
		ChildExecutionId jsonValue `json:"childExecutionId"`
	}](v) // a reply that is no object has none of the keys
	return r, nil
}

// wait returns the host's reply for key once the executor settles it (wake.go), nil after seconds.
func wait(db *sql.DB, table, key string, seconds int, artifact bool) (*hostReply, error) {
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
func reply(db *sql.DB, table, key string, artifact bool) (*hostReply, bool, error) {
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
	v, err := readReply(raw.String)
	if err != nil {
		return nil, false, unreadReply{err} // the stored reply does not decode: the request was made, its outcome unknown
	}
	if table != "transfers" || state == "failed" || (artifact && v.Keys.ArtifactId.Set) || (!artifact && state == "done") {
		return v, true, nil
	}
	return nil, false, nil
}

// journaledRequest: the members of a journaled delegation_requests body (AgentToolsRequest, ChildReceipt or
// ProjectReceipt) that agent_tool_result checks.
type journaledRequest struct {
	CommandId   string    `json:"commandId"`
	ExecutionId string    `json:"executionId"`
	Request     jsonValue `json:"request"`
}

// childArgs: request_child's arguments (mcp-tools.json).
type childArgs struct {
	RequestId *string `json:"requestId"`
	ProfileId *string `json:"profileId"`
	Input     *string `json:"input"`
}

// delegationCall answers one tools/call of the delegation server.
func delegationCall(name string, q, ctx any, db *sql.DB) (any, error) {
	if !truth(q) {
		q = M{}
	}
	c := contextOf(ctx)
	if name == "agent_tool_result" {
		key := typed[struct {
			RequestKey string `json:"requestKey"`
		}](q).RequestKey
		var body string
		var raw sql.NullString
		if !strings.HasPrefix(key, str0(c.CommandId)+":") {
			return mcpText(M{"error": "Request belongs to another command"}, &yes), nil
		}
		err := db.QueryRow("SELECT body,response FROM delegation_requests WHERE id=?", key).Scan(&body, &raw)
		if errors.Is(err, sql.ErrNoRows) {
			return mcpText(M{"error": "Unknown request"}, &yes), nil
		}
		if err != nil {
			return nil, dbe(err)
		}
		journaled, err := dec(body)
		if err != nil {
			return nil, dbe(err)
		}
		stored := typed[journaledRequest](journaled)
		if stored.CommandId != str0(c.CommandId) {
			return mcpText(M{"error": "Request belongs to another command"}, &yes), nil
		}
		if execution, _ := c.execution(); stored.ExecutionId != execution {
			return mcpText(M{"error": "Request belongs to another execution"}, &yes), nil
		}
		if raw.String == "" {
			return mcpText(M{"status": "pending", "requestKey": key, "instruction": "Outcome unknown; inspect this request again. Do not repeat the mutation."}, &yes), nil
		}
		result, err := readReply(raw.String)
		if err != nil {
			return nil, dbe(err)
		}
		if stored.Request.Set {
			if result.Keys.Result.Set {
				return result.Keys.Result.V, nil
			}
			return mcpText(result.Value, &yes), nil
		}
		isErr := result.Keys.Error.Set
		return mcpText(result.Value, &isErr), nil
	}
	if slices.Contains(artifactTools, name) && str0(c.Workdir) != "" {
		return artifactCall(c, name, q, dbExec{db})
	}
	need := func(k string, v *string) (string, error) {
		if v == nil {
			return "", fmt.Errorf("'%s'", k)
		}
		return *v, nil
	}
	agent := slices.Contains(agentTools, name) || slices.ContainsFunc(c.Operations, func(o fleetOperation) bool { return o.Name == name })
	var key string
	var body any
	route := contract.RouteAgentTools.Path // one route per envelope (contracts/host-protocol.schema.json routes)
	if agent {
		cmd, err := need("commandId", c.CommandId)
		if err != nil {
			return nil, err
		}
		key = cmd + ":agent:" + newID()
		var execution *string
		if e, ok := c.execution(); ok {
			execution = &e
		}
		// The coordinator derives the host from its token; the arguments are the agent's, passed on as they came.
		body = contract.AgentToolsRequest{HostId: str0(c.HostId), CommandId: cmd, ExecutionId: execution, AgentToolKey: &key,
			Request: json.RawMessage(enc(M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": M{"name": name, "arguments": q}}))}
	} else {
		child := name == "request_child"
		route = map[bool]string{true: contract.RouteDelegations.Path, false: contract.RouteProjectOperations.Path}[child]
		if !child && !(str0(c.ProjectId) != "" && slices.Contains(projectTools, name)) {
			return nil, errors.New("Unsupported method")
		}
		q = jClone(q)           // the arguments in JSON form: what is checked here is what is sent
		args, isObject := q.(M) // the arguments by key
		var rid string
		var ca childArgs
		if child {
			ca = typed[childArgs](q)
			_, hasRequest := args["requestId"]
			_, hasProfile := args["profileId"]
			_, hasInput := args["input"]
			bad := !isObject || len(args) != 3 || !hasRequest || !hasProfile || !hasInput || ca.ProfileId == nil || !slices.Contains(c.profiles(), *ca.ProfileId)
			for _, f := range []struct {
				v *string
				n int
			}{{ca.RequestId, 200}, {ca.Input, 32000}} {
				bad = bad || f.v == nil || *f.v == "" || utf8.RuneCountInString(*f.v) > f.n
			}
			if bad {
				return nil, errors.New("Invalid child request")
			}
			rid = *ca.RequestId
		} else {
			if !isObject {
				return nil, errors.New("Invalid project request")
			}
			if err := argumentsFit(name, args); err != nil {
				return nil, err
			}
			// An effect (its schema requires requestId) keeps the caller's requestId, so a retry is the same request.
			rid = name + "-" + newID()
			if slices.Contains(toolSchemas[name].Required, "requestId") {
				rid, _ = args["requestId"].(string) // argumentsFit: a non-blank string
			}
		}
		cmd, err := need("commandId", c.CommandId)
		if err != nil {
			return nil, err
		}
		key = cmd + ":" + rid
		// The typed receipt (contracts ChildReceipt / ProjectReceipt): every identity must be a string.
		host, err := need("hostId", c.HostId)
		if err != nil {
			return nil, err
		}
		execution, ok := c.execution()
		if !ok {
			return nil, fmt.Errorf("'%s'", "executionId")
		}
		if child {
			body = contract.ChildReceipt{HostId: host, CommandId: cmd, ExecutionId: execution,
				Delegation: contract.ChildRequest{RequestId: rid, ProfileId: *ca.ProfileId, Input: *ca.Input}}
		} else {
			project, err := need("projectId", c.ProjectId)
			if err != nil {
				return nil, err
			}
			body = contract.ProjectReceipt{HostId: host, CommandId: cmd, ExecutionId: execution,
				Delegation: contract.ProjectRequest{Operation: contract.ProjectOperation(name), Arguments: projectArguments(args), RequestId: rid, ProjectId: project}}
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
		if r.Keys.Result.Set {
			return r.Keys.Result.V, nil
		}
		return mcpText(r.Value, &yes), nil
	}
	var value any
	isErr := false
	if r != nil {
		value, isErr = r.Value, r.Keys.Error.Set
	}
	if name == "request_child" {
		status := "pending-or-rejected"
		if r != nil && r.Keys.ChildExecutionId.Set {
			status = "accepted"
		}
		value = M{"status": status, "response": value, "instruction": "Finish this turn. Child work runs after your completed receipt. Retry only with the same requestId; submission is not completion."}
	}
	return mcpText(value, &isErr), nil
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
		spec, err := dec(args[2]) // passed on as written
		if err != nil {
			return err
		}
		return mcpStdio(args[1], spec, os.Stdin, os.Stdout, args[3], typed[mcpLaunchSpec](spec).PathPolicy)
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
		if ctx, err = dec(s); err != nil { // the grant data or forge context, handed to its server as is
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
		v, err := dec(s) // decoded once; a context of another shape leaves every member unset
		if err != nil {
			return err
		}
		c := typed[delegationContext](v)
		ctx = &c
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
func operationsOf(ctx any) []fleetOperation { return contextOf(ctx).Operations }

// projectArguments: checked project arguments as the contract carries them, each value as checked.
func projectArguments(args M) contract.ProjectArguments {
	out := contract.ProjectArguments{}
	for k, v := range args {
		out[k] = json.RawMessage(enc(v))
	}
	return out
}
