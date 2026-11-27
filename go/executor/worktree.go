package main

// Worktree lifecycle: roots, base, branch, links, setup, teardown; trial worktrees for lab scripts.

import (
	"encoding/json"
	"errors"
	"fleet/contract"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// setAsideStale renames a working-area directory that is no git worktree (no .git: an interrupted teardown removed
// the link and left the files) to <dir>.stale-<unix ms>, so git worktree add gets an empty path. Nothing is deleted:
// the files may hold the session's only copy of its work. 2026-11-24: such a leftover failed every start of a session.
func setAsideStale(w string) error {
	if _, err := os.Lstat(w); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	return os.Rename(w, w+".stale-"+strconv.FormatInt(time.Now().UnixMilli(), 10))
}

// removeStale deletes the <w>.stale-* copies setAsideStale made (links inside are removed, never followed).
func removeStale(w string) {
	stale, _ := filepath.Glob(w + ".stale-*")
	for _, p := range stale {
		makeTreeWritable(p)
		os.RemoveAll(p)
	}
}

// hardenedGit: cleared environment, no system or global config, hooks and fsmonitor off, file protocol refused.
func hardenedGit(args ...string) *exec.Cmd {
	c := exec.Command("git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "protocol.file.allow=never"}, args...)...)
	c.Env = []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	if p, ok := os.LookupEnv("PATH"); ok {
		c.Env = append(c.Env, "PATH="+p)
	}
	return c
}

// cleanCmd: child environment from an allowlist only, never executor secrets.
func cleanCmd(name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Env = []string{}
	for _, k := range []string{"PATH", "HOME", "SSH_AUTH_SOCK", "LANG"} {
		if v, ok := os.LookupEnv(k); ok {
			c.Env = append(c.Env, k+"="+v)
		}
	}
	return c
}

func splitRoots() []string {
	var v []string
	for _, s := range strings.Split(os.Getenv("FLEET_PROJECT_ROOTS"), ":") {
		if strings.HasPrefix(s, "/") {
			v = append(v, s)
		}
	}
	return v
}

func canon(p string) (string, error) {
	a, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(a)
}

// under is Path::starts_with: component-wise prefix.
func under(p, root string) bool {
	return p == root || strings.HasPrefix(p, strings.TrimSuffix(root, "/")+"/")
}

func projectRoots() []string {
	var v []string
	for _, s := range splitRoots() {
		if c, err := canon(s); err == nil {
			v = append(v, c)
		}
	}
	return v
}

// projectRootsFor: the checkouts the project's environments declare for this host, then FLEET_PROJECT_ROOTS as an extra
// allow-list. A project's environment is its own allow-list, so a new project needs no edit of the host's env file
// (DAM Lab, 2026-11-25: owner-mac lacked /Users/kirs/Projects/dam and every Claude trial waited unplaced). The
// forbidden directories still apply to both.
func projectRootsFor(project contract.ProjectContext, host string) []string {
	v := []string{}
	for _, e := range project.Environments {
		if str0(e.HostId) == host && e.CheckoutPath != nil && strings.HasPrefix(*e.CheckoutPath, "/") {
			if c, err := canon(*e.CheckoutPath); err == nil {
				v = append(v, c)
			}
		}
	}
	return append(v, projectRoots()...)
}

// reportedRoots: the roots as configured and as resolved, for the coordinator's check.
func reportedRoots() []string {
	v := append(splitRoots(), projectRoots()...)
	sort.Strings(v)
	out := []string{}
	for i, s := range v {
		if i == 0 || s != v[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// repoPath: payload workingDirectory, else this host's first project checkout.
func repoPath(workingDirectory *string, project contract.ProjectContext, host string) (string, bool) {
	if workingDirectory != nil {
		return *workingDirectory, true
	}
	var primary *string
	if r := gitRepo(project, nil); r != nil {
		primary = r.Id
	}
	for _, e := range project.Environments {
		if str0(e.HostId) == host && e.CheckoutPath != nil && (e.Repo == nil || primary != nil && *e.Repo == *primary) {
			return *e.CheckoutPath, true
		}
	}
	return "", false
}

func tail(b []byte) string {
	s := string(b)
	if len(s) <= 4000 {
		return s
	}
	n := len(s) - 4000
	for n < len(s) && (s[n]&0xC0) == 0x80 {
		n++
	}
	return s[n:]
}

func overlaps(p string, forbidden []string) bool {
	for _, f := range forbidden {
		if c, err := canon(f); err == nil && (under(p, c) || under(c, p)) {
			return true
		}
	}
	return false
}

func gitFetch(top string, project contract.ProjectContext) bool {
	access, err := gitEnv(project, gitRepo(project, nil), connectionsDir())
	if err != nil {
		return false
	}
	c := cleanCmd("git", "-c", "core.hooksPath=/dev/null", "-C", top, "fetch", "--quiet", "origin")
	gitApply(c, access)
	return c.Run() == nil
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

// worktree: a project session whose working directory is a git repository runs in its own worktree (branch
// <prefix>/<session>, or detached at the same base when the start says branch false: a session with no branch). The
// payload path is untrusted: it must resolve under a configured project root and outside the executor and session dirs.
// Returns (worktree, checkout toplevel, created); ok false means no worktree.
func worktree(dir, session, repo string, hasRepo bool, roots, forbidden []string, project contract.ProjectContext, detached bool) (w, top string, created, ok bool, err error) {
	if !hasRepo {
		return
	}
	allowed := func(p string) string {
		if overlaps(p, forbidden) {
			return ""
		}
		for _, r := range roots {
			if under(p, r) {
				return r
			}
		}
		return ""
	}
	r, e := canon(repo)
	if e != nil {
		err = errors.New("working directory unavailable")
		return
	}
	root := allowed(r)
	if root == "" {
		err = errors.New("working directory outside configured project roots")
		return
	}
	rev := func(arg string) string {
		c := hardenedGit("-C", r, "rev-parse", "--path-format=absolute", arg)
		c.Env = append(c.Env, "GIT_CEILING_DIRECTORIES="+root)
		o, e := c.Output()
		if e != nil {
			return ""
		}
		p, e := canon(strings.TrimSpace(string(o)))
		if e != nil {
			return ""
		}
		return p
	}
	top = rev("--show-toplevel")
	common := rev("--git-common-dir")
	if top == "" || common == "" {
		return "", "", false, false, nil
	}
	if allowed(top) == "" || allowed(common) == "" {
		err = errors.New("repository outside configured project roots")
		return
	}
	w = filepath.Join(dir, "worktree")
	if exists(filepath.Join(w, ".git")) {
		return w, top, false, true, nil
	}
	if err = setAsideStale(w); err != nil {
		return
	}
	wt := contract.ProjectWorktree{}
	if project.Worktree != nil {
		wt = *project.Worktree
	}
	branch := sessionBranch(project, session)
	ceil := "GIT_CEILING_DIRECTORIES=" + root
	v := hardenedGit("-C", top, "rev-parse", "--verify", "--quiet", branch)
	v.Env = append(v.Env, ceil)
	args := []string{"-C", top, "worktree", "add"}
	if !detached && v.Run() == nil {
		args = append(args, w, branch)
	} else {
		if detached {
			args = append(args, "--detach", w)
		} else {
			args = append(args, "--no-track", "-b", branch, w)
		}
		// Base: explicit baseRef, else fresh origin/<defaultBranch> when startFromOrigin, else HEAD.
		if wt.BaseRef != nil {
			args = append(args, *wt.BaseRef)
		} else if wt.StartFromOrigin != nil && *wt.StartFromOrigin && gitFetch(top, project) {
			def := "main"
			if repos := project.Repos; len(repos) > 0 && repos[0].DefaultBranch != nil {
				def = *repos[0].DefaultBranch
			}
			args = append(args, "origin/"+def)
		}
	}
	c := hardenedGit(args...)
	c.Env = append(c.Env, ceil)
	if c.Run() != nil {
		err = errors.New("git worktree add failed")
		return
	}
	// linkPaths and sharedDirectories: symlinks into the main checkout; only existing, relative, non-escaping paths.
	for _, list := range [][]json.RawMessage{wt.LinkPaths, wt.SharedDirectories} {
		for _, rel := range stringsOf(list) {
			if strings.HasPrefix(rel, "/") || contains(strings.Split(rel, "/"), "..") {
				continue
			}
			src, dst := filepath.Join(top, rel), filepath.Join(w, rel)
			if !exists(src) || exists(dst) {
				continue
			}
			d := filepath.Dir(dst)
			if e := os.MkdirAll(d, 0o755); e != nil {
				err = e
				return
			}
			cd, e1 := canon(d)
			cw, e2 := canon(w)
			cs, e3 := canon(src)
			if e1 != nil || e2 != nil || e3 != nil || !under(cd, cw) || !under(cs, top) {
				continue
			}
			if e := os.Symlink(src, dst); e != nil {
				err = e
				return
			}
		}
	}
	return w, top, true, true, nil
}

func isHex(s string) bool {
	return onlyBytes(s, func(b byte) bool { return b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F' })
}

// trialWorktree: detached at the pinned commit under the trial's session dir, no links. A retried command reuses it.
func trialWorktree(dir, repo, commit string, roots, forbidden []string) (string, string, error) {
	if !(len(commit) == 40 || len(commit) == 64) || !isHex(commit) {
		return "", "", errors.New("trial commit must be a full hash")
	}
	r, err := canon(repo)
	if err != nil {
		return "", "", errors.New("working directory unavailable")
	}
	inside := func(p string) bool {
		if overlaps(p, forbidden) {
			return false
		}
		for _, x := range roots {
			if under(p, x) {
				return true
			}
		}
		return false
	}
	if !inside(r) {
		return "", "", errors.New("working directory outside configured project roots")
	}
	o, err := hardenedGit("-C", r, "rev-parse", "--path-format=absolute", "--show-toplevel", "--git-common-dir").Output()
	var paths []string
	for _, l := range strings.Split(strings.TrimSpace(string(o)), "\n") {
		if p, e := canon(strings.TrimSpace(l)); e == nil {
			paths = append(paths, p)
		}
	}
	if err != nil || len(paths) != 2 || !inside(paths[0]) || !inside(paths[1]) {
		return "", "", errors.New("repository outside configured project roots")
	}
	w := filepath.Join(dir, "trial")
	if exists(filepath.Join(w, ".git")) {
		head, _ := hardenedGit("-C", w, "rev-parse", "HEAD").Output()
		if !strings.EqualFold(strings.TrimSpace(string(head)), commit) {
			return "", "", errors.New("trial worktree exists at another commit")
		}
		return w, paths[0], nil
	}
	if err := setAsideStale(w); err != nil {
		return "", "", err
	}
	if hardenedGit("-C", paths[0], "worktree", "add", "--detach", w, commit).Run() != nil {
		return "", "", errors.New("git worktree add failed (commit missing?)")
	}
	return w, paths[0], nil
}

func removeTrial(top, w string) bool {
	removeStale(w) // a trial checks out a fixed commit: its set-aside copies hold no work
	makeTreeWritable(w)
	return hardenedGit("-C", top, "worktree", "remove", "--force", w).Run() == nil
}

// makeTreeWritable adds owner write/search bits to directories (0o700) and owner write to regular files (0o200)
// inside w so read-only tool caches (e.g. Go module caches) can be removed, without following symlinks.
func makeTreeWritable(w string) {
	st, err := os.Lstat(w)
	if err != nil || st.Mode()&os.ModeSymlink != 0 || !st.IsDir() {
		return
	}
	root, err := os.OpenRoot(w)
	if err != nil {
		return
	}
	defer root.Close()
	_ = fs.WalkDir(root.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil || d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		need := fs.FileMode(0)
		switch {
		case d.IsDir():
			need = 0o700
		case d.Type().IsRegular():
			need = 0o200
		default:
			return nil
		}
		info, err := root.Lstat(rel)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		if mode := info.Mode().Perm(); mode&need != need {
			_ = root.Chmod(rel, mode|need)
		}
		return nil
	})
}

// projectScript runs a project script in the worktree and waits; event data with exit code and truncated output.
func projectScript(name, cmd, top, w, session string) contract.ProjectScriptResult {
	c := cleanCmd("sh", "-c", cmd)
	c.Dir = w
	c.Env = append(c.Env, "FLEET_ROOT_PATH="+top, "FLEET_WORKTREE_PATH="+w, "FLEET_SESSION_ID="+session)
	var so, se strings.Builder
	c.Stdout, c.Stderr = &so, &se
	err := c.Run()
	if err != nil && c.ProcessState == nil {
		msg := err.Error()
		return contract.ProjectScriptResult{Script: name, Ok: false, Stderr: &msg}
	}
	var code *int64
	if n := c.ProcessState.ExitCode(); n != -1 {
		v := int64(n)
		code = &v
	}
	stdout, stderr := tail([]byte(so.String())), tail([]byte(se.String()))
	return contract.ProjectScriptResult{Script: name, ExitCode: code, Ok: c.ProcessState.Success(), Stdout: &stdout, Stderr: &stderr}
}

// keepReasons: the project's cleanup.keepWhen (default uncommitted, unpushed, teardown-failed).
func keepReasons(project contract.ProjectContext) map[string]bool {
	keep := map[string]bool{}
	list := []string{"uncommitted", "unpushed", "teardown-failed"}
	if project.Cleanup != nil && project.Cleanup.KeepWhen != nil {
		list = stringsOf(project.Cleanup.KeepWhen)
	}
	for _, s := range list {
		keep[s] = true
	}
	return keep
}

// teardown: teardown script, then remove the worktree unless a cleanup.keepWhen reason holds (uncommitted changes,
// commits on no remote branch, a failed teardown script). The branch goes with -d (merged only), or -D
// when the work was delivered (deleteBranch: the task was accepted).
func teardown(dir, session, top string, project contract.ProjectContext, deleteBranch, detached bool) contract.Teardown {
	return teardownWith(dir, session, top, project, teardownOptions{deleteBranch: deleteBranch, detached: detached, roots: []string{dir}})
}

// teardownOptions: how a teardown treats the worktree (docs/designs/fleet-host-cleanup.md#10-worktree-lifecycle-for-every-run-kind).
// dropOutput: the run's results are stored (or nothing can collect them any more), so worktree/output goes before the
// keepWhen checks. ignoreUncommitted: a personal project's pipeline step, which writes no tracked work.
// roots: Fleet's own directories (the executor data and sessions roots); a worktree outside them, or under any
// .claude/worktrees (a person's or Claude Code's checkout), is never torn down.
type teardownOptions struct {
	deleteBranch, detached, dropOutput, ignoreUncommitted bool
	roots                                                 []string
}

// fleetOwned: w is a real directory (not a link) whose real path is under one of roots and not under .claude/worktrees.
func fleetOwned(w string, roots []string) bool {
	m, err := os.Lstat(w)
	if err != nil || m.Mode()&os.ModeSymlink != 0 || !m.IsDir() {
		return false
	}
	c, err := canon(w)
	if err != nil {
		return false
	}
	if strings.Contains(filepath.ToSlash(c)+"/", "/.claude/worktrees/") {
		return false
	}
	for _, r := range roots {
		if rc, err := canon(r); err == nil && under(c, rc) {
			return true
		}
	}
	return false
}

func teardownWith(dir, session, top string, project contract.ProjectContext, opt teardownOptions) contract.Teardown {
	deleteBranch, detached := opt.deleteBranch, opt.detached
	keep := keepReasons(project)
	if opt.ignoreUncommitted {
		delete(keep, "uncommitted")
	}
	w := filepath.Join(dir, "worktree")
	if exists(w) && !fleetOwned(w, opt.roots) {
		return refusedTeardown("outside-fleet-roots")
	}
	if opt.dropOutput {
		// Only untracked results: a repository that tracks files under output/ keeps them.
		if st, err := os.Lstat(filepath.Join(w, "output")); err == nil && st.IsDir() {
			if tracked, err := hardenedGit("-C", w, "ls-files", "--", "output").Output(); err == nil && len(tracked) == 0 {
				_ = removeTree(filepath.Join(w, "output"))
			}
		}
	}
	removeProjectEnv(session) // the dev environment is disposable; env_exec starts it again for a kept worktree
	// Copies set aside by setAsideStale go when the task was accepted, or when the session never had a branch of its own
	// (a detached conversation keeps no work). A cancelled or failed task's copy stays: it may be the only copy of its work.
	if deleteBranch || detached {
		removeStale(w)
	}
	if !exists(filepath.Join(w, ".git")) {
		return refusedTeardown("no-worktree")
	}
	out := contract.Teardown{}
	if project.Scripts != nil && project.Scripts.Teardown != nil {
		t := projectScript("teardown", *project.Scripts.Teardown, top, w, session)
		out.Teardown = &t
		if !t.Ok && keep["teardown-failed"] {
			out.Reason = new("teardown-failed")
			return out
		}
	}
	// Fleet-made links are excluded from the dirty check and removed before `worktree remove`.
	var links []string
	if wt := project.Worktree; wt != nil {
		for _, list := range [][]json.RawMessage{wt.LinkPaths, wt.SharedDirectories} {
			for _, r := range stringsOf(list) {
				if m, err := os.Lstat(filepath.Join(w, r)); err == nil && m.Mode()&os.ModeSymlink != 0 {
					links = append(links, r)
				}
			}
		}
	}
	args := []string{"-C", w, "status", "--porcelain", "--", "."}
	for _, r := range links {
		args = append(args, ":(exclude)"+r)
	}
	o, err := hardenedGit(args...).Output()
	if keep["uncommitted"] && (err != nil || len(o) > 0) {
		out.Reason = new("uncommitted")
		return out
	}
	// Delivered work (deleteBranch: the task was accepted) is not unpushed even when its squash-merged remote branch
	// was deleted and pruned; uncommitted changes still keep the worktree.
	if keep["unpushed"] && !deleteBranch {
		// Commits on no remote-tracking branch (session branches are created --no-track, so there is no @{u} to ask). A
		// detached worktree (a session with no branch) also started from the checkout's own branches: what they hold is not
		// its work, so a local unpushed commit of the checkout never keeps it.
		// Branchless by how it started (the teardown command's branch: false), never by the refs it finds now.
		not := []string{"-C", w, "rev-list", "-n", "1", "HEAD", "--not", "--remotes"}
		if detached {
			not = append(not, "--branches")
		}
		ahead, err := hardenedGit(not...).Output()
		if err != nil || len(strings.TrimSpace(string(ahead))) > 0 {
			out.Reason = new("unpushed")
			return out
		}
	}
	for _, r := range links {
		os.Remove(filepath.Join(w, r))
	}
	makeTreeWritable(w)
	removed := hardenedGit("-C", top, "worktree", "remove", w).Run() == nil
	out.Removed = removed
	if !removed {
		out.Reason = new("worktree-remove-failed")
		return out
	}
	if detached { // no session branch was ever made: nothing to delete or report
		return out
	}
	flag := "-d"
	if deleteBranch {
		flag = "-D"
	}
	deleted := hardenedGit("-C", top, "branch", flag, sessionBranch(project, session)).Run() == nil
	out.BranchDeleted = &deleted
	return out
}

// sessionBranch: <prefix>/<session>, the branch a session with a branch works on.
func sessionBranch(project contract.ProjectContext, session string) string {
	prefix := "fleet"
	if project.Worktree != nil && project.Worktree.BranchPrefix != nil {
		prefix = *project.Worktree.BranchPrefix
	}
	return prefix + "/" + session
}

// refusedTeardown: a teardown that removed nothing, with its reason.
func refusedTeardown(reason string) contract.Teardown {
	return contract.Teardown{Removed: false, Reason: &reason}
}

// nativeGitRoots grants this checkout's common metadata and the linked worktree's more-specific admin directory.
func nativeGitRoots(checkout, work string) ([]any, error) {
	read := func(dir, arg string) (string, error) {
		b, err := hardenedGit("-C", dir, "rev-parse", "--path-format=absolute", arg).Output()
		if err != nil {
			return "", err
		}
		return canon(strings.TrimSpace(string(b)))
	}
	common, err := read(checkout, "--git-common-dir")
	if err != nil {
		return nil, err
	}
	own, err := read(work, "--git-common-dir")
	if err != nil {
		return nil, err
	}
	admin, err := read(work, "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	if common != own || !under(admin, common) {
		return nil, errors.New("native Git worktree ownership mismatch")
	}
	roots := []any{common}
	if admin != common {
		roots = append(roots, admin)
	}
	return roots, nil
}
