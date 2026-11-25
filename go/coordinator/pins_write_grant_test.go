package main

import "testing"

// TestPinsWriteCoordinatorOnly: the pin write connection is granted only to the coordinator's own read token.
func TestPinsWriteCoordinatorOnly(t *testing.T) {
	s := func(v string) *string { return &v }
	coordinator := mintRun{ExecutionID: s(pinsWriteConn + "-read:1"), ProjectID: s("fleet"), Step: true, SystemActor: s("coordinator")}
	if !coordinatorOnlyAllowed(pinsWriteConn, coordinator) {
		t.Fatal("coordinator refused")
	}
	for name, run := range map[string]mintRun{
		"agent run":         {ExecutionID: s("e1"), ProjectID: s("fleet"), DirectedBy: s("owner@x")},
		"other read token":  {ExecutionID: s("github-read:1"), ProjectID: s("fleet"), Step: true, SystemActor: s("coordinator")},
		"pipeline step":     {ExecutionID: s(pinsWriteConn + "-read:1"), PipelineRunID: s("r"), SystemActor: s("coordinator")},
		"claimed by record": {ExecutionID: s("e2"), SystemActor: s("coordinator")},
	} {
		if coordinatorOnlyAllowed(pinsWriteConn, run) {
			t.Errorf("%s granted the pin write connection", name)
		}
	}
	if !coordinatorOnlyAllowed("github", mintRun{ExecutionID: s("e1")}) {
		t.Fatal("other connections changed")
	}
}
