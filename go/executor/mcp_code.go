package main

// `executor mcp code <spec>`: the code mode server (docs/designs/fleet-code-mode.md). Its `code` tool runs JavaScript in
// one Node runtime per server; Fleet tools and granted connections are bridge functions there, served by the same
// handlers as the `fleet` and connection servers. The runtime gets no run token and runs under Node's permission model.

import (
	"bufio"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed code_runtime.js
var codeRuntimeJS string

var codeTools = []any{
	M{"name": "code", "description": "Run JavaScript: the body of an async function (await, return, Promise.all). Fleet tools are fleet.<tool>(args); granted connections are conn[\"<name>\"].<tool>(args); fleet.tools() and conn[\"<name>\"].tools() list names and input schemas. A failed call throws FleetCallError {tool, outcome, code, output}. `state` keeps values between calls; let/const/var do not. print() or console.log add to output. Only the returned value and printed text reach you, so filter in code. Files under the working directory are readable with require('node:fs'); writes and commands use your own tools. Still running after detachAfterSeconds (default 50): replies with a jobId for poll.",
		"inputSchema": M{"type": "object", "properties": M{"source": M{"type": "string"}, "detachAfterSeconds": M{"type": "number"}}, "required": []any{"source"}, "additionalProperties": false}},
	M{"name": "poll", "description": "Wait for a detached code job: its final output and value, or its output so far after waitSeconds (default 50). Without jobId: list this runtime's jobs.",
		"inputSchema": M{"type": "object", "properties": M{"jobId": M{"type": "string"}, "waitSeconds": M{"type": "number"}}, "additionalProperties": false}},
}

type codeJob struct {
	id     string
	output strings.Builder
	calls  []any
	done   chan struct{}
	ok     bool
	value  any
	err    any
}

type codeServer struct {
	spec   any
	ctx    any     // the fleet server context, nil without Fleet tools
	db     *sql.DB // the executor journal, nil without Fleet tools
	fleet  map[string]bool
	conns  map[string]any // connection name -> spec
	mu     sync.Mutex
	jobs   map[string]*codeJob
	order  []string
	rt     *codeRuntime
	starts int
}

type codeRuntime struct {
	cmd  *exec.Cmd
	mu   sync.Mutex // serializes writes to stdin
	in   io.WriteCloser
	dead chan struct{}
}

func codeMCP(spec any, in io.Reader, w io.Writer) error {
	s := &codeServer{spec: spec, jobs: map[string]*codeJob{}, fleet: map[string]bool{}, conns: map[string]any{}}
	if journal := jStr(spec, "journal"); journal != "" {
		db, err := openJournal(journal)
		if err != nil {
			return err
		}
		defer db.Close()
		s.db, s.ctx = db, jAt(spec, "context")
		for _, t := range jArr(mcpTools(s.ctx), "tools") {
			s.fleet[jStr(t, "name")] = true
		}
		s.fleet["agent_tool_result"] = true
	}
	for _, c := range jArr(spec, "connections") {
		s.conns[jStr(c, "name")] = c
	}
	if f := jStr(spec, "runFile"); f != "" {
		if err := loadRunFile(f); err != nil {
			return err
		}
	}
	defer s.stop()
	out := bufio.NewWriter(w)
	sc := bufio.NewScanner(in)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		m, err := dec(sc.Text())
		if err != nil {
			fmt.Fprintln(out, enc(M{"jsonrpc": "2.0", "id": nil, "error": M{"code": -32700, "message": "Parse error"}}))
			out.Flush()
			continue
		}
		if !has(m, "id") {
			continue
		}
		params := jAt(m, "params")
		reply := M{"jsonrpc": "2.0", "id": jAt(m, "id")}
		switch jStr(m, "method") {
		case "initialize":
			pv := any("2025-06-18")
			if has(params, "protocolVersion") {
				pv = jAt(params, "protocolVersion")
			}
			reply["result"] = M{"protocolVersion": pv, "capabilities": M{"tools": M{}}, "serverInfo": M{"name": "fleet-code", "version": "1"}}
		case "ping":
			reply["result"] = M{}
		case "tools/list":
			reply["result"] = M{"tools": codeTools}
		case "tools/call":
			args := jAt(params, "arguments")
			switch jStr(params, "name") {
			case "code":
				reply["result"] = s.code(jStr(args, "source"), seconds(jAt(args, "detachAfterSeconds")))
			case "poll":
				reply["result"] = s.poll(jStr(args, "jobId"), seconds(jAt(args, "waitSeconds")))
			default:
				reply["error"] = M{"code": -32602, "message": "unknown tool"}
			}
		default:
			reply["error"] = M{"code": -32601, "message": "method not found"}
		}
		fmt.Fprintln(out, enc(reply))
		out.Flush()
	}
	return sc.Err()
}

