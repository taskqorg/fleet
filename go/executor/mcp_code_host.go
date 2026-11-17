package main

// Code mode host functions (docs/designs/fleet-code-mode.md, host functions): the code server's side of sh, write and
// edit, each checked against the execution's grant. read, glob and grep run in the runtime (code_runtime.js).

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
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
	if s.closed || rt != nil && (rt.stopped || rt.gone) {
		s.mu.Unlock()
		return nil, callError("error", "job-ended", "the runtime was reset or the code server is stopping")
	}
	tmp, err := s.shellTmp()
	s.mu.Unlock()
	if err != nil {
		return nil, callError("error", "sandbox", err.Error())
	}
	policy := jAt(s.spec, "shell")
	argv, err := shellArgv(policy, jStr(s.spec, "sandbox"), cwd, tmp, command)
	if err != nil {
		return nil, callError("error", "sandbox", err.Error())
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if jAt(policy, "sandbox") == true {
		filter, err := shellSocketFilter()
		if err != nil {
			return nil, callError("error", "sandbox", err.Error())
		}
		if filter != nil {
			defer filter.Close()
			cmd.ExtraFiles = []*os.File{filter}
			cmd.Args = slices.Insert(cmd.Args, 1, "--seccomp", "3")
		}
	}
	cmd.Dir = cwd
	cmd.Env = shellEnv(jAt(s.spec, "env"), jAt(s.spec, "shell"))
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// The group is killed when the command exits. ponytail: 1 s for a child that left the group (setsid) and keeps the
	// output open; then the output is closed and the call returns.
	cmd.WaitDelay = time.Second
	s.mu.Lock()
	if s.closed || rt != nil && (rt.stopped || rt.gone) { // a reset or a dead runtime: nobody gets the result
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
	if ms, ok := num(jAt(args, "timeoutMs")); ok && ms > 0 && ms < 1e12 { // beyond 30 years: no timeout, not an overflow
		t := time.AfterFunc(time.Duration(ms*float64(time.Millisecond)), p.timeout)
		defer t.Stop()
	}
	// Until cmd.Wait reaps it, the exited leader stays a zombie, so its PID and process group id cannot be reused: what
	// the command left running in its group is killed before then, never another command's group. Without waitid the
	// group is not killed: a kill after the reap could hit another group.
	if waitExited(cmd.Process.Pid) {
		p.mu.Lock()
		p.exited = true // a timeout from here on comes too late: the command had ended
		p.mu.Unlock()
		p.kill()
		p.reap()
	} else {
		// No waitid: Wait blocks while the command runs, so a timeout, reset or shutdown can still kill it, through the
		// leader only (os.Process refuses a reaped one), never a group id that may be reused.
		p.mu.Lock()
		p.leaderOnly = true
		p.mu.Unlock()
		p.cmd.Wait()
		p.mu.Lock()
		p.exited, p.reaped = true, true
		p.mu.Unlock()
	}
	s.mu.Lock()
	delete(s.procs, p)
	s.mu.Unlock()
	r := M{"code": nil, "stdout": stdout.String(), "stderr": stderr.String()}
	if st, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && st.Signaled() {
		r["signal"] = st.Signal().String()
		p.mu.Lock()
		if p.timedOut { // a command that ended by itself just before the timer is no timeout
			r["timedOut"] = true
		}
		p.mu.Unlock()
	} else {
		r["code"] = cmd.ProcessState.ExitCode()
	}
	return r, nil
}

// shProc is one running sh command. Its group is killed only while the leader is unreaped.
type shProc struct {
	cmd                      *exec.Cmd
	mu                       sync.Mutex
	exited, reaped, timedOut bool
	leaderOnly               bool
}

// timeout kills a command whose leader still runs and marks it timed out, in one step with that check.
func (p *shProc) timeout() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.exited && !p.reaped {
		p.timedOut = true
		p.signal()
	}
}

// signal kills the group, or only the leader when the group id could be reused (no waitid). The caller holds p.mu.
func (p *shProc) signal() {
	if p.leaderOnly {
		p.cmd.Process.Kill()
	} else {
		syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
	}
}

// kill kills the command's process group unless the leader was already reaped.
func (p *shProc) kill() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.reaped {
		p.signal()
	}
}

func (p *shProc) reap() {
	p.mu.Lock()
	p.reaped = true // set before the reap: a kill after this could hit a reused process group id
	p.mu.Unlock()
	p.cmd.Wait()
}

