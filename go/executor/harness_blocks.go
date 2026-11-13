package main

// Event projection and typed transcript blocks (docs/designs/fleet-conversations.md, "Block
// model"): text, reasoning and tool, the only producer of reply text. State per execution: tools started and not yet
// ended (claude tool_use, ACP tool_call) with their start time, and the open streams (ACP chunks). A chunk record carries
// `deltaOf` and its chunk as a block; the next record that is not a chunk closes every open stream, carries the joined
// final blocks first, then its own, and names the closed streams in `closes`. The first chunk of a stream also carries
// `opens: true`.

import (
	"encoding/json"
	"fleet/contract"
	"math"
	"sort"
	"strings"
)

// first returns the value of the first key present in v (Rust's jAt(a).or(jAt(b))).
func first(v any, keys ...string) any {
	for _, k := range keys {
		if has(v, k) {
			return jAt(v, k)
		}
	}
	return nil
}

func nonEmptyObj(x any) bool { o, ok := x.(M); return x != nil && !(ok && len(o) == 0) }

func toolsOf(h string, v any) []any {
	switch {
	case h == "claude":
		out := []any{}
		content, _ := jAt(v, "message", "content").([]any)
		for _, b := range content {
			if t := jAt(b, "type"); t == "tool_use" || t == "tool_result" {
				out = append(out, M{"id": first(b, "id", "tool_use_id"), "name": jAt(b, "name"), "arguments": jAt(b, "input"), "result": jAt(b, "content"), "error": jAt(b, "is_error")})
			}
		}
		return out
	case isACP(h) && (jAt(v, "update", "sessionUpdate") == "tool_call" || jAt(v, "update", "sessionUpdate") == "tool_call_update"):
		b := jAt(v, "update")
		result := first(b, "content", "rawOutput")
		if nonEmptyObj(jAt(b, "rawOutput")) {
			result = jAt(b, "rawOutput")
		}
		return []any{M{"id": jAt(b, "toolCallId"), "name": first(b, "name", "title"), "arguments": jAt(b, "rawInput"), "result": result, "status": jAt(b, "status"), "kind": jAt(b, "kind")}}
	case isACP(h) && jAt(v, "step_update", "step_type") == "tool":
		b := jAt(v, "step_update")
		return []any{M{"id": jAt(b, "step_index"), "name": jAt(b, "tool_name"), "arguments": jAt(b, "tool_info", "parameters"), "result": jAt(b, "tool_info", "result"), "status": jAt(b, "state")}}
	case isACP(h) && jAt(v, "type") == "PLANNER_RESPONSE":
		out := []any{}
		calls, _ := jAt(v, "tool_calls").([]any)
		for _, b := range calls {
			out = append(out, M{"id": jAt(b, "id"), "name": jAt(b, "name"), "arguments": first(b, "arguments", "args"), "result": nil, "status": "requested"})
		}
		return out
	case isACP(h) && jAt(v, "type") == "TOOL_RESPONSE":
		return []any{M{"id": jAt(v, "tool_call_id"), "name": nil, "arguments": nil, "result": first(v, "content", "result"), "status": jAt(v, "status")}}
	case h == "codex":
		switch jAt(v, "item", "type") {
		case "command_execution", "mcp_tool_call", "function_call", "function_call_output", "commandExecution", "mcpToolCall", "collabAgentToolCall":
			b := jAt(v, "item")
			return []any{M{"id": jAt(b, "id"), "name": first(b, "tool", "name", "type"), "arguments": first(b, "arguments", "command"),
				"result": first(b, "result", "aggregatedOutput", "aggregated_output", "output"), "status": jAt(b, "status"), "exitCode": first(b, "exitCode", "exit_code")}}
		}
	}
	return []any{}
}

func nativeUsage(v any) (any, bool) {
	if has(v, "usage") {
		return jAt(v, "usage"), true
	}
	if has(jAt(v, "result"), "usage") {
		return jAt(v, "result", "usage"), true
	}
	return nil, false
}

