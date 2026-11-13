package main

// Harness protocol tests: each guards a native wire risk.

import (
	"encoding/json"
	"fleet/contract"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
)

func j(s string) M {
	var v M
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(s)
	}
	return v
}

func eq(t *testing.T, got any, want string) {
	t.Helper()
	var w any
	json.Unmarshal([]byte(want), &w)
	if enc(jClone(got)) != enc(jClone(w)) {
		t.Fatalf("got %s want %s", enc(got), want)
	}
}

// Risk: the transcript loses or duplicates reply text, or a cut-off tool disappears (fleet-conversations.md Block model).
func TestBlocksPerHarness(t *testing.T) {
	b := NewBlocks()
	eq(t, b.Project("codex", j(`{"type":"item.completed","item":{"type":"agent_message","text":"Hi"}}`), 5)["blocks"], `[{"kind":"text","role":"agent","markdown":"Hi"}]`)
	p := b.Project("codex", j(`{"type":"item.completed","item":{"type":"mcpToolCall","tool":"read","arguments":{"p":1},"result":"ok","status":"completed","startedAtMs":1,"completedAtMs":4}}`), 5)
	eq(t, p["blocks"], `[{"kind":"tool","name":"read","input":{"p":1},"output":"ok","state":"completed","startedAt":1,"endedAt":4}]`)
	eq(t, b.Project("codex", j(`{"type":"item.completed","item":{"type":"reasoning","summary":[],"content":[]}}`), 5)["blocks"], `[]`)
	eq(t, b.Project("codex", j(`{"type":"item.completed","item":{"type":"userMessage","content":[{"text":"hi"}]}}`), 5)["blocks"], `[]`)
	c := NewBlocks()
	eq(t, c.Project("claude", j(`{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"Yes"},{"type":"tool_use","id":"t","name":"Read","input":{"f":1}}]}}`), 10)["blocks"],
		`[{"kind":"reasoning","markdown":"hm"},{"kind":"text","role":"agent","markdown":"Yes"}]`)
	eq(t, c.Project("claude", j(`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"ok"}]}}`), 30)["blocks"],
		`[{"kind":"tool","name":"Read","input":{"f":1},"output":"ok","state":"completed","startedAt":10,"endedAt":30}]`)
	a := NewBlocks()
	chunk := func(s string) M {
		return j(`{"type":"acp.update","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"` + s + `"}}}`)
	}
	d1, d2 := a.Project("antigravity", chunk("Hel"), 1), a.Project("antigravity", chunk("lo"), 2)
	if d1["deltaOf"] != "text:1" || d2["deltaOf"] != "text:1" || d1["opens"] != true || has(d2, "opens") {
		t.Fatal(d1, d2)
	}
	end := a.Project("antigravity", j(`{"type":"turn.completed","stopReason":"end_turn"}`), 3)
	eq(t, end["closes"], `["text:1"]`)
	eq(t, end["blocks"], `[{"kind":"text","role":"agent","markdown":"Hello"}]`)
	a.Project("antigravity", j(`{"type":"acp.update","update":{"sessionUpdate":"tool_call","toolCallId":"x","title":"Edit","rawInput":{"a":1},"status":"pending"}}`), 4)
	eq(t, a.Project("antigravity", j(`{"type":"acp.update","update":{"sessionUpdate":"tool_call_update","toolCallId":"x","status":"completed","rawOutput":"done"}}`), 9)["blocks"],
		`[{"kind":"tool","name":"Edit","input":{"a":1},"output":"done","state":"completed","startedAt":4,"endedAt":9}]`)
	s := NewBlocks()
	s.Project("antigravity", j(`{"step_update":{"text_delta":"Hi"}}`), 1)
	eq(t, s.Project("antigravity", j(`{"event":"result","result":{"status":"SUCCESS","response":"Hi"}}`), 2)["blocks"], `[{"kind":"text","role":"agent","markdown":"Hi"}]`)
	c.Project("claude", j(`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"u","name":"Bash","input":{}}]}}`), 40)
	if st := jAt(jClone(c.Project("claude", j(`{"type":"result","is_error":false}`), 50)["blocks"].([]any)[0]), "state"); st != "unfinished" {
		t.Fatal(st)
	}
	e := jClone(b.Project("codex", j(`{"type":"item.completed","item":{"type":"error","message":"stream disconnected"}}`), 5)["blocks"].([]any)[0])
	if jAt(e, "state") != "failed" || jAt(e, "output") != "stream disconnected" {
		t.Fatal(e)
	}
	u := jClone(a.Project("codex", j(`{"type":"unparsed","raw":"x"}`), 7)["blocks"].([]any)[0])
	if jAt(u, "name") != "unparsed" || jAt(u, "output", "raw") != "x" {
		t.Fatal(u)
	}
	// Risk: ACP usage categories merged, or an empty rawOutput hides a diff result.
	p = project("acp", j(`{"result":{"usage":{"cachedReadTokens":4046,"totalTokens":35281}},"update":{"sessionUpdate":"tool_call_update","toolCallId":"1","rawOutput":{},"content":[{"type":"diff"}]}}`))
	if jAt(p, "usage", "cache_read_tokens") != 4046.0 || has(jAt(p, "usage"), "cached_input_tokens") || jAt(p["tools"].([]any)[0], "result").([]any)[0].(M)["type"] != "diff" {
		t.Fatal(enc(p))
	}
}

