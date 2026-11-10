package main

// Event projection and typed transcript blocks (docs/designs/fleet-conversations.md, "Block
// model"): text, reasoning and tool, the only producer of reply text. State per execution: tools started and not yet
// ended (claude tool_use, ACP tool_call) with their start time, and the open streams (ACP chunks). A chunk record carries
// `deltaOf` and its chunk as a block; the next record that is not a chunk closes every open stream, carries the joined
// final blocks first, then its own, and names the closed streams in `closes`. The first chunk of a stream also carries
// `opens: true`.

import (
	"sort"
	"strings"
)

// first returns the value of the first key present in v (Rust's get(a).or(get(b))).
func first(v any, keys ...string) any {
	for _, k := range keys {
		if has(v, k) {
			return get(v, k)
		}
	}
	return nil
}

func nonEmptyObj(x any) bool { o, ok := x.(obj); return x != nil && !(ok && len(o) == 0) }

func toolsOf(h string, v any) []any {
	switch {
	case h == "claude":
		out := []any{}
		content, _ := get(v, "message", "content").([]any)
		for _, b := range content {
			if t := get(b, "type"); t == "tool_use" || t == "tool_result" {
				out = append(out, obj{"id": first(b, "id", "tool_use_id"), "name": get(b, "name"), "arguments": get(b, "input"), "result": get(b, "content"), "error": get(b, "is_error")})
			}
		}
		return out
	case isACP(h) && (get(v, "update", "sessionUpdate") == "tool_call" || get(v, "update", "sessionUpdate") == "tool_call_update"):
		b := get(v, "update")
		result := first(b, "content", "rawOutput")
		if nonEmptyObj(get(b, "rawOutput")) {
			result = get(b, "rawOutput")
		}
		return []any{obj{"id": get(b, "toolCallId"), "name": first(b, "name", "title"), "arguments": get(b, "rawInput"), "result": result, "status": get(b, "status"), "kind": get(b, "kind")}}
	case isACP(h) && get(v, "step_update", "step_type") == "tool":
		b := get(v, "step_update")
		return []any{obj{"id": get(b, "step_index"), "name": get(b, "tool_name"), "arguments": get(b, "tool_info", "parameters"), "result": get(b, "tool_info", "result"), "status": get(b, "state")}}
	case isACP(h) && get(v, "type") == "PLANNER_RESPONSE":
		out := []any{}
		calls, _ := get(v, "tool_calls").([]any)
		for _, b := range calls {
			out = append(out, obj{"id": get(b, "id"), "name": get(b, "name"), "arguments": first(b, "arguments", "args"), "result": nil, "status": "requested"})
		}
		return out
	case isACP(h) && get(v, "type") == "TOOL_RESPONSE":
		return []any{obj{"id": get(v, "tool_call_id"), "name": nil, "arguments": nil, "result": first(v, "content", "result"), "status": get(v, "status")}}
	case h == "codex":
		switch get(v, "item", "type") {
		case "command_execution", "mcp_tool_call", "function_call", "function_call_output", "commandExecution", "mcpToolCall", "collabAgentToolCall":
			b := get(v, "item")
			return []any{obj{"id": get(b, "id"), "name": first(b, "tool", "name", "type"), "arguments": first(b, "arguments", "command"),
				"result": first(b, "result", "aggregatedOutput", "aggregated_output", "output"), "status": get(b, "status"), "exitCode": first(b, "exitCode", "exit_code")}}
		}
	}
	return []any{}
}

