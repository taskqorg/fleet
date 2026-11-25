package main

// Harness protocol tests: each guards a native wire risk.

import (
	"encoding/json"
	"errors"
	"fleet/contract"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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
	if slices.Contains(app.Args, "skills.enabled=false") || !slices.Contains(app.Args, "skills.include_instructions=false") || !slices.Contains(app.Args, "skip_host_skill_discovery") {
		t.Fatalf("unexpected Codex skill controls: %v", app.Args)
	}
}

// Requirement: every codex launch applies one isolation: the run's exec command keeps all of it through codexAppServer,
// and the account limits probe starts app-server with the same flags and the same host environment. Risk: a copy of
// the list drifts, so a probe or run starts user MCP servers, plugins, browser or project docs the other path blocks.
func TestCodexIsolationEveryPath(t *testing.T) {
	isolation := [][2]string{{"-c", "project_doc_max_bytes=0"}, {"-c", "skills.include_instructions=false"}, {"-c", "mcp_servers={}"},
		{"--disable", "memories"}, {"--disable", "hooks"}, {"--enable", "skip_host_skill_discovery"},
		{"-c", `forced_login_method="chatgpt"`}, {"-c", `approval_policy="never"`}, {"-c", `sandbox_mode="workspace-write"`},
		{"-c", "sandbox_workspace_write.network_access=false"}, {"--disable", "apps"}, {"--disable", "plugins"},
		{"--disable", "browser_use"}, {"--disable", "computer_use"}, {"--disable", "image_generation"}}
	hasPair := func(args []string, p [2]string) bool {
		for i := 0; i+1 < len(args); i++ {
			if args[i] == p[0] && args[i+1] == p[1] {
				return true
			}
		}
		return false
	}
	root := t.TempDir()
	mk := func(p string) string { d := root + "/" + p; os.MkdirAll(d, 0o700); return d }
	argv, envOut := root+"/argv", root+"/env"
	fake := root + "/codex"
	os.WriteFile(fake, []byte("#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > "+argv+"\nenv > "+envOut+"\n"), 0o700)
	reg := root + "/accounts.json"
	entry := registryEntry{Harness: "codex", Home: mk("h"), NativeHome: mk("nh"), Executable: fake}
	os.WriteFile(reg, []byte(enc(M{"cx": M{"harness": "codex", "home": entry.Home, "nativeHome": entry.NativeHome, "executable": fake}})), 0o600)
	t.Setenv("FLEET_NATIVE_ACCOUNTS", reg)
	t.Setenv("OPENAI_API_KEY", "leak")
	t.Setenv("LC_ALL", "C")

	run, _, err := Harnesses["codex"].Command(mk("w"), "", M{"account": "cx", "text": "t"})
	if err != nil {
		t.Fatal(err)
	}
	app := codexAppServer(run)
	for _, p := range isolation {
		if !hasPair(run.Args, p) || !hasPair(app.Args, p) {
			t.Errorf("run lost %v: exec %v, app-server %v", p, run.Args, app.Args)
		}
	}
	// exec-only flags: codex app-server refuses them (codexExecOnly says why), so they stay on exec alone.
	for _, f := range []string{"--ignore-user-config", "--ignore-rules", "--skip-git-repo-check"} {
		if !slices.Contains(run.Args, f) || slices.Contains(app.Args, f) {
			t.Errorf("exec-only %s: exec %v, app-server %v", f, run.Args, app.Args)
		}
	}

	codexReadLimits(entry) // the fake CLI records its argv and environment, then exits without answering
	raw, err := os.ReadFile(argv)
	if err != nil {
		t.Fatal(err)
	}
	probe := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if probe[0] != "app-server" {
		t.Fatal(probe)
	}
	for _, p := range isolation {
		if !hasPair(probe, p) {
			t.Errorf("limits probe lacks %v: %v", p, probe)
		}
	}
	env, _ := os.ReadFile(envOut)
	if !strings.Contains(string(env), "LC_ALL=C\n") || strings.Contains(string(env), "OPENAI") || !slices.Contains(run.Env, "LC_ALL=C") {
		t.Errorf("probe and run environments differ: probe %q, run %v", env, run.Env)
	}
}

