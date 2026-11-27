// Inventory and disk readings (docs/designs/fleet-host-cleanup.md sections 11 and 12): the hourly sweep lists every
// session directory that still holds a worktree and measures what Fleet keeps on the host, sends both in one request
// (POST /api/hosts/inventory) and writes one host.disk span. The coordinator queues teardowns for settled sessions and
// answers the ones it has no live row for; their worktrees are torn down here through the same teardown, keepWhen
// included.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"fleet/contract"
	"fleet/obs"
)

// inventoryEntry: one session directory with a worktree, as measured.
type inventoryEntry struct {
	root, id  string
	legacy    bool
	worktrees []string // the worktree directories in it (worktree, workspaces/*/worktree)
	bytes     int64
	changed   time.Time
	output    bool
}

// worktreesIn: the worktree directories of a session directory (its own and its workspaces'); set-aside copies
// (worktree-stale-*) are not worktrees git knows, so they are left to teardown's removeStale.
func worktreesIn(p string) []string {
	var out []string
	if exists(filepath.Join(p, "worktree", ".git")) {
		out = append(out, filepath.Join(p, "worktree"))
	}
	ws, _ := filepath.Glob(filepath.Join(p, "workspaces", "*", "worktree"))
	for _, w := range ws {
		if exists(filepath.Join(w, ".git")) {
			out = append(out, w)
		}
	}
	return out
}

// takeInventory walks the sessions roots once: the directories that hold a worktree (measured each) and the disk use
// of the others.
func takeInventory(roots []string) ([]inventoryEntry, contract.HostDisk) {
	var entries []inventoryEntry
	var worktreeBytes, otherBytes float64
	var worktreeCount, otherCount int64
	for i, root := range roots {
		list, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, en := range list {
			p := filepath.Join(root, en.Name())
			st, err := os.Lstat(p)
			if err != nil || !st.IsDir() || strings.HasPrefix(en.Name(), ".") {
				continue
			}
			size := treeSize(p)
			ws := worktreesIn(p)
			if len(ws) == 0 {
				otherBytes += float64(size)
				otherCount++
				continue
			}
			newest, _ := recent(p, st, 2)
			e := inventoryEntry{root: root, id: en.Name(), legacy: i > 0, worktrees: ws, bytes: size, changed: newest}
			for _, w := range ws {
				if st, err := os.Lstat(filepath.Join(w, "output")); err == nil && st.IsDir() {
					e.output = true
				}
			}
			entries = append(entries, e)
			worktreeBytes += float64(size)
			worktreeCount++
		}
	}
	d := contract.HostDisk{SessionsWorktree: &worktreeBytes, SessionsWorktreeCount: &worktreeCount, SessionsOther: &otherBytes, SessionsOtherCount: &otherCount}
	return entries, d
}

// measureData: the executor's own data directory and releases root, added to a disk reading.
func measureData(d *contract.HostDisk, root string, tmpOwned bool) {
	f := func(v int64) *float64 { x := float64(v); return &x }
	d.RunTmp = f(treeSize(filepath.Join(root, "run-tmp")))
	d.Candidates = f(treeSize(filepath.Join(root, "candidates")))
	d.Spool = f(treeSize(filepath.Join(root, "spool")) + treeSize(filepath.Join(root, "spans")))
	var journal int64
	for _, n := range []string{"journal.sqlite", "journal.sqlite-wal", "journal.sqlite-shm"} {
		if st, err := os.Stat(filepath.Join(root, n)); err == nil {
			journal += st.Size()
		}
	}
	d.Journal = f(journal)
	if exe, err := os.Executable(); err == nil {
		if exe, err = filepath.EvalSymlinks(exe); err == nil {
			releases := filepath.Dir(filepath.Dir(exe))
			if filepath.Base(releases) == "releases" {
				harness := treeSize(filepath.Join(releases, "harnesses"))
				d.Releases, d.Harnesses = f(treeSize(releases)-harness), f(harness)
			}
		}
	}
	if tmpOwned {
		var n int64
		if list, err := os.ReadDir("/tmp"); err == nil {
			for _, en := range list {
				p := filepath.Join("/tmp", en.Name())
				if st, err := os.Lstat(p); err == nil && owner(st) == os.Geteuid() {
					n += treeSize(p)
				}
			}
		}
		d.TmpOwned = f(n)
	}
	var fs syscall.Statfs_t
	if syscall.Statfs(root, &fs) == nil {
		d.Free, d.Total = f(int64(fs.Bavail)*int64(fs.Bsize)), f(int64(fs.Blocks)*int64(fs.Bsize))
	}
}

// diskSpan writes the host.disk check span: every reading as fleet.disk.<name> plus the largest sessions by project
// are left to the coordinator, which knows each session's project.
func diskSpan(d contract.HostDisk) {
	_, s := obs.Start(context.Background(), "host.disk", obs.KindCheck)
	b, _ := json.Marshal(d)
	var m map[string]float64
	json.Unmarshal(b, &m)
	for k, v := range m {
		s.Set("fleet.disk."+snake(k), strconv.FormatFloat(v, 'f', 0, 64))
	}
	s.End(nil)
}

