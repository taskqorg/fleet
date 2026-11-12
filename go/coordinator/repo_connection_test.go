package main

import "testing"

// Requirement: placement uses the same first host environment as the executor.
// Risk: a second environment makes dispatch check a different repository connection.
func TestRepoConnectionFirstEnvironment(t *testing.T) {

	got := repoConnection([]any{doc{"id": "main", "connection": "cs-gitlab"}}, []map[string]any{{"hostId": "h"}, {"hostId": "h", "repo": "missing"}}, "h")
	if got != "cs-gitlab" {
		t.Fatalf("connection = %q", got)
	}
}
