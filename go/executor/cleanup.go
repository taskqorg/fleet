// Host cleanup (docs/designs/fleet-host-cleanup.md): every process a run starts carries the run marker
// (FLEET_EXECUTION_ID plus FLEET_EXECUTION_OWNER, this executor's data directory) and the run's own TMPDIR. At run end
// and at executor start, marked processes of runs that are not live are killed and run temporary directories removed;
// with FLEET_TMP_SWEEP=owned, old entries of /tmp the executor user owns are removed every hour. Each pass that removed
// something writes one host.cleanup span.
package main

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fleet/errtrack"
	"fleet/obs"
)

const (
	runMarkerKey = "FLEET_EXECUTION_ID"
	runOwnerKey  = "FLEET_EXECUTION_OWNER"
)

// runTempDir: the run's TMPDIR, under the executor's private data directory.
func runTempDir(root, execution string) string { return filepath.Join(root, "run-tmp", execution) }

// markRun sets the run marker and creates the run's TMPDIR (mode 0700).
func markRun(cmd *exec.Cmd, root, execution string) error {
	d := runTempDir(root, execution)
	if err := os.MkdirAll(d, 0o700); err != nil {
		return err
	}
	setEnv(cmd, runMarkerKey, execution)
	setEnv(cmd, runOwnerKey, root)
	setEnv(cmd, "TMPDIR", d)
	return nil
}

type markedProc struct {
	pid              int
	ident, execution string
}

// markedProcesses: live processes carrying this executor's run marker, with their incarnation. Another user's process
// has an unreadable environment and is never listed.
func markedProcesses(root string) ([]markedProc, error) {
	table, err := processes()
	if err != nil {
		return nil, err
	}
	var out []markedProc
	for _, p := range table {
		if p.pid == os.Getpid() || strings.HasPrefix(p.stat, "Z") {
			continue
		}
		if owner, ok := processEnv(p.pid, runOwnerKey); !ok || owner != root {
			continue
		}
		ex, ok := processEnv(p.pid, runMarkerKey)
		if !ok {
			continue
		}
		if id, err := identity(p.pid); err == nil {
			out = append(out, markedProc{p.pid, id, ex})
		}
	}
	return out, nil
}

