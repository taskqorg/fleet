package main

// Project readiness: read-only checks for this host's environments. Each check is ok|failed with a reason; no raw stderr
// or secrets leave the host.

import (
	"errors"
	"fleet/contract"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func check(name, target string, failed string) contract.ReadinessCheck {
	if failed == "" {
		return contract.ReadinessCheck{Check: name, Target: target, Status: contract.CheckStatusOk}
	}
	return contract.ReadinessCheck{Check: name, Target: target, Status: contract.CheckStatusFailed, Reason: &failed}
}

func validRemote(r string) bool {
	if strings.HasPrefix(r, "-") {
		return false
	}
	for _, p := range []string{"https://", "ssh://", "git@"} {
		if strings.HasPrefix(r, p) {
			return true
		}
	}
	return false
}

func anyIn(s string, xs ...string) bool {
	for _, x := range xs {
		if strings.Contains(s, x) {
			return true
		}
	}
	return false
}

// lsRemoteReason classifies git stderr without passing it on.
func lsRemoteReason(code int, stderr string) string {
	e := strings.ToLower(stderr)
	kind := "unclassified"
	switch {
	case anyIn(e, "could not resolve host", "network is unreachable", "connection timed out", "connection refused", "operation timed out", "temporary failure in name resolution"):
		kind = "network-unreachable"
	case anyIn(e, "permission denied", "authentication failed", "could not read username", "host key verification failed", "invalid username or password"):
		kind = "auth-denied"
	case anyIn(e, "repository not found", "does not appear to be a git repository", "not found"):
		kind = "repo-missing"
	case code == 2:
		kind = "head-missing"
	}
	return "ls-remote-" + kind + "-exit-" + itoa(code)
}

// claudePlacement: FLEET_CLAUDE_PLACEMENT is mac or windows-wsl and must match the platform (WSL from the kernel release).
func claudePlacement(setting, goos, release string) (string, error) {
	wsl := goos == "linux" && strings.Contains(strings.ToLower(release), "microsoft")
	switch {
	case setting == "":
		return "", errors.New("claude-placement-unset")
	case setting == "mac" && goos == "darwin":
		return "mac", nil
	case setting == "windows-wsl" && wsl:
		return "windows-wsl", nil
	case setting == "mac" || setting == "windows-wsl":
		return "", errors.New("claude-placement-platform-mismatch")
	}
	return "", errors.New("claude-placement-invalid")
}

func claudePlacementHere() (string, error) {
	r, _ := os.ReadFile("/proc/sys/kernel/osrelease")
	return claudePlacement(os.Getenv("FLEET_CLAUDE_PLACEMENT"), runtime.GOOS, string(r))
}

func readiness(project any, host, registry string) contract.Readiness {
	checks := []contract.ReadinessCheck{}
	repos := jArr(project, "repos")
	for _, env := range jArr(project, "environments") {
		if jStr(env, "hostId") != host {
			continue
		}
		path := jStr(env, "checkoutPath")
		f := ""
		if !exists(filepath.Join(path, ".git")) {
			f = "checkout-missing"
		}
		checks = append(checks, check("checkout", path, f))
		for _, r := range repos {
			if jAt(env, "repo") != nil && !jEq(jAt(env, "repo"), jAt(r, "id")) {
				continue
			}
			remote := jStr(r, "remote")
			access, aerr := gitEnv(project, r, connectionsDir())
			failed := ""
			switch {
			case !validRemote(remote):
				failed = "invalid-remote"
			case aerr != nil:
				failed = aerr.Error()
			default:
				g := cleanCmd("git", "-c", "core.hooksPath=/dev/null", "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always", "ls-remote", "--exit-code", "--", remote, "HEAD")
				gitApply(g, access)
				var se strings.Builder
				g.Stderr = &se
				if err := g.Run(); err != nil {
					var ee *exec.ExitError
					if errors.As(err, &ee) {
						failed = lsRemoteReason(ee.ExitCode(), se.String())
					} else {
						failed = "git-unavailable"
					}
				}
			}
			target := remote
			if jIsStr(r, "id") {
				target = jStr(r, "id")
			}
			checks = append(checks, check("remote", target, failed))
		}
		if s := jStr(project, "scripts", "setup"); jIsStr(project, "scripts", "setup") {
			first := ""
			if fs := strings.Fields(s); len(fs) > 0 {
				first = fs[0]
			}
			f := ""
			if strings.Contains(first, "/") && !exists(filepath.Join(path, first)) {
				f = "setup-script-missing"
			}
			checks = append(checks, check("setup", first, f))
		}
	}
	var harnesses []any
	for _, v := range jObj(project, "roles") {
		harnesses = append(harnesses, v)
	}
	var accounts M
	if registry != "" {
		if b, err := os.ReadFile(registry); err == nil {
			if v, err := dec(string(b)); err == nil {
				accounts, _ = v.(M)
			}
		}
	}
	has := func(h any) bool {
		for _, x := range harnesses {
			if jEq(x, h) {
				return true
			}
		}
		return false
	}
	if len(harnesses) > 0 {
		if accounts == nil {
			checks = append(checks, check("accounts", host, "registry-unavailable"))
		} else {
			for _, name := range sortedKeys(accounts) {
				a := accounts[name]
				if !has(jAt(a, "harness")) {
					continue
				}
				f := ""
				if r, ok := jObj(a)["readiness"]; ok && r != "ready" {
					f = "login-absent"
				}
				checks = append(checks, check("account", name, f))
			}
		}
	}
	if has("claude") {
		var homes []string
		for _, name := range sortedKeys(accounts) {
			a := accounts[name]
			if jStr(a, "harness") == "claude" && jIsStr(a, "nativeHome") {
				homes = append(homes, jStr(a, "nativeHome"))
			}
		}
		pl, perr := claudePlacementHere()
		checks = append(checks, claudeChecks(host, pl, perr, homes)...)
	}
	ok := true
	for _, c := range checks {
		if c.Status != contract.CheckStatusOk {
			ok = false
		}
	}
	return contract.Readiness{HostId: host, Ok: ok, Checks: checks}
}

// claudeChecks: the placement, and on WSL the login (.credentials.json) of each Claude account home; presence only.
func claudeChecks(host, placement string, perr error, homes []string) []contract.ReadinessCheck {
	f := ""
	if perr != nil {
		f = perr.Error()
	}
	out := []contract.ReadinessCheck{check("claude-placement", host, f)}
	if perr == nil && placement == "windows-wsl" {
		if len(homes) == 0 {
			out = append(out, check("claude-login", host, "claude-account-absent"))
		}
		for _, h := range homes {
			f := ""
			if !isFile(filepath.Join(h, ".credentials.json")) {
				f = "claude-login-absent"
			}
			out = append(out, check("claude-login", h, f))
		}
	}
	return out
}
