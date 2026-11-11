package main

import "testing"

// Requirement: placement uses the same first host environment as the executor.
// Risk: a second environment makes dispatch check a different repository connection.
func TestRepoConnectionFirstEnvironment(t *testing.T) {
	t.Setenv("FLEET_PROJECT_CONFIG_DIRECTORY", "../../config")
	got := repoConnection("fleet-hello-world", []map[string]any{{"hostId": "h"}, {"hostId": "h", "repo": "missing"}}, "h")
	if got != "fleet-hello-world" {
		t.Fatalf("connection = %q", got)
	}
}
