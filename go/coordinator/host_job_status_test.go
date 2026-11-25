package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: a host job has no session (fleet-conversations.md, migration step 2), and its status still reads.
// Risk: execution_status resumes the run's session and answers 400 "Expected sessionId" for every readiness check
// (prod 2026-11-26, after release 2).
func TestSessionlessHostJobStatus(t *testing.T) {
	e := openTestCoordinator(t, false)
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		h := "h1"
		x := agentExecution{ID: "rx", ExecutionID: "rx", HostID: &h, State: "preparing", CreatedAt: nowMs(), Actor: "program-worker"}
		if err := executionPut(q, &x); err != nil {
			return err
		}
		rep, err := executionStatus(q, &contract.ExecutionStatusRequest{ExecutionId: "rx"})
		if err != nil {
			return err
		}
		if rep.SessionId != nil {
			t.Fatalf("a host job has no session, got %v", *rep.SessionId)
		}
		return rep.Validate()
	}); err != nil {
		t.Fatal(err)
	}
}
