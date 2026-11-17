package main

// `executor mcp code <spec>`: the code mode server (docs/designs/fleet-code-mode.md). Its `code` tool runs JavaScript in
// one Node runtime per server; Fleet tools and granted connections are bridge functions there, served by the same
// handlers as the `fleet` and connection servers. The runtime gets no run token, runs under Node's permission model, and
// has no network: an OS sandbox (sandbox-exec on macOS, bwrap on Linux) removes it.

import (
	"bufio"
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

//go:embed code_runtime.js
var codeRuntimeJS string

var codeTools = []any{
	M{"name": "code", "description": "Run JavaScript: the body of an async function (await, return, Promise.all). Fleet tools are fleet.<tool>(args); granted connections are conn[\"<name>\"].<tool>(args); fleet.$tools() and conn[\"<name>\"].$tools() list names and input schemas. A failed call throws FleetCallError {tool, outcome, code, output}; outcome unknown means it may have happened: inspect, never repeat. `state` keeps values between calls; let/const/var do not. print() or console.log add to output. Only the returned value and printed text reach you, so filter in code. Files under the working directory are readable with require('node:fs'); writes, commands and network use your own tools. Still running after detachAfterSeconds (default 25): replies with a jobId for poll.",
		"inputSchema": M{"type": "object", "properties": M{"source": M{"type": "string"}, "detachAfterSeconds": M{"type": "number"}}, "required": []any{"source"}, "additionalProperties": false}},
	M{"name": "poll", "description": "Wait for a detached code job: its final output and value (returned once), or its output so far after waitSeconds (default 25). Without jobId: list the unfinished and undelivered jobs. reset: true stops the runtime (for a job that never yields): state is lost, and every running job fails once its bridged calls in flight end.",
		"inputSchema": M{"type": "object", "properties": M{"jobId": M{"type": "string"}, "waitSeconds": M{"type": "number"}, "reset": M{"type": "boolean"}}, "additionalProperties": false}},
}

type codeJob struct {
	id     string
	rt     *codeRuntime
	output strings.Builder // output not yet replied
	calls  []any           // calls not yet replied
	busy   int             // bridged calls in flight
	ended  *[3]any         // the runtime's done line (ok, value, error) while calls are in flight
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
	seq    int
	closed bool // the server is shutting down: no new runtime
}

type codeRuntime struct {
	gone bool    // its stdout ended (under codeServer.mu)
	tail tailBuf // the end of its stderr, for the runtime-exited reason
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
	}
	for _, c := range jArr(spec, "connections") {
		s.conns[jStr(c, "name")] = c
	}
	if f := jStr(spec, "runFile"); f != "" {
		if err := loadRunFile(f); err != nil {
			return err
		}
	}
	// On shutdown the runtime stops and the requests return; bridged calls still in flight end with the process. A journal
	// write is one SQLite transaction, so it is either recorded or not, and its outcome stays unknown to the agent as for
	// any lost reply.
	// Requests are served concurrently, so a long `code` call never delays a ping, a poll or a second call; only the
	// writes are serialized.
	var wmu sync.Mutex
	var wg sync.WaitGroup
	// On stdin end the runtime stops first, so waiting requests return with runtime-exited instead of their full wait.
	defer wg.Wait()
	defer func() {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		s.stop()
		// Nobody reads these replies any more: end every job now, calls in flight included, so no request waits.
		s.mu.Lock()
		var open []*codeJob
		for _, j := range s.jobs {
			open = append(open, j)
		}
		s.mu.Unlock()
		for _, j := range open {
			s.finish(j, false, nil, runtimeExited())
		}
	}()
	out := bufio.NewWriter(w)
	write := func(m M) {
		wmu.Lock()
		defer wmu.Unlock()
		fmt.Fprintln(out, enc(m))
		out.Flush()
	}
	r := bufio.NewReader(in) // no line cap: a request may carry a large source
	for {
		line, rerr := r.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			m, err := dec(string(line))
			if err != nil {
				write(M{"jsonrpc": "2.0", "id": nil, "error": M{"code": -32700, "message": "Parse error"}})
			} else if has(m, "id") {
				wg.Add(1)
				go func() {
					defer wg.Done()
					write(s.serve(m))
				}()
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return rerr
		}
	}
}