// Requirement: a Claude get_usage answer (shape seen live from Claude Code 2.1.319) becomes limit windows: seven_day
// as weekly with its percent and reset, model_scoped as weekly-<model>, null windows and non-limit keys skipped
// (fleet-people-access.md section 8). Risk: Claude subscriptions show no percent and no per-model limit.
func TestClaudeUsageReading(t *testing.T) {
	answer := j(`{"rate_limits":{"five_hour":null,"seven_day":{"utilization":74,"resets_at":"2026-11-16T05:00:00.015107+00:00","limit_dollars":null},
		"seven_day_opus":null,"seven_day_breakdown":{"rows":[]},"extra_usage":{"utilization":null},
		"model_scoped":[{"display_name":"Fable","utilization":14,"resets_at":"2026-11-16T05:00:00.015240+00:00"}]}}`)
	eq(t, claudeUsageReading(json.RawMessage(enc(answer)), nil)["readings"],
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
		calls := 40 - i // t00..t34 decreasing calls, with tie at t30 and t31 (both 10 calls) to verify lexicographical retention.
		if i == 31 {
			calls = 10
		}
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
	if _, ok := oout.ToolCalls["t30"]; !ok {
		t.Fatal("expected lexicographically earlier tied tool t30 to be retained over t31")
	}
	if _, ok := oout.ToolCalls["t31"]; ok {
		t.Fatal("expected tied tool t31 to overflow into ToolCallsOther")
	}
	// Retained: t34 (1 error, 6 calls) + t00..t30 (0 errors, 40..10 calls). Overflow: t31 (10), t32 (8), t33 (7) => 25 calls, 0 errors.
	if oout.ToolCallsOther == nil || *oout.ToolCallsOther != (contract.ToolCallCounter{Calls: 25, Errors: 0}) {
		t.Fatalf("unexpected ToolCallsOther: %+v want {Calls:25 Errors:0}", oout.ToolCallsOther)
	}
}

// Requirement: fleet-conversations.md "Start a conversation": a read-only preset starts the agent without write tools.
// Risk: a Discuss or Review conversation edits the repository.
func TestReadOnlyStart(t *testing.T) {
	if got := strings.Join(readOnlyTools([]string{"Read", "Edit", "Write", "Bash", "Glob", "Grep", "Task", "TaskStop", "Skill"}), ","); got != "Read,Glob,Grep,Skill" {
		t.Fatal(got)
	}
	c := exec.Command("not-executed")
	claudeMCP(c, M{"readOnly": true}, M{}, nil)
	if c.Args[len(c.Args)-1] != "Read,Glob,Grep" {
		t.Fatalf("read-only grant: %v", c.Args)
	}
	s := &codexSession{root: "/w", profile: j(`{"model":"m","readOnly":true,"writableRoots":["/w/.git"],"text":"t"}`), nextID: 100}
	r, _, _, _ := s.Receive(j(`{"id":1,"result":{}}`))
	if jAt(r[1], "params", "sandbox") != "read-only" {
		t.Fatal(r[1])
	}
	if jAt(s.turnRequest(), "params", "sandboxPolicy", "type") != "readOnly" {
		t.Fatal(s.turnRequest())
	}
	w := &codexSession{root: "/w", profile: j(`{"model":"m","text":"t"}`), nextID: 100}
	if r, _, _, _ := w.Receive(j(`{"id":1,"result":{}}`)); jAt(r[1], "params", "sandbox") != "workspace-write" {
		t.Fatal("a write start keeps the workspace sandbox", r[1])
	}
	if a := newACPSession("/w", "", M{"readOnly": true, "agentConfig": M{"settings": M{"mode": "accept-edits"}}}, nil); a.mode != "plan" {
		t.Fatal(a.mode)
	}
}

// Requirement: T-FLT-54 gives managed Codex commands writable Go caches.
// Risk: a cache stays under the account home, is omitted from the native sandbox, or expands a read-only grant.
func TestCodexGoCacheLaunch(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(dir, "go-cache")
	cmd := newCmd("/bin/echo", dir)
	readOnly := M{"readOnly": true}
	if _, err := codexLaunchProfile(cmd, dir, readOnly, nil); err != nil || len(cmd.Env) != 0 {
		t.Fatalf("read-only launch changed the environment: %v, %v", cmd.Env, err)
	}
	if _, err := os.Lstat(cache); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only launch created a cache: %v", err)
	}

	for _, gitNetwork := range []bool{false, true} {
		var gitRoots []any
		if gitNetwork {
			gitRoots = []any{filepath.Join(dir, "project.git")}
		}
		input := M{"gitNetwork": gitNetwork}
		profile, err := codexLaunchProfile(cmd, dir, input, gitRoots)
		if err != nil {
			t.Fatal(err)
		}
		if has(input, "writableRoots") {
			t.Fatal("launch modified the input profile")
		}
		wantRoots := append([]any{cache}, gitRoots...)
		for _, kv := range []string{"GOCACHE=" + filepath.Join(cache, "build"), "GOMODCACHE=" + filepath.Join(cache, "modules")} {
			if !slices.Contains(codexAppServer(cmd).Env, kv) {
				t.Fatalf("missing %q in app-server environment", kv)
			}
		}
		s := &codexSession{profile: profile}
		r, _, _, err := s.Receive(j(`{"id":1,"result":{}}`))
		if err != nil {
			t.Fatal(err)
		}
		eq(t, jAt(r[1], "params", "config", "sandbox_workspace_write.writable_roots"), enc(wantRoots))
		policy := M{"type": "workspaceWrite", "writableRoots": wantRoots, "networkAccess": gitNetwork}
		eq(t, jAt(s.turnRequest(), "params", "sandboxPolicy"), enc(policy))
	}
	if err := os.Remove(cache); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), cache); err != nil {
		t.Fatal(err)
	}
	if _, err := codexLaunchProfile(newCmd("/bin/echo", dir), dir, M{}, nil); err == nil {
		t.Fatal("accepted a cache symlink as a writable root")
	}
}

