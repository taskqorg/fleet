package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// codeClient drives one in-process code server over its stdio, as a harness does.
type codeClient struct {
	t   *testing.T
	in  io.WriteCloser
	out *bufio.Scanner
	id  int
}

func newCodeClient(t *testing.T, spec M) *codeClient {
	t.Helper()
	node, sandbox, err := codeHost()
	if err != nil {
		if os.Getenv("FLEET_REQUIRE_CODE_MODE") != "" { // CI's hosted unit job, where the sandbox must work
			t.Fatal(err)
		}
		t.Skip(err.Error())
	}
	spec["node"], spec["sandbox"] = node, sandbox
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- codeMCP(spec, inR, outW); outW.Close() }()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("code server did not stop")
		}
	})
	sc := bufio.NewScanner(outR)
	sc.Buffer(nil, 256<<20)
	return &codeClient{t: t, in: inW, out: sc}
}

// parts sends one tools/call and returns its text parts and isError.
func (c *codeClient) parts(tool string, args M) ([]string, bool) {
	texts, _, bad := c.response(tool, args)
	return texts, bad
}

func (c *codeClient) response(tool string, args M) ([]string, M, bool) {
	c.t.Helper()
	c.id++
	io.WriteString(c.in, enc(M{"jsonrpc": "2.0", "id": c.id, "method": "tools/call", "params": M{"name": tool, "arguments": args}})+"\n")
	if !c.out.Scan() {
		c.t.Fatalf("no reply: %v", c.out.Err())
	}
	reply, err := dec(c.out.Text())
	if err != nil {
		c.t.Fatal(err)
	}
	r := jAt(reply, "result")
	var texts []string
	for _, p := range jArr(r, "content") {
		texts = append(texts, jStr(p, "text"))
	}
	meta := jObj(r, "structuredContent")
	if meta == nil {
		meta = jObj(r, "_meta", "fleet/code")
	}
	return texts, meta, jAt(r, "isError") == true
}

// call returns the reply as one object (fleet-code-mode.md, reply): the `[code] ` part's fields, `output` from the
// printed part and, for a finished job, `value` from the value part (JSON when it decodes, else the text itself).
func (c *codeClient) call(tool string, args M) (M, bool) {
	c.t.Helper()
	plain, v, bad := c.response(tool, args)
	if len(v) > 0 {
		plain = plain[:len(plain)-1]
	} else {
		v = M{}
	}
	if !has(v, "status") && !has(v, "error") && !has(v, "jobs") && !has(v, "reset") {
		v["status"] = "done"
		if len(plain) == 0 {
			c.t.Fatalf("finished job without a value part: %q", plain)
		}
		last := plain[len(plain)-1]
		if x, err := dec(last); err == nil {
			v["value"] = x
		} else {
			v["value"] = last
		}
		plain = plain[:len(plain)-1]
	}
	if len(plain) > 1 {
		c.t.Fatalf("more than one output part: %q", plain)
	}
	v["output"] = strings.Join(plain, "")
	return v, bad
}

func (c *codeClient) code(source string) (M, bool) { return c.call("code", M{"source": source}) }

// Risk: values do not persist between calls, so every call starts over and the agent pays for re-reading
// (fleet-code-mode.md, code).
func TestCodeModeStatePersists(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	c.id++
	io.WriteString(c.in, enc(M{"jsonrpc": "2.0", "id": c.id, "method": "tools/list"})+"\n")
	c.out.Scan()
	if list, _ := dec(c.out.Text()); !strings.Contains(jStr(jArr(list, "result", "tools")[0], "description"), "No file reads") {
		t.Fatalf("a runtime without read roots promises reads: %v", list)
	}
	// The first call starts the runtime and says so: `state` is empty, whatever an earlier execution stored.
	if r, bad := c.code(`state.n = 41; print("set", state.n); require("node:fs").writeSync(1, "raw"); return {ok: true}`); bad || r["output"] != "set 41\n" || jAt(r, "value", "ok") != true || r["newRuntime"] != true {
		t.Fatalf("first call: %v %v", bad, r)
	}
	if r, bad := c.code(`return state.n + 1`); bad || enc(r["value"]) != "42" || r["newRuntime"] != nil {
		t.Fatalf("state lost: %v %v", bad, r)
	}
	// The runtime's own bindings are out of reach: code cannot reuse call ids or forge another job's reply.
	if r, bad := c.code(`return [typeof nextId, typeof send, typeof pending, typeof job]`); bad || enc(r["value"]) != `["undefined","undefined","undefined","undefined"]` {
		t.Fatalf("state lost: %v %v", bad, r)
	}
	// A plain declaration lives only inside its call.
	c.code(`const local = 1`)
	if r, bad := c.code(`return typeof local`); bad || r["value"] != "undefined" {
		t.Fatalf("declaration leaked: %v", r)
	}
}

