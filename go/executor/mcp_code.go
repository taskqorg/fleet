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
	"encoding/json"
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

	"fleet/errtrack"
	"fleet/executor/supervise"
	"fleet/mcpserve"
)

//go:embed code_runtime.js
var codeRuntimeJS string

var codeTools = []any{
	M{"name": "code", "inputSchema": M{"type": "object", "properties": M{"source": M{"type": "string"}, "detachAfterSeconds": M{"type": "number"}}, "required": []any{"source"}, "additionalProperties": false}},
	M{"name": "poll", "description": "Wait for a detached code job: its final output and value (returned once), or its output so far after waitSeconds (default 25). Without jobId: list the unfinished and undelivered jobs. reset: true stops the runtime (for a job that never yields) and kills running sh commands: state is lost, and every running job fails once its bridged calls in flight end.",
		"inputSchema": M{"type": "object", "properties": M{"jobId": M{"type": "string"}, "waitSeconds": M{"type": "number"}, "reset": M{"type": "boolean"}}, "additionalProperties": false}},
}

// codeSpec is the code server's spec file: addCodeServer writes it, `executor mcp code` reads it. A field of another
// JSON type reads as its zero value.
type codeSpec struct {
	Dir          string             `json:"dir"`
	Read         []string           `json:"read"` // the runtime's read roots
	Write        bool               `json:"write"`
	Edit         bool               `json:"edit"`
	RunFile      string             `json:"runFile"`
	Connections  []json.RawMessage  `json:"connections"` // the bridged connection servers' specs
	Node         string             `json:"node"`
	Sandbox      string             `json:"sandbox"`
	Instructions []codeInstructions `json:"instructions"` // with codeMode only: the connections' own instructions
	Shell        *codeShell         `json:"shell"`        // nil: no sh
	Env          map[string]string  `json:"env"`          // FLEET_INPUT_DIR and FLEET_OUTPUT_DIR for sh
	GitEnv       *[]string          `json:"gitEnv"`
	Journal      string             `json:"journal"` // the executor journal, "" without Fleet tools
	Context      json.RawMessage    `json:"context"` // the fleet server context
}

type codeInstructions struct {
	Name         string `json:"name"`
	Instructions string `json:"instructions"`
}

// codeShell is the sh policy (codeAuthority.shell): unsandboxed, or an OS sandbox with these roots.
type codeShell struct {
	Sandbox  bool     `json:"sandbox"`
	Network  bool     `json:"network"`
	ReadOnly bool     `json:"readOnly"`
	Pinned   bool     `json:"pinned"` // the roots are canonical already: a changed one fails
	Hide     []string `json:"hide"`
	Read     []string `json:"read"`
	Write    []string `json:"write"`
}

// codeConn is one bridged connection: its server spec (as connectionCall and modelTool take it) and its tools.
type codeConn struct {
	name, instructions string
	limits             any
	spec               any
	tools              []codeConnTool
}

type codeConnTool struct {
	name string
	spec any
}

func codeConnOf(spec any) codeConn {
	var head struct {
		Name         string            `json:"name"`
		Instructions string            `json:"instructions"`
		Limits       json.RawMessage   `json:"limits"`
		Tools        []json.RawMessage `json:"tools"`
	}
	retype(spec, &head)
	c := codeConn{name: head.Name, instructions: head.Instructions, limits: decRaw(head.Limits), spec: spec}
	for _, raw := range head.Tools {
		var t struct {
			Name string `json:"name"`
		}
		json.Unmarshal(raw, &t)
		c.tools = append(c.tools, codeConnTool{t.Name, decRaw(raw)})
	}
	return c
}

// toolNames are the connection's tool names, as mcpToolNames lists them.
func (c codeConn) toolNames() []any {
	out := []any{}
	for _, t := range c.tools {
		out = append(out, t.name)
	}
	return out
}

// codeArgs are the arguments of the code and poll tools (codeTools).
type codeArgs struct {
	Source             string          `json:"source"`
	DetachAfterSeconds json.RawMessage `json:"detachAfterSeconds"`
	JobID              string          `json:"jobId"`
	WaitSeconds        json.RawMessage `json:"waitSeconds"`
	Reset              bool            `json:"reset"`
}

