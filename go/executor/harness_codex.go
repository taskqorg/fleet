package main

// Codex local app-server protocol; Fleet owns command durability and process cleanup. Also the Harness
// methods WireMCP, Protocol and Normalize for every harness.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// num reads a JSON number of either decoder form.
func num(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, true
	case int:
		return float64(x), true
	case int64:
		return float64(x), true
	case interface{ Float64() (float64, error) }:
		f, err := x.Float64()
		return f, err == nil
	}
	return 0, false
}

// ms: a provider reset time in seconds as ms since epoch; nil when absent or not a whole non-negative number.
func ms(v any) *int64 {
	if f, ok := num(v); ok && f >= 0 && f < math.MaxInt64/1000 && f == math.Trunc(f) {
		t := int64(f) * 1000
		return &t
	}
	return nil
}

// limitReading is the limit_reading event (contract.LimitReading) as the executor's event map.
func limitReading(harness string, windows []contract.LimitWindow, native any) M {
	t := string(contract.FleetEventTypeLimitReading)
	return decM(enc(contract.LimitReading{Type: &t, Harness: harness, Readings: windows, Native: json.RawMessage(enc(native))}))
}

func (h harness) Normalize(event map[string]any) map[string]any {
	if h.name == "claude" && typed[claudeRateLimitEvent](event).Type == "rate_limit_event" {
		return claudeLimitReading(event)
	}
	return event
}

// claudeLimitReading: Claude stream-json rate_limit_event to a limit_reading: status, reset time and, when utilization is given, the percent.
func claudeLimitReading(v M) M {
	var i claudeRateLimit
	if e := typed[claudeRateLimitEvent](v); e.RateLimitInfo.Set {
		i = typed[claudeRateLimit](e.RateLimitInfo)
	} else {
		i = typed[claudeRateLimit](v)
	}
	period, t := "unknown", i.RateLimitType
	if t != "" {
		period = claudePeriod(t)
	}
	r := contract.LimitWindow{Period: period, ResetsAt: ms(i.ResetsAt), Status: i.Status}
	if m := strings.TrimPrefix(t, "seven_day_"); m != t {
		r.Model = &m // a per-model weekly limit
	}
	// fleet-people-access.md section 8: utilization (0-1) gives the percent, to 0.01; without it the reading stays
	// status only.
	if u, ok := num(i.Utilization); ok {
		pct := math.Round(u*10000) / 100
		r.Pct = &pct
	}
	return limitReading("claude", []contract.LimitWindow{r}, v)
}

// codexLimitReading: one reading per rate-limit window, its period named from its duration.
func codexLimitReading(r, native any) M {
	readings := []contract.LimitWindow{}
	limits := typed[codexRateLimits](r)
	for _, w := range []*codexWindow{limits.Primary, limits.Secondary} {
		if w == nil {
			continue
		}
		pick := func(a, b any) any {
			if a != nil {
				return a
			}
			return b
		}
		period := "unknown"
		if m, ok := num(pick(w.WindowDurationMins, w.WindowMinutes)); ok && m >= 0 && m == float64(uint64(m)) {
			period = map[uint64]string{300: "5h", 10080: "weekly"}[uint64(m)]
			if period == "" {
				period = fmt.Sprintf("%dm", uint64(m))
			}
		}
		lw := contract.LimitWindow{Period: period, ResetsAt: ms(pick(w.ResetsAt, w.ResetsAtSnake))}
		if pct, ok := num(pick(w.UsedPercent, w.UsedPercentSnake)); ok {
			lw.Pct = &pct
		}
		readings = append(readings, lw)
	}
	return limitReading("codex", readings, native)
}

// mcpServerEntry: one server entry in the executor's WireMCP server map.
type mcpServerEntry struct {
	Command jsonValue `json:"command"`
	Args    jsonValue `json:"args"`
	Tools   []string  `json:"tools"`
	Env     jsonValue `json:"env"`
	// ToolTimeoutSec: a server whose calls run longer than the harness default (project-env).
	ToolTimeoutSec int `json:"toolTimeoutSec"`
}

