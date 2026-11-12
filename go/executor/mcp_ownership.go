package main

// Local MCP ownership extends the existing execution journal, never the task scheduler.
import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type mcpOwner struct {
	db                     *sql.DB
	execution, dir, socket string
	listener               *net.UnixListener
	ready                  chan struct{}
	mu                     sync.Mutex
	readyOnce              sync.Once
	closed                 bool
	generations            map[string]*mcpGeneration
	connections            map[*net.UnixConn]bool
	pending                sync.WaitGroup
}

func newMCPOwner(db *sql.DB, execution string) (*mcpOwner, error) {
	dir, err := os.MkdirTemp("", "fleet-mcp-")
	if err != nil {
		return nil, err
	}
	o := &mcpOwner{db: db, execution: execution, dir: dir, socket: filepath.Join(dir, "owner.sock"), ready: make(chan struct{}), connections: map[*net.UnixConn]bool{}, generations: map[string]*mcpGeneration{}}
	o.listener, err = net.ListenUnix("unix", &net.UnixAddr{Name: o.socket, Net: "unix"})
	if err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	if err = os.Chmod(o.socket, 0600); err != nil {
		o.listener.Close()
		os.RemoveAll(dir)
		return nil, err
	}
	go o.accept()
	return o, nil
}
func (o *mcpOwner) accept() {
	for {
		c, err := o.listener.AcceptUnix()
		if err != nil {
			return
		}
		o.mu.Lock()
		if o.closed {
			o.mu.Unlock()
			c.Close()
			return
		}
		o.connections[c] = true
		o.pending.Add(1)
		o.mu.Unlock()
		go func() {
			defer o.pending.Done()
			defer c.Close()
			defer func() { o.mu.Lock(); delete(o.connections, c); o.mu.Unlock() }()
			c.SetDeadline(time.Now().Add(probeWait))
			var request M
			if json.NewDecoder(c).Decode(&request) != nil {
				return
			}
			select {
			case <-o.ready:
			case <-time.After(probeWait):
				return
			}

			o.mu.Lock()
			if o.closed {
				o.mu.Unlock()
				return
			}
			pid, uid, err := mcpPeer(c)
			if err == nil && uid != os.Geteuid() {
				err = errors.New("MCP peer owner differs")
			}
			answer := M{}
			keep := false
			var end func()
			if err == nil {
				if jStr(request, "action") == "prepare" || jStr(request, "action") == "lookup" || jStr(request, "generation") != "" {
					answer, keep, end, err = o.generationRequest(c, pid, request)
				} else {
					err = o.register(pid, jStr(request, "identity"))
					if err == nil {
						keep = true
						end = func() {}
					}
				}
			}
			if answer == nil {
				answer = M{}
			}
			answer["registered"] = err == nil
			answer["executionId"] = o.execution
			if err != nil {
				answer["error"] = err.Error()
			}
			writeErr := json.NewEncoder(c).Encode(answer)
			o.mu.Unlock()
			if keep && err == nil {
				if writeErr == nil {
					c.SetDeadline(time.Time{})
					io.Copy(io.Discard, c)
				}
				o.mu.Lock()
				end()
				o.mu.Unlock()
			}

		}()
	}
}
func descendant(pid, root int) bool {
	table, err := processes()
	if err != nil {
		return false
	}
	parents := map[int]int{}
	for _, p := range table {
		parents[p.pid] = p.ppid
	}
	seen := map[int]bool{}
	for pid > 1 && !seen[pid] {
		if pid == root {
			return true
		}
		seen[pid] = true
		pid = parents[pid]
	}
	return false
}
func (o *mcpOwner) register(pid int, id string) error {
	tx, err := o.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var root int
	var rootID string
	var fenced int
	err = tx.QueryRow(`SELECT l.grp,p.identity,l.mcp_fenced FROM launches l JOIN owned p ON p.execution=l.execution AND p.pid=l.grp JOIN commands c ON c.id=l.command WHERE l.execution=? AND c.terminal=0`, o.execution).Scan(&root, &rootID, &fenced)
	if err != nil || fenced != 0 {
		return errors.New("MCP execution launch is not open")
	}
	verify := func() error {
		actual, e := identity(pid)
		if e != nil || id == "" || actual != id {
			return errors.New("MCP guardian identity differs")
		}
		actual, e = identity(root)
		if e != nil || actual != rootID || !descendant(pid, root) {
			return errors.New("MCP guardian belongs to another execution")
		}
		group, e := syscall.Getpgid(pid)
		if e != nil || group != pid {
			return errors.New("MCP guardian is not its group leader")
		}
		path, e := processExecutable(pid)
		self, _ := filepath.EvalSymlinks(exe())
		if e != nil || path != self {
			return errors.New("MCP guardian executable differs")
		}
		return nil
	}
	if err = verify(); err != nil {
		return err
	}
	res, err := tx.Exec(`INSERT INTO mcp_groups(execution,pid,identity) VALUES(?,?,?) ON CONFLICT(execution,pid) DO UPDATE SET identity=excluded.identity WHERE identity=excluded.identity`, o.execution, pid, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return errors.New("MCP group registration conflicts with retained identity")
	}
	if err = verify(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return verify() // lost ack never releases useful work
}
func (o *mcpOwner) close() error {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return nil
	}
	o.closed = true
	o.readyOnce.Do(func() { close(o.ready) })
	_, err := o.db.Exec("UPDATE launches SET mcp_fenced=1 WHERE execution=?", o.execution)
	o.listener.Close()
	for c := range o.connections {
		c.Close()
	}
	o.mu.Unlock()
	o.pending.Wait()
	return err
}