// codeLine is one protocol line of the runtime (code_runtime.js): print, call, done or settled.
type codeLine struct {
	Type   string          `json:"type"`
	Job    string          `json:"job"`
	Text   string          `json:"text"`
	ID     json.RawMessage `json:"id"`
	OK     bool            `json:"ok"`
	Value  json.RawMessage `json:"value"`
	Error  json.RawMessage `json:"error"`
	Server string          `json:"server"`
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
}

// key is the call id as the awaiting map holds it.
func (l codeLine) key() string { return enc(decRaw(l.ID)) }

// codeHostNames are the host functions this execution has (fleet-code-mode.md, host functions).
func codeHostNames(spec codeSpec) []string {
	var names []string
	if len(spec.Read) > 0 {
		names = append(names, "read", "glob", "grep")
	}
	if spec.Write {
		names = append(names, "write")
	}
	if spec.Edit {
		names = append(names, "edit")
	}
	if spec.Shell != nil {
		names = append(names, "sh")
	}
	return names
}

// codeDescription is the code tool's description for this execution: Claude Code's REPL rules, the functions it has, and
// an example built from them.
func codeDescription(spec codeSpec) string {
	host := codeHostNames(spec)
	can := func(n string) bool { return slices.Contains(host, n) }
	var b strings.Builder
	b.WriteString("Run JavaScript in this execution's persistent Node runtime: the body of an async function. The last expression is the result (or return one); return values themselves, not JSON.stringify(...).\n\n" +
		"Few, full scripts: do a step's investigating in as few code calls as the dependencies between its steps allow, reading and searching more widely than you strictly need, because each extra call is a round trip and each extra read inside one is nearly free. IMPORTANT: batch ALL operations of a step into ONE code call. Use Promise.all() for parallel operations. Only the result and print() output reach you: filter in code and return a compact object.\n\nFunctions (paths are relative to the working directory):\n")
	if can("read") {
		b.WriteString("- read(path) -> text; glob(pattern, {path}) -> paths, pattern like \"**/*.go\"; grep(regex, {path, glob, ignoreCase}) -> [{file, line, text}].\n")
	} else {
		b.WriteString("- No file reads.\n")
	}
	if can("write") {
		b.WriteString("- write(path, text) creates or replaces a file.\n")
	}
	if can("edit") {
		b.WriteString("- edit(path, old, new, {all}) replaces old, which must occur once unless all: true.\n")
	}
	if can("sh") {
		b.WriteString("- sh`cmd` or sh(cmd, {cwd, timeoutMs, args}) -> {code, stdout, stderr} with .text(), .json(), .lines(). For dynamic values pass args and reference them in the command as \"$1\", \"$2\", etc. A non-zero code does not throw.")
		if shell := spec.Shell; shell.Sandbox {
			if shell.ReadOnly {
				b.WriteString(" Commands write nothing")
			} else {
				b.WriteString(" Commands write only under the working directory and temporary directories")
			}
			if !shell.Network {
				b.WriteString(" and have no network")
			}
			b.WriteString(".")
		}
		b.WriteString("\n")
	}
	b.WriteString("- fleet.<tool>(args) calls a Fleet tool; conn[\"<name>\"].<tool>(args) a granted connection. fleet.$tools() and conn[\"<name>\"].$tools() return names and input schemas: use them only to look up what you do not know.\n" +
		"- A failed call throws FleetCallError {tool, outcome, code, output}; outcome unknown means it may have happened: inspect, never repeat.\n" +
		"- `state` keeps values between calls; let/const/var do not. print() and console.log add to the output.\n" +
		"- Still running after detachAfterSeconds (default 25): replies with a jobId for poll.\n\nExample (invented names):\n")
	var lines, fields []string
	if can("read") {
		lines = append(lines, `const [mod, readme] = await Promise.all([read("go.mod"), read("README.md")])`, `const todos = await grep("TODO", {glob: "**/*.go"})`)
		fields = append(fields, `module: mod.split("\n")[0]`, `readmeLines: readme.split("\n").length`, `todos: todos.slice(0, 5)`)
	}
	if can("sh") {
		lines = append(lines, "const log = await sh`git log --oneline -3`")
		fields = append(fields, "log: log.lines()")
	}
	if len(spec.Connections) > 0 {
		lines = append(lines, `const bugs = await conn["tracker"].search({query: "open bugs"})`)
		fields = append(fields, "bugs: bugs.map(b => b.id)")
	} else if spec.Journal != "" { // Fleet tools
		lines = append(lines, `const me = await fleet.execution_status({})`)
		fields = append(fields, "status: me.status")
	}
	b.WriteString(strings.Join(lines, "\n") + "\n({" + strings.Join(fields, ", ") + "})")
	return b.String()
}

