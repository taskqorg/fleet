package main

// Codex local app-server protocol (codex.rs); Fleet owns command durability and process cleanup. Also the Harness
// methods WireMCP, Protocol and Normalize for every harness.

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// num reads a JSON number of either decoder form.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case interface{ Float64() (float64, error) }:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

func isID(v any, n float64) bool { f, ok := num(get(v, "id")); return ok && f == n }

// ms converts native epoch seconds to ms; nil when not a non-negative integer.
func ms(v any) any {
	if f, ok := num(v); ok && f >= 0 && f == float64(uint64(f)) {
		return uint64(f) * 1000
	}
	return nil
}

func (h harness) Normalize(event map[string]any) map[string]any {
	if h.name == "claude" && event["type"] == "rate_limit_event" {
		return claudeLimitReading(event)
	}
	return event
}

// claudeLimitReading: Claude stream-json rate_limit_event to a limit_reading: status and reset time, never a percent.
func claudeLimitReading(v obj) obj {
	i := v["rate_limit_info"]
	if !has(v, "rate_limit_info") {
		i = v
	}
	period := "unknown"
	if t, ok := str(get(i, "rateLimitType")); ok {
		period = map[string]string{"five_hour": "5h", "seven_day": "weekly", "seven_day_opus": "weekly-opus", "seven_day_sonnet": "weekly-sonnet"}[t]
		if period == "" {
			period = t
		}
	}
	return obj{"type": "limit_reading", "harness": "claude", "readings": []any{obj{"period": period, "status": get(i, "status"), "resetsAt": ms(get(i, "resetsAt"))}}, "native": v}
}

// codexLimitReading: one reading per rate-limit window, its period named from its duration.
func codexLimitReading(r, native any) obj {
	readings := []any{}
	for _, k := range []string{"primary", "secondary"} {
		w, ok := get(r, k).(obj)
		if !ok {
			continue
		}
		pick := func(a, b string) any {
			if w[a] != nil {
				return w[a]
			}
			return w[b]
		}
		period := "unknown"
		if m, ok := num(pick("windowDurationMins", "window_minutes")); ok && m >= 0 && m == float64(uint64(m)) {
			period = map[uint64]string{300: "5h", 10080: "weekly"}[uint64(m)]
			if period == "" {
				period = fmt.Sprintf("%dm", uint64(m))
			}
		}
		readings = append(readings, obj{"period": period, "pct": pick("usedPercent", "used_percent"), "resetsAt": ms(pick("resetsAt", "resets_at"))})
	}
	return obj{"type": "limit_reading", "harness": "codex", "readings": readings, "native": native}
}

func (h harness) WireMCP(cmd *exec.Cmd, p map[string]any, servers map[string]any, extra []string) {
	switch {
	case h.name == "codex":
		for name, s := range servers {
			pre := "-c\x00mcp_servers." + name + "."
			add := func(k, v string) { cmd.Args = append(cmd.Args, strings.Split(pre+k+"="+v, "\x00")...) }
			add("command", encode(get(s, "command")))
			add("args", encode(get(s, "args")))
			add("required", "true")
			if tools, ok := get(s, "tools").([]any); ok {
				add("enabled_tools", encode(tools))
				for _, t := range tools {
					add("tools."+sv(t)+".approval_mode", `"approve"`)
				}
			} else if tools, ok := get(s, "tools").([]string); ok {
				add("enabled_tools", encode(tools))
				for _, t := range tools {
					add("tools."+t+".approval_mode", `"approve"`)
				}
			}
			if env := get(s, "env"); env != nil {
				add("env_vars", encode(env))
			}
		}
	case h.name == "claude":
		profile := any(p)
		if has(p, "agentConfig") {
			profile = p["agentConfig"]
		}
		claudeMCP(cmd, profile, servers, extra)
	}
}

