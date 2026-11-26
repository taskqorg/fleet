package main

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
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
	deliverySiteEvidenceOf([]db.DeliverySiteObservation{siteSighting("2026-11-14T10:58:00Z", "2026-11-14T11:30:00Z", "2026-11-14T11:30:00Z", "1.2.4", "1.2.4")}, nil).reconcile(&r)
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
	deliverySiteEvidenceOf([]db.DeliverySiteObservation{siteSighting("2026-11-14T10:58:00Z", "2026-11-14T11:30:00Z", "2026-11-14T11:30:00Z", "1.2.3", "1.2.3")}, nil).reconcile(&exact)
	if exact.InstalledVersions["coordinator"] != "1.2.3" {
		t.Fatal(exact)
	}
}

// Requirement: baseline basis affects only exact install populations; refusal is observed, not inferred.
// Risk: old samples silently compare or a missed release is reported as failed.
func TestDeliveryCompatibilityAndRefusal(t *testing.T) {
	r := contract.DeliveryRelease{Tag: "v1.2.3"}
	deliveryUpdateObservations(&r, []db.DeliveryHostsSeenRow{{ID: "h", Update: jsonBytes(doc{"version": "1.2.3", "state": "refused", "at": "2026-11-14T11:00:00Z", "error": "synthetic refusal"})}})
	if r.UpdateObservations["h"].Version != "1.2.3" || deref(r.UpdateObservations["h"].Error) != "synthetic refusal" {
		t.Fatal(r)
	}
	deliveryUpdateObservations(&r, []db.DeliveryHostsSeenRow{{ID: "h", Update: jsonBytes(doc{"version": "1.2.4", "state": "ok", "at": "2026-11-14T12:00:00Z"})}})
	if r.UpdateObservations["h"].State != "refused" {
		t.Fatal("refusal overwritten by unrelated version")
	}
	ui := contract.DeliveryRelease{Tag: "ui-1.2.2-v1.2.3", Published: "2026-11-14T10:00:00Z"}
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	deliveryInstalls(&ui, "2026-11-14T10:00:00Z", map[string]hostVersion{"h": {"1.2.3", "2026-11-14T11:00:00Z"}}, now)
	deliverySiteEvidenceOf(nil, map[string]string{"1.2.3": "2026-11-14T11:00:00Z"}).reconcile(&ui)
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
	// exact-install-v1 measured install as now, but its total left UI-only releases out: total compares from delivered-v2 on.
	base.MetricBasis = ptr("exact-install-v1")
	if got := deliveryCompare(nil, cur, base, settings); len(got) != 4 || slices.ContainsFunc(got, func(r contract.DeliveryRegression) bool { return r.Key == "total" }) {
		t.Fatal(got)
	}
	base.MetricBasis = ptr(deliveryBasis)
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

// Requirement: a pull request a UI-only release (ui-<backend>-v<UI version>) delivered is matched to it and done when the
// site serves its UI: no install stage, total from opened to UI on site; while the UI is not on the site the wait counts
// against the ui stage. Re-deriving stored records (as every pass does) gives records stored before this rule the same.
// Risk: a UI-only pull request stays "releasing" or "installing" forever, never measured, or red past an install norm.
func TestDeliveryUIOnlyRelease(t *testing.T) {
	prs := []contract.DeliveryPr{
		// As stored before UI-only releases counted: release and UI on site known, no total.
		{Number: 626, MergeSha: "m626", Opened: "2026-11-17T16:01:47Z", Merged: "2026-11-17T16:08:57Z", Durations: map[string]int64{"ui": 153000}},
		{Number: 627, MergeSha: "m627", Opened: "2026-11-17T16:20:00Z", Merged: "2026-11-17T16:30:00Z"},
	}
	rels := []contract.DeliveryRelease{
		{Tag: "ui-1.261117.26-v1.261117.27", Target: "m626", Published: "2026-11-17T16:09:46Z", UiOnSite: ptr("2026-11-17T16:12:19Z"), Installed: map[string]string{}},
		{Tag: "ui-1.261117.26-v1.261117.28", Target: "m627", Published: "2026-11-17T16:31:00Z", Installed: map[string]string{}},
	}
	deliveryDerive(prs, nil, rels)
	p := prs[0]
	if deref(p.Release) != "ui-1.261117.26-v1.261117.27" || releaseVersionOf(deref(p.Release)) != "1.261117.27" || p.Durations["release"] != 49000 {
		t.Fatalf("matched release: %+v", p)
	}
	if p.Durations["total"] != 632000 || p.Durations["ui_install"] != 153000 { // 16:01:47 to 16:12:19
		t.Fatalf("total is opened to UI on site: %+v", p.Durations)
	}
	// ui is native releases' UI on site only; a UI-only one's is ui_install.
	for _, k := range []string{"install", "availability", "time_to_available", "ui"} {
		if _, ok := p.Durations[k]; ok {
			t.Fatalf("a UI-only release has no %s: %+v", k, p.Durations)
		}
	}
	if _, ok := prs[1].Durations["total"]; ok {
		t.Fatalf("not on the site yet: %+v", prs[1].Durations)
	}
	cur := deliverySeries{}
	deliveryPending(cur, prs, "2026-11-17T00:00:00Z", "2026-11-17T17:31:00Z", "2026-11-12T00:00:00Z")
	if len(cur["ui_wait"]) != 1 || cur["ui_wait"][0].pr != 627 || cur["ui_wait"][0].ms != 3600e3 || len(cur["availability_wait"]) != 0 {
		t.Fatalf("pending: %+v", cur)
	}
	// Against UI-only installs (ui_install), not ui, which native releases' coordinator restarts dominate.
	base := contract.DeliveryBaseline{MetricBasis: ptr(deliveryBasis), Stats: []contract.DeliveryStat{{Key: "ui", N: 5, P50Ms: 3600e3, P90Ms: 7200e3}, {Key: "ui_install", N: 5, P50Ms: 150000, P90Ms: 200000}}}
	regs := deliveryCompare(prs, cur, base, contract.DeliverySettings{Ratio: 1.5, MinSamples: 1, MinDeltaMs: 60000})
	if len(regs) != 2 || regs[0].Key != "ui_wait" || regs[0].BaselineMs != 150000 {
		t.Fatalf("a stalled UI release regresses against UI-only installs: %+v", regs)
	}
	// An exact UI-only identity keeps the clock-skew allowance. Same-or-newer
	// evidence must cover publication, including when the version was already served.
	const published = "2026-11-17T17:00:01Z"
	for _, tc := range []struct {
		name         string
		observations []db.DeliverySiteObservation
		legacy       map[string]string
		want         string
	}{
		{"exact legacy clock skew", nil, map[string]string{"1.261117.29": "2026-11-17T16:50:00Z"}, published},
		{"stale newer sighting", []db.DeliverySiteObservation{siteSighting("2026-11-17T16:50:00Z", "2026-11-17T16:50:00Z", "2026-11-17T16:50:00Z", "1.261117.26", "1.261117.30")}, nil, ""},
		{"near publication clock skew", []db.DeliverySiteObservation{siteSighting("2026-11-17T16:50:00Z", "2026-11-17T17:00:00Z", "2026-11-17T17:00:00Z", "1.261117.26", "1.261117.30")}, nil, published},
		{"already serving newer UI", []db.DeliverySiteObservation{siteSighting("2026-11-17T16:50:00Z", "2026-11-17T16:50:00Z", "2026-11-17T17:04:00Z", "1.261117.26", "1.261117.30")}, nil, published},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := contract.DeliveryRelease{Tag: "ui-1.261117.26-v1.261117.29", Published: published}
			deliverySiteEvidenceOf(tc.observations, tc.legacy).reconcile(&r)
			if deref(r.UiOnSite) != tc.want {
				t.Fatalf("UI on site = %q, want %q", deref(r.UiOnSite), tc.want)
			}
		})
	}
	for tag, want := range map[string]bool{"ui-1.261117.26-v1.261117.27": true, "ui-kit-1.0": false, "ui-v2.0.0": false, "v1.2.3": false} {
		if uiOnlyRelease(tag) != want || deliveryReleaseTag(tag) != (want || tag[0] == 'v') {
			t.Errorf("%s: uiOnly %v", tag, uiOnlyRelease(tag))
		}
	}
}