// The guardian stays a live incarnation after the actual server exits. Only it signals its own group.
func mcpGuardian(args []string) error {
	if len(args) != 2 {
		return errors.New("MCP guardian needs local owner and launch spec")
	}
	if g, e := syscall.Getpgid(os.Getpid()); e != nil || g != os.Getpid() {
		return errors.New("MCP guardian requires its own process group")
	}
	control := os.NewFile(3, "owner-lifetime")
	if control == nil {
		return errors.New("MCP owner lifetime missing")
	}
	if st, e := control.Stat(); e != nil || st.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("MCP owner lifetime is not a pipe")
	}
	syscall.CloseOnExec(3)
	status := os.NewFile(4, "server-status")
	if status == nil {
		return errors.New("MCP private status pipe missing")
	}
	if st, e := status.Stat(); e != nil || st.Mode()&os.ModeNamedPipe == 0 {
		return errors.New("MCP private status is not a pipe")
	}
	syscall.CloseOnExec(4)
	defer status.Close()
	defer control.Close()
	report := M{"stage": "ownership", "registration": "unknown", "guardianPid": os.Getpid(), "provenance": "guardian-private-status", "outcome": "unknown"}
	reported := false
	publish := func() {
		if !reported {
			json.NewEncoder(status).Encode(report)
			status.Close()
			reported = true
		}
	}
	defer publish()
	kill := func() { syscall.Kill(-os.Getpid(), syscall.SIGKILL) }
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGUSR1)
	defer signal.Stop(signals)
	go func() { <-signals; kill() }()
	ended := make(chan struct{})
	go func() { io.Copy(io.Discard, control); close(ended); kill() }()
	if err := privateSocket(args[0]); err != nil {
		return err
	}
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: args[0], Net: "unix"})
	if err != nil {
		return err
	}
	defer func() { publish(); c.Close() }()
	c.SetDeadline(time.Now().Add(probeWait))
	id, err := identity(os.Getpid())
	if err != nil {
		return err
	}
	report["guardianIdentity"] = id
	spec, err := dec(args[1])
	if err != nil {
		return err
	}
	if err = json.NewEncoder(c).Encode(M{"identity": id, "generation": jStr(spec, "generation"), "dependency": jStr(spec, "dependency"), "command": jStr(spec, "command"), "targetSha256": jStr(spec, "targetSha256")}); err != nil {
		return err
	}
	var answer M
	if err = json.NewDecoder(c).Decode(&answer); err != nil {
		return errors.New("MCP ownership acknowledgement unknown; no server start")
	}
	if !jBoolOr(false, answer, "registered") {
		report["registration"] = "refused"
		return errors.New("MCP ownership refused: " + jStr(answer, "error"))
	}
	report["registration"] = "acknowledged"
	report["executionId"] = answer["executionId"]
	report["stage"] = "binding"
	c.SetDeadline(time.Time{})
	go func() { io.Copy(io.Discard, c); kill() }()
	cmd, cleanup, err := mcpBoundCommand(spec)
	if err != nil {
		return err
	}
	defer cleanup()
	select {
	case <-ended:
		return errors.New("MCP owner closed before start")
	default:
	}
	current, err := identity(os.Getpid())
	if err != nil || current != id {
		return errors.New("MCP incarnation changed before work")
	}
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer discard.Close()
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, discard
	// ExtraFiles is empty: the private lifetime descriptor cannot keep itself alive in server descendants.
	report["stage"] = "spawn"
	if err = cmd.Start(); err == nil {
		cmd.Wait()
		report["stage"] = "server-exit"
		if ws, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			report["signal"], report["outcome"] = int(ws.Signal()), "signaled"
		} else {
			report["exitCode"], report["outcome"] = cmd.ProcessState.ExitCode(), "exited"
		}
	}
	publish()
	os.Stdout.Close() // downstream sees server exit, while this guardian still owns orphaned descendants
	<-ended
	return err
}
func privateSocket(path string) error {
	if !filepath.IsAbs(path) {
		return errors.New("MCP socket must be absolute")
	}
	parent, err := os.Lstat(filepath.Dir(path))
	if err != nil || !parent.IsDir() || parent.Mode().Perm() != 0700 || owner(parent) != os.Geteuid() {
		return errors.New("MCP owner directory unsafe")
	}
	st, err := os.Lstat(path)
	if err != nil || st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0600 || owner(st) != os.Geteuid() {
		return errors.New("MCP owner socket unsafe")
	}
	return nil
}

