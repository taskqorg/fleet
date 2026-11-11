package main

import (
	"slices"
	"testing"
)

// Requirement: fleet-people-access.md route decision, one list of wait reasons: every dispatch stall cause maps to a
// member, and the three "waits for a person"
// kinds of OSC 7501 (permission, question, auth) come from the causes a person must act on. Risk: free-text reasons
// nobody can filter, or a person-blocked wait shown as Fleet retrying.
func TestDispatchCausesMapToWaitReasons(t *testing.T) {
	for cause, want := range map[string]string{
		"host:h:trust": "permission", "host:h:holder": "permission", "subscription:s:not-taken": "permission", "budget:b:reached": "permission",
		"director:d:claude": "auth", "subscription:s:not-ready": "auth",
		"host:h:offline": "placement", "host:h:paused": "placement", "execution:e:no-candidate": "placement",
	} {
		got := waitKindOf(cause)
		if got != want || !slices.Contains(waitReasons, got) {
			t.Errorf("%s: %s, want %s", cause, got, want)
		}
	}
	for _, k := range []string{"question", "dependency", "artifacts"} { // task and artifact waits
		if !slices.Contains(waitReasons, k) {
			t.Errorf("%s is not a wait reason", k)
		}
	}
}