func siteSighting(covered, first, last, coordinator, ui string) db.DeliverySiteObservation {
	parse := func(s string) time.Time {
		at, err := time.Parse(time.RFC3339, s)
		if err != nil {
			panic(err)
		}
		return at
	}
	return db.DeliverySiteObservation{CoveredFrom: ptr(parse(covered)), FirstSeen: parse(first), LastSeen: parse(last), CoordinatorVersion: coordinator, UiVersion: ui}
}

// Requirement: recover times from retained version sightings even when GitHub
// history was unavailable during several deployments. A genuine observation
// gap cannot provide a success or a pending wait for the missing interval.
func TestDeliverySiteRecovery(t *testing.T) {
	const covered = "2026-11-26T08:05:00Z"
	observations := []db.DeliverySiteObservation{
		siteSighting("2026-11-26T08:00:00Z", "2026-11-26T08:00:00Z", "2026-11-26T08:01:00Z", "1.261126.10", "1.261126.10"),
		siteSighting(covered, covered, "2026-11-26T10:00:00Z", "1.261126.12", "1.261126.12"),
		siteSighting(covered, "2026-11-26T10:01:00Z", "2026-11-26T10:01:00Z", "1.261126.14", "1.261126.14"),
		siteSighting(covered, "2026-11-26T10:02:00Z", "2026-11-26T10:02:00Z", "1.261126.14", "1.261126.15"),
		siteSighting(covered, "2026-11-26T10:03:00Z", "2026-11-26T10:05:00Z", "1.261126.16", "1.261126.16"),
	}
	site := deliverySiteEvidenceOf(observations, nil)
	rels := []contract.DeliveryRelease{
		{Tag: "v1.261126.11", Published: "2026-11-26T08:04:00Z"},
		{Tag: "v1.261126.13", Published: "2026-11-26T08:06:00Z"},
		{Tag: "v1.261126.14", Published: "2026-11-26T10:00:15Z", Available: map[string]string{"coordinator": "2026-11-26T10:03:00Z"}, AvailabilityVersions: map[string]string{"coordinator": "1.261126.16"}, UiOnSite: ptr("2026-11-26T10:03:00Z")},
		{Tag: "ui-1.261126.14-v1.261126.15", Published: "2026-11-26T10:01:30Z"},
		{Tag: "v1.261126.17", Published: "2026-11-26T10:04:00Z"},
	}
	prs := make([]contract.DeliveryPr, len(rels))
	for i := range rels {
		r := &rels[i]
		pub, _ := time.Parse(time.RFC3339, r.Published)
		r.Target = r.Tag
		prs[i] = contract.DeliveryPr{Number: int64(i + 1), MergeSha: r.Target, Opened: pub.Add(-time.Minute).Format(time.RFC3339), Merged: pub.Add(-time.Second).Format(time.RFC3339)}
		site.reconcile(r)
	}
	deliveryDerive(prs, nil, rels)
	if rels[0].UiOnSite != nil || rels[0].Available["coordinator"] != "" {
		t.Fatalf("missing coverage became a successful observation: %+v", rels[0])
	}
	if prs[1].Durations["availability"] != 6_900_000 || rels[1].Installed["coordinator"] != "" || rels[1].AvailabilityVersions["coordinator"] != "1.261126.14" {
		t.Fatalf("late qualifying release skipped the exact install incorrectly: %+v", prs[1])
	}
	if prs[2].Durations["availability"] != 45_000 || prs[2].Durations["install"] != 45_000 || prs[2].Durations["ui"] != 45_000 {
		t.Fatalf("earlier retained evidence did not replace later derived times: %+v", prs[2])
	}
	if prs[3].Durations["ui_install"] != 30_000 || prs[3].Durations["total"] != 90_000 || len(prs[3].Available) != 0 {
		t.Fatalf("UI-only release did not retain its own clock: %+v", prs[3])
	}
	from, until := "2026-11-26T08:00:00Z", "2026-11-26T10:05:00Z"
	series := deliveryValues(prs, nil, from, until)
	last := observations[len(observations)-1]
	deliveryPending(series, prs, from, until, site.pendingFrom(last.LastSeen, from))
	if len(series["availability"]) != 2 || len(series["availability_wait"]) != 1 || series["availability_wait"][0].pr != 5 || len(series["ui_wait"]) != 0 {
		t.Fatalf("regression inputs include the gap or omit the pending release: %+v", series)
	}
	if from := site.pendingFrom(last.LastSeen.Add(3*time.Minute), covered); from != "" {
		t.Fatalf("stale collector still asserts pending since %s", from)
	}
}

