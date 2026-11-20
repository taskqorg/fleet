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

func nonEmptyObj(x any) bool { o, ok := x.(M); return x != nil && !(ok && len(o) == 0) }

func toolsOf(h string, e harnessEvent) []any {
	switch u, step := typed[acpUpdate](e.Update), typed[acpStepUpdate](e.StepUpdate); {
	case h == "claude":
		out := []any{}
		for _, b := range claudeContentOf(e) {
			if b.Type == "tool_use" || b.Type == "tool_result" {
				out = append(out, M{"id": firstRaw(b.ID, b.ToolUseID), "name": b.Name, "arguments": b.Input, "result": b.Content, "error": b.IsError})
			}
		}
		return out
	case isACP(h) && (u.SessionUpdate == "tool_call" || u.SessionUpdate == "tool_call_update"):
		result := any(nil)
		if u.Content.Set {
			result = u.Content.V
		} else {
			result = u.RawOutput
		}
		if nonEmptyObj(u.RawOutput) {
			result = u.RawOutput
		}
		return []any{M{"id": u.ToolCallID, "name": firstRaw(u.Name, u.Title), "arguments": u.RawInput, "result": result, "status": u.Status, "kind": u.Kind}}
	case isACP(h) && step.StepType == "tool":
		return []any{M{"id": step.StepIndex, "name": step.ToolName, "arguments": step.ToolInfo.Parameters, "result": step.ToolInfo.Result, "status": step.State}}
	case isACP(h) && e.kind() == "PLANNER_RESPONSE":
		out := []any{}
		for _, b := range e.ToolCalls {
			args := b.Args
			if b.Arguments.Set {
				args = b.Arguments.V
			}
			out = append(out, M{"id": b.ID, "name": b.Name, "arguments": args, "result": nil, "status": "requested"})
		}
		return out
	case isACP(h) && e.kind() == "TOOL_RESPONSE":
		return []any{M{"id": e.ToolCallID, "name": nil, "arguments": nil, "result": firstRaw(e.Content, e.Result), "status": e.Status.V}}
	case h == "codex":
		b := typed[codexItem](e.Item)
		switch b.Type {
		case "command_execution", "mcp_tool_call", "function_call", "function_call_output", "commandExecution", "mcpToolCall", "collabAgentToolCall":
			name := firstRaw(b.Tool, b.Name)
			if !b.Tool.Set && !b.Name.Set {
				name = b.Type
			}
			return []any{M{"id": b.ID, "name": name, "arguments": firstRaw(b.Arguments, b.Command),
				"result": firstRaw(b.Result, b.AggregatedOutput, b.AggregatedOutputSnake, b.Output), "status": b.Status, "exitCode": firstRaw(b.ExitCode, b.ExitCodeSnake)}}
		}
	}
	return []any{}
}

func nativeUsage(e harnessEvent) (any, bool) {
	if e.Usage.Set {
		return e.Usage.V, true
	}
	if r := e.result(); r.Usage.Set {
		return r.Usage.V, true
	}
	return nil, false
}