func (s *codeServer) serve(m any) M {
	params := jAt(m, "params")
	reply := M{"jsonrpc": "2.0", "id": jAt(m, "id")}
	switch jStr(m, "method") {
	case "initialize":
		pv := any("2025-06-18")
		if has(params, "protocolVersion") {
			pv = jAt(params, "protocolVersion")
		}
		// With `only`, the direct connection servers are gone: their instructions (query syntax, limits) come from here.
		var instr []string
		for _, c := range jArr(s.spec, "instructions") {
			if i := jStr(c, "instructions"); i != "" {
				instr = append(instr, "conn[\""+jStr(c, "name")+"\"]: "+i)
			}
		}
		reply["result"] = M{"protocolVersion": pv, "capabilities": M{"tools": M{}}, "serverInfo": M{"name": "fleet-code", "version": "1"}, "instructions": strings.Join(instr, "\n\n")}
	case "ping":
		reply["result"] = M{}
	case "tools/list":
		tools := jClone(codeTools).([]any)
		if len(jArr(s.spec, "read")) == 0 { // a profile without read tools gets no file reads in code either
			d := tools[0].(M)
			d["description"] = strings.Replace(jStr(d, "description"), "Files under the working directory are readable with require('node:fs'); writes", "No file reads; writes", 1)
		}
		reply["result"] = M{"tools": tools}
	case "tools/call":
		args := jAt(params, "arguments")
		switch jStr(params, "name") {
		case "code":
			reply["result"] = s.code(jStr(args, "source"), seconds(jAt(args, "detachAfterSeconds")))
		case "poll":
			if jAt(args, "reset") == true {
				s.stop()
				reply["result"] = codeResult(M{"reset": true}, false)
			} else {
				reply["result"] = s.poll(jStr(args, "jobId"), seconds(jAt(args, "waitSeconds")))
			}
		default:
			reply["error"] = M{"code": -32602, "message": "unknown tool"}
		}
	default:
		reply["error"] = M{"code": -32601, "message": "method not found"}
	}
	return reply
}

// seconds is a reply wait: absent or negative means 25 s, 0 answers at once, and more than 50 s means 50 s. The executor's MCP filter
// forwards one request at a time and Codex times out an MCP call at 60 s, so a longer reply wait would lose the reply and
// hold every later call, reset included; a longer job keeps running and is read with further polls.
func seconds(v any) time.Duration {
	if f, ok := num(v); ok && f >= 0 {
		return time.Duration(math.Min(f, 50) * float64(time.Second))
	}
	return 25 * time.Second
}

func codeResult(v M, isError bool) M { return mcpText(v, &isError) }

func (s *codeServer) code(source string, detach time.Duration) M {
	if strings.TrimSpace(source) == "" {
		return codeResult(M{"error": "source required"}, true)
	}
	s.mu.Lock()
	rt, started, err := s.runtime()
	if err != nil {
		s.mu.Unlock()
		return codeResult(M{"error": "runtime did not start: " + err.Error()}, true)
	}
	s.seq++
	j := &codeJob{id: fmt.Sprintf("job-%d", s.seq), rt: rt, done: make(chan struct{})}
	s.jobs[j.id] = j
	s.order = append(s.order, j.id)
	s.mu.Unlock()
	// The send may block while the runtime does not read (a job that never yields); the reply still comes at detach,
	// and poll reset ends the send.
	go func() {
		if err := rt.send(M{"type": "run", "job": j.id, "source": source}); err != nil {
			s.finish(j, false, nil, runtimeExited())
		}
	}()
	r := s.wait(j, detach)
	if started {
		r["newRuntime"] = true
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
		return codeResult(M{"error": "unknown jobId: never started or already returned"}, true)
	}
	r := s.wait(j, wait)
	return codeResult(r, has(r, "error"))
}

func jobStatus(j *codeJob) string {
	select {
	case <-j.done:
		if j.ok {
			return "done"
		}
		return "failed"
	default:
		return "running"
	}
}

// tailBuf keeps the last 4 KiB written to it: the reason a runtime stopped, not its whole log.
type tailBuf struct {
	mu sync.Mutex
	b  []byte
}