// codeReply renders a job reply (fleet-code-mode.md, reply): the printed output, a finished job's value as itself (a
// string as is, other values as JSON), and a `[code] ` part only when it carries something. A reply that is no job's
// result is one `[code] ` part.
func codeReply(r codeResult) M {
	status := r.Status
	if status == "" {
		note := M{"jobId": r.JobID, "error": r.Error}
		if r.NewRuntime {
			note["newRuntime"] = true
		}
		return codeNote(note, true)
	}
	var parts []any
	add := func(t string) { parts = append(parts, M{"type": "text", "text": t}) }
	if r.Output != "" {
		add(r.Output)
	}
	meta := M{}
	if status == "done" {
		if v, ok := r.Value.(string); ok && v != "" {
			add(v)
		} else if ok {
			add(`""`) // an empty text part is refused by the Claude API
		} else {
			add(enc(r.Value))
		}
	} else {
		meta["status"] = status
		if status == "running" {
			meta["jobId"] = r.JobID
		}
		if status == "failed" {
			meta["error"] = r.Error
		}
	}
	if r.NewRuntime {
		meta["newRuntime"] = true
	}
	calls := r.Calls
	if status != "done" && len(calls) > 0 || slices.ContainsFunc(calls, func(c codeCall) bool { return c.Outcome != "ok" }) {
		meta["calls"] = calls
	}
	if len(meta) > 0 || len(parts) == 0 {
		add("[code] " + enc(meta))
	}
	reply := M{"content": parts, "isError": status == "failed"}
	if len(meta) > 0 {
		if status == "done" {
			// Claude Code prefers structuredContent and would drop the completed value.
			reply["_meta"] = M{"fleet/code": meta}
		} else {
			reply["structuredContent"] = meta
		}
	}
	return reply
}

func codeNote(v M, isError bool) M {
	return M{"content": []any{M{"type": "text", "text": "[code] " + enc(v)}}, "structuredContent": v, "isError": isError}
}

// codeResult is one reply of a job (codeServer.wait); Status "" is no job's result: an unknown jobId.
type codeResult struct {
	JobID, Status, Output string
	Calls                 []codeCall // the calls since the job's previous reply
	Value, Error          any        // a done job's value, a failed job's error, or the unknown jobId error
	NewRuntime            bool
}

// codeCall is one bridged call in a reply's calls; the fields are in the order of their JSON keys.
type codeCall struct {
	Ms      int64  `json:"ms"`
	Outcome string `json:"outcome"`
	Tool    string `json:"tool"`
}

type codeJob struct {
	id     string
	rt     *codeRuntime
	output strings.Builder // output not yet replied
	calls  []codeCall      // calls not yet replied
	busy   int             // bridged calls in flight
	ended  *[3]any         // the runtime's done line (ok, value, error) while calls are in flight
	done   chan struct{}
	ok     bool
	value  any
	err    any
}

type codeServer struct {
	spec   codeSpec
	ctx    any     // open JSON: the fleet server context, read by the delegation server's handlers
	db     *sql.DB // the executor journal, nil without Fleet tools
	fleet  map[string]bool
	conns  map[string]codeConn // connection name -> spec
	mu     sync.Mutex
	jobs   map[string]*codeJob
	order  []string
	rt     *codeRuntime
	seq    int
	closed bool                     // the server is shutting down: no new runtime
	procs  map[*supervise.Proc]bool // running sh commands, killed by reset and shutdown
	tmp    string                   // the private /tmp of Linux sh commands, removed at shutdown
}

type codeRuntime struct {
	awaiting map[string]*codeJob // replies sent, not yet settled by the runtime (under codeServer.mu)
	gone     bool                // its stdout ended (under codeServer.mu)
	stopped  bool                // a reset or shutdown stopped it: no new sh for its jobs (under codeServer.mu)
	tail     tailBuf             // the end of its stderr, for the runtime-exited reason
	proc     *supervise.Proc
	mu       sync.Mutex // serializes writes to stdin
	in       io.WriteCloser
	dead     chan struct{}
}

