package main

import (
	"bufio"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"fleet/contract"
	"fleet/executor/supervise"
)

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
	frame := typedRaw[struct {
		Method *string `json:"method"`
		Id     *uint64 `json:"id"`
	}](b)
	method := "response"
	if frame.Method != nil {
		method = *frame.Method
	}
	var rid any
	if frame.Id != nil {
		rid = *frame.Id
	}
	return M{"type": "protocol.write", "method": method, "requestId": rid, "bytes": len(b), "sha256": sha(b),
		"delivery": "written to local transport; native acceptance unknown"}, nil
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
	swept   bool // cleanupRun ran
	orphans int  // marked processes that escaped the owned tree, killed at run end (fleet.orphans)
}

// commandID and sessionID: the live command's identities, checked when it ran ("" for one that is not).
func (l *live) commandID() string { s, _ := idOf(l.command, "id"); return s }

func (l *live) sessionID() string { s, _ := idOf(l.command, "sessionId"); return s }

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
	cleanupAtStart(e.root, unresolved)
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
		// What escaped the owned tree (double fork, setsid) still carries the run marker.
		if !l.swept {
			l.orphans, l.swept = cleanupRun(e.root, execution), true
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
			n, _ := projection["session"].(string) // the in-memory projection Blocks built, not received JSON
			if n != "" && (!l.nativeObserved || n != l.native) {
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
	// Diagnostic retention must not turn an original unknown/failure into another execution outcome
	// or hold a clean exit in waiting behind optional diagnostic log uploads.
	var diagnostics []string
	if err := e.artifacts(dir, []string{execution + ".mcp"}, execution, &diagnostics); err != nil {
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
