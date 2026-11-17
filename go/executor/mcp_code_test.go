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

// call sends one tools/call and returns the decoded text result and isError.
func (c *codeClient) call(tool string, args M) (M, bool) {
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
	v, err := dec(jStr(jArr(r, "content")[0], "text"))
	if err != nil {
		c.t.Fatal(err)
	}
	return v.(M), jAt(r, "isError") == true
}

func (c *codeClient) code(source string) (M, bool) { return c.call("code", M{"source": source}) }

// Risk: values do not persist between calls, so every call starts over and the agent pays for re-reading
// (fleet-code-mode.md, code).
func TestCodeModeStatePersists(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	if r, bad := c.code(`state.n = 41; print("set", state.n); return {ok: true}`); bad || r["output"] != "set 41\n" || jAt(r, "value", "ok") != true {
		t.Fatalf("first call: %v %v", bad, r)
	}
	if r, bad := c.code(`return state.n + 1`); bad || enc(r["value"]) != "42" {
		t.Fatalf("state lost: %v %v", bad, r)
	}
	// A plain declaration lives only inside its call.
	c.code(`const local = 1`)
	if r, bad := c.code(`return typeof local`); bad || r["value"] != "undefined" {
		t.Fatalf("declaration leaked: %v", r)
	}
}

// Risk: a bridged connection call bypasses the grant or the gateway, or parallel calls are serialized or lost
// (fleet-code-mode.md, bridge functions).
func TestCodeModeConnectionBridge(t *testing.T) {
	var calls atomic.Int32
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("x-fleet-call-id", "c")
		switch r.URL.Path {
		case "/v1/call/web/search":
			time.Sleep(300 * time.Millisecond)
			w.Write([]byte(`{"results":[{"title":"a"},{"title":"b"}]}`))
		case "/v1/call/web/broken":
			w.Header().Set("x-fleet-error", "provider-502")
			w.WriteHeader(502)
			w.Write([]byte(`{"why":"down"}`))
		}
	}))
	defer gw.Close()
	t.Setenv("FLEET_GATEWAY_URL", gw.URL)
	t.Setenv("FLEET_RUN_TOKEN", "tok")
	web := M{"name": "web", "tools": []any{M{"name": "search", "description": "find"}, M{"name": "broken"}}}
	c := newCodeClient(t, M{"dir": t.TempDir(), "connections": []any{web}})

	start := time.Now()
	r, bad := c.code(`const xs = await Promise.all([1,2,3,4].map(() => conn.web.search({q: "x"})));
		return xs.flatMap(x => x.results.map(r => r.title)).length`)
	if bad || enc(r["value"]) != "8" {
		t.Fatalf("parallel search: %v %v", bad, r)
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("4 calls of 300 ms took %v: not parallel", d)
	}
	if n := len(jArr(r, "calls")); n != 4 || jStr(jArr(r, "calls")[0], "tool") != "web.search" {
		t.Fatalf("calls summary: %v", r["calls"])
	}

	r, bad = c.code(`try { await conn.web.broken({}) } catch (e) { return [e.name, e.tool, e.outcome, e.code, e.output] }`)
	want := []any{"FleetCallError", "web.broken", "error", "http-502", M{"why": "down"}}
	if bad || enc(r["value"]) != enc(want) {
		t.Fatalf("tool error: %v", r["value"])
	}

	before := calls.Load()
	r, _ = c.code(`const out = []
		for (const f of [() => conn.web.delete({}), () => conn.other.search({}), () => fleet.ask({})])
			try { await f() } catch (e) { out.push(e.code) }
		return out`)
	if enc(r["value"]) != enc([]any{"tool-not-granted", "connection-not-granted", "no-fleet-tools"}) || calls.Load() != before {
		t.Fatalf("ungranted calls: %v, gateway calls %d", r["value"], calls.Load()-before)
	}
	if r, _ := c.code(`return (await conn.web.$tools()).map(t => t.name)`); enc(r["value"]) != `["search","broken"]` {
		t.Fatalf("tools: %v", r["value"])
	}
	// $get and $call reach names that built-ins probe; here they reach ordinary ones the same way.
	if r, bad := c.code(`return (await conn.$get("web").$call("search", {q: 1})).results.length`); bad || enc(r["value"]) != "2" {
		t.Fatalf("$get/$call: %v %v", bad, r)
	}

	// A call made after its job ended is refused, not sent.
	before = calls.Load()
	c.code(`setTimeout(() => conn.web.search({}).catch(e => { state.late = e.code + " " + e.tool }), 50); return 1`)
	if r, _ := c.code(`await new Promise(r => setTimeout(r, 300)); return state.late`); r["value"] != "job-ended web.search" || calls.Load() != before {
		t.Fatalf("late call: %v, gateway calls %d", r["value"], calls.Load()-before)
	}
}