// Requirement: a codex or claude start launches on Linux, whose kernel refuses any one argument over 128 KiB
// (MAX_ARG_STRLEN). Risk (live 2026-11-17): the director's operation list with schemas, escaped through the MCP
// wrappers, outgrew it and every codex PM start on a Linux host failed at exec with no event.
func TestFleetMCPContextStaysOutOfArgv(t *testing.T) {
	ops := []any{}
	for n, o := range contract.Operations {
		var s any
		if err := json.Unmarshal(o.InputSchema, &s); err != nil {
			t.Fatal(n, err)
		}
		ops = append(ops, M{"name": n, "inputSchema": s, "description": strings.Repeat("Describes the operation. ", 40)})
	}
	dir := t.TempDir()
	ctx := M{"hostId": "h", "commandId": "c", "executionId": "e", "projectId": "p", "workdir": dir, "operations": ops}
	fleet, err := delegationServer(ctx, filepath.Join(dir, "journal.sqlite"), filepath.Join(dir, "e.fleet.json"))
	if err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "e.fleet.json")); err != nil || len(b) < 100000 {
		t.Fatalf("the context file holds the operations: %d bytes, %v", len(b), err)
	}
	servers, err := wrapMCPServers(M{"fleet": fleet}, filepath.Join(dir, "s.sock"), dir, filepath.Join(dir, "r"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"codex", "claude"} {
		cmd := exec.Command(name)
		harness{name: name}.WireMCP(cmd, M{}, servers, nil)
		for _, a := range cmd.Args {
			if len(a) > 64<<10 {
				t.Fatalf("%s: one argument is %d bytes", name, len(a))
			}
		}
	}
}

// Requirement: ACP v1 config options select the exact model and mode before prompting.
// Risk: modern peers cannot launch, or a peer silently substitutes a model or skips read-only mode.
func TestACPSelectOptions(t *testing.T) {
	profile := j(`{"model":"requested","text":"check","readOnly":true}`)
	model := func(value string) string {
		return `{"id":"model-choice","category":"model","type":"select","currentValue":"` + value + `","options":[{"group":"Provider","options":[{"value":"requested"}]}]}`
	}
	mode := func(value string) string {
		return `{"id":"mode-choice","category":"mode","type":"select","currentValue":"` + value + `","options":[{"value":"plan"}]}`
	}
	for _, first := range []string{"default", "requested"} {
		t.Run(first, func(t *testing.T) {
			s := newACPSession("/work", "", profile, nil)
			s.stage = 1
			r, _, _, err := s.Receive(j(`{"id":2,"result":{"sessionId":"s","configOptions":[{"id":"fallback","category":"model","type":"select","currentValue":"other","options":[{"value":"other"}]},` + model(first) + `,` + mode("build") + `]}}`))
			if first == "default" {
				if err != nil || jStr(r[0], "method") != "session/set_config_option" || jStr(r[0], "params", "configId") != "model-choice" || jStr(r[0], "params", "value") != "requested" {
					t.Fatal(r, err)
				}
				if _, _, _, err := s.Receive(j(`{"id":3,"result":{"configOptions":[` + model("default") + `]}}`)); err == nil {
					t.Fatal("model substitution accepted")
				}
				r, _, _, err = s.Receive(j(`{"id":3,"result":{"configOptions":[{"id":"secondary","category":"mode","type":"select","currentValue":"plan"},` + model("requested") + `,` + mode("build") + `]}}`))
			}
			if err != nil || jStr(r[0], "method") != "session/set_config_option" || jStr(r[0], "params", "configId") != "mode-choice" || jStr(r[0], "params", "value") != "plan" {
				t.Fatal(r, err)
			}
			if _, _, _, err := s.Receive(j(`{"id":4,"result":{"configOptions":[` + model("default") + `,` + mode("plan") + `]}}`)); err == nil {
				t.Fatal("mode update changed the selected model")
			}
			r, _, _, err = s.Receive(j(`{"id":4,"result":{"configOptions":[` + model("requested") + `,` + mode("plan") + `]}}`))
			if err != nil || jStr(r[0], "method") != "session/prompt" {
				t.Fatal(r, err)
			}
		})
	}
}