func (h harness) WireMCP(cmd *exec.Cmd, p map[string]any, servers map[string]any, extra []string) {
	switch {
	case h.name == "codex":
		for name, s := range servers {
			srv := typed[mcpServerEntry](s)
			pre := "-c\x00mcp_servers." + name + "."
			add := func(k, v string) { cmd.Args = append(cmd.Args, strings.Split(pre+k+"="+v, "\x00")...) }
			add("command", enc(srv.Command.V))
			add("args", enc(srv.Args.V))
			add("required", "true")
			if srv.Tools != nil {
				add("enabled_tools", enc(srv.Tools))
				for _, t := range srv.Tools {
					add("tools."+t+".approval_mode", `"approve"`)
				}
			}
			if srv.Env.V != nil {
				add("env_vars", enc(srv.Env.V))
			}
			if srv.ToolTimeoutSec > 0 {
				add("tool_timeout_sec", strconv.Itoa(srv.ToolTimeoutSec))
			}
		}
	case h.name == "claude":
		profile := any(p)
		if has(p, "agentConfig") {
			profile = p["agentConfig"]
			if jIsTrue(p, "readOnly") { // the start's read-only grant narrows the profile's (claudeMCP)
				c := decM(enc(profile)) // a copy: the profile itself stays as sent
				c["readOnly"] = true
				profile = c
			}
		}
		claudeMCP(cmd, profile, servers, extra)
	}
}

// claudeMCP passes the servers as --mcp-config and grants their Fleet tools with --allowedTools.
func claudeMCP(c *exec.Cmd, profile any, servers M, extra []string) {
	allowed, _ := claudeNativeTools(profile) // Command validates the selection before MCP wiring.
	allowed = codeVisible(allowed, launchOf(profile).CodeMode)
	allowed = append(allowed, extra...)
	if jIsTrue(profile, "readOnly") {
		allowed = readOnlyTools(allowed)
	}
	if has(servers, "design-rules") {
		allowed = append(allowed, "mcp__design-rules__get_design_rules")
	}
	config := M{}
	for name, server := range servers {
		srv := typed[mcpServerEntry](server)
		for _, tool := range srv.Tools {
			allowed = append(allowed, "mcp__"+name+"__"+tool)
		}
		config[name] = M{"command": srv.Command.V, "args": srv.Args.V}
	}
	if has(servers, "fleet") {
		for _, t := range []string{"session_presets", "session_transition", "session_fork", "session_resume", "session_handoffs", "session_rename", "message_send", "queue_list", "ask", "reply", "questions", "worker_done", "execution_status", "artifact_save", "artifact_list", "artifact_get"} {
			allowed = append(allowed, "mcp__fleet__"+t)
		}
		if d, _ := jAt(profile, "delegationProfiles").([]any); len(d) > 0 {
			allowed = append(allowed, "mcp__fleet__request_child")
		}
		var ctx any
		if args, _ := jAt(servers, "fleet", "args").([]any); len(args) > 0 {
			if s, ok := args[len(args)-1].(string); ok {
				ctx, _ = dec(s)
			}
		}
		if _, ok := str(jAt(ctx, "projectId")); ok {
			for _, t := range projectTools {
				allowed = append(allowed, "mcp__fleet__"+t)
			}
		}
	}
	c.Args = append(c.Args, "--mcp-config", enc(M{"mcpServers": config}), "--allowedTools", strings.Join(allowed, ","))
}

func (h harness) Protocol(cmd *exec.Cmd, root, native string, p map[string]any, servers map[string]any) (*exec.Cmd, Session) {
	switch {
	case h.name == "codex":
		s := &codexSession{profile: p, root: root, resume: native, nextID: 100}
		if native == "" {
			s.fork, _ = str(jAt(p, "fork", "nativeSessionId"))
		}
		return codexAppServer(cmd), s
	case isACP(h.name):
		return cmd, newACPSession(root, native, p, servers)
	}
	return cmd, nil
}

// codexAppServer turns the exec command into `codex app-server`, keeping only its -c/--disable/--enable flags: all of
// codexIsolation and the per-run additions; codexExecOnly has no app-server form (see there).
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
	marks                            string // directory of the instructions each native thread already holds (instructionsMark)
	stage                            int
	profile                          M
	steers                           map[float64]string
	nextID                           float64
	version                          any
	turnStartedAtMs                  int64
}