// Risk: a long run hits the harness MCP timeout (Codex: 60 s) and its result is lost (fleet-code-mode.md, poll).
func TestCodeModeDetachAndPoll(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	r, bad := c.call("code", M{"source": `
		print("started")
		while (!state.release) await new Promise(r => setTimeout(r, 10))
		return "late"
	`, "detachAfterSeconds": 0})
	if bad || r["status"] != "running" {
		t.Fatalf("detach: %v %v", bad, r)
	}
	id := jStr(r, "jobId")
	output := jStr(r, "output")
	if r, _ := c.call("poll", M{}); jStr(jArr(r, "jobs")[0], "status") != "running" {
		t.Fatalf("list: %v", r)
	}
	if r, bad := c.code(`state.release = true; return true`); bad || r["value"] != true {
		t.Fatalf("release: %v %v", bad, r)
	}
	if r, bad := c.call("poll", M{"jobId": id, "waitSeconds": 5}); bad || r["status"] != "done" || r["value"] != "late" || output+jStr(r, "output") != "started\n" {
		t.Fatalf("poll: %v %v", bad, r)
	}
	// A final result is returned once; the server keeps nothing of it.
	if r, bad := c.call("poll", M{"jobId": id}); !bad || !strings.HasPrefix(jStr(r, "error"), "unknown jobId") {
		t.Fatalf("second poll: %v", r)
	}
	if r, _ := c.call("poll", M{}); len(jArr(r, "jobs")) != 0 {
		t.Fatalf("delivered job listed: %v", r)
	}
}

// Risk: a reply wait past Codex's 60 s MCP timeout loses the reply and holds every later call behind the serial MCP
// filter (fleet-code-mode.md, code).
func TestCodeModeReplyWait(t *testing.T) {
	for in, want := range map[any]time.Duration{nil: 25 * time.Second, float64(-1): 25 * time.Second, float64(0): 0, float64(3): 3 * time.Second, float64(1e300): 50 * time.Second} {
		if got := seconds(in); got != want {
			t.Errorf("seconds(%v) = %v, want %v", in, got, want)
		}
	}
}

// Risk: printing or returning a bridge object sends real tool calls (fleet-code-mode.md, bridge functions).
func TestCodeModeProbesSendNoCalls(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	r, bad := c.code(`print(fleet, conn); return [JSON.stringify({f: fleet, n: conn}), typeof fleet.then, typeof fleet.sesion_start, String(conn)]`)
	if bad || len(jArr(r, "calls")) != 0 || strings.Contains(jStr(r, "output"), "Unhandled") || enc(r["value"]) != `["{\"f\":{},\"n\":{}}","undefined","undefined","[object Object]"]` {
		t.Fatalf("probes: %v %v", bad, r)
	}
}

