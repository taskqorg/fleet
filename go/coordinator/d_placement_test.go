package main

import "testing"

// Requirement: a confirmed retention rule keeps its confirmation (artifact_rule_confirmation.rule_hash) across the rewrite.
// Risk: a different hash silently un-confirms every rule, so moves and deletes stop. Hashes from placement.ts ruleHash.
func TestRuleHashMatchesTypeScript(t *testing.T) {
	for want, r := range map[string]artifactRule{
		"a50da7d3ec279517bfbfdce28045fb4b5b8272889b6a266b24516edfce409e29": {Scope: "lab", OlderThanDays: 30, Action: "move", To: []string{"pds18"}},
		"1465b5fcb4a3b06ab8189e7cf14ae917368f36606508f4ded369e9847c21c3b9": {Scope: "prod", OlderThanDays: 0, Action: "delete"},
	} {
		if got := ruleHash(r); got != want {
			t.Errorf("%+v: %s", r, got)
		}
	}
}