// seconds is a reply wait in seconds; absent or not positive means the 50 s default, under Codex's 60 s MCP tool timeout.
func seconds(v any) time.Duration {
	if f, ok := num(v); ok && f > 0 {
		return time.Duration(f * float64(time.Second))
	}
	return 50 * time.Second
}

func codeResult(v M, isError bool) M {
	return M{"content": []any{M{"type": "text", "text": enc(v)}}, "isError": isError}
}

func (s *codeServer) code(source string, detach time.Duration) M {
	if strings.TrimSpace(source) == "" {
		return codeResult(M{"error": "source required"}, true)
	}
	rt, restarted, err := s.runtime()
	if err != nil {
		return codeResult(M{"error": "runtime did not start: " + err.Error()}, true)
	}
	s.mu.Lock()
	j := &codeJob{id: fmt.Sprintf("job-%d", len(s.order)+1), done: make(chan struct{})}
	s.jobs[j.id] = j
	s.order = append(s.order, j.id)
	s.mu.Unlock()
	if err := rt.send(M{"type": "run", "job": j.id, "source": source}); err != nil {
		s.finish(j, false, nil, M{"message": "runtime-exited"})
	}
	r := s.wait(j, detach)
	if restarted {
		r["restarted"] = true
	}
	return codeResult(r, has(r, "error"))
}

func (s *codeServer) poll(id string, wait time.Duration) M {
	s.mu.Lock()
	if id == "" {
		list := []any{}
		for _, k := range s.order {
			list = append(list, M{"jobId": k, "status": jobStatus(s.jobs[k])})
		}
		s.mu.Unlock()
		return codeResult(M{"jobs": list}, false)
	}
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return codeResult(M{"error": "unknown jobId"}, true)
	}
	r := s.wait(j, wait)
	return codeResult(r, has(r, "error"))
}

func jobStatus(j *codeJob) string {
	select {
	case <-j.done:
		return map[bool]string{true: "done", false: "failed"}[j.ok]
	default:
		return "running"
	}
}

// wait replies with the job's end, or with its output so far when the wait passes first; the job keeps running.
func (s *codeServer) wait(j *codeJob, d time.Duration) M {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-j.done:
	case <-t.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := M{"jobId": j.id, "status": jobStatus(j), "output": j.output.String(), "calls": append([]any{}, j.calls...)}
	if r["status"] == "done" {
		r["value"] = j.value
	} else if r["status"] == "failed" {
		r["error"] = j.err
	}
	return r
}

func (s *codeServer) finish(j *codeJob, ok bool, value, err any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-j.done:
		return
	default:
	}
	j.ok, j.value, j.err = ok, value, err
	close(j.done)
}

