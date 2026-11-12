package main

import (
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: each merged pull request gets its stage times, release and queue removals from the recorded runs and
// releases, and a stage whose last 24 h are past the baseline's P50 by the ratio is a regression naming the first
// pull request over it and the one before.
// Risk: a release boundary, a failed queue run or the wrong window moves a number, and a regression is missed or false.
func TestDeliveryHistoryDeriveAndCompare(t *testing.T) {
	s := func(v string) *string { return &v }
	n := func(v int64) *int64 { return &v }
	prs := []contract.DeliveryPr{
		{Number: 1, Url: "u1", MergeSha: "m1", Opened: "2026-11-12T10:00:00Z", PrCiDone: s("2026-11-12T10:05:00Z"), Merged: "2026-11-12T10:20:00Z"},
		{Number: 2, Url: "u2", MergeSha: "m2", Opened: "2026-11-12T11:00:00Z", PrCiDone: s("2026-11-12T11:05:00Z"), Merged: "2026-11-12T11:40:00Z"},
	}
	runs := []contract.DeliveryRun{
		{Id: 10, Pr: n(1), Sha: "m1", Created: "2026-11-12T10:06:00Z", Completed: s("2026-11-12T10:18:00Z"), Conclusion: "success", WallMs: n(12 * 60e3)},
		{Id: 11, Pr: n(2), Sha: "x", Created: "2026-11-12T11:06:00Z", Completed: s("2026-11-12T11:16:00Z"), Conclusion: "failure"},
		{Id: 12, Pr: n(2), Sha: "m2", Created: "2026-11-12T11:20:00Z", Completed: s("2026-11-12T11:38:00Z"), Conclusion: "success"},
	}
	rels := []contract.DeliveryRelease{
		{Tag: "v1.2.1", Target: "m1", Published: "2026-11-12T10:25:00Z", Installed: map[string]string{"coordinator": "2026-11-12T10:30:00Z"}},
		{Tag: "v1.2.2", Target: "m2", Published: "2026-11-12T11:45:00Z", Installed: map[string]string{}},
	}
	deliveryDerive(prs, runs, rels)
	p1, p2 := prs[0], prs[1]
	if deref(p1.Release) != "v1.2.1" || p1.Durations["pr_ci"] != 5*60e3 || p1.Durations["queue"] != 12*60e3 || p1.Durations["install"] != 5*60e3 || p1.Durations["total"] != 30*60e3 {
		t.Fatalf("pr 1: %+v", p1)
	}
	if deref(p2.Release) != "v1.2.2" || p2.QueueRuns != 2 || p2.QueueRemovals != 1 || p2.Flakes != 1 || deref(p2.Enqueued) != "2026-11-12T11:06:00Z" || p2.Durations["queue"] != 32*60e3 {
		t.Fatalf("pr 2: %+v", p2)
	}
	if _, ok := p2.Durations["install"]; ok {
		t.Fatalf("pr 2 is not installed: %+v", p2.Durations)
	}
	base := contract.DeliveryBaseline{Stats: []contract.DeliveryStat{{Key: "queue", N: 2, P50Ms: 10 * 60e3, P90Ms: 40 * 60e3}}}
	now, _ := time.Parse(time.RFC3339, "2026-11-12T12:00:00Z")
	cur := deliveryValues(prs, runs, now.Add(-24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
	regs := deliveryCompare(prs, cur, base, contract.DeliverySettings{Ratio: 1.5, MinSamples: 2, MinDeltaMs: 60e3})
	// queue P50 is 32m (nearest rank of 12m, 32m) against 10m; P90 32m is under 40m.
	if len(regs) != 1 || regs[0].Key != "queue" || regs[0].Metric != contract.DeliveryMetricP50 || regs[0].CurrentMs != 32*60e3 || regs[0].FirstPr == nil || *regs[0].FirstPr != 2 || regs[0].LastGoodPr == nil || *regs[0].LastGoodPr != 1 || deref(regs[0].CompareUrl) != "https://github.com/taskqorg/fleet/compare/m1...m2" {
		t.Fatalf("regressions: %+v", regs)
	}
	if got := deliveryCompare(prs, cur, base, contract.DeliverySettings{Ratio: 1.5, MinSamples: 3, MinDeltaMs: 60e3}); len(got) != 0 {
		t.Fatalf("too few values must not regress: %+v", got)
	}
	pending := deliverySeries{}
	deliveryPending(pending, []contract.DeliveryPr{{Number: 3, Merged: "2026-11-12T11:00:00Z"}}, "2026-11-12T00:00:00Z", "2026-11-12T12:00:00Z", "")
	if len(pending["release"]) != 1 || pending["release"][0].ms != 3600e3 {
		t.Fatalf("a pull request waiting for its release counts its wait so far: %+v", pending)
	}
	got, errs := dhParallel(40, func(i int) (int, error) { return i * 2, nil })
	if len(got) != 40 || got[39] != 78 || errs[0] != nil {
		t.Fatalf("parallel reads keep their index: %v", got)
	}
}
