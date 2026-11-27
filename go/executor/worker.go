package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"fleet/contract"
	"fleet/errtrack"
	"fleet/executor/supervise"
)

var releaseVerRe = regexp.MustCompile(`^[1-9][0-9]*\.[0-9]{6}\.(0|[1-9][0-9]*)$`)

type workerSpec struct {
	Kind         string          `json:"kind"` // "harness" or "script"
	Execution    string          `json:"execution"`
	CommandId    string          `json:"commandId"`
	Session      string          `json:"session"`
	DataDir      string          `json:"dataDir"`
	Dir          string          `json:"dir"`
	Work         string          `json:"work"`
	Root         string          `json:"root"`
	InputDir     string          `json:"inputDir"`
	Harness      string          `json:"harness"`
	ACP          bool            `json:"acp"`
	Native       string          `json:"native"`
	NativeInput  string          `json:"nativeInput,omitempty"`
	CmdPath      string          `json:"cmdPath"`
	CmdArgs      []string        `json:"cmdArgs"`
	CmdEnv       []string        `json:"cmdEnv"`
	CmdDir       string          `json:"cmdDir"`
	Profile      M               `json:"profile,omitempty"`
	Servers      M               `json:"servers,omitempty"`
	Extra        []string        `json:"extra,omitempty"`
	GitRoots     []any           `json:"gitRoots,omitempty"`
	GatewayConns map[string]bool `json:"gatewayConns,omitempty"`
	UseMCP       bool            `json:"useMcp"`
	TimeoutMs    *int64          `json:"timeoutMs,omitempty"`
	Trial        *[2]string      `json:"trial,omitempty"`
	InitialSeq   uint64          `json:"initialSeq"`
}

type workerStartReply struct {
	Ok         bool           `json:"ok"`
	FailReason string         `json:"failReason,omitempty"`
	Unknown    bool           `json:"unknown,omitempty"`
	Pid        int            `json:"pid,omitempty"`
	Grp        int            `json:"grp,omitempty"`
	Owned      map[int]string `json:"owned,omitempty"`
	MCPDir     string         `json:"mcpDir,omitempty"`
}

