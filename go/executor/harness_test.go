package main

// Harness protocol tests ported from harness.rs, codex.rs and antigravity.rs: each guards a native wire risk.

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func j(s string) obj {
	var v obj
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		panic(s)
	}
	return v
}

func eq(t *testing.T, got any, want string) {
	t.Helper()
	var w any
	json.Unmarshal([]byte(want), &w)
	if encode(clone(got)) != encode(clone(w)) {
		t.Fatalf("got %s want %s", encode(got), want)
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
	chunk := func(s string) obj {
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
	if st := get(c.Project("claude", j(`{"type":"result","is_error":false}`), 50)["blocks"].([]any)[0], "state"); st != "unfinished" {
		t.Fatal(st)
	}
	e := b.Project("codex", j(`{"type":"item.completed","item":{"type":"error","message":"stream disconnected"}}`), 5)["blocks"].([]any)[0]
	if get(e, "state") != "failed" || get(e, "output") != "stream disconnected" {
		t.Fatal(e)
	}
	u := a.Project("codex", j(`{"type":"unparsed","raw":"x"}`), 7)["blocks"].([]any)[0]
	if get(u, "name") != "unparsed" || get(u, "output", "raw") != "x" {
		t.Fatal(u)
	}
	// Risk: ACP usage categories merged, or an empty rawOutput hides a diff result.
	p = project("acp", j(`{"result":{"usage":{"cachedReadTokens":4046,"totalTokens":35281}},"update":{"sessionUpdate":"tool_call_update","toolCallId":"1","rawOutput":{},"content":[{"type":"diff"}]}}`))
	if get(p, "usage", "cache_read_tokens") != 4046.0 || has(get(p, "usage"), "cached_input_tokens") || get(p["tools"].([]any)[0], "result").([]any)[0].(obj)["type"] != "diff" {
		t.Fatal(encode(p))
	}
}

// Risk: codex handshake order, resume identity, developer-role injection, approval refusal and steer correlation drift.
func TestCodexSession(t *testing.T) {
	for _, resume := range []string{"", "thread"} {
		s := &codexSession{root: "/w", resume: resume, profile: j(`{"model":"m","effort":"low","instructions":"dev","text":"task"}`), nextID: 100}
		r, _, _, _ := s.Receive(j(`{"id":1,"result":{"userAgent":"codex/9"}}`))
		want := map[bool]string{true: "thread/resume", false: "thread/start"}[resume != ""]
		if len(r) != 2 || r[1]["method"] != want || get(r[1], "params", "developerInstructions") != "dev" {
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
		if get(f, "params", "expectedTurnId") != "turn" {
			t.Fatal(f)
		}
		_, ev, done, _ := s.Receive(obj{"id": f["id"], "error": obj{"code": -1}})
		if ev["type"] != "steer.result" || ev["ok"] != false || done {
			t.Fatal(ev)
		}
		_, ev, _, _ = s.Receive(j(`{"method":"item/completed","params":{"threadId":"thread","turnId":"turn","item":{"type":"collabAgentToolCall","tool":"spawnAgent","receiverThreadIds":["c"],"agentsStates":{"c":{"status":"running"}}}}}`))
		if ch := ev["children"].([]any)[0]; get(ch, "parentThreadId") != "thread" || get(ch, "binding", "model") != "m" {
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
}

// Risk: ACP prompts without the qualified runtime, the requested model or mode, or answers client permission requests.
func TestACPSession(t *testing.T) {
	profile := j(`{"model":"g","text":"task","agentConfig":{"instructions":"explicit user","settings":{"mode":"accept-edits"}}}`)
	opened := `{"id":2,"result":{"sessionId":"s","models":{"availableModels":[{"modelId":"g"}]},"modes":{"availableModes":[{"id":"accept-edits"}]}}}`
	for _, caps := range []string{`{"loadSession":true}`, `{"sessionCapabilities":{"resume":{}}}`} {
		s := newACPSession("/w", "s", profile, obj{"fleet": obj{"command": "x", "args": []any{}}})
		r, _, _, err := s.Receive(j(`{"id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"1.5.3"},"agentCapabilities":` + caps + `}}`))
		if err != nil || get(r[0], "params", "sessionId") != "s" || len(get(r[0], "params", "mcpServers").([]any)) != 1 {
			t.Fatal(r, err)
		}
		steps := []string{opened, `{"id":3,"result":{}}`, `{"id":4,"result":{}}`}
		for i, m := range []string{"session/set_model", "session/set_mode", "session/prompt"} {
			r, _, _, err = s.Receive(j(steps[i]))
			if err != nil || r[0]["method"] != m {
				t.Fatal(m, r, err)
			}
		}
		if !strings.Contains(sv(get(r[0], "params", "prompt").([]any)[0].(obj)["text"]), "user role") {
			t.Fatal(r)
		}
		if r, ev, _, _ := s.Receive(j(`{"id":9,"method":"session/request_permission","params":{}}`)); get(r[0], "result", "outcome", "outcome") != "cancelled" || ev["type"] != "permission.refused" {
			t.Fatal(r)
		}
		if _, _, done, _ := s.Receive(j(`{"id":5,"result":{"stopReason":"end_turn"}}`)); !done {
			t.Fatal("not done")
		}
	}
	s := newACPSession("/w", "", profile, obj{})
	if _, _, _, err := s.Receive(j(`{"id":1,"result":{"protocolVersion":1,"agentInfo":{"version":"1.4.0"}}}`)); err == nil {
		t.Fatal("unqualified runtime accepted")
	}
	s = newACPSession("/w", "", profile, obj{})
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
	accounts := obj{"ag": obj{"harness": "antigravity", "home": mk("h"), "nativeHome": mk("nh"), "executable": "/bin/echo"},
		"cx": obj{"harness": "codex", "home": mk("h2"), "nativeHome": mk("nh2"), "executable": "/bin/echo"}}
	os.WriteFile(reg, []byte(encode(accounts)), 0o600)
	t.Setenv("FLEET_NATIVE_ACCOUNTS", reg)
	t.Setenv("OPENAI_API_KEY", "leak")
	c, in, err := Harnesses["antigravity"].Command(work, "", obj{"harness": "antigravity", "account": "ag", "model": "g", "text": "t", "settings": obj{"sandbox": false}})
	if err != nil || in != nil {
		t.Fatal(err)
	}
	args := strings.Join(c.Args, " ")
	if !strings.Contains(args, "acp --gemini-dir "+root+"/nh") || !strings.Contains(args, "--sandbox=false") || strings.Contains(strings.Join(c.Env, " "), "OPENAI") {
		t.Fatal(c.Args, c.Env)
	}
	if _, _, err := Harnesses["antigravity"].Command(work, "retained", obj{"harness": "antigravity", "account": "ag", "model": "g", "text": "t"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Harnesses["codex"].Command(work, "retained", obj{"account": "cx", "text": "t"}); err == nil {
		t.Fatal("resume under another account binding accepted")
	}
	c, _, err = Harnesses["codex"].Command(mk("w2"), "", obj{"account": "cx", "text": "t"})
	if err != nil || c.Args[len(c.Args)-1] != "t" || !slices.Contains(c.Env, "CODEX_HOME="+root+"/nh2") {
		t.Fatal(err, c)
	}
	app := codexAppServer(c)
	if app.Args[1] != "app-server" || slices.Contains(app.Args, "t") || !slices.Contains(app.Args, "--disable") {
		t.Fatal(app.Args)
	}
}
