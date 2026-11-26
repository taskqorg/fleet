package main

import (
	"sort"
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/obs"
)

// Requirement: a run on another host than the session's last run starts a fresh native session.
// Risk: the executor refuses to resume a native session that lives on another host, on every run.
func TestNativeForOtherHost(t *testing.T) {
	native, mac := ptr("01a20ebe"), ptr("owner-mac")
	if got := nativeFor(native, mac, "owner-mac"); got != native {
		t.Fatalf("same host: %v", got)
	}
	if got := nativeFor(native, mac, "pds18-native"); got != nil {
		t.Fatalf("other host: got native session ID %q, want it omitted", *got)
	}
	if got := nativeFor(native, nil, "pds18-native"); got != native {
		t.Fatalf("no last host: %v", got)
	}
	if got := nativeFor(ptr(""), mac, "owner-mac"); got != nil {
		t.Fatalf("empty ID: got %q, want it omitted", *got)
	}
}

// Requirement: docs/designs/fleet-conversations.md#rules: a continuation keeps its host and its account; it changes
// account only when its last one cannot serve.
// Risk: a bot's conversation rotates accounts on every wake and loses its native session each time (C-FLT-592: 45
// handoffs, all account changes).
func TestParkRankKeepsHostAndAccount(t *testing.T) {
	sub := func(id string) *candidateSubscription { return &candidateSubscription{ID: id} }
	out := []*candidate{{Host: "pds18", Subscription: sub("a")}, {Host: "mac", Subscription: sub("a")}, {Host: "mac", Subscription: sub("b")}}
	sort.SliceStable(out, func(i, j int) bool { return parkRank(out[i], "mac", "b", false) < parkRank(out[j], "mac", "b", false) })
	if out[0].Host != "mac" || out[0].Subscription.ID != "b" || out[1].Host != "mac" || out[2].Host != "pds18" {
		t.Fatalf("order: %s/%s %s/%s %s/%s", out[0].Host, out[0].Subscription.ID, out[1].Host, out[1].Subscription.ID, out[2].Host, out[2].Subscription.ID)
	}
	if got := effectiveAccountID([]byte(`{"accountId":"b","binding":"x"}`)); got != "b" {
		t.Fatalf("effectiveAccountID = %q", got)
	}
	if got := effectiveAccountID([]byte(`{"binding":"x","legacy":true}`)); got != "" {
		t.Fatalf("legacy effectiveAccountID = %q", got)
	}
}

// Requirement: a host_move handoff stays within its budget however long the conversation is.
// Risk: a PM session moved between hosts again and again starts each run with its whole history.
func TestHostMoveHandoffIsBounded(t *testing.T) {
	turns := make([]agentTurn, 40)
	for i := range turns {
		turns[i] = agentTurn{harness: "codex", state: "succeeded", user: strings.Repeat("x", 3000), assistant: []string{"ok"}}
	}
	out := renderHandoff(turns, "host_move", "session s on owner-mac")
	if n := len([]rune(out)); n > hostMoveRunes || !strings.Contains(out, "local commits are not here") || !strings.Contains(out, "Only the last 12 of 40 turns") {
		t.Fatalf("host_move handoff: %d runes", n)
	}
}

