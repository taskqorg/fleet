package main

import (
	"os"
	"path/filepath"
	"testing"

	"fleet/contract"
)

// A project's environment checkout on this host is allowed without FLEET_PROJECT_ROOTS; another host's is not.
func TestProjectRootsForEnvironments(t *testing.T) {
	t.Setenv("FLEET_PROJECT_ROOTS", "")
	dir, err := canon(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	mine, other := filepath.Join(dir, "dam"), filepath.Join(dir, "other")
	for _, d := range []string{mine, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	host, peer := "owner-mac", "pds18"
	project := contract.ProjectContext{Environments: []contract.ProjectEnvironment{{HostId: &host, CheckoutPath: &mine}, {HostId: &peer, CheckoutPath: &other}}}
	roots := projectRootsFor(project, host)
	if len(roots) != 1 || roots[0] != mine {
		t.Fatalf("roots = %v, want [%s]", roots, mine)
	}
}