// Requirement: dual-advertising peers still accept an exact legacy model-and-effort ID.
// Risk: matching a base model selector would drop the requested effort or refuse Antigravity's existing profiles.
func TestACPLegacyModelAndEffort(t *testing.T) {
	s := newACPSession("/work", "", M{"model": "model-low"}, nil)
	s.stage = 1
	r, _, _, err := s.Receive(j(`{"id":2,"result":{"sessionId":"s","configOptions":[{"id":"model","category":"model","type":"select","currentValue":"model","options":[{"value":"model"}]}],"models":{"availableModels":[{"modelId":"model-low"}]}}}`))
	if err != nil || jStr(r[0], "method") != "session/set_model" || jStr(r[0], "params", "modelId") != "model-low" {
		t.Fatal(r, err)
	}
}

// Requirement: legacy model changes still require a fresh acknowledgement of modern plan mode.
// Risk: the peer resets plan mode while applying the model, and Fleet admits a writable turn.
func TestACPLegacyModelConfirmsPlan(t *testing.T) {
	s := newACPSession("/work", "", M{"model": "model", "readOnly": true}, nil)
	s.stage = 1
	r, _, _, err := s.Receive(j(`{"id":2,"result":{"sessionId":"s","models":{"availableModels":[{"modelId":"model"}]},"configOptions":[{"id":"mode","category":"mode","type":"select","currentValue":"plan","options":[{"value":"plan"}]}]}}`))
	if err != nil || jStr(r[0], "method") != "session/set_model" {
		t.Fatal(r, err)
	}
	r, _, _, err = s.Receive(j(`{"id":3,"result":{}}`))
	if err != nil || jStr(r[0], "method") != "session/set_config_option" || jStr(r[0], "params", "value") != "plan" {
		t.Fatal(r, err)
	}
	if _, _, _, err := s.Receive(j(`{"id":4,"result":{}}`)); err == nil {
		t.Fatal("prompted without a fresh plan acknowledgement")
	}
	const plan = `"configOptions":[{"id":"mode","category":"mode","type":"select","currentValue":"plan"}]`
	if _, _, _, err := s.Receive(j(`{"id":4,"result":{` + plan + `,"models":{"currentModelId":"other"}}}`)); err == nil {
		t.Fatal("mode change silently changed the legacy model")
	}
	r, _, _, err = s.Receive(j(`{"id":4,"result":{` + plan + `}}`))
	if err != nil || jStr(r[0], "method") != "session/prompt" {
		t.Fatal(r, err)
	}

}

// Requirement: a legacy mode setter can acknowledge with an empty result.
// Risk: a peer with a modern model selector and legacy modes never gets prompted.
func TestACPModernModelWithLegacyMode(t *testing.T) {
	s := newACPSession("/work", "", M{"model": "model", "readOnly": true}, nil)
	s.stage = 1
	r, _, _, err := s.Receive(j(`{"id":2,"result":{"sessionId":"s","configOptions":[{"id":"model","type":"select","currentValue":"model"}],"modes":{"availableModes":[{"id":"plan"}]}}}`))
	if err != nil || jStr(r[0], "method") != "session/set_mode" {
		t.Fatal(r, err)
	}
	r, _, _, err = s.Receive(j(`{"id":4,"result":{}}`))
	if err != nil || jStr(r[0], "method") != "session/prompt" {
		t.Fatal(r, err)
	}
}