// Risk: code writes, runs commands or opens the network outside the harness sandbox, so code mode adds authority
// (fleet-code-mode.md, authority).
func TestCodeModePermissions(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "in.txt"), []byte("in"), 0o600)
	outside := filepath.Join(t.TempDir(), "run.env") // like the run file: outside every read root
	os.WriteFile(outside, []byte("FLEET_RUN_TOKEN=invented"), 0o600)
	c := newCodeClient(t, M{"dir": dir, "read": []any{dir}})
	src := `const fs = require("node:fs"); const out = {}
		try { process.kill(` + fmt.Sprint(sleeper.Process.Pid) + `, "SIGTERM"); out.signal = "allowed" } catch (e) { out.signal = e.code === "EPERM" || e.code === "ESRCH" ? "refused" : e.code }
		try { fs.writeFileSync(` + enc(filepath.Join(dir, "x")) + `, "1"); out.write = "allowed" } catch (e) { out.write = e.code }
		try { require("node:child_process").execSync("true"); out.exec = "allowed" } catch (e) { out.exec = e.code }
		out.read = fs.readdirSync(` + enc(dir) + `).length
		out.inside = fs.readFileSync(` + enc(filepath.Join(dir, "in.txt")) + `, "utf8")
		try { fs.readFileSync(` + enc(outside) + `); out.outside = "allowed" } catch (e) { out.outside = e.code }
		try { await fetch(` + enc(srv.URL) + `); out.fetch = "allowed" } catch (e) { out.fetch = "refused" }
		out.raw = await new Promise(done => {
			const r = new (require("_http_client").ClientRequest)(` + enc(srv.URL) + `, () => done("allowed"))
			r.on("error", () => done("refused")); r.end() })
		return out`
	r, bad := c.code(src)
	if bad || enc(r["value"]) != `{"exec":"ERR_ACCESS_DENIED","fetch":"refused","inside":"in","outside":"ERR_ACCESS_DENIED","raw":"refused","read":1,"signal":"refused","write":"ERR_ACCESS_DENIED"}` || hits.Load() != 0 {
		t.Fatalf("permissions: %v %v, server hits %d", bad, r, hits.Load())
	}
	if sleeper.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("the other process was signalled")
	}
}

// Risk: a dead runtime hangs its job, or a new runtime hides that state was lost (fleet-code-mode.md, unknown outcomes).
func TestCodeModeRuntimeRestart(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	c.code(`state.kept = 1`)
	r, bad := c.code(`require("node:fs").writeSync(2, "the reason"); process.exit(3)`)
	if !bad || !strings.HasPrefix(jStr(r, "error", "message"), "runtime-exited") || jStr(r, "error", "stderrTail") != "the reason" {
		t.Fatalf("exit: %v %v", bad, r)
	}
	r, bad = c.code(`return state.kept ?? "gone"`)
	if bad || r["value"] != "gone" || r["newRuntime"] != true {
		t.Fatalf("restart: %v %v", bad, r)
	}
	if r, bad := c.code(`throw new Error("boom")`); !bad || jStr(r, "error", "message") != "boom" || r["newRuntime"] != nil {
		t.Fatalf("throw: %v %v", bad, r)
	}
	if r, bad := c.code(`throw Object.create(null)`); !bad || jStr(r, "error", "message") != "unprintable thrown value" {
		t.Fatalf("unprintable throw: %v %v", bad, r)
	}
	// A throw in a timer and a write callback keep the runtime and its state.
	c.code(`state.n = 2; setTimeout(() => { throw new Error("late") }, 10)`)
	r, bad = c.code(`await new Promise(r => setTimeout(r, 100)); await new Promise(r => process.stdout.write("w", r))
		process.stdout.write("a"); process.stdout.write(new Uint8Array([98, 10])); process.stdout.write("aGk=", "base64"); return state.n`)
	if bad || enc(r["value"]) != "2" || r["newRuntime"] != nil || r["output"] != "wab\nhi" {
		t.Fatalf("after timer throw: %v %v", bad, r)
	}
}

// Risk: an unconfirmed Fleet mutation reaches code as a failure, so the agent repeats it (fleet-code-mode.md, unknown
// outcomes).
func TestCodeModeFleetResults(t *testing.T) {
	pending := mcpText(M{"status": "pending", "requestKey": "k"}, &yes)
	if _, fail := mcpValue(pending); fail == nil || fail.Outcome != "unknown" || fail.Code != "pending" || jStr(fail.Output, "requestKey") != "k" {
		t.Fatalf("pending: %+v", fail)
	}
	if _, fail := mcpValue(mcpText(M{"error": "nope"}, &yes)); fail == nil || fail.Code != "tool-error" || jStr(fail.Output, "error") != "nope" {
		t.Fatalf("error: %+v", fail)
	}
	if v, fail := mcpValue(M{"content": []any{M{"type": "text", "text": "plain"}}}); fail != nil || v != "plain" {
		t.Fatalf("text: %v", v)
	}
	child := mcpText(M{"status": "pending-or-rejected", "response": nil}, &no)
	if _, fail := mcpValue(child); fail == nil || fail.Outcome != "unknown" {
		t.Fatalf("request_child without an answer: %+v", fail)
	}
	refused := mcpText(M{"status": "pending-or-rejected", "response": M{"error": "no such profile"}}, &yes)
	if _, fail := mcpValue(refused); fail == nil || fail.Outcome != "error" {
		t.Fatalf("request_child refused: %+v", fail)
	}

	c := newCodeClient(t, M{"dir": t.TempDir(), "journal": filepath.Join(t.TempDir(), "journal.sqlite"),
		"context": M{"commandId": "cmd", "executionId": "ex", "hostId": "h"}})
	r, bad := c.code(`const names = (await fleet.$tools()).map(t => t.name)
		return {start: names.includes("session_start"), project: names.includes("task_create"), fn: typeof fleet.session_start, none: typeof fleet.task_create}`)
	if bad || enc(r["value"]) != `{"fn":"function","none":"undefined","project":false,"start":true}` {
		t.Fatalf("fleet tools: %v %v", bad, r)
	}
}

