package main

import (
	"encoding/json"
	"testing"
)

// Requirement: docs/designs/fleet-analytics.md, System activity: a prepared project summary leaves Fleet's own calls
// out like the live reads. Risk: prepared project resources keep counting the coordinator's reads.
func TestSystemActivityNotInPreparedContribution(t *testing.T) {
	for leaf, want := range map[string]bool{
		`{"project_id":"p1","kind":"tool_run","state":"succeeded","has_totals":true}`:                              true,
		`{"project_id":"p1","kind":"tool_run","state":"succeeded","has_totals":true,"system_actor":"coordinator"}`: false,
	} {
		c, _, err := resourceContribution("call:x", resourceSnapshot{Leaf: json.RawMessage(leaf)})
		if err != nil || c.Included != want {
			t.Fatalf("%s: included %v, want %v (%v)", leaf, c.Included, want, err)
		}
	}
}
