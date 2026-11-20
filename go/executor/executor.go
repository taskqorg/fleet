package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
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

var shutdown atomic.Bool

// cmdID returns a command identity field: non-empty ASCII alphanumerics, '_' and '-'.
func cmdID(v any, k string) (string, error) {
	s, ok := jAt(v, k).(string)
	if !ok {
		return "", errors.New("missing identity")
	}
	return checkedID(s)
}

// idOf returns a command's identity field (id, executionId or sessionId) as cmdID checks it; a command that does not fit
// its kind's type is read from its JSON.
func idOf(c contract.Command, k string) (string, error) {
	if c.Value() != nil {
		var s *string
		switch k {
		case "id":
			s = new(c.Id())
		case "executionId":
			s = new(c.ExecutionId())
		case "sessionId":
			s = c.SessionId()
		}
		if s != nil && *s != "" {
			return checkedID(*s)
		}
	}
	raw := c.Raw
	if raw == nil {
		raw, _ = json.Marshal(c)
	}
	var envelope map[string]json.RawMessage
	json.Unmarshal(raw, &envelope)
	s, ok := rawStr(envelope[k])
	if !ok {
		return "", errors.New("missing identity")
	}
	return checkedID(s)
}

func checkedID(s string) (string, error) {
	if s == "" || !onlyBytes(s, func(b byte) bool { return alnum(b) || b == '_' || b == '-' }) {
		return "", errors.New("invalid identity")
	}
	return s, nil
}

// harnessName: payload harness, else agentConfig.harness; a non-string agentConfig harness is "invalid"; default codex.
func harnessName(p contract.StartPayload) string {
	if p.Harness != nil {
		return *p.Harness
	}
	raw, ok := p.AgentConfig["harness"]
	if !ok {
		return "codex"
	}
	if s, isS := rawStr(raw); isS {
		return s
	}
	return "invalid"
}

// commandHarness: the harness of a start or input command; other kinds name none, so codex.
func commandHarness(c contract.Command) string {
	switch {
	case c.Start != nil && c.Start.Payload != nil:
		return harnessName(*c.Start.Payload)
	case c.Input != nil && c.Input.Payload != nil:
		return harnessName(*c.Input.Payload)
	}
	return harnessName(contract.StartPayload{})
}

// workspaceKey: the command payload's workspaceKey ("" when absent or not a string).
func workspaceKey(c contract.Command) string {
	var x struct {
		Payload struct {
			WorkspaceKey json.RawMessage `json:"workspaceKey"`
		} `json:"payload"`
	}
	raw := c.Raw
	if raw == nil {
		raw, _ = json.Marshal(c)
	}
	json.Unmarshal(raw, &x)
	key, _ := rawStr(x.Payload.WorkspaceKey)
	return key
}

// safeNativeID: native ids reach argv; ASCII alphanumerics and '-', at most 64, never a leading '-'.
func safeNativeID(n string) bool {
	return n != "" && len(n) <= 64 && n[0] != '-' && onlyBytes(n, func(b byte) bool { return alnum(b) || b == '-' })
}

// hostSecret reads the host secret file: not a symlink, no group or other access, at least 32 characters.
func hostSecret(path string) (string, error) {
	m, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if m.Mode()&os.ModeSymlink != 0 || m.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s: host secret file must be a private file (0600)", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(b))
	if len(s) < 32 {
		return "", errors.New("host secret too short")
	}
	return s, nil
}

// platform for the coordinator: a WSL host whose Claude placement is windows-wsl counts as win32.
func platform() string {
	if p, err := claudePlacementHere(); err == nil && p == "windows-wsl" {
		return "win32"
	}
	return runtime.GOOS
}

func capabilities() []string {
	c := []string{}
	for _, x := range contract.CapabilityValues {
		c = append(c, string(x))
	}
	if v := ffmpegVersion(); v != "" {
		c = append(c, contract.FfmpegCapability(v))
	}
	for _, tool := range []string{"node", "pnpm"} {
		if _, err := exec.LookPath(tool); err == nil {
			c = append(c, contract.ToolCapability(tool))
		}
	}
	if store := os.Getenv("FLEET_FORGE_CONFIG_DIR"); filepath.IsAbs(store) && isFile(filepath.Join(store, "hosts.yml")) {
		for _, repo := range strings.Split(os.Getenv("FLEET_FORGE_REPOSITORIES"), ",") {
			if forgeRepo("https://github.com/"+repo) == repo && repo != "" {
				c = append(c, contract.ForgeCapability(repo))
			}
		}
	}
	if isFile(os.Getenv("FLEET_PLAYWRIGHT_MODULE")) && isFile(os.Getenv("FLEET_BROWSER_EXECUTABLE")) && private(os.Getenv("FLEET_BROWSER_BINDINGS")) == nil {
		c = append(c, contract.ToolCapability("browser"))
	}
	return c
}

// mediaType from the file extension; unknown types are stored as octet-stream.
func mediaType(name string) string {
	ext := strings.ToLower(name[strings.LastIndexByte(name, '.')+1:])
	for types, exts := range map[string][]string{
		"text/html": {"html", "htm"}, "text/css": {"css"}, "text/javascript": {"js", "mjs"}, "application/json": {"json"},
		"text/plain": {"txt", "md", "log"}, "text/csv": {"csv"}, "application/xml": {"xml"}, "application/pdf": {"pdf"},
		"image/svg+xml": {"svg"}, "image/png": {"png"}, "image/jpeg": {"jpg", "jpeg"}, "image/webp": {"webp"}, "image/gif": {"gif"}, "image/x-icon": {"ico"},
		"video/mp4": {"mp4"}, "video/quicktime": {"mov"}, "video/webm": {"webm"}, "video/x-matroska": {"mkv"},
		"model/gltf-binary": {"glb"}, "model/gltf+json": {"gltf"}, "model/obj": {"obj"}, "model/stl": {"stl"}, "model/vnd.usdz+zip": {"usdz"},
		"font/woff2": {"woff2"}, "application/zip": {"zip"}, "application/x-ndjson": {"jsonl", "ndjson"},
	} {
		if contains(exts, ext) {
			return types
		}
	}
	return "application/octet-stream"
}