// Risk: `only` leaves the direct servers, a read-restricted Claude profile reads files through code, or code mode starts
// with nothing to bridge (fleet-code-mode.md, profile setting).
func TestCodeModeLaunchServers(t *testing.T) {
	old := codeHost
	defer func() { codeHost = old }()
	codeHost = func() (string, string, error) { return "/node", "/sandbox", nil }
	granted := []any{M{"name": "web", "tools": []any{M{"name": "search"}}}}
	launch := func(mode, harness string, profile M, journal string) (M, M, error) {
		servers := M{"fleet": M{}, "web": M{}, "serena": M{}}
		var fleet *codeFleet
		if journal != "" {
			fleet = &codeFleet{journal, M{"commandId": "c"}}
		}
		file := filepath.Join(t.TempDir(), "runs", "x.code.json")
		err := addCodeServer(servers, mode, fleet, granted, "/run.env", file, "/work", codeAuthorityFor(harness, profile, "/work", []string{"/work", "/input"}, []any{"/git"}, true, profile["readOnly"] == true, "/exec"))
		if err != nil {
			return servers, nil, err
		}
		if jStr(jArr(servers, "code", "args")[2]) != file {
			t.Fatalf("args: %v", jAt(servers, "code", "args"))
		}
		b, _ := os.ReadFile(file)
		spec, _ := dec(string(b))
		return servers, spec.(M), nil
	}
	servers, spec, err := launch("add", "codex", M{}, "/journal")
	if err != nil || !has(servers, "fleet") || !has(servers, "web") || enc(spec["read"]) != `["/work","/input"]` || jStr(spec, "journal") != "/journal" || enc(jAt(jArr(spec, "connections")[0], "tools")) != `[{"name":"search"}]` {
		t.Fatalf("add: %v %v %v", err, sortedKeys(servers), spec)
	}
	servers, _, _ = launch("only", "codex", M{}, "/journal")
	if has(servers, "fleet") || has(servers, "web") || !has(servers, "serena") || !has(servers, "code") {
		t.Fatalf("only: %v", sortedKeys(servers))
	}
	// Codex: sh sandboxed with its writable roots and gitNetwork's network; write and edit.
	if _, spec, _ := launch("only", "codex", M{}, "/journal"); enc(spec["shell"]) != `{"hide":["/exec"],"network":true,"read":["/work","/input"],"sandbox":true,"write":["/work","/git"]}` || spec["write"] != true || spec["edit"] != true {
		t.Fatalf("codex authority: %v", spec)
	}
	// Claude: each host function only with its native tool; no Bash, no sh.
	if _, spec, _ := launch("add", "claude", M{"tools": []any{"Edit"}}, "/journal"); enc(spec["read"]) != `[]` || has(spec, "shell") || spec["write"] != false || spec["edit"] != true {
		t.Fatalf("restricted claude: %v", spec)
	}
	if _, spec, _ := launch("add", "claude", M{}, "/journal"); enc(spec["read"]) != `["/work","/input"]` || enc(spec["shell"]) != `{"sandbox":false}` || spec["write"] != true {
		t.Fatalf("default claude: %v", spec)
	}
	// ACP refuses client file and terminal operations, including Antigravity with its own sandbox disabled.
	for _, harness := range []string{"acp", "antigravity"} {
		_, spec, err := launch("only", harness, M{"settings": M{"sandbox": false}}, "/journal")
		if err != nil || has(spec, "shell") || spec["write"] != false || spec["edit"] != false || len(jArr(spec, "read")) != 2 {
			t.Fatalf("%s authority: %v %v", harness, spec, err)
		}
	}
	// A read-only start: Codex's sh runs read-only, Claude keeps reads only, ACP agents get nothing that writes.
	if _, spec, _ := launch("add", "codex", M{"readOnly": true}, "/journal"); enc(spec["shell"]) != `{"hide":["/exec"],"network":false,"read":["/work","/input"],"readOnly":true,"sandbox":true,"write":["/work"]}` || spec["write"] != false || spec["edit"] != false {
		t.Fatalf("codex read-only: %v", spec)
	}
	if _, spec, _ := launch("add", "claude", M{"readOnly": true}, "/journal"); has(spec, "shell") || spec["write"] != false || spec["edit"] != false || len(jArr(spec, "read")) != 2 {
		t.Fatalf("claude read-only: %v", spec)
	}
	if _, spec, _ := launch("add", "acp", M{"readOnly": true}, "/journal"); has(spec, "shell") || spec["write"] != false {
		t.Fatalf("acp read-only: %v", spec)
	}
	if d := codeDescription(codeSpec{Shell: &codeShell{Sandbox: true, ReadOnly: true}}); !strings.Contains(d, "Commands write nothing and have no network.") {
		t.Fatalf("read-only description: %s", d)
	}
	if d := codeDescription(codeSpec{Connections: []jsonValue{{}}}); !strings.Contains(d, "NEVER return raw") || !strings.Contains(d, "target < 2 KB") || !strings.Contains(d, "state.hits = await conn[") {
		t.Fatalf("compact-return description: %s", d)
	}
	// only hides the Claude tools that host functions cover; add keeps them.
	for mode, want := range map[string]string{"only": "Read,Task,TaskStop", "add": "Read,Edit,Write,Bash,Glob,Grep,Task,TaskStop"} {
		c := exec.Command("not-executed")
		claudeMCP(c, M{"codeMode": mode}, M{}, nil)
		if got := c.Args[len(c.Args)-1]; got != want {
			t.Fatalf("%s: allowed tools %s", mode, got)
		}
	}
	granted = nil
	if _, _, err := launch("add", "codex", M{}, ""); err == nil || err.Error() != "codeMode needs Fleet tools or a connection" {
		t.Fatalf("empty: %v", err)
	}
	codeHost = func() (string, string, error) { return "", "", errors.New("code-mode-unready: node is not installed") }
	if _, _, err := launch("add", "codex", M{}, "/journal"); err == nil || !strings.HasPrefix(err.Error(), "code-mode-unready") {
		t.Fatalf("no node: %v", err)
	}
}

