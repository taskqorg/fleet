package main

import (
	"context"
	"strings"
	"testing"

	"fleet/contract"
)

// TestPinsPublishOnlyProposals: the write key posts statuses and turns on auto-merge only on automation/<kind>-pins
// heads based on main; every other branch is refused before any call (owner's restriction, 2026-11-26).
func TestPinsPublishOnlyProposals(t *testing.T) {
	var calls []string
	old := pinsGatewayCall
	pinsGatewayCall = func(_ context.Context, _ *App, tool string, args any) contract.CallLine {
		calls = append(calls, tool+" "+string(jsonBytes(args)))
		return contract.CallLine{Outcome: contract.CallOutcomeOk}
	}
	t.Cleanup(func() { pinsGatewayCall = old })
	sha := strings.Repeat("a", 40)
	pr := func(head, base string) ghPull {
		p := ghPull{URL: "u", NodeID: "PR_1", Head: ghPullHead{Ref: head, SHA: sha}, Base: ghPullHead{Ref: base}}
		p.Head.Repo.FullName = githubPublicRepo
		return p
	}
	fork := pr("automation/cli-pins", "main")
	fork.Head.Repo.FullName = "someone/fleet"
	for _, refused := range []ghPull{
		pr("feature", "main"), pr("automation/harness", "main"), pr("automation/cli-pins/x", "main"),
		pr("automation/-pins", "main"), pr("claude/automation/cli-pins", "main"), pr("automation/cli-pins", "release"),
		{URL: "u", NodeID: "PR_1", Head: ghPullHead{Ref: "automation/cli-pins", SHA: "abc"}, Base: ghPullHead{Ref: "main"}}, fork,
	} {
		if err := pinsPublish(context.Background(), nil, refused, true, "ok", ""); err == nil {
			t.Errorf("%s -> %s: written", refused.Head.Ref, refused.Base.Ref)
		}
	}
	if len(calls) != 0 {
		t.Fatalf("refused branches called: %v", calls)
	}
	if err := pinsPublish(context.Background(), nil, pr("automation/tool-pins", "main"), false, "node failed", "x"); err != nil || len(calls) != 1 || !strings.Contains(calls[0], `"state":"failure"`) {
		t.Fatalf("failed qualification: %v %v", err, calls)
	}
	calls = nil
	if err := pinsPublish(context.Background(), nil, pr("automation/cli-pins", "main"), true, "ok", "x"); err != nil || len(calls) != 2 ||
		!strings.HasPrefix(calls[1], "status_create") || !strings.Contains(calls[1], `"context":"code-review"`) {
		t.Fatalf("passed qualification: %v %v", err, calls)
	}
}