// Requirement: docs/designs/fleet-host-cleanup.md#6-host-saturation: swap use above ruleSwapUsedShare of swap total
// overloads a host whatever its free memory; the reason names swap, free memory and the top three consumers, from a
// poll's load and from a heartbeat span's fleet.* attributes alike.
// Risk: a host with full swap keeps taking work, or waits it with a reason that does not say what holds its memory.
func TestHostOverloadReasonSwapAndTopConsumers(t *testing.T) {
	const gib = 1 << 30
	poll := hostLoadFromContract(&contract.HostLoad{Load1: ptr(2.0), Cpus: ptr(int64(8)), MemFree: ptr(0.03 * 32 * gib), MemTotal: ptr(32.0 * gib),
		SwapUsed: ptr(37.0 * gib), SwapTotal: ptr(38.0 * gib), Top: []contract.HostConsumer{
			{Pid: 1, Command: "com.apple.Virtualization.VirtualMachine", MemBytes: 20 * gib},
			{Pid: 2, Command: "postgres", MemBytes: 2 * gib, Fleet: ptr(true)},
			{Pid: 3, Command: "node", MemBytes: 1 * gib},
			{Pid: 4, Command: "go", MemBytes: 0.5 * gib},
		}}, 1000)
	// Low free memory refuses placement; swap above 80% and the top consumers are named.
	want := "owner-mac is overloaded (free memory 3% below 5%, swap 37/38 GB); top: com.apple.Virtualization.VirtualMachine 20 GB, postgres 2 GB (fleet), node 1 GB"
	if got := hostOverloadReason("owner-mac", poll, 1000); got != want {
		t.Fatalf("poll reason = %q, want %q", got, want)
	}
	span := spanHostLoad(&obs.Span{Start: time.UnixMilli(1000).UTC().Format(obs.TimeFormat), Load1: ptr(2.0), MemFree: ptr(0.03 * 32 * gib), Attrs: map[string]string{
		"fleet.cpus": "8", "fleet.mem_total": "34359738368", "fleet.swap_used": "39728447488", "fleet.swap_total": "40802189312",
		"fleet.top": "com.apple.Virtualization.VirtualMachine:20480,postgres:2048*,node:1024,go:512"}}, 1000)
	if got := hostOverloadReason("owner-mac", span, 1000); got != want {
		t.Fatalf("span reason = %q, want %q", got, want)
	}
	// Swap alone never refuses placement: macOS keeps swap after pressure passes.
	poll.MemFree = ptr(0.25 * 32 * gib)
	if got := hostOverloadReason("owner-mac", poll, 1000); got != "" {
		t.Fatalf("swap with free memory: reason = %q", got)
	}
	// Swap at 80% is not named.
	poll.MemFree, poll.SwapUsed = ptr(0.03*32*gib), ptr(0.8*38*gib)
	if got := hostOverloadReason("owner-mac", poll, 1000); !strings.HasPrefix(got, "owner-mac is overloaded (free memory 3% below 5%); top: ") {
		t.Fatalf("memory reason = %q", got)
	}
}

// Requirement: dispatch places no start on a host whose executor fences starts for an update, and places again once
// the fence opens; risk: starts wait preparing behind a long run while other hosts idle (2026-11-26, pds18-native).
func TestHostDrainMirrorsExecutorFence(t *testing.T) {
	old, newer, now := "1.261125.155", "1.261125.165", time.Now()
	if d := hostDrain(nil, &old, &newer, "on", now); d == nil || !d.Fenced || d.Target != newer || (hostSeen{Drain: d}).drainTarget() != newer {
		t.Fatalf("an older executor with a newer offer is not fenced: %+v", d)
	}
	if hostDrain(nil, &newer, &newer, "on", now) != nil || hostDrain(nil, &old, &newer, "off", now) != nil ||
		hostDrain(nil, &old, &newer, now.Add(time.Hour).UTC().Format(time.RFC3339), now) != nil || hostDrain(nil, &old, nil, "on", now) != nil {
		t.Fatal("fenced without a pending update")
	}
	open := &contract.DrainReport{Target: newer, Fenced: false, WaitsFor: []string{"1 run"}}
	if d := hostDrain(open, &old, &newer, "on", now); (hostSeen{Drain: d}).drainTarget() != newer {
		t.Fatal("an open pause of the drain let work in: the host would never reach its idle moment")
	}
	if got := drainDetail(&contract.DrainReport{Target: newer, Fenced: true, WaitsFor: []string{"1 run"}}); got != "updating to 1.261125.165: takes no new start; waits for 1 run" {
		t.Fatal(got)
	}
}

func TestRollingDrainFewestRunsFirst(t *testing.T) {
	a := &App{}
	if other := a.rollingDrain("pds18", true, 4); other != "" {
		t.Fatalf("the only busy host was held: %q", other)
	}
	if other := a.rollingDrain("windows", true, 6); other != "pds18" {
		t.Fatalf("a busier host took the turn: %q", other)
	}
	if other := a.rollingDrain("owner-mac", true, 0); other != "" {
		t.Fatalf("an idle host waited for the turn: %q", other)
	}
	if other := a.rollingDrain("windows", true, 1); other != "" {
		t.Fatalf("the emptier host did not take the turn: %q", other)
	}
	if other := a.rollingDrain("pds18", true, 4); other != "windows" {
		t.Fatalf("pds18 kept the turn from an emptier host: %q", other)
	}
	a.rollingDrain("windows", false, 0) // windows updated
	if other := a.rollingDrain("pds18", true, 4); other != "" {
		t.Fatalf("pds18 held after windows finished: %q", other)
	}
}
