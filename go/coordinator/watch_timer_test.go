package main

import (
	"slices"
	"testing"

	"fleet/contract"
)

// Requirement (Conversations, Watch): timer-only changes preserve the saved event watch.
// Risk: a stale edit overwrites another timer change, or pausing/cancelling drops the event cursor.
func TestWatchTimerChangesPreserveSubscriptions(t *testing.T) {
	e := openTestCoordinator(t, false)
	sid := newID()
	if err := e.appStore.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return sessionPut(q, &agentSession{ID: sid, ProjectID: "desiign", DirectedBy: testOwnerEmail})
	}); err != nil {
		t.Fatal(err)
	}
	owner := e.ownerHeaders()
	member := e.ownerHeaders()
	member["X-Fleet-Person"] = "member@trial.test"
	call := func(name string, req any, headers map[string]string, want int) map[string]any {
		t.Helper()
		status, reply := e.callJSON(t, "POST", "/api/ops/"+name, req, headers)
		if status != want {
			t.Fatalf("%s: got %d, want %d (%v)", name, status, want, reply)
		}
		return reply
	}
	initial := float64(300_000)
	kinds := []string{"task", "limits"}
	first := call("watch_set", contract.WatchSetRequest{SessionId: sid, Kinds: kinds, TimerMs: &initial}, owner, 200)
	read := func(headers map[string]string) map[string]any {
		return call("watch_get", contract.WatchGetRequest{SessionId: sid}, headers, 200)
	}

	if got := read(member)["canManageTimer"]; got != false {
		t.Fatalf("another operator's timer permission = %v", got)
	}
	pause := contract.WatchTimerSetRequest{SessionId: sid, Action: "pause", BaseTimerMs: initial}
	call("watch_timer_set", pause, member, 403)
	call("watch_timer_set", pause, owner, 200)
	call("watch_timer_set", pause, owner, 409)
	paused := mapOf(read(owner)["watch"])
	if paused["timerPaused"] != true || paused["timerMs"] != initial || paused["nextWakeAt"] != nil {
		t.Fatalf("paused timer = %v", paused)
	}
	if err := e.appStore.Read(e.ctx, nil, func(q *Request) error {
		_, timer, err := watchTriggers(q, sid)
		if err == nil && (timer == nil || timer.Enabled) {
			t.Fatal("paused timer remains visible to the scheduler")
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}

	edited := float64(17_001)
	edit := contract.WatchTimerSetRequest{SessionId: sid, Action: "edit", BaseTimerMs: initial, BasePaused: true, TimerMs: &edited}
	updated := call("watch_timer_set", edit, owner, 200)
	if updated["timerPaused"] != true || updated["timerMs"] != edited || updated["nextWakeAt"] != nil {
		t.Fatalf("editing a paused timer activated it: %v", updated)
	}
	resume := contract.WatchTimerSetRequest{SessionId: sid, Action: "resume", BaseTimerMs: edited, BasePaused: true}
	active := call("watch_timer_set", resume, owner, 200)
	if active["timerPaused"] != false || active["nextWakeAt"] == nil || active["timerMs"] != edited {
		t.Fatalf("resumed timer = %v", active)
	}
	cancel := contract.WatchTimerSetRequest{SessionId: sid, Action: "cancel", BaseTimerMs: edited}
	call("watch_timer_set", cancel, owner, 200)
	final := mapOf(read(owner)["watch"])
	if final["timerMs"] != nil || final["nextWakeAt"] != nil || final["timerPaused"] != false ||
		final["cursor"] != first["cursor"] || !slices.Equal(strs(final["kinds"]), kinds) {
		t.Fatalf("cancel did not preserve event watch: before %v, after %v", first, final)
	}
}