func usage(v any) any {
	raw, ok := nativeUsage(v)
	if !ok {
		return nil
	}
	source := raw
	if has(raw, "total") {
		source = jAt(raw, "total")
	}
	fields := M{}
	for _, m := range [][2]string{{"inputTokens", "input_tokens"}, {"outputTokens", "output_tokens"}, {"cachedInputTokens", "cached_input_tokens"},
		{"cacheWriteInputTokens", "cache_write_input_tokens"}, {"reasoningOutputTokens", "reasoning_output_tokens"}, {"totalTokens", "total_tokens"},
		{"cachedReadTokens", "cache_read_tokens"}, {"cachedWriteTokens", "cache_write_tokens"}, {"thoughtTokens", "thought_tokens"},
		{"input_tokens", "input_tokens"}, {"output_tokens", "output_tokens"}, {"cached_input_tokens", "cached_input_tokens"}} {
		if n := jAt(source, m[0]); n != nil {
			if _, isNum := num(n); isNum {
				fields[m[1]] = n
			}
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func project(h string, v M) M {
	var session any
	switch h {
	case "codex":
		session = v["thread_id"]
	case "claude":
		session = v["session_id"]
	default:
		switch {
		case has(v, "conversation_id"):
			session = v["conversation_id"]
		case has(v, "conversationId"):
			session = v["conversationId"]
		case has(jAt(v, "result"), "conversation_id"):
			session = jAt(v, "result", "conversation_id")
		case v["type"] == "conversation.started" || v["type"] == "system" || v["type"] == "result":
			session = v["session_id"]
		}
	}
	var success bool
	switch h {
	case "codex":
		success = v["type"] == "turn.completed"
	case "claude":
		success = v["type"] == "result" && v["is_error"] == false
	default:
		success = (v["event"] == "result" && jAt(v, "result", "status") == "SUCCESS") || v["type"] == "turn.completed"
	}
	message := v["item"]
	switch h {
	case "claude":
		message = v["message"]
	case "antigravity", "acp":
		message = first(v, "step_update", "result")
	}
	errv := v["error"]
	if !has(v, "error") {
		errv = jAt(v, "result", "error")
	}
	nu, _ := nativeUsage(v)
	var context any
	if jAt(v, "update", "sessionUpdate") == "usage_update" {
		context = M{"used": jAt(v, "update", "used"), "capacity": jAt(v, "update", "size"), "scope": "native context window; not additive billing usage"}
	}
	scope := "native report; scope unknown, may be cumulative; not additive"
	if h == "codex" && has(jAt(v, "usage"), "total") {
		scope = "native thread cumulative total; not additive"
	}
	return M{"harness": h, "session": session, "completed": success, "lifecycle": first(v, "type", "event"), "error": errv, "message": message,
		"tools": toolsOf(h, v), "usage": usage(v), "nativeUsage": nu, "contextUsage": context, "usageScope": scope, "native": v}
}

type openTool struct {
	name, input any
	at          uint64
	seq         int
}

type stream struct{ kind, id, text string }

type blocks struct {
	tools   map[string]*openTool
	streams []*stream
	opened  int
	seq     int
}

func textOf(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case []any:
		var parts []string
		for _, e := range x {
			if s := textOf(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case M:
		return textOf(x["text"])
	}
	return ""
}

func blockKey(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return enc(v)
}

func textBlock(kind, markdown string) []any {
	if markdown == "" {
		return nil
	}
	if kind == "text" {
		return []any{contract.TranscriptText{Kind: "text", Role: "agent", Markdown: markdown}}
	}
	return []any{contract.TranscriptReasoning{Kind: "reasoning", Markdown: markdown}}
}

// label: a native name or state as text; nil is def, a non-string value its JSON.
func label(v any, def string) string {
	switch x := v.(type) {
	case nil:
		return def
	case string:
		return x
	}
	return enc(v)
}

// msOf: a native or recorded time in ms, exact for integers and rounded for fractions; nil when not a number.
func msOf(v any) *int64 {
	if n, ok := v.(json.Number); ok {
		if t, err := n.Int64(); err == nil {
			return &t
		}
	}
	if u, ok := v.(uint64); ok && u <= math.MaxInt64 {
		t := int64(u)
		return &t
	}
	if f, ok := num(v); ok && math.Abs(f) < math.MaxInt64 {
		t := int64(math.Round(f))
		return &t
	}
	return nil
}

func toolBlock(name, input, output, state, started, ended any) contract.TranscriptTool {
	return contract.TranscriptTool{Kind: "tool", Name: label(name, "tool"), Input: json.RawMessage(enc(input)), Output: json.RawMessage(enc(output)),
		State: label(state, "completed"), StartedAt: msOf(started), EndedAt: msOf(ended)}
}

// Project is project() plus `blocks` (and `deltaOf` / `closes` for streams); at is the record's time in ms.
func (b *blocks) Project(h string, v map[string]any, at uint64) map[string]any {
	p := project(h, v)
	if kind, text, ok := b.chunk(h, v); ok {
		var s *stream
		for _, x := range b.streams {
			if x.kind == kind {
				s = x
			}
		}
		if s != nil {
			s.text += text
		} else {
			b.opened++
			s = &stream{kind, kind + ":" + itoa(b.opened), text}
			b.streams = append(b.streams, s)
			p["opens"] = true
		}
		p["deltaOf"] = s.id
		p["blocks"] = append([]any{}, textBlock(kind, text)...)
		return p
	}
	out := []any{}
	streamed := false
	if len(b.streams) > 0 {
		ids := []any{}
		for _, s := range b.streams {
			ids = append(ids, s.id)
			streamed = streamed || s.kind == "text"
			out = append(out, textBlock(s.kind, s.text)...)
		}
		p["closes"] = ids
		b.streams = nil
	}
	_, response := jAt(v, "result", "response").(string)
	for _, x := range b.own(h, v, at) {
		// The step format repeats the streamed reply in result.response: the closed stream already holds it.
		if _, text := x.(contract.TranscriptText); !(streamed && response && text) {
			out = append(out, x)
		}
	}
	p["blocks"] = out
	return p
}

func itoa(n int) string { return strings.TrimSpace(enc(n)) }

func (b *blocks) chunk(h string, v M) (string, string, bool) {
	if !isACP(h) {
		return "", "", false
	}
	switch jAt(v, "update", "sessionUpdate") {
	case "agent_message_chunk":
		return "text", textOf(jAt(v, "update", "content")), true
	case "agent_thought_chunk":
		return "reasoning", textOf(jAt(v, "update", "content")), true
	}
	t, ok := str(jAt(v, "step_update", "text_delta"))
	return "text", t, ok
}

func (b *blocks) start(id string, name, input any, at uint64) *openTool {
	b.seq++
	t := &openTool{name, input, at, b.seq}
	b.tools[id] = t
	return t
}

func (b *blocks) own(h string, v M, at uint64) []any {
	t := jStr(v["type"])
	if t == "unparsed" || strings.HasSuffix(t, ".failed") || (h == "claude" && t == "result" && v["is_error"] == true) {
		return []any{toolBlock(t, nil, v, "failed", at, at)}
	}
	out := []any{}
	// A turn end settles tools that never reported an end, so a cut-off tool still shows.
	if t == "turn.completed" || t == "turn.failed" || t == "result" || v["event"] == "result" {
		open := make([]*openTool, 0, len(b.tools))
		for _, x := range b.tools {
			open = append(open, x)
		}
		sort.Slice(open, func(i, j int) bool {
			if open[i].at != open[j].at {
				return open[i].at < open[j].at
			}
			return open[i].seq < open[j].seq
		})
		for _, x := range open {
			out = append(out, toolBlock(x.name, x.input, nil, "unfinished", x.at, at))
		}
		b.tools = map[string]*openTool{}
	}
	switch {
	case h == "codex" && t == "item.completed":
		i := jAt(v, "item")
		started, ended := any(at), any(at)
		if _, ok := num(jAt(i, "startedAtMs")); ok {
			started = jAt(i, "startedAtMs")
		}
		if _, ok := num(jAt(i, "completedAtMs")); ok {
			ended = jAt(i, "completedAtMs")
		}
		switch kind := jStr(jAt(i, "type")); kind {
		case "agent_message", "agentMessage":
			out = append(out, textBlock("text", textOf(jAt(i, "text")))...)
		case "reasoning", "plan":
			var parts []string
			for _, k := range []string{"text", "summary", "content"} {
				if s := textOf(jAt(i, k)); s != "" {
					parts = append(parts, s)
				}
			}
			out = append(out, textBlock("reasoning", strings.Join(parts, "\n"))...)
		case "userMessage", "user_message":
		default:
			name := first(i, "tool", "name")
			if name == nil {
				name = kind
			}
			state := jAt(i, "status")
			if kind == "error" {
				state = "failed"
			}
			out = append(out, toolBlock(name, first(i, "arguments", "command", "changes"), first(i, "result", "aggregatedOutput", "aggregated_output", "output", "error", "message"), state, started, ended))
		}
	case h == "claude":
		content, _ := jAt(v, "message", "content").([]any)
		for _, c := range content {
			switch jAt(c, "type") {
			case "text":
				if t == "assistant" {
					out = append(out, textBlock("text", textOf(jAt(c, "text")))...)
				}
			case "thinking":
				if t == "assistant" {
					out = append(out, textBlock("reasoning", textOf(jAt(c, "thinking")))...)
				}
			case "tool_use":
				b.start(blockKey(jAt(c, "id")), jAt(c, "name"), jAt(c, "input"), at)
			case "tool_result":
				k := blockKey(jAt(c, "tool_use_id"))
				x, ok := b.tools[k]
				if !ok {
					x = &openTool{at: at}
				}
				delete(b.tools, k)
				state := "completed"
				if jAt(c, "is_error") == true {
					state = "failed"
				}
				out = append(out, toolBlock(x.name, x.input, jAt(c, "content"), state, x.at, at))
			}
		}
	case isACP(h):
		if r, ok := str(jAt(v, "result", "response")); ok {
			out = append(out, textBlock("text", r)...)
		}
		u := jAt(v, "update")
		if su := jAt(u, "sessionUpdate"); su == "tool_call" || su == "tool_call_update" {
			id := blockKey(jAt(u, "toolCallId"))
			x, ok := b.tools[id]
			if !ok {
				x = b.start(id, first(u, "title", "name"), jAt(u, "rawInput"), at)
			}
			if x.name == nil {
				x.name = jAt(u, "title")
			}
			if x.input == nil {
				x.input = jAt(u, "rawInput")
			}
			if st := jAt(u, "status"); st == "completed" || st == "failed" {
				delete(b.tools, id)
				output := jAt(u, "content")
				if nonEmptyObj(jAt(u, "rawOutput")) {
					output = jAt(u, "rawOutput")
				}
				out = append(out, toolBlock(x.name, x.input, output, st, x.at, at))
			}
		}
	}
	return out
}

const maxToolEntries = 32

// runMetrics accumulates non-gateway tool calls, turn counts, and active duration from normalized harness events.
type runMetrics struct {
	gatewayConns     map[string]bool
	tools            map[string]*contract.ToolCallCounter
	toolNameByID     map[string]string
	seenToolIDs      map[string]bool
	erroredToolIDs   map[string]bool
	ignoredToolIDs   map[string]bool
	usageSteps       int64
	assistantSteps   int64
	completedTurns   int64
	reportedTurns    int64
	hasAssistantItem  bool
	inAssistantStep   bool
	hasReportedActive bool
	reportedActiveMs  int64
	firstActivityMs   int64
	lastActivityMs    int64
}

func newRunMetrics(gatewayConns map[string]bool) *runMetrics {
	return &runMetrics{
		gatewayConns:   gatewayConns,
		tools:          map[string]*contract.ToolCallCounter{},
		toolNameByID:   map[string]string{},
		seenToolIDs:    map[string]bool{},
		erroredToolIDs: map[string]bool{},
		ignoredToolIDs: map[string]bool{},
	}
}

func (m *runMetrics) isGatewayTool(server, rawName string) bool {
	if len(m.gatewayConns) == 0 {
		return false
	}
	if server != "" && m.gatewayConns[server] {
		return true
	}
	if strings.HasPrefix(rawName, "mcp__") {
		if srv, tool, ok := strings.Cut(rawName[5:], "__"); ok && srv != "" && tool != "" && m.gatewayConns[srv] {
			return true
		}
	}
	for conn := range m.gatewayConns {
		if conn != "" && (strings.HasPrefix(rawName, conn+"/") || strings.HasPrefix(rawName, conn+":")) {
			return true
		}
	}
	return false
}

func canonicalToolName(server, rawName string) string {
	if strings.HasPrefix(rawName, "mcp__") {
		if srv, tool, ok := strings.Cut(rawName[5:], "__"); ok && srv != "" && tool != "" {
			return srv + "/" + tool
		}
	}
	trimmed := strings.TrimSpace(rawName)
	if server != "" && trimmed != "" && !strings.Contains(trimmed, "/") {
		return server + "/" + trimmed
	}
	return trimmed
}

func (m *runMetrics) touch(atMs int64) {
	if atMs <= 0 {
		return
	}
	if m.firstActivityMs == 0 || atMs < m.firstActivityMs {
		m.firstActivityMs = atMs
	}
	if atMs > m.lastActivityMs {
		m.lastActivityMs = atMs
	}
}

func (m *runMetrics) recordCall(id, server, rawName string) (string, bool) {
	if m.isGatewayTool(server, rawName) {
		if id != "" {
			m.ignoredToolIDs[id] = true
		}
		return "", false
	}
	if id != "" && (m.seenToolIDs[id] || m.ignoredToolIDs[id]) {
		return m.toolNameByID[id], false
	}
	tname := canonicalToolName(server, rawName)
	if tname == "" {
		tname = "tool"
	}
	if id != "" {
		m.seenToolIDs[id] = true
		m.toolNameByID[id] = tname
	}
	c := m.tools[tname]
	if c == nil {
		c = &contract.ToolCallCounter{}
		m.tools[tname] = c
	}
	c.Calls++
	return tname, true
}

func toolID(prefix string, v any) string {
	if v == nil {
		return ""
	}
	k := blockKey(v)
	if k == "" || k == "null" {
		return ""
	}
	return prefix + ":" + k
}

func (m *runMetrics) recordErrorByID(id string) {
	if id == "" || m.ignoredToolIDs[id] || m.erroredToolIDs[id] {
		return
	}
	tname := m.toolNameByID[id]
	if tname == "" {
		return
	}
	if id != "" {
		m.erroredToolIDs[id] = true
	}
	c := m.tools[tname]
	if c == nil {
		c = &contract.ToolCallCounter{}
		m.tools[tname] = c
	}
	c.Errors++
}

func (m *runMetrics) observeEvent(h string, v M, atMs int64) {
	t := jStr(v, "type")
	if t == "unparsed" ||
		t == string(contract.FleetEventTypeProtocolWrite) ||
		t == string(contract.FleetEventTypeProtocolFailed) ||
		t == string(contract.FleetEventTypeLimitReading) ||
		t == "steer.result" ||
		(strings.HasSuffix(t, ".failed") && t != "turn.failed") {
		return
	}
	switch {
	case h == "codex":
		m.observeCodex(t, v, atMs)
	case h == "claude":
		m.observeClaude(t, v, atMs)
	case isACP(h):
		m.observeACP(t, v, atMs)
	}
}

func (m *runMetrics) observeCodex(t string, v M, atMs int64) {
	switch t {
	case "thread.started", "item.completed", "usage.observation", "turn.completed", "turn.failed":
		m.touch(atMs)
	}
	switch t {
	case "usage.observation":
		m.usageSteps++
	case "turn.completed", "turn.failed":
		m.completedTurns++
		if d, ok := num(first(v, "durationMs")); ok && d >= 0 {
			m.hasReportedActive = true
			m.reportedActiveMs += int64(math.Round(d))
		} else if d, ok := num(jAt(v, "native", "params", "turn", "durationMs")); ok && d >= 0 {
			m.hasReportedActive = true
			m.reportedActiveMs += int64(math.Round(d))
		}
	case "item.completed":
		i := jAt(v, "item")
		kind := jStr(jAt(i, "type"))
		switch kind {
		case "agent_message", "agentMessage", "reasoning", "plan":
			m.hasAssistantItem = true
		case "command_execution", "commandExecution", "mcp_tool_call", "mcpToolCall", "function_call", "collabAgentToolCall":
			srv := jStr(jAt(i, "server"))
			rawName := label(first(i, "tool", "name"), kind)
			id := toolID("codex", jAt(i, "id"))
			tname, counted := m.recordCall(id, srv, rawName)
			if !counted || tname == "" {
				return
			}
			failed := jStr(jAt(i, "status")) == "failed" || jAt(i, "error") != nil
			if !failed && (kind == "command_execution" || kind == "commandExecution") {
				if ec, ok := num(first(i, "exitCode", "exit_code")); ok && ec != 0 {
					failed = true
				}
			}
			if failed {
				if id != "" {
					m.erroredToolIDs[id] = true
				}
				m.tools[tname].Errors++
			}
		}
	}
}

func (m *runMetrics) observeClaude(t string, v M, atMs int64) {
	if t == "assistant" || t == "user" || t == "result" {
		m.touch(atMs)
	}
	switch t {
	case "assistant":
		m.assistantSteps++
	case "result":
		if n, ok := num(v["num_turns"]); ok && n > 0 {
			m.reportedTurns = max(m.reportedTurns, int64(math.Round(n)))
		}
		if d, ok := num(v["duration_ms"]); ok && d >= 0 {
			m.hasReportedActive = true
			m.reportedActiveMs += int64(math.Round(d))
		}
		return
	}
	if t != "assistant" && t != "user" {
		return
	}
	content, _ := jAt(v, "message", "content").([]any)
	for _, c := range content {
		switch jAt(c, "type") {
		case "tool_use":
			id := toolID("claude", jAt(c, "id"))
			rawName := label(jAt(c, "name"), "tool")
			m.recordCall(id, "", rawName)
		case "tool_result":
			if jAt(c, "is_error") == true {
				id := toolID("claude", jAt(c, "tool_use_id"))
				m.recordErrorByID(id)
			}
		}
	}
}

func (m *runMetrics) observeACP(t string, v M, atMs int64) {
	if t == "session.started" || t == "configuration.applied" || t == "acp.update" ||
		t == "turn.completed" || t == "turn.failed" || t == "PLANNER_RESPONSE" ||
		t == "TOOL_RESPONSE" || has(v, "step_update") || v["event"] == "result" {
		m.touch(atMs)
	}
	switch {
	case t == "PLANNER_RESPONSE":
		m.assistantSteps++
		m.inAssistantStep = false
		for _, tc := range jArr(v, "tool_calls") {
			id := toolID("call", jAt(tc, "id"))
			rawName := label(jAt(tc, "name"), "tool")
			m.recordCall(id, "", rawName)
		}
	case t == "TOOL_RESPONSE":
		st := jStr(v, "status")
		if st == "ERROR" || st == "failed" {
			id := toolID("call", jAt(v, "tool_call_id"))
			m.recordErrorByID(id)
		}
	case t == "acp.update":
		u := jAt(v, "update")
		su := jStr(jAt(u, "sessionUpdate"))
		if (su == "agent_message_chunk" || su == "agent_thought_chunk" || su == "tool_call") && !m.inAssistantStep {
			m.inAssistantStep = true
			m.assistantSteps++
		}
		if su == "tool_call" || su == "tool_call_update" {
			id := toolID("acp", jAt(u, "toolCallId"))
			rawName := jStr(first(u, "name", "title"))
			if su == "tool_call" || rawName != "" || id != "" {
				m.recordCall(id, "", rawName)
			}
			st := jStr(jAt(u, "status"))
			if st == "completed" || st == "failed" {
				m.inAssistantStep = false
			}
			if st == "failed" {
				m.recordErrorByID(id)
			}
		}
	case has(v, "step_update"):
		su := jAt(v, "step_update")
		if _, ok := str(jAt(su, "text_delta")); ok && !m.inAssistantStep {
			m.inAssistantStep = true
			m.assistantSteps++
		}
		if jAt(su, "step_type") == "tool" {
			m.inAssistantStep = false
			id := toolID("step", jAt(su, "step_index"))
			rawName := label(jAt(su, "tool_name"), "tool")
			m.recordCall(id, "", rawName)
			st := jStr(jAt(su, "state"))
			if st == "failed" || st == "ERROR" {
				m.recordErrorByID(id)
			}
		}
	case t == "turn.completed" || t == "turn.failed" || v["event"] == "result":
		m.completedTurns++
		m.inAssistantStep = false
		if d, ok := num(v["durationMs"]); ok && d >= 0 {
			m.hasReportedActive = true
			m.reportedActiveMs += int64(math.Round(d))
		}
	}
}

func (m *runMetrics) applyTo(h string, out *contract.ExecutionResourceSummary) {
	var turns int64
	if h == "codex" {
		switch {
		case m.usageSteps > 0:
			turns = m.usageSteps
		case m.completedTurns > 0:
			turns = m.completedTurns
		case m.hasAssistantItem || len(m.tools) > 0:
			turns = 1
		}
	} else {
		turns = max(m.reportedTurns, max(m.assistantSteps, m.completedTurns))
	}
	if turns > 0 {
		out.TurnCount = &turns
	}
	if m.hasReportedActive {
		active := m.reportedActiveMs
		out.ActiveMs = &active
	} else if m.firstActivityMs > 0 && m.lastActivityMs >= m.firstActivityMs {
		active := m.lastActivityMs - m.firstActivityMs
		out.ActiveMs = &active
	}
	if len(m.tools) == 0 {
		return
	}
	names := make([]string, 0, len(m.tools))
	for name := range m.tools {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		a, b := m.tools[names[i]], m.tools[names[j]]
		if a.Errors != b.Errors {
			return a.Errors > b.Errors
		}
		if a.Calls != b.Calls {
			return a.Calls > b.Calls
		}
		return names[i] < names[j]
	})
	keep := min(len(names), maxToolEntries)
	out.ToolCalls = make(map[string]contract.ToolCallCounter, keep)
	for _, name := range names[:keep] {
		out.ToolCalls[name] = *m.tools[name]
	}
	if len(names) > maxToolEntries {
		var other contract.ToolCallCounter
		for _, name := range names[maxToolEntries:] {
			c := m.tools[name]
			other.Calls += c.Calls
			other.Errors += c.Errors
		}
		out.ToolCallsOther = &other
	}
}