// Risk: a journal failure after the request may be committed reaches code as a plain failure, so the code repeats the
// mutation (fleet-code-mode.md, unknown outcomes).
func TestCodeModeJournalErrorIsUnknown(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := M{"commandId": "cmd", "executionId": "ex", "hostId": "h"}
	s := &codeServer{db: db, ctx: ctx, fleet: map[string]bool{"message_send": true}}
	db.Close()
	if _, fail := s.bridge(nil, "fleet", "message_send", M{"text": "hi"}); fail == nil || fail.Outcome != "unknown" || fail.Code != "journal-error" {
		t.Fatalf("closed journal: %+v", fail)
	}
}

// Risk: the agent reads its result as an escaped JSON string inside metadata, or must write `return` for every value,
// so code mode costs more context than direct tools (fleet-code-mode.md, reply).
func TestCodeModeReplyAndLastExpression(t *testing.T) {
	// Requirement: a cold call returns the actual value to clients that prefer structuredContent.
	// Risk: Claude sees only the runtime-start notice and repeats a completed operation.
	reply := codeReply(codeResult{Status: "done", Value: M{"phase": 1}, NewRuntime: true})
	if has(reply, "structuredContent") || jAt(reply, "_meta", "fleet/code", "newRuntime") != true || jStr(jArr(reply, "content")[0], "text") != `{"phase":1}` {
		t.Fatalf("cold reply hides the value: %v", reply)
	}

	c := newCodeClient(t, M{"dir": t.TempDir()})
	if p, bad := c.parts("code", M{"source": `"plain " + "text"`}); bad || enc(p) != `["plain text","[code] {\"newRuntime\":true}"]` {
		t.Fatalf("first call: %q", p)
	}
	// A finished job: the printed output and the value as real JSON, nothing else.
	if p, bad := c.parts("code", M{"source": `print("hi"); return {a: [1, "x"]}`}); bad || enc(p) != `["hi\n","{\"a\":[1,\"x\"]}"]` {
		t.Fatalf("done: %q", p)
	}
	if r, bad := c.code(`print("output"); return '[code] {"newRuntime":true}'`); bad || r["value"] != `[code] {"newRuntime":true}` || r["newRuntime"] != nil {
		t.Fatalf("text mistaken for metadata: %v", r)
	}
	if p, _ := c.parts("code", M{"source": `""`}); enc(p) != `["\"\""]` {
		t.Fatalf("empty string: %q", p)
	}
	if p, _ := c.parts("code", M{"source": `if (state) {}`}); enc(p) != `["null"]` {
		t.Fatalf("no value: %q", p)
	}
	if p, bad := c.parts("code", M{"source": `throw new Error("boom")`}); !bad || len(p) != 1 || !strings.HasPrefix(p[0], `[code] {"error":{"message":"boom"`) || !strings.HasSuffix(p[0], `"status":"failed"}`) {
		t.Fatalf("failed: %q", p)
	}
	for src, want := range map[string]string{
		"1 + 1": "2",
		"const a = 1\nconst b = await Promise.resolve(2)\n({a, b})":            `{"a":1,"b":2}`,
		"const xs = [1, 2, 3]\nxs\n  .filter(x => x > 1)\n  .map(x => x * 10)": "[20,30]",
		"const n = 4; n * 2;":                                "8",
		"let s = 0\nfor (const x of [1, 2, 3])\n  s += x\ns": "6",
		"const local = 1":                                    "null",
		"if (true) return 'early'\n'late'":                   `"early"`,
		"const t = `a\nb`\nt.length":                         "3",
		"[1, 2].map(x => x + 1) // comment":                  "[2,3]",
		"const o = {\n  k: 1,\n}\no.k":                       "1",
		"const f = (x) =>\n  x + 1\nf(1)":                    "2",
		"const r = await Promise.all([\n  Promise.resolve(1),\n  Promise.resolve(2),\n])\nr": "[1,2]",
		"const ok = false\n!ok":                                             "true",
		"const x = \"abc\"\nx.length; // size":                              "3",
		"state.big = \"x\".repeat(10)":                                      "null",
		"// save for the next call\nstate.big = \"x\".repeat(10)":           "null",
		"return\n({answer: 42})":                                            "null",
		"const u = \"http://x\"\nu + \"\"":                                  `"http://x"`,
		"const r = helper(2)\nasync function helper(x) { return x * 2 }\nr": "4",
		"print(1)\nfunction f() {}":                                         "null",
		"const r = f(2);\n// helper\nfunction f(x) { return x * 2 }":        "null",
		"const r = f(2);\n/** doc */ function f(x) { return x * 2 }\nr":     "4",
		"1 + 1\n// todo; cleanup":                                           "2",
		"const q = 2\nq // a;b":                                             "2",
		"const a = 1\na\n!== 2":                                             "true",
	} {
		if r, bad := c.code(src); bad || enc(r["value"]) != want {
			t.Errorf("%q: %v %v, want %s", src, bad, r, want)
		}
	}
	if r, bad := c.code(`print("[code] printed"); return "[code] value"`); bad || r["value"] != "[code] value" || r["output"] != "[code] printed\n" {
		t.Fatalf("metadata-like text: %v %v", bad, r)
	}
}

