package main

import (
	"fleet/contract"
	"strings"
	"testing"
)

// Requirement: fleet-people-access.md section 8: burn is the drops in left over the window per hour (resets add
// nothing), the projection falls at that burn, steps to 100 at the reported reset, and the pool weights members by
// planWeight, keeping unknown members out. Risk: the Limits screen and the PM signal show a wrong empty time.
func TestLimitBurnProjectionPool(t *testing.T) {
	pf := func(v float64) *float64 { return &v }
	now := int64(100 * hourMs)
	o := limitOptions{now: now, from: now - 7*24*hourMs, to: now + 10*hourMs, windowHours: 3, margin: 5}
	reset := now + 4*hourMs
	// 40 -> 50 -> 20 (a reset, adds nothing) -> 50: drops 10 + 30 = 40 points in 3 h.
	obs := []limitObs{{at: now - 3*hourMs, pct: pf(40)}, {at: now - 2*hourMs, pct: pf(50)}, {at: now - hourMs, pct: pf(20)}, {at: now, pct: pf(50), resetsAt: &reset}}
	if b := limitBurn(obs, o); b == nil || *b != 40.0/3 {
		t.Fatalf("burn %v", b)
	}
	a := limitAccount{id: "a", harness: "codex", weight: 2}
	ps := limitPeriods(a, map[string][]limitObs{"5h": obs}, o)
	p := ps[0]
	// left 50 at 13.33/h empties after 3.75 h, before the reset at 4 h.
	if p.FirstEmpty.State != contract.LimitEmptyStateAt || *p.FirstEmpty.At != now+int64(3.75*hourMs) {
		t.Fatalf("first empty %+v", p.FirstEmpty)
	}
	if v, ok := valueAt(p.Projection, reset); !ok || v != 100 {
		t.Fatalf("no step to 100 at the reset: %+v", p.Projection)
	}
	unknown := limitAccount{id: "b", harness: "codex", weight: 1}
	pools := limitPools([]limitAccount{a, unknown}, map[string][]contract.LimitPeriod{"a": ps, "b": {{Period: "5h"}}}, o)
	if len(pools) != 1 || pools[0].Capacity != 200 || pools[0].Left != 100 || len(pools[0].Unknown) != 1 || *pools[0].Burn != 2*40.0/3 {
		t.Fatalf("pool %+v", pools)
	}
	if pools[0].PoolEmpty.State != contract.LimitEmptyStateAt || pools[0].NextReset.At == nil || *pools[0].NextReset.Restore != 200 {
		t.Fatalf("pool empty %+v reset %+v", pools[0].PoolEmpty, pools[0].NextReset)
	}
}

// Requirement: section 8 "PM agents": the watch tells a director's PM once per (harness, period, next reset) when a
// pool holding their subscription has a member empty before that reset; other people's pools stay silent. Risk: the
// PM is never told before limits run out, or is told every tick.
func TestPoolSignalsOncePerReset(t *testing.T) {
	at, reset := int64(1_000), int64(2_000)
	r := contract.SubscriptionLimitsReply{
		Subscriptions: []contract.LimitSubscription{{Id: "a", Holder: []byte(`"d@x"`)}, {Id: "b", Holder: []byte(`"o@x"`)}},
		Pools: []contract.LimitPool{
			{Harness: "codex", Period: "weekly", Members: []string{"a"}, FirstEmpty: contract.LimitEmpty{State: contract.LimitEmptyStateAt, At: &at}, NextReset: contract.LimitReset{At: &reset}},
			{Harness: "claude", Period: "weekly", Members: []string{"b"}, FirstEmpty: contract.LimitEmpty{State: contract.LimitEmptyStateAt, At: &at}, NextReset: contract.LimitReset{At: &reset}},
		}}
	dir := &Person{Email: "d@x"}
	parts, keys := poolSignals(r, dir, nil)
	if len(parts) != 1 || !strings.Contains(parts[0], "codex weekly") || !strings.Contains(parts[0], "never set it") {
		t.Fatal(parts)
	}
	if again, _ := poolSignals(r, dir, keys); len(again) != 0 {
		t.Fatal("sent twice", again)
	}
}