// Requirement: a peer can expose the exact model after confirming its requested mode.
// Risk: Fleet refuses usable profiles or prompts before it verifies dependent options.
func TestACPModelDependsOnMode(t *testing.T) {
	s := newACPSession("/work", "", M{"model": "requested", "text": "task", "readOnly": true}, nil)
	s.stage = 1
	r, _, _, err := s.Receive(j(`{"id":2,"result":{"sessionId":"s","configOptions":[{"id":"mode","type":"select","currentValue":"build","options":[{"value":"plan"}]},{"id":"secondary-mode","category":"mode","type":"select","currentValue":"plan"}]}}`))
	if err != nil || jStr(r[0], "params", "configId") != "mode" || jStr(r[0], "params", "value") != "plan" {
		t.Fatal(r, err)
	}
	r, _, _, err = s.Receive(j(`{"id":6,"result":{"configOptions":[{"id":"mode","type":"select","currentValue":"plan"},{"id":"model","type":"select","currentValue":"default","options":[{"value":"requested"}]}]}}`))
	if err != nil || jStr(r[0], "params", "configId") != "model" || jStr(r[0], "params", "value") != "requested" {
		t.Fatal(r, err)
	}
	r, _, _, err = s.Receive(j(`{"id":3,"result":{"configOptions":[{"id":"mode","type":"select","currentValue":"plan"},{"id":"model","type":"select","currentValue":"requested"}]}}`))
	if err != nil || jStr(r[0], "method") != "session/prompt" {
		t.Fatal(r, err)
	}
}

// Requirement: Antigravity can invoke the explicitly injected Code Mode MCP tools in its own turn.
// Risk: a broad ACP permission handler would also authorize native commands, unrelated tools or other sessions.
func TestACPCodePermission(t *testing.T) {
	const valid = `{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{"sessionId":"s","toolCall":{"toolCallId":"call-1","name":"mcp_code_code","kind":"other","status":"pending"},"options":[{"kind":"allow_always","optionId":"all"},{"kind":"allow_once","optionId":"once"}]}}`
	for _, tc := range []struct {
		name, harness, mode string
		server              bool
		request             string
		want                string
	}{
		{"code", "antigravity", "only", true, valid, "once"},
		{"poll", "antigravity", "add", true, strings.Replace(valid, "mcp_code_code", "mcp_code_poll", 1), "once"},
		{"different session", "antigravity", "only", true, strings.Replace(valid, `"sessionId":"s"`, `"sessionId":"other"`, 1), ""},
		{"native command", "antigravity", "only", true, strings.Replace(valid, "mcp_code_code", "run_command", 1), ""},
		{"filesystem", "antigravity", "only", true, strings.Replace(valid, "session/request_permission", "fs/read_text_file", 1), ""},
		{"persistent permission", "antigravity", "only", true, strings.Replace(valid, "allow_once", "reject_once", 1), ""},
		{"code unavailable", "antigravity", "only", false, valid, ""},
		{"code disabled", "antigravity", "", true, valid, ""},
		{"other ACP peer", "acp", "only", true, valid, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			servers := M{}
			if tc.server {
				servers["code"] = M{}
			}
			s := newACPSession("/work", "", M{"harness": tc.harness, "agentConfig": M{"codeMode": tc.mode}}, servers)
			s.session, s.stage = "s", 5
			r, ev, _, err := s.Receive(j(tc.request))
			if err != nil || jStr(r[0], "result", "outcome", "optionId") != tc.want ||
				(ev["type"] == "permission.granted") != (tc.want != "") {
				t.Fatal(r, ev, err)
			}
		})
	}
}