// runtime returns the live Node process, starting one if none runs; restarted reports a replaced runtime whose state
// is gone.
func (s *codeServer) runtime() (*codeRuntime, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rt != nil {
		select {
		case <-s.rt.dead:
		default:
			return s.rt, false, nil
		}
	}
	node := jStr(s.spec, "node")
	if node == "" {
		node = "node"
	}
	// Node's permission model: no writes, child processes, workers or addons; reads only the declared roots.
	args := []string{"--permission"}
	for _, r := range jArr(s.spec, "read") {
		if p, ok := str(r); ok && p != "" {
			args = append(args, "--allow-fs-read="+p)
		}
	}
	cmd := exec.Command(node, append(args, "-e", codeRuntimeJS)...)
	cmd.Dir = jStr(s.spec, "dir")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "NODE_NO_WARNINGS=1"}
	cmd.Stderr = os.Stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, false, err
	}
	rt := &codeRuntime{cmd: cmd, in: in, dead: make(chan struct{})}
	s.rt = rt
	s.starts++
	go s.read(rt, stdout)
	return rt, s.starts > 1, nil
}

func (rt *codeRuntime) send(m M) error {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	_, err := io.WriteString(rt.in, enc(m)+"\n")
	return err
}

func (s *codeServer) stop() {
	s.mu.Lock()
	rt := s.rt
	s.mu.Unlock()
	if rt != nil {
		rt.in.Close()
		rt.cmd.Process.Kill()
		<-rt.dead
	}
}

// read handles the runtime's lines until it exits; then every unfinished job ends with runtime-exited.
func (s *codeServer) read(rt *codeRuntime, stdout io.Reader) {
	sc := bufio.NewScanner(stdout)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		m, err := dec(sc.Text())
		if err != nil {
			continue
		}
		s.mu.Lock()
		j := s.jobs[jStr(m, "job")]
		s.mu.Unlock()
		switch jStr(m, "type") {
		case "print":
			if j != nil {
				s.mu.Lock()
				j.output.WriteString(jStr(m, "text"))
				s.mu.Unlock()
			} else {
				fmt.Fprint(os.Stderr, jStr(m, "text"))
			}
		case "done":
			if j != nil {
				s.finish(j, jAt(m, "ok") == true, jAt(m, "value"), jAt(m, "error"))
			}
		case "call":
			go s.call(rt, j, m)
		}
	}
	rt.cmd.Wait()
	close(rt.dead)
	s.mu.Lock()
	var open []*codeJob
	for _, j := range s.jobs {
		if jobStatus(j) == "running" {
			open = append(open, j)
		}
	}
	s.mu.Unlock()
	for _, j := range open {
		s.finish(j, false, nil, M{"message": "runtime-exited: the runtime stopped; state is lost"})
	}
}

// call serves one bridge call with the same handler as the direct server and answers the runtime.
func (s *codeServer) call(rt *codeRuntime, j *codeJob, m any) {
	start := time.Now()
	server, tool, args := jStr(m, "server"), jStr(m, "tool"), jAt(m, "args")
	value, fail := s.bridge(server, tool, args)
	label := tool
	if c, ok := strings.CutPrefix(server, "conn:"); ok {
		label = c + "." + tool
	}
	outcome := "ok"
	if fail != nil {
		outcome = jStr(fail, "outcome")
	}
	if j != nil {
		s.mu.Lock()
		j.calls = append(j.calls, M{"tool": label, "ms": time.Since(start).Milliseconds(), "outcome": outcome})
		s.mu.Unlock()
	}
	reply := M{"type": "reply", "id": jAt(m, "id"), "ok": fail == nil, "value": value}
	if fail != nil {
		fail["tool"] = label
		reply["error"] = fail
	}
	rt.send(reply)
}

func callError(outcome, code string, output any) M {
	return M{"outcome": outcome, "code": code, "output": output}
}

