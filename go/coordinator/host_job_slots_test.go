package main

import "testing"

// A host job takes a slot once it runs; a script step handed to its host and queued there does not (work-allocation
// B7). An agent run takes one while it prepares.
func TestHostJobSlotsCountOnceRunning(t *testing.T) {
	e := openTestCoordinator(t, false)
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		for i, st := range []string{"preparing", "preparing", "running"} {
			id := "job-" + string(rune('a'+i))
			if err := executionWrite(q, pipelineHostJob{ID: id, ExecutionID: id, HostID: "mac", State: st, CreatedAt: nowMs(),
				Actor: "program-worker", ProjectSnapshot: []byte("null")}); err != nil {
				return err
			}
		}
		x := agentExecution{ID: "run-1", ExecutionID: "run-1", State: "preparing", CreatedAt: nowMs()}
		x.HostID = ptr("mac")
		if err := executionPut(q, &x); err != nil {
			return err
		}
		rows, err := q.db().DispRunningByHost(q.ctx())
		if err != nil {
			return err
		}
		n := map[string]int32{}
		for _, r := range rows {
			n[r.HostID] = r.N
		}
		if n["mac"] != 2 {
			t.Fatalf("mac counts %d slots, want 2 (the running host job and the preparing agent run)", n["mac"])
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