// Risk: write or edit reaches outside the worktree (a path, a symlink, .git), or a profile without the native tool gets
// it in code (fleet-code-mode.md, host functions).
func TestCodeModeFiles(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	os.Mkdir(filepath.Join(dir, ".git"), 0o755)
	os.Symlink(outside, filepath.Join(dir, "out"))
	os.Symlink(filepath.Join(dir, ".git"), filepath.Join(dir, "g"))
	c := newCodeClient(t, M{"dir": dir, "read": []any{dir}, "write": true, "edit": true})
	r, bad := c.code(`const w = await write("a/b.txt", "one two two")
		const once = await edit("a/b.txt", "one", "1")
		const fail = async (f) => { try { await f(); return "allowed" } catch (e) { return e.code } }
		return {w, once, many: await fail(() => edit("a/b.txt", "two", "2")), all: await edit("a/b.txt", "two", "2", {all: true}),
			missing: await fail(() => edit("a/b.txt", "zzz", "")), read: await read("a/b.txt"), glob: await glob("**/*.txt"),
			grep: await grep("2", {glob: "**/*.txt"}), types: [typeof sh, typeof write],
			up: await fail(() => write("../x.txt", "")), abs: await fail(() => write(` + enc(filepath.Join(outside, "y.txt")) + `, "")),
			link: await fail(() => write("out/z.txt", "")), git: await fail(() => write(".git/config", "")), viaLink: await fail(() => write("g/config", "")),
			upper: await fail(() => write(".GIT/config", ""))}`)
	want := `{"abs":"outside-worktree","all":{"path":"a/b.txt","replaced":2},"git":"outside-worktree","glob":["a/b.txt"],"grep":[{"file":"a/b.txt","line":1,"text":"1 2 2"}],` +
		`"link":"outside-worktree","many":"not-unique","missing":"not-found","once":{"path":"a/b.txt","replaced":1},"read":"1 2 2","types":["undefined","function"],` +
		`"up":"outside-worktree","upper":"outside-worktree","viaLink":"outside-worktree","w":{"bytes":11,"path":"a/b.txt"}}`
	if bad || enc(r["value"]) != want {
		t.Fatalf("files: %v %v\n got %s\nwant %s", bad, r, enc(r["value"]), want)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a", "b.txt")); string(b) != "1 2 2" {
		t.Fatalf("file: %q", b)
	}
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("written outside: %v", entries)
	}
	if entries, _ := os.ReadDir(filepath.Join(dir, ".git")); len(entries) != 0 {
		t.Fatalf("written under .git: %v", entries)
	}
	// Parallel edits of one file both land; symlinks inside the worktree are followed, as by the native Write tool, even
	// upward; a missing text is refused; grep's ignoreCase and a file path with glob.
	os.Symlink(filepath.Join(dir, "a", "b.txt"), filepath.Join(dir, "lnk"))
	os.Mkdir(filepath.Join(dir, "pkg"), 0o755)
	os.Symlink("../a", filepath.Join(dir, "pkg", "shared"))
	r, bad = c.code(`await write("c.txt", "a b")
		const fail = async (f) => { try { await f(); return "allowed" } catch (e) { return e.code } }
		await Promise.all([edit("c.txt", "a", "A"), edit("c.txt", "b", "B")])
		;[await read("c.txt"), await fail(() => write("lnk", "x")), await fail(() => write("pkg/shared/n.txt", "n")), await fail(() => write("c.txt")),
			(await grep(/a B/, {ignoreCase: true})).length, (await glob("*.txt", {path: "c.txt"})).length]`)
	if bad || enc(r["value"]) != `["A B","allowed","allowed","bad-arguments",1,1]` {
		t.Fatalf("parallel edits, symlinks, grep: %v %v", bad, r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a", "n.txt")); string(b) != "n" {
		t.Fatalf("upward symlink: %q", b)
	}
	// A hard link into .git keeps its contents (the write replaces the name), a nested .git is refused, the mode stays,
	// and a slash-less grep glob matches at any depth.
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\n"), 0o644)
	os.Link(filepath.Join(dir, ".git", "config"), filepath.Join(dir, "hard"))
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("x"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".github"), 0o755)
	os.WriteFile(filepath.Join(dir, ".github", "ci.yml"), []byte("on: push"), 0o644)
	r, bad = c.code(`const fail = async (f) => { try { await f(); return "allowed" } catch (e) { return e.code } }
		;[await fail(() => write("hard", "evil")), await fail(() => write("sub/.git/hooks/x", "")), await fail(() => edit("run.sh", "x", "y")),
			(await grep("n", {glob: "*.txt"})).map(h => h.file).sort(), await glob("**/*.yml"), await glob("./**/*.yml"), await glob("[!r]*.txt")]`)
	if bad || enc(r["value"]) != `["allowed","outside-worktree","allowed",["a/n.txt"],[".github/ci.yml"],[".github/ci.yml"],["c.txt"]]` {
		t.Fatalf("hard link, nested .git, grep glob: %v %v", bad, r)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".git", "config")); string(b) != "[core]\n" {
		t.Fatalf("written through a hard link into .git: %q", b)
	}
	if st, _ := os.Stat(filepath.Join(dir, "run.sh")); st.Mode().Perm() != 0o755 {
		t.Fatalf("mode: %v", st.Mode())
	}
	if runtime.GOOS == "darwin" { // a case-insensitive file system: .GIT through a symlink is the same directory
		os.Symlink(filepath.Join(dir, ".GIT"), filepath.Join(dir, "g2"))
		if _, fail := codeFile(dir, "write", M{"path": "g2/config", "text": ""}); fail == nil || fail.Code != "outside-worktree" {
			t.Fatalf(".GIT through a symlink: %+v", fail)
		}
	}
	// Without the grant the functions do not exist, and the server refuses the call too.
	c = newCodeClient(t, M{"dir": dir})
	if r, _ := c.code(`[typeof read, typeof write, typeof edit, typeof sh]`); enc(r["value"]) != `["undefined","undefined","undefined","undefined"]` {
		t.Fatalf("ungranted: %v", r["value"])
	}
	srv := &codeServer{spec: codeSpec{Dir: dir}}
	for _, tool := range []string{"write", "edit", "sh"} {
		if _, fail := srv.bridge(nil, "host", tool, M{"path": "x", "command": "true"}); fail == nil || fail.Code != "tool-not-granted" {
			t.Errorf("%s: %+v", tool, fail)
		}
	}
}

