package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
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
	return texts, jObj(r, "structuredContent"), jAt(r, "isError") == true
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
			time.Sleep(500 * time.Millisecond)
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
	web := M{"name": "web", "instructions": "Use short queries.", "tools": []any{M{"name": "search", "description": "find"}, M{"name": "broken"}}}
	c := newCodeClient(t, M{"dir": t.TempDir(), "connections": []any{web}, "instructions": []any{web}})
	c.id++
	io.WriteString(c.in, enc(M{"jsonrpc": "2.0", "id": c.id, "method": "initialize", "params": M{}})+"\n")
	c.out.Scan()
	if init, _ := dec(c.out.Text()); jStr(init, "result", "instructions") != `conn["web"]: Use short queries.` {
		t.Fatalf("instructions: %v", init)
	}

	start := time.Now()
	r, bad := c.code(`const xs = await Promise.all([1,2,3,4].map(() => conn.web.search({q: "x"})));
		return xs.flatMap(x => x.results.map(r => r.title)).length`)
	if bad || enc(r["value"]) != "8" {
		t.Fatalf("parallel search: %v %v", bad, r)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("4 calls of 500 ms took %v: not parallel (in a row: 2 s)", d)
	}
	if has(r, "calls") || calls.Load() != 4 { // every call succeeded: no calls list in the reply
		t.Fatalf("calls summary: %v, gateway calls %d", r["calls"], calls.Load())
	}

	r, bad = c.code(`try { await conn.web.broken({}) } catch (e) { return [e.name, e.tool, e.outcome, e.code, e.output] }`)
	want := []any{"FleetCallError", "web.broken", "error", "http-502", M{"why": "down"}}
	if bad || enc(r["value"]) != enc(want) {
		t.Fatalf("tool error: %v", r["value"])
	}

	// Only granted names exist in the runtime; the server refuses the rest too.
	before := calls.Load()
	r, _ = c.code(`return [typeof conn.web.delete, typeof conn.other, typeof fleet.ask, Object.keys(conn)]`)
	if enc(r["value"]) != `["undefined","undefined","undefined",["web"]]` || calls.Load() != before {
		t.Fatalf("ungranted names: %v, gateway calls %d", r["value"], calls.Load()-before)
	}
	srv := &codeServer{conns: map[string]any{"web": web}, fleet: map[string]bool{}}
	for _, x := range [][3]string{{"conn:web", "delete", "tool-not-granted"}, {"conn:other", "search", "connection-not-granted"}, {"fleet", "ask", "no-fleet-tools"}} {
		if _, fail := srv.bridge(nil, x[0], x[1], M{}); jStr(fail, "code") != x[2] {
			t.Errorf("bridge %s.%s: %v", x[0], x[1], fail)
		}
	}
	if r, _ := c.code(`return (await conn.web.$tools()).map(t => t.name)`); enc(r["value"]) != `["search","broken"]` {
		t.Fatalf("tools: %v", r["value"])
	}

	// An unawaited failed call is reported in its job's output.
	if r, _ := c.code(`conn.web.broken({}); await new Promise(r => setTimeout(r, 100)); return 1`); !strings.Contains(jStr(r, "output"), "Unhandled rejection") {
		t.Fatalf("unhandled rejection not in the job's output: %v", r)
	}
	// A call chained off an unawaited one is part of the job too, while the first is in flight.
	// A failed call keeps the calls list, so the chain's second call shows.
	if r, _ := c.code(`conn.web.search({}).then(() => conn.web.broken({}).catch(() => 0)); return 1`); len(jArr(r, "calls")) != 2 || jStr(jArr(r, "calls")[1], "tool") != "web.broken" {
		t.Fatalf("chained call: %v", r)
	}
	// A call started just before the return is part of the job: the final reply carries its outcome.
	if r, _ := c.code(`conn.web.broken({}).catch(() => 0); return 1`); len(jArr(r, "calls")) != 1 || jStr(jArr(r, "calls")[0], "outcome") != "error" {
		t.Fatalf("unawaited call before return: %v", r)
	}
	// Arguments that do not serialize reject as FleetCallError, not a raw TypeError.
	if r, _ := c.code(`try { await conn.web.search({n: 1n}) } catch (e) { return [e.name, e.code, e.tool] }`); enc(r["value"]) != `["FleetCallError","bad-arguments","web.search"]` {
		t.Fatalf("bigint: %v", r["value"])
	}
	// The runtime dies while a call runs: the job ends after the call, with its outcome listed, not as a bare failure.
	r, bad = c.code(`conn.web.search({}); await new Promise(r => setTimeout(r, 50)); process.exit(1)`)
	if !bad || !strings.HasPrefix(jStr(r, "error", "message"), "runtime-exited") || len(jArr(r, "calls")) != 1 || jStr(jArr(r, "calls")[0], "outcome") != "ok" {
		t.Fatalf("exit with a call in flight: %v %v", bad, r)
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
	if r, bad := c.code(`return state.kept ?? "gone"`); bad || r["value"] != "gone" || r["newRuntime"] != true {
		t.Fatalf("after reset: %v", r)
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
	refused := mcpText(M{"status": "pending-or-rejected", "response": M{"error": "no such profile"}}, &yes)
	if _, fail := mcpValue(refused); jStr(fail, "outcome") != "error" {
		t.Fatalf("request_child refused: %v", fail)
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
	if d := codeDescription(M{"shell": M{"sandbox": true, "readOnly": true}}); !strings.Contains(d, "Commands write nothing and have no network.") {
		t.Fatalf("read-only description: %s", d)
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
	if _, fail := s.bridge(nil, "fleet", "message_send", M{"text": "hi"}); jStr(fail, "outcome") != "unknown" || jStr(fail, "code") != "journal-error" {
		t.Fatalf("closed journal: %v", fail)
	}
}

// Risk: the agent reads its result as an escaped JSON string inside metadata, or must write `return` for every value,
// so code mode costs more context than direct tools (fleet-code-mode.md, reply).
func TestCodeModeReplyAndLastExpression(t *testing.T) {
	c := newCodeClient(t, M{"dir": t.TempDir()})
	if p, bad := c.parts("code", M{"source": `"plain " + "text"`}); bad || enc(p) != `["plain text","[code] {\"newRuntime\":true}"]` {
		t.Fatalf("first call: %q", p)
	}
	// A finished job: the printed output and the value as real JSON, nothing else.
	if p, bad := c.parts("code", M{"source": `print("hi"); return {a: [1, "x"]}`}); bad || enc(p) != `["hi\n","{\"a\":[1,\"x\"]}"]` {
		t.Fatalf("done: %q", p)
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
		if _, fail := codeFile(dir, "write", M{"path": "g2/config", "text": ""}); jStr(fail, "code") != "outside-worktree" {
			t.Fatalf(".GIT through a symlink: %v", fail)
		}
	}
	// Without the grant the functions do not exist, and the server refuses the call too.
	c = newCodeClient(t, M{"dir": dir})
	if r, _ := c.code(`[typeof read, typeof write, typeof edit, typeof sh]`); enc(r["value"]) != `["undefined","undefined","undefined","undefined"]` {
		t.Fatalf("ungranted: %v", r["value"])
	}
	srv := &codeServer{spec: M{"dir": dir}}
	for _, tool := range []string{"write", "edit", "sh"} {
		if _, fail := srv.bridge(nil, "host", tool, M{"path": "x", "command": "true"}); jStr(fail, "code") != "tool-not-granted" {
			t.Errorf("%s: %v", tool, fail)
		}
	}
}

// Risk: sh writes outside the harness's write roots, reaches the network without gitNetwork, signals other processes,
// sees the run token, or hangs the runtime past reset (fleet-code-mode.md, authority per harness).
func TestCodeModeShell(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	sleeper := exec.Command("sleep", "30")
	if err := sleeper.Start(); err != nil {
		t.Fatal(err)
	}
	defer sleeper.Process.Kill()
	dir := t.TempDir()
	// Outside every write root and outside /tmp, which is the sandbox's own on Linux: a refused write there is the
	// sandbox's refusal, not a missing directory.
	outside, err := os.MkdirTemp("/var/tmp", "fleet-code-outside-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(outside) })
	os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: elsewhere\n"), 0o644)
	t.Setenv("TMPDIR", t.TempDir()) // the temporary directory the sandbox may write; `outside` is not under it
	t.Setenv("FLEET_RUN_TOKEN", "invented")
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, "marker"), []byte("owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	port := strings.TrimPrefix(srv.URL, "http://127.0.0.1:")
	httpGet := `exec 3<>/dev/tcp/127.0.0.1/` + port + ` && printf 'GET / HTTP/1.0\r\n\r\n' >&3 && cat <&3 >/dev/null`
	probe := `const run = async (cmd) => (await sh(cmd)).code
		return {pwd: (await sh("pwd -P")).text(), word: (await sh('printf %s "$1"', {args:["a 'b'"]})).stdout, json: (await sh("echo '{\"k\":1}'")).json(),
			exit: await run("exit 3"), inside: await run("echo x > made.txt"), outside: await run("echo x > ` + filepath.Join(outside, "o.txt") + `"),
			tmp: await run("echo x > \"$TMPDIR/t.txt\""), git: await run("echo x >> .git"), net: await run(` + enc(httpGet) + `),
			signal: await run("kill ` + fmt.Sprint(sleeper.Process.Pid) + `"), token: (await sh("echo ${FLEET_RUN_TOKEN:-none}")).text()}`
	sandboxed := M{"sandbox": true, "write": []any{dir}, "network": false}
	c := newCodeClient(t, M{"dir": dir, "shell": sandboxed, "gitEnv": []any{"GIT_AUTHOR_NAME=Fixture"}})
	r, bad := c.code(probe)
	real, _ := canon(dir)
	if bad || jStr(r, "value", "pwd") != real || jStr(r, "value", "word") != "a 'b'" || enc(jAt(r, "value", "json")) != `{"k":1}` || jStr(r, "value", "token") != "none" {
		t.Fatalf("sh: %v %v", bad, r)
	}
	for k, want := range map[string]bool{"exit": false, "inside": true, "outside": false, "tmp": true, "git": false, "net": false, "signal": false} {
		if got := enc(jAt(r, "value", k)) == "0"; got != want {
			t.Errorf("%s: exit code %v", k, jAt(r, "value", k))
		}
	}
	if enc(jAt(r, "value", "exit")) != "3" {
		t.Errorf("exit: %v", jAt(r, "value", "exit"))
	}
	if r, bad := c.code(`(await sh('printf "%s " "$GIT_AUTHOR_NAME"; cat "$HOME/marker"')).text()`); bad || r["value"] != "Fixture owned" {
		t.Fatalf("Git settings or isolated HOME: %v %v", bad, r)
	}
	if _, err := os.Stat(filepath.Join(dir, "made.txt")); err != nil {
		t.Error("the write inside the worktree is missing")
	}
	if _, err := os.Stat(filepath.Join(outside, "o.txt")); err == nil || hits.Load() != 0 {
		t.Errorf("wrote outside or reached the network: %v, server hits %d", err, hits.Load())
	}
	if b, _ := os.ReadFile(filepath.Join(dir, ".git")); string(b) != "gitdir: elsewhere\n" || sleeper.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatalf(".git changed or the other process was signalled: %q", b)
	}
	// A timeout kills the command; reset kills a running one, so its job ends.
	start := time.Now()
	if r, _ := c.code(`(await sh("true", {timeoutMs: 1e300})).code`); enc(r["value"]) != "0" {
		t.Fatalf("huge timeout: %v", r)
	}
	if r, _ := c.code(`await sh("sleep 20", {timeoutMs: 200})`); jAt(r, "value", "timedOut") != true || time.Since(start) > 5*time.Second {
		t.Fatalf("timeout: %v after %v", r, time.Since(start))
	}
	r, _ = c.call("code", M{"source": `await sh("sleep 20")`, "detachAfterSeconds": 0.3})
	c.call("poll", M{"reset": true})
	if r, _ := c.call("poll", M{"jobId": jStr(r, "jobId"), "waitSeconds": 10}); r["status"] != "failed" || time.Since(start) > 8*time.Second {
		t.Fatalf("reset with sh running: %v after %v", r, time.Since(start))
	}

	// With gitNetwork the sandbox has network; the harness's unsandboxed shell (Claude's Bash) writes anywhere.
	c = newCodeClient(t, M{"dir": dir, "shell": M{"sandbox": true, "write": []any{dir}, "network": true}})
	if r, _ := c.code(`(await sh(` + enc(httpGet) + `)).code`); enc(r["value"]) != "0" || hits.Load() != 1 {
		t.Fatalf("network with gitNetwork: %v, hits %d", r, hits.Load())
	}
	// A read-only start: commands run, nothing is written, not even the temporary directory.
	c = newCodeClient(t, M{"dir": dir, "shell": M{"sandbox": true, "readOnly": true, "network": false, "read": []any{dir}}})
	if r, _ := c.code(`[(await sh("cat .git")).code, (await sh("echo x > ro.txt")).code, (await sh("echo x > \"$TMPDIR/ro.txt\"")).code]`); jStr(r, "value") != "" || enc(jArr(r, "value")[0]) != "0" || enc(jArr(r, "value")[1]) == "0" || enc(jArr(r, "value")[2]) == "0" {
		t.Fatalf("read-only sh: %v", r)
	}
	c = newCodeClient(t, M{"dir": dir, "shell": M{"sandbox": false}})
	if r, _ := c.code(`(await sh("echo x > ` + filepath.Join(outside, "free.txt") + `")).code`); enc(r["value"]) != "0" {
		t.Fatalf("unsandboxed: %v", r)
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

// Risk: a sandboxed sh reaches the host through a Unix socket (the executor's MCP sockets, docker.sock, D-Bus) or a
// launchd job outside the sandbox, loses /tmp between calls, or writes .git under another case (fleet-code-mode.md,
// authority per harness).
func TestCodeModeShellHost(t *testing.T) {
	dir := t.TempDir()
	sockDir, err := os.MkdirTemp("/var/tmp", "fcs") // short: a Unix socket path has about 100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(sockDir) })
	// A planted .git file pointing into the hidden data directory grants no read: grants come from the launch only.
	planted := "gitdir: " + sockDir + "\n"
	os.WriteFile(filepath.Join(dir, ".git"), []byte(planted), 0o644)
	os.WriteFile(filepath.Join(sockDir, "commondir"), []byte(".\n"), 0o644)
	// Keep the listener outside the hidden data directory so this test checks the socket policy itself.
	socketRoot, err := os.MkdirTemp("/var/tmp", "fcs-socket")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketRoot) })
	sock := filepath.Join(socketRoot, "s")
	os.WriteFile(filepath.Join(sockDir, "run.env"), []byte("FLEET_RUN_TOKEN=invented\n"), 0o600) // like the executor data directory
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	var accepted atomic.Int32
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			accepted.Add(1)
			conn.Close()
		}
	}()
	connect := `node -e 'require("net").connect(` + enc(sock) + `).on("connect", () => process.exit(0)).on("error", () => process.exit(1))'`
	name := fmt.Sprintf("fleet-code-%d", time.Now().UnixNano())
	t.Cleanup(func() { os.Remove(filepath.Join("/private/tmp", name)) })
	probe := `const run = async (cmd) => (await sh(cmd)).code
		await sh("echo kept > /tmp/` + name + `")
		return {socket: await run(` + enc(connect) + `), tmp: (await sh("cat /tmp/` + name + `")).text(), upper: await run("echo x >> .GIT"),
			launchd: await run("launchctl submit -l ` + name + ` -- /usr/bin/true"), run: (await sh("find /run ! -type d -print")).text(),
			runFile: await run("cat ` + filepath.Join(sockDir, "run.env") + `"), input: (await sh("echo $FLEET_INPUT_DIR")).text()}`
	// With network, as with gitNetwork: IP yes, host Unix sockets no.
	c := newCodeClient(t, M{"dir": dir, "shell": M{"sandbox": true, "write": []any{dir}, "network": true, "hide": []any{sockDir}}, "env": M{"FLEET_INPUT_DIR": "/in"}})
	r, bad := c.code(probe)
	if bad || enc(jAt(r, "value", "socket")) == "0" || accepted.Load() != 0 || jStr(r, "value", "tmp") != "kept" ||
		enc(jAt(r, "value", "runFile")) == "0" || jStr(r, "value", "input") != "/in" {
		t.Fatalf("socket or /tmp: %v %v, accepted %d", bad, r, accepted.Load())
	}
	switch runtime.GOOS {
	case "darwin":
		exec.Command("launchctl", "remove", name).Run()
		if enc(jAt(r, "value", "upper")) == "0" || enc(jAt(r, "value", "launchd")) == "0" {
			t.Fatalf(".GIT or a launchd job: %v", r["value"])
		}
		if b, _ := os.ReadFile(filepath.Join(dir, ".git")); string(b) != planted {
			t.Fatalf(".git changed through .GIT: %q", b)
		}
	case "linux":
		want := ""
		if file, err := filepath.EvalSymlinks("/etc/resolv.conf"); err == nil && under(file, "/run") {
			want = file
		}
		if got := jStr(r, "value", "run"); got != want {
			t.Fatalf("unexpected files in /run: got %q, want %q", got, want)
		}
	}
	// The listener works: an unsandboxed command reaches it.
	c = newCodeClient(t, M{"dir": dir, "shell": M{"sandbox": false}})
	if r, _ := c.code(`(await sh(` + enc(connect) + `)).code`); enc(r["value"]) != "0" || accepted.Load() != 1 {
		t.Fatalf("control: %v, accepted %d", r, accepted.Load())
	}
}
