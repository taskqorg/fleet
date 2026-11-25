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
	"slices"
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
	case anyIn(e, "could not be found or you don't have permission", "could not be found or you do not have permission"):
		// GitLab's one answer for a missing project and for a key not added to it: the two cannot be told apart.
		kind = "repo-missing-or-denied"
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

// accountView: the readiness fields of one FLEET_NATIVE_ACCOUNTS entry; a field of another type reads as absent and an
// entry that is not an object has none.
type accountView struct {
	Harness    jsonValue `json:"harness"`
	Readiness  jsonValue `json:"readiness"`
	Home       string    `json:"home"`
	NativeHome jsonValue `json:"nativeHome"`
}

func readiness(payload any, host, registry string, forbidden ...[]string) contract.Readiness {
	pp, err := contractOf[contract.ProjectPayload](payload)
	project, perr := projectContext(pp.Project)
	if err != nil || perr != nil {
		reason := "invalid project payload"
		if perr != nil {
			reason = perr.Error()
		}
		return contract.Readiness{HostId: host, Ok: false, Checks: []contract.ReadinessCheck{check("project", host, reason)}}
	}
	checks := []contract.ReadinessCheck{}
	var denied []string
	if len(forbidden) > 0 {
		denied = forbidden[0]
	}
	if _, err := resolveAttemptContext(project, host, projectRoots(), denied, ""); err != nil {
		return contract.Readiness{HostId: host, Ok: false, Checks: []contract.ReadinessCheck{check("instruction-sources", host, err.Error())}}
	}
	if project.ContextSources != nil {
		checks = append(checks, check("instruction-sources", host, ""))
	}
	for _, env := range project.Environments {
		if str0(env.HostId) != host {
			continue
		}
		path := str0(env.CheckoutPath)
		f := ""
		if !exists(filepath.Join(path, ".git")) {
			f = "checkout-missing"
		}
		checks = append(checks, check("checkout", path, f))
		for i := range project.Repos {
			r := &project.Repos[i]
			if env.Repo != nil && (r.Id == nil || *r.Id != *env.Repo) {
				continue
			}
			remote := str0(r.Remote)
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
			if r.Id != nil {
				target = *r.Id
			}
			checks = append(checks, check("remote", target, failed))
		}
		if project.Scripts != nil && project.Scripts.Setup != nil {
			first := ""
			if fs := strings.Fields(*project.Scripts.Setup); len(fs) > 0 {
				first = fs[0]
			}
			f := ""
			if strings.Contains(first, "/") && !exists(filepath.Join(path, first)) {
				f = "setup-script-missing"
			}
			checks = append(checks, check("setup", first, f))
		}
	}
	var harnesses []string
	for _, v := range project.Roles {
		harnesses = append(harnesses, v)
	}
	var accounts map[string]accountView
	if registry != "" {
		if b, err := os.ReadFile(registry); err == nil {
			if all, err := dec(string(b)); err == nil {
				if entries, ok := all.(M); ok {
					accounts = map[string]accountView{}
					for name, raw := range entries {
						accounts[name] = typed[accountView](raw)
					}
				}
			}
		}
	}
	has := func(h string, isStr bool) bool { return isStr && slices.Contains(harnesses, h) }
	if len(harnesses) > 0 {
		if accounts == nil {
			checks = append(checks, check("accounts", host, "registry-unavailable"))
		} else {
			for _, name := range sortedKeysOf(accounts) {
				a := accounts[name]
				if !has(valStr(a.Harness)) {
					continue
				}
				f := ""
				if r, isStr := valStr(a.Readiness); a.Readiness.Set && !(isStr && r == "ready") {
					f = "login-absent"
				}
				checks = append(checks, check("account", name, f))
			}
		}
	}
	if has("claude", true) {
		var homes []string
		for _, name := range sortedKeysOf(accounts) {
			a := accounts[name]
			if h, _ := valStr(a.Harness); h == "claude" {
				if home, ok := valStr(a.NativeHome); ok {
					homes = append(homes, home)
				}
			}
		}
		pl, perr := claudePlacementHere()
		checks = append(checks, claudeChecks(host, pl, perr, homes)...)
	}
	codingHarnesses := map[string]bool{}
	for role, profile := range project.ProfileIds {
		if projectRequirements(project, profile) != nil {
			codingHarnesses[project.Roles[role]] = true
		}
	}
	execution := contract.ProjectExecution{}
	if project.Execution != nil {
		execution = *project.Execution
	}
	if len(execution.Profiles) == 0 {
		for _, h := range harnesses {
			codingHarnesses[h] = true
		}
	}
	if (len(execution.Profiles) == 0 || len(codingHarnesses) > 0) && len(execution.Tools) > 0 && len(accounts) == 0 {
		checks = append(checks, check("coding-tools", host, "managed-account-unavailable"))
	}
	if (len(execution.Profiles) == 0 || len(codingHarnesses) > 0) && execution.Forge != nil && *execution.Forge {
		var forgeErr string
		if repo := gitRepo(project, nil); repo != nil {
			forgeErr = publishingReadiness(str0(repo.Remote), str0(repo.Forge), str0(repo.Host))
		} else {
			forgeErr = "forge-repository-not-granted"
		}
		checks = append(checks, check("forge", host, forgeErr))
	}
	for _, name := range sortedKeysOf(accounts) {
		a := accounts[name]
		harness, _ := valStr(a.Harness)
		if r, isStr := valStr(a.Readiness); !codingHarnesses[harness] || (isStr && r != "ready") {
			continue
		}
		home := a.Home
		for _, tool := range execution.Tools {
			failed := ""
			var err error
			if tool == "browser" {
				err = browserReady(payload, home)
			} else {
				err = codingTool(tool, home)
			}
			if err != nil {
				failed = "coding-tool-unready"
			}
			checks = append(checks, check("tool:"+tool, name, failed))
		}
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

// projectRequirements uses the execution's frozen project and profile.
func projectRequirements(project contract.ProjectContext, profile string) *contract.ProjectExecution {
	if project.Execution == nil {
		return nil
	}
	ids := project.Execution.Profiles
	if len(ids) == 0 || slices.Contains(ids, profile) {
		return project.Execution
	}
	return nil
}