func (t *tailBuf) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.b = append(t.b, p...)
	if len(t.b) > 4096 {
		t.b = t.b[len(t.b)-4096:]
	}
	return len(p), nil
}

func (t *tailBuf) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return string(t.b)
}

func runtimeExited() M { return M{"message": "runtime-exited: the runtime stopped; state is lost"} }

// callLabel names a bridged call as in `calls` and FleetCallError.tool: `<connection>.<tool>` or the Fleet tool.
func callLabel(server, tool string) string {
	if c, ok := strings.CutPrefix(server, "conn:"); ok {
		return c + "." + tool
	}
	return tool
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
	if s.jobs[j.id] != j {
		return M{"jobId": j.id, "error": "unknown jobId: never started or already returned"} // another wait returned it
	}
	// Each reply carries only the output and calls since the job's previous reply; the server keeps nothing replied.
	calls := j.calls
	if calls == nil {
		calls = []any{}
	}
	r := M{"jobId": j.id, "status": jobStatus(j), "output": j.output.String(), "calls": calls}
	j.output.Reset()
	j.calls = nil
	if r["status"] == "running" {
		return r
	}
	if r["status"] == "done" {
		r["value"] = j.value
	} else {
		r["error"] = j.err
	}
	// A final result is returned once; the server keeps no delivered output or value.
	delete(s.jobs, j.id)
	s.order = slices.DeleteFunc(s.order, func(k string) bool { return k == j.id })
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

// runtime returns the live Node process, starting one if none runs; started reports a new runtime, whose `state` is
// empty: the first one of this code server (a new execution's server has none of an earlier one's state) or a
// replacement. The caller holds s.mu.
func (s *codeServer) runtime() (*codeRuntime, bool, error) {
	if s.closed {
		return nil, false, errors.New("the code server is stopping")
	}
	if s.rt != nil && !s.rt.gone {
		return s.rt, false, nil
	}
	argv, err := codeArgv(s.spec)
	if err != nil {
		return nil, false, err
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if len(jArr(s.spec, "read")) > 0 {
		cmd.Dir = jStr(s.spec, "dir")
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "NODE_NO_WARNINGS=1"}
	// The protocol has its own pipe (fd 3): a raw write to fd 1 (fs.writeSync(1, ...)) cannot corrupt a protocol line; it
	// goes to this server's stderr log.
	rt := &codeRuntime{dead: make(chan struct{})}
	cmd.Stdout, cmd.Stderr = os.Stderr, io.MultiWriter(os.Stderr, &rt.tail)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, false, err
	}
	stdout, proto, err := os.Pipe()
	if err != nil {
		return nil, false, err
	}
	cmd.ExtraFiles = []*os.File{proto}
	err = cmd.Start()
	proto.Close()
	if err != nil {
		stdout.Close()
		return nil, false, err
	}
	rt.cmd, rt.in = cmd, in
	s.rt = rt
	go s.read(rt, stdout)
	// The granted names become real properties of `fleet` and `conn`: a typo or a probe (`then`, `toJSON`) is undefined
	// in the runtime, not a call.
	names := M{}
	for n, spec := range s.conns {
		names[n] = mcpToolNames(spec)
	}
	fleetNames := []string{}
	for n := range s.fleet {
		fleetNames = append(fleetNames, n)
	}
	slices.Sort(fleetNames)
	rt.send(M{"type": "tools", "fleet": anyStrings(fleetNames), "conns": names}) // before any run line
	return rt, true, nil
}

// codeArgv is the runtime command: Node with its permission model (no writes, child processes, workers or addons; reads
// only the declared roots) inside an OS sandbox without network.
func codeArgv(spec any) ([]string, error) {
	node := jStr(spec, "node")
	if node == "" {
		return nil, errors.New("node missing from the code server spec")
	}
	args := []string{node, "--permission"}
	for _, r := range jArr(spec, "read") {
		if p, ok := str(r); ok && p != "" {
			args = append(args, "--allow-fs-read="+p)
		}
	}
	args = append(args, "-e", codeRuntimeJS)
	return sandboxArgv(jStr(spec, "sandbox"), jArr(spec, "read"), args)
}

// codeProfile is the macOS sandbox for the runtime: no network (Unix sockets included), no file writes except /dev/null,
// signals only to itself, and no process but Node itself (no fork, no other exec). Mach services stay reachable.
func codeProfile(node string) string {
	quoted := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(node)
	return noNetworkProfile + `(deny file-write*)(allow file-write-data (literal "/dev/null"))(deny signal)(allow signal (target self))` +
		`(deny process-exec)(allow process-exec (literal "` + quoted + `"))(deny process-fork)`
}

// sandboxArgv wraps a command in the OS sandbox: no network, no writes, no signals to other processes. On macOS Unix
// sockets are refused too; on Linux a socket file inside a read root stays reachable, as it is for the harness's tools. On Linux the sandbox has its own PID namespace and sees only the system directories, Node's own install and
// the read roots, read-only, with its own /tmp, /proc and /dev.
// args[0] is Node's resolved path: on Linux only that file of its install is mounted.
func sandboxArgv(sandbox string, read []any, args []string) ([]string, error) {
	switch runtime.GOOS {
	case "darwin":
		return append([]string{sandbox, "-p", codeProfile(args[0])}, args...), nil
	case "linux":
		argv := []string{sandbox, "--unshare-net", "--unshare-ipc", "--unshare-pid", "--die-with-parent", "--new-session"}
		for _, d := range []string{"/usr", "/lib", "/lib64", "/lib32", "/bin", "/sbin", "/etc"} {
			argv = append(argv, "--ro-bind-try", d, d)
		}
		argv = append(argv, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp") // before the binds: one may be under /tmp
		argv = append(argv, "--ro-bind", args[0], args[0])
		for _, r := range read {
			if p, ok := str(r); ok && p != "" {
				argv = append(argv, "--ro-bind", p, p)
			}
		}
		return append(argv, args...), nil
	}
	return nil, errors.New("no network sandbox on " + runtime.GOOS)
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

// read handles the runtime's lines until its stdout ends or fails; then it stops the process, and every unfinished job of
// this runtime ends with runtime-exited. Lines have no size cap: the runtime never cuts output.
func (s *codeServer) read(rt *codeRuntime, stdout io.Reader) {
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			s.line(rt, line)
		}
		if err != nil {
			break
		}
	}
	s.mu.Lock()
	rt.gone = true // from here no new job uses this runtime: runtime() starts a fresh one
	s.mu.Unlock()
	rt.cmd.Process.Kill()
	rt.cmd.Wait()
	exited := runtimeExited()
	if t := strings.TrimSpace(rt.tail.String()); t != "" {
		exited["stderr"] = t // why it stopped: a sandbox that cannot start, a crash
	}
	s.mu.Lock()
	var open []*codeJob
	for _, j := range s.jobs {
		if j.rt != rt || jobStatus(j) != "running" {
			continue
		}
		if j.busy > 0 {
			// Bridged calls still run in the code server: the job ends when they do, with their outcomes in `calls`.
			// A job that had returned keeps its value.
			if j.ended == nil {
				j.ended = &[3]any{false, nil, exited}
			}
			continue
		}
		open = append(open, j)
	}
	s.mu.Unlock()
	for _, j := range open {
		s.finish(j, false, nil, exited)
	}
	close(rt.dead)
}

func (s *codeServer) line(rt *codeRuntime, line []byte) {
	m, err := dec(string(line))
	if err != nil {
		return
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
			// A call started before the return (unawaited) is still part of the job: the final reply waits for it, so
			// its outcome is in `calls`.
			s.mu.Lock()
			end := [3]any{jAt(m, "ok") == true, jAt(m, "value"), jAt(m, "error")}
			wait := j.busy > 0
			if wait {
				j.ended = &end
			}
			s.mu.Unlock()
			if !wait {
				s.finish(j, end[0] == true, end[1], end[2])
			}
		}
	case "call":
		s.mu.Lock()
		ended := j == nil || jobStatus(j) != "running" || j.ended != nil // the code returned: a timer's call is late
		if !ended {
			j.busy++
		}
		s.mu.Unlock()
		if ended {
			// A call made after its job ended (a timer) would have an effect nobody sees.
			fail := callError("error", "job-ended", nil)
			fail["tool"] = callLabel(jStr(m, "server"), jStr(m, "tool"))
			go rt.send(M{"type": "reply", "id": jAt(m, "id"), "ok": false, "error": fail})
			return
		}
		go s.call(rt, j, m)
	}
}