func newUUID() string {
	var b [16]byte
	if f, err := os.Open("/dev/urandom"); err == nil {
		io.ReadFull(f, b[:])
		f.Close()
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := fmt.Sprintf("%x", b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// wire is the protocol side of a live run: the session, its stdin and the outbound journal (protocol-out.jsonl).
type wire struct {
	mu      sync.Mutex
	session Session
	stdin   io.WriteCloser
	log     *os.File
}

// writeFrame persists intent bytes before sending; a successful pipe write is not native acceptance.
func writeFrame(w io.Writer, log *os.File, v M) (M, error) {
	b := []byte(enc(v) + "\n")
	if _, err := log.Write(b); err != nil {
		return nil, err
	}
	if err := log.Sync(); err != nil {
		return nil, err
	}
	if _, err := w.Write(b); err != nil {
		return nil, err
	}
	var frame struct {
		Method json.RawMessage `json:"method"`
		Id     json.RawMessage `json:"id"`
	}
	json.Unmarshal(b, &frame)
	method := "response"
	if m, ok := rawStr(frame.Method); ok {
		method = m
	}
	var rid any
	var n uint64
	if len(frame.Id) > 0 && json.Unmarshal(frame.Id, &n) == nil && string(frame.Id) != "null" {
		rid = n
	}
	return M{"type": "protocol.write", "method": method, "requestId": rid, "bytes": len(b), "sha256": sha(b),
		"delivery": "written to local transport; native acceptance unknown"}, nil
}

type live struct {
	mcp            *mcpOwner
	cmd            *exec.Cmd
	exited         <-chan struct{} // closed once the process was reaped (supervise.Proc.Done)
	proc           *supervise.Proc
	command        contract.Command // as journaled; idOf reads its identities
	native         string
	nativeObserved bool
	seq            uint64
	completed      bool
	stopped        bool
	owned          map[int]string
	journaled      map[int]bool // owned pids already in the journal's owned table
	cleanupUnknown bool
	lines          chan string // closed at stdout EOF
	eof            bool
	wire           *wire
	// Script commands: the process group stop and timeout kill, the deadline, and the trial worktree (top, path).
	group     int
	deadline  time.Time
	timedOut  bool
	timeoutMs *int64 // the script payload's timeout_ms, for the script.timeout event
	trial     *[2]string
	blocks    Blocks
	metrics   *runMetrics
	// Resources of the owned process tree (fleet-telemetry.md, Process readings): start, peak resident bytes, and each
	// pid's last CPU seconds (a gone pid keeps its last reading).
	started time.Time
	peakRSS uint64
	cpu     map[int]float64
	spanned bool // the execution span is written
}

// account adds one snapshot of the owned tree to the run's peak memory and CPU.
func (l *live) account(table []proc) {
	if l.cpu == nil {
		l.cpu = map[int]float64{}
	}
	var rss uint64
	for _, p := range table {
		if strings.HasPrefix(p.stat, "Z") {
			continue
		}
		if expected, ok := l.owned[p.pid]; ok {
			if id, err := identity(p.pid); err != nil || id != expected {
				continue
			}
			rss += p.rssKB * 1024
			l.cpu[p.pid] = max(l.cpu[p.pid], p.cpu)
		}
	}
	l.peakRSS = max(l.peakRSS, rss)
}

// state: the reaped process's state; nil while it runs.
func (l *live) state() *os.ProcessState {
	if !l.exitedNow() {
		return nil
	}
	return l.cmd.ProcessState
}

func (l *live) exitedNow() bool {
	select {
	case <-l.exited:
		return true
	default:
		return false
	}
}

type executor struct {
	stopCtx      context.Context
	activity     *os.File
	updateReady  atomic.Pointer[string]
	db           *sql.DB
	ex           execer // db, or the open transaction
	root         string
	url          string
	token        string
	host         string
	live         map[string]*live
	sessions     string
	harnessCache map[string]M
	storeCfg     *storeFile
	declared     []string
	storeAt      time.Time
	storeLast    []contract.StoreReport
}

// inTx runs fn with every journal write of the executor inside one transaction.
func (e *executor) inTx(fn func() error) error {
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	e.ex = tx
	defer func() { e.ex = e.db }()
	if err := fn(); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// sessionDir: existing sessions keep their directory; new ones go under FLEET_SESSIONS_ROOT.
func (e *executor) sessionDir(session string) string {
	legacy := filepath.Join(e.root, "sessions", session)
	if exists(legacy) {
		return legacy
	}
	return filepath.Join(e.sessions, session)
}

func workspaceSession(session, key string) string {
	if key != "" {
		sum := sha256.Sum256([]byte(key))
		return session + "-" + fmt.Sprintf("%x", sum[:])
	}
	return session
}

// commandDir keeps transitioned workspaces isolated; legacy commands retain their original directory.
func (e *executor) commandDir(session, key string) string {
	dir := e.sessionDir(session)
	if key != "" {
		// Hash the untrusted key so it cannot escape the owned session directory.
		sum := sha256.Sum256([]byte(key))
		dir = filepath.Join(dir, "workspaces", fmt.Sprintf("%x", sum[:]))
	}
	return dir
}

func (e *executor) receipt(c contract.Command, status string, native string) error {
	declared := e.declared
	e.declared = nil
	cid, err := idOf(c, "id")
	if err != nil {
		return err
	}
	ex, err := idOf(c, "executionId")
	if err != nil {
		return err
	}
	if declared == nil {
		declared = []string{}
	}
	r := contract.Receipt{HostId: e.host, CommandId: cid, ExecutionId: ex, Status: contract.ReceiptStatus(status), Artifacts: declared}
	if native != "" {
		r.NativeSessionId = &native
	}
	return queueTo(e.ex, contract.RouteReceipts, r)
}

// event queues one Fleet-originated execution event; nativeEvent one harness record under its own open type.
func (e *executor) event(c contract.Command, seq uint64, kind contract.FleetEventType, data any) error {
	return e.nativeEvent(c, seq, string(kind), data)
}

func (e *executor) nativeEvent(c contract.Command, seq uint64, kind string, data any) error {
	cid, err := idOf(c, "id")
	if err != nil {
		return err
	}
	ex, err := idOf(c, "executionId")
	if err != nil {
		return err
	}
	ev := contract.HostEvents{HostId: e.host, ExecutionId: ex, Events: []contract.HostEvent{{Id: fmt.Sprintf("%s-%d", cid, seq), Seq: int64(seq), Type: kind, Timestamp: nowMs(), Data: json.RawMessage(enc(data))}}}
	return queueTo(e.ex, contract.RouteEvents, ev)
}

func (e *executor) finish(c contract.Command, status, native string) error {
	id, _ := idOf(c, "id")
	execution, _ := idOf(c, "executionId")
	if l := e.live[execution]; l != nil && l.mcp != nil && l.commandID() == id {
		if err := l.mcp.close(); err != nil {
			return err
		}
	}
	return e.inTx(func() error {
		if err := e.receipt(c, status, native); err != nil {
			return err
		}
		cid, err := idOf(c, "id")
		if err != nil {
			return err
		}
		if _, err = e.ex.Exec("UPDATE launches SET mcp_fenced=1 WHERE execution=? AND command=?", execution, cid); err != nil {
			return err
		}
		_, err = e.ex.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
		return err
	})
}

// launch records the intent to spawn before the process exists; own records owned processes as they are observed;
// release forgets both once the run is reaped.
func (e *executor) launch(execution, command, session string) error {
	_, err := e.db.Exec("INSERT OR REPLACE INTO launches(execution,command,session) VALUES(?,?,?)", execution, command, session)
	return err
}

func (e *executor) own(execution string, l *live) error {
	if l.journaled == nil {
		l.journaled = map[int]bool{}
	}
	for pid, ident := range l.owned {
		if l.journaled[pid] {
			continue
		}
		if _, err := e.db.Exec("INSERT OR IGNORE INTO owned(execution,pid,identity) VALUES(?,?,?)", execution, pid, ident); err != nil {
			return err
		}
		l.journaled[pid] = true
	}
	return nil
}

func (e *executor) ownGroup(execution string, grp int, l *live) error {
	if _, err := e.db.Exec("UPDATE launches SET grp=? WHERE execution=?", grp, execution); err != nil {
		return err
	}
	return e.own(execution, l)
}

// releaseLaunchWindow: a stop for an execution whose launch never recorded a process is the explicit decision to
// accept that uncertainty; the session is admitted again. The stop's result stays unknown.
func (e *executor) releaseLaunchWindow(execution, session string) error {
	res, err := e.db.Exec("DELETE FROM launches WHERE execution=? AND session=? AND NOT EXISTS (SELECT 1 FROM owned WHERE execution=?) AND NOT EXISTS (SELECT 1 FROM mcp_groups WHERE execution=?)", execution, session, execution, execution)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		removeRunFiles(e.root, execution) // the run token goes with the launch
	}
	return nil
}

func (e *executor) release(execution string) error {
	if err := cleanupMCPGroups(e.db, execution); err != nil {
		return err
	}
	var directory sql.NullString
	e.db.QueryRow("SELECT mcp_dir FROM launches WHERE execution=?", execution).Scan(&directory)
	err := e.inTx(func() error {
		if _, err := e.ex.Exec("DELETE FROM owned WHERE execution=?", execution); err != nil {
			return err
		}
		_, err := e.ex.Exec("DELETE FROM launches WHERE execution=?", execution)
		return err
	})
	if err == nil && directory.Valid {
		removeMCPOwnerDir(directory.String)
	}
	return err
}

// reconcileProcesses runs at start, before any poll (K2-3). The stdout and stdin pipes of a run died with the executor
// that started it, so nothing is reattached: every recorded process that survives, and every descendant still linked to
// one, is frozen and killed, each signal bound to the recorded incarnation, then its run file is removed (K2-6). A launch
// with no owned row crashed between spawn and the identity read, so its process cannot be named; the event says so,
// and only a stop for that execution releases it (releaseLaunchWindow). A launch whose processes cannot be confirmed
// gone stays recorded, which keeps its session refused, and is tried again at the next start.
func (e *executor) reconcileProcesses() error {
	if _, err := e.db.Exec("UPDATE launches SET mcp_fenced=1"); err != nil {
		return err
	}
	type launch struct {
		execution, command string
		l                  *live
		survived           bool
		err                error
	}
	var launches []*launch
	rs, err := e.db.Query("SELECT execution,command,grp FROM launches")
	if err != nil {
		return err
	}
	groups := map[*launch]int{}
	for rs.Next() {
		x := &launch{l: &live{owned: map[int]string{}}}
		var grp int
		if err := rs.Scan(&x.execution, &x.command, &grp); err != nil {
			rs.Close()
			return err
		}
		launches, groups[x] = append(launches, x), grp
	}
	rs.Close()
	for _, x := range launches {
		rows, err := e.db.Query("SELECT pid,identity FROM owned WHERE execution=?", x.execution)
		if err != nil {
			return err
		}
		for rows.Next() {
			var pid int
			var ident string
			if err := rows.Scan(&pid, &ident); err != nil {
				rows.Close()
				return err
			}
			x.l.owned[pid] = ident
		}
		rows.Close()
		if err := cleanupMCPGroups(e.db, x.execution); err != nil {
			x.err = err
			continue
		}
		// The group id is ours only while its leader is the recorded incarnation.
		if g := groups[x]; g != 0 {
			if id, err := identity(g); err == nil && id == x.l.owned[g] {
				x.l.group = g
			}
		}
		if x.survived, x.err = x.l.aliveOwned(); x.err == nil && x.survived {
			x.err = x.l.cleanup()
		}
	}
	// ponytail: one 2 s window for every killed process to leave; one still there (uninterruptible sleep) stays
	// unresolved until the next start.
	for i := 0; i < 20; i++ {
		waiting := false
		for _, x := range launches {
			if x.err == nil {
				// An unknown identity is asked again on the next pass; the final check below decides.
				alive, err := x.l.aliveOwned()
				var unknown identityUnknown
				waiting = waiting || alive || errors.As(err, &unknown)
				if !errors.As(err, &unknown) {
					x.err = err
				}
			}
		}
		if !waiting {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	unresolved := map[string]bool{}
	for _, x := range launches {
		alive, err := x.l.aliveOwned()
		// No owned row: the executor died between spawn and the identity write, so the process cannot be named and
		// its absence is not proven. The launch stays and keeps its session refused until a stop for it (W3-1).
		resolved := len(x.l.owned) > 0 && x.err == nil && err == nil && !alive
		// Reported once, while the command is open; the reconcile below then finishes it unknown.
		if b, ok := e.commandBody(x.command, true); ok {
			if err := e.event(commandOf(b), uint64(nowMs()), contract.FleetEventTypeProcessReconciled, contract.ProcessReconciled{OwnedProcesses: int64(len(x.l.owned)), Survived: x.survived,
				LaunchWindowUnresolved: len(x.l.owned) == 0, Resolved: resolved}); err != nil {
				return err
			}
		}
		if !resolved {
			unresolved[x.execution] = true
			continue
		}
		if err := e.release(x.execution); err != nil {
			return err
		}
	}
	// Run files and code server specs of every execution without an unresolved launch: no process of this executor can need them.
	files, err := filepath.Glob(filepath.Join(e.root, "runs", "*.env"))
	if err != nil {
		return err
	}
	specs, _ := filepath.Glob(filepath.Join(e.root, "runs", "*.code.json"))
	contexts, _ := filepath.Glob(filepath.Join(e.root, "runs", "*.fleet.json"))
	for _, f := range append(append(files, specs...), contexts...) {
		if !unresolved[strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(filepath.Base(f), ".env"), ".code.json"), ".fleet.json")] {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
	}
	return nil
}

func (e *executor) reconcile() error {
	if err := e.reconcileProcesses(); err != nil {
		return err
	}
	rs, err := e.db.Query("SELECT body FROM commands WHERE terminal=0")
	if err != nil {
		return err
	}
	var bodies []string
	for rs.Next() {
		var b string
		if err := rs.Scan(&b); err != nil {
			rs.Close()
			return err
		}
		bodies = append(bodies, b)
	}
	rs.Close()
	for _, b := range bodies {
		if err := e.finish(commandOf(b), "unknown", ""); err != nil {
			return err
		}
	}
	return nil
}

// insideDirs: dir is a real directory under the executor or session root.
func (e *executor) insideDirs(dir string) bool {
	m, err := os.Lstat(dir)
	if err != nil || m.Mode()&os.ModeSymlink != 0 || !m.IsDir() {
		return false
	}
	d, err := canon(dir)
	if err != nil {
		return false
	}
	for _, r := range []string{e.root, e.sessions} {
		if c, err := canon(r); err == nil && under(d, c) {
			return true
		}
	}
	return false
}

func (e *executor) forbidden() []string { return []string{e.root, e.sessions} }

// observeHarness: harness version and model-visible built-in tools, cached per executable and argument set.
func (e *executor) observeHarness(cmd *exec.Cmd, harness string) M {
	exe := cmd.Path
	args := cmd.Args[1:]
	var flags []string
	for i := 0; i+1 < len(args); i++ {
		if contains([]string{"-c", "--enable", "--disable"}, args[i]) {
			flags = append(flags, args[i], args[i+1])
		}
	}
	key := harness + "\x00" + exe + "\x00"
	if harness == "codex" {
		key += strings.Join(flags, "\x00")
	}
	if v, ok := e.harnessCache[key]; ok {
		return maps.Clone(v) // callers set top-level keys only
	}
	var version any
	if o, err := exec.Command(exe, "--version").Output(); err == nil {
		version = strings.TrimSpace(string(o))
	}
	v := M{"harness": harness, "executable": exe, "version": version}
	switch harness {
	case "codex":
		// Same env, cwd and -c/--enable/--disable flags as the run; the prompt text is not sent anywhere.
		pargs := []string{"debug", "prompt-input"}
		for i := 0; i+1 < len(args); {
			if contains([]string{"-c", "--enable", "--disable"}, args[i]) {
				pargs = append(pargs, args[i], args[i+1])
				i += 2
			} else {
				i++
			}
		}
		probe := exec.Command(exe, append(pargs, "tools")...)
		probe.Env, probe.Dir = cmd.Env, cmd.Dir
		if probe.Env == nil {
			probe.Env = []string{}
		}
		var tools any
		if o, err := probe.Output(); err == nil {
			if _, err := dec(string(o)); err == nil {
				// A tool group {tools: [...]} nests; a leaf names a tool. Anything else is skipped.
				type node struct {
					Tools json.RawMessage `json:"tools"`
					Name  json.RawMessage `json:"name"`
				}
				array := func(raw json.RawMessage) []json.RawMessage {
					var items []json.RawMessage
					if len(raw) > 0 && raw[0] == '[' {
						json.Unmarshal(raw, &items)
					}
					return items
				}
				out := []any{}
				var walk func(raw json.RawMessage)
				walk = func(raw json.RawMessage) {
					var g node
					json.Unmarshal(raw, &g)
					for _, t := range array(g.Tools) {
						var x node
						json.Unmarshal(t, &x)
						if len(x.Tools) > 0 && x.Tools[0] == '[' {
							walk(t)
						} else if n, ok := rawStr(x.Name); ok {
							out = append(out, n)
						}
					}
				}
				for _, it := range array(o) {
					walk(it)
				}
				tools = out
			}
		}
		v["builtinTools"] = tools
	case "claude":
		v["builtinTools"] = "reported in native system/init event"
	default:
		v["builtinTools"] = "not exposed by ACP"
	}
	e.harnessCache[key] = v
	return maps.Clone(v)
}

func (e *executor) commandBody(id string, onlyOpen bool) (string, bool) {
	q := "SELECT body FROM commands WHERE id=?"
	if onlyOpen {
		q += " AND terminal=0"
	}
	var b string
	if e.db.QueryRow(q, id).Scan(&b) != nil {
		return "", false
	}
	return b, true
}

func (e *executor) run(c M) error {
	cid, err := cmdID(c, "id")
	if err != nil {
		return err
	}
	execution, err := cmdID(c, "executionId")
	if err != nil {
		return err
	}
	session, err := cmdID(c, "sessionId")
	if err != nil {
		return err
	}
	body := enc(c)
	// The command as its contract type (contract.Command); a payload that does not fit is refused by its kind below.
	var cmd contract.Command
	typedErr := json.Unmarshal([]byte(body), &cmd)
	kind := contract.CommandKind(cmd.Kind())
	if typedErr != nil || cmd.Value() == nil {
		kind = commandKind(body) // a payload that does not fit, or a kind this executor does not know (refused below)
	}
	if typedErr != nil {
		cmd = contract.Command{Raw: json.RawMessage(body)} // receipts and events still read its identities
	}
	signalMu.Lock()
	draining := updatePending(lastSignal)
	signalMu.Unlock()
	if draining && kind != contract.CommandKindStop && kind != contract.CommandKindSteer {
		return nil
	}
	leased, leaseErr := e.activityLease()
	if leaseErr != nil {
		return leaseErr
	}
	if !leased {
		return nil
	}
	if err := e.withdrawUpdateReady(); err != nil {
		return err
	}
	if old, ok := e.commandBody(cid, false); ok {
		if old != body {
			return errors.New("command identity conflict")
		}
		return nil
	}
	if err := e.inTx(func() error {
		if _, err := e.ex.Exec("INSERT INTO commands(id,body) VALUES(?,?)", cid, body); err != nil {
			return err
		}
		return e.receipt(cmd, "accepted", "")
	}); err != nil {
		return err
	}
	payload := c["payload"]
	switch kind {
	case contract.CommandKindStop:
		result := "unknown"
		if l, ok := e.live[execution]; ok {
			l.stopped = true
			if l.cleanup() == nil {
				result = "accepted"
			} else {
				l.cleanupUnknown = true
			}
		} else if err := e.releaseLaunchWindow(execution, session); err != nil {
			return err
		}
		if err := e.receipt(cmd, result, ""); err != nil {
			return err
		}
		if result == "unknown" {
			_, err := e.db.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
			return err
		}
		return nil
	case contract.CommandKindSteer:
		var sp contract.SteerPayload
		perr := typedErr
		if perr == nil && cmd.Steer.Payload != nil {
			sp = *cmd.Steer.Payload
		}
		sent := false
		if l, ok := e.live[execution]; ok && l.wire != nil {
			w := l.wire
			w.mu.Lock()
			if perr == nil && w.session != nil && w.stdin != nil && sp.Text != "" {
				if frame := w.session.Steer(sp.Text, cid); frame != nil {
					_, err := writeFrame(w.stdin, w.log, frame)
					sent = err == nil
				}
			}
			w.mu.Unlock()
		}
		if sent {
			return nil
		}
		if perr != nil {
			if err := e.event(cmd, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid steer payload: " + perr.Error()}); err != nil {
				return err
			}
		}
		return e.finish(cmd, "failed", "")
	case contract.CommandKindReadiness:
		data := readiness(payload, e.host, os.Getenv("FLEET_NATIVE_ACCOUNTS"), e.forbidden())
		ok := data.Ok
		if err := e.event(cmd, 0, contract.FleetEventTypeReadiness, data); err != nil {
			return err
		}
		if !ok {
			if err := e.event(cmd, 1, contract.FleetEventTypeReadinessFailed, contract.ReadinessFailed{Reason: "readiness-check-failed"}); err != nil {
				return err
			}
		}
		return e.finish(cmd, okStatus(ok), "")
	case contract.CommandKindBaseUpdated:
		data, ok := checkoutSync(payload, e.host, projectRoots(), e.forbidden())
		k := contract.FleetEventTypeSyncFailed
		if ok {
			k = contract.FleetEventTypeCheckoutSynced
		}
		if err := e.event(cmd, 0, k, data); err != nil {
			return err
		}
		return e.finish(cmd, okStatus(ok), "")
	case contract.CommandKindTeardown:
		// Session finish: teardown script, keep on dirty/teardown failure, safe branch delete. Lookup only: never create a
		// worktree; the session dir must be a real dir under the executor or session roots. A payload that does not fit
		// its type deletes no branch.
		var tp contract.ProjectPayload
		if typedErr == nil && cmd.Teardown.Payload != nil {
			tp = *cmd.Teardown.Payload
		}
		key := workspaceKey(cmd)
		dir := e.commandDir(session, key)
		data := refusedTeardown("no-worktree")
		if project, err := projectContext(tp.Project); err != nil {
			data = refusedTeardown(err.Error())
		} else if e.insideDirs(dir) && exists(filepath.Join(dir, "worktree", ".git")) {
			var wd *string
			if s, ok := rawStr(tp.Rest["workingDirectory"]); ok {
				wd = &s
			}
			repo, hasRepo := repoPath(wd, project, e.host)
			_, top, created, ok, err := worktree(dir, workspaceSession(session, key), repo, hasRepo, projectRoots(), e.forbidden(), project, false)
			switch {
			case err != nil:
				data = refusedTeardown(err.Error())
			case ok && !created:
				data = teardown(dir, workspaceSession(session, key), top, project, tp.DeleteBranch != nil && *tp.DeleteBranch, tp.Branch != nil && !*tp.Branch)
			}
		}
		reason := str0(data.Reason)
		ok := data.Removed || reason == "uncommitted" || reason == "unpushed" || reason == "no-worktree"
		// The teardown command reuses the last execution, whose seq 0.. are taken by its turn: use a fresh seq.
		if err := e.event(cmd, uint64(nowMs()), contract.FleetEventTypeTeardown, data); err != nil {
			return err
		}
		return e.finish(cmd, okStatus(ok), "")
	case contract.CommandKindArtifactCopy, contract.CommandKindArtifactVerify:
		// Long transfers run on the transfer worker, which sends the final receipt; the journal row resumes them.
		b, _ := payload.(M) // open JSON: the transfer job is the payload as received, plus the command and spool
		b = maps.Clone(b)
		if b == nil {
			b = M{}
		}
		b["command"] = M{"id": cid, "executionId": execution}
		b["spool"] = filepath.Join(e.root, "spool")
		k := "verify"
		if kind == contract.CommandKindArtifactCopy {
			k = "copy"
		}
		if err := enqueue(e.db, cid, k, b); err != nil {
			return err
		}
		_, err := e.db.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
		return err
	case contract.CommandKindAccountInspect:
		return e.accountInspect(cmd)
	case contract.CommandKindConnectionRemove:
		if typedErr != nil || cmd.ConnectionRemove.Payload == nil || !removeConnection(connectionsDir(), cmd.ConnectionRemove.Payload.Connection) {
			return e.finish(cmd, "failed", "")
		}
		return e.finish(cmd, "completed", "")
	case contract.CommandKindScript:
		return e.script(cmd, execution, session)
	case contract.CommandKindStart, contract.CommandKindInput:
		p, _ := payload.(M) // open JSON: the harness adapters still read the start payload as a map
		return e.start(cmd, cid, execution, session, p)
	}
	return e.finish(cmd, "failed", "")
}

// commandID and sessionID: the live command's identities, checked when it ran ("" for one that is not).
func (l *live) commandID() string { s, _ := idOf(l.command, "id"); return s }
func (l *live) sessionID() string { s, _ := idOf(l.command, "sessionId"); return s }

// typedCommand: a decoded command as its contract type (commandOf of its journal body).
func typedCommand(c M) contract.Command { return commandOf(enc(c)) }

// commandOf: a journal body as its contract type; one that does not fit its kind's type keeps only Raw (no member set),
// so its identities are still read from the JSON.
func commandOf(body string) contract.Command {
	var cmd contract.Command
	if json.Unmarshal([]byte(body), &cmd) != nil {
		return contract.Command{Raw: json.RawMessage(body)}
	}
	return cmd
}

// commandKind: the kind of a command's JSON, "" when it names none.
func commandKind(body string) contract.CommandKind {
	var k struct {
		Kind contract.CommandKind `json:"kind"`
	}
	json.Unmarshal([]byte(body), &k)
	return k.Kind
}

// payloadOf decodes a command payload into its per-kind contract type (an absent or null payload is the zero value).
func payloadOf[T any](c contract.Command) (T, error) {
	var v T
	raw := c.Raw
	if raw == nil {
		raw, _ = json.Marshal(c)
	}
	var x struct {
		Payload json.RawMessage `json:"payload"`
	}
	json.Unmarshal(raw, &x)
	if len(x.Payload) > 0 && string(x.Payload) != "null" {
		err := json.Unmarshal(x.Payload, &v)
		return v, err
	}
	return v, nil
}

// str0 is the value of an optional string field, "" when absent.
func str0(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func okStatus(ok bool) string {
	if ok {
		return "completed"
	}
	return "failed"
}

func (e *executor) start(c contract.Command, cid, execution, session string, p M) error {
	// A session with a recorded launch is running, or its process from before a restart is not confirmed gone.
	var busy string
	err := e.db.QueryRow("SELECT execution FROM launches WHERE session=? LIMIT 1", session).Scan(&busy)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if busy != "" {
		reason := "session busy: execution " + busy + " is live, or its process from before an executor restart is not confirmed gone"
		if _, live := e.live[busy]; !live {
			reason += "; a stop for execution " + busy + " accepts the uncertainty and releases the session"
		}
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	sp, err := payloadOf[contract.StartPayload](c)
	if err != nil {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid start payload: " + err.Error()}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	cfg, hasCfg := p["agentConfig"] // the profile as received: evidence and hashes keep its bytes
	var profile contract.Profile
	if hasCfg {
		if profile, err = contract.ParseProfile([]byte(enc(cfg))); err != nil {
			return e.finish(c, "failed", "")
		}
	}
	name := harnessName(sp)
	key := str0(sp.WorkspaceKey)
	dir := e.commandDir(session, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !e.insideDirs(dir) {
		return e.finish(c, "failed", "")
	}
	if err := os.WriteFile(filepath.Join(dir, ".fleet-instructions.tmp"), []byte(str0(sp.Instructions)), 0o644); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(dir, ".fleet-instructions.tmp"), filepath.Join(dir, "AGENTS.md")); err != nil {
		return err
	}
	// The coordinator owns the native id per harness:account continuation; no id means a new native session.
	native := str0(sp.NativeSessionId)
	if sp.NativeSessionId != nil && !safeNativeID(native) {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid native session id"}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	// failSeq: the first event seq a failure may use; the setup event takes 1, so a failure after it uses 2 (two events
	// with one id are a conflict the coordinator refuses, and the failure reason was lost that way).
	failSeq := uint64(1)
	fail := func(seq uint64, reason string) error {
		seq = max(seq, failSeq)
		if err := e.event(c, seq, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		return e.finish(c, "failed", native)
	}
	var evidence M
	selectedMCP := M{}
	if hasCfg {
		ev, selected, err := profilePrepare(cfg, str0(sp.Account), p["skills"])
		if err != nil {
			return fail(1, err.Error())
		}
		evidence = ev
		selectedMCP = selected
	}
	h := Harnesses[name]
	if h == nil {
		return fail(1, "unknown harness")
	}
	attachTurn(p, filepath.Join(dir, "input"), name) // the same folder as FLEET_INPUT_DIR below
	cmd, nativeInput, err := h.Command(dir, native, p)
	if err != nil {
		return fail(1, err.Error())
	}
	fleetTools := profile.FleetTools == nil || *profile.FleetTools
	collaboration := profile.CollaborationTools == nil || *profile.CollaborationTools
	if name == "codex" && !collaboration {
		cmd.Args = append(cmd.Args, "--disable", "multi_agent", "--disable", "multi_agent_v2")
	}
	project, err := projectContext(sp.Project)
	if err != nil {
		return fail(1, err.Error())
	}
	repo, hasRepo := repoPath(sp.WorkingDirectory, project, e.host)
	checkoutTop := ""
	w, top, created, hasW, err := worktree(dir, workspaceSession(session, key), repo, hasRepo, projectRoots(), e.forbidden(), project, sp.Branch != nil && !*sp.Branch)
	if err != nil {
		return fail(1, err.Error())
	}
	if hasW {
		checkoutTop = top
		if created && project.Scripts != nil && project.Scripts.Setup != nil && str0(sp.Setup) != "skip" {
			r := projectScript("setup", *project.Scripts.Setup, top, w, session)
			failSeq = 2
			if err := e.event(c, 1, contract.FleetEventTypeSetup, r); err != nil {
				return err
			}
			if !r.Ok {
				return fail(2, "setup-failed")
			}
		}
		cmd.Dir = w
	}
	work := dir
	if hasW {
		work = w
	}
	// Output collected when the run ends, and the folder artifact_get downloads into.
	outputDir, inputDir := filepath.Join(work, "output"), filepath.Join(dir, "input")
	os.MkdirAll(outputDir, 0o755)
	os.MkdirAll(inputDir, 0o755)
	setEnvKV(cmd, "FLEET_OUTPUT_DIR="+outputDir, "FLEET_INPUT_DIR="+inputDir)
	if err := e.stageInputs(p, inputDir, execution); err != nil {
		return fail(1, err.Error())
	}
	resolved, err := resolveAttemptContext(project, e.host, projectRoots(), e.forbidden(), inputDir)
	if err != nil {
		return fail(1, err.Error())
	}
	p, evidence = prepareInstructions(cmd, name, p, str0(sp.Instructions), resolved, evidence)
	if err := e.event(c, 0, contract.FleetEventTypePreparation, evidence); err != nil {
		return err
	}
	var gitAccess []string
	if sp.Project != nil {
		// Repo of this host's environment (env.repo), else the project's single/first repo.
		var envRepo *string
		for _, en := range project.Environments {
			if str0(en.HostId) == e.host {
				envRepo = en.Repo
				break
			}
		}
		access, err := gitEnv(project, gitRepo(project, envRepo), connectionsDir())
		if err != nil {
			return fail(1, err.Error())
		}
		gitAccess = access
		gitApply(cmd, access)
	}
	requirements := contract.ProjectExecution{}
	if r := projectRequirements(project, profile.Id); r != nil {
		requirements = *r
	}
	for _, tool := range requirements.Tools {
		var err error
		if tool == "browser" {
			err = browserReady(p, commandHome(cmd))
		} else {
			err = codingTool(tool, commandHome(cmd))
		}
		if err != nil {
			return fail(1, "coding-tool-unready: "+tool)
		}
	}
	observed := e.observeHarness(cmd, name)
	servers := mcpAtRoot(selectedMCP, work)
	for _, tool := range requirements.Tools {
		if tool == "browser" {
			cfg, err := browserConfig(p)
			if err != nil {
				return fail(1, "browser-binding-unready")
			}
			servers["project-browser"] = M{"command": exe(), "args": []any{"mcp", "project-browser", enc(cfg)}, "tools": []any{"navigate", "read", "evaluate", "click", "fill", "close"}, "dependencies": M{"fleet-browser-chromium": os.Getenv("FLEET_BROWSER_EXECUTABLE")}, "ownedDependencies": []any{"fleet-browser-chromium"}}
		}
	}
	if requirements.Forge != nil && *requirements.Forge {
		if reason := forgeReadiness(primaryRemote(project)); reason != "" {
			return fail(1, reason)
		}
		repo := gitRepo(project, nil)
		if repo == nil {
			repo = &contract.ProjectRepo{}
		}
		remote := forgeRepo(str0(repo.Remote))
		store := os.Getenv("FLEET_FORGE_CONFIG_DIR")
		branchBytes, err := exec.Command("git", "-C", work, "symbolic-ref", "--short", "HEAD").Output()
		if err != nil || remote == "" || !forgeGranted(remote) || !filepath.IsAbs(store) {
			return fail(1, "project-forge-unready")
		}
		var prefix, mergeMethod string
		var autoMerge bool
		if project.Worktree != nil {
			prefix = str0(project.Worktree.BranchPrefix)
		}
		if in := project.Integration; in != nil {
			mergeMethod = str0(in.MergeMethod)
			if in.AutoMerge != nil {
				autoMerge = *in.AutoMerge
			}
		}
		context := forgeContext{Repositories: os.Getenv("FLEET_FORGE_REPOSITORIES"), CredentialHome: os.Getenv("HOME"), Repo: remote, Base: str0(repo.DefaultBranch), Branch: strings.TrimSpace(string(branchBytes)), Prefix: prefix, AutoMerge: looseBool(autoMerge), MergeMethod: mergeMethod, Workdir: work, Home: commandHome(cmd), Store: store}
		servers["project-forge"] = M{"command": exe(), "args": []any{"mcp", "project-forge", enc(context)}, "tools": []any{"publish_draft", "delivery"}}
	}
	profiles := anyStrings(profile.DelegationProfiles)
	fleetMCP := []any{}
	var codeFleetTools *codeFleet
	if fleetTools {
		var projectID any
		if sp.ProjectId != nil {
			projectID = *sp.ProjectId
		}
		context := M{"hostId": e.host, "commandId": cid, "executionId": execution, "profiles": profiles, "projectId": projectID, "workdir": work, "inputDir": inputDir, "spool": filepath.Join(e.root, "spool"), "operations": p["fleetOperations"]}
		journal := filepath.Join(e.root, "journal.sqlite")
		if err := os.MkdirAll(filepath.Join(e.root, "runs"), 0o700); err != nil {
			return err
		}
		server, err := delegationServer(context, journal, fleetContextFile(e.root, execution))
		if err != nil {
			return fail(1, "fleet MCP context: "+err.Error())
		}
		codeFleetTools = &codeFleet{journal, context}
		fleetMCP = jArr(server, "tools")
		servers["fleet"] = server
	}
	observed["fleetTools"], observed["collaborationTools"] = fleetTools, collaboration
	observed["cwd"], observed["worktree"], observed["platform"] = work, hasW, platform()
	// Granted connections: one `<executor> mcp connection` server each, calling fleet-gateway with this run's token. The
	// token and gateway URL reach the servers through the private run file below, never the harness environment or argv.
	var connectionTools []string
	runFile, launched := "", false
	defer func() {
		if runFile != "" && !launched {
			os.Remove(runFile) // K2-6: a launch that failed never needs its run token
		}
		if !launched {
			os.Remove(codeSpecFile(e.root, execution))     // nor its code server spec
			os.Remove(fleetContextFile(e.root, execution)) // nor its Fleet server context
		}
	}()
	granted := sp.Connections
	if len(granted) > 0 {
		gateway := os.Getenv("FLEET_GATEWAY_URL")
		if gateway == "" || sp.RunToken == nil {
			return fail(1, "connections granted but FLEET_GATEWAY_URL or the run token is missing")
		}
		// The run token, gateway URL and Access file path reach only the connection servers, through a private run file
		// in the executor's data directory; the harness environment never carries them. The work and input folders are
		// the base of relative upload paths and the parent of downloaded attachments (fleet-connections.md §9).
		if strings.ContainsAny(work+inputDir+gateway+str0(sp.RunToken)+accessFile(), "\n\r") {
			return fail(1, "a run file value holds a line break") // it would add a key line of its own
		}
		lines := "FLEET_RUN_TOKEN=" + str0(sp.RunToken) + "\nFLEET_GATEWAY_URL=" + gateway + "\nFLEET_WORK_DIR=" + work + "\nFLEET_INPUT_DIR=" + inputDir + "\n"
		if f := accessFile(); f != "" && isFile(f) {
			lines += "FLEET_CF_ACCESS_FILE=" + f + "\n"
		}
		lines += uploadDenyLines(e.root)
		dir := filepath.Join(e.root, "runs")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		runFile = filepath.Join(dir, execution+".env")
		if err := os.WriteFile(runFile, []byte(lines), 0o600); err != nil {
			return err
		}
	}
	gatewayConns := map[string]bool{}
	codeMode := ""
	if profile.CodeMode != nil {
		codeMode = string(*profile.CodeMode)
	}
	var bridged []any // the granted connections with the tools their direct servers expose, for code mode
	for _, raw := range granted {
		// A grant names its connection and tools; the rest of the spec reaches the connection server as sent.
		var grant struct {
			Name  json.RawMessage `json:"name"`
			Tools json.RawMessage `json:"tools"`
		}
		json.Unmarshal(raw, &grant)
		conn, _ := rawStr(grant.Name)
		if conn == "" || !onlyBytes(conn, func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' }) {
			continue
		}
		var grantTools []json.RawMessage
		if len(grant.Tools) > 0 && grant.Tools[0] == '[' {
			json.Unmarshal(grant.Tools, &grantTools)
		}
		tools, specs := []any{}, []any{}
		for _, t := range grantTools {
			var tool struct {
				Name json.RawMessage `json:"name"`
			}
			json.Unmarshal(t, &tool)
			if n, _ := rawStr(tool.Name); n != "" && onlyBytes(n, func(b byte) bool { return alnum(b) || b == '_' }) {
				tools, specs = append(tools, n), append(specs, decRaw(t))
				connectionTools = append(connectionTools, "mcp__"+conn+"__"+n)
			}
		}
		if codeMode != "" {
			b := decM(string(raw))
			b["tools"] = specs
			bridged = append(bridged, b)
		}
		if _, taken := servers[conn]; taken {
			return fail(1, "MCP server name collides with a granted connection")
		}
		servers[conn] = M{"command": exe(), "args": []any{"mcp", "connection", conn, enc(decRaw(raw)), runFile}, "tools": tools, "env": []any{"HOME"}}
		gatewayConns[conn] = true
	}
	// Code mode (docs/designs/fleet-code-mode.md): one `code` server bridges the fleet server and the connection servers.
	// gitNetwork: Codex's sandbox network on and the checkout's common .git writable; code mode's sh gets the same.
	var gitRoots []any
	if name == "codex" && sp.GitNetwork != nil && *sp.GitNetwork && checkoutTop != "" {
		if gitRoots, err = nativeGitRoots(checkoutTop, work); err != nil {
			return fail(1, err.Error())
		}
	}
	if codeMode != "" {
		auth := codeAuthorityFor(name, cfg, work, []string{work, inputDir}, gitRoots, sp.GitNetwork != nil && *sp.GitNetwork, sp.ReadOnly != nil && *sp.ReadOnly, e.root)
		auth.env = M{"FLEET_INPUT_DIR": inputDir, "FLEET_OUTPUT_DIR": outputDir}
		if sp.Project != nil {
			auth.gitEnv = anyStrings(gitAccess)
		}
		// The worktree's Git directories stay readable to a sandboxed sh that hides the data directory. They come from the
		// checkout the executor made, now, never from .git files the sandboxed commands can rewrite.
		if jAt(auth.shell, "sandbox") == true && checkoutTop != "" {
			roots := gitRoots
			if roots == nil {
				roots, _ = nativeGitRoots(checkoutTop, work) // on a failure no extra read: sh lacks Git, nothing more
			}
			auth.shell["read"] = append(jArr(auth.shell, "read"), roots...)
		}
		if jAt(auth.shell, "sandbox") == true {
			auth.shell["pinned"] = true
			workRoot, err := canon(work)
			if err != nil {
				return fail(1, "code-mode work root: "+err.Error())
			}
			for _, key := range []string{"read", "write", "hide"} {
				var paths []any
				for _, path := range jArr(auth.shell, key) {
					s, _ := str(path) // the roots above are strings
					c, err := canon(s)
					if err != nil {
						return fail(1, "code-mode shell root: "+err.Error())
					}
					// The work root already covers its children. Avoid a separate bind of a movable descendant.
					if key == "read" && c != workRoot && under(c, workRoot) {
						continue
					}
					paths = append(paths, c)
				}
				auth.shell[key] = paths
			}
		}
		if err := addCodeServer(servers, codeMode, codeFleetTools, bridged, runFile, codeSpecFile(e.root, execution), work, auth); err != nil {
			return fail(1, err.Error())
		}
		observed["codeMode"], observed["codeTools"] = codeMode, jAt(servers, "code", "tools")
		if codeMode == "only" {
			connectionTools, fleetMCP = nil, []any{} // the fleet server is gone; its tools are reached through code
			if name == "codex" {
				// The shell is hidden: sh in code replaces it. Codex 0.167 has no switch for apply_patch.
				cmd.Args = append(cmd.Args, "--disable", "shell_tool", "--disable", "unified_exec")
			}
		}
	}
	// Codex: the fleet server's tools; other harnesses: the launch's servers without the connection servers.
	if name == "codex" {
		observed["fleetMcpTools"] = fleetMCP
	} else {
		var direct []string
		for _, k := range sortedKeys(servers) {
			if !gatewayConns[k] {
				direct = append(direct, k)
			}
		}
		observed["fleetMcpTools"] = anyStrings(direct)
	}
	// Built-in web for pipeline agent steps: off unless the step's run sets webRoute builtin.
	if name == "codex" && sp.WebRoute != nil {
		if str0(sp.WebRoute) == "builtin" {
			cmd.Args = append(cmd.Args, "-c", `web_search="live"`)
		} else {
			cmd.Args = append(cmd.Args, "-c", `web_search="disabled"`)
		}
	}
	extra := connectionTools
	if name == "claude" {
		if str0(sp.WebRoute) == "builtin" {
			extra = append(extra, "WebSearch", "WebFetch")
		}
		if len(sp.Skills) > 0 {
			extra = append(extra, "Skill")
		}
	}
	var mcp *mcpOwner
	if len(servers) > 0 {
		mcp, err = newMCPOwner(e.db, execution)
		if err != nil {
			return err
		}
		defer func() {
			if !launched {
				mcp.close()
				os.RemoveAll(mcp.dir)
			}
		}()
		receiptRoot, err := canon(dir)
		if err != nil {
			return err
		}
		servers, err = wrapMCPServers(servers, mcp.socket, work, filepath.Join(receiptRoot, execution+".mcp"))
		if err != nil {
			return err
		}
	}
	h.WireMCP(cmd, p, servers, extra)
	root := dir
	if hasW {
		root = w
	}
	pp := p
	if gitRoots != nil {
		pp = maps.Clone(p) // a top-level key only
		pp["writableRoots"] = gitRoots
	}
	cmd, sess := h.Protocol(cmd, root, native, pp, servers)
	protocol := sess != nil
	var stdin io.WriteCloser
	if nativeInput != nil || protocol {
		if stdin, err = cmd.StdinPipe(); err != nil {
			return err
		}
	}
	stderr, err := os.Create(filepath.Join(dir, execution+".stderr"))
	if err != nil {
		return err
	}
	defer stderr.Close()
	cmd.Stderr = stderr
	// Our own pipe, so Wait never closes the read side and an exit is seen while a descendant holds stdout.
	pr, pw, err := os.Pipe()
	if err != nil {
		return err
	}
	cmd.Stdout = pw
	rawLog, err := os.Create(filepath.Join(dir, execution+".native.jsonl"))
	if err != nil {
		pr.Close()
		pw.Close()
		return err
	}
	var outLog *os.File
	if protocol {
		if outLog, err = os.OpenFile(filepath.Join(dir, execution+".protocol-out.jsonl"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600); err != nil {
			pr.Close()
			pw.Close()
			rawLog.Close()
			return err
		}
	}
	closeAll := func() {
		pr.Close()
		rawLog.Close()
		if outLog != nil {
			outLog.Close()
		}
	}
	if err := e.launch(execution, cid, session); err != nil {
		pw.Close()
		closeAll()
		return err
	}
	if mcp != nil {
		if _, err = e.db.Exec("UPDATE launches SET mcp_dir=? WHERE execution=?", mcp.dir, execution); err != nil {
			return err
		}
	}
	// The identity is read before the reap: a harness that exits at once is still a zombie then.
	var ident string
	var identErr error
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd, Started: func(pid int) { ident, identErr = identity(pid) }})
	if err != nil {
		pw.Close()
		closeAll()
		e.release(execution)
		return fail(1, "harness did not start: "+err.Error())
	}
	pw.Close()
	l := &live{mcp: mcp, cmd: cmd, proc: proc, exited: proc.Done(), command: c, native: native, seq: 2, owned: map[int]string{}, metrics: newRunMetrics(gatewayConns), started: time.Now()}
	kill := func() { proc.Kill(); proc.Wait(); closeAll(); e.release(execution) }
	if nativeInput != nil && !protocol {
		_, err := stdin.Write(nativeInput)
		stdin.Close()
		if err != nil {
			kill()
			return e.finish(c, "unknown", native)
		}
	}
	if identErr != nil {
		kill()
		return e.finish(c, "unknown", native)
	}
	l.owned[cmd.Process.Pid] = ident
	_ = l.observe()
	// Harness commands run in their own process group (newCmd): a restart can also kill descendants not yet observed.
	if err := e.ownGroup(execution, cmd.Process.Pid, l); err != nil {
		kill()
		return e.finish(c, "unknown", native)
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
			kill()
			return e.finish(c, "unknown", native)
		}
		lines <- enc(meta)
	}
	acpProtocol := acp(name)
	wire := l.wire
	errtrack.Go("harness.stdout", func() { readStdout(pr, rawLog, wire, acpProtocol, lines) })
	e.live[execution], launched = l, true
	l.seq++
	if err := e.event(c, l.seq, contract.FleetEventTypeHarness, observed); err != nil {
		return err
	}
	return e.receipt(c, "started", native)
}

// stderrTail: the end of a run's stderr file when it exited non-zero; nil otherwise. Harness stderr carries no
// credentials (they reach the harness through its own login files, never argv or stderr echo).
func stderrTail(dir, execution string, code *int64) *string {
	if code == nil || *code == 0 {
		return nil
	}
	b, err := os.ReadFile(filepath.Join(dir, execution+".stderr"))
	if err != nil || len(b) == 0 {
		return nil
	}
	t := tail(b[max(0, len(b)-2000):])
	return &t
}

func anyStrings(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

// protocolFailed is the stdout line of a protocol.failed event (contract.ProtocolFailed).
func protocolFailed(msg, reason string) string {
	t := string(contract.FleetEventTypeProtocolFailed)
	v := contract.ProtocolFailed{Type: &t, Error: msg}
	if reason != "" {
		v.Reason = &reason
	}
	return enc(v)
}

// readStdout journals every stdout line (raw log, fsync per line), then either passes it on or feeds the protocol.
func readStdout(r *os.File, rawLog *os.File, w *wire, acpProtocol bool, out chan<- string) {
	defer close(out)
	defer r.Close()
	defer rawLog.Close()
	acpComplete := false
	sc := bufio.NewReader(r)
	for {
		line, err := sc.ReadString('\n')
		if line == "" && err != nil {
			break
		}
		line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
		if _, werr := rawLog.WriteString(line + "\n"); werr != nil || rawLog.Sync() != nil {
			break
		}
		if w == nil {
			out <- line
			if err != nil {
				break
			}
			continue
		}
		w.mu.Lock()
		if w.session == nil || w.stdin == nil {
			w.mu.Unlock()
			continue
		}
		v, derr := dec(line)
		frame, isObj := v.(M)
		var requests []map[string]any
		var event map[string]any
		var done bool
		if derr == nil && isObj {
			requests, event, done, derr = w.session.Receive(frame)
		} else if derr == nil {
			derr = errors.New("frame is not an object")
		}
		if derr != nil {
			w.mu.Unlock()
			out <- protocolFailed("native protocol outcome unconfirmed; no replay", derr.Error())
			return
		}
		for _, req := range requests {
			meta, err := writeFrame(w.stdin, w.log, req)
			if err != nil {
				w.mu.Unlock()
				out <- protocolFailed("outbound delivery unknown; no replay", "")
				return
			}
			out <- enc(meta)
		}
		if event != nil {
			out <- enc(event)
		}
		if done {
			acpComplete = true
			w.stdin.Close()
			w.stdin = nil
		}
		w.mu.Unlock()
		if err != nil {
			break
		}
	}
	if acpProtocol && !acpComplete {
		out <- protocolFailed("ACP EOF before terminal outcome; no replay", "")
	}
}

// script: a lab script step: `sh -c <command>` in its own process group, in the trial's own worktree at the pinned commit
// (or the session dir when the payload names no repository). Stop and timeout kill the whole group. Full stdout and stderr
// (output/script.stdout, output/script.stderr) and everything in FLEET_OUTPUT_DIR become artifacts.
func (e *executor) script(c contract.Command, execution, session string) error {
	dir := e.sessionDir(session)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if !e.insideDirs(dir) {
		return e.finish(c, "failed", "")
	}
	refuse := func(reason string) error {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	sp, err := payloadOf[contract.ScriptPayload](c)
	if err != nil {
		return refuse("invalid script payload: " + err.Error())
	}
	command := sp.Command
	if command == "" {
		return refuse("script command missing")
	}
	var timeout time.Duration
	if sp.TimeoutMs != nil {
		if *sp.TimeoutMs <= 0 || *sp.TimeoutMs > math.MaxInt64/int64(time.Millisecond) {
			return refuse("timeout_ms must be a positive integer")
		}
		timeout = time.Duration(*sp.TimeoutMs) * time.Millisecond
	}
	output, input := filepath.Join(dir, "output"), filepath.Join(dir, "input")
	if err := os.MkdirAll(output, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(input, 0o755); err != nil {
		return err
	}
	if err := e.stageInputs(M{"inputs": sp.Inputs}, input, execution); err != nil {
		return refuse(err.Error())
	}
	stdout, err := os.Create(filepath.Join(output, "script.stdout"))
	if err != nil {
		return err
	}
	defer stdout.Close()
	stderr, err := os.Create(filepath.Join(output, "script.stderr"))
	if err != nil {
		return err
	}
	defer stderr.Close()
	project, err := projectContext(sp.Project)
	if err != nil {
		return refuse(err.Error())
	}
	var wd *string
	if s, ok := rawStr(sp.Rest["workingDirectory"]); ok {
		wd = &s
	}
	repo, hasRepo := repoPath(wd, project, e.host)
	work := dir
	var trial *[2]string
	switch {
	case hasRepo && sp.Commit != nil:
		w, top, err := trialWorktree(dir, repo, *sp.Commit, projectRoots(), e.forbidden())
		if err != nil {
			return refuse(err.Error())
		}
		work, trial = w, &[2]string{top, w}
	case !hasRepo && sp.Commit == nil:
	default:
		return refuse("script needs both a repository and a commit, or neither")
	}
	dropTrial := func() {
		if trial != nil {
			removeTrial(trial[0], trial[1])
		}
	}
	mcp, err := newMCPOwner(e.db, execution)
	if err != nil {
		dropTrial()
		return err
	}
	admitted := false
	defer func() {
		if !admitted {
			mcp.close()
			os.RemoveAll(mcp.dir)
		}
	}()
	cmd := cleanCmd("sh", "-c", command)
	cmd.Dir = work
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(cmd.Env, "FLEET_INPUT_DIR="+input, "FLEET_OUTPUT_DIR="+output, "FLEET_SESSION_ID="+session, "FLEET_EXECUTION_ID="+execution,
		// Lab scripts ship in the release next to the binary (fleet-asset-pipelines §4); Blender is a host setting.
		"FLEET_LAB_SCRIPTS="+filepath.Join(filepath.Dir(exe()), "scripts", "lab"), "FLEET_MCP_OWNER="+mcp.socket)
	if v := os.Getenv("FLEET_BLENDER"); v != "" {
		cmd.Env = append(cmd.Env, "FLEET_BLENDER="+v)
	}
	cid, _ := idOf(c, "id")
	if err := e.launch(execution, cid, session); err != nil {
		dropTrial()
		return err
	}
	if _, err = e.db.Exec("UPDATE launches SET mcp_dir=? WHERE execution=?", mcp.dir, execution); err != nil {
		return err
	}
	// The identity is read before the reap: a script that exits at once is still a zombie then.
	var ident string
	var identErr error
	proc, err := supervise.Spawn(supervise.Spec{Cmd: cmd, Started: func(pid int) { ident, identErr = identity(pid) }})
	if err != nil {
		dropTrial()
		e.release(execution)
		return refuse("script spawn failed: " + err.Error())
	}
	pid := proc.Pid
	l := &live{mcp: mcp, cmd: cmd, proc: proc, exited: proc.Done(), command: c, seq: 2, completed: true, owned: map[int]string{}, started: time.Now(), group: pid, trial: trial}
	if identErr != nil {
		proc.Kill()
		proc.Wait()
		dropTrial()
		e.release(execution)
		return e.finish(c, "unknown", "")
	}
	l.owned[pid] = ident
	_ = l.observe()
	if err := e.ownGroup(execution, pid, l); err != nil {
		proc.Kill()
		proc.Wait()
		dropTrial()
		e.release(execution)
		return e.finish(c, "unknown", "")
	}
	mcp.readyOnce.Do(func() { close(mcp.ready) })
	admitted = true
	// No stdout stream to read: the closed channel marks EOF at once.
	lines := make(chan string)
	close(lines)
	l.lines = lines
	if timeout > 0 {
		l.deadline, l.timeoutMs = time.Now().Add(timeout), sp.TimeoutMs
	}
	e.live[execution] = l
	if err := e.event(c, 1, contract.FleetEventTypeScriptStarted, contract.ScriptStarted{Commit: sp.Commit, TimeoutMs: sp.TimeoutMs, TrialWorktree: work}); err != nil {
		return err
	}
	return e.receipt(c, "started", "")
}

// stageInputs: payload inputs [{artifactId, path}] are downloaded to FLEET_INPUT_DIR/<path> before the step starts.
// Paths are relative and never leave the input folder.
func (e *executor) stageInputs(p M, input string, execution any) error {
	var in struct {
		Inputs []contract.InputArtifact `json:"inputs"`
	}
	if json.Unmarshal([]byte(enc(p)), &in) != nil {
		return errors.New("invalid input")
	}
	list := in.Inputs
	if len(list) == 0 {
		return nil
	}
	root, err := os.OpenRoot(input)
	if err != nil {
		return fmt.Errorf("input folder: %w", err)
	}
	defer root.Close()
	c := newTransferClient(e.url, e.token, e.host, accessToken(accessFile()))
	executionID, _ := execution.(string)
	for _, i := range list {
		id, rel := i.ArtifactId, i.Path
		if strings.HasPrefix(rel, "/") || anyPart(rel, func(x string) bool { return x == "" || x == "." || x == ".." }) {
			return fmt.Errorf("invalid input path %s", rel)
		}
		r, f := transferReply(c, contract.RouteArtifactsGet, contract.HostArtifactGetRequest{HostId: e.host, ExecutionId: optStr(executionID), ArtifactId: id})
		if f != nil {
			return fmt.Errorf("input %s: %s", rel, f.msg)
		}
		if r.Url == nil {
			return fmt.Errorf("input %s unavailable", rel)
		}
		// Every component is resolved inside the held input root (os.Root): a link whose target leaves the input folder,
		// present or swapped in at any moment, is refused, so an input can only be created inside the input folder.
		if err := root.MkdirAll(filepath.Dir(rel), 0o755); err != nil {
			return fmt.Errorf("input %s: %w", rel, err)
		}
		out, err := root.OpenFile(rel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if err != nil {
			return fmt.Errorf("input %s: %w", rel, err)
		}
		src, f := c.get(*r.Url)
		if f == nil {
			_, err = io.Copy(out, src)
			src.Close()
		} else {
			err = errors.New(f.msg)
		}
		out.Close()
		if err != nil {
			root.Remove(rel) // no partial input left behind
			return fmt.Errorf("input %s: %w", rel, err)
		}
	}
	return nil
}

// tickEvery: the process loop period while the coordinator holds the poll (README, Side effects: observe()).
const tickEvery = 100 * time.Millisecond

// tickFailed: a process-loop failure during the held poll; the main loop treats it as fatal, like its own tick.
type tickFailed struct{ error }

func (t tickFailed) Unwrap() error { return t.error }

// heldPoll waits for the coordinator's held poll while this goroutine keeps ticking, so a process exit, its output,
// a script deadline and resource samples never wait up to longPollWait for the answer. It ticks only while
// executions are live; an idle host keeps one update drain and journal count per exchange. A tick failure cancels
// the poll, waits for its goroutine and drops any reply: the executor stops, and commands without a receipt stay
// pending on the coordinator (host_poll.go selects status 'pending') for delivery again.
func (e *executor) heldPoll() (*pollReply, error) {
	ctx, cancel := context.WithCancel(e.baseCtx())
	defer cancel()
	type answered struct {
		reply *pollReply
		err   error
	}
	got := make(chan answered, 1)
	errtrack.Go("poll.held", func() {
		reply, err := e.pollWait(ctx, longPollWait)
		got <- answered{reply, err}
	})
	beat := time.NewTicker(tickEvery)
	defer beat.Stop()
	for {
		select {
		case a := <-got:
			return a.reply, a.err
		case <-beat.C:
			if len(e.live) == 0 {
				continue
			}
			if err := e.tick(); err != nil {
				cancel()
				<-got
				return nil, tickFailed{err}
			}
		}
	}
}

func anyPart(rel string, bad func(string) bool) bool {
	for _, x := range strings.Split(rel, "/") {
		if bad(x) {
			return true
		}
	}
	return false
}

func (e *executor) tick() error {
	for execution, l := range e.live {
		done, err := e.tickOne(execution, l)
		if err != nil {
			return err
		}
		if done {
			if err := e.release(execution); err != nil {
				return err
			}
			delete(e.live, execution)
		}
	}
	return e.drainUpdate()
}

func (e *executor) tickOne(execution string, l *live) (bool, error) {
	if l.observe() != nil {
		l.cleanupUnknown = true
	}
	if err := e.own(execution, l); err != nil {
		return false, err
	}
	exited := l.exitedNow()
	if !exited && !l.stopped && !l.deadline.IsZero() && !time.Now().Before(l.deadline) {
		l.timedOut, l.stopped = true, true
		l.seq++
		if err := e.event(l.command, l.seq, contract.FleetEventTypeScriptTimeout, contract.ScriptTimeout{TimeoutMs: l.timeoutMs}); err != nil {
			return false, err
		}
	}
	if exited || l.stopped {
		if l.mcp != nil {
			if err := l.mcp.close(); err != nil {
				return false, err
			}
		}
		if err := cleanupMCPGroups(e.db, execution); err != nil {
			l.cleanupUnknown = true
			if _, open := e.commandBody(l.commandID(), true); open {
				l.seq++
				if reportErr := e.nativeEvent(l.command, l.seq, "mcp.cleanup_unknown", M{"reason": err.Error(), "ownershipRetained": true}); reportErr != nil {
					return false, reportErr
				}
				if reportErr := e.finish(l.command, "unknown", l.native); reportErr != nil {
					return false, reportErr
				}
			}
			return false, err
		}
		if l.cleanup() != nil {
			l.cleanupUnknown = true
		}
	}
	name := commandHarness(l.command)
	h := Harnesses[name]
	if l.blocks == nil && NewBlocks != nil {
		l.blocks = NewBlocks()
	}
	for drained := false; !drained; {
		select {
		case line, ok := <-l.lines:
			if !ok {
				l.eof, drained = true, true
				break
			}
			v, err := dec(line)
			event, isObj := v.(M)
			if err != nil || !isObj {
				event = M{"type": "unparsed", "raw": line}
			}
			if h != nil {
				event = h.Normalize(event)
			}
			atMs := nowMs()
			if l.metrics != nil {
				l.metrics.observeEvent(name, event, atMs)
			}
			projection := M{}
			if l.blocks != nil {
				projection = l.blocks.Project(name, event, uint64(atMs))
			}
			// A harness may repeat its native identity on every event. Publish the first
			// observation and changes so ordinary event traffic can batch in the outbox.
			if n := str0(typed[struct {
				Session *string `json:"session"`
			}](projection).Session); n != "" && (!l.nativeObserved || n != l.native) {
				sid, err := idOf(l.command, "sessionId")
				if err != nil {
					return false, err
				}
				if _, err := e.db.Exec("INSERT OR REPLACE INTO sessions(id,native) VALUES(?,?)", sid, n); err != nil {
					return false, err
				}
				if err := e.receipt(l.command, "started", n); err != nil {
					return false, err
				}
				l.native, l.nativeObserved = n, true
			}
			kind := "unknown"
			ev := typed[harnessEvent](event)
			if k, ok := valStr(ev.Type); ok {
				kind = k
			}
			if kind == "steer.result" {
				steer := typed[steerResult](event)
				if b, ok := e.commandBody(str0(steer.CommandID), true); ok {
					if err := e.finish(commandOf(b), okStatus(steer.Ok != nil && *steer.Ok), ""); err != nil {
						return false, err
					}
				}
			}
			if kind == string(contract.FleetEventTypeProtocolFailed) && !l.stopped {
				l.cleanupUnknown, l.stopped = true, true
			}
			if projection["completed"] == true {
				l.completed = true
			}
			l.seq++
			retained := maps.Clone(event) // the record as normalized, plus its projection
			retained["fleetNormalized"] = projection
			if err := e.nativeEvent(l.command, l.seq, kind, retained); err != nil {
				return false, err
			}
		default:
			drained = true
		}
	}
	if exited {
		alive, err := l.aliveOwned()
		var unknown identityUnknown
		if errors.As(err, &unknown) {
			// Not confirmed gone: the run stays live (and its session held) until the process can be seen to leave.
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if alive {
			return false, nil
		}
	}
	// Wait for stdout EOF before reaping to retain partial records.
	if !l.eof || !exited {
		return false, nil
	}
	if l.wire != nil {
		l.wire.mu.Lock()
		if l.wire.log != nil {
			l.wire.log.Close()
		}
		l.wire.mu.Unlock()
	}
	removeRunFiles(e.root, execution) // the run token is not needed after the run
	var code *int64
	if l.state() != nil && l.state().Exited() {
		c := int64(l.state().ExitCode())
		code = &c
	}
	_ = l.observe()
	res := buildExecutionResourceSummary(l)
	executionSpan(execution, l)
	l.seq++
	if err := e.event(l.command, l.seq, contract.FleetEventTypeProcessClosed, contract.ProcessClosed{ExitCode: code, StdoutClosed: l.eof, ObservedOwnedProcessesRemaining: new(bool), CleanupUnknown: l.cleanupUnknown, Resources: res, StderrTail: stderrTail(e.commandDir(l.sessionID(), workspaceKey(l.command)), execution, code)}); err != nil {
		return false, err
	}
	sid, err := idOf(l.command, "sessionId")
	if err != nil {
		return false, err
	}
	dir := e.commandDir(sid, workspaceKey(l.command))
	// Project sessions run in their worktree, so the agent writes output/ there.
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
	// Diagnostic retention must not turn an original unknown/failure into another execution outcome.
	if err := e.artifacts(dir, []string{execution + ".mcp"}, execution, &declared); err != nil {
		l.seq++
		_ = e.event(l.command, l.seq, "mcp.diagnostics_unavailable", M{"reason": "safe receipt collection failed"})
	}
	// The coordinator holds a completed run with required artifacts still uploading as waiting.
	e.declared = declared
	if l.trial != nil {
		l.seq++
		if err := e.event(l.command, l.seq, contract.FleetEventTypeTrialRemoved, contract.TrialRemoved{Removed: removeTrial(l.trial[0], l.trial[1])}); err != nil {
			return false, err
		}
	}
	status := "failed"
	switch {
	case l.cleanupUnknown:
		status = "unknown"
	case l.timedOut:
	case l.stopped:
		status = "stopped"
	case l.state() != nil && l.state().Success() && l.completed:
		status = "completed"
	}
	if err := e.finish(l.command, status, l.native); err != nil {
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

// artifacts collects the output folder dir/parts...: the folder and every entry below it are opened relative to their
// parent descriptor with O_NOFOLLOW, so no link at any level (including one swapped in after listing) leads outside.
// Each regular file with one link is copied into the spool, so later edits and worktree cleanup cannot change what is
// uploaded, and queued with its relative path as name. Links and hard-linked files are skipped, the same policy as
// artifact_save. A missing folder collects nothing.
func (e *executor) artifacts(dir string, parts []string, execution string, declared *[]string) error {
	root, err := openInside(dir, parts)
	if err != nil {
		if _, serr := os.Lstat(filepath.Join(append([]string{dir}, parts...)...)); os.IsNotExist(serr) {
			return nil
		}
		return errors.New("artifact folder is a link or unreadable")
	}
	return e.collect(root, "", execution, declared)
}

func (e *executor) collect(d *os.File, rel, execution string, declared *[]string) error {
	defer d.Close()
	if st, err := d.Stat(); err != nil || !st.IsDir() {
		return errors.New("artifact folder is not a directory")
	}
	entries, err := d.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, en := range entries {
		name := en.Name()
		if rel != "" {
			name = rel + "/" + en.Name()
		}
		fd, err := unix.Openat(int(d.Fd()), en.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if errors.Is(err, unix.ELOOP) {
			continue // a link
		}
		if err != nil {
			return err
		}
		f := os.NewFile(uintptr(fd), name)
		st, err := f.Stat()
		switch {
		case err != nil:
			f.Close()
			return err
		case st.IsDir():
			if err := e.collect(f, name, execution, declared); err != nil {
				return err
			}
			continue
		case !st.Mode().IsRegular() || st.Sys().(*syscall.Stat_t).Nlink != 1:
			f.Close()
			continue
		}
		id := newUUID()
		spool := filepath.Join(e.root, "spool", id)
		out, err := os.Create(spool)
		if err == nil {
			_, err = io.Copy(out, f)
			out.Close()
		}
		f.Close()
		if err != nil {
			return err
		}
		if err := enqueue(e.db, id, "upload", M{"path": spool, "spool": true, "previews": true, "name": name, "mediaType": mediaType(name), "executionId": execution, "required": false}); err != nil {
			return err
		}
		*declared = append(*declared, name)
	}
	return nil
}

func (e *executor) stores() []contract.StoreReport {
	if e.storeCfg == nil {
		return []contract.StoreReport{}
	}
	if e.storeAt.IsZero() || time.Since(e.storeAt) >= 15*time.Second {
		e.storeAt, e.storeLast = time.Now(), storeReport(e.storeCfg)
	}
	return e.storeLast
}

func buildExecutionResourceSummary(l *live) *contract.ExecutionResourceSummary {
	out := &contract.ExecutionResourceSummary{}
	if l.metrics != nil {
		l.metrics.applyTo(commandHarness(l.command), out)
	} else if !l.started.IsZero() {
		active := max(int64(0), time.Since(l.started).Milliseconds())
		out.ActiveMs = &active
	}
	missingReason := func() *contract.ExecutionResourceMissingReason {
		r := contract.ExecutionResourceMissingReasonNoSample
		if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
			r = contract.ExecutionResourceMissingReasonOsUnsupported
		} else if l.stopped || l.timedOut {
			r = contract.ExecutionResourceMissingReasonInterrupted
		}
		return &r
	}
	if len(l.cpu) > 0 {
		var cpuSec float64
		for _, sec := range l.cpu {
			cpuSec += sec
		}
		cpuMs := int64(math.Round(cpuSec * 1000))
		out.CpuMs = &cpuMs
	} else {
		out.CpuMissingReason = missingReason()
	}
	if l.peakRSS > 0 {
		peak := int64(l.peakRSS)
		out.PeakMemoryBytes = &peak
	} else {
		out.MemoryMissingReason = missingReason()
	}
	return out
}

// observe adds descendants of owned processes, each with its incarnation, confirming the parent link after capture.
func (l *live) observe() error {
	table, err := processes()
	if err != nil {
		return err
	}
	defer l.account(table)
	for added := true; added; {
		added = false
		for _, p := range table {
			if _, ok := l.owned[p.pid]; ok {
				continue
			}
			expected, ok := l.owned[p.ppid]
			if !ok {
				continue
			}
			if id, err := identity(p.ppid); err != nil || id != expected {
				continue
			}
			ident, err := identity(p.pid)
			if err != nil {
				continue
			}
			again, err := processes()
			if err != nil {
				return err
			}
			linked := false
			for _, q := range again {
				if q.pid == p.pid && q.ppid == p.ppid {
					linked = true
				}
			}
			if id, err := identity(p.ppid); linked && err == nil && id == expected {
				l.owned[p.pid] = ident
				added = true
			}
		}
	}
	return nil
}

// identityUnknown: an owned pid exists but its identity cannot be read, so whether it is the recorded process is
// unknown.
type identityUnknown struct {
	pid int
	err error
}

func (u identityUnknown) Error() string {
	return fmt.Sprintf("process %d exists but its identity cannot be read: %v", u.pid, u.err)
}

func (l *live) aliveOwned() (bool, error) {
	table, err := processes()
	if err != nil {
		return false, err
	}
	for _, p := range table {
		if strings.HasPrefix(p.stat, "Z") {
			continue
		}
		expected, ok := l.owned[p.pid]
		if !ok {
			continue
		}
		cur, err := identity(p.pid)
		if err == nil {
			if cur == expected {
				return true, nil
			}
			continue // another incarnation holds the pid
		}
		// An identity read failure proves nothing (W3-3): only ESRCH from kill(pid, 0) shows the pid has gone; a pid
		// that still exists but cannot be inspected is unknown.
		if errors.Is(syscall.Kill(p.pid, 0), syscall.ESRCH) {
			continue
		}
		return false, identityUnknown{p.pid, err}
	}
	return false, nil
}

// gone: a signal failed because the recorded incarnation exited between the check and the signal: kill(pid, 0) says no
// such process, or the pid now has another identity. An identity that cannot be read is not gone (W3-3): that failure
// stays an error.
func gone(pid int, ident string) bool {
	if errors.Is(syscall.Kill(pid, 0), syscall.ESRCH) {
		return true
	}
	id, err := identity(pid)
	return err == nil && id != ident
}

func running(table []proc, pid int) bool {
	for _, p := range table {
		if p.pid == pid && !strings.HasPrefix(p.stat, "Z") {
			return true
		}
	}
	return false
}

// cleanup freezes every owned process (SIGSTOP, repeated until no new descendant appears), then kills them, each signal
// bound to the recorded incarnation. Script commands also kill their whole group; the group id is dropped once killpg
// reports no such group (ESRCH).
func (l *live) cleanup() error {
	// Only ESRCH proves the group has no members; any other error keeps the group, the owned processes are still
	// killed one by one below, and the error leaves cleanup unknown.
	var groupErr error
	if l.group != 0 {
		if err := syscall.Kill(-l.group, syscall.SIGKILL); errors.Is(err, syscall.ESRCH) {
			l.group = 0
		} else {
			groupErr = err
		}
	}
	frozen := map[int]bool{}
	for {
		if err := l.observe(); err != nil {
			return err
		}
		table, err := processes()
		if err != nil {
			return err
		}
		changed := false
		for pid, ident := range l.owned {
			if frozen[pid] || !running(table, pid) {
				continue
			}
			if id, err := identity(pid); err == nil && id == ident {
				if err := signalOwned(pid, ident, syscall.SIGSTOP); err != nil && !errors.Is(err, syscall.ESRCH) && !gone(pid, ident) {
					return err
				}
				frozen[pid], changed = true, true
			}
		}
		if !changed {
			break
		}
	}
	table, err := processes()
	if err != nil {
		return err
	}
	for pid, ident := range l.owned {
		if id, err := identity(pid); running(table, pid) && err == nil && id == ident {
			if err := signalOwned(pid, ident, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) && !gone(pid, ident) {
				return err
			}
		}
	}
	return groupErr
}

// reproject: `executor reproject`: one JSON line per stored record on stdin, {executionId, harness, timestamp, record}, in
// seq order; one projection per line on stdout. Needs no data dir, lock or journal.
func reproject() error {
	if NewBlocks == nil {
		return errors.New("reproject: no block projection built")
	}
	state := map[string]Blocks{}
	out := bufio.NewWriter(os.Stdout)
	in := bufio.NewScanner(os.Stdin)
	in.Buffer(make([]byte, 1<<20), 1<<30)
	for in.Scan() {
		if strings.TrimSpace(in.Text()) == "" {
			continue
		}
		if _, err := dec(in.Text()); err != nil {
			return err
		}
		// A field of another type reads as absent; the record is the open native record, decoded as the journal does.
		var line struct {
			ExecutionId json.RawMessage `json:"executionId"`
			Harness     json.RawMessage `json:"harness"`
			Timestamp   json.RawMessage `json:"timestamp"`
			Record      json.RawMessage `json:"record"`
		}
		json.Unmarshal([]byte(in.Text()), &line)
		ex, _ := rawStr(line.ExecutionId)
		b := state[ex]
		if b == nil {
			b = NewBlocks()
			state[ex] = b
		}
		h := "codex"
		if s, ok := rawStr(line.Harness); ok {
			h = s
		}
		var ts uint64
		json.Unmarshal(line.Timestamp, &ts)
		rec := M{}
		if isObject(line.Record) {
			rec = decM(string(line.Record))
		}
		fmt.Fprintln(out, enc(b.Project(h, rec, ts)))
	}
	if err := in.Err(); err != nil {
		return err
	}
	return out.Flush()
}

func runExecutor() error {
	root := envOr("FLEET_DATA_DIR", defaultDataDir())
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(root, "executor.lock"), os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB) != nil {
		return errors.New("executor already running")
	}
	journal := filepath.Join(root, "journal.sqlite")
	db, err := openJournal(journal)
	if err != nil {
		return err
	}
	defer db.Close()
	url := envOr("FLEET_COORDINATOR_URL", defaultCoordinatorURL)
	host := os.Getenv("FLEET_HOST_ID")
	secretFile := envOr("FLEET_HOST_SECRET_FILE", defaultSecretFile())
	if host == "" {
		return errors.New("FLEET_HOST_ID required")
	}
	// The host's own secret, from a private file; the coordinator derives the host from it.
	token, err := hostSecret(secretFile)
	if err != nil {
		return err
	}
	sessions := os.Getenv("FLEET_SESSIONS_ROOT")
	if sessions == "" {
		sessions = filepath.Join(root, "sessions")
	}
	e := &executor{db: db, ex: db, root: root, url: url, token: token, host: host, live: map[string]*live{}, sessions: sessions, harnessCache: map[string]M{}, storeCfg: storeConfig()}
	if err := requeueUnauthorized(db); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(root, "spool"), 0o755); err != nil {
		return err
	}
	// Artifact transfers run on their own goroutines; the Access token for store tunnels from FLEET_CF_ACCESS_FILE.
	if err := spawnTransfers(journal, newTransferClient(url, token, host, accessToken(accessFile())), func(_ *transferClient, db *sql.DB, body M, response any) {
		// A remote store answers "verifying" until the store's host checks the copy; the bytes are uploaded either way.
		answer, _ := decodeAs[transferAnswer](response)
		file, _ := decodeAs[transferFile](body)
		if st, _ := rawStr(answer.State); body["previews"] == true && (st == "stored" || st == "verifying") {
			buildPreviews(db, body, response)
		}
		if path, ok := rawStr(file.Path); body["spool"] == true && ok {
			os.Remove(path)
		}
	}); err != nil {
		return err
	}
	if e.storeCfg != nil {
		if r := storeRegistration(e.storeCfg, host); r != nil {
			if err := queueTo(db, contract.RouteStores, *r); err != nil {
				return err
			}
		}
	}
	odb, err := openJournal(journal)
	if err != nil {
		return err
	}
	spawnOutbox(odb, url, token, host)
	startTelemetry(root, odb, host)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(sessions, 0o700); err != nil {
		return err
	}
	stopCtx, cancelStop := context.WithCancel(context.Background())
	defer cancelStop()
	e.stopCtx = stopCtx
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	errtrack.Go("signals", func() { <-sig; shutdown.Store(true); cancelStop() })
	if err := e.reconcile(); err != nil {
		return err
	}
	status("working", "")
	var failingSince time.Time
	for {
		if shutdown.Load() {
			for _, l := range e.live {
				l.cleanup()
				<-l.exited
			}
			status("done", "")
			return nil
		}
		if err := e.tick(); err != nil {
			status("error", "")
			return err
		}
		err := e.exchange()
		var failed tickFailed
		if errors.As(err, &failed) {
			status("error", "")
			return failed.error
		}
		if errors.Is(err, errHostRemoved) {
			// The owner removed this host (fleet-system-management.md, Hosts): stop polling and exit 0, so the unit's
			// Restart=on-failure does not start it again.
			fmt.Fprintln(os.Stderr, "executor: this host was removed from Fleet; stopping its runs")
			for _, l := range e.live {
				l.cleanup()
				<-l.exited
			}
			status("done", "")
			return nil
		} else if err != nil {
			if shutdown.Load() {
				continue
			}
			// A redirected poll is already signalled once by the edge state; other failures log here.
			var r redirected
			if !errors.As(err, &r) {
				// A coordinator restart is a few seconds of refused or reset polls: retried silently, logged once it lasts.
				if failingSince.IsZero() {
					failingSince = time.Now()
				}
				if time.Since(failingSince) >= exchangeQuiet {
					fmt.Fprintf(os.Stderr, "executor exchange failed for %s; retained journal, retry pending: %v\n", time.Since(failingSince).Round(time.Second), err)
					status("error", "")
				}
			} else {
				failingSince = time.Time{}
				status("blocked", "auth") // edge login page or host token not accepted
			}
		} else {
			failingSince = time.Time{}
			status("working", "")
		}
		time.Sleep(tickEvery)
	}
}

var errHostRemoved = errors.New("host removed")

// The generated envelope owns wire fields. Raw command bodies retain all journal identity fields,
// including extensions from older coordinators, until the internal command runner is migrated.
type pollReply struct {
	contract.PollReply
	bodies []M
}

func decodePoll(raw json.RawMessage) (*pollReply, error) {
	// A command that does not fit its contract type fails the whole reply (nothing runs or is acknowledged); the
	// coordinator stops such a command instead of sending it. A kind this executor does not know keeps its JSON (Raw).
	var reply pollReply
	if err := json.Unmarshal(raw, &reply.PollReply); err != nil {
		return nil, err
	}
	var journal struct {
		Commands []M `json:"commands"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber() // Match the existing journal encoding, including large integers and numeric spelling.
	if err := decoder.Decode(&journal); err != nil {
		return nil, err
	}
	reply.bodies = journal.Commands
	return &reply, nil
}

const longPollWait = 25 * time.Second

func (e *executor) baseCtx() context.Context {
	if e.stopCtx != nil {
		return e.stopCtx
	}
	return context.Background()
}

// poll owns the store-report cache and update signal; command execution never runs in this goroutine.
func (e *executor) poll() (*pollReply, error) {
	return e.pollWait(e.baseCtx(), 0)
}

func (e *executor) pollWait(ctx context.Context, wait time.Duration) (*pollReply, error) {
	v, roots := version, reportedRoots()
	body := contract.PollRequest{HostId: e.host, Platform: contract.Platform(platform()), Capabilities: capabilities(), Stores: e.stores(),
		Problems: edgeProblems(pollRoute), ProjectRoots: roots, Version: &v, HarnessVersions: managedVersions(), Update: lastUpdate(e.root), GitConnections: gitConnections(connectionsDir())}
	var a answer
	if wait > 0 {
		a = sendPoll(ctx, e.url, e.token, pollRoute+"?wait="+wait.String(), body)
	} else {
		a = sendWithClient(ctx, outboxClient, e.url, e.token, pollRoute, pollRoute, body)
	}
	if a.status == 410 && strings.Contains(a.text, "host-removed") {
		return nil, errHostRemoved
	}
	if err := ctx.Err(); err != nil && a.kind == answerRetry {
		// The exchange cancelled its own heartbeat poll (or the executor stops): not a poll failure for update.json.
		return nil, err
	}
	signalMu.Lock()
	defer signalMu.Unlock()
	sig := updateSignal{HostId: e.host, Running: version, AutoUpdate: "on"}
	if lastSignal != nil {
		sig.Target, sig.AutoUpdate = lastSignal.Target, lastSignal.AutoUpdate
	}
	sig.PollOk, sig.PollError = pollFields(a)
	var reply *pollReply
	if a.kind == answerOK {
		var err error
		reply, err = decodePoll(a.value)
		if err != nil {
			a.kind, a.text = answerRetry, "invalid poll reply: "+err.Error()
			sig.PollOk, sig.PollError = pollFields(a)
		} else {
			sig.Target = reply.Target
			if reply.AutoUpdate != nil {
				sig.AutoUpdate = *reply.AutoUpdate
			}
		}
	}
	if ready := e.updateReady.Load(); ready != nil && sig.Target != nil && *ready == *sig.Target {
		sig.RestartTarget = ready
	}
	if err := writeUpdateSignal(e.root, sig); err != nil {
		fmt.Fprintln(os.Stderr, "executor: update.json:", err)
	}
	_, err := answerValue(a)
	return reply, err
}

// exchange keeps command order and the single owner of live/process/journal state. Slow preparation must not
// stop host liveness; extra polls leave returned commands pending for the next exchange, never execute them twice.
// exchangeQuiet: how long exchange failures stay unlogged; a coordinator restart behind its held socket takes less.
const exchangeQuiet = 30 * time.Second

func (e *executor) exchange() (err error) {
	// Retain the edge backoff for both ordinary polls and liveness polls.
	if edgeWait(pollRoute) {
		return nil
	}
	reply, err := e.heldPoll()
	if err != nil {
		return err
	}
	beatCtx, cancelBeat := context.WithCancel(e.baseCtx())
	stop, done := make(chan struct{}), make(chan struct{})
	var removed atomic.Bool
	errtrack.Go("update.drain", func() {
		defer close(done)
		beat := time.NewTicker(time.Second)
		defer beat.Stop()
		for {
			select {
			case <-stop:
				return
			case <-beat.C:
				if edgeWait(pollRoute) {
					continue
				}
				if _, err := e.pollWait(beatCtx, 0); errors.Is(err, errHostRemoved) {
					removed.Store(true)
					return
				}
			}
		}
	})
	defer func() {
		cancelBeat()
		close(stop)
		<-done
		if err == nil && removed.Load() {
			err = errHostRemoved
		}
	}()

	for index := range reply.Commands {
		if removed.Load() {
			return errHostRemoved
		}
		if err := e.tick(); err != nil {
			return err
		}
		if err := e.runOrFail(reply.bodies[index]); err != nil {
			return err
		}
	}
	return nil
}

// runOrFail runs one command. A command that errors before its run is live fails, and a start or input says why:
// without the reason an execution failed with no problem and no preparation.failed event.
func (e *executor) runOrFail(m M) error {
	err := e.run(m)
	if err == nil {
		return nil
	}
	c := typedCommand(m)
	id, _ := idOf(c, "id")
	execution, _ := idOf(c, "executionId")
	if _, isLive := e.live[execution]; isLive {
		return nil
	}
	b, ok := e.commandBody(id, true)
	if !ok || b != enc(m) {
		return nil
	}
	if k := commandKind(b); k == contract.CommandKindStart || k == contract.CommandKindInput {
		// A fresh seq: the failure may follow events of this command that took the low ones.
		if err := e.event(c, uint64(nowMs()), contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "executor error before the harness started: " + err.Error()}); err != nil {
			return err
		}
	}
	return e.finish(c, "failed", "")
}