// claudeMCP passes the servers as --mcp-config and grants their Fleet tools with --allowedTools.
func claudeMCP(c *exec.Cmd, profile any, servers obj, extra []string) {
	var allowed []string
	list, _ := get(profile, "tools").([]any)
	for _, t := range list {
		if s, ok := t.(string); ok {
			allowed = append(allowed, s)
		}
	}
	allowed = append(allowed, extra...)
	if has(servers, "design-rules") {
		allowed = append(allowed, "mcp__design-rules__get_design_rules")
	}
	config := obj{}
	for name, s := range servers {
		config[name] = obj{"command": get(s, "command"), "args": get(s, "args")}
	}
	if has(servers, "fleet") {
		for _, t := range []string{"session_transition", "session_fork", "session_resume", "session_handoffs", "message_send", "queue_list", "ask", "reply", "questions", "worker_done", "execution_status", "artifact_save", "artifact_list", "artifact_get"} {
			allowed = append(allowed, "mcp__fleet__"+t)
		}
		if d, _ := get(profile, "delegationProfiles").([]any); len(d) > 0 {
			allowed = append(allowed, "mcp__fleet__request_child")
		}
		var ctx any
		if args, _ := get(servers, "fleet", "args").([]any); len(args) > 0 {
			if s, ok := args[len(args)-1].(string); ok {
				ctx, _ = decode([]byte(s))
			}
		}
		if _, ok := str(get(ctx, "projectId")); ok {
			for _, t := range projectTools {
				allowed = append(allowed, "mcp__fleet__"+t)
			}
		}
	}
	c.Args = append(c.Args, "--mcp-config", encode(obj{"mcpServers": config}), "--allowedTools", strings.Join(allowed, ","))
}

func (h harness) Protocol(cmd *exec.Cmd, root, native string, p map[string]any, servers map[string]any) (*exec.Cmd, Session) {
	switch {
	case h.name == "codex":
		s := &codexSession{profile: p, root: root, resume: native, nextID: 100}
		if native == "" {
			s.fork, _ = str(get(p, "fork", "nativeSessionId"))
		}
		return codexAppServer(cmd), s
	case isACP(h.name):
		return cmd, newACPSession(root, native, p, servers)
	}
	return cmd, nil
}

// codexAppServer turns the exec command into `codex app-server`, keeping only its -c/--disable/--enable flags.
func codexAppServer(old *exec.Cmd) *exec.Cmd {
	c := exec.Command(old.Path)
	c.Args = []string{old.Args[0], "app-server"}
	c.Env, c.Dir, c.SysProcAttr = old.Env, old.Dir, old.SysProcAttr
	args := old.Args[1:]
	for i := 0; i < len(args); i++ {
		if (args[i] == "-c" || args[i] == "--disable" || args[i] == "--enable") && i+1 < len(args) {
			c.Args = append(c.Args, args[i], args[i+1])
			i++
		}
	}
	return c
}

type codexSession struct {
	thread, turn, resume, fork, root string
	stage                            int
	profile                          obj
	steers                           map[float64]string
	nextID                           float64
	version                          any
}

func (s *codexSession) Initialize() map[string]any {
	return obj{"id": 1, "method": "initialize", "params": obj{"clientInfo": obj{"name": "fleet", "version": "0.1.0"}, "capabilities": obj{"experimentalApi": true}}}
}

// Steer is T3 CodexAdapterV2 steer: turn/steer with expectedTurnId of the active turn.
func (s *codexSession) Steer(text, command string) map[string]any {
	if s.stage != 3 || s.thread == "" || s.turn == "" {
		return nil
	}
	s.nextID++
	if s.steers == nil {
		s.steers = map[float64]string{}
	}
	s.steers[s.nextID] = command
	return obj{"id": s.nextID, "method": "turn/steer", "params": obj{"threadId": s.thread, "expectedTurnId": s.turn, "input": []any{obj{"type": "text", "text": text, "text_elements": []any{}}}}}
}

func (s *codexSession) config() any {
	if has(s.profile, "agentConfig") {
		return s.profile["agentConfig"]
	}
	return s.profile
}

func (s *codexSession) turnRequest() obj {
	return obj{"id": 3, "method": "turn/start", "params": obj{"threadId": s.thread, "model": s.profile["model"], "effort": s.profile["effort"],
		"input": []any{obj{"type": "text", "text": s.profile["text"], "text_elements": []any{}}}}}
}