// Risk: codex handshake order, resume identity, developer-role injection, approval refusal and steer correlation drift.
func TestCodexSession(t *testing.T) {
	for _, resume := range []string{"", "thread"} {
		s := &codexSession{root: "/w", resume: resume, profile: j(`{"model":"m","effort":"low","instructions":"dev","text":"task"}`), nextID: 100}
		r, _, _, _ := s.Receive(j(`{"id":1,"result":{"userAgent":"codex/9"}}`))
		want := map[bool]string{true: "thread/resume", false: "thread/start"}[resume != ""]
		if len(r) != 2 || r[1]["method"] != want || jAt(r[1], "params", "developerInstructions") != "dev" {
			t.Fatal(r)
		}
		if r, _, _, _ := s.Receive(j(`{"id":1,"result":{}}`)); len(r) != 0 {
			t.Fatal("repeated initialize answered")
		}
		next, ev, _, _ := s.Receive(j(`{"id":2,"result":{"thread":{"id":"thread"}}}`))
		if ev["harnessVersion"] != "codex/9" {
			t.Fatal(ev)
		}
		if resume != "" {
			if next[0]["method"] != "thread/inject_items" {
				t.Fatal(next)
			}
			if next, _, _, _ = s.Receive(j(`{"id":4,"result":{}}`)); next[0]["method"] != "turn/start" {
				t.Fatal(next)
			}
		}
		if s.Steer("x", "c") != nil {
			t.Fatal("steer before turn")
		}
		s.Receive(j(`{"id":3,"result":{"turn":{"id":"turn"}}}`))
		if r, _, _, _ := s.Receive(j(`{"id":42,"method":"item/commandExecution/requestApproval","params":{}}`)); !has(r[0], "error") {
			t.Fatal("approval granted")
		}
		f := s.Steer("more", "cmd")
		if jAt(f, "params", "expectedTurnId") != "turn" {
			t.Fatal(f)
		}
		_, ev, done, _ := s.Receive(M{"id": f["id"], "error": M{"code": -1}})
		if ev["type"] != "steer.result" || ev["ok"] != false || done {
			t.Fatal(ev)
		}
		_, ev, _, _ = s.Receive(j(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["c"],"agentsStates":{"c":{"status":"running"}}}}}`))
		if ch := ev["children"].([]any)[0]; jAt(ch, "parentThreadId") != "thread" || jAt(ch, "binding", "model") != "m" {
			t.Fatal(ev)
		}
		if _, _, done, _ := s.Receive(j(`{"method":"turn/completed","params":{"threadId":"foreign","turn":{"id":"turn","status":"completed"}}}`)); done {
			t.Fatal("foreign thread completed the turn")
		}
		if _, _, done, _ := s.Receive(j(`{"method":"turn/completed","params":{"threadId":"thread","turn":{"id":"turn","status":"completed"}}}`)); !done {
			t.Fatal("turn not completed")
		}
	}
	s := &codexSession{resume: "saved", profile: j(`{"model":"m"}`)}
	s.Receive(j(`{"id":1,"result":{}}`))
	if _, _, _, err := s.Receive(j(`{"id":2,"result":{"thread":{"id":"other"}}}`)); err == nil {
		t.Fatal("resume identity mismatch accepted")
	}
	_, ev, _, _ := s.Receive(j(`{"method":"account/rateLimits/updated","params":{"rateLimits":{"primary":{"usedPercent":92,"windowDurationMins":10080,"resetsAt":1794400000}}}}`))
	eq(t, ev["readings"], `[{"period":"weekly","pct":92,"resetsAt":1794400000000}]`)
	eq(t, claudeLimitReading(j(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed_warning","resetsAt":1794330000,"rateLimitType":"five_hour"}}`))["readings"],
		`[{"period":"5h","status":"allowed_warning","resetsAt":1794330000000}]`)
	eq(t, claudeLimitReading(j(`{"type":"rate_limit_event","rate_limit_info":{"status":"allowed","utilization":0.29,"resetsAt":1794330000,"rateLimitType":"seven_day"}}`))["readings"],
		`[{"period":"weekly","pct":29,"resetsAt":1794330000000,"status":"allowed"}]`)
}