// Risk: a long run hits the harness MCP timeout (Codex: 60 s) and its result is lost (fleet-code-mode.md, poll).
func TestCodeModeDetachAndPoll(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	r, bad := c.call("code", M{"source": `print("started"); await new Promise(r => setTimeout(r, 800)); return "late"`, "detachAfterSeconds": 0.2})
	if bad || r["status"] != "running" || r["output"] != "started\n" {
		t.Fatalf("detach: %v %v", bad, r)
	}
	id := jStr(r, "jobId")
	if r, _ := c.call("poll", M{}); jStr(jArr(r, "jobs")[0], "status") != "running" {
		t.Fatalf("list: %v", r)
	}
	if r, bad := c.call("poll", M{"jobId": id, "waitSeconds": 5}); bad || r["status"] != "done" || r["value"] != "late" || r["output"] != "" {
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

// Risk: a job that never yields wedges code mode for the rest of the execution (fleet-code-mode.md, poll reset).
func TestCodeModeReset(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	c.code(`state.kept = 1`)
	r, _ := c.call("code", M{"source": `while (true) {}`, "detachAfterSeconds": 0.3})
	id := jStr(r, "jobId")
	if r["status"] != "running" {
		t.Fatalf("spin: %v", r)
	}
	if r, bad := c.call("poll", M{"reset": true}); bad || r["reset"] != true {
		t.Fatalf("reset: %v", r)
	}
	if r, bad := c.call("poll", M{"jobId": id, "waitSeconds": 5}); !bad || !strings.HasPrefix(jStr(r, "error", "message"), "runtime-exited") {
		t.Fatalf("spun job: %v", r)
	}
	if r, bad := c.code(`return state.kept ?? "gone"`); bad || r["value"] != "gone" || r["restarted"] != true {
		t.Fatalf("after reset: %v", r)
	}
}

// Risk: printing or returning a bridge object sends real tool calls (fleet-code-mode.md, bridge functions).
func TestCodeModeProbesSendNoCalls(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	r, bad := c.code(`print(fleet); return [JSON.stringify({f: fleet, c: conn.web, n: conn}), String(typeof fleet.then), typeof conn.toString]`)
	if bad || len(jArr(r, "calls")) != 0 || strings.Contains(jStr(r, "output"), "Unhandled") || enc(r["value"]) != `["{\"f\":{},\"c\":{},\"n\":{}}","undefined","undefined"]` {
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
	c := newCodeClient(t, M{"dir": dir, "read": []any{dir}})
	src := `const fs = require("node:fs"); const out = {}
		try { process.kill(` + fmt.Sprint(sleeper.Process.Pid) + `, "SIGTERM"); out.signal = "allowed" } catch (e) { out.signal = e.code === "EPERM" || e.code === "ESRCH" ? "refused" : e.code }
		try { fs.writeFileSync(` + enc(filepath.Join(dir, "x")) + `, "1"); out.write = "allowed" } catch (e) { out.write = e.code }
		try { require("node:child_process").execSync("true"); out.exec = "allowed" } catch (e) { out.exec = e.code }
		out.read = fs.readdirSync(` + enc(dir) + `).length
		try { await fetch(` + enc(srv.URL) + `); out.fetch = "allowed" } catch (e) { out.fetch = "refused" }
		out.raw = await new Promise(done => {
			const r = new (require("_http_client").ClientRequest)(` + enc(srv.URL) + `, () => done("allowed"))
			r.on("error", () => done("refused")); r.end() })
		return out`
	r, bad := c.code(src)
	if bad || enc(r["value"]) != `{"exec":"ERR_ACCESS_DENIED","fetch":"refused","raw":"refused","read":0,"signal":"refused","write":"ERR_ACCESS_DENIED"}` || hits.Load() != 0 {
		t.Fatalf("permissions: %v %v, server hits %d", bad, r, hits.Load())
	}
	if sleeper.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("the other process was signalled")
	}
}

// Risk: one large line stalls the runtime, so every later call stays running (fleet-code-mode.md, code: output is not
// cut).
func TestCodeModeLargeValueAndConcurrentRequests(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	if r, bad := c.code(`return "x".repeat(70e6).length`); bad || enc(r["value"]) != "70000000" {
		t.Fatalf("setup: %v", r)
	}
	r, bad := c.code(`return "x".repeat(70e6)`)
	if bad || len(jStr(r, "value")) != 70e6 {
		t.Fatalf("large value: %v %d", bad, len(jStr(r, "value")))
	}
	// A ping sent while a code call runs is answered first.
	io.WriteString(c.in, enc(M{"jsonrpc": "2.0", "id": 100, "method": "tools/call", "params": M{"name": "code", "arguments": M{"source": `await new Promise(r => setTimeout(r, 1500)); return 1`}}})+"\n")
	io.WriteString(c.in, enc(M{"jsonrpc": "2.0", "id": 101, "method": "ping"})+"\n")
	start := time.Now()
	c.out.Scan()
	first, _ := dec(c.out.Text())
	if enc(jAt(first, "id")) != "101" || time.Since(start) > time.Second {
		t.Fatalf("ping waited for code: %v after %v", jAt(first, "id"), time.Since(start))
	}
	c.out.Scan()
}

// Risk: a dead runtime hangs its job, or a new runtime hides that state was lost (fleet-code-mode.md, unknown outcomes).
func TestCodeModeRuntimeRestart(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	c.code(`state.kept = 1`)
	r, bad := c.code(`process.exit(3)`)
	if !bad || !strings.HasPrefix(jStr(r, "error", "message"), "runtime-exited") {
		t.Fatalf("exit: %v %v", bad, r)
	}
	r, bad = c.code(`return state.kept ?? "gone"`)
	if bad || r["value"] != "gone" || r["restarted"] != true {
		t.Fatalf("restart: %v %v", bad, r)
	}
	if r, bad := c.code(`throw new Error("boom")`); !bad || jStr(r, "error", "message") != "boom" || r["restarted"] != nil {
		t.Fatalf("throw: %v %v", bad, r)
	}
	// A throw in a timer and a write callback keep the runtime and its state.
	c.code(`state.n = 2; setTimeout(() => { throw new Error("late") }, 10)`)
	r, bad = c.code(`await new Promise(r => setTimeout(r, 100)); await new Promise(r => process.stdout.write("w", r))
		process.stdout.write("a"); process.stdout.write(new Uint8Array([98, 10])); return state.n`)
	if bad || enc(r["value"]) != "2" || r["restarted"] != nil || r["output"] != "wab\n" {
		t.Fatalf("after timer throw: %v %v", bad, r)
	}
}

// Risk: an unconfirmed Fleet mutation reaches code as a failure, so the agent repeats it (fleet-code-mode.md, unknown
// outcomes).
func TestCodeModeFleetResults(t *testing.T) {
	pending := mcpText(M{"status": "pending", "requestKey": "k"}, &yes)
	if _, fail := mcpValue(pending); jStr(fail, "outcome") != "unknown" || jStr(fail, "code") != "pending" || jStr(fail, "output", "requestKey") != "k" {
		t.Fatalf("pending: %v", fail)
	}
	if _, fail := mcpValue(mcpText(M{"error": "nope"}, &yes)); jStr(fail, "code") != "tool-error" || jStr(fail, "output", "error") != "nope" {
		t.Fatalf("error: %v", fail)
	}
	if v, fail := mcpValue(M{"content": []any{M{"type": "text", "text": "plain"}}}); fail != nil || v != "plain" {
		t.Fatalf("text: %v", v)
	}
	child := mcpText(M{"status": "pending-or-rejected", "response": nil}, &no)
	if _, fail := mcpValue(child); jStr(fail, "outcome") != "unknown" {
		t.Fatalf("request_child without an answer: %v", fail)
	}

	c := newCodeClient(t, M{"dir": t.TempDir(), "journal": filepath.Join(t.TempDir(), "journal.sqlite"),
		"context": M{"commandId": "cmd", "executionId": "ex", "hostId": "h"}})
	r, bad := c.code(`const names = (await fleet.$tools()).map(t => t.name)
		let code; try { await fleet.task_create({}) } catch (e) { code = e.code }
		return {start: names.includes("session_start"), project: names.includes("task_create"), code}`)
	if bad || enc(r["value"]) != `{"code":"tool-not-granted","project":false,"start":true}` {
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
		err := addCodeServer(servers, mode, harness, profile, fleet, granted, "/run.env", file, []string{"/work", "/input"})
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
	if _, spec, _ := launch("add", "claude", M{"tools": []any{"Edit"}}, "/journal"); enc(spec["read"]) != `[]` {
		t.Fatalf("restricted claude reads: %v", spec["read"])
	}
	if _, spec, _ := launch("add", "claude", M{}, "/journal"); enc(spec["read"]) != `["/work","/input"]` {
		t.Fatalf("default claude: %v", spec["read"])
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
	if _, fail := s.bridge("fleet", "message_send", M{"text": "hi"}); jStr(fail, "outcome") != "unknown" || jStr(fail, "code") != "journal-error" {
		t.Fatalf("closed journal: %v", fail)
	}
}
