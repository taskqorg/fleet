package main

import "testing"

// TestPinsBlockedReason: a qualified proposal GitHub holds says why: a missing status by name, else the ruleset's
// required approval; never "merging".
func TestPinsBlockedReason(t *testing.T) {
	ok := map[string]string{"qualify-offline": "success", "qualify-hosts": "success", "code-review": "success"}
	if got := pinsBlockedReason(ok); got != "needs owner approval (ruleset)" {
		t.Fatal(got)
	}
	ok["code-review"] = "pending"
	if got := pinsBlockedReason(ok); got != "waits for code-review" {
		t.Fatal(got)
	}
}
