package main

// Code mode host functions (docs/designs/fleet-code-mode.md, host functions): the code server's side of sh, write and
// edit, each checked against the execution's grant. read, glob and grep run in the runtime (code_runtime.js).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

// sh runs one command for the code: bash -c in the worktree or cwd, under the execution's shell policy, with the code
// server's environment without FLEET_* (no run token, gateway URL or Access file). It returns the whole stdout and
// stderr; a non-zero exit is a result, not a failure.
func (s *codeServer) sh(args any) (any, M) {
	command := jStr(args, "command")
	if strings.TrimSpace(command) == "" {
		return nil, callError("error", "bad-arguments", "command required")
	}
	dir := jStr(s.spec, "dir")
	cwd := jStr(args, "cwd")
	if cwd == "" {
		cwd = dir
	} else if !filepath.IsAbs(cwd) {
		cwd = filepath.Join(dir, cwd)
	}
	argv, err := shellArgv(jAt(s.spec, "shell"), jStr(s.spec, "sandbox"), cwd, command)
	if err != nil {
		return nil, callError("error", "sandbox", err.Error())
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, "FLEET_") })
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// ponytail: 1 s for a background child that keeps the output open after the command exits; then its group is killed.
	cmd.WaitDelay = time.Second
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, callError("error", "job-ended", "the code server is stopping")
	}
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return nil, callError("error", "sh-start", err.Error())
	}
	s.procs[cmd] = true
	s.mu.Unlock()
	var timedOut atomic.Bool
	if ms, ok := num(jAt(args, "timeoutMs")); ok && ms > 0 {
		t := time.AfterFunc(time.Duration(ms*float64(time.Millisecond)), func() { timedOut.Store(true); killGroup(cmd) })
		defer t.Stop()
	}
	cmd.Wait()
	s.mu.Lock()
	delete(s.procs, cmd)
	killGroup(cmd) // what the command left running in its process group
	s.mu.Unlock()
	r := M{"code": nil, "stdout": stdout.String(), "stderr": stderr.String()}
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && st.Signaled() {
		r["signal"] = st.Signal().String()
	} else {
		r["code"] = cmd.ProcessState.ExitCode()
	}
	if timedOut.Load() {
		r["timedOut"] = true
	}
	return r, nil
}

// killGroup kills a started command's process group.
func killGroup(c *exec.Cmd) {
	if c.Process != nil {
		syscall.Kill(-c.Process.Pid, syscall.SIGKILL)
	}
}

