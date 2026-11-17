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
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// sh runs one command for the code: bash -c in the worktree or cwd, under the execution's shell policy, with the code
// server's environment without FLEET_* (no run token, gateway URL or Access file). It returns the whole stdout and
// stderr; a non-zero exit is a result, not a failure. rt is the runtime that sent the call: once a reset stopped it, no
// new command starts for it.
func (s *codeServer) sh(rt *codeRuntime, args any) (any, M) {
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
	s.mu.Lock()
	if s.closed || rt != nil && rt.stopped {
		s.mu.Unlock()
		return nil, callError("error", "job-ended", "the runtime was reset or the code server is stopping")
	}
	tmp, err := s.shellTmp()
	s.mu.Unlock()
	if err != nil {
		return nil, callError("error", "sandbox", err.Error())
	}
	argv, err := shellArgv(jAt(s.spec, "shell"), jStr(s.spec, "sandbox"), cwd, tmp, command)
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
	if s.closed || rt != nil && rt.stopped { // a reset between the check above and here
		s.mu.Unlock()
		return nil, callError("error", "job-ended", "the runtime was reset or the code server is stopping")
	}
	if err := cmd.Start(); err != nil {
		s.mu.Unlock()
		return nil, callError("error", "sh-start", err.Error())
	}
	p := &shProc{cmd: cmd}
	s.procs[p] = true
	s.mu.Unlock()
	timedOut := false
	if ms, ok := num(jAt(args, "timeoutMs")); ok && ms > 0 {
		t := time.AfterFunc(time.Duration(ms*float64(time.Millisecond)), func() {
			if p.kill() {
				s.mu.Lock()
				timedOut = true
				s.mu.Unlock()
			}
		})
		defer t.Stop()
	}
	// Until cmd.Wait reaps it, the exited leader stays a zombie, so its PID and process group id cannot be reused: what
	// the command left running in its group is killed before then, never another command's group.
	waitExited(cmd.Process.Pid)
	p.kill()
	p.reap()
	s.mu.Lock()
	delete(s.procs, p)
	r := M{"code": nil, "stdout": stdout.String(), "stderr": stderr.String()}
	if timedOut {
		r["timedOut"] = true
	}
	s.mu.Unlock()
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && st.Signaled() {
		r["signal"] = st.Signal().String()
	} else {
		r["code"] = cmd.ProcessState.ExitCode()
	}
	return r, nil
}

// shProc is one running sh command. Its group is killed only while the leader is unreaped.
type shProc struct {
	cmd    *exec.Cmd
	mu     sync.Mutex
	reaped bool
}

// kill kills the command's process group unless the leader was already reaped; it reports whether it did.
func (p *shProc) kill() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.reaped {
		return false
	}
	syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	return true
}

func (p *shProc) reap() {
	p.mu.Lock()
	p.reaped = true // set before the reap: a kill after this could hit a reused process group id
	p.mu.Unlock()
	p.cmd.Wait()
}

// shellTmp is the code server's private /tmp for Linux sh commands, made on first use and kept for the execution, so
// one command's /tmp files reach the next; nil elsewhere. The caller holds s.mu.
func (s *codeServer) shellTmp() (string, error) {
	if runtime.GOOS != "linux" || jAt(s.spec, "shell", "sandbox") != true || jAt(s.spec, "shell", "readOnly") == true {
		return "", nil
	}
	if s.tmp == "" {
		d, err := os.MkdirTemp("", "fleet-code-tmp-")
		if err != nil {
			return "", err
		}
		s.tmp = d
	}
	return s.tmp, nil
}

// darwinShellProfile is the macOS sandbox for sh, deny-default as Codex's seatbelt policy: processes, reads, writes
// only under the write roots, the system services ordinary commands need (user lookup, notifications, logging), no
// other Mach service (so no launchd job or app runs outside the sandbox for it), signals only inside the sandbox, and
// network (IP and the DNS socket, no other Unix socket) only when the policy has it.
func darwinShellProfile(roots, protected []string, network bool) string {
	q := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace
	var b strings.Builder
	b.WriteString("(version 1)(deny default)(allow process-exec)(allow process-fork)(allow signal (target same-sandbox))" +
		"(allow process-info* (target same-sandbox))(allow file-read*)(allow sysctl-read)(allow user-preference-read)" +
		"(allow ipc-posix-sem)(allow ipc-posix-shm-read-data ipc-posix-shm-read-metadata)(allow pseudo-tty)" +
		`(allow iokit-open (iokit-registry-entry-class "RootDomainUserClient"))` +
		`(allow file-write-data file-ioctl (literal "/dev/null") (literal "/dev/zero") (literal "/dev/dtracehelper") (regex #"^/dev/tty") (regex #"^/dev/fd/"))` +
		`(allow file-read* file-write* file-ioctl (literal "/dev/ptmx") (regex #"^/dev/ttys[0-9]+"))` +
		`(allow mach-lookup (global-name "com.apple.system.opendirectoryd.libinfo") (global-name "com.apple.system.opendirectoryd.membership")` +
		` (global-name "com.apple.system.notification_center") (global-name "com.apple.logd") (global-name "com.apple.system.logger")` +
		` (global-name "com.apple.sysmond") (global-name "com.apple.bsd.dirhelper") (global-name "com.apple.securityd.xpc"))`)
	if len(roots) > 0 {
		b.WriteString("(allow file-write*")
		for _, r := range roots {
			b.WriteString(` (subpath "` + q(r) + `")`)
		}
		b.WriteString(")")
	}
	for _, g := range protected { // in any case: macOS file systems ignore it, so .GIT is the same directory
		b.WriteString(`(deny file-write* (subpath "` + q(g) + `") (regex #"^` + strings.ReplaceAll(regexp.QuoteMeta(filepath.Dir(g)), `"`, `.`) + `/\.[Gg][Ii][Tt](/|$)"))`)
	}
	if network {
		b.WriteString(`(allow network-outbound (remote ip) (literal "/private/var/run/mDNSResponder"))(allow network-inbound (local ip))(allow system-socket)` +
			`(allow mach-lookup (global-name "com.apple.dnssd.service") (global-name "com.apple.SystemConfiguration.configd")` +
			` (global-name "com.apple.SystemConfiguration.DNSConfiguration") (global-name "com.apple.networkd") (global-name "com.apple.trustd")` +
			` (global-name "com.apple.trustd.agent") (global-name "com.apple.SecurityServer") (global-name "com.apple.ocspd"))`)
	}
	return b.String()
}

