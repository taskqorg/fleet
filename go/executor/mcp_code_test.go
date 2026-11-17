package main

import (
	"bufio"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
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
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
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
	sc.Buffer(nil, 64<<20)
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
	if r, _ := c.code(`return (await conn.web.tools()).map(t => t.name)`); enc(r["value"]) != `["search","broken"]` {
		t.Fatalf("tools: %v", r["value"])
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
	if r, bad := c.call("poll", M{"jobId": id, "waitSeconds": 5}); bad || r["status"] != "done" || r["value"] != "late" {
		t.Fatalf("poll: %v %v", bad, r)
	}
}

// Risk: code writes or runs commands outside the harness sandbox, so code mode adds authority
// (fleet-code-mode.md, authority).
func TestCodeModePermissions(t *testing.T) {
	dir := t.TempDir()
	c := newCodeClient(t, M{"dir": dir, "read": []any{dir}})
	src := `const fs = require("node:fs"); const out = {}
		try { fs.writeFileSync(` + enc(filepath.Join(dir, "x")) + `, "1"); out.write = "allowed" } catch (e) { out.write = e.code }
		try { require("node:child_process").execSync("true"); out.exec = "allowed" } catch (e) { out.exec = e.code }
		out.read = fs.readdirSync(` + enc(dir) + `).length
		return out`
	r, bad := c.code(src)
	if bad || enc(r["value"]) != `{"exec":"ERR_ACCESS_DENIED","read":0,"write":"ERR_ACCESS_DENIED"}` {
		t.Fatalf("permissions: %v %v", bad, r)
	}
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
}

// Risk: an unconfirmed Fleet mutation reaches code as a failure, so the agent repeats it (fleet-code-mode.md, unknown
// outcomes).
func TestCodeModeFleetResults(t *testing.T) {
	pending := mcpText(M{"status": "pending", "requestKey": "k"}, &yes)
	if v, fail := mcpValue(pending); fail != nil || jStr(v, "requestKey") != "k" {
		t.Fatalf("pending: %v %v", v, fail)
	}
	if _, fail := mcpValue(mcpText(M{"error": "nope"}, &yes)); jStr(fail, "code") != "tool-error" || jStr(fail, "output", "error") != "nope" {
		t.Fatalf("error: %v", fail)
	}
	if v, fail := mcpValue(M{"content": []any{M{"type": "text", "text": "plain"}}}); fail != nil || v != "plain" {
		t.Fatalf("text: %v", v)
	}

	c := newCodeClient(t, M{"dir": t.TempDir(), "journal": filepath.Join(t.TempDir(), "journal.sqlite"),
		"context": M{"commandId": "cmd", "executionId": "ex", "hostId": "h"}})
	r, bad := c.code(`const names = (await fleet.tools()).map(t => t.name)
		let code; try { await fleet.task_create({}) } catch (e) { code = e.code }
		return {start: names.includes("session_start"), project: names.includes("task_create"), code}`)
	if bad || enc(r["value"]) != `{"code":"tool-not-granted","project":false,"start":true}` {
		t.Fatalf("fleet tools: %v %v", bad, r)
	}
}

// Risk: `only` leaves the direct servers, a read-restricted Claude profile reads files through code, or code mode starts
// with nothing to bridge (fleet-code-mode.md, profile setting).
func TestCodeModeLaunchServers(t *testing.T) {
	granted := []any{M{"name": "web", "tools": []any{M{"name": "search"}}}}
	launch := func(mode, harness string, profile M, journal string) (M, M, error) {
		servers := M{"fleet": M{}, "web": M{}, "serena": M{}}
		err := addCodeServer(servers, mode, harness, profile, journal, M{"commandId": "c"}, granted, map[string]bool{"web": true}, "/run.env", []string{"/work", "/input"})
		if err != nil {
			return servers, nil, err
		}
		spec, _ := dec(jStr(jArr(servers, "code", "args")[2]))
		return servers, spec.(M), nil
	}
	servers, spec, err := launch("add", "codex", M{}, "/journal")
	if err != nil || !has(servers, "fleet") || !has(servers, "web") || enc(spec["read"]) != `["/work","/input"]` || jStr(spec, "journal") != "/journal" || len(jArr(spec, "connections")) != 1 {
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
}