func nativeUsage(v any) (any, bool) {
	if has(v, "usage") {
		return get(v, "usage"), true
	}
	if has(get(v, "result"), "usage") {
		return get(v, "result", "usage"), true
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
		source = get(raw, "total")
	}
	fields := obj{}
	for _, m := range [][2]string{{"inputTokens", "input_tokens"}, {"outputTokens", "output_tokens"}, {"cachedInputTokens", "cached_input_tokens"},
		{"cacheWriteInputTokens", "cache_write_input_tokens"}, {"reasoningOutputTokens", "reasoning_output_tokens"}, {"totalTokens", "total_tokens"},
		{"cachedReadTokens", "cache_read_tokens"}, {"cachedWriteTokens", "cache_write_tokens"}, {"thoughtTokens", "thought_tokens"},
		{"input_tokens", "input_tokens"}, {"output_tokens", "output_tokens"}, {"cached_input_tokens", "cached_input_tokens"}} {
		if n := get(source, m[0]); n != nil {
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

func project(h string, v obj) obj {
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
		case has(get(v, "result"), "conversation_id"):
			session = get(v, "result", "conversation_id")
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
		success = (v["event"] == "result" && get(v, "result", "status") == "SUCCESS") || v["type"] == "turn.completed"
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
		errv = get(v, "result", "error")
	}
	nu, _ := nativeUsage(v)
	var context any
	if get(v, "update", "sessionUpdate") == "usage_update" {
		context = obj{"used": get(v, "update", "used"), "capacity": get(v, "update", "size"), "scope": "native context window; not additive billing usage"}
	}
	scope := "native report; scope unknown, may be cumulative; not additive"
	if h == "codex" && has(get(v, "usage"), "total") {
		scope = "native thread cumulative total; not additive"
	}
	return obj{"harness": h, "session": session, "completed": success, "lifecycle": first(v, "type", "event"), "error": errv, "message": message,
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
	case obj:
		return textOf(x["text"])
	}
	return ""
}

func blockKey(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return encode(v)
}

func textBlock(kind, markdown string) []any {
	if markdown == "" {
		return nil
	}
	if kind == "text" {
		return []any{obj{"kind": "text", "role": "agent", "markdown": markdown}}
	}
	return []any{obj{"kind": "reasoning", "markdown": markdown}}
}

func toolBlock(name, input, output, state, started, ended any) obj {
	if name == nil {
		name = "tool"
	}
	if state == nil {
		state = "completed"
	}
	return obj{"kind": "tool", "name": name, "input": input, "output": output, "state": state, "startedAt": started, "endedAt": ended}
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
	_, response := get(v, "result", "response").(string)
	for _, x := range b.own(h, v, at) {
		// The step format repeats the streamed reply in result.response: the closed stream already holds it.
		if !(streamed && response && get(x, "kind") == "text") {
			out = append(out, x)
		}
	}
	p["blocks"] = out
	return p
}

func itoa(n int) string { return strings.TrimSpace(encode(n)) }

func (b *blocks) chunk(h string, v obj) (string, string, bool) {
	if !isACP(h) {
		return "", "", false
	}
	switch get(v, "update", "sessionUpdate") {
	case "agent_message_chunk":
		return "text", textOf(get(v, "update", "content")), true
	case "agent_thought_chunk":
		return "reasoning", textOf(get(v, "update", "content")), true
	}
	t, ok := str(get(v, "step_update", "text_delta"))
	return "text", t, ok
}

func (b *blocks) start(id string, name, input any, at uint64) *openTool {
	b.seq++
	t := &openTool{name, input, at, b.seq}
	b.tools[id] = t
	return t
}

func (b *blocks) own(h string, v obj, at uint64) []any {
	t := sv(v["type"])
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
		i := get(v, "item")
		started, ended := any(at), any(at)
		if _, ok := num(get(i, "startedAtMs")); ok {
			started = get(i, "startedAtMs")
		}
		if _, ok := num(get(i, "completedAtMs")); ok {
			ended = get(i, "completedAtMs")
		}
		switch kind := sv(get(i, "type")); kind {
		case "agent_message", "agentMessage":
			out = append(out, textBlock("text", textOf(get(i, "text")))...)
		case "reasoning", "plan":
			var parts []string
			for _, k := range []string{"text", "summary", "content"} {
				if s := textOf(get(i, k)); s != "" {
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
			state := get(i, "status")
			if kind == "error" {
				state = "failed"
			}
			out = append(out, toolBlock(name, first(i, "arguments", "command", "changes"), first(i, "result", "aggregatedOutput", "aggregated_output", "output", "error", "message"), state, started, ended))
		}
	case h == "claude":
		content, _ := get(v, "message", "content").([]any)
		for _, c := range content {
			switch get(c, "type") {
			case "text":
				if t == "assistant" {
					out = append(out, textBlock("text", textOf(get(c, "text")))...)
				}
			case "thinking":
				if t == "assistant" {
					out = append(out, textBlock("reasoning", textOf(get(c, "thinking")))...)
				}
			case "tool_use":
				b.start(blockKey(get(c, "id")), get(c, "name"), get(c, "input"), at)
			case "tool_result":
				k := blockKey(get(c, "tool_use_id"))
				x, ok := b.tools[k]
				if !ok {
					x = &openTool{at: at}
				}
				delete(b.tools, k)
				state := "completed"
				if get(c, "is_error") == true {
					state = "failed"
				}
				out = append(out, toolBlock(x.name, x.input, get(c, "content"), state, x.at, at))
			}
		}
	case isACP(h):
		if r, ok := str(get(v, "result", "response")); ok {
			out = append(out, textBlock("text", r)...)
		}
		u := get(v, "update")
		if su := get(u, "sessionUpdate"); su == "tool_call" || su == "tool_call_update" {
			id := blockKey(get(u, "toolCallId"))
			x, ok := b.tools[id]
			if !ok {
				x = b.start(id, first(u, "title", "name"), get(u, "rawInput"), at)
			}
			if x.name == nil {
				x.name = get(u, "title")
			}
			if x.input == nil {
				x.input = get(u, "rawInput")
			}
			if st := get(u, "status"); st == "completed" || st == "failed" {
				delete(b.tools, id)
				output := get(u, "content")
				if nonEmptyObj(get(u, "rawOutput")) {
					output = get(u, "rawOutput")
				}
				out = append(out, toolBlock(x.name, x.input, output, st, x.at, at))
			}
		}
	}
	return out
}