// Risk: ACP prompts without the qualified protocol version (any runtime release of protocol 1 runs), the requested model or mode, or answers client permission requests.
func TestACPSession(t *testing.T) {
	profile := j(`{"model":"g","text":"task","agentConfig":{"instructions":"explicit user","settings":{"mode":"accept-edits"}}}`)
	opened := `{"id":2,"result":{"sessionId":"s","models":{"availableModels":[{"modelId":"g"}]},"modes":{"availableModes":[{"id":"accept-edits"}]}}}`
	for _, caps := range []string{`{"loadSession":true}`, `{"sessionCapabilities":{"resume":{}}}`} {
		s := newACPSession("/w", "s", profile, M{"fleet": M{"command": "x", "args": []any{}}})
		r, _, _, err := s.Receive(j(`{"id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"1.5.3"},"agentCapabilities":` + caps + `}}`))
		if err != nil || jAt(r[0], "params", "sessionId") != "s" || len(jAt(r[0], "params", "mcpServers").([]any)) != 1 {
			t.Fatal(r, err)
		}
		steps := []string{opened, `{"id":3,"result":{}}`, `{"id":4,"result":{}}`}
		for i, m := range []string{"session/set_model", "session/set_mode", "session/prompt"} {
			r, _, _, err = s.Receive(j(steps[i]))
			if err != nil || r[0]["method"] != m {
				t.Fatal(m, r, err)
			}
		}
		if !strings.Contains(jStr(jAt(r[0], "params", "prompt").([]any)[0].(M)["text"]), "user role") {
			t.Fatal(r)
		}
		if r, ev, _, _ := s.Receive(j(`{"id":9,"method":"session/request_permission","params":{}}`)); jAt(r[0], "result", "outcome", "outcome") != "cancelled" || ev["type"] != "permission.refused" {
			t.Fatal(r)
		}
		if _, _, done, _ := s.Receive(j(`{"id":5,"result":{"stopReason":"end_turn"}}`)); !done {
			t.Fatal("not done")
		}
	}
	s := newACPSession("/w", "", profile, M{})
	if _, _, _, err := s.Receive(j(`{"id":1,"result":{"protocolVersion":2,"agentInfo":{"version":"1.5.3"}}}`)); err == nil {
		t.Fatal("unqualified protocol accepted")
	}
	s = newACPSession("/w", "", profile, M{})
	if _, ev, _, err := s.Receive(j(`{"id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"1.5.4"}}}`)); err != nil || jAt(ev, "native", "result", "agentInfo", "version") != "1.5.4" {
		t.Fatal("a patch release of protocol 1 refused, or its version not retained", err)
	}
	s = newACPSession("/w", "", profile, M{})
	s.Receive(j(`{"id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"1.5.3"}}}`))
	if _, _, _, err := s.Receive(j(`{"id":2,"result":{"sessionId":"s","models":{"availableModels":[]}}}`)); err == nil {
		t.Fatal("unavailable model accepted")
	}
}

