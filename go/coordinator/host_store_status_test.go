package main

import (
	"fleet/contract"
	"testing"
)

// Requirement: host storage health uses its own fresh observations.
// Risk: global reachability or stale/missing checks falsely claim usable storage.
func TestHostStoreStatus(t *testing.T) {
	report := func(v bool) any { return map[string]any{"id": "fixture", "reachable": v} }
	for _, c := range []struct {
		name    string
		reports any
		seen    float64
		want    string
	}{
		{"missing", nil, 100000, "unknown"},
		{"empty", []any{}, 100000, "unknown"},
		{"reachable", []any{report(true)}, 100000, "reachable"},
		{"unreachable", []any{report(false)}, 100000, "unreachable"},
		{"mixed", []any{report(true), report(false)}, 100000, "mixed"},
		{"stale", []any{report(true)}, 70000, "unknown"},
		{"unobserved", []any{report(true)}, 0, "unknown"},
		{"unsupported", []any{map[string]any{"id": "old"}}, 100000, "unknown"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, reason := hostStoreStatus(c.reports, c.seen, 100000)
			if got != c.want || reason == "" {
				t.Fatalf("got %s (%s), want %s", got, reason, c.want)
			}
		})
	}
}

// Requirement: poll store changes persist immediately while unchanged polls retain debounce.
// Risk: hosts_list shows an old successful check after a new failed or missing report.
func TestHostStorePollProjection(t *testing.T) {
	env := openTestCoordinator(t, false)
	secret := env.registerHost(t, "h-store-check")
	hdr := map[string]string{"Authorization": "Bearer " + secret}
	poll := func(reports []contract.StoreReport) {
		t.Helper()
		code, body := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{HostId: "h-store-check", Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: reports, Problems: []contract.EdgeProblem{}}, hdr)
		if code != 200 {
			t.Fatalf("poll %d %v", code, body)
		}
	}
	check := func(want string) {
		t.Helper()
		if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
			o, err := get(q, "h-store-check", "host-observation")
			if err != nil {
				return err
			}
			got, reason := hostStoreStatus(o["stores"], env.app.effectiveHostSeen("h-store-check", num(o["lastSeenAt"])), nowMs())
			if got != want || reason == "" {
				t.Fatalf("got %s %s want %s", got, reason, want)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	reports := []contract.StoreReport{{Id: "own", Reachable: true}}
	poll(reports)
	check("reachable")
	poll(reports)
	check("reachable")
	reports[0].Reachable = false
	poll(reports)
	check("unreachable")
	poll([]contract.StoreReport{})
	check("unknown")
}
