package main

import (
	"testing"

	"fleet/contract"
)

func TestModelRefusal(t *testing.T) {
	probed := accountBinding{Models: []string{"opus", "claude-sonnet-5-5"}}
	for _, c := range []struct {
		b     accountBinding
		model string
		want  bool
	}{
		{accountBinding{}, "claude-opus-5-5", false},                  // not probed: never skips
		{probed, "", false},                                           // no model asked
		{probed, "claude-opus-5-5", false},                            // alias "opus"
		{probed, "claude-sonnet-5-5", false},                          // exact
		{probed, "claude-haiku-5-5", true},                            // not offered
		{accountBinding{Models: []string{}}, "claude-opus-5-5", true}, // probed, offers nothing
		{accountBinding{RejectedModels: map[string]int64{"gpt-5.5": 1}}, "gpt-5.5", true},
	} {
		if got := c.b.modelRefusal(c.model) != ""; got != c.want {
			t.Errorf("%+v %q: refused %v, want %v", c.b, c.model, got, c.want)
		}
	}
}

func TestRejectedModelText(t *testing.T) {
	for p, want := range map[string]bool{
		"turn.failed: model_not_found: The model `gpt-9` does not exist": true,
		"harness exited 1: Error: model claude-opus-9 not found":         true,
		"turn.failed: invalid model: foo":                                true,
		"harness exited 1: rate limit reached":                           false,
		"preparation failed: no usable key":                              false,
	} {
		if got := rejectedModelText.MatchString(p); got != want {
			t.Errorf("%q: %v, want %v", p, got, want)
		}
	}
}

// Requirement: J5 router skips are readable per subscription (route_decisions subscriptionId) and spend groups by UTC
// day for the System sparklines. Risk: the JSON path or the day key breaks on a real database.
func TestSubscriptionSkipsAndSpendByDay(t *testing.T) {
	env := openTestCoordinator(t, false)
	if _, err := env.ownerStore.pool.Exec(env.ctx, `INSERT INTO route_decision(execution_id, candidates, winner, at) VALUES
		('e-skip', '[{"subscription":{"id":"s1"},"host":"pds18","skipped":"max on pds18 does not offer m (probe)","cause":"subscription:s1:model:m","warnings":[]},{"subscription":{"id":"s1"},"host":"mac","warnings":[]}]', '{"host":"mac"}', now()),
		('e-other', '[{"subscription":{"id":"s2"},"host":"pds18","skipped":"x","cause":"subscription:s2:model:m","warnings":[]}]', NULL, now()),
		('e-old', '[{"subscription":{"id":"s1"},"host":"pds18","skipped":"x","cause":"subscription:s1:model:m","warnings":[]}]', NULL, now() - interval '2 days')`); err != nil {
		t.Fatal(err)
	}
	sub := "s1"
	code, body := env.callJSON(t, "POST", "/api/ops/route_decisions", contract.RouteDecisionsRequest{SubscriptionId: &sub}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("route_decisions(subscriptionId) = %d %v", code, body)
	}
	if ds := anys(body["decisions"]); len(ds) != 1 || mapOf(ds[0])["executionId"] != "e-skip" {
		t.Fatalf("decisions = %v, want only e-skip (s1 skipped within 24 h)", body["decisions"])
	}
	code, body = env.callJSON(t, "POST", "/api/ops/spend_summary", map[string]any{"by": "day", "window": map[string]any{"from": "2026-01-01", "to": "2027-01-01"}}, env.ownerHeaders())
	if code != 200 || body["by"] != "day" {
		t.Fatalf("spend_summary by day = %d %v", code, body)
	}
}