// cleanupMCPGroups never sends a bare stored PGID a signal. Missing guardian plus a surviving group stays unknown.
func cleanupMCPGroups(db *sql.DB, execution string) error {
	rows, err := db.Query("SELECT pid,identity FROM mcp_groups WHERE execution=?", execution)
	if err != nil {
		return err
	}
	ids := map[int]string{}
	for rows.Next() {
		var pid int
		var id string
		if err = rows.Scan(&pid, &id); err != nil {
			rows.Close()
			return err
		}
		ids[pid] = id
	}
	rows.Close()
	for pid, id := range ids {
		actual, e := identity(pid)
		if e == nil && actual == id {
			if e = signalOwned(pid, id, syscall.SIGUSR1); e != nil && !gone(pid, id) {
				return e
			}
		}
		// Existing reconciliation's bounded exit observation; timeout is uncertainty, never permission to rerun.
		absent := false
		for i := 0; i < 20; i++ {
			if errors.Is(syscall.Kill(-pid, 0), syscall.ESRCH) {
				absent = true
				break
			}

			time.Sleep(100 * time.Millisecond)
		}
		if !absent {
			return fmt.Errorf("MCP group %d cleanup unknown; ownership retained", pid)
		}
		if _, err = db.Exec("DELETE FROM mcp_groups WHERE execution=? AND pid=? AND identity=?", execution, pid, id); err != nil {
			return err
		}
	}
	return nil
}

type mcpChild struct {
	cmd             *exec.Cmd
	control, status *os.File
	in              io.WriteCloser
	out             io.ReadCloser
	done            chan error
}

