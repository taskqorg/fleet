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
		"host:h:offline": "placement", "host:h:paused": "placement", "host:h:overloaded": "placement", "host:h:concurrency": "placement", "execution:e:no-candidate": "placement",
		"host:h:subscription:claude": "placement",
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

// Requirement: a run pinned to a host where none of the director's subscriptions is bound says where they are bound
// (live 2026-11-26: a review pinned to pds18-native waited on "no claude subscription of site-agents applies" while
// claude-owner is bound to windows). Risk: the agent and HAL0 chase a missing subscription that exists.
func TestBoundElsewhereNamesTheHosts(t *testing.T) {
	name, win, pds := "claude-owner", "windows", "pds18-native"
	s := &dispatchAccountRec{Name: &name, parsedBindings: []accountBinding{{HostID: &win}}}
	if got := boundElsewhere([]*dispatchAccountRec{s}, pds); got != "claude-owner on windows" {
		t.Errorf("pinned elsewhere: %q", got)
	}
	if got := boundElsewhere([]*dispatchAccountRec{s}, win); got != "" {
		t.Errorf("bound here: %q", got)
	}
	if got := boundElsewhere(nil, pds); got != "" {
		t.Errorf("no subscription: %q", got)
	}
}
