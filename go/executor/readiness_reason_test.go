package main

import "testing"

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