func startMCPChild(socket string, spec any, root string, input io.Reader, output io.Writer, direct bool) (*mcpChild, error) {
	lease, writer, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		lease.Close()
		writer.Close()
		return nil, err
	}
	cmd := newCmd(exe(), root)
	cmd.Env = mcpChildEnv(os.Getenv("PATH"))
	cmd.Args = append(cmd.Args, "mcp-guardian", socket, enc(spec))
	cmd.ExtraFiles = []*os.File{lease, statusWriter}
	child := &mcpChild{cmd: cmd, control: writer, status: statusReader, done: make(chan error, 1)}
	fail := func(err error) (*mcpChild, error) {
		lease.Close()
		writer.Close()
		statusReader.Close()
		statusWriter.Close()
		if child.in != nil {
			child.in.Close()
		}
		if child.out != nil {
			child.out.Close()
		}
		return nil, err
	}
	if direct {
		// Version probes may inherit the MCP input pipe without reading it. A copying relay would steal protocol bytes.
		source, ok := input.(*os.File)
		if !ok {
			return fail(errors.New("owned dependency input must be a file descriptor"))
		}
		destination, ok := output.(*os.File)
		if !ok {
			return fail(errors.New("owned dependency output must be a file descriptor"))
		}
		cmd.Stdin, cmd.Stdout = source, destination
	} else {
		child.in, err = cmd.StdinPipe()
		if err != nil {
			return fail(err)
		}
		child.out, err = cmd.StdoutPipe()
		if err != nil {
			return fail(err)
		}
	}
	discard, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		return fail(err)
	}
	defer discard.Close()
	cmd.Stderr = discard
	if err = cmd.Start(); err != nil {
		return fail(err)
	}
	lease.Close()
	statusWriter.Close()
	go func() {
		child.done <- cmd.Wait()
		if !direct {
			if closer, ok := input.(io.Closer); ok {
				closer.Close()
			}
		}
	}()
	return child, nil
}
func (c *mcpChild) close() {
	c.control.Close()
	if c.in != nil {
		c.in.Close()
	}
	if c.out != nil {
		c.out.Close()
	}
	c.status.Close()
	<-c.done
}
func mcpStdio(socket string, spec any, input io.Reader, output io.Writer, root, policy string, parentLease ...io.ReadCloser) (resultErr error) {
	phase, status := "socket", safeMCPStatus(nil)
	if prefix := jStr(spec, "receiptPath"); prefix != "" {
		f, err := openMCPReceipt(prefix)
		if err == nil {
			// An interrupted sidecar leaves explicit unknown, never a fabricated exit.
			json.NewEncoder(f).Encode(M{"phase": phase, "returnedError": nil, "status": status, "provenance": "runtime-sidecar-observation"})
			f.Sync()
			defer func() {
				json.NewEncoder(f).Encode(M{"phase": phase, "returnedError": resultErr != nil, "status": safeMCPStatus(status), "provenance": "runtime-sidecar-observation"})
				f.Sync()
				f.Close()
			}()
		} else {
			fmt.Fprintln(os.Stderr, "MCP safe diagnostic receipt unavailable")
		}
	}
	if err := privateSocket(socket); err != nil {
		return err
	}
	spec = jObj(jClone(spec))
	phase = "dependency"
	if len(jArr(spec, "ownedDependencies")) > 0 {
		answer, connection, err := mcpOwnerCall(socket, M{"action": "prepare", "dependencies": jAt(spec, "dependencies"), "ownedDependencies": jAt(spec, "ownedDependencies")})
		if err != nil {
			return err
		}
		connection.Close()
		jObj(spec)["generation"] = answer["generation"]
		jObj(spec)["ownerSocket"] = socket
	}
	phase = "child-home"
	direct := policy == "owned-command"
	if !direct {
		home, err := os.MkdirTemp(filepath.Dir(socket), "mcp-home-")
		if err != nil {
			return err
		}
		jObj(spec)["childHome"] = home
		path, err := mcpPrivatePath(jObj(spec, "dependencies"), filepath.Dir(socket), jArr(spec, "ownedDependencies"))
		if err != nil {
			return err
		}
		jObj(spec)["childPath"] = path
	}
	phase = "spawn"
	child, err := startMCPChild(socket, spec, root, input, output, direct)
	if err != nil {
		return err
	}
	defer child.close()
	if direct {
		if len(parentLease) != 1 {
			return errors.New("owned dependency lacks parent lifetime")
		}
		lease := parentLease[0]
		defer lease.Close()
		go func() { io.Copy(io.Discard, lease); child.control.Close() }()
		status = readMCPStatus(child.status)
		if jStr(status, "outcome") != "exited" || jAt(status, "exitCode") != float64(0) {
			return errors.Join(errors.New("owned dependency result unknown or failed; no replay"), &mcpLifecycleError{status})
		}
		return nil
	}
	phase = "transport"
	source, ok := input.(io.ReadCloser)
	if !ok {
		return errors.New("MCP input must be owned and closable")
	}
	destination, ok := output.(*os.File)
	if !ok {
		return errors.New("MCP output requires an owned file descriptor")
	}
	ownedOutput, err := mcpPollableFile(destination)
	if err != nil {
		return err
	}
	defer ownedOutput.Close()
	var closing, unexpectedExit atomic.Bool
	var observedStatus atomic.Value
	queued, err := newMCPInput(source, func() { closing.Store(true); child.control.Close(); child.out.Close(); ownedOutput.Close() })
	if err != nil {
		return err
	}
	defer queued.close()
	statusDone := make(chan struct{})
	go func() {
		defer close(statusDone)
		status := readMCPStatus(child.status)
		observedStatus.Store(status)
		if !closing.Load() {
			unexpectedExit.Store(true)
			child.control.Close()
			child.in.Close()
			child.out.Close()
			queued.source.Close()
			ownedOutput.Close()
		}
	}()
	defer func() {
		closing.Store(true)
		child.control.Close() // The sole private status writer exits with its guardian; descendants never inherit it.
		<-statusDone
		child.status.Close()
		status = observedStatus.Load().(M)
		if resultErr != nil {
			resultErr = errors.Join(resultErr, &mcpLifecycleError{status})
		}
	}()
	phase = "forward"
	err = filterMCP(queued, ownedOutput, child.in, child.out, root, jArr(spec, "tools"), policy)
	if unexpectedExit.Load() {
		return errors.Join(err, errors.New("MCP server exited or status was lost; outcome unknown; no replay"))
	}
	if err == nil {
		phase = "input-eof"
	}
	return err
}