// Risk: aborting the MCP code server leaves its shell group alive after the execution ends.
func TestCodeModeShellLease(t *testing.T) {
	lease, release, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	defer release.Close()
	cmd := exec.Command(exe(), "code-shell", "none", "/bin/sh", "-c", "printf 'ready\\n'; exec sleep 30")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.ExtraFiles = []*os.File{lease}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer cmd.Process.Kill()
	if line, err := bufio.NewReader(out).ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("shell did not start: %q %v", line, err)
	}
	if err := release.Close(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		var exit *exec.ExitError
		if !errors.As(err, &exit) || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
			t.Fatalf("expected shell group kill: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shell outlived its lifetime pipe")
	}
}

// Risk: codeMode:only agents waste a first-turn $tools() discovery round-trip when tool signatures are absent from the
// code tool description, or bloat prompt tokens when large schemas are not capped (FLT-5).
func TestCodeDescriptionToolSignatures(t *testing.T) {
	conn := M{
		"name": "tracker",
		"tools": []any{
			M{
				"name": "search",
				"inputSchema": M{
					"type": "object",
					"properties": M{
						"query":  M{"type": "string"},
						"org":    M{"type": "string"},
						"limit":  M{"type": "integer"},
						"cursor": M{"type": "string"},
					},
					"required": []any{"query", "org"},
				},
			},
			M{
				"name": "ping",
				"inputSchema": M{
					"type":       "object",
					"properties": M{},
				},
			},
			M{
				"name": "many",
				"inputSchema": M{
					"type": "object",
					"properties": M{
						"id": M{"type": "string"},
						"a":  M{"type": "string"},
						"b":  M{"type": "string"},
						"c":  M{"type": "string"},
						"d":  M{"type": "string"},
						"e":  M{"type": "string"},
						"f":  M{"type": "string"},
						"g":  M{"type": "string"},
					},
					"required": []any{"id"},
				},
			},
		},
	}
	d := codeDescription(codeSpec{
		Connections: []jsonValue{{V: conn}},
		Journal:     "/journal",
	})
	wantConn := `- conn["tracker"]: search({org, query, cursor?, limit?}), ping({}), many({id, a?, b?, c?, d?, e?, ...})`
	if !strings.Contains(d, wantConn) {
		t.Fatalf("missing compact connection signatures:\nwant: %s\ngot:\n%s", wantConn, d)
	}
	if !strings.Contains(d, "\n- fleet: ") {
		t.Fatalf("missing fleet tool signatures:\n%s", d)
	}
	if !strings.Contains(d, "Call the listed tools directly on your first code turn") {
		t.Fatalf("missing direct-call guidance:\n%s", d)
	}
	noFleet := codeDescription(codeSpec{Connections: []jsonValue{{V: conn}}})
	if strings.Contains(noFleet, "\n- fleet: ") {
		t.Fatalf("unexpected fleet line without journal:\n%s", noFleet)
	}
}