// killMarked: SIGTERM (so a database shuts down and frees its shared memory), up to 3 s to leave, then SIGKILL; each
// signal bound to the listed incarnation. Shared memory segments the killed processes created or last used are then
// removed. Answers how many processes were signalled.
func killMarked(ps []markedProc) int {
	n := 0
	for _, p := range ps {
		if signalOwned(p.pid, p.ident, syscall.SIGTERM) == nil {
			n++
		}
	}
	for i := 0; i < 30; i++ {
		alive := false
		for _, p := range ps {
			alive = alive || !gone(p.pid, p.ident)
		}
		if !alive {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	pids := map[string]bool{}
	for _, p := range ps {
		pids[strconv.Itoa(p.pid)] = true
		if !gone(p.pid, p.ident) {
			_ = signalOwned(p.pid, p.ident, syscall.SIGKILL)
		}
	}
	removeSharedMemory(pids)
	return n
}

// removeSharedMemory: SysV segments whose creator or last user is one of pids (ipcs -m -p: Linux "shmid owner cpid
// lpid", macOS "m ID KEY MODE OWNER GROUP CPID LPID").
func removeSharedMemory(pids map[string]bool) {
	if len(pids) == 0 {
		return
	}
	out, err := exec.Command("/usr/bin/ipcs", "-m", "-p").Output()
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(out), "\n") {
		f := strings.Fields(l)
		var id, cpid, lpid string
		switch {
		case len(f) >= 8 && f[0] == "m":
			id, cpid, lpid = f[1], f[6], f[7]
		case len(f) >= 4:
			if _, err := strconv.Atoi(f[0]); err != nil {
				continue
			}
			id, cpid, lpid = f[0], f[2], f[3]
		default:
			continue
		}
		if pids[cpid] || pids[lpid] {
			_ = exec.Command("/usr/bin/ipcrm", "-m", id).Run()
		}
	}
}

type cleanupLedger struct {
	processes, dirs int
	bytes           int64
}

// record writes one host.cleanup span when the pass removed something.
func (c cleanupLedger) record(trigger string) {
	if c.processes == 0 && c.dirs == 0 {
		return
	}
	_, s := obs.Start(context.Background(), "host.cleanup", obs.KindCheck)
	s.Set("fleet.cleanup.trigger", trigger)
	s.Set("fleet.cleanup.processes", strconv.Itoa(c.processes))
	s.Set("fleet.cleanup.dirs", strconv.Itoa(c.dirs))
	s.Set("fleet.cleanup.bytes", strconv.FormatInt(c.bytes, 10))
	s.End(nil)
}

// removeDir removes a directory tree and adds it to the ledger.
func (c *cleanupLedger) removeDir(d string) {
	size := treeSize(d)
	if err := removeTree(d); err == nil {
		c.dirs++
		c.bytes += size
	}
}

// removeTree: os.RemoveAll, and once more after making every folder writable: a Go module cache (GOMODCACHE) is
// read-only, so a plain RemoveAll leaves it in place.
func removeTree(d string) error {
	if os.RemoveAll(d) == nil {
		return nil
	}
	_ = filepath.WalkDir(d, func(p string, e fs.DirEntry, err error) error {
		if err == nil && e.IsDir() {
			_ = os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(d)
}

func treeSize(d string) int64 {
	var n int64
	_ = filepath.WalkDir(d, func(_ string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() {
			if i, err := e.Info(); err == nil {
				n += i.Size()
			}
		}
		return nil
	})
	return n
}

// cleanupRun: after a run's owned tree is killed, its marked processes that escaped it (double fork, setsid) are
// killed and its TMPDIR removed. Answers the number of escaped processes (the execution span's fleet.orphans).
func cleanupRun(root, execution string) int {
	var c cleanupLedger
	if ps, err := markedProcesses(root); err == nil {
		var mine []markedProc
		for _, p := range ps {
			if p.execution == execution {
				mine = append(mine, p)
			}
		}
		c.processes = killMarked(mine)
	}
	d := runTempDir(root, execution)
	if _, err := os.Lstat(d); err == nil {
		// The run's own directory is expected: only what it held counts.
		c.bytes += treeSize(d)
		_ = removeTree(d)
	}
	if c.processes > 0 || c.bytes > 0 {
		c.dirs = max(c.dirs, 1)
	}
	c.record("run-end")
	return c.processes
}

// cleanupAtStart: after reconcileProcesses, every marked process and run TMPDIR of an execution that is not still
// unresolved is a leftover of an earlier executor.
func cleanupAtStart(root string, unresolved map[string]bool) {
	var c cleanupLedger
	if ps, err := markedProcesses(root); err == nil {
		var left []markedProc
		for _, p := range ps {
			if !unresolved[p.execution] {
				left = append(left, p)
			}
		}
		c.processes = killMarked(left)
	}
	dirs, _ := filepath.Glob(filepath.Join(root, "run-tmp", "*"))
	for _, d := range dirs {
		if !unresolved[filepath.Base(d)] {
			c.removeDir(d)
		}
	}
	c.record("start")
}

// startTmpSweep: with FLEET_TMP_SWEEP=owned (a host whose executor has a user of its own), at start and every hour,
// remove top-level entries of /tmp the executor user owns that nothing changed for FLEET_TMP_SWEEP_AGE_HOURS (24).
func startTmpSweep() {
	if os.Getenv("FLEET_TMP_SWEEP") != "owned" {
		return
	}
	age := time.Duration(setting("FLEET_TMP_SWEEP_AGE_HOURS", 24)) * time.Hour
	errtrack.Go("cleanup.tmp", func() {
		for {
			sweepTmp("/tmp", age, time.Now()).record("sweep")
			time.Sleep(time.Hour)
		}
	})
}

// sweepTmp: an entry is removed when the executor user owns it, its newest change within two levels is older than age,
// and it holds no socket within those levels (a live program's endpoint).
func sweepTmp(dir string, age time.Duration, now time.Time) cleanupLedger {
	var c cleanupLedger
	entries, err := os.ReadDir(dir)
	if err != nil {
		return c
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		st, err := os.Lstat(p)
		if err != nil || owner(st) != os.Geteuid() {
			continue
		}
		newest, keep := recent(p, st, 2)
		if keep || now.Sub(newest) < age {
			continue
		}
		if st.IsDir() {
			c.removeDir(p)
		} else if os.Remove(p) == nil {
			c.dirs++
			c.bytes += st.Size()
		}
	}
	return c
}

// recent: the newest modification time of p and its entries down to depth levels, and whether p must be kept (a socket
// or an unreadable folder among them).
func recent(p string, st os.FileInfo, depth int) (time.Time, bool) {
	newest := st.ModTime()
	if st.Mode()&os.ModeSocket != 0 {
		return newest, true
	}
	if !st.IsDir() || depth == 0 {
		return newest, false
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return newest, errors.Is(err, fs.ErrPermission)
	}
	for _, e := range entries {
		i, err := os.Lstat(filepath.Join(p, e.Name()))
		if err != nil {
			continue
		}
		t, keep := recent(filepath.Join(p, e.Name()), i, depth-1)
		if keep {
			return t, true
		}
		if t.After(newest) {
			newest = t
		}
	}
	return newest, false
}