// call serves one bridge call with the same handler as the direct server and answers the runtime.
func (s *codeServer) call(rt *codeRuntime, j *codeJob, m any) {
	start := time.Now()
	server, tool, args := jStr(m, "server"), jStr(m, "tool"), jAt(m, "args")
	value, fail := s.bridge(server, tool, args)
	label := callLabel(server, tool)
	outcome := "ok"
	if fail != nil {
		outcome = jStr(fail, "outcome")
	}
	reply := M{"type": "reply", "id": jAt(m, "id"), "ok": fail == nil, "value": value}
	if fail != nil {
		fail["tool"] = label
		reply["error"] = fail
	}
	rt.send(reply)
	s.mu.Lock()
	j.calls = append(j.calls, M{"tool": label, "ms": time.Since(start).Milliseconds(), "outcome": outcome})
	j.busy--
	end := j.ended
	last := j.busy == 0 && end != nil
	s.mu.Unlock()
	if last {
		s.finish(j, end[0] == true, end[1], end[2])
	}
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
		if tool == "$tools" {
			return jArr(spec, "tools"), nil
		}
		if !slices.ContainsFunc(jArr(spec, "tools"), func(t any) bool { return jStr(t, "name") == tool }) {
			return nil, callError("error", "tool-not-granted", nil)
		}
		line := connectionCall(name, tool, args)
		var output any
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
	if tool == "$tools" {
		return jArr(mcpTools(s.ctx), "tools"), nil
	}
	if !s.fleet[tool] {
		return nil, callError("error", "tool-not-granted", nil)
	}
	r, err := delegation(M{"method": "tools/call", "params": M{"name": tool, "arguments": args}}, s.ctx, s.db)
	var de dbError
	var ur unreadReply
	if errors.As(err, &de) || errors.As(err, &ur) {
		// The request may already be journaled and forwarded: its effect is unknown, never a failure to retry.
		return nil, callError("unknown", "journal-error", err.Error())
	}
	if err != nil {
		return nil, callError("error", "invalid-call", err.Error())
	}
	return mcpValue(r)
}