func usage(e harnessEvent) any {
	raw, ok := nativeUsage(e)
	if !ok {
		return nil
	}
	counts := typed[usageCounts](raw)
	if counts.Total.Set {
		counts = typed[usageCounts](counts.Total)
	}
	fields := M{}
	for _, c := range counts.fields() {
		if c.n != nil {
			if _, isNum := num(c.n); isNum {
				fields[c.name] = c.n
			}
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

func project(h string, v M) M { return projectEvent(h, v, typed[harnessEvent](v)) }

// projectEvent: the projection of v, whose typed view is e.
func projectEvent(h string, v M, e harnessEvent) M {
	t, res := e.kind(), e.result()
	var session any
	switch h {
	case "codex":
		session = e.ThreadID
	case "claude":
		session = e.SessionID
	default:
		switch {
		case e.ConversationID.Set:
			session = e.ConversationID.V
		case e.ConversationIDCamel.Set:
			session = e.ConversationIDCamel.V
		case res.ConversationID.Set:
			session = res.ConversationID.V
		case t == "conversation.started" || t == "system" || t == "result":
			session = e.SessionID
		}
	}
	var success bool
	switch h {
	case "codex":
		success = t == "turn.completed"
	case "claude":
		success = t == "result" && e.IsError != nil && !*e.IsError
	default:
		success = (e.event() == "result" && res.Status == "SUCCESS") || t == "turn.completed"
	}
	message := e.Item.V
	switch h {
	case "claude":
		message = e.Message.V
	case "antigravity", "acp":
		message = firstRaw(e.StepUpdate, e.Result)
	}
	errv := e.Error.V
	if !e.Error.Set {
		errv = res.Error.V
	}
	nu, _ := nativeUsage(e)
	var context any
	if u := typed[acpUpdate](e.Update); u.SessionUpdate == "usage_update" {
		context = M{"used": u.Used, "capacity": u.Size, "scope": "native context window; not additive billing usage"}
	}
	scope := "native report; scope unknown, may be cumulative; not additive"
	if h == "codex" && typed[usageCounts](e.Usage.V).Total.Set {
		scope = "native thread cumulative total; not additive"
	}
	return M{"harness": h, "session": session, "completed": success, "lifecycle": firstRaw(e.Type, e.Event), "error": errv, "message": message,
		"tools": toolsOf(h, e), "usage": usage(e), "nativeUsage": nu, "contextUsage": context, "usageScope": scope, "native": v}
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
	e := typed[harnessEvent](v)
	p := projectEvent(h, v, e)
	if kind, text, ok := b.chunk(h, e); ok {
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
	response := e.result().Response != nil
	for _, x := range b.own(h, v, e, at) {
		// The step format repeats the streamed reply in result.response: the closed stream already holds it.
		if _, text := x.(contract.TranscriptText); !(streamed && response && text) {
			out = append(out, x)
		}
	}
	p["blocks"] = out
	return p
}

func itoa(n int) string { return strings.TrimSpace(enc(n)) }

func (b *blocks) chunk(h string, e harnessEvent) (string, string, bool) {
	if !isACP(h) {
		return "", "", false
	}
	switch u := typed[acpUpdate](e.Update); u.SessionUpdate {
	case "agent_message_chunk":
		return "text", textOf(u.Content.V), true
	case "agent_thought_chunk":
		return "reasoning", textOf(u.Content.V), true
	}
	if t := typed[acpStepUpdate](e.StepUpdate).TextDelta; t != nil {
		return "text", *t, true
	}
	return "text", "", false
}

func (b *blocks) start(id string, name, input any, at uint64) *openTool {
	b.seq++
	t := &openTool{name, input, at, b.seq}
	b.tools[id] = t
	return t
}

// own: the blocks of v itself, whose typed view is e.
func (b *blocks) own(h string, v M, e harnessEvent, at uint64) []any {
	t := e.kind()
	isError := e.IsError != nil && *e.IsError
	if t == "unparsed" || strings.HasSuffix(t, ".failed") || (h == "claude" && t == "result" && isError) {
		return []any{toolBlock(t, nil, v, "failed", at, at)}
	}
	out := []any{}
	// A turn end settles tools that never reported an end, so a cut-off tool still shows.
	if t == "turn.completed" || t == "turn.failed" || t == "result" || e.event() == "result" {
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
		i := typed[codexItem](e.Item)
		started, ended := any(at), any(at)
		if _, ok := num(i.StartedAtMs); ok {
			started = i.StartedAtMs
		}
		if _, ok := num(i.CompletedAtMs); ok {
			ended = i.CompletedAtMs
		}
		switch kind := i.Type; kind {
		case "agent_message", "agentMessage":
			out = append(out, textBlock("text", textOf(i.Text))...)
		case "reasoning", "plan":
			var parts []string
			for _, x := range []any{i.Text, i.Summary, i.Content} {
				if s := textOf(x); s != "" {
					parts = append(parts, s)
				}
			}
			out = append(out, textBlock("reasoning", strings.Join(parts, "\n"))...)
		case "userMessage", "user_message":
		default:
			name := firstRaw(i.Tool, i.Name)
			if name == nil {
				name = kind
			}
			state := i.Status
			if kind == "error" {
				state = "failed"
			}
			out = append(out, toolBlock(name, firstRaw(i.Arguments, i.Command, i.Changes), firstRaw(i.Result, i.AggregatedOutput, i.AggregatedOutputSnake, i.Output, i.Error, i.Message), state, started, ended))
		}
	case h == "claude":
		for _, c := range claudeContentOf(e) {
			switch c.Type {
			case "text":
				if t == "assistant" {
					out = append(out, textBlock("text", textOf(c.Text))...)
				}
			case "thinking":
				if t == "assistant" {
					out = append(out, textBlock("reasoning", textOf(c.Thinking))...)
				}
			case "tool_use":
				b.start(blockKey(c.ID.V), c.Name, c.Input, at)
			case "tool_result":
				k := blockKey(c.ToolUseID.V)
				x, ok := b.tools[k]
				if !ok {
					x = &openTool{at: at}
				}
				delete(b.tools, k)
				state := "completed"
				if c.IsError == true {
					state = "failed"
				}
				out = append(out, toolBlock(x.name, x.input, c.Content, state, x.at, at))
			}
		}
	case isACP(h):
		if r := e.result().Response; r != nil {
			out = append(out, textBlock("text", *r)...)
		}
		u := typed[acpUpdate](e.Update)
		if su := u.SessionUpdate; su == "tool_call" || su == "tool_call_update" {
			id := blockKey(u.ToolCallID)
			x, ok := b.tools[id]
			if !ok {
				x = b.start(id, firstRaw(u.Title, u.Name), u.RawInput, at)
			}
			if x.name == nil {
				x.name = u.Title.V
			}
			if x.input == nil {
				x.input = u.RawInput
			}
			if st := u.Status; st == "completed" || st == "failed" {
				delete(b.tools, id)
				output := u.Content.V
				if nonEmptyObj(u.RawOutput) {
					output = u.RawOutput
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
	gatewayConns      map[string]bool
	tools             map[string]*contract.ToolCallCounter
	toolNameByID      map[string]string
	seenToolIDs       map[string]bool
	erroredToolIDs    map[string]bool
	ignoredToolIDs    map[string]bool
	usageSteps        int64
	assistantSteps    int64
	completedTurns    int64
	reportedTurns     int64
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
	m.erroredToolIDs[id] = true
	c := m.tools[tname]
	if c == nil {
		c = &contract.ToolCallCounter{}
		m.tools[tname] = c
	}
	c.Errors++
}

func (m *runMetrics) observeEvent(h string, v M, atMs int64) {
	e := typed[harnessEvent](v)
	t := e.kind()
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
		m.observeCodex(t, e, atMs)
	case h == "claude":
		m.observeClaude(t, e, atMs)
	case isACP(h):
		m.observeACP(t, e, atMs)
	}
}

func (m *runMetrics) observeCodex(t string, e harnessEvent, atMs int64) {
	switch t {
	case "thread.started", "item.completed", "usage.observation", "turn.completed", "turn.failed":
		m.touch(atMs)
	}
	switch t {
	case "usage.observation":
		m.usageSteps++
	case "turn.completed", "turn.failed":
		m.completedTurns++
		if d, ok := num(e.DurationMs); ok && d >= 0 {
			m.hasReportedActive = true
			m.reportedActiveMs += int64(math.Round(d))
		} else if d, ok := num(typed[codexParams](typed[rpcFrame](e.Native).Params).Turn.DurationMs); ok && d >= 0 {
			m.hasReportedActive = true
			m.reportedActiveMs += int64(math.Round(d))
		}
	case "item.completed":
		i := typed[codexItem](e.Item)
		kind := i.Type
		switch kind {
		case "agent_message", "agentMessage", "reasoning", "plan":
			m.hasAssistantItem = true
		case "command_execution", "commandExecution", "mcp_tool_call", "mcpToolCall", "function_call", "collabAgentToolCall":
			rawName := label(firstRaw(i.Tool, i.Name), kind)
			id := toolID("codex", i.ID)
			tname, counted := m.recordCall(id, i.Server, rawName)
			if !counted || tname == "" {
				return
			}
			status, _ := i.Status.(string)
			failed := status == "failed" || i.Error.V != nil
			if !failed && (kind == "command_execution" || kind == "commandExecution") {
				if ec, ok := num(firstRaw(i.ExitCode, i.ExitCodeSnake)); ok && ec != 0 {
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

func (m *runMetrics) observeClaude(t string, e harnessEvent, atMs int64) {
	if t == "assistant" || t == "user" || t == "result" {
		m.touch(atMs)
	}
	switch t {
	case "assistant":
		m.assistantSteps++
	case "result":
		if n, ok := num(e.NumTurns); ok && n > 0 {
			m.reportedTurns = max(m.reportedTurns, int64(math.Round(n)))
		}
		if d, ok := num(e.DurationMsSnake); ok && d >= 0 {
			m.hasReportedActive = true
			m.reportedActiveMs += int64(math.Round(d))
		}
		return
	}
	if t != "assistant" && t != "user" {
		return
	}
	for _, c := range claudeContentOf(e) {
		switch c.Type {
		case "tool_use":
			id := toolID("claude", c.ID.V)
			rawName := label(c.Name, "tool")
			m.recordCall(id, "", rawName)
		case "tool_result":
			if c.IsError == true {
				id := toolID("claude", c.ToolUseID.V)
				m.recordErrorByID(id)
			}
		}
	}
}

func (m *runMetrics) observeACP(t string, e harnessEvent, atMs int64) {
	hasStep := e.StepUpdate.Set
	if t == "session.started" || t == "configuration.applied" || t == "acp.update" ||
		t == "turn.completed" || t == "turn.failed" || t == "PLANNER_RESPONSE" ||
		t == "TOOL_RESPONSE" || hasStep || e.event() == "result" {
		m.touch(atMs)
	}
	switch {
	case t == "PLANNER_RESPONSE":
		m.assistantSteps++
		m.inAssistantStep = false
		for _, tc := range e.ToolCalls {
			id := toolID("call", tc.ID)
			rawName := label(tc.Name, "tool")
			m.recordCall(id, "", rawName)
		}
	case t == "TOOL_RESPONSE":
		st, _ := rawStr(e.Status)
		if st == "ERROR" || st == "failed" {
			id := toolID("call", e.ToolCallID)
			m.recordErrorByID(id)
		}
	case t == "acp.update":
		u := typed[acpUpdate](e.Update)
		su := u.SessionUpdate
		if (su == "agent_message_chunk" || su == "agent_thought_chunk" || su == "tool_call") && !m.inAssistantStep {
			m.inAssistantStep = true
			m.assistantSteps++
		}
		if su == "tool_call" || su == "tool_call_update" {
			id := toolID("acp", u.ToolCallID)
			rawName, _ := firstRaw(u.Name, u.Title).(string)
			if su == "tool_call" || rawName != "" || id != "" {
				m.recordCall(id, "", rawName)
			}
			st, _ := u.Status.(string)
			if st == "completed" || st == "failed" {
				m.inAssistantStep = false
			}
			if st == "failed" {
				m.recordErrorByID(id)
			}
		}
	case hasStep:
		su := typed[acpStepUpdate](e.StepUpdate)
		if su.TextDelta != nil && !m.inAssistantStep {
			m.inAssistantStep = true
			m.assistantSteps++
		}
		if su.StepType == "tool" {
			m.inAssistantStep = false
			id := toolID("step", su.StepIndex)
			rawName := label(su.ToolName, "tool")
			m.recordCall(id, "", rawName)
			st, _ := su.State.(string)
			if st == "failed" || st == "ERROR" {
				m.recordErrorByID(id)
			}
		}
	case t == "turn.completed" || t == "turn.failed" || e.event() == "result":
		m.completedTurns++
		m.inAssistantStep = false
		if d, ok := num(e.DurationMs); ok && d >= 0 {
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
