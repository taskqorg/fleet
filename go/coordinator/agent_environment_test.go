package main

import (
	"slices"
	"testing"

	"fleet/contract"
)

// Requirement: system_changes answers the agent environment: the grants drift and the sandbox each shipped profile
// gets, grouped by harness, mode and network (coding-worker runs Codex without a sandbox; claude-reviewer has no Bash).
func TestAgentEnvironment(t *testing.T) {
	e := openTestCoordinator(t, false)
	var d contract.SystemChangesReply
	e.opInto(t, "system_changes", map[string]any{}, e.ownerHeaders(), &d)
	env := d.AgentEnvironment
	if env.Profiles == 0 || env.Grants.Connections == nil {
		t.Fatalf("agent environment: %+v", env)
	}
	group := func(profile string) contract.SandboxGroup {
		for _, g := range env.Sandbox {
			if slices.Contains(g.Profiles, profile) {
				return g
			}
		}
		t.Fatalf("no sandbox group has %s", profile)
		return contract.SandboxGroup{}
	}
	if g := group("coding-worker"); g.Harness != "codex" || g.Mode != "no sandbox on writable runs" || !g.Network {
		t.Errorf("coding-worker: %+v", g)
	}
	if g := group("claude-reviewer"); g.Harness != "claude" || g.Mode != "file tools only, no Bash" || g.Network {
		t.Errorf("claude-reviewer: %+v", g)
	}
	if g := group("claude-worker"); g.Mode != "Bash without a sandbox" || !g.Network {
		t.Errorf("claude-worker: %+v", g)
	}
}