func codeMCP(file any, in io.Reader, w io.Writer) error {
	var spec codeSpec
	retype(file, &spec)
	s := &codeServer{spec: spec, jobs: map[string]*codeJob{}, fleet: map[string]bool{}, conns: map[string]codeConn{}, procs: map[*supervise.Proc]bool{}}
	// A bridged upload reads only what the sandbox may read; without read roots it reads nothing.
	uploadRoots = pinUploadRoots(append([]string{}, spec.Read...))
	if spec.Journal != "" {
		db, err := openJournal(spec.Journal)
		if err != nil {
			return err
		}
		defer db.Close()
		s.db, s.ctx = db, decRaw(spec.Context)
		for _, n := range mcpToolNames(mcpTools(s.ctx)) {
			name, _ := str(n)
			s.fleet[name] = true
		}
	}
	for _, raw := range spec.Connections {
		c := codeConnOf(decRaw(raw))
		s.conns[c.name] = c
		connectionLimits[c.name] = c.limits
	}
	if f := spec.RunFile; f != "" {
		if err := loadRunFile(f); err != nil {
			return err
		}
	}
	// On shutdown the runtime stops and the requests return; bridged calls still in flight end with the process. A journal
	// write is one SQLite transaction, so it is either recorded or not, and its outcome stays unknown to the agent as for
	// any lost reply.
	// Requests are served concurrently, so a long `code` call never delays a ping, a poll or a second call; only the
	// writes are serialized.
	defer func() { // last: the commands are killed by then
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.tmp != "" {
			os.RemoveAll(s.tmp)
		}
	}()
	// On stdin end the runtime stops first, so waiting requests return with runtime-exited instead of their full wait.
	closed := func() {
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
	}
	// With `only`, the direct connection servers are gone: their instructions (query syntax, limits) come from here.
	var instr []string
	for _, c := range s.spec.Instructions {
		if c.Instructions != "" {
			instr = append(instr, "conn[\""+c.Name+"\"]: "+c.Instructions)
		}
	}
	return mcpserve.Stdio{Name: "fleet-code", Version: "1", Instructions: strings.Join(instr, "\n\n"), Concurrent: true, Closed: closed,
		Tools: func() any {
			tools := jClone(codeTools).([]any)
			tools[0].(M)["description"] = codeDescription(s.spec)
			return tools
		},
		Call: func(name string, arguments json.RawMessage) (any, error) {
			var args codeArgs
			json.Unmarshal(arguments, &args)
			switch name {
			case "code":
				return s.code(args.Source, seconds(decRaw(args.DetachAfterSeconds))), nil
			case "poll":
				if args.Reset {
					s.stop()
					return codeNote(M{"reset": true}, false), nil
				}
				return s.poll(args.JobID, seconds(decRaw(args.WaitSeconds))), nil
			}
			return nil, errors.New("unknown tool")
		}}.Serve(in, w)
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

func (s *codeServer) code(source string, detach time.Duration) M {
	if strings.TrimSpace(source) == "" {
		return codeNote(M{"error": "source required"}, true)
	}
	s.mu.Lock()
	rt, started, err := s.runtime()
	if err != nil {
		s.mu.Unlock()
		return codeNote(M{"error": "runtime did not start: " + err.Error()}, true)
	}
	s.seq++
	j := &codeJob{id: fmt.Sprintf("job-%d", s.seq), rt: rt, done: make(chan struct{})}
	s.jobs[j.id] = j
	s.order = append(s.order, j.id)
	s.mu.Unlock()
	// The send may block while the runtime does not read (a job that never yields); the reply still comes at detach,
	// and poll reset ends the send.
	errtrack.Go("code.run", func() {
		if err := rt.send(M{"type": "run", "job": j.id, "source": source}); err != nil {
			s.finish(j, false, nil, runtimeExited())
		}
	})
	r := s.wait(j, detach)
	if started {
		r.NewRuntime = true
	}
	return codeReply(r)
}

func (s *codeServer) poll(id string, wait time.Duration) M {
	s.mu.Lock()
	if id == "" {
		list := []any{}
		for _, k := range s.order {
			list = append(list, M{"jobId": k, "status": jobStatus(s.jobs[k])})
		}
		s.mu.Unlock()
		return codeNote(M{"jobs": list}, false)
	}
	j := s.jobs[id]
	s.mu.Unlock()
	if j == nil {
		return codeNote(M{"error": "unknown jobId: never started or already returned"}, true)
	}
	return codeReply(s.wait(j, wait))
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
func (s *codeServer) wait(j *codeJob, d time.Duration) codeResult {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-j.done:
	case <-t.C:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jobs[j.id] != j {
		return codeResult{JobID: j.id, Error: "unknown jobId: never started or already returned"} // another wait returned it
	}
	// Each reply carries only the output and calls since the job's previous reply; the server keeps nothing replied.
	r := codeResult{JobID: j.id, Status: jobStatus(j), Output: j.output.String(), Calls: j.calls}
	j.output.Reset()
	j.calls = nil
	if r.Status == "running" {
		return r
	}
	if r.Status == "done" {
		r.Value = j.value
	} else {
		r.Error = j.err
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
	if j.rt != nil && j.rt.stopped {
		ok, value, err = false, nil, runtimeExited()
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
	if len(s.spec.Read) > 0 {
		cmd.Dir = s.spec.Dir
	}
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "NODE_NO_WARNINGS=1"}
	// The protocol has its own pipe (fd 3): a raw write to fd 1 (fs.writeSync(1, ...)) cannot corrupt a protocol line; it
	// goes to this server's stderr log.
	rt := &codeRuntime{dead: make(chan struct{}), awaiting: map[string]*codeJob{}}
	cmd.Stdout, cmd.Stderr = os.Stderr, io.MultiWriter(os.Stderr, &rt.tail)
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, false, err
	}
	stdout, proto, err := os.Pipe()
	if err != nil {
		in.Close()
		return nil, false, err
	}
	cmd.ExtraFiles = []*os.File{proto}
	// The runtime stays in the code server's group: the MCP guardian's group kill ends it (macOS has no --die-with-parent).
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd, SameGroup: true})
	proto.Close()
	if err != nil {
		stdout.Close()
		in.Close()
		return nil, false, err
	}
	rt.proc, rt.in = proc, in
	s.rt = rt
	errtrack.Go("code.read", func() { s.read(rt, stdout) })
	// The granted names become real properties of `fleet` and `conn`: a typo or a probe (`then`, `toJSON`) is undefined
	// in the runtime, not a call.
	names := M{}
	for n, c := range s.conns {
		names[n] = c.toolNames()
	}
	// The tools line goes first: rt.mu is taken here and released after the write, so every later send (run lines,
	// replies) waits for it, and s.mu is not held while a slow runtime starts reading.
	tools := enc(M{"type": "tools", "fleet": anyStrings(sortedKeysOf(s.fleet)), "conns": names, "host": anyStrings(codeHostNames(s.spec)), "dir": s.spec.Dir}) + "\n"
	rt.mu.Lock()
	errtrack.Go("code.tools", func() {
		defer rt.mu.Unlock()
		io.WriteString(rt.in, tools)
	})
	return rt, true, nil
}