// shellArgv is the sh command: bash -c, unsandboxed for a harness whose own shell has no sandbox (Claude), else in an OS
// sandbox that reads files, writes only the policy's write roots and the temporary directories (nothing for a read-only
// start), signals only inside itself, reaches no host Unix socket, and has network only when the policy says so. A write
// root's .git stays read-only unless another write root lies inside it, as in Codex. tmp is the private /tmp on Linux.
func shellArgv(policy any, sandbox, cwd, tmp, command string) ([]string, error) {
	bash := "/bin/bash"
	if !isFile(bash) {
		bash = "/bin/sh"
	}
	run := []string{bash, "-c", command}
	if jAt(policy, "sandbox") != true {
		return run, nil
	}
	network, readOnly := jAt(policy, "network") == true, jAt(policy, "readOnly") == true
	paths := func(list []any) []string {
		var out []string
		for _, r := range list {
			if p, ok := str(r); ok && p != "" {
				if c, err := canon(p); err == nil && !slices.Contains(out, c) {
					out = append(out, c)
				}
			}
		}
		return out
	}
	var roots []string // none for a read-only start, not even the temporary directories
	if !readOnly {
		roots = paths(append(jArr(policy, "write"), os.Getenv("TMPDIR")))
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
		return append([]string{sandbox, "-p", darwinShellProfile(roots, protected, network)}, run...), nil
	case "linux":
		argv := []string{sandbox, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
		// Host Unix sockets live under /run (/var/run, XDG_RUNTIME_DIR, docker.sock, D-Bus), /tmp and the executor's data
		// directory (its MCP sockets): each gets an empty tmpfs; abstract sockets stay in the host network namespace,
		// which --unshare-net leaves.
		hidden := []string{"/run"}
		if st, err := os.Lstat("/var/run"); err == nil && st.Mode()&os.ModeSymlink == 0 {
			hidden = append(hidden, "/var/run")
		}
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" && !under(x, "/run") {
			if st, err := os.Stat(x); err == nil && st.IsDir() {
				hidden = append(hidden, x)
			}
		}
		hidden = append(hidden, paths(jArr(policy, "hide"))...)
		for _, h := range hidden {
			argv = append(argv, "--tmpfs", h)
		}
		if tmp != "" {
			argv = append(argv, "--bind", tmp, "/tmp")
		} else {
			argv = append(argv, "--tmpfs", "/tmp")
		}
		if network { // the resolver file may live under /run (systemd-resolved)
			if r, err := filepath.EvalSymlinks("/etc/resolv.conf"); err == nil && under(r, "/run") {
				argv = append(argv, "--ro-bind", r, r)
			}
		}
		for _, r := range paths(jArr(policy, "read")) { // read roots under a hidden directory stay readable
			argv = append(argv, "--ro-bind", r, r)
		}
		for _, r := range roots {
			argv = append(argv, "--bind", r, r)
		}
		for _, g := range protected {
			argv = append(argv, "--ro-bind", g, g)
		}
		if readOnly {
			argv = append(argv, "--remount-ro", "/tmp")
		}
		argv = append(argv, "--unshare-ipc", "--unshare-pid", "--die-with-parent", "--new-session")
		if !network {
			argv = append(argv, "--unshare-net")
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
			if strings.EqualFold(strings.Split(rel, "/")[0], ".git") { // any case: macOS file systems ignore it
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
	git, err := os.Stat(filepath.Join(real, ".git"))
	if err != nil {
		return nil // no .git in the worktree
	}
	p := filepath.Join(real, rel)
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			// By file identity, not by name: another spelling (macOS ignores case) resolves to the same directory.
			for a := r; ; a = filepath.Dir(a) {
				if st, err := os.Stat(a); err == nil && os.SameFile(st, git) {
					return errors.New(rel + " resolves under .git")
				}
				if a == filepath.Dir(a) {
					return nil
				}
			}
		}
		if p == real {
			return nil
		}
		p = filepath.Dir(p)
	}
}
