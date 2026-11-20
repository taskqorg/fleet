package main

import "testing"

// Requirement: placement uses the same first host environment as the executor.
// Risk: a second environment makes dispatch check a different repository connection.
func TestRepoConnectionFirstEnvironment(t *testing.T) {

	missing := "missing"
	got := repoConnection([]projectRepo{{ID: "main", Connection: "cs-gitlab"}}, []projectEnvironment{{HostID: "h"}, {HostID: "h", Repo: &missing}}, "h")
	if got != "cs-gitlab" {
		t.Fatalf("connection = %q", got)
	}
}
