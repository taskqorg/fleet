package main

import (
	"fleet/contract"
	"testing"
)

func TestLsRemoteReason(t *testing.T) {
	for _, c := range []struct {
		code   int
		stderr string
		want   string
	}{
		{128, "remote: The project you were looking for could not be found or you don't have permission to view it.\nfatal: Could not read from remote repository.", "ls-remote-repo-missing-or-denied-exit-128"},
		{128, "ssh: Could not resolve hostname gitlab.example.com: nodename nor servname provided", "ls-remote-network-unreachable-exit-128"},
		{128, "fatal: unable to access: Could not resolve host: gitlab.example.com", "ls-remote-network-unreachable-exit-128"},
		{128, "git@github.com: Permission denied (publickey).", "ls-remote-auth-denied-exit-128"},
		{128, "ERROR: Repository not found.", "ls-remote-repo-missing-exit-128"},
		{2, "", "ls-remote-head-missing-exit-2"},
	} {
		if got := lsRemoteReason(c.code, c.stderr); got != c.want {
			t.Errorf("%q: got %s, want %s", c.stderr, got, c.want)
		}
	}
}

// Requirement: a failed readiness names the failed checks in its reason, so the run's cause tells which check failed
// (2026-11-26: Live showed only "a tool or check it needs failed" for a missing checkout and a missing key).
func TestReadinessFailureNamesChecks(t *testing.T) {
	r := contract.Readiness{Checks: []contract.ReadinessCheck{check("instruction-sources", "h", ""), check("checkout", "/p", "checkout-missing"), check("remote", "main", "connection-missing")}}
	if got, want := readinessFailure(r), "readiness-check-failed: checkout checkout-missing, remote connection-missing"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := readinessFailure(contract.Readiness{}); got != "readiness-check-failed" {
		t.Errorf("no failed check: got %q", got)
	}
}
