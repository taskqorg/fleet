package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: a pipeline script step that names no host is placed as dispatch places agent work: an online host that
// accepts work and can run scripts, a spare host only when no other qualifies, the default host when none does.
// Risk: 2026-11-25 every hostless script step went to the default host (FLEET_HOST_ID, owner-mac on prod) even after
// owner-mac was made spare, and even while it was overloaded or updating.
func TestScriptStepHostChoice(t *testing.T) {
	env := openTestCoordinator(t, false)
	spare, open := "script-spare", "script-open"
	for _, h := range []string{spare, open} {
		secret := env.registerHost(t, h)
		v := "1.261125.1"
		if code, body := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
			HostId: h, Platform: contract.Platform("linux"), Capabilities: []string{"codex", "script"}, Stores: []contract.StoreReport{},
			Problems: []contract.EdgeProblem{}, ProjectRoots: []string{}, GitConnections: []string{}, ActiveExecutions: []string{},
			Load: &contract.HostLoad{Load1: ptr(1.0), Cpus: ptr(int64(8)), MemFree: ptr(8e9), MemTotal: ptr(24e9)}, Version: &v,
		}, map[string]string{"Authorization": "Bearer " + secret}); code != 200 {
			t.Fatalf("poll %s = %d %v", h, code, body)
		}
	}
	intake := func(h, v string) {
		t.Helper()
		if code, body := env.callJSON(t, "POST", "/api/ops/hosts_update", doc{"id": h, "intake": v}, env.ownerHeaders()); code != 200 {
			t.Fatalf("hosts_update %s %s = %d %v", h, v, code, body)
		}
	}
	pick := func() string {
		t.Helper()
		var got string
		if err := env.app.store.Write(env.ctx, nil, []string{GLOBAL}, func(q *Request) error {
			var err error
			got, err = scriptHost(q, "", testOwnerEmail)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return got
	}
	intake(spare, "spare")
	if h := pick(); h != open {
		t.Fatalf("script host with an open and a spare host = %s, want %s", h, open)
	}
	intake(open, "paused")
	if h := pick(); h != spare {
		t.Fatalf("script host with only a spare host accepting = %s, want %s", h, spare)
	}
	intake(spare, "paused")
	if h := pick(); h != env.app.hostID {
		t.Fatalf("script host with no host accepting = %s, want the default %s", h, env.app.hostID)
	}
}