func (s *codeServer) bridge(server, tool string, args any) (any, M) {
	if !truth(args) {
		args = M{}
	}
	if name, ok := strings.CutPrefix(server, "conn:"); ok {
		spec, granted := s.conns[name]
		if !granted {
			return nil, callError("error", "connection-not-granted", nil)
		}
		if tool == "tools" {
			return jArr(spec, "tools"), nil
		}
		if !slices.ContainsFunc(jArr(spec, "tools"), func(t any) bool { return jStr(t, "name") == tool }) {
			return nil, callError("error", "tool-not-granted", nil)
		}
		line := connectionCall(name, tool, args)
		var output any = nil
		if len(line.Output) > 0 {
			output, _ = dec(string(line.Output))
		}
		if string(line.Outcome) != "ok" {
			code := ""
			if line.ErrorCode != nil {
				code = *line.ErrorCode
			}
			return nil, callError(string(line.Outcome), code, output)
		}
		return output, nil
	}
	if server != "fleet" || s.db == nil {
		return nil, callError("error", "no-fleet-tools", nil)
	}
	if tool == "tools" {
		return jArr(mcpTools(s.ctx), "tools"), nil
	}
	if !s.fleet[tool] {
		return nil, callError("error", "tool-not-granted", nil)
	}
	r, err := delegation(M{"method": "tools/call", "params": M{"name": tool, "arguments": args}}, s.ctx, s.db)
	if err != nil {
		return nil, callError("error", "invalid-call", err.Error())
	}
	return mcpValue(r)
}

// mcpValue unwraps an MCP tool result: its text parsed as JSON when it is JSON; isError becomes a call error.
func mcpValue(r any) (any, M) {
	content := jArr(r, "content")
	if content == nil {
		return r, nil
	}
	var parts []string
	for _, c := range content {
		parts = append(parts, jStr(c, "text"))
	}
	text := strings.Join(parts, "\n")
	v, err := dec(text)
	if err != nil {
		v = text
	}
	if jAt(r, "isError") == true {
		if jStr(v, "status") == "pending" {
			return v, nil // an unconfirmed mutation: the caller reads it later with agent_tool_result, never repeats it
		}
		return nil, callError("error", "tool-error", v)
	}
	return v, nil
}

// codeNode finds the host's Node for the runtime: 22.15 or later, for module.registerHooks (the network refusal).
var codeNode = func() (string, error) {
	node, err := exec.LookPath("node")
	if err != nil {
		return "", errors.New("code-mode-unready: node is not installed")
	}
	out, err := exec.Command(node, "--version").Output()
	v := strings.TrimSpace(string(out))
	var major, minor int
	if _, scanErr := fmt.Sscanf(v, "v%d.%d", &major, &minor); err != nil || scanErr != nil || major < 22 || major == 22 && minor < 15 {
		return "", fmt.Errorf("code-mode-unready: node %s, needs 22.15 or later", v)
	}
	return node, nil
}

// addCodeServer adds the `code` server to a launch's servers; mode `only` removes the fleet and connection servers it
// bridges. A Claude profile without Read, Grep, Glob or Bash gets no file reads in the runtime.
func addCodeServer(servers M, mode, harness string, profile any, journal string, fleetContext any, granted []any, gatewayConns map[string]bool, runFile string, read []string) error {
	spec := M{"dir": read[0], "read": []any{}, "runFile": runFile}
	if journal != "" {
		spec["journal"], spec["context"] = journal, fleetContext
	}
	conns := []any{}
	for _, c := range granted {
		if gatewayConns[jStr(c, "name")] {
			conns = append(conns, c)
		}
	}
	if journal == "" && len(conns) == 0 {
		return errors.New("codeMode needs Fleet tools or a connection")
	}
	if has(servers, "code") {
		return errors.New("MCP server name collides with the code server")
	}
	spec["connections"] = conns
	node, err := codeNode()
	if err != nil {
		return err
	}
	spec["node"] = node
	native, _ := claudeNativeTools(profile)
	if harness != "claude" || slices.ContainsFunc(native, func(t string) bool { return t == "Read" || t == "Grep" || t == "Glob" || t == "Bash" }) {
		for _, r := range read {
			if r != "" {
				spec["read"] = append(spec["read"].([]any), r)
			}
		}
	}
	if mode == "only" {
		delete(servers, "fleet")
		for c := range gatewayConns {
			delete(servers, c)
		}
	}
	servers["code"] = M{"command": exe(), "args": []any{"mcp", "code", enc(spec)}, "tools": []any{"code", "poll"}, "env": []any{"HOME", "PATH"}}
	return nil
}