// snake: sessionsWorktreeCount -> sessions_worktree_count.
func snake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteByte('_')
			}
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}

// inventoryPass: one hourly inventory; answers the ledger of the worktrees torn down here.
func (e *executor) inventoryPass(roots []string, tmpOwned bool) cleanupLedger {
	startedAt := nowMs()
	entries, disk := takeInventory(roots)
	measureData(&disk, e.root, tmpOwned)
	diskSpan(disk)
	if e.url == "" {
		return cleanupLedger{}
	}
	req := contract.HostInventoryRequest{Sessions: make([]contract.HostInventorySession, 0, len(entries)), Disk: &disk}
	for _, en := range entries {
		out := en.output
		req.Sessions = append(req.Sessions, contract.HostInventorySession{Id: en.id, Bytes: float64(en.bytes), ChangedAt: float64(en.changed.UnixMilli()), Output: &out})
	}
	ctx, cancel := context.WithTimeout(e.baseCtx(), time.Minute)
	defer cancel()
	a := sendWithClient(ctx, httpClient(time.Minute), e.url, e.token, contract.RouteInventory.Path, contract.RouteInventory.Path, req)
	report, c := e.reconcileInventory(startedAt, entries, a)
	e.lastInventory.Store(report)
	return c
}

// Only Fleet session IDs are reported. An arbitrary legacy directory name can contain private text.
var inventorySessionID = regexp.MustCompile(`(?i)^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

func (e *executor) reconcileInventory(startedAt int64, entries []inventoryEntry, a answer) (*contract.DiagnosisInventory, cleanupLedger) {
	report := &contract.DiagnosisInventory{
		StartedAt: startedAt, Response: contract.InventoryResponseResponseFailed, Candidates: []contract.InventoryCandidate{},
	}
	var c cleanupLedger
	if a.kind != answerOK {
		if a.kind == answerEdge && a.status >= 200 && a.status < 300 {
			report.Response = contract.InventoryResponseDecodeFailed
		}
		return report, c
	}
	var reply contract.HostInventoryReply
	report.Response = contract.InventoryResponseDecodeFailed
	if json.Unmarshal(a.value, &reply) != nil {
		return report, c
	}
	// A malformed but decodable reply is still handled by the existing unknown-ID and journal checks.
	if reply.Validate() == nil {
		report.Response = contract.InventoryResponseReceived
		report.InventoryAt = reply.InventoryAt
	}
	unknown := map[string]bool{}
	for _, id := range reply.Unknown {
		unknown[id] = true
	}
	for _, en := range entries {
		if !unknown[en.id] {
			continue
		}
		block := e.sessionCleanupBlock(en.id)
		candidate := contract.InventoryCandidate{Root: contract.InventoryRootEffective}
		if en.legacy {
			candidate.Root = contract.InventoryRootLegacy
		}
		if inventorySessionID.MatchString(en.id) {
			candidate.SessionId = &en.id
		}
		for _, w := range en.worktrees {
			candidate.Layout = contract.InventoryLayoutDirect
			if w != filepath.Join(en.root, en.id, "worktree") {
				candidate.Layout = contract.InventoryLayoutWorkspace
			}
			candidate.Outcome = block
			if block == "" {
				var removed cleanupLedger
				removed, candidate.Outcome = teardownUnknown(w, en.id)
				c.add(removed)
			}
			report.Candidates = append(report.Candidates, candidate)
		}
	}
	return report, c
}

// teardownUnknown: the worktree of a session the coordinator has no live row for goes through teardown with the
// default keepWhen; its outputs go first, since no session can collect them any more. The branch goes only when merged
// (git branch -d).
func teardownUnknown(w, session string) (cleanupLedger, contract.InventoryOutcome) {
	var c cleanupLedger
	common, err := hardenedGit("-C", w, "rev-parse", "--path-format=absolute", "--git-common-dir").Output()
	if err != nil {
		return c, contract.InventoryOutcomeGitCommonDirError
	}
	top := filepath.Dir(strings.TrimSpace(string(common)))
	dir := filepath.Dir(w)
	name := session
	if filepath.Base(filepath.Dir(dir)) == "workspaces" { // a workspace's worktree: its branch names the workspace session
		name = session + "-" + filepath.Base(dir)
	}
	size := treeSize(w)
	t, outcome := teardownWithOutcome(dir, name, top, contract.ProjectContext{}, teardownOptions{dropOutput: true})
	if t.Removed {
		c.dirs++
		c.bytes += size
	}
	return c, outcome
}

// projectPersonal: the command's project has data: personal (docs/designs/fleet-project-access.md#personal-data).
func projectPersonal(project map[string]json.RawMessage) bool {
	var d string
	return json.Unmarshal(project["data"], &d) == nil && d == "personal"
}

// dropPersonal: a personal project's run leaves its outputs and inputs nowhere on disk once its uploads are queued
// (the upload reads its own spool copy).
func dropPersonal(dir string, output []string) {
	_ = removeTree(filepath.Join(append([]string{dir}, output...)...))
	_ = removeTree(filepath.Join(dir, "input"))
}