// codeArgv is the runtime command: Node with its permission model (no writes, child processes, workers or addons; reads
// only the declared roots) inside an OS sandbox without network.
func codeArgv(spec codeSpec) ([]string, error) {
	node := spec.Node
	if node == "" {
		return nil, errors.New("node missing from the code server spec")
	}
	args := []string{node, "--permission"}
	for _, p := range spec.Read {
		if p != "" {
			args = append(args, "--allow-fs-read="+p)
		}
	}
	args = append(args, "-e", codeRuntimeJS)
	return sandboxArgv(spec.Sandbox, spec.Read, args)
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
func sandboxArgv(sandbox string, read []string, args []string) ([]string, error) {
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
		for _, p := range read {
			if p != "" {
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
	if rt != nil {
		rt.stopped = true
	}
	for p := range s.procs {
		p.Kill()
	}
	s.mu.Unlock()
	if rt != nil {
		rt.in.Close()
		rt.proc.Kill()
		<-rt.dead
	}
}

// read handles the runtime's lines until its stdout ends or fails; then it stops the process, and every unfinished job of
// this runtime ends with runtime-exited. Lines have no size cap: the runtime never cuts output.
func (s *codeServer) read(rt *codeRuntime, stdout io.ReadCloser) {
	defer stdout.Close()
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
	rt.proc.Kill()
	rt.proc.Wait()
	exited := runtimeExited()
	if t := strings.TrimSpace(rt.tail.String()); t != "" {
		exited["stderrTail"] = t // the end of the runtime's stderr: why it stopped, such as a sandbox that cannot start
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
	// Replies the dead runtime will never settle.
	var unsettled []*codeJob
	for k, j := range rt.awaiting {
		unsettled = append(unsettled, j)
		delete(rt.awaiting, k)
	}
	s.mu.Unlock()
	for _, j := range open {
		s.finish(j, false, nil, exited)
	}
	for _, j := range unsettled {
		s.settle(j)
	}
	close(rt.dead)
}

func (s *codeServer) line(rt *codeRuntime, line []byte) {
	var m codeLine
	if !json.Valid(line) {
		// Only user code writing to fd 3 breaks a line; every running job of this runtime says so, since its own done or
		// call line may be the one lost.
		s.mu.Lock()
		for _, j := range s.jobs {
			if j.rt == rt && jobStatus(j) == "running" {
				j.output.WriteString("[code server: dropped a protocol line that does not decode; fd 3 is the runtime's own pipe]\n")
			}
		}
		s.mu.Unlock()
		return
	}
	json.Unmarshal(line, &m) // a field of another type reads as its zero value
	if m.Type == "print" {
		s.mu.Lock() // one lock for lookup and write: a concurrent final reply cannot orphan the text
		if j := s.jobs[m.Job]; j != nil {
			j.output.WriteString(m.Text)
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		fmt.Fprint(os.Stderr, m.Text)
		return
	}
	s.mu.Lock()
	j := s.jobs[m.Job]
	s.mu.Unlock()
	switch m.Type {
	case "settled":
		key := m.key()
		s.mu.Lock()
		sj, ok := rt.awaiting[key]
		delete(rt.awaiting, key)
		s.mu.Unlock()
		if ok {
			s.settle(sj)
		}
	case "done":
		if j != nil {
			// A call started before the return (unawaited) is still part of the job: the final reply waits for it, so
			// its outcome is in `calls`.
			s.mu.Lock()
			end := [3]any{m.OK, decRaw(m.Value), decRaw(m.Error)}
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
		// After the code returned, a call is still part of the job while another of its calls is in flight (a chain such as
		// fleet.a().then(fleet.b)); once none is, a call (a timer) is late.
		ended := j == nil || rt.stopped || jobStatus(j) != "running" || j.ended != nil && j.busy == 0
		if !ended {
			j.busy++
		}
		s.mu.Unlock()
		if ended {
			// A call made after its job ended (a timer) would have an effect nobody sees.
			fail := callError("error", "job-ended", nil)
			fail["tool"] = callLabel(m.Server, m.Tool)
			errtrack.Go("code.reply", func() { rt.send(M{"type": "reply", "id": decRaw(m.ID), "ok": false, "error": fail}) })
			return
		}
		errtrack.Go("code.call", func() { s.call(rt, j, m) })
	}
}

// call serves one bridge call with the same handler as the direct server and answers the runtime.
func (s *codeServer) call(rt *codeRuntime, j *codeJob, m codeLine) {
	start := time.Now()
	server, tool, args := m.Server, m.Tool, decRaw(m.Args)
	value, fail := s.bridge(rt, server, tool, args)
	label := callLabel(server, tool)
	outcome := "ok"
	if fail != nil {
		outcome = jStr(fail, "outcome")
	}
	reply := M{"type": "reply", "id": decRaw(m.ID), "ok": fail == nil, "value": value}
	if fail != nil {
		fail["tool"] = label
		reply["error"] = fail
	}
	// The call counts as busy until the runtime settles it: its `settled` line comes after the reply's promise callbacks
	// ran, so a call chained off this reply (fleet.a().then(fleet.b)) arrives while this one still counts.
	key := m.key()
	s.mu.Lock()
	j.calls = append(j.calls, codeCall{Tool: label, Ms: time.Since(start).Milliseconds(), Outcome: outcome}) // done at the provider: listed now
	gone := rt.gone
	if !gone {
		rt.awaiting[key] = j
	}
	s.mu.Unlock()
	if gone || rt.send(reply) != nil {
		s.mu.Lock()
		delete(rt.awaiting, key)
		s.mu.Unlock()
		s.settle(j)
	}
}

// settle ends one bridged call of j; the job's final reply waits until none is left.
func (s *codeServer) settle(j *codeJob) {
	s.mu.Lock()
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

func (s *codeServer) bridge(rt *codeRuntime, server, tool string, args any) (any, M) {
	if !truth(args) {
		args = M{}
	}
	if server == "host" {
		if !slices.Contains(codeHostNames(s.spec), tool) {
			return nil, callError("error", "tool-not-granted", nil)
		}
		switch tool {
		case "sh":
			return s.sh(rt, args)
		case "write", "edit":
			return codeFile(s.spec.Dir, tool, args)
		}
		return nil, callError("error", "tool-not-granted", nil) // read, glob and grep run in the runtime
	}
	if name, ok := strings.CutPrefix(server, "conn:"); ok {
		conn, granted := s.conns[name]
		if !granted {
			return nil, callError("error", "connection-not-granted", nil)
		}
		if tool == "$tools" {
			listed := []any{}
			for _, t := range conn.tools {
				listed = append(listed, modelTool(t.spec))
			}
			return listed, nil
		}
		i := slices.IndexFunc(conn.tools, func(t codeConnTool) bool { return t.name == tool })
		if i < 0 {
			return nil, callError("error", "tool-not-granted", nil)
		}
		line := connectionCall(name, conn.tools[i].spec, args)
		var output any
		if raw := callErrorOutput(line); len(raw) > 0 {
			output, _ = dec(string(raw))
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
		return mcpTools(s.ctx)["tools"], nil
	}
	if !s.fleet[tool] {
		return nil, callError("error", "tool-not-granted", nil)
	}
	r, err := delegationCall(tool, args, s.ctx, s.db)
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
	var result struct {
		Content looseList `json:"content"`
		IsError bool      `json:"isError"`
	}
	retype(r, &result)
	if !result.Content.IsArray {
		return r, nil
	}
	type part struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	parts := make([]part, len(result.Content.Items))
	for i, c := range result.Content.Items {
		json.Unmarshal(c, &parts[i])
	}
	var v any
	if slices.ContainsFunc(parts, func(p part) bool { return p.Type != "text" }) {
		v = decRaw(json.RawMessage(enc(result.Content.Items)))
	} else {
		var texts []string
		for _, p := range parts {
			texts = append(texts, p.Text)
		}
		text := strings.Join(texts, "\n")
		var err error
		if v, err = dec(text); err != nil {
			v = text
		}
	}
	// open JSON: v is the tool's own reply; only its status and response are read.
	var reply struct {
		Status   string          `json:"status"`
		Response json.RawMessage `json:"response"`
	}
	retype(v, &reply)
	if reply.Status == "pending-or-rejected" {
		if decRaw(reply.Response) == nil {
			// request_child without a host answer: unknown; a retry with the same requestId is the same request.
			return nil, callError("unknown", "pending", v)
		}
		return nil, callError("error", "tool-error", v) // the host answered and refused it
	}
	if result.IsError {
		if reply.Status == "pending" {
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
	var background bool
	return func() (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case at.IsZero() || err == nil && !(isFile(node) && isFile(sandbox)):
			// No result yet, or a ready one whose files are gone: probe now. Concurrent callers wait for this result.
			node, sandbox, err = probeCodeHost()
			at = time.Now()
		case err != nil && time.Since(at) >= time.Minute && !background:
			background = true
			errtrack.Go("code.probe", func() {
				n, sb, e := probeCodeHost()
				mu.Lock()
				node, sandbox, err, at, background = n, sb, e, time.Now(), false
				mu.Unlock()
			})
		}
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

// removeRunFiles removes an execution's private files under runs/: its run token file and its code server spec.
func removeRunFiles(root, execution string) {
	os.Remove(filepath.Join(root, "runs", execution+".env"))
	os.Remove(codeSpecFile(root, execution))
	os.Remove(fleetContextFile(root, execution))
}

// fleetContextFile is the Fleet MCP server's context for one execution: private to the executor (runs/ is 0700, outside
// every agent's working directory), removed with the run file.
func fleetContextFile(root, execution string) string {
	p, err := filepath.Abs(filepath.Join(root, "runs", execution+".fleet.json"))
	if err != nil {
		return filepath.Join(root, "runs", execution+".fleet.json")
	}
	return p
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

// codeAuthority is what the host functions may do in one execution (fleet-code-mode.md, authority per harness): the
// runtime's read roots, write and edit, and the sh policy (nil: no sh) {sandbox, write roots, network}.
type codeAuthority struct {
	read        []string
	write, edit bool
	shell       M
	env         M // FLEET_INPUT_DIR and FLEET_OUTPUT_DIR for sh, as the harness has them
	gitEnv      []any
}

// codeAuthorityFor mirrors the harness's own tools for this execution, never more. work is the worktree; writable are
// Codex's extra writable roots and network its sandbox network (gitNetwork); readOnly is a read-only start
// (StartPayload.readOnly): Codex's read-only sandbox and Claude's Read, Glob and Grep. ACP denies client file
// and terminal operations, so its code mode keeps only the existing read roots. A sandboxed sh on Linux sees
// an empty executor data directory (root: its MCP sockets) except the read roots.
func codeAuthorityFor(harness string, profile any, work string, read []string, writable []any, network, readOnly bool, root string) codeAuthority {
	a := codeAuthority{read: slices.DeleteFunc(slices.Clone(read), func(r string) bool { return r == "" })}
	sandboxed := func(extra []any, net bool) M {
		m := M{"sandbox": true, "write": append([]any{work}, extra...), "network": net, "read": anyStrings(a.read)}
		if root != "" {
			m["hide"] = []any{root}
		}
		return m
	}
	switch harness {
	case "claude":
		native, _ := claudeNativeTools(profile)
		if readOnly {
			native = readOnlyTools(native)
		}
		granted := func(t string) bool { return slices.Contains(native, t) }
		if !granted("Read") && !granted("Grep") && !granted("Glob") && !granted("Bash") {
			a.read = nil
		}
		a.write, a.edit = granted("Write"), granted("Edit")
		if granted("Bash") {
			a.shell = M{"sandbox": false} // Fleet runs Claude's own Bash without a sandbox
		}
	case "codex":
		if readOnly { // Codex's read-only sandbox: commands run, nothing is written, no network
			a.shell = sandboxed(nil, false)
			a.shell["readOnly"] = true
			return a
		}
		a.write, a.edit, a.shell = true, true, sandboxed(writable, network)
	}
	return a
}

// codeCovered are the native Claude tools that host functions cover: `codeMode: only` hides them. Read stays: it is
// the only way the agent sees an attached image or PDF, which read() returns as text.
var codeCovered = []string{"Bash", "Write", "Edit", "Glob", "Grep"}

// codeVisible is the Claude tool grant without the covered tools when the profile's code mode is `only`.
func codeVisible(tools []string, codeMode string) []string {
	if codeMode != "only" {
		return tools
	}
	return slices.DeleteFunc(slices.Clone(tools), func(t string) bool { return slices.Contains(codeCovered, t) })
}

// addCodeServer adds the `code` server to a launch's servers; mode `only` removes the fleet and connection servers it
// bridges.
// The spec goes to specFile (0600), not argv: fleet operations and connection schemas can exceed one argv string.
// conns are the launch's connection servers' specs, with the tools those servers expose.
func addCodeServer(servers M, mode string, fleet *codeFleet, conns []any, runFile, specFile, dir string, auth codeAuthority) error {
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
	spec := M{"dir": dir, "read": anyStrings(auth.read), "write": auth.write, "edit": auth.edit, "runFile": runFile, "connections": conns, "node": node, "sandbox": sandbox, "instructions": []any{}}
	if auth.shell != nil {
		spec["shell"], spec["env"] = auth.shell, auth.env
		if auth.gitEnv != nil {
			spec["gitEnv"] = auth.gitEnv
		}
	}
	if mode == "only" {
		instr := []any{}
		for _, c := range conns {
			conn := codeConnOf(c)
			instr = append(instr, M{"name": conn.name, "instructions": conn.instructions})
		}
		spec["instructions"] = instr
	}
	if fleet != nil {
		spec["journal"], spec["context"] = fleet.journal, fleet.context
	}
	if mode == "only" {
		delete(servers, "fleet")
		for _, c := range conns {
			delete(servers, codeConnOf(c).name)
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