// mcpValue unwraps an MCP tool result: a text result parsed as JSON when it is JSON; a result with non-text parts as its
// parts; isError becomes a call error.
func mcpValue(r any) (any, M) {
	content := jArr(r, "content")
	if content == nil {
		return r, nil
	}
	var v any = content
	if !slices.ContainsFunc(content, func(c any) bool { return jStr(c, "type") != "text" }) {
		var parts []string
		for _, c := range content {
			parts = append(parts, jStr(c, "text"))
		}
		text := strings.Join(parts, "\n")
		var err error
		if v, err = dec(text); err != nil {
			v = text
		}
	}
	if jStr(v, "status") == "pending-or-rejected" {
		if jAt(v, "response") == nil {
			// request_child without a host answer: unknown; a retry with the same requestId is the same request.
			return nil, callError("unknown", "pending", v)
		}
		return nil, callError("error", "tool-error", v) // the host answered and refused it
	}
	if jAt(r, "isError") == true {
		if jStr(v, "status") == "pending" {
			// An unconfirmed mutation: unknown, read later with fleet.agent_tool_result({requestKey}), never repeated.
			return nil, callError("unknown", "pending", v)
		}
		return nil, callError("error", "tool-error", v)
	}
	return v, nil
}

// codeHost finds the host's Node (with --permission) and sandbox for the runtime. The first call probes; after that it
// answers from the last result. A ready result whose files are gone probes again at once; a failure older than one minute
// is probed again in the background, so a hanging sandbox never holds the launch path (and the executor's process loop)
// more than once. A host made ready later needs no executor restart. Tests replace it.
// ponytail: one minute; a readiness event from the host settings if hosts change often.
var codeHost = func() func() (string, string, error) {
	var mu sync.Mutex
	var node, sandbox string
	var err error
	var at time.Time
	var probing bool
	probe := func() {
		n, sb, e := probeCodeHost()
		mu.Lock()
		node, sandbox, err, at, probing = n, sb, e, time.Now(), false
		mu.Unlock()
	}
	return func() (string, string, error) {
		mu.Lock()
		switch {
		case at.IsZero() || err == nil && !(isFile(node) && isFile(sandbox)):
			mu.Unlock()
			probe()
			mu.Lock()
		case err != nil && time.Since(at) >= time.Minute && !probing:
			probing = true
			go probe()
		}
		defer mu.Unlock()
		return node, sandbox, err
	}
}()

