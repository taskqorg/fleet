package main

import (
	"encoding/json"
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
		{InstalledVersions: map[string]string{"coordinator": "1.2.1"}, Tag: "v1.2.1", Target: "m1", Published: "2026-11-12T10:25:00Z", Installed: map[string]string{"coordinator": "2026-11-12T10:30:00Z"}},
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

// Requirement: exact install evidence differs from superseding availability.
// Risk: skipped releases acquire fabricated successful-install samples.
func TestDeliveryObservationBasis(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	published := "2026-11-14T11:00:00Z"
	for _, c := range []struct {
		tag, version, at, want string
		exact                  bool
	}{
		{"v1.2.3", "1.2.3", "2026-11-14T11:30:00Z", "2026-11-14T11:30:00Z", true},
		{"v1.2.3", "1.2.4", "2026-11-14T11:30:00Z", "2026-11-14T11:30:00Z", false},
		{"v1.2.3", "1.2.2", published, "", false},
		{"ui-1.2.2-v1.2.3", "1.2.3", published, "", false},
		{"v1.2.3", "1.2.4", "2026-11-14T10:00:00Z", now.Format(time.RFC3339), false},
		{"v1.2.3", "1.2.3", "", now.Format(time.RFC3339), true},
	} {
		at, exact := deliveryObservation(c.tag, published, hostVersion{c.version, c.at}, now)
		if at != c.want || exact != c.exact {
			t.Fatalf("%+v: %s exact=%v", c, at, exact)
		}
	}
}

// Requirement: persisted legacy timestamps survive and skipped versions never supply exact latency.
// Risk: reloading history fabricates installs or pending waits pollute successful samples.
func TestDeliveryEvidenceIntegration(t *testing.T) {
	var r contract.DeliveryRelease
	raw := `{"tag":"v1.2.3","target":"m","published":"2026-11-14T11:00:00Z","installed":{"coordinator":"2026-11-14T11:10:00Z"}}`
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	deliveryInstalls(&r, dhState{}, map[string]hostVersion{"coordinator": {"1.2.4", "2026-11-14T11:30:00Z"}}, now)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var loaded contract.DeliveryRelease
	if err = json.Unmarshal(data, &loaded); err != nil {
		t.Fatal(err)
	}
	if loaded.Installed["coordinator"] != "2026-11-14T11:10:00Z" || loaded.InstalledVersions["coordinator"] != "" || loaded.AvailabilityVersions["coordinator"] != "1.2.4" {
		t.Fatalf("history: %+v", loaded)
	}
	prs := []contract.DeliveryPr{{Number: 1, MergeSha: "m", Opened: "2026-11-14T10:00:00Z", Merged: "2026-11-14T10:30:00Z"}}
	deliveryDerive(prs, nil, []contract.DeliveryRelease{loaded})
	if _, ok := prs[0].Durations["install"]; ok {
		t.Fatal("legacy became exact")
	}
	if prs[0].Durations["availability"] != 1800000 {
		t.Fatal(prs[0].Durations)
	}
	prs[0].Available = nil
	series := deliverySeries{}
	deliveryPending(series, prs, "2026-11-14T00:00:00Z", now.Format(time.RFC3339), "2026-11-14T00:00:00Z")
	if len(series["install"]) != 0 || len(series["availability_wait"]) != 1 {
		t.Fatal(series)
	}
	exact := contract.DeliveryRelease{Tag: "v1.2.3", Published: loaded.Published}
	deliveryInstalls(&exact, dhState{}, map[string]hostVersion{"coordinator": {"1.2.3", "2026-11-14T11:30:00Z"}}, now)
	if exact.InstalledVersions["coordinator"] != "1.2.3" {
		t.Fatal(exact)
	}
}

// Requirement: baseline basis affects only exact install populations; refusal is observed, not inferred.
// Risk: old samples silently compare or a missed release is reported as failed.
func TestDeliveryCompatibilityAndRefusal(t *testing.T) {
	r := contract.DeliveryRelease{Tag: "v1.2.3"}
	deliveryUpdateObservations(&r, []doc{{"id": "h", "update": doc{"version": "1.2.3", "state": "refused", "at": "2026-11-14T11:00:00Z", "error": "synthetic refusal"}}})
	if r.UpdateObservations["h"].Version != "1.2.3" || deref(r.UpdateObservations["h"].Error) != "synthetic refusal" {
		t.Fatal(r)
	}
	deliveryUpdateObservations(&r, []doc{{"id": "h", "update": doc{"version": "1.2.4", "state": "ok", "at": "2026-11-14T12:00:00Z"}}})
	if r.UpdateObservations["h"].State != "refused" {
		t.Fatal("refusal overwritten by unrelated version")
	}
	ui := contract.DeliveryRelease{Tag: "ui-1.2.2-v1.2.3", Published: "2026-11-14T10:00:00Z"}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	deliveryInstalls(&ui, dhState{UISeen: map[string]string{"1.2.3": "2026-11-14T11:00:00Z"}}, map[string]hostVersion{"h": {"1.2.3", "2026-11-14T11:00:00Z"}}, now)
	if len(ui.Installed) != 0 || len(ui.Available) != 0 || ui.UiOnSite == nil {
		t.Fatal(ui)
	}
	cur := deliverySeries{"install": {{ms: 100}}, "total": {{ms: 100}}, "queue": {{ms: 100}}}
	base := contract.DeliveryBaseline{Stats: []contract.DeliveryStat{{Key: "install", N: 1, P50Ms: 10, P90Ms: 10}, {Key: "total", N: 1, P50Ms: 10, P90Ms: 10}, {Key: "queue", N: 1, P50Ms: 10, P90Ms: 10}}}
	settings := contract.DeliverySettings{Ratio: 1.5, MinSamples: 1, MinDeltaMs: 1}
	regs := deliveryCompare(nil, cur, base, settings)
	if len(regs) != 2 || regs[0].Key != "queue" {
		t.Fatal(regs)
	}
	base.MetricBasis = ptr("exact-install-v1")
	if got := deliveryCompare(nil, cur, base, settings); len(got) != 6 {
		t.Fatal(got)
	}
	if got := pctl([]int64{10, 20, 30}, .9); got != 30 {
		t.Fatal(got)
	}
}

// Requirement: unavailable changes retain baseline-based alerting without becoming successful installs.
// Risk: separating pending samples silently disables deployment alerts.
func TestDeliveryPendingAvailabilityAlerts(t *testing.T) {
	prs := []contract.DeliveryPr{{Number: 42, Url: "synthetic", Merged: "2026-11-14T10:00:00Z", Released: ptr("2026-11-14T11:00:00Z"), Release: ptr("v1.2.3")}}
	cur := deliverySeries{}
	deliveryPending(cur, prs, "2026-11-14T00:00:00Z", "2026-11-14T12:00:00Z", "2026-11-14T00:00:00Z")
	base := contract.DeliveryBaseline{MetricBasis: ptr("exact-install-v1"), Stats: []contract.DeliveryStat{{Key: "availability", N: 5, P50Ms: 60000, P90Ms: 60000}}}
	regs := deliveryCompare(prs, cur, base, contract.DeliverySettings{Ratio: 1.5, MinSamples: 1, MinDeltaMs: 60000})
	if len(regs) != 2 || regs[0].Key != "availability_wait" || len(cur["install"]) != 0 {
		t.Fatalf("%+v %+v", regs, cur)
	}
}