func (s *codexSession) Receive(v map[string]any) ([]map[string]any, map[string]any, bool, error) {
	if has(v, "id") && has(v, "method") {
		return []obj{{"id": v["id"], "error": obj{"code": -32601, "message": "Fleet denies ungranted native request"}}}, nil, false, nil
	}
	if id, ok := num(v["id"]); ok {
		if cmd, ok := s.steers[id]; ok {
			delete(s.steers, id)
			return nil, obj{"type": "steer.result", "commandId": cmd, "ok": !has(v, "error"), "native": v}, false, nil
		}
	}
	if has(v, "error") {
		return nil, nil, false, fmt.Errorf("native protocol request failed: %s", encode(get(v, "error", "code")))
	}
	instructions := sv(get(s.config(), "instructions"))
	switch {
	case isID(v, 1) && s.stage == 0:
		s.stage = 1
		if ua, ok := str(get(v, "result", "userAgent")); ok {
			s.version = ua
		}
		p := s.profile
		params := obj{"cwd": s.root, "model": p["model"], "approvalPolicy": "never", "sandbox": "workspace-write", "developerInstructions": instructions,
			"config": obj{"model_reasoning_effort": p["effort"], "sandbox_workspace_write.network_access": p["gitNetwork"] == true}}
		if has(p, "writableRoots") {
			params["config"].(obj)["sandbox_workspace_write.writable_roots"] = p["writableRoots"]
		}
		method := "thread/start"
		if s.resume != "" {
			method, params["threadId"] = "thread/resume", s.resume
		} else if s.fork != "" {
			method, params["threadId"] = "thread/fork", s.fork
		}
		return []obj{{"method": "initialized"}, {"id": 2, "method": method, "params": params}}, nil, false, nil
	case isID(v, 2) && s.stage == 1:
		thread, ok := str(get(v, "result", "thread", "id"))
		if !ok {
			return nil, nil, false, errors.New("missing native thread")
		}
		if s.resume != "" && s.resume != thread {
			return nil, nil, false, errors.New("resumed thread identity mismatch")
		}
		s.thread, s.stage = thread, 2
		next := []obj{s.turnRequest()}
		if (s.resume != "" || s.fork != "") && instructions != "" {
			s.stage = 4
			next = []obj{{"id": 4, "method": "thread/inject_items", "params": obj{"threadId": thread,
				"items": []any{obj{"type": "message", "role": "developer", "content": []any{obj{"type": "input_text", "text": instructions}}}}}}}
		}
		return next, obj{"type": "thread.started", "thread_id": thread, "harnessVersion": s.version, "native": v}, false, nil
	case isID(v, 4) && s.stage == 4:
		s.stage = 2
		return []obj{s.turnRequest()}, nil, false, nil
	case isID(v, 3) && s.stage == 2:
		s.turn, _ = str(get(v, "result", "turn", "id"))
		s.stage = 3
		return nil, nil, false, nil
	}
	p := get(v, "params")
	// Limit readings (people-access §8): the account-wide notification carries no thread.
	if v["method"] == "account/rateLimits/updated" {
		return nil, codexLimitReading(get(p, "rateLimits"), v), false, nil
	}
	if t, ok := str(get(p, "threadId")); !ok || s.thread == "" || t != s.thread {
		return nil, nil, false, nil
	}
	if v["method"] == "turn/started" && s.turn == "" {
		s.turn, _ = str(get(p, "turn", "id"))
	}
	if t, ok := str(get(p, "turnId")); ok && t != s.turn {
		return nil, nil, false, nil
	}
	turnID, _ := str(get(p, "turnId"))
	switch {
	case v["method"] == "item/completed" && s.turn != "" && turnID == s.turn:
		item, _ := clone(get(p, "item")).(obj)
		if item == nil {
			return nil, nil, false, errors.New("codex item: not an object")
		}
		// contracts Item: these fields are strings when present.
		for _, k := range []string{"type", "text", "command", "name", "status"} {
			if x, ok := item[k]; ok && x != nil {
				if _, ok := x.(string); !ok {
					return nil, nil, false, fmt.Errorf("codex item: %s must be a string", k)
				}
			}
		}
		if item["type"] == "agentMessage" {
			item["type"] = "agent_message"
		}
		// Codex collab (spawn_agent etc.): child threads are accounted under this parent execution.
		children := []any{}
		if item["type"] == "collabAgentToolCall" {
			ids, _ := item["receiverThreadIds"].([]any)
			for _, c := range ids {
				model := item["model"]
				if model == nil {
					model = s.profile["model"]
				}
				children = append(children, obj{"threadId": c, "parentThreadId": s.thread, "tool": item["tool"], "binding": obj{"model": model, "account": "parent"}, "state": get(item, "agentsStates", sv(c))})
			}
		}
		return nil, obj{"item": item, "type": "item.completed", "children": children, "native": v}, false, nil
	case v["method"] == "thread/tokenUsage/updated":
		return nil, obj{"type": "usage.observation", "usage": get(p, "tokenUsage"), "native": v}, false, nil
	case v["method"] == "turn/completed" && s.turn != "" && get(p, "turn", "id") == s.turn:
		t := "turn.failed"
		if get(p, "turn", "status") == "completed" {
			t = "turn.completed"
		}
		return nil, obj{"type": t, "error": get(p, "turn", "error"), "native": v}, true, nil
	}
	return nil, nil, false, nil
}