// Requirement: historical reconciliation preserves observed exact installs and
// UI sightings while discarding ambiguous same-or-newer times across the outage.
func TestDeliveryHistoricalObservationGap(t *testing.T) {
	gap := siteSighting("2026-11-25T18:00:00Z", "2026-11-25T18:00:00Z", "2026-11-26T04:20:00Z", "", "")
	gap.CoveredFrom = nil
	const firstSeen = "2026-11-26T04:00:00Z"
	for _, cv := range []string{"1.261125.116", "1.261126.9"} {
		r := contract.DeliveryRelease{
			Tag: "v" + cv, Published: "2026-11-25T18:10:00Z",
			Installed: map[string]string{"coordinator": firstSeen},
			Available: map[string]string{"coordinator": firstSeen}, AvailabilityVersions: map[string]string{"coordinator": "1.261126.9"},
			UiOnSite: ptr(firstSeen),
		}
		if cv == "1.261126.9" {
			r.Published = "2026-11-26T03:59:12Z"
			r.InstalledVersions = map[string]string{"coordinator": cv}
		}
		deliverySiteEvidenceOf([]db.DeliverySiteObservation{gap}, map[string]string{"1.261126.9": firstSeen}).reconcile(&r)
		if r.Installed["coordinator"] != firstSeen {
			t.Fatalf("recorded install timestamp changed: %+v", r)
		}
		if cv == "1.261125.116" && (r.Available["coordinator"] != "" || r.UiOnSite != nil || r.InstalledVersions["coordinator"] != "") {
			t.Fatalf("unqualified history became a sample: %+v", r)
		}
		if cv == "1.261126.9" && (r.Available["coordinator"] != firstSeen || deref(r.UiOnSite) != firstSeen || r.InstalledVersions["coordinator"] != cv) {
			t.Fatalf("exact version history was lost: %+v", r)
		}
	}
}