type workerRecord struct {
	Seq       uint64          `json:"seq"`
	Kind      string          `json:"kind"` // "owned", "mcp_group", "native", "native_event", "fleet_event", "steer_result", "stopped"
	EventSeq  uint64          `json:"eventSeq,omitempty"`
	EventType string          `json:"eventType,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Native    string          `json:"native,omitempty"`
	Owned     map[int]string  `json:"owned,omitempty"`
	Pid       int             `json:"pid,omitempty"`
	Identity  string          `json:"identity,omitempty"`
	CommandId string          `json:"commandId,omitempty"`
	Ok        bool            `json:"ok,omitempty"`
}

type workerControlReq struct {
	Action    string `json:"action"` // "steer", "stop", "ping"
	CommandId string `json:"commandId,omitempty"`
	Text      string `json:"text,omitempty"`
}

type workerControlResp struct {
	Ok       bool   `json:"ok"`
	Accepted bool   `json:"accepted,omitempty"`
	Error    string `json:"error,omitempty"`
}

type workerExit struct {
	Status         string                            `json:"status"`
	ExitCode       *int64                            `json:"exitCode,omitempty"`
	StdoutClosed   bool                              `json:"stdoutClosed"`
	CleanupUnknown bool                              `json:"cleanupUnknown"`
	TimedOut       bool                              `json:"timedOut"`
	Stopped        bool                              `json:"stopped"`
	Completed      bool                              `json:"completed"`
	Succeeded      bool                              `json:"succeeded"`
	Native         string                            `json:"native,omitempty"`
	Seq            uint64                            `json:"seq"`
	Orphans        int                               `json:"orphans"`
	MCPDir         string                            `json:"mcpDir,omitempty"`
	StartedAt      time.Time                         `json:"startedAt"`
	CPUSeconds     float64                           `json:"cpuSeconds"`
	HasCPU         bool                              `json:"hasCpu"`
	PeakRSS        uint64                            `json:"peakRss"`
	Resources      *contract.ExecutionResourceSummary `json:"resources,omitempty"`
	StderrTail     *string                           `json:"stderrTail,omitempty"`
}

type workerMCPStore struct {
	mu         sync.Mutex
	root       int
	rootID     string
	fenced     bool
	groupsFile string
	groups     map[int]string
	onGroup    func(pid int, id string)
}

func (s *workerMCPStore) rootState() (int, string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root, s.rootID, s.fenced
}

func (s *workerMCPStore) setRoot(pid int, id string) {
	s.mu.Lock()
	s.root, s.rootID = pid, id
	s.mu.Unlock()
}

func (s *workerMCPStore) fence() {
	s.mu.Lock()
	s.fenced = true
	s.mu.Unlock()
}

func (s *workerMCPStore) snapshot() map[int]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[int]string, len(s.groups))
	for k, v := range s.groups {
		out[k] = v
	}
	return out
}

func (s *workerMCPStore) register(pid int, id string, verify func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fenced || s.root == 0 {
		return errors.New("MCP execution launch is not open")
	}
	if err := verify(); err != nil {
		return err
	}
	if prev, ok := s.groups[pid]; ok {
		if prev != id {
			return errors.New("MCP group registration conflicts with retained identity")
		}
		return nil
	}
	if err := appendJSONLine(s.groupsFile, M{"pid": pid, "identity": id}); err != nil {
		return err
	}
	s.groups[pid] = id
	if err := verify(); err != nil {
		return err
	}
	if s.onGroup != nil {
		s.onGroup(pid, id)
	}
	return nil
}

func appendJSONLine(path string, v any) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(enc(v) + "\n"); err != nil {
		return err
	}
	return f.Sync()
}

// acquireReleasePin holds LOCK_SH on <dataDir>/active-releases/<ver>.lock so fleet-update keeps this worker's
// release directory and its managed harness/tool pins until the worker exits.
func acquireReleasePin(dataDir string) *os.File {
	if dataDir == "" {
		return nil
	}
	self, err := os.Executable()
	if err != nil {
		return nil
	}
	resolved, err := filepath.EvalSymlinks(self)
	if err != nil {
		return nil
	}
	ver := filepath.Base(filepath.Dir(resolved))
	if !releaseVerRe.MatchString(ver) {
		return nil
	}
	dir := filepath.Join(dataDir, "active-releases")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(dir, ver+".lock"), os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_SH); err != nil {
		f.Close()
		return nil
	}
	return f
}

func workerDirFor(root, execution string) string {
	return filepath.Join(root, "workers", execution)
}

func workerSockPath(workerDir string) (sockPath string, cleanupDir string, err error) {
	candidate := filepath.Join(workerDir, "worker.sock")
	if len(candidate) <= 95 {
		return candidate, "", nil
	}
	dir, err := os.MkdirTemp("", "fw-")
	if err != nil {
		return "", "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	sockPath = filepath.Join(dir, "s")
	if err := os.WriteFile(filepath.Join(workerDir, "sock.path"), []byte(sockPath), 0o600); err != nil {
		os.RemoveAll(dir)
		return "", "", err
	}
	return sockPath, dir, nil
}

func readWorkerSockPath(workerDir string) string {
	if b, err := os.ReadFile(filepath.Join(workerDir, "sock.path")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	return filepath.Join(workerDir, "worker.sock")
}

func removeWorkerDir(root, execution string) {
	dir := workerDirFor(root, execution)
	if b, err := os.ReadFile(filepath.Join(dir, "sock.path")); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			sockDir := filepath.Dir(s)
			if strings.HasPrefix(filepath.Base(sockDir), "fw-") {
				_ = os.RemoveAll(sockDir)
			}
		}
	}
	_ = os.RemoveAll(dir)
}

type workerLogWriter struct {
	mu   sync.Mutex
	f    *os.File
	seq  uint64
}

func (w *workerLogWriter) emit(rec workerRecord) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.seq++
	rec.Seq = w.seq
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := w.f.Write(append(b, '\n')); err != nil {
		return err
	}
	return w.f.Sync()
}

func runWorker(args []string) error {
	if len(args) != 1 {
		return errors.New("run-worker requires worker directory")
	}
	workerDir := args[0]
	ready := os.NewFile(3, "worker-ready")
	if ready == nil {
		return errors.New("worker ready pipe missing")
	}
	unix.CloseOnExec(3)
	replyOnce := sync.Once{}
	sendReply := func(r workerStartReply) {
		replyOnce.Do(func() {
			_, _ = ready.WriteString(enc(r) + "\n")
			_ = ready.Close()
		})
	}
	defer sendReply(workerStartReply{Ok: false, FailReason: "worker exited before starting"})

	specBytes, err := os.ReadFile(filepath.Join(workerDir, "spec.json"))
	if err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}
	var spec workerSpec
	if err := json.Unmarshal(specBytes, &spec); err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}
	if err := useManagedTools(); err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}
	if pin := acquireReleasePin(spec.DataDir); pin != nil {
		defer pin.Close()
	}
	wlock, err := os.OpenFile(filepath.Join(workerDir, "worker.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}
	defer wlock.Close()
	if err := unix.Flock(int(wlock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: "worker lock held: " + err.Error()})
		return err
	}

	evFile, err := os.OpenFile(filepath.Join(workerDir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}
	defer evFile.Close()
	lw := &workerLogWriter{f: evFile}

	sockPath, cleanupSockDir, err := workerSockPath(workerDir)
	if err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}
	if cleanupSockDir != "" {
		defer os.RemoveAll(cleanupSockDir)
	}
	_ = os.Remove(sockPath)
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: sockPath, Net: "unix"})
	if err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}
	defer listener.Close()
	if err := os.Chmod(sockPath, 0o600); err != nil {
		sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
		return err
	}

	var mcp *mcpOwner
	var mcpStore *workerMCPStore
	if spec.UseMCP || len(spec.Servers) > 0 {
		mcpStore = &workerMCPStore{
			groupsFile: filepath.Join(workerDir, "mcp_groups.jsonl"),
			groups:     map[int]string{},
			onGroup: func(pid int, id string) {
				_ = lw.emit(workerRecord{Kind: "mcp_group", Pid: pid, Identity: id})
			},
		}
		mcp, err = newLocalMCPOwner(mcpStore, spec.Execution)
		if err != nil {
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
		defer func() {
			_ = mcp.close()
			removeMCPOwnerDir(mcp.dir)
		}()
	}

	cmd := &exec.Cmd{Path: spec.CmdPath, Args: append([]string(nil), spec.CmdArgs...), Env: append([]string(nil), spec.CmdEnv...), Dir: spec.CmdDir}
	var sess Session
	if spec.Kind == "harness" {
		servers := spec.Servers
		if servers == nil {
			servers = M{}
		}
		if mcp != nil {
			receiptRoot, err := canon(spec.Dir)
			if err != nil {
				sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
				return err
			}
			servers, err = wrapMCPServers(servers, mcp.socket, spec.Work, filepath.Join(receiptRoot, spec.Execution+".mcp"))
			if err != nil {
				sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
				return err
			}
		}
		h := Harnesses[spec.Harness]
		if h == nil {
			err := errors.New("unknown harness")
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
		h.WireMCP(cmd, spec.Profile, servers, spec.Extra)
		pp := spec.Profile
		if spec.Harness == "codex" {
			pp, err = codexLaunchProfile(cmd, spec.Dir, spec.Profile, spec.GitRoots)
			if err != nil {
				sendReply(workerStartReply{Ok: false, FailReason: "Codex Go cache: " + err.Error()})
				return err
			}
		}
		cmd, sess = h.Protocol(cmd, spec.Root, spec.Native, pp, servers)
		if cs, ok := sess.(*codexSession); ok {
			cs.marks = filepath.Join(spec.DataDir, "codex-instructions")
		}
		if as, ok := sess.(*acpSession); ok {
			as.readRoots = []string{spec.InputDir}
		}
	} else {
		if mcp != nil {
			cmd.Env = append(cmd.Env, "FLEET_MCP_OWNER="+mcp.socket)
		}
	}

	protocol := sess != nil
	var stdin io.WriteCloser
	if spec.NativeInput != "" || protocol {
		if stdin, err = cmd.StdinPipe(); err != nil {
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
	}

	var stderr, stdoutFile, pr, pw, rawLog, outLog *os.File
	if spec.Kind == "harness" {
		if stderr, err = os.Create(filepath.Join(spec.Dir, spec.Execution+".stderr")); err != nil {
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
		defer stderr.Close()
		cmd.Stderr = stderr
		if pr, pw, err = os.Pipe(); err != nil {
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
		cmd.Stdout = pw
		if rawLog, err = os.Create(filepath.Join(spec.Dir, spec.Execution+".native.jsonl")); err != nil {
			pr.Close()
			pw.Close()
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
		if protocol {
			if outLog, err = os.OpenFile(filepath.Join(spec.Dir, spec.Execution+".protocol-out.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
				pr.Close()
				pw.Close()
				rawLog.Close()
				sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
				return err
			}
		}
	} else {
		output := filepath.Join(spec.Dir, "output")
		if stdoutFile, err = os.Create(filepath.Join(output, "script.stdout")); err != nil {
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
		defer stdoutFile.Close()
		if stderr, err = os.Create(filepath.Join(output, "script.stderr")); err != nil {
			sendReply(workerStartReply{Ok: false, FailReason: err.Error()})
			return err
		}
		defer stderr.Close()
		cmd.Stdout, cmd.Stderr = stdoutFile, stderr
	}
	closePipeSide := func() {
		if pr != nil {
			pr.Close()
		}
		if rawLog != nil {
			rawLog.Close()
		}
		if outLog != nil {
			outLog.Close()
		}
	}

	if err := markRun(cmd, spec.DataDir, spec.Execution); err != nil {
		if pw != nil {
			pw.Close()
		}
		closePipeSide()
		sendReply(workerStartReply{Ok: false, FailReason: "run temporary directory: " + err.Error()})
		return err
	}

	var ident string
	var identErr error
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd, KillLeftovers: true, Started: func(pid int) { ident, identErr = identity(pid) }})
	if err != nil {
		if pw != nil {
			pw.Close()
		}
		closePipeSide()
		prefix := "harness did not start: "
		if spec.Kind == "script" {
			prefix = "script spawn failed: "
		}
		sendReply(workerStartReply{Ok: false, FailReason: prefix + err.Error()})
		return err
	}
	if pw != nil {
		pw.Close()
	}

	l := &live{
		mcp:       mcp,
		cmd:       cmd,
		proc:      proc,
		exited:    proc.Done(),
		native:    spec.Native,
		seq:       spec.InitialSeq,
		completed: spec.Kind == "script",
		owned:     map[int]string{},
		journaled: map[int]bool{},
		metrics:   newRunMetrics(spec.GatewayConns),
		started:   time.Now(),
		timeoutMs: spec.TimeoutMs,
	}
	if spec.Kind == "harness" && NewBlocks != nil {
		l.blocks = NewBlocks()
	}
	if spec.TimeoutMs != nil && *spec.TimeoutMs > 0 {
		l.deadline = time.Now().Add(time.Duration(*spec.TimeoutMs) * time.Millisecond)
	}
	killFail := func() {
		proc.Kill()
		proc.Wait()
		closePipeSide()
	}
	if spec.NativeInput != "" && !protocol {
		_, werr := stdin.Write([]byte(spec.NativeInput))
		stdin.Close()
		if werr != nil {
			killFail()
			sendReply(workerStartReply{Ok: false, Unknown: true})
			return werr
		}
	}
	if identErr != nil {
		killFail()
		sendReply(workerStartReply{Ok: false, Unknown: true})
		return identErr
	}
	l.owned[proc.Pid] = ident
	_ = l.observe()
	ownedFile := filepath.Join(workerDir, "owned.jsonl")
	for pid, id := range l.owned {
		if err := appendJSONLine(ownedFile, M{"pid": pid, "identity": id}); err != nil {
			killFail()
			sendReply(workerStartReply{Ok: false, Unknown: true})
			return err
		}
		l.journaled[pid] = true
	}
	if mcpStore != nil {
		mcpStore.setRoot(proc.Pid, ident)
	}
	if mcp != nil {
		mcp.readyOnce.Do(func() { close(mcp.ready) })
	}

	lines := make(chan string, 1024)
	l.lines = lines
	if protocol {
		l.wire = &wire{session: sess, stdin: stdin, log: outLog}
		meta, err := writeFrame(stdin, outLog, sess.Initialize())
		if err != nil {
			killFail()
			sendReply(workerStartReply{Ok: false, Unknown: true})
			return err
		}
		lines <- enc(meta)
	}
	if spec.Kind == "harness" {
		w := l.wire
		acpProto := spec.ACP
		errtrack.Go("worker.stdout", func() { readStdout(pr, rawLog, w, acpProto, lines) })
	} else {
		close(lines)
	}

	mcpDir := ""
	if mcp != nil {
		mcpDir = mcp.dir
	}
	sendReply(workerStartReply{Ok: true, Pid: proc.Pid, Grp: proc.Pid, Owned: l.owned, MCPDir: mcpDir})

	var mu sync.Mutex
	var closedSock atomic.Bool
	killOwned := func() error {
		proc.Kill() // the supervisor fences group signals against the leader's reap
		return l.cleanup()
	}
	errtrack.Go("worker.sock", func() {
		for {
			c, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			if closedSock.Load() {
				c.Close()
				return
			}
			errtrack.Go("worker.conn", func() {
				defer c.Close()
				if uid, err := peerUID(c); err != nil || uid != os.Geteuid() {
					return
				}
				var req workerControlReq
				if json.NewDecoder(c).Decode(&req) != nil {
					return
				}
				var resp workerControlResp
				switch req.Action {
				case "ping":
					resp.Ok = true
				case "stop":
					mu.Lock()
					l.stopped = true
					if killOwned() == nil {
						resp.Ok, resp.Accepted = true, true
					} else {
						l.cleanupUnknown = true
					}
					mu.Unlock()
					if resp.Accepted {
						_ = lw.emit(workerRecord{Kind: "stopped", CommandId: req.CommandId, Ok: true})
					}
				case "steer":
					sent := false
					if l.wire != nil {
						l.wire.mu.Lock()
						if l.wire.session != nil && l.wire.stdin != nil && req.Text != "" {
							if frame := l.wire.session.Steer(req.Text, req.CommandId); frame != nil {
								_, serr := writeFrame(l.wire.stdin, l.wire.log, frame)
								sent = serr == nil
							}
						}
						l.wire.mu.Unlock()
					}
					resp.Ok = sent
				}
				_ = json.NewEncoder(c).Encode(resp)
			})
		}
	})

	recordOwned := func() {
		mu.Lock()
		defer mu.Unlock()
		if l.observe() != nil {
			l.cleanupUnknown = true
		}
		added := map[int]string{}
		for pid, id := range l.owned {
			if l.journaled[pid] {
				continue
			}
			_ = appendJSONLine(ownedFile, M{"pid": pid, "identity": id})
			l.journaled[pid] = true
			added[pid] = id
		}
		if len(added) > 0 {
			_ = lw.emit(workerRecord{Kind: "owned", Owned: added})
		}
	}

	handleLine := func(line string) bool {
		v, derr := dec(line)
		event, isObj := v.(M)
		if derr != nil || !isObj {
			event = M{"type": "unparsed", "raw": line}
		}
		if h := Harnesses[spec.Harness]; h != nil {
			event = h.Normalize(event)
		}
		atMs := nowMs()
		if l.metrics != nil {
			l.metrics.observeEvent(spec.Harness, event, atMs)
		}
		projection := M{}
		if l.blocks != nil {
			projection = l.blocks.Project(spec.Harness, event, uint64(atMs))
		}
		n, _ := projection["session"].(string)
		if n != "" && (!l.nativeObserved || n != l.native) {
			l.native, l.nativeObserved = n, true
			_ = lw.emit(workerRecord{Kind: "native", Native: n})
		}
		kind := "unknown"
		ev := typed[harnessEvent](event)
		if k, ok := valStr(ev.Type); ok {
			kind = k
		}
		if kind == "steer.result" {
			steer := typed[steerResult](event)
			_ = lw.emit(workerRecord{Kind: "steer_result", CommandId: str0(steer.CommandID), Ok: steer.Ok != nil && *steer.Ok})
		}
		mu.Lock()
		if kind == string(contract.FleetEventTypeProtocolFailed) && !l.stopped {
			l.cleanupUnknown, l.stopped = true, true
		}
		if projection["completed"] == true {
			l.completed = true
		}
		mu.Unlock()
		l.seq++
		retained := maps.Clone(event)
		retained["fleetNormalized"] = projection
		_ = lw.emit(workerRecord{Kind: "native_event", EventSeq: l.seq, EventType: kind, Data: json.RawMessage(enc(retained))})
		return protocol && (kind == "turn.completed" || kind == "turn.failed" || kind == string(contract.FleetEventTypeProtocolFailed))
	}

	beat := time.NewTicker(tickEvery)
	defer beat.Stop()
	var deadlineC <-chan time.Time
	var deadlineTimer *time.Timer
	if !l.deadline.IsZero() {
		deadlineTimer = time.NewTimer(max(time.Millisecond, time.Until(l.deadline)))
		defer deadlineTimer.Stop()
		deadlineC = deadlineTimer.C
	}
	linesOpen := lines != nil
	if !linesOpen {
		l.eof = true
	}
	exitedSent := false
	// A protocol turn can finish while its server stays open. Give EOF on stdin
	// time to shut it down before forcing an unconfirmed process outcome.
	const shutdownGrace = 5 * time.Second
	var shutdownC <-chan time.Time
	for {
		if exitedSent && l.eof {
			break
		}
		var lc <-chan string
		if linesOpen {
			lc = lines
		}
		var ex <-chan struct{}
		if !exitedSent {
			ex = l.exited
		}
		select {
		case line, ok := <-lc:
			if !ok {
				l.eof, linesOpen = true, false
				continue
			}
			if handleLine(line) && !exitedSent && shutdownC == nil {
				shutdownC = time.After(shutdownGrace)
			}
		case <-ex:
			exitedSent = true
			shutdownC = nil
			// Owned descendants can keep stdout open or await the MCP owner.
			// Close ownership and clean up before waiting for either of them.
			if mcp != nil && mcp.close() != nil {
				mu.Lock()
				l.cleanupUnknown = true
				mu.Unlock()
			}
			mu.Lock()
			if l.cleanup() != nil {
				l.cleanupUnknown = true
			}
			mu.Unlock()
			l.orphans = cleanupRun(spec.DataDir, spec.Execution)
			for {
				mu.Lock()
				alive, aerr := l.aliveOwned()
				mu.Unlock()
				var unknown identityUnknown
				if errors.As(aerr, &unknown) {
					time.Sleep(100 * time.Millisecond)
					continue
				}
				if aerr != nil {
					mu.Lock()
					l.cleanupUnknown = true
					mu.Unlock()
					break
				}
				if !alive {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		case <-shutdownC:
			shutdownC = nil
			if !l.exitedNow() {
				mu.Lock()
				l.cleanupUnknown = true
				_ = killOwned()
				mu.Unlock()
			}
		case <-deadlineC:
			deadlineC = nil
			mu.Lock()
			if !l.exitedNow() && !l.stopped {
				l.timedOut, l.stopped = true, true
				l.seq++
				_ = lw.emit(workerRecord{Kind: "fleet_event", EventSeq: l.seq, EventType: string(contract.FleetEventTypeScriptTimeout), Data: json.RawMessage(enc(contract.ScriptTimeout{TimeoutMs: l.timeoutMs}))})
				if killOwned() != nil {
					l.cleanupUnknown = true
				}
			}
			mu.Unlock()
		case <-beat.C:
			recordOwned()
		}
	}

	closedSock.Store(true)
	listener.Close()
	if l.wire != nil {
		l.wire.mu.Lock()
		if l.wire.log != nil {
			l.wire.log.Close()
		}
		l.wire.mu.Unlock()
	}
	if mcpStore != nil {
		if err := cleanupMCPPIDs(mcpStore.snapshot(), nil); err != nil {
			mu.Lock()
			l.cleanupUnknown = true
			mu.Unlock()
			l.seq++
			_ = lw.emit(workerRecord{Kind: "fleet_event", EventSeq: l.seq, EventType: "mcp.cleanup_unknown", Data: json.RawMessage(enc(M{"reason": err.Error(), "ownershipRetained": true}))})
		}
	}
	var code *int64
	if l.state() != nil && l.state().Exited() {
		c := int64(l.state().ExitCode())
		code = &c
	}
	_ = l.observe()
	res := buildExecutionResourceSummary(l)
	var cpu float64
	for _, c := range l.cpu {
		cpu += c
	}
	mu.Lock()
	cleanupUnknown := l.cleanupUnknown
	timedOut := l.timedOut
	stopped := l.stopped
	completed := l.completed
	mu.Unlock()
	status := "failed"
	switch {
	case cleanupUnknown:
		status = "unknown"
	case timedOut:
	case stopped:
		status = "stopped"
	case l.state() != nil && l.state().Success() && completed:
		status = "completed"
	}
	l.seq++
	ex := workerExit{
		Status:         status,
		ExitCode:       code,
		StdoutClosed:   l.eof,
		CleanupUnknown: cleanupUnknown,
		TimedOut:       timedOut,
		Stopped:        stopped,
		Completed:      completed,
		Succeeded:      l.state() != nil && l.state().Success(),
		Native:         l.native,
		Seq:            l.seq,
		Orphans:        l.orphans,
		MCPDir:         mcpDir,
		StartedAt:      l.started,
		CPUSeconds:     cpu,
		HasCPU:         len(l.cpu) > 0,
		PeakRSS:        l.peakRSS,
		Resources:      res,
		StderrTail:     stderrTail(spec.Dir, spec.Execution, code),
	}
	tmpExit := filepath.Join(workerDir, "exit.json.tmp")
	if err := os.WriteFile(tmpExit, []byte(enc(ex)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmpExit, filepath.Join(workerDir, "exit.json"))
}

func peerUID(c *net.UnixConn) (int, error) {
	_, uid, err := mcpPeer(c)
	return uid, err
}

// ---- Poller-side worker client & reattachment ----

type liveWorker struct {
	dir     string
	sock    string
	trial   *[2]string
	lastSeq uint64
	offset  int64
}

func (e *executor) spawnWorker(c contract.Command, spec workerSpec) (*live, error) {
	wdir := workerDirFor(e.root, spec.Execution)
	if err := os.MkdirAll(wdir, 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(wdir, "spec.json"), []byte(enc(spec)), 0o600); err != nil {
		return nil, err
	}
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer pr.Close()
	cmd := newCmd(exe(), e.root)
	cmd.Args = append(cmd.Args, "run-worker", wdir)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err == nil {
		defer devNull.Close()
		cmd.Stdin, cmd.Stdout = devNull, devNull
	}
	if werr, err := os.OpenFile(filepath.Join(wdir, "worker.stderr"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer werr.Close()
		cmd.Stderr = werr
	}
	cmd.ExtraFiles = []*os.File{pw}
	if err := cmd.Start(); err != nil {
		pw.Close()
		return nil, err
	}
	pw.Close()
	// Reap our direct child when it exits if this poller remains alive; ignore wait result since worker.lock + exit.json own completion.
	errtrack.Go("worker.reap", func() { _ = cmd.Wait() })

	var rep workerStartReply
	line, rerr := bufio.NewReader(pr).ReadString('\n')
	if rerr != nil && !errors.Is(rerr, io.EOF) {
		return nil, rerr
	}
	if line == "" {
		return nil, errors.New("worker exited without reporting start status")
	}
	if err := json.Unmarshal([]byte(line), &rep); err != nil {
		return nil, err
	}
	if !rep.Ok {
		if rep.Unknown {
			return nil, errWorkerStartUnknown
		}
		return nil, errors.New(rep.FailReason)
	}
	l := &live{
		command:   c,
		native:    spec.Native,
		seq:       spec.InitialSeq,
		completed: spec.Kind == "script",
		owned:     rep.Owned,
		journaled: map[int]bool{},
		started:   time.Now(),
		group:     rep.Grp,
		trial:     spec.Trial,
		worker: &liveWorker{
			dir:  wdir,
			sock: readWorkerSockPath(wdir),
			trial: spec.Trial,
		},
	}
	if rep.MCPDir != "" {
		if _, err := e.db.Exec("UPDATE launches SET mcp_dir=? WHERE execution=?", rep.MCPDir, spec.Execution); err != nil {
			_ = workerStop(l.worker.sock, "")
			return nil, err
		}
	}
	if err := e.ownGroup(spec.Execution, rep.Grp, l); err != nil {
		_ = workerStop(l.worker.sock, "")
		return nil, errWorkerStartUnknown
	}
	return l, nil
}

var errWorkerStartUnknown = errors.New("worker start outcome unknown")

func workerCall(sock string, req workerControlReq) (workerControlResp, error) {
	var resp workerControlResp
	if err := privateSocket(sock); err != nil {
		return resp, err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		return resp, err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if err := json.NewEncoder(c).Encode(req); err != nil {
		return resp, err
	}
	if err := json.NewDecoder(c).Decode(&resp); err != nil {
		return resp, err
	}
	return resp, nil
}

func workerStop(sock, cid string) error {
	resp, err := workerCall(sock, workerControlReq{Action: "stop", CommandId: cid})
	if err != nil || !resp.Accepted {
		if err == nil {
			err = errors.New("worker stop not accepted")
		}
		return err
	}
	return nil
}

func workerSteer(sock, cid, text string) bool {
	resp, err := workerCall(sock, workerControlReq{Action: "steer", CommandId: cid, Text: text})
	return err == nil && resp.Ok
}

// workerLockAlive checks whether worker.lock is currently held by a running run-worker.
func workerLockAlive(workerDir string) (alive bool, err error) {
	lockPath := filepath.Join(workerDir, "worker.lock")
	f, err := os.Open(lockPath)
	if err != nil {
		return false, err
	}
	defer f.Close()
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return true, nil
	}
	return false, nil
}

func readWorkerExit(workerDir string) (*workerExit, bool, error) {
	b, err := os.ReadFile(filepath.Join(workerDir, "exit.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var ex workerExit
	if err := json.Unmarshal(b, &ex); err != nil {
		return nil, true, err
	}
	return &ex, true, nil
}

func readWorkerSpec(workerDir string) (*workerSpec, bool) {
	b, err := os.ReadFile(filepath.Join(workerDir, "spec.json"))
	if err != nil {
		return nil, false
	}
	var s workerSpec
	if json.Unmarshal(b, &s) != nil {
		return nil, false
	}
	return &s, true
}

func readWorkerPIDLines(path string) map[int]string {
	out := map[int]string{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			Pid      int    `json:"pid"`
			Identity string `json:"identity"`
		}
		if json.Unmarshal([]byte(line), &row) == nil && row.Pid > 0 && row.Identity != "" {
			out[row.Pid] = row.Identity
		}
	}
	return out
}

func (e *executor) drainWorkerEvents(execution string, l *live) error {
	w := l.worker
	f, err := os.Open(filepath.Join(w.dir, "events.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	if w.offset > 0 {
		if _, err := f.Seek(w.offset, io.SeekStart); err != nil {
			return err
		}
	}
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadString('\n')
		if !strings.HasSuffix(line, "\n") {
			// Incomplete line at EOF: leave w.offset before this fragment.
			break
		}
		w.offset += int64(len(line))
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			if rerr != nil {
				break
			}
			continue
		}
		var rec workerRecord
		if json.Unmarshal([]byte(trimmed), &rec) != nil {
			if rerr != nil {
				break
			}
			continue
		}
		if rec.Seq <= w.lastSeq {
			if rerr != nil {
				break
			}
			continue
		}
		if err := e.applyWorkerRecord(execution, l, rec); err != nil {
			return err
		}
		w.lastSeq = rec.Seq
		if rerr != nil {
			break
		}
	}
	return nil
}

func (e *executor) applyWorkerRecord(execution string, l *live, rec workerRecord) error {
	return e.inTx(func() error {
		switch rec.Kind {
		case "owned":
			if l.owned == nil {
				l.owned = map[int]string{}
			}
			for pid, id := range rec.Owned {
				if _, err := e.ex.Exec("INSERT OR IGNORE INTO owned(execution,pid,identity) VALUES(?,?,?)", execution, pid, id); err != nil {
					return err
				}
				l.owned[pid] = id
			}
		case "mcp_group":
			if _, err := e.ex.Exec("INSERT OR IGNORE INTO mcp_groups(execution,pid,identity) VALUES(?,?,?)", execution, rec.Pid, rec.Identity); err != nil {
				return err
			}
		case "native":
			sid, err := workOf(l.command)
			if err != nil {
				return err
			}
			if _, err := e.ex.Exec("INSERT OR REPLACE INTO sessions(id,native) VALUES(?,?)", sid, rec.Native); err != nil {
				return err
			}
			if err := e.receipt(l.command, "started", rec.Native); err != nil {
				return err
			}
			l.native = rec.Native
		case "native_event":
			if rec.EventSeq > l.seq {
				l.seq = rec.EventSeq
			}
			if err := e.nativeEvent(l.command, rec.EventSeq, rec.EventType, rec.Data); err != nil {
				return err
			}
		case "fleet_event":
			if rec.EventSeq > l.seq {
				l.seq = rec.EventSeq
			}
			if err := e.event(l.command, rec.EventSeq, contract.FleetEventType(rec.EventType), rec.Data); err != nil {
				return err
			}
		case "steer_result":
			if b, ok := e.commandBody(rec.CommandId, true); ok {
				if err := e.finish(commandOf(b), okStatus(rec.Ok), ""); err != nil {
					return err
				}
			}
		case "stopped":
			l.stopped = true
		}
		_, err := e.ex.Exec("UPDATE launches SET worker_seq=? WHERE execution=?", rec.Seq, execution)
		return err
	})
}

func (e *executor) finishWorkerRun(execution string, l *live, ex *workerExit) (bool, error) {
	l.native = ex.Native
	l.stopped = ex.Stopped
	l.timedOut = ex.TimedOut
	l.cleanupUnknown = ex.CleanupUnknown
	l.completed = ex.Completed
	l.orphans = ex.Orphans
	l.eof = ex.StdoutClosed
	l.workerExit = ex
	removeRunFiles(e.root, execution)
	executionSpan(execution, l)
	if err := e.event(l.command, ex.Seq, contract.FleetEventTypeProcessClosed, contract.ProcessClosed{
		ExitCode:                        ex.ExitCode,
		StdoutClosed:                    ex.StdoutClosed,
		ObservedOwnedProcessesRemaining: new(bool),
		CleanupUnknown:                  ex.CleanupUnknown,
		Resources:                       ex.Resources,
		StderrTail:                      ex.StderrTail,
	}); err != nil {
		return false, err
	}
	sid, err := workOf(l.command)
	if err != nil {
		return false, err
	}
	dir := e.commandDir(sid, workspaceKey(l.command))
	output := []string{"output"}
	if st, err := os.Lstat(filepath.Join(dir, "worktree", "output")); err == nil && st.IsDir() {
		output = []string{"worktree", "output"}
	}
	var declared []string
	if e.artifacts(dir, output, execution, &declared) != nil {
		if l.trial != nil {
			removeTrial(l.trial[0], l.trial[1])
		}
		return true, e.finish(l.command, "failed", l.native)
	}
	var diagnostics []string
	if err := e.artifacts(dir, []string{execution + ".mcp"}, execution, &diagnostics); err != nil {
		_ = e.event(l.command, ex.Seq+1, "mcp.diagnostics_unavailable", M{"reason": "safe receipt collection failed"})
	}
	if c := l.command; c.Start != nil && c.Start.Payload != nil && projectPersonal(c.Start.Payload.Project) {
		dropPersonal(dir, output)
	}
	e.declared = declared
	if l.trial != nil {
		if err := e.event(l.command, ex.Seq+2, contract.FleetEventTypeTrialRemoved, contract.TrialRemoved{Removed: removeTrial(l.trial[0], l.trial[1])}); err != nil {
			return false, err
		}
	}
	if err := e.finish(l.command, ex.Status, l.native); err != nil {
		return false, err
	}
	if l.stopped {
		rs, err := e.db.Query("SELECT body FROM commands WHERE json_extract(body,'$.executionId')=? AND json_extract(body,'$.kind')='stop'", execution)
		if err != nil {
			return false, err
		}
		var bodies []string
		for rs.Next() {
			var b string
			rs.Scan(&b)
			bodies = append(bodies, b)
		}
		rs.Close()
		st := "stopped"
		if l.cleanupUnknown {
			st = "unknown"
		}
		for _, b := range bodies {
			if err := e.finish(commandOf(b), st, l.native); err != nil {
				return false, err
			}
		}
	}
	return true, nil
}

// baseEnvState snapshots PATH and tool env vars before useManagedTools mutates them so unix.Exec starts clean.
type baseEnvState struct {
	path string
	vars map[string]*string
}

func captureBaseEnv(keys map[string]string) baseEnvState {
	s := baseEnvState{path: os.Getenv("PATH"), vars: map[string]*string{}}
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			c := v
			s.vars[k] = &c
		} else {
			s.vars[k] = nil
		}
	}
	return s
}

func (s baseEnvState) restore() {
	_ = os.Setenv("PATH", s.path)
	for k, v := range s.vars {
		if v == nil {
			_ = os.Unsetenv(k)
		} else {
			_ = os.Setenv(k, *v)
		}
	}
}

// selfExecTarget returns the resolved current/fleet-executor binary path when this poller runs inside a versioned
// release tree and current now points to another installed release.
func selfExecTarget() string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	resolvedSelf, err := filepath.EvalSymlinks(self)
	if err != nil {
		return ""
	}
	return selfExecTargetFrom(resolvedSelf)
}

func selfExecTargetFrom(resolvedSelf string) string {
	releaseDir := filepath.Dir(resolvedSelf)
	ver := filepath.Base(releaseDir)
	if !releaseVerRe.MatchString(ver) {
		return ""
	}
	currentLink := filepath.Join(filepath.Dir(releaseDir), "current", filepath.Base(resolvedSelf))
	resolvedCur, err := filepath.EvalSymlinks(currentLink)
	if err != nil || resolvedCur == resolvedSelf {
		return ""
	}
	nextVer := filepath.Base(filepath.Dir(resolvedCur))
	if !releaseVerRe.MatchString(nextVer) || !isFile(resolvedCur) {
		return ""
	}
	return resolvedCur
}