// Risk: a registry launch runs with the wrong home, leaks executor env, or resumes under another account binding.
func TestRegistryCommand(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) string { d := root + "/" + p; os.MkdirAll(d, 0o700); return d }
	work := mk("work")
	reg := root + "/accounts.json"
	accounts := M{"ag": M{"harness": "antigravity", "home": mk("h"), "nativeHome": mk("nh"), "executable": "/bin/echo"},
		"cx": M{"harness": "codex", "home": mk("h2"), "nativeHome": mk("nh2"), "executable": "/bin/echo"}}
	os.WriteFile(reg, []byte(enc(accounts)), 0o600)
	t.Setenv("FLEET_NATIVE_ACCOUNTS", reg)
	t.Setenv("OPENAI_API_KEY", "leak")
	c, in, err := Harnesses["antigravity"].Command(work, "", M{"harness": "antigravity", "account": "ag", "model": "g", "text": "t", "settings": M{"sandbox": false}})
	if err != nil || in != nil {
		t.Fatal(err)
	}
	args := strings.Join(c.Args, " ")
	if !strings.Contains(args, "acp --gemini-dir "+root+"/nh") || !strings.Contains(args, "--sandbox=false") || strings.Contains(strings.Join(c.Env, " "), "OPENAI") {
		t.Fatal(c.Args, c.Env)
	}
	if _, _, err := Harnesses["antigravity"].Command(work, "retained", M{"harness": "antigravity", "account": "ag", "model": "g", "text": "t"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Harnesses["codex"].Command(work, "retained", M{"account": "cx", "text": "t"}); err == nil {
		t.Fatal("resume under another account binding accepted")
	}
	c, _, err = Harnesses["codex"].Command(mk("w2"), "", M{"account": "cx", "text": "t"})
	if err != nil || c.Args[len(c.Args)-1] != "t" || !slices.Contains(c.Env, "CODEX_HOME="+root+"/nh2") {
		t.Fatal(err, c)
	}
	app := codexAppServer(c)
	if app.Args[1] != "app-server" || slices.Contains(app.Args, "t") || !slices.Contains(app.Args, "--disable") {
		t.Fatal(app.Args)
	}
}

// Requirement: a Claude get_usage answer (shape seen live from Claude Code 2.1.319) becomes limit windows: seven_day
// as weekly with its percent and reset, model_scoped as weekly-<model>, null windows and non-limit keys skipped
// (fleet-people-access.md section 8). Risk: Claude subscriptions show no percent and no per-model limit.
func TestClaudeUsageReading(t *testing.T) {
	answer := j(`{"rate_limits":{"five_hour":null,"seven_day":{"utilization":74,"resets_at":"2026-11-16T05:00:00.015107+00:00","limit_dollars":null},
		"seven_day_opus":null,"seven_day_breakdown":{"rows":[]},"extra_usage":{"utilization":null},
		"model_scoped":[{"display_name":"Fable","utilization":14,"resets_at":"2026-11-16T05:00:00.015240+00:00"}]}}`)
	eq(t, claudeUsageReading(answer, nil)["readings"],
		`[{"period":"weekly","pct":74,"resetsAt":1794805200015},{"period":"weekly-fable","pct":14,"resetsAt":1794805200015,"model":"fable"}]`)
}

// Requirement: runtime-integration preserves native work and optional delegation; risk: Claude exposes tools but dontAsk denies them, or an explicit empty grant expands.
func TestClaudeNativeToolDelivery(t *testing.T) {
	for _, tc := range []struct {
		profile M
		want    string
		invalid bool
	}{
		{M{}, "Read,Edit,Write,Bash,Glob,Grep,Task,TaskStop", false},
		{M{"tools": []any{}}, "", false},
		{M{"tools": []any{"Read", "Task"}}, "Read,Task", false},
		{M{"tools": []any{"unknown"}}, "", true},
		{M{"tools": "Read"}, "", true},
	} {
		tools, err := claudeNativeTools(tc.profile)
		if (err != nil) != tc.invalid {
			t.Fatalf("%v: %v", tc.profile, err)
		}
		if tc.invalid {
			continue
		}
		if strings.Join(tools, ",") != tc.want {
			t.Fatal(tools)
		}
		c := exec.Command("not-executed")
		claudeMCP(c, tc.profile, M{}, nil)
		if c.Args[len(c.Args)-1] != tc.want {
			t.Fatalf("permission grant differs: %v", c.Args)
		}
	}
}

// Requirement: non-gateway harness tool calls, turn counts, active duration, and bounded overflow metrics are extracted accurately per harness.
// Risk: gateway connection tools, synthetic wire frames, ID-less tool collisions, or >32 distinct tools distort analytics or bloat event payloads.
func TestRunMetricsPerHarnessAndOverflow(t *testing.T) {
	// Codex: codexSession fallback durationMs (int64), usage steps, non-gateway + gateway + synthetic filtering.
	cs := &codexSession{stage: 2, thread: "th-1"}
	cs.Receive(j(`{"id":3,"result":{"turn":{"id":"turn-1"}}}`))
	_, ev, done, err := cs.Receive(j(`{"method":"turn/completed","params":{"threadId":"th-1","turn":{"id":"turn-1","status":"completed"}}}`))
	if err != nil || !done {
		t.Fatalf("codex Receive unexpected done=%v err=%v", done, err)
	}
	if _, ok := ev["durationMs"].(int64); !ok {
		t.Fatalf("expected int64 fallback durationMs, got %T (%v)", ev["durationMs"], ev["durationMs"])
	}
	cm := newRunMetrics(map[string]bool{"linear": true})
	cm.observeEvent("codex", ev, 100)
	cm.observeEvent("codex", j(`{"type":"usage.observation"}`), 110)
	cm.observeEvent("codex", j(`{"type":"usage.observation"}`), 120)
	cm.observeEvent("codex", j(`{"type":"item.completed","item":{"id":"i1","type":"mcpToolCall","server":"git","tool":"status","status":"completed"}}`), 130)
	cm.observeEvent("codex", j(`{"type":"item.completed","item":{"id":"i2","type":"commandExecution","exitCode":1}}`), 140)
	cm.observeEvent("codex", j(`{"type":"item.completed","item":{"id":"i3","type":"mcpToolCall","server":"linear","tool":"issue_list","status":"completed"}}`), 150)
	cm.observeEvent("codex", j(`{"type":"item.completed","item":{"id":"i4","type":"function_call","name":"mcp__linear__issue_create"}}`), 160)
	cm.observeEvent("codex", j(`{"type":"unparsed"}`), 170)
	cm.observeEvent("codex", j(`{"type":"protocol.write"}`), 180)
	cm.observeEvent("codex", j(`{"type":"stream.failed"}`), 190)

	var cout contract.ExecutionResourceSummary
	cm.applyTo("codex", &cout)
	if cout.ActiveMs == nil {
		t.Fatal("codex ActiveMs expected non-nil")
	}
	if cout.TurnCount == nil || *cout.TurnCount != 2 {
		t.Fatalf("codex TurnCount got %v want 2", cout.TurnCount)
	}
	if got := cout.ToolCalls["git/status"]; got != (contract.ToolCallCounter{Calls: 1, Errors: 0}) {
		t.Fatalf("codex git/status got %+v", got)
	}
	if got := cout.ToolCalls["commandExecution"]; got != (contract.ToolCallCounter{Calls: 1, Errors: 1}) {
		t.Fatalf("codex commandExecution got %+v", got)
	}
	for k := range cout.ToolCalls {
		if strings.HasPrefix(k, "linear/") {
			t.Fatalf("gateway connection tool leaked into Codex ToolCalls: %q", k)
		}
	}

	// Claude: ID-less tool_use calls must not collide, named tool_use error tracking, gateway exclusion, num_turns + duration_ms.
	clm := newRunMetrics(map[string]bool{"github": true})
	clm.observeEvent("claude", j(`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash"},{"type":"tool_use","name":"Bash"},{"type":"tool_use","id":"t1","name":"Edit"},{"type":"tool_use","id":"t2","name":"mcp__github__pr_view"}]}}`), 200)
	clm.observeEvent("claude", j(`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","is_error":true},{"type":"tool_result","tool_use_id":"t2","is_error":true}]}}`), 250)
	clm.observeEvent("claude", j(`{"type":"result","num_turns":3,"duration_ms":450}`), 300)

	var clout contract.ExecutionResourceSummary
	clm.applyTo("claude", &clout)
	if clout.TurnCount == nil || *clout.TurnCount != 3 {
		t.Fatalf("claude TurnCount got %v want 3", clout.TurnCount)
	}
	if clout.ActiveMs == nil || *clout.ActiveMs != 450 {
		t.Fatalf("claude ActiveMs got %v want 450", clout.ActiveMs)
	}
	if got := clout.ToolCalls["Bash"]; got != (contract.ToolCallCounter{Calls: 2, Errors: 0}) {
		t.Fatalf("claude Bash got %+v", got)
	}
	if got := clout.ToolCalls["Edit"]; got != (contract.ToolCallCounter{Calls: 1, Errors: 1}) {
		t.Fatalf("claude Edit got %+v", got)
	}
	for k := range clout.ToolCalls {
		if strings.HasPrefix(k, "github/") {
			t.Fatalf("gateway connection tool leaked into Claude ToolCalls: %q", k)
		}
	}

	// ACP: acpSession prompt durationMs (int64), acp.update + PLANNER_RESPONSE + TOOL_RESPONSE, gateway exclusion.
	as := &acpSession{session: "s1", profile: M{"text": "hi"}}
	as.prompt()
	_, aev, adone, aerr := as.Receive(j(`{"id":5,"result":{"stopReason":"end_turn"}}`))
	if aerr != nil || !adone {
		t.Fatalf("acp Receive unexpected done=%v err=%v", adone, aerr)
	}
	if _, ok := aev["durationMs"].(int64); !ok {
		t.Fatalf("expected int64 acp durationMs, got %T (%v)", aev["durationMs"], aev["durationMs"])
	}
	am := newRunMetrics(map[string]bool{"github": true})
	am.observeEvent("acp", aev, 400)
	am.observeEvent("acp", j(`{"type":"acp.update","update":{"sessionUpdate":"tool_call","toolCallId":"tc1","name":"Read"}}`), 410)
	am.observeEvent("acp", j(`{"type":"acp.update","update":{"sessionUpdate":"tool_call_update","toolCallId":"tc1","status":"failed"}}`), 420)
	am.observeEvent("antigravity", j(`{"type":"PLANNER_RESPONSE","tool_calls":[{"id":"c1","name":"github/issues"},{"id":"c2","name":"grep"}]}`), 430)
	am.observeEvent("antigravity", j(`{"type":"TOOL_RESPONSE","tool_call_id":"c1","status":"ERROR"}`), 440)
	am.observeEvent("antigravity", j(`{"type":"TOOL_RESPONSE","tool_call_id":"c2","status":"ERROR"}`), 450)

	var aout contract.ExecutionResourceSummary
	am.applyTo("acp", &aout)
	if aout.ActiveMs == nil {
		t.Fatal("acp ActiveMs expected non-nil")
	}
	if got := aout.ToolCalls["Read"]; got != (contract.ToolCallCounter{Calls: 1, Errors: 1}) {
		t.Fatalf("acp Read got %+v", got)
	}
	if got := aout.ToolCalls["grep"]; got != (contract.ToolCallCounter{Calls: 1, Errors: 1}) {
		t.Fatalf("acp grep got %+v", got)
	}
	if _, ok := aout.ToolCalls["github/issues"]; ok {
		t.Fatal("gateway connection tool github/issues leaked into ACP ToolCalls")
	}

	// Overflow (>32 distinct tools): keep top 32 by (-Errors, -Calls, name) and aggregate rest into ToolCallsOther.
	om := newRunMetrics(nil)
	for i := 0; i < 35; i++ {
		name := fmt.Sprintf("t%02d", i)
		calls := 40 - i // t00..t34 strictly decreasing calls
		for c := 0; c < calls; c++ {
			id := fmt.Sprintf("id-%d-%d", i, c)
			ev := M{
				"type":    "assistant",
				"message": M{"content": []any{M{"type": "tool_use", "id": id, "name": name}}},
			}
			om.observeEvent("claude", ev, int64(500+i))
			// Give t34 one error so error-bearing tools sort first and remain retained.
			if i == 34 && c == 0 {
				om.observeEvent("claude", M{
					"type":    "user",
					"message": M{"content": []any{M{"type": "tool_result", "tool_use_id": id, "is_error": true}}},
				}, 600)
			}
		}
	}
	var oout contract.ExecutionResourceSummary
	om.applyTo("claude", &oout)
	if err := contract.Check(&oout, "ExecutionResourceSummary"); err != nil {
		t.Fatal(err)
	}
	if len(oout.ToolCalls) != maxToolEntries {
		t.Fatalf("expected %d kept tool entries, got %d", maxToolEntries, len(oout.ToolCalls))
	}
	if _, ok := oout.ToolCalls["t34"]; !ok {
		t.Fatal("expected error-bearing tool t34 to be retained in top 32")
	}
	// Retained: t34 (1 error, 6 calls) + t00..t30 (0 errors, 40..10 calls). Overflow: t31 (9), t32 (8), t33 (7) => 24 calls, 0 errors.
	if oout.ToolCallsOther == nil || *oout.ToolCallsOther != (contract.ToolCallCounter{Calls: 24, Errors: 0}) {
		t.Fatalf("unexpected ToolCallsOther: %+v want {Calls:24 Errors:0}", oout.ToolCallsOther)
	}
}