// Requirement: sandboxed Antigravity sessions in accept-edits mode can execute commands via run_command (#640).
// Risk: refusing run_command prevents Antigravity workers from writing results, while granting it without sandbox=true or accept-edits mode allows unsandboxed or read-only shell execution.
func TestACPSandboxRunCommandPermission(t *testing.T) {
	const req = `{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{"sessionId":"s","toolCall":{"toolCallId":"call-1","name":"run_command","title":"mkdir -p output","kind":"execute","status":"pending"},"options":[{"kind":"allow_always","optionId":"all"},{"kind":"allow_once","optionId":"once"}]}}`
	for _, tc := range []struct {
		name    string
		profile M
		stage   int
		request string
		want    string
	}{
		{"sandboxed accept-edits agentConfig", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": true}}}, 5, req, "once"},
		{"sandboxed accept-edits top-level", M{"harness": "antigravity", "settings": M{"mode": "accept-edits", "sandbox": true}}, 5, req, "once"},
		{"sandbox false refused", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": false}}}, 5, req, ""},
		{"sandbox omitted refused", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "accept-edits"}}}, 5, req, ""},
		{"plan mode refused", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "plan", "sandbox": true}}}, 5, req, ""},
		{"readOnly overrides accept-edits", M{"harness": "antigravity", "readOnly": true, "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": true}}}, 5, req, ""},
		{"non-antigravity acp refused", M{"harness": "acp", "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": true}}}, 5, req, ""},
		{"wrong stage refused", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": true}}}, 3, req, ""},
		{"wrong session refused", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": true}}}, 5, strings.Replace(req, `"sessionId":"s"`, `"sessionId":"other"`, 1), ""},
		{"non-run_command execute refused", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": true}}}, 5, strings.Replace(req, `"run_command"`, `"send_command_input"`, 1), ""},
		{"non-execute kind refused", M{"harness": "antigravity", "agentConfig": M{"settings": M{"mode": "accept-edits", "sandbox": true}}}, 5, strings.Replace(req, `"execute"`, `"edit"`, 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newACPSession("/work", "", tc.profile, M{})
			s.session, s.stage = "s", tc.stage
			r, ev, _, err := s.Receive(j(tc.request))
			if err != nil || jStr(r[0], "result", "outcome", "optionId") != tc.want ||
				(ev["type"] == "permission.granted") != (tc.want != "") {
				t.Fatal(r, ev, err)
			}
		})
	}
}

// Requirement: FLT-4 — Antigravity 1.5.10 sessions can invoke Fleet-injected MCP server tools (connections, fleet, design-rules, project-forge) via session/request_permission.
// Risk: refusing injected mcp_<server>_<tool> requests breaks all direct MCP and Fleet tools on antigravity 1.5.10, while a blanket kind=="other" allow or unsanitized prefix match would authorize uninjected MCP servers, disabled code servers, or native antigravity tools.
func TestACPInjectedMCPPermission(t *testing.T) {
	const req = `{"jsonrpc":"2.0","id":9,"method":"session/request_permission","params":{"sessionId":"s","toolCall":{"toolCallId":"call-1","name":"mcp_tavily_web_search","kind":"other","status":"pending"},"options":[{"kind":"allow_always","optionId":"all"},{"kind":"allow_once","optionId":"once"}]}}`
	servers := M{
		"tavily":       M{},
		"fleet":        M{},
		"design-rules": M{},
		"custom.srv":   M{},
		"code":         M{},
	}
	for _, tc := range []struct {
		name    string
		harness string
		stage   int
		request string
		want    string
	}{
		{"injected connection mcp_tavily_web_search", "antigravity", 5, req, "once"},
		{"injected fleet mcp_fleet_execution_status", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_fleet_execution_status", 1), "once"},
		{"hyphenated server kept mcp_design-rules_get_design_rules", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_design-rules_get_design_rules", 1), "once"},
		{"hyphenated server sanitized mcp_design_rules_get_design_rules", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_design_rules_get_design_rules", 1), "once"},
		{"sanitized punctuation server mcp_custom_srv_ping", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_custom_srv_ping", 1), "once"},
		{"uninjected mcp server refused", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_youtrack_issue_get", 1), ""},
		{"prefix collision refused", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_tavily2_web_search", 1), ""},
		{"empty tool suffix refused", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_tavily_", 1), ""},
		{"disabled code server still refused", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "mcp_code_code", 1), ""},
		{"native tool with kind other refused", "antigravity", 5, strings.Replace(req, "mcp_tavily_web_search", "read_file", 1), ""},
		{"non-other kind refused", "antigravity", 5, strings.Replace(req, `"kind":"other"`, `"kind":"execute"`, 1), ""},
		{"non-antigravity acp refused", "acp", 5, req, ""},
		{"wrong stage refused", "antigravity", 3, req, ""},
		{"wrong session refused", "antigravity", 5, strings.Replace(req, `"sessionId":"s"`, `"sessionId":"other"`, 1), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newACPSession("/work", "", M{"harness": tc.harness}, servers)
			s.session, s.stage = "s", tc.stage
			r, ev, _, err := s.Receive(j(tc.request))
			if err != nil || jStr(r[0], "result", "outcome", "optionId") != tc.want ||
				(ev["type"] == "permission.granted") != (tc.want != "") {
				t.Fatal(r, ev, err)
			}
		})
	}
}