// Prefixes are supplied only by the execution owner; each invocation creates an exclusive private file.
func openMCPReceipt(prefix string) (*os.File, error) {
	invalid := errors.New("unsafe MCP diagnostic destination")
	if !filepath.IsAbs(prefix) || filepath.Clean(prefix) != prefix {
		return nil, invalid
	}
	if _, err := os.Lstat(prefix); !os.IsNotExist(err) {
		return nil, invalid
	}
	parent := filepath.Dir(prefix)
	current := string(filepath.Separator)
	parts := strings.Split(strings.TrimPrefix(parent, current), string(filepath.Separator))
	for _, part := range parts {
		if part == "" {
			return nil, invalid
		}
		current = filepath.Join(current, part)
		st, err := os.Lstat(current)
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 || (owner(st) != 0 && owner(st) != os.Geteuid()) || (st.Mode().Perm()&0022 != 0 && !(owner(st) == 0 && st.Mode()&os.ModeSticky != 0)) {
			return nil, invalid
		}
	}
	st, err := os.Stat(parent)
	if err != nil || owner(st) != os.Geteuid() || st.Mode().Perm() != 0700 {
		return nil, invalid
	}
	directory, err := openInside(string(filepath.Separator), parts)
	if err != nil {
		return nil, invalid
	}
	defer directory.Close()
	fd, err := unix.Openat(int(directory.Fd()), filepath.Base(prefix)+"-"+newUUID()+".jsonl", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return nil, invalid
	}
	return os.NewFile(uintptr(fd), "mcp-safe-receipt"), nil
}

// A directory name alone is insufficient authority to remove a path from the journal.
func removeMCPOwnerDir(path string) {
	if !filepath.IsAbs(path) || !strings.HasPrefix(filepath.Base(path), "fleet-mcp-") {
		return
	}
	st, err := os.Lstat(path)
	if err == nil && st.IsDir() && st.Mode().Perm() == 0700 && owner(st) == os.Geteuid() {
		filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				os.Chmod(p, 0700)
			}
			return err
		})
		os.RemoveAll(path)
	}
}

