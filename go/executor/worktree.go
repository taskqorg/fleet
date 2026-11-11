package main

// Worktree lifecycle: roots, base, branch, links, setup, teardown; trial worktrees for lab scripts.

import (
	"errors"
	"fleet/contract"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

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
func repoPath(p any, host string) (string, bool) {
	if jIsStr(p, "workingDirectory") {
		return jStr(p, "workingDirectory"), true
	}
	for _, e := range jArr(p, "project", "environments") {
		if jStr(e, "hostId") == host && jIsStr(e, "checkoutPath") {
			return jStr(e, "checkoutPath"), true
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

func gitFetch(top string, project any) bool {
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
// <prefix>/<session>). The payload path is untrusted: it must resolve under a configured project root and outside the
// executor and session dirs. Returns (worktree, checkout toplevel, created); ok false means no worktree.
func worktree(dir, session, repo string, hasRepo bool, roots, forbidden []string, project any) (w, top string, created, ok bool, err error) {
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
	wt := jObj(project, "worktree")
	prefix := "fleet"
	if jIsStr(wt, "branchPrefix") {
		prefix = jStr(wt, "branchPrefix")
	}
	branch := prefix + "/" + session
	ceil := "GIT_CEILING_DIRECTORIES=" + root
	v := hardenedGit("-C", top, "rev-parse", "--verify", "--quiet", branch)
	v.Env = append(v.Env, ceil)
	args := []string{"-C", top, "worktree", "add"}
	if v.Run() == nil {
		args = append(args, w, branch)
	} else {
		args = append(args, "--no-track", "-b", branch, w)
		// Base: explicit baseRef, else fresh origin/<defaultBranch> when startFromOrigin, else HEAD.
		if jIsStr(wt, "baseRef") {
			args = append(args, jStr(wt, "baseRef"))
		} else if jBoolOr(false, wt, "startFromOrigin") && gitFetch(top, project) {
			def := "main"
			if repos := jArr(project, "repos"); len(repos) > 0 && jIsStr(repos[0], "defaultBranch") {
				def = jStr(repos[0], "defaultBranch")
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
	for _, k := range []string{"linkPaths", "sharedDirectories"} {
		for _, rv := range jArr(wt, k) {
			rel, isS := rv.(string)
			if !isS || strings.HasPrefix(rel, "/") || contains(strings.Split(rel, "/"), "..") {
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
	if hardenedGit("-C", paths[0], "worktree", "add", "--detach", w, commit).Run() != nil {
		return "", "", errors.New("git worktree add failed (commit missing?)")
	}
	return w, paths[0], nil
}

func removeTrial(top, w string) bool {
	return hardenedGit("-C", top, "worktree", "remove", "--force", w).Run() == nil
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
func keepReasons(project any) map[string]bool {
	keep := map[string]bool{}
	list := jArr(project, "cleanup", "keepWhen")
	if list == nil {
		list = []any{"uncommitted", "unpushed", "teardown-failed"}
	}
	for _, v := range list {
		if s, ok := v.(string); ok {
			keep[s] = true
		}
	}
	return keep
}

// teardown: teardown script, then remove the worktree unless a cleanup.keepWhen reason holds (uncommitted changes,
// commits on no remote branch, a failed teardown script). The branch goes with -d (merged only), or -D
// when the work was delivered (deleteBranch: the task was accepted).
func teardown(dir, session, top string, project any, deleteBranch bool) contract.Teardown {
	keep := keepReasons(project)
	w := filepath.Join(dir, "worktree")
	if !exists(filepath.Join(w, ".git")) {
		return refusedTeardown("no-worktree")
	}
	out := contract.Teardown{}
	if jIsStr(project, "scripts", "teardown") {
		t := projectScript("teardown", jStr(project, "scripts", "teardown"), top, w, session)
		out.Teardown = &t
		if !t.Ok && keep["teardown-failed"] {
			out.Reason = new("teardown-failed")
			return out
		}
	}
	// Fleet-made links are excluded from the dirty check and removed before `worktree remove`.
	var links []string
	for _, k := range []string{"linkPaths", "sharedDirectories"} {
		for _, v := range jArr(project, "worktree", k) {
			if r, ok := v.(string); ok {
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
		// Commits on no remote-tracking branch (session branches are created --no-track, so there is no @{u} to ask).
		ahead, err := hardenedGit("-C", w, "rev-list", "-n", "1", "HEAD", "--not", "--remotes").Output()
		if err != nil || len(strings.TrimSpace(string(ahead))) > 0 {
			out.Reason = new("unpushed")
			return out
		}
	}
	for _, r := range links {
		os.Remove(filepath.Join(w, r))
	}
	removed := hardenedGit("-C", top, "worktree", "remove", w).Run() == nil
	out.Removed = removed
	if !removed {
		out.Reason = new("worktree-remove-failed")
		return out
	}
	prefix := "fleet"
	if jIsStr(project, "worktree", "branchPrefix") {
		prefix = jStr(project, "worktree", "branchPrefix")
	}
	flag := "-d"
	if deleteBranch {
		flag = "-D"
	}
	deleted := hardenedGit("-C", top, "branch", flag, prefix+"/"+session).Run() == nil
	out.BranchDeleted = &deleted
	return out
}

// refusedTeardown: a teardown that removed nothing, with its reason.
func refusedTeardown(reason string) contract.Teardown {
	return contract.Teardown{Removed: false, Reason: &reason}
}