// shellEnv is a command's environment: the code server's, without the run token, gateway URL, Access file and MCP owner
// (FLEET_RUN_TOKEN, FLEET_GATEWAY_URL, FLEET_CF_ACCESS_FILE, FLEET_MCP_*), plus the execution's own variables (extra:
// FLEET_INPUT_DIR, FLEET_OUTPUT_DIR), USER and LOGNAME as the harness has them, and TMPDIR=/tmp in a Linux sandbox,
// whose /tmp is private.
func shellEnv(extra, policy any) []string {
	linuxSandbox := runtime.GOOS == "linux" && jAt(policy, "sandbox") == true
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return k == "FLEET_RUN_TOKEN" || k == "FLEET_GATEWAY_URL" || k == "FLEET_CF_ACCESS_FILE" || strings.HasPrefix(k, "FLEET_MCP_") ||
			linuxSandbox && k == "TMPDIR"
	})
	if linuxSandbox {
		env = append(env, "TMPDIR=/tmp")
	}
	if u, err := user.Current(); err == nil {
		for _, k := range []string{"USER", "LOGNAME"} {
			if os.Getenv(k) == "" {
				env = append(env, k+"="+u.Username)
			}
		}
	}
	for _, k := range sortedKeys(jObj(extra)) {
		if v := jStr(extra, k); strings.HasPrefix(k, "FLEET_") && v != "" {
			env = append(env, k+"="+v)
		}
	}
	return env
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
func darwinShellProfile(roots, protected, hidden, visible []string, network bool) string {
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
	for _, h := range hidden {
		b.WriteString(`(deny file-read* (subpath "` + q(h) + `"))`)
	}
	if len(hidden) > 0 && len(visible) > 0 {
		b.WriteString("(allow file-read*")
		for _, v := range visible {
			b.WriteString(` (subpath "` + q(v) + `")`)
		}
		b.WriteString(")")
	}
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
	// hide: the executor's data directory (run files with run tokens, MCP sockets), unreadable but for the read roots, the
	// write roots and a HOME inside it (a harness home: its Git configuration).
	hide, read := paths(jArr(policy, "hide")), paths(jArr(policy, "read"))
	if home, err := canon(os.Getenv("HOME")); err == nil && os.Getenv("HOME") != "" && slices.ContainsFunc(hide, func(h string) bool { return under(home, h) }) {
		read = append(read, home)
	}
	var roots []string // none for a read-only start, not even the temporary directories
	if !readOnly {
		roots = paths(jArr(policy, "write"))
		if runtime.GOOS == "darwin" { // on Linux /tmp is private and TMPDIR points there
			for _, r := range paths([]any{os.Getenv("TMPDIR"), "/private/tmp"}) {
				if !slices.Contains(roots, r) {
					roots = append(roots, r)
				}
			}
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
		// The executor's data directory (run files with run tokens, MCP sockets) is unreadable except the read and write
		// roots in it, as on Linux.
		return append([]string{sandbox, "-p", darwinShellProfile(roots, protected, hide, append(read, roots...), network)}, run...), nil
	case "linux":
		argv := []string{sandbox, "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc"}
		// Hide host runtime files and executor secrets. shellSocketFilter blocks Unix sockets independently of their
		// filesystem location or the network namespace.
		hidden := []string{"/run"}
		if st, err := os.Lstat("/var/run"); err == nil && st.Mode()&os.ModeSymlink == 0 {
			hidden = append(hidden, "/var/run")
		}
		if x := os.Getenv("XDG_RUNTIME_DIR"); x != "" && !under(x, "/run") {
			if st, err := os.Stat(x); err == nil && st.IsDir() {
				hidden = append(hidden, x)
			}
		}
		hidden = append(hidden, hide...)
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
		for _, r := range read { // read roots under a hidden directory stay readable
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

// codeFileMu makes each write and edit one step: an edit's read and write never interleave with another's.
var codeFileMu sync.Mutex

// resolveIn follows the symlinks of rel inside the worktree real, as the native Write tool does: the result has none
// left, or the path leaves the worktree (an error).
func resolveIn(real, rel string) (string, error) {
	for hops := 0; ; {
		parts := strings.Split(rel, "/")
		changed := false
		for i := range parts {
			at := filepath.Join(append([]string{real}, parts[:i+1]...)...)
			st, err := os.Lstat(at)
			if err != nil {
				break // not there yet: write creates it
			}
			if st.Mode()&os.ModeSymlink == 0 {
				continue
			}
			if hops++; hops > 40 {
				return "", errors.New(rel + ": too many symlinks")
			}
			target, err := os.Readlink(at)
			if err != nil {
				return "", err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(at), target)
			}
			if c, err := canon(filepath.Dir(target)); err == nil { // the real spelling (/var is /private/var on macOS)
				target = filepath.Join(c, filepath.Base(target))
			}
			next, err := worktreePath(real, filepath.Join(append([]string{target}, parts[i+1:]...)...))
			if err != nil {
				return "", err
			}
			rel, changed = next, true
			break
		}
		if !changed {
			return rel, nil
		}
	}
}

// codeFile serves write and edit inside the worktree dir. It resolves the path's symlinks inside the worktree, then
// walks it by directory handle (os.Root), checking that no component became a symlink meanwhile and comparing each
// directory, and the file, with the worktree's .git by file identity: no symlink, other spelling (macOS ignores case)
// or concurrent rename reaches .git. The file itself is opened without following a symlink.
func codeFile(dir, tool string, args any) (any, M) {
	codeFileMu.Lock()
	defer codeFileMu.Unlock()
	real, err := canon(dir)
	if err != nil {
		return nil, callError("error", "io", err.Error())
	}
	rel, err := worktreePath(dir, jStr(args, "path"))
	if err == nil {
		rel, err = resolveIn(real, rel)
	}
	if err != nil {
		return nil, callError("error", "outside-worktree", err.Error())
	}
	old := jStr(args, "old")
	if tool == "edit" && old == "" {
		return nil, callError("error", "bad-arguments", "old required")
	}
	fail := func(err error) (any, M) {
		if strings.Contains(err.Error(), "path escapes") {
			return nil, callError("error", "outside-worktree", err.Error())
		}
		return nil, callError("error", "io", err.Error())
	}
	cur, err := os.OpenRoot(real)
	if err != nil {
		return fail(err)
	}
	defer func() { cur.Close() }()
	git, gitErr := cur.Lstat(".git")
	isGit := func(st os.FileInfo) bool { return gitErr == nil && os.SameFile(st, git) }
	if d := filepath.Dir(rel); d != "." {
		for _, c := range strings.Split(d, "/") {
			if tool == "write" {
				if err := cur.Mkdir(c, 0o755); err != nil && !errors.Is(err, os.ErrExist) {
					return fail(err)
				}
			}
			next, err := cur.OpenRoot(c)
			if err != nil {
				return fail(err)
			}
			opened, err := next.Stat(".")
			link, lerr := cur.Lstat(c) // after the open: a component swapped for a symlink in between does not match
			cur.Close()
			cur = next
			if err != nil || lerr != nil || link.Mode()&os.ModeSymlink != 0 || !os.SameFile(opened, link) {
				return nil, callError("error", "outside-worktree", rel+" changed while it was written")
			}
			if isGit(opened) {
				return nil, callError("error", "outside-worktree", rel+" is under .git")
			}
		}
	}
	// The new contents go to a temporary file renamed over the name: readers never see a half-written file, a failed
	// write keeps the old contents, and a hard link (to a file in .git, say) keeps its contents.
	name := filepath.Base(rel)
	mode := os.FileMode(0o644)
	var b []byte
	// Open without waiting on a FIFO. Only regular files pass the check below.
	if f, err := cur.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0); err == nil {
		st, err := f.Stat()
		if err == nil && isGit(st) {
			err = errors.New(rel + " is .git")
		}
		if err == nil && !st.Mode().IsRegular() {
			err = errors.New(rel + " is not a regular file")
		}
		if err == nil {
			mode = st.Mode().Perm()
			if tool == "edit" {
				b, err = io.ReadAll(f)
			}
		}
		f.Close()
		if err != nil {
			return fail(err)
		}
	} else if tool == "edit" || !errors.Is(err, os.ErrNotExist) {
		return fail(err)
	}
	data := []byte(jStr(args, "text"))
	replaced := 0
	if tool == "edit" {
		replaced = strings.Count(string(b), old)
		switch {
		case replaced == 0:
			return nil, callError("error", "not-found", "old does not occur in "+rel)
		case replaced > 1 && jAt(args, "all") != true:
			return nil, callError("error", "not-unique", fmt.Sprintf("old occurs %d times in %s: pass {all: true} or a longer old", replaced, rel))
		}
		data = []byte(strings.ReplaceAll(string(b), old, jStr(args, "new")))
	}
	tmp := fmt.Sprintf(".fleet-%d.tmp", time.Now().UnixNano()) // short: any legal name stays legal beside it
	f, err := cur.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, mode)
	if err != nil {
		return fail(err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Chmod(mode) // the umask narrowed the create
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = cur.Rename(tmp, name)
	}
	if err != nil {
		cur.Remove(tmp)
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
			// Any .git, a nested repository's (submodule) too, in any case: macOS file systems ignore it.
			if slices.ContainsFunc(strings.Split(rel, "/"), func(c string) bool { return strings.EqualFold(c, ".git") }) {
				return "", errors.New(rel + " is under .git")
			}
			return rel, nil
		}
	}
	return "", errors.New(p + " is outside the working directory")
}