// Keep reading input while a response is pending so queued requests cannot hide owner EOF. Bytes stay in memory only.
type mcpInput struct {
	source   io.ReadCloser
	original io.Closer
	mu       sync.Mutex
	cond     *sync.Cond
	buffer   bytes.Buffer
	err      error
	done     chan struct{}
}

func newMCPInput(source io.ReadCloser, closed func()) (*mcpInput, error) {
	file, ok := source.(*os.File)
	if !ok {
		return nil, errors.New("MCP input requires an owned file descriptor")
	}
	owned, err := mcpPollableFile(file)
	if err != nil {
		return nil, err
	}
	q := &mcpInput{source: owned, original: source, done: make(chan struct{})}
	q.cond = sync.NewCond(&q.mu)

	go func() {
		defer close(q.done)
		buffer := make([]byte, 32*1024)
		for {
			n, err := q.source.Read(buffer)
			q.mu.Lock()
			if n > 0 {
				q.buffer.Write(buffer[:n])
			}
			q.err = err
			q.cond.Broadcast()
			q.mu.Unlock()
			if err != nil {
				closed()
				return
			}
		}
	}()
	return q, nil
}
func (q *mcpInput) Read(p []byte) (int, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.buffer.Len() == 0 && q.err == nil {
		q.cond.Wait()
	}
	if q.buffer.Len() > 0 {
		return q.buffer.Read(p)
	}
	return 0, q.err
}
func (q *mcpInput) close() { q.source.Close(); q.original.Close(); <-q.done }

// Inherited pipes may be blocking; an owned pollable duplicate makes Close interrupt pending I/O.
func mcpPollableFile(file *os.File) (*os.File, error) {
	fd, err := unix.Dup(int(file.Fd()))
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	if err = unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, err
	}
	return os.NewFile(uintptr(fd), "mcp-io"), nil
}

// Project the private status explicitly; missing status never establishes exit.
type mcpLifecycleError struct{ status M }

func (e *mcpLifecycleError) Error() string {
	return "MCP lifecycle interrupted; outcome unknown; no replay: " + enc(e.status)
}
func safeMCPStatus(raw M) M {
	out := M{"stage": "unknown", "registration": "unknown", "outcome": "unknown", "provenance": "status-unavailable"}
	for key, values := range map[string][]string{"stage": {"ownership", "binding", "spawn", "server-exit"}, "registration": {"unknown", "refused", "acknowledged"}, "outcome": {"unknown", "exited", "signaled"}, "provenance": {"guardian-private-status"}} {
		for _, allowed := range values {
			if jStr(raw, key) == allowed {
				out[key] = allowed
			}
		}
	}
	identity := jStr(raw, "guardianIdentity")
	if len(identity) > 0 && len(identity) <= 80 && onlyBytes(identity, func(b byte) bool { return b >= '0' && b <= '9' || b == '-' }) {
		out["guardianIdentity"] = identity
	}
	execution := jStr(raw, "executionId")
	if len(execution) == 36 && execution[8] == '-' && execution[13] == '-' && execution[18] == '-' && execution[23] == '-' {
		hex := strings.ReplaceAll(execution, "-", "")
		if len(hex) == 32 && onlyBytes(hex, func(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' }) {
			out["executionId"] = execution
		}
	}

	for key, max := range map[string]float64{"guardianPid": 2147483647, "exitCode": 255, "signal": 127} {
		if n, ok := jAt(raw, key).(float64); ok && n >= 0 && n <= max && n == float64(int64(n)) {
			out[key] = n
		}
	}
	if out["outcome"] == "exited" && !has(out, "exitCode") || out["outcome"] == "signaled" && !has(out, "signal") {
		out["outcome"] = "unknown"
	}
	return out
}

func readMCPStatus(input io.Reader) M {
	var raw M
	if json.NewDecoder(input).Decode(&raw) != nil {
		return safeMCPStatus(nil)
	}
	return safeMCPStatus(raw)
}