// shellArgv is the sh command: bash -c, unsandboxed for a harness whose own shell has no sandbox (Claude), else in an OS
// sandbox that reads everything, writes only the policy's write roots and the temporary directories, signals only inside
// itself, and has network only when the policy says so. A write root's .git stays read-only unless another write root
// lies inside it, as in Codex.
func shellArgv(policy any, sandbox, cwd, command string) ([]string, error) {
	bash := "/bin/bash"
	if !isFile(bash) {
		bash = "/bin/sh"
	}
	run := []string{bash, "-c", command}
	if jAt(policy, "sandbox") != true {
		return run, nil
	}
	network := jAt(policy, "network") == true
	var roots []string // none for a read-only start, not even the temporary directories
	if jAt(policy, "readOnly") != true {
		for _, r := range append(jArr(policy, "write"), os.Getenv("TMPDIR")) {
			if p, ok := str(r); ok && p != "" {
				if c, err := canon(p); err == nil && !slices.Contains(roots, c) {
					roots = append(roots, c)
				}
			}
		}
		if runtime.GOOS == "darwin" {
			roots = append(roots, "/private/tmp")
		}
	}
	var protected []string
	for _, r := range roots {
		g := filepath.Join(r, ".git")
		if _, err := os.Lstat(g); err == nil && !slices.ContainsFunc(roots, func(o string) bool { return under(o, g) }) {
			protected = append(protected, g)
		}
	}
	switch runtime.GOOS {
	case "darwin":
		q := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
		p := "(version 1)(allow default)"
		if !network {
			p += "(deny network*)"
		}
		p += `(deny file-write*)(allow file-write* (literal "/dev/null") (literal "/dev/zero") (regex #"^/dev/tty") (regex #"^/dev/fd/")`
		for _, r := range roots {
			p += ` (subpath "` + q(r) + `")`
		}
		p += ")"
		for _, g := range protected {
			p += `(deny file-write* (subpath "` + q(g) + `"))`
		}
		p += "(deny signal)(allow signal (target same-sandbox))"
		return append([]string{sandbox, "-p", p}, run...), nil
	case "linux":
		argv := []string{sandbox, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--unshare-ipc", "--unshare-pid", "--die-with-parent", "--new-session"}
		if jAt(policy, "readOnly") != true {
			argv = append(argv, "--tmpfs", "/tmp") // its own writable /tmp, before the binds: one may be under /tmp
		}
		if !network {
			argv = append(argv, "--unshare-net")
		}
		for _, r := range roots {
			argv = append(argv, "--bind", r, r)
		}
		for _, g := range protected {
			argv = append(argv, "--ro-bind", g, g)
		}
		return append(append(argv, "--chdir", cwd), run...), nil
	}
	return nil, errors.New("no shell sandbox on " + runtime.GOOS)
}

// codeFile serves write and edit inside the worktree dir. os.Root keeps the path, symlinks included, inside it; a path
// that resolves under the worktree's .git is refused as well.
func codeFile(dir, tool string, args any) (any, M) {
	rel, err := worktreePath(dir, jStr(args, "path"))
	if err == nil {
		err = underGitDir(dir, rel)
	}
	if err != nil {
		return nil, callError("error", "outside-worktree", err.Error())
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, callError("error", "io", err.Error())
	}
	defer root.Close()
	fail := func(err error) (any, M) {
		if strings.Contains(err.Error(), "path escapes") {
			return nil, callError("error", "outside-worktree", err.Error())
		}
		return nil, callError("error", "io", err.Error())
	}
	var data []byte
	replaced := 0
	switch tool {
	case "write":
		data = []byte(jStr(args, "text"))
		if d := filepath.Dir(rel); d != "." {
			if err := root.MkdirAll(d, 0o755); err != nil {
				return fail(err)
			}
		}
	case "edit":
		old := jStr(args, "old")
		if old == "" {
			return nil, callError("error", "bad-arguments", "old required")
		}
		b, err := root.ReadFile(rel)
		if err != nil {
			return fail(err)
		}
		replaced = strings.Count(string(b), old)
		switch {
		case replaced == 0:
			return nil, callError("error", "not-found", "old does not occur in "+rel)
		case replaced > 1 && jAt(args, "all") != true:
			return nil, callError("error", "not-unique", fmt.Sprintf("old occurs %d times in %s: pass {all: true} or a longer old", replaced, rel))
		}
		data = []byte(strings.ReplaceAll(string(b), old, jStr(args, "new")))
	}
	if err := root.WriteFile(rel, data, 0o644); err != nil {
		return fail(err)
	}
	if tool == "edit" {
		return M{"path": rel, "replaced": replaced}, nil
	}
	return M{"path": rel, "bytes": len(data)}, nil
}

// worktreePath is p relative to the worktree dir; a path outside it, the worktree itself or one under .git is refused.
func worktreePath(dir, p string) (string, error) {
	if p == "" {
		return "", errors.New("path required")
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(dir, p)
	}
	p = filepath.Clean(p)
	bases := []string{filepath.Clean(dir)}
	if c, err := canon(dir); err == nil {
		bases = append(bases, c)
	}
	for _, b := range bases {
		if rel, err := filepath.Rel(b, p); err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, "../") {
			if strings.Split(rel, "/")[0] == ".git" {
				return "", errors.New(rel + " is under .git")
			}
			return rel, nil
		}
	}
	return "", errors.New(p + " is outside the working directory")
}

// underGitDir refuses a path whose existing part resolves, through a symlink inside the worktree, under its .git.
func underGitDir(dir, rel string) error {
	real, err := canon(dir)
	if err != nil {
		return err
	}
	p := filepath.Join(real, rel)
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			if under(r, filepath.Join(real, ".git")) {
				return errors.New(rel + " resolves under .git")
			}
			return nil
		}
		if p == real {
			return nil
		}
		p = filepath.Dir(p)
	}
}