func probeCodeHost() (node, sandbox string, err error) {
	node, err = exec.LookPath("node")
	if err == nil {
		node, err = filepath.EvalSymlinks(node) // the binary itself: the Linux sandbox mounts only this file
	}
	if err != nil {
		return "", "", errors.New("code-mode-unready: node is not installed")
	}
	// A probe that hangs (a stuck sandbox setup) fails the launch instead of holding every later one.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, node, "--version").Output()
	v := strings.TrimSpace(string(out))
	var major, minor int
	// --permission (not only --experimental-permission): 22.13 or later in 22, 23.5 or later in 23, every later major.
	if _, scanErr := fmt.Sscanf(v, "v%d.%d", &major, &minor); err != nil || scanErr != nil || major < 22 || major == 22 && minor < 13 || major == 23 && minor < 5 {
		return "", "", fmt.Errorf("code-mode-unready: node %s, needs 22.13, 23.5 or later", v)
	}
	tool := map[string]string{"darwin": "sandbox-exec", "linux": "bwrap"}[runtime.GOOS]
	if tool == "" {
		return "", "", errors.New("code-mode-unready: no network sandbox on " + runtime.GOOS)
	}
	if sandbox, err = exec.LookPath(tool); err != nil {
		return "", "", errors.New("code-mode-unready: " + tool + " is not installed")
	}
	// The sandbox must start here: a host without network namespaces (Linux user-namespace limits) fails the launch.
	argv, err := sandboxArgv(sandbox, nil, []string{node, "--permission", "-e", ""})
	if err == nil {
		err = exec.CommandContext(ctx, argv[0], argv[1:]...).Run()
	}
	if err != nil {
		return "", "", fmt.Errorf("code-mode-unready: %s does not start: %v", tool, err)
	}
	return node, sandbox, nil
}

// codeSpecFile is the code server's spec for one execution, beside its run file and removed with it.
func codeSpecFile(root, execution string) string {
	return filepath.Join(root, "runs", execution+".code.json")
}

// codeFleet is the fleet server's journal and context for the code server; nil without Fleet tools.
type codeFleet struct {
	journal string
	context any
}

// addCodeServer adds the `code` server to a launch's servers; mode `only` removes the fleet and connection servers it
// bridges. A Claude profile without Read, Grep, Glob or Bash gets no file reads in the runtime.
// The spec goes to specFile (0600), not argv: fleet operations and connection schemas can exceed one argv string.
// conns are the launch's connection servers' specs, with the tools those servers expose.
func addCodeServer(servers M, mode, harness string, profile any, fleet *codeFleet, conns []any, runFile, specFile string, read []string) error {
	if conns == nil {
		conns = []any{}
	}
	if fleet == nil && len(conns) == 0 {
		return errors.New("codeMode needs Fleet tools or a connection")
	}
	if has(servers, "code") {
		return errors.New("a granted connection is named code, the code server's name: rename the connection")
	}
	node, sandbox, err := codeHost()
	if err != nil {
		return err
	}
	spec := M{"dir": read[0], "read": []any{}, "runFile": runFile, "connections": conns, "node": node, "sandbox": sandbox, "instructions": []any{}}
	if mode == "only" {
		instr := []any{}
		for _, c := range conns {
			instr = append(instr, M{"name": jAt(c, "name"), "instructions": jAt(c, "instructions")})
		}
		spec["instructions"] = instr
	}
	if fleet != nil {
		spec["journal"], spec["context"] = fleet.journal, fleet.context
	}
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
		for _, c := range conns {
			delete(servers, jStr(c, "name"))
		}
	}
	if err := os.MkdirAll(filepath.Dir(specFile), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(specFile, []byte(enc(spec)), 0o600); err != nil {
		return err
	}
	servers["code"] = M{"command": exe(), "args": []any{"mcp", "code", specFile}, "tools": mcpToolNames(M{"tools": codeTools}), "env": []any{"HOME", "PATH"}}
	return nil
}
