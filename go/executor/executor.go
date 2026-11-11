package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var shutdown atomic.Bool

// cmdID returns a command identity field: non-empty ASCII alphanumerics, '_' and '-'.
func cmdID(v any, k string) (string, error) {
	s, ok := jAt(v, k).(string)
	if !ok {
		return "", errors.New("missing identity")
	}
	if s == "" || !onlyBytes(s, func(b byte) bool { return alnum(b) || b == '_' || b == '-' }) {
		return "", errors.New("invalid identity")
	}
	return s, nil
}

// harnessName: payload harness, else agentConfig.harness; a non-string is "invalid"; default codex.
func harnessName(p any) string {
	o := jObj(p)
	v, ok := o["harness"]
	if !ok {
		v, ok = jObj(o["agentConfig"])["harness"]
	}
	if !ok {
		return "codex"
	}
	if s, isS := v.(string); isS {
		return s
	}
	return "invalid"
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
	c := []string{"codex", "gpt-6.1-sol", "low", "static-output", "steer", "native-fork", "artifact-store", "script"}
	if v := ffmpegVersion(); v != "" {
		c = append(c, "ffmpeg:"+v)
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
	method := "response"
	if jIsStr(v, "method") {
		method = jStr(v, "method")
	}
	var rid any
	if n, ok := jU64(v, "id"); ok {
		rid = n
	}
	return M{"type": "protocol.write", "method": method, "requestId": rid, "bytes": len(b), "sha256": sha(b),
		"delivery": "written to local transport; native acceptance unknown"}, nil
}

type live struct {
	cmd            *exec.Cmd
	exited         chan struct{} // closed when Wait returned
	state          *os.ProcessState
	command        M
	native         string
	seq            uint64
	completed      bool
	stopped        bool
	owned          map[int]string
	journaled      map[int]bool // owned pids already in the journal's owned table
	cleanupUnknown bool
	lines          chan string // closed at stdout EOF
	eof            bool
	lastSample     time.Time
	wire           *wire
	// Script commands: the process group stop and timeout kill, the deadline, and the trial worktree (top, path).
	group    int
	deadline time.Time
	timedOut bool
	trial    *[2]string
	blocks   Blocks
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
	db           *sql.DB
	ex           execer // db, or the open transaction
	root         string
	url          string
	token        string
	host         string
	live         map[string]*live
	sessions     string
	harnessCache map[string]M
	storeCfg     M
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

func (e *executor) queue(route string, v any) error { return queue(e.ex, route, v) }

func (e *executor) receipt(c M, status string, native string) error {
	declared := e.declared
	e.declared = nil
	cid, err := cmdID(c, "id")
	if err != nil {
		return err
	}
	ex, err := cmdID(c, "executionId")
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
	return e.queue("/api/hosts/receipts", r)
}

// event queues one Fleet-originated execution event; nativeEvent one harness record under its own open type.
func (e *executor) event(c M, seq uint64, kind contract.FleetEventType, data any) error {
	return e.nativeEvent(c, seq, string(kind), data)
}

func (e *executor) nativeEvent(c M, seq uint64, kind string, data any) error {
	cid, err := cmdID(c, "id")
	if err != nil {
		return err
	}
	ex, err := cmdID(c, "executionId")
	if err != nil {
		return err
	}
	ev := contract.HostEvents{HostId: e.host, ExecutionId: ex, Events: []contract.HostEvent{{Id: fmt.Sprintf("%s-%d", cid, seq), Seq: int64(seq), Type: kind, Timestamp: nowMs(), Data: json.RawMessage(enc(data))}}}
	return e.queue("/api/hosts/events", ev)
}

func (e *executor) finish(c M, status, native string) error {
	return e.inTx(func() error {
		if err := e.receipt(c, status, native); err != nil {
			return err
		}
		cid, err := cmdID(c, "id")
		if err != nil {
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
	res, err := e.db.Exec("DELETE FROM launches WHERE execution=? AND session=? AND NOT EXISTS (SELECT 1 FROM owned WHERE execution=?)", execution, session, execution)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		os.Remove(filepath.Join(e.root, "runs", execution+".env")) // the run token goes with the launch
	}
	return nil
}

func (e *executor) release(execution string) error {
	return e.inTx(func() error {
		if _, err := e.ex.Exec("DELETE FROM owned WHERE execution=?", execution); err != nil {
			return err
		}
		_, err := e.ex.Exec("DELETE FROM launches WHERE execution=?", execution)
		return err
	})
}

// reconcileProcesses runs at start, before any poll (K2-3). The stdout and stdin pipes of a run died with the executor
// that started it, so nothing is reattached: every recorded process that survives, and every descendant still linked to
// one, is frozen and killed, each signal bound to the recorded incarnation, then its run file is removed (K2-6). A launch
// with no owned row crashed between spawn and the identity read, so its process cannot be named; the event says so,
// and only a stop for that execution releases it (releaseLaunchWindow). A launch whose processes cannot be confirmed
// gone stays recorded, which keeps its session refused, and is tried again at the next start.
func (e *executor) reconcileProcesses() error {
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
				alive, err := x.l.aliveOwned()
				x.err, waiting = err, waiting || alive
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
			if err := e.event(decM(b), uint64(nowMs()), contract.FleetEventTypeProcessReconciled, contract.ProcessReconciled{OwnedProcesses: int64(len(x.l.owned)), Survived: x.survived,
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
	// Run files of every execution without an unresolved launch: no process of this executor can need them.
	files, err := filepath.Glob(filepath.Join(e.root, "runs", "*.env"))
	if err != nil {
		return err
	}
	for _, f := range files {
		if !unresolved[strings.TrimSuffix(filepath.Base(f), ".env")] {
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
		if err := e.finish(decM(b), "unknown", ""); err != nil {
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
		return jObj(jClone(v))
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
			if items, err := dec(string(o)); err == nil {
				out := []any{}
				var walk func(v any)
				walk = func(v any) {
					for _, t := range jArr(v, "tools") {
						if jArr(t, "tools") != nil {
							walk(t)
						} else if jIsStr(t, "name") {
							out = append(out, jStr(t, "name"))
						}
					}
				}
				for _, it := range jArr(items) {
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
	return jObj(jClone(v))
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
		return e.receipt(c, "accepted", "")
	}); err != nil {
		return err
	}
	kind := jStr(c, "kind")
	p := jObj(c, "payload")
	switch kind {
	case "stop":
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
		if err := e.receipt(c, result, ""); err != nil {
			return err
		}
		if result == "unknown" {
			_, err := e.db.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
			return err
		}
		return nil
	case "steer":
		sp, perr := payloadOf[contract.SteerPayload](c)
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
			if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid steer payload: " + perr.Error()}); err != nil {
				return err
			}
		}
		return e.finish(c, "failed", "")
	case "readiness":
		data := readiness(jAt(p, "project"), e.host, os.Getenv("FLEET_NATIVE_ACCOUNTS"))
		ok := data["ok"] == true
		if err := e.event(c, 0, contract.FleetEventTypeReadiness, data); err != nil {
			return err
		}
		if !ok {
			if err := e.event(c, 1, contract.FleetEventTypeReadinessFailed, M{"reason": "readiness-check-failed"}); err != nil {
				return err
			}
		}
		return e.finish(c, okStatus(ok), "")
	case "base-updated":
		data, ok := checkoutSync(p, e.host, projectRoots(), e.forbidden())
		k := contract.FleetEventTypeSyncFailed
		if ok {
			k = contract.FleetEventTypeCheckoutSynced
		}
		if err := e.event(c, 0, k, data); err != nil {
			return err
		}
		return e.finish(c, okStatus(ok), "")
	case "teardown":
		// Session finish: teardown script, keep on dirty/teardown failure, safe branch delete. Lookup only: never create a
		// worktree; the session dir must be a real dir under the executor or session roots.
		dir := e.sessionDir(session)
		repo, hasRepo := repoPath(p, e.host)
		data := M{"removed": false, "reason": "no-worktree"}
		if e.insideDirs(dir) && exists(filepath.Join(dir, "worktree", ".git")) {
			_, top, created, ok, err := worktree(dir, session, repo, hasRepo, projectRoots(), e.forbidden(), jAt(p, "project"))
			switch {
			case err != nil:
				data = M{"removed": false, "reason": err.Error()}
			case ok && !created:
				data = teardown(dir, session, top, jAt(p, "project"))
			}
		}
		ok := data["removed"] == true || data["reason"] == "uncommitted" || data["reason"] == "no-worktree"
		// The teardown command reuses the last execution, whose seq 0.. are taken by its turn: use a fresh seq.
		if err := e.event(c, uint64(nowMs()), contract.FleetEventTypeTeardown, data); err != nil {
			return err
		}
		return e.finish(c, okStatus(ok), "")
	case "artifact-copy", "artifact-verify":
		// Long transfers run on the transfer worker, which sends the final receipt; the journal row resumes them.
		b := jObj(jClone(p))
		if b == nil {
			b = M{}
		}
		b["command"] = M{"id": c["id"], "executionId": c["executionId"]}
		b["spool"] = filepath.Join(e.root, "spool")
		k := "verify"
		if kind == "artifact-copy" {
			k = "copy"
		}
		if err := enqueue(e.db, cid, k, b); err != nil {
			return err
		}
		_, err := e.db.Exec("UPDATE commands SET terminal=1 WHERE id=?", cid)
		return err
	case "script":
		return e.script(c, execution, session)
	case "start", "input":
		return e.start(c, cid, execution, session, p)
	}
	return e.finish(c, "failed", "")
}

// payloadOf decodes a command payload into its per-kind contract type (an absent payload is the zero value).
func payloadOf[T any](c M) (T, error) {
	var v T
	if raw, has := c["payload"]; has && raw != nil {
		err := json.Unmarshal([]byte(enc(raw)), &v)
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

func (e *executor) start(c M, cid, execution, session string, p M) error {
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
		if err := e.event(c, 1, "preparation.failed", M{"reason": reason}); err != nil {
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
	cfg, hasCfg := p["agentConfig"]
	if hasCfg && profileValidate(cfg) != nil {
		return e.finish(c, "failed", "")
	}
	name := harnessName(p)
	dir := e.sessionDir(session)
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
	if !hasCfg && name == "codex" {
		shell := "workspace-write, network disabled, approvals denied"
		if sp.GitNetwork != nil && *sp.GitNetwork {
			shell = "workspace-write, git network enabled, checkout .git writable, approvals denied"
		}
		if err := e.event(c, 0, contract.FleetEventTypePreparation, M{"model": p["model"], "effort": p["effort"], "subscription": true, "instructionsSha256": sha([]byte(str0(sp.Instructions))),
			"tools": M{"shell": shell, "mcp": []any{}, "collaborationTools": jBoolOr(true, p, "agentConfig", "collaborationTools")}, "skills": []any{}, "repairs": false,
			"nativeConfig": "Fleet-owned HOME/CODEX_HOME required; config and execpolicy ignored; AGENTS discovery, skills, memories and hooks disabled; context observations pending"}); err != nil {
			return err
		}
	}
	// The coordinator owns the native id per harness:account continuation; no id means a new native session.
	native := str0(sp.NativeSessionId)
	if sp.NativeSessionId != nil && !safeNativeID(native) {
		if err := e.event(c, 1, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: "invalid native session id"}); err != nil {
			return err
		}
		return e.finish(c, "failed", "")
	}
	fail := func(seq uint64, reason string) error {
		if err := e.event(c, seq, contract.FleetEventTypePreparationFailed, contract.PreparationFailed{Reason: reason}); err != nil {
			return err
		}
		return e.finish(c, "failed", native)
	}
	var evidence M
	if hasCfg {
		ev, err := profilePrepare(cfg)
		if err != nil {
			return e.finish(c, "failed", native)
		}
		evidence = ev
	}
	h := Harnesses[name]
	if h == nil {
		return fail(1, "unknown harness")
	}
	cmd, nativeInput, err := h.Command(dir, native, p)
	if err != nil {
		return fail(1, err.Error())
	}
	if name != "codex" && evidence == nil {
		role := "user"
		if name == "claude" {
			role = "system"
		}
		if err := e.event(c, 0, contract.FleetEventTypePreparation, M{"requested": p, "effective": M{"harness": name, "account": p["account"], "model": p["model"], "instructionRole": role,
			"nativePermissions": "native; no sandbox parity claimed", "authentication": "host-provisioned; readiness unknown"}}); err != nil {
			return err
		}
	}
	if evidence != nil {
		evidence["selection"] = M{"harness": name, "account": p["account"], "authority": "trusted host account reference; native observation follows"}
		if name == "codex" {
			profileApply(cmd, withSkills(cfg, p["skills"]))
		}
		if err := e.event(c, 0, contract.FleetEventTypePreparation, evidence); err != nil {
			return err
		}
	}
	fleetTools := jBoolOr(true, p, "agentConfig", "fleetTools")
	collaboration := jBoolOr(true, p, "agentConfig", "collaborationTools")
	if name == "codex" && !collaboration {
		cmd.Args = append(cmd.Args, "--disable", "multi_agent", "--disable", "multi_agent_v2")
	}
	repo, hasRepo := repoPath(p, e.host)
	checkoutTop := ""
	w, top, created, hasW, err := worktree(dir, session, repo, hasRepo, projectRoots(), e.forbidden(), jAt(p, "project"))
	if err != nil {
		return fail(1, err.Error())
	}
	if hasW {
		checkoutTop = top
		if script := jStr(p, "project", "scripts", "setup"); created && jIsStr(p, "project", "scripts", "setup") && str0(sp.Setup) != "skip" {
			r := projectScript("setup", script, top, w, session)
			if err := e.event(c, 1, contract.FleetEventTypeSetup, r); err != nil {
				return err
			}
			if r["ok"] != true {
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
	if err := e.stageInputs(p, inputDir, c["executionId"]); err != nil {
		return fail(1, err.Error())
	}
	if jObj(p, "project") != nil {
		// Repo of this host's environment (env.repo), else the project's single/first repo.
		var envRepo any
		for _, en := range jArr(p, "project", "environments") {
			if jStr(en, "hostId") == e.host {
				envRepo = jAt(en, "repo")
				break
			}
		}
		access, err := gitEnv(jAt(p, "project"), gitRepo(jAt(p, "project"), envRepo), connectionsDir())
		if err != nil {
			return fail(1, err.Error())
		}
		gitApply(cmd, access)
	}
	observed := e.observeHarness(cmd, name)
	servers := M{}
	if name != "codex" {
		servers = profileServers(jAt(p, "agentConfig"))
	}
	profiles := jArr(p, "agentConfig", "delegationProfiles")
	if profiles == nil {
		profiles = []any{}
	}
	fleetMCP := []any{}
	if fleetTools {
		context := M{"hostId": e.host, "commandId": c["id"], "executionId": c["executionId"], "profiles": profiles, "projectId": p["projectId"], "workdir": work, "inputDir": inputDir, "spool": filepath.Join(e.root, "spool"), "operations": p["fleetOperations"]}
		args := []any{"mcp", "delegation", filepath.Join(e.root, "journal.sqlite"), enc(context)}
		server := M{"command": exe(), "args": args}
		if name == "codex" {
			tools := []any{"session_start", "session_deliver", "session_transition", "session_fork", "session_resume", "session_handoffs", "message_send", "queue_list", "ask", "reply", "questions", "worker_done", "execution_status", "artifact_save", "artifact_list", "artifact_get"}
			if len(profiles) > 0 {
				tools = append(tools, "request_child")
			}
			if sp.ProjectId != nil {
				for _, t := range projectTools {
					tools = append(tools, t)
				}
			}
			for _, o := range jArr(p, "fleetOperations") {
				if n := jStr(o, "name"); n != "" && !slices.Contains(tools, any(n)) {
					tools = append(tools, n)
				}
			}
			server["tools"] = tools
			fleetMCP = tools
		}
		servers["fleet"] = server
	}
	observed["fleetTools"], observed["collaborationTools"] = fleetTools, collaboration
	if name == "codex" {
		observed["fleetMcpTools"] = fleetMCP
	} else {
		observed["fleetMcpTools"] = anyStrings(sortedKeys(servers))
	}
	observed["cwd"], observed["worktree"], observed["platform"] = work, hasW, platform()
	// Granted connections: one `<executor> mcp connection` server each, calling fleet-gateway with this run's token. The
	// token and gateway URL reach the servers through the harness environment, never a file or argv.
	var connectionTools []string
	runFile, launched := "", false
	defer func() {
		if runFile != "" && !launched {
			os.Remove(runFile) // K2-6: a launch that failed never needs its run token
		}
	}()
	granted := jArr(p, "connections")
	if len(granted) > 0 {
		gateway := os.Getenv("FLEET_GATEWAY_URL")
		if gateway == "" || sp.RunToken == nil {
			return fail(1, "connections granted but FLEET_GATEWAY_URL or the run token is missing")
		}
		// The run token, gateway URL and Access file path reach only the connection servers, through a private run file
		// in the executor's data directory; the harness environment never carries them.
		lines := "FLEET_RUN_TOKEN=" + str0(sp.RunToken) + "\nFLEET_GATEWAY_URL=" + gateway + "\n"
		if f := accessFile(); f != "" && isFile(f) {
			lines += "FLEET_CF_ACCESS_FILE=" + f + "\n"
		}
		dir := filepath.Join(e.root, "runs")
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		runFile = filepath.Join(dir, execution+".env")
		if err := os.WriteFile(runFile, []byte(lines), 0o600); err != nil {
			return err
		}
	}
	for _, spec := range granted {
		conn := jStr(spec, "name")
		if conn == "" || !onlyBytes(conn, func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' }) {
			continue
		}
		tools := []any{}
		for _, t := range jArr(spec, "tools") {
			if n := jStr(t, "name"); n != "" && onlyBytes(n, func(b byte) bool { return alnum(b) || b == '_' }) {
				tools = append(tools, n)
				connectionTools = append(connectionTools, "mcp__"+conn+"__"+n)
			}
		}
		servers[conn] = M{"command": exe(), "args": []any{"mcp", "connection", conn, enc(spec), runFile}, "tools": tools, "env": []any{"HOME"}}
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
		if len(jArr(p, "skills")) > 0 {
			extra = append(extra, "Skill")
		}
	}
	h.WireMCP(cmd, p, servers, extra)
	root := dir
	if hasW {
		root = w
	}
	pp := p
	if name == "codex" && sp.GitNetwork != nil && *sp.GitNetwork && checkoutTop != "" {
		// gitNetwork: sandbox network on and the checkout's common .git writable.
		pp = jObj(jClone(p))
		pp["writableRoots"] = []any{filepath.Join(checkoutTop, ".git")}
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
	if err := cmd.Start(); err != nil {
		pw.Close()
		closeAll()
		e.release(execution)
		return e.finish(c, "failed", native)
	}
	pw.Close()
	exited := make(chan struct{})
	l := &live{cmd: cmd, exited: exited, command: c, native: native, seq: 2, owned: map[int]string{}, lastSample: time.Now()}
	go func() { cmd.Wait(); l.state = cmd.ProcessState; close(exited) }()
	kill := func() { cmd.Process.Kill(); <-exited; closeAll(); e.release(execution) }
	if nativeInput != nil && !protocol {
		_, err := stdin.Write(nativeInput)
		stdin.Close()
		if err != nil {
			kill()
			return e.finish(c, "unknown", native)
		}
	}
	ident, err := identity(cmd.Process.Pid)
	if err != nil {
		kill()
		return e.finish(c, "unknown", native)
	}
	l.owned[cmd.Process.Pid] = ident
	// Harness commands run in their own process group (newCmd): a restart can also kill descendants not yet observed.
	if err := e.ownGroup(execution, cmd.Process.Pid, l); err != nil {
		kill()
		return e.finish(c, "unknown", native)
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
	go readStdout(pr, rawLog, l.wire, acpProtocol, lines)
	e.live[execution], launched = l, true
	l.seq++
	if err := e.event(c, l.seq, contract.FleetEventTypeHarness, observed); err != nil {
		return err
	}
	return e.receipt(c, "started", native)
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
func (e *executor) script(c M, execution, session string) error {
	p := jObj(c, "payload")
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
	if err := e.stageInputs(p, input, c["executionId"]); err != nil {
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
	repo, hasRepo := repoPath(p, e.host)
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
	cmd := cleanCmd("sh", "-c", command)
	cmd.Dir = work
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.Env = append(cmd.Env, "FLEET_INPUT_DIR="+input, "FLEET_OUTPUT_DIR="+output, "FLEET_SESSION_ID="+session)
	if err := e.launch(execution, jStr(c, "id"), session); err != nil {
		dropTrial()
		return err
	}
	if err := cmd.Start(); err != nil {
		dropTrial()
		e.release(execution)
		return refuse("script spawn failed: " + err.Error())
	}
	pid := cmd.Process.Pid
	exited := make(chan struct{})
	l := &live{cmd: cmd, exited: exited, command: c, seq: 2, completed: true, owned: map[int]string{}, lastSample: time.Now(), group: pid, trial: trial}
	go func() { cmd.Wait(); l.state = cmd.ProcessState; close(exited) }()
	ident, err := identity(pid)
	if err != nil {
		syscall.Kill(-pid, syscall.SIGKILL)
		<-exited
		dropTrial()
		e.release(execution)
		return e.finish(c, "unknown", "")
	}
	l.owned[pid] = ident
	if err := e.ownGroup(execution, pid, l); err != nil {
		syscall.Kill(-pid, syscall.SIGKILL)
		<-exited
		dropTrial()
		e.release(execution)
		return e.finish(c, "unknown", "")
	}
	// No stdout stream to read: the closed channel marks EOF at once.
	lines := make(chan string)
	close(lines)
	l.lines = lines
	if timeout > 0 {
		l.deadline = time.Now().Add(timeout)
	}
	e.live[execution] = l
	if err := e.event(c, 1, contract.FleetEventTypeScriptStarted, M{"commit": p["commit"], "timeoutMs": p["timeout_ms"], "trialWorktree": work}); err != nil {
		return err
	}
	return e.receipt(c, "started", "")
}

// stageInputs: payload inputs [{artifactId, path}] are downloaded to FLEET_INPUT_DIR/<path> before the step starts.
// Paths are relative and never leave the input folder.
func (e *executor) stageInputs(p M, input string, execution any) error {
	list := jArr(p, "inputs")
	if len(list) == 0 {
		return nil
	}
	root, err := os.OpenRoot(input)
	if err != nil {
		return fmt.Errorf("input folder: %w", err)
	}
	defer root.Close()
	c := newTransferClient(e.url, e.token, e.host, accessToken(accessFile()))
	for _, i := range list {
		id, rel := jStr(i, "artifactId"), jStr(i, "path")
		if !jIsStr(i, "artifactId") || !jIsStr(i, "path") {
			return errors.New("invalid input")
		}
		if strings.HasPrefix(rel, "/") || anyPart(rel, func(x string) bool { return x == "" || x == "." || x == ".." }) {
			return fmt.Errorf("invalid input path %s", rel)
		}
		r, f := c.post("/api/hosts/artifacts/get", contract.HostArtifactGetRequest{HostId: e.host, ExecutionId: jStr(execution), ArtifactId: jStr(id)})
		if f != nil {
			return fmt.Errorf("input %s: %s", rel, f.msg)
		}
		if !jIsStr(r, "url") {
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
		src, f := c.get(jStr(r, "url"))
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
			delete(e.live, execution)
			if err := e.release(execution); err != nil {
				return err
			}
		}
	}
	return nil
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
		if err := e.event(l.command, l.seq, contract.FleetEventTypeScriptTimeout, M{"timeoutMs": jAt(l.command, "payload", "timeout_ms")}); err != nil {
			return false, err
		}
	}
	if exited || l.stopped {
		if l.cleanup() != nil {
			l.cleanupUnknown = true
		}
	}
	name := harnessName(jAt(l.command, "payload"))
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
			projection := M{}
			if l.blocks != nil {
				projection = l.blocks.Project(name, event, uint64(nowMs()))
			}
			if n, ok := projection["session"].(string); ok {
				sid, err := cmdID(l.command, "sessionId")
				if err != nil {
					return false, err
				}
				if _, err := e.db.Exec("INSERT OR REPLACE INTO sessions(id,native) VALUES(?,?)", sid, n); err != nil {
					return false, err
				}
				l.native = n
				if err := e.receipt(l.command, "started", n); err != nil {
					return false, err
				}
			}
			if jStr(event, "type") == "steer.result" {
				if b, ok := e.commandBody(jStr(event, "commandId"), true); ok {
					if err := e.finish(decM(b), okStatus(jIsTrue(event, "ok")), ""); err != nil {
						return false, err
					}
				}
			}
			if jStr(event, "type") == string(contract.FleetEventTypeProtocolFailed) && !l.stopped {
				l.cleanupUnknown, l.stopped = true, true
			}
			if projection["completed"] == true {
				l.completed = true
			}
			l.seq++
			retained := jObj(jClone(event))
			retained["fleetNormalized"] = projection
			kind := "unknown"
			if jIsStr(event, "type") {
				kind = jStr(event, "type")
			}
			if err := e.nativeEvent(l.command, l.seq, kind, retained); err != nil {
				return false, err
			}
		default:
			drained = true
		}
	}
	if time.Since(l.lastSample) >= 2*time.Second {
		l.seq++
		if err := e.event(l.command, l.seq, contract.FleetEventTypeResourceObservation, resources(l.cmd.Process.Pid, e.root)); err != nil {
			return false, err
		}
		l.lastSample = time.Now()
	}
	if exited {
		alive, err := l.aliveOwned()
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
	os.Remove(filepath.Join(e.root, "runs", execution+".env")) // the run token is not needed after the run
	var code *int64
	if l.state != nil && l.state.Exited() {
		c := int64(l.state.ExitCode())
		code = &c
	}
	l.seq++
	if err := e.event(l.command, l.seq, contract.FleetEventTypeProcessClosed, contract.ProcessClosed{ExitCode: code, StdoutClosed: l.eof, ObservedOwnedProcessesRemaining: new(bool), CleanupUnknown: l.cleanupUnknown}); err != nil {
		return false, err
	}
	sid, err := cmdID(l.command, "sessionId")
	if err != nil {
		return false, err
	}
	dir := e.sessionDir(sid)
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
	// The coordinator holds a completed run with required artifacts still uploading as waiting.
	e.declared = declared
	if l.trial != nil {
		l.seq++
		if err := e.event(l.command, l.seq, contract.FleetEventTypeTrialRemoved, M{"removed": removeTrial(l.trial[0], l.trial[1])}); err != nil {
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
	case l.state != nil && l.state.Success() && l.completed:
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
			if err := e.finish(decM(b), st, l.native); err != nil {
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

func resources(pid int, root string) M {
	var fields []float64
	if o, err := exec.Command("/bin/ps", "-p", strconv.Itoa(pid), "-o", "%cpu=,rss=").Output(); err == nil {
		for _, s := range strings.Fields(string(o)) {
			if f, err := strconv.ParseFloat(s, 64); err == nil {
				fields = append(fields, f)
			}
		}
	}
	var cpu, rss, disk any
	if len(fields) > 0 {
		cpu = fields[0]
	}
	if len(fields) > 1 {
		rss = fields[1] * 1024
	}
	if o, err := exec.Command("/usr/bin/du", "-sk", root).Output(); err == nil {
		if f := strings.Fields(string(o)); len(f) > 0 {
			if v, err := strconv.ParseUint(f[0], 10, 64); err == nil {
				disk = v * 1024
			}
		}
	}
	return M{"cpuPercent": cpu, "residentBytes": rss, "diskBytes": disk, "cost": nil, "ioBytes": nil, "coverage": M{
		"cpu": "native leader ps lifetime average, excludes descendants", "ram": "native leader rss only",
		"disk": "entire executor data directory allocated bytes; not per execution", "cost": "unknown subscription allocation", "io": "unknown"}}
}

// observe adds descendants of owned processes, each with its incarnation, confirming the parent link after capture.
func (l *live) observe() error {
	table, err := processes()
	if err != nil {
		return err
	}
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

func (l *live) aliveOwned() (bool, error) {
	table, err := processes()
	if err != nil {
		return false, err
	}
	for _, p := range table {
		if strings.HasPrefix(p.stat, "Z") {
			continue
		}
		// An identity that cannot be read is not the recorded incarnation: the process left between the snapshot
		// and the read, or the pid now belongs to a process this user cannot inspect.
		if expected, ok := l.owned[p.pid]; ok {
			if cur, err := identity(p.pid); err == nil && cur == expected {
				return true, nil
			}
		}
	}
	return false, nil
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
				if err := signalOwned(pid, ident, syscall.SIGSTOP); err != nil {
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
			if err := signalOwned(pid, ident, syscall.SIGKILL); err != nil {
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
		v, err := dec(in.Text())
		if err != nil {
			return err
		}
		ex := jStr(v, "executionId")
		b := state[ex]
		if b == nil {
			b = NewBlocks()
			state[ex] = b
		}
		h := "codex"
		if jIsStr(v, "harness") {
			h = jStr(v, "harness")
		}
		ts, _ := jU64(v, "timestamp")
		rec := jObj(v, "record")
		if rec == nil {
			rec = M{}
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
		if st := jStr(response, "state"); body["previews"] == true && (st == "stored" || st == "verifying") {
			buildPreviews(db, body, response)
		}
		if body["spool"] == true && jIsStr(body, "path") {
			os.Remove(jStr(body, "path"))
		}
	}); err != nil {
		return err
	}
	if e.storeCfg != nil {
		if r := storeRegistration(e.storeCfg, host); r != nil {
			if err := queue(db, "/api/hosts/stores", r); err != nil {
				return err
			}
		}
	}
	odb, err := openJournal(journal)
	if err != nil {
		return err
	}
	spawnOutbox(odb, url, token, host)
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(sessions, 0o700); err != nil {
		return err
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	go func() { <-sig; shutdown.Store(true) }()
	if err := e.reconcile(); err != nil {
		return err
	}
	status("working", "")
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
		if err := e.exchange(); err != nil {
			// A redirected poll is already signalled once by the edge state; other failures log here.
			var r redirected
			if !errors.As(err, &r) {
				fmt.Fprintln(os.Stderr, "executor exchange failed; retained journal, retry pending")
				status("error", "")
			} else {
				status("blocked", "auth") // edge login page or host token not accepted
			}
		} else {
			status("working", "")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// exchange: one poll; each returned command runs once. A command that failed before it went live is finished as failed
// when its accepted journal row is still this exact command.
func (e *executor) exchange() error {
	// A poll the edge answered is asked again every 2 s, not every 100 ms.
	if edgeWait(pollRoute) {
		return nil
	}
	v, roots := version, reportedRoots()
	body := contract.PollRequest{HostId: e.host, Platform: contract.Platform(platform()), Capabilities: capabilities(), Stores: e.stores(),
		Problems: edgeProblems(pollRoute), ProjectRoots: roots, Version: &v, Update: lastUpdate(e.root)}
	a := send(e.url, e.token, pollRoute, body)
	sig := updateSignal{HostID: e.host, Running: version, AutoUpdate: "on"}
	if lastSignal != nil {
		sig.Target, sig.AutoUpdate = lastSignal.Target, lastSignal.AutoUpdate
	}
	sig.PollOk, sig.PollError = pollSignal(a)
	if a.kind == answerOK {
		var r contract.PollReply
		if b, err := json.Marshal(a.value); err == nil && json.Unmarshal(b, &r) == nil {
			sig.Target = r.Target
			if r.AutoUpdate != nil {
				sig.AutoUpdate = *r.AutoUpdate
			}
		}
	}
	if err := writeUpdateSignal(e.root, sig); err != nil {
		fmt.Fprintln(os.Stderr, "executor: update.json:", err)
	}
	reply, err := answerValue(a)
	if err != nil {
		return err
	}
	for _, cv := range jArr(reply, "commands") {
		c, ok := cv.(M)
		if !ok {
			continue
		}
		if e.run(c) != nil {
			if _, isLive := e.live[jStr(c, "executionId")]; isLive {
				continue
			}
			if b, ok := e.commandBody(jStr(c, "id"), true); ok && b == enc(c) {
				if err := e.finish(c, "failed", ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