// instructionsSum: the mark of one developer instructions text.
func instructionsSum(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// instructionsMark: the mark of the instructions the native thread already holds, or "" when unknown (no marks
// directory, a thread from before marks, an unreadable file): unknown injects, as before.
func (s *codexSession) instructionsMark(thread string) string {
	if s.marks == "" || thread == "" || strings.ContainsAny(thread, `/\`) {
		return ""
	}
	b, err := os.ReadFile(filepath.Join(s.marks, thread))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// markInstructions records that the thread now holds this instructions text; a failed write only costs one injection.
func (s *codexSession) markInstructions(thread, instructions string) {
	if s.marks == "" || thread == "" || instructions == "" || strings.ContainsAny(thread, `/\`) {
		return
	}
	if os.MkdirAll(s.marks, 0o700) == nil {
		_ = os.WriteFile(filepath.Join(s.marks, thread), []byte(instructionsSum(instructions)), 0o600)
	}
}

func (s *codexSession) Initialize() map[string]any {
	return M{"id": 1, "method": "initialize", "params": M{"clientInfo": M{"name": "fleet", "version": "0.1.0"}, "capabilities": M{"experimentalApi": true}}}
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
	return M{"id": s.nextID, "method": "turn/steer", "params": M{"threadId": s.thread, "expectedTurnId": s.turn, "input": []any{M{"type": "text", "text": text, "text_elements": []any{}}}}}
}

func (s *codexSession) config() any {
	if has(s.profile, "agentConfig") {
		return s.profile["agentConfig"]
	}
	return s.profile
}

func (s *codexSession) turnRequest() M {
	input := []any{M{"type": "text", "text": s.profile["text"], "text_elements": []any{}}}
	for _, path := range jArr(s.profile, "localImages") { // attached images (attachments.go)
		input = append(input, M{"type": "localImage", "path": path})
	}
	params := M{"threadId": s.thread, "model": s.profile["model"], "effort": s.profile["effort"], "input": input}
	if jIsTrue(s.profile, "readOnly") {
		params["sandboxPolicy"] = M{"type": "readOnly"}
	} else if has(s.profile, "writableRoots") {
		params["sandboxPolicy"] = M{"type": "workspaceWrite", "writableRoots": s.profile["writableRoots"], "networkAccess": s.profile["gitNetwork"] == true}
	}
	return M{"id": 3, "method": "turn/start", "params": params}
}

func (s *codexSession) Receive(v map[string]any) ([]map[string]any, map[string]any, bool, error) {
	f := typed[rpcFrame](v)
	if f.request() {
		return []M{{"id": f.ID.V, "error": M{"code": -32601, "message": "Fleet denies ungranted native request"}}}, nil, false, nil
	}
	if id, ok := num(f.ID.V); ok {
		if cmd, ok := s.steers[id]; ok {
			delete(s.steers, id)
			return nil, M{"type": "steer.result", "commandId": cmd, "ok": !f.Error.Set, "native": v}, false, nil
		}
	}
	if f.Error.Set {
		return nil, nil, false, fmt.Errorf("native protocol request failed: %s", f.errorCode())
	}
	instructions := launchOf(s.config()).Instructions
	r := typed[codexResult](f.Result)
	switch {
	case f.isID(1) && s.stage == 0:
		s.stage = 1
		if r.UserAgent != nil {
			s.version = *r.UserAgent
		}
		p := s.profile
		params := M{"cwd": s.root, "model": p["model"], "approvalPolicy": "never", "sandbox": "workspace-write", "developerInstructions": instructions,
			"config": M{"model_reasoning_effort": p["effort"], "sandbox_workspace_write.network_access": p["gitNetwork"] == true}}
		if jIsTrue(p, "readOnly") {
			params["sandbox"] = "read-only"
		} else if has(p, "writableRoots") {
			params["config"].(M)["sandbox_workspace_write.writable_roots"] = p["writableRoots"]
		}
		method := "thread/start"
		if s.resume != "" {
			method, params["threadId"] = "thread/resume", s.resume
		} else if s.fork != "" {
			method, params["threadId"] = "thread/fork", s.fork
		}
		return []M{{"method": "initialized"}, {"id": 2, "method": method, "params": params}}, nil, false, nil
	case f.isID(2) && s.stage == 1:
		if r.Thread.ID == nil {
			return nil, nil, false, errors.New("missing native thread")
		}
		thread := *r.Thread.ID
		if s.resume != "" && s.resume != thread {
			return nil, nil, false, errors.New("resumed thread identity mismatch")
		}
		s.thread, s.stage = thread, 2
		next := []M{s.turnRequest()}
		held := s.resume
		if held == "" {
			held = s.fork // a fork copies its source thread's history
		}
		switch {
		case held == "":
			s.markInstructions(thread, instructions) // a new thread got them as its initial developer context
		case instructions != "" && s.instructionsMark(held) != instructionsSum(instructions):
			// A resumed thread keeps its recorded developer context; changed instructions are appended once. The same
			// text is not appended again: every copy stays in the history and is re-read on every later turn.
			s.stage = 4
			next = []M{{"id": 4, "method": "thread/inject_items", "params": M{"threadId": thread,
				"items": []any{M{"type": "message", "role": "developer", "content": []any{M{"type": "input_text", "text": instructions}}}}}}}
		default:
			s.markInstructions(thread, instructions)
		}
		return next, M{"type": "thread.started", "thread_id": thread, "harnessVersion": s.version, "native": v}, false, nil
	case f.isID(4) && s.stage == 4:
		s.stage = 2
		s.markInstructions(s.thread, instructions)
		return []M{s.turnRequest()}, nil, false, nil
	case f.isID(3) && s.stage == 2:
		s.turn = r.Turn.ID
		if s.turn != "" && s.turnStartedAtMs == 0 {
			s.turnStartedAtMs = nowMs()
		}
		s.stage = 3
		return nil, nil, false, nil
	}
	p := typed[codexParams](f.Params)
	method := f.method()
	// Limit readings (people-access §8): the account-wide notification carries no thread.
	if method == "account/rateLimits/updated" {
		return nil, codexLimitReading(p.RateLimits, v), false, nil
	}
	if p.ThreadID == nil || s.thread == "" || *p.ThreadID != s.thread {
		return nil, nil, false, nil
	}
	if method == "turn/started" && s.turn == "" {
		s.turn = p.Turn.ID
		if s.turn != "" && s.turnStartedAtMs == 0 {
			s.turnStartedAtMs = nowMs()
		}
	}
	if p.TurnID != nil && *p.TurnID != s.turn {
		return nil, nil, false, nil
	}
	switch {
	case method == "item/completed" && s.turn != "" && isStr(p.TurnID, s.turn):
		item, _ := jClone(p.Item.V).(M) // a copy: the frame is forwarded as received
		if item == nil {
			return nil, nil, false, errors.New("codex item: not an object")
		}
		// contracts Item: these fields are strings when present.
		strs := typed[codexItemStrings](p.Item)
		for _, x := range []struct {
			k string
			r jsonValue
		}{{"type", strs.Type}, {"text", strs.Text}, {"command", strs.Command}, {"name", strs.Name}, {"status", strs.Status}} {
			if x.r.V != nil {
				if _, ok := valStr(x.r); !ok {
					return nil, nil, false, fmt.Errorf("codex item: %s must be a string", x.k)
				}
			}
		}
		it := typed[codexItem](p.Item)
		if it.Type == "agentMessage" {
			item["type"] = "agent_message"
		}
		// Codex collab (spawn_agent etc.): child threads are accounted under this parent execution.
		children := []any{}
		if it.Type == "collabAgentToolCall" {
			for _, c := range it.ReceiverThreadIDs {
				model := it.Model
				if model == nil {
					model = s.profile["model"]
				}
				thread, _ := c.(string)
				children = append(children, M{"threadId": c, "parentThreadId": s.thread, "tool": it.Tool.V, "binding": M{"model": model, "account": "parent"}, "state": it.AgentsStates[thread]})
			}
		}
		return nil, M{"item": item, "type": "item.completed", "children": children, "native": v}, false, nil
	case method == "thread/tokenUsage/updated":
		return nil, M{"type": "usage.observation", "usage": p.TokenUsage, "native": v}, false, nil
	case method == "turn/completed" && s.turn != "" && p.Turn.ID == s.turn:
		t := "turn.failed"
		if p.Turn.Status == "completed" {
			t = "turn.completed"
		}
		ev := M{"type": t, "error": p.Turn.Error, "native": v}
		dur := p.Turn.DurationMs
		if dur == nil && s.turnStartedAtMs > 0 {
			dur = max(nowMs()-s.turnStartedAtMs, int64(0))
		}
		s.turnStartedAtMs = 0
		if dur != nil {
			ev["durationMs"] = dur
		}
		return nil, ev, true, nil
	}
	return nil, nil, false, nil
}
