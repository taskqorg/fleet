package main

import (
	"encoding/json"
	"testing"
	"time"

	"fleet/coordinator/db"
)

// Requirement: the installed coordinator and every required executor cover the release's own target.
// Risk: counting only release tags strands coordinator-only work, while borrowing a different release's
// pacing marker can credit a host that is still waiting for an executor change.
func TestNativeTaskRolloutPacing(t *testing.T) {
	const target = "1.261127.62"
	const mergeSHA = "072cc77c006ddf3674ebfca96130ddb740c547f6"
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	repo := ghRepo{
		closed:   []ghPull{{Number: 1336, MergedAt: ptr("2026-11-27T23:35:55Z"), MergeSHA: mergeSHA}},
		releases: []ghRelease{{Tag: "v" + target, Target: mergeSHA, Published: "2026-11-27T23:39:27Z"}},
	}
	for _, tc := range []struct {
		name, coordinator, manifest, changedIn, executor string
		unknown, unhealthy, want, wantDisplay            bool
	}{
		{name: "coordinator only", coordinator: target, manifest: target, changedIn: "1.261127.61", executor: "1.261127.61", want: true, wantDisplay: true},
		{name: "newer coordinator with same host part", coordinator: "1.261127.63", manifest: "1.261127.63", changedIn: "1.261127.61", executor: "1.261127.61", want: true, wantDisplay: true},
		{name: "changed executor still deferred", coordinator: target, manifest: target, changedIn: target, executor: "1.261127.61"},
		{name: "changed executor installed", coordinator: target, manifest: target, changedIn: target, executor: target, want: true, wantDisplay: true},
		{name: "coordinator behind", coordinator: "1.261127.61", manifest: "1.261127.61", changedIn: "1.261127.61", executor: target, wantDisplay: true},
		{name: "historical host part unproved", coordinator: "1.261127.63", manifest: "1.261127.63", changedIn: "1.261127.63", executor: "1.261127.61"},
		{name: "unmatched manifest", coordinator: target, manifest: "1.261127.61", changedIn: "1.261127.61", executor: "1.261127.61"},
		{name: "legacy manifest", coordinator: target, manifest: target, executor: "1.261127.61"},
		{name: "no executor observation", coordinator: target, manifest: target, changedIn: "1.261127.61"},
		{name: "unknown required host", coordinator: target, manifest: target, changedIn: "1.261127.61", executor: "1.261127.61", unknown: true, wantDisplay: true},
		{name: "coordinator still in health check", coordinator: target, manifest: target, changedIn: "1.261127.61", executor: "1.261127.61", unhealthy: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var rel releaseInfo
			rel.Version, rel.Executor.ChangedIn = tc.manifest, tc.changedIn
			rollout := taskDeliveryRollout{release: rel, coordinator: tc.coordinator, coordinatorHealthy: !tc.unhealthy, unknown: tc.unknown}
			if tc.executor != "" {
				rollout.hasHostObservation = true
				rollout.liveVersions = []string{tc.executor}
				rollout.hosts = []taskDeliveryHostProof{{Target: "executor", Version: tc.executor}}
			}
			proof := rollout.installation("v" + target)
			if (len(proof) != 0) != tc.want {
				t.Fatalf("installation = %+v, want proven = %v", proof, tc.want)
			}
			items, _, merged := deliveryStagesWithMerged("fleet", githubPublicRepo, repo, rollout, now)
			if len(items) != 1 || len(merged) != 1 || (items[0].Stage == "installed") != tc.wantDisplay ||
				merged[0].Delivered != tc.want || merged[0].Release != "v"+target || merged[0].ReleaseTarget != mergeSHA {
				t.Fatalf("stage or selected release disagrees with rollout: items=%+v merged=%+v", items, merged)
			}
			if !tc.want {
				return
			}
			raw, err := json.Marshal(deliveryMergedPR{Release: "v" + target, InstalledOn: proof})
			if err != nil {
				t.Fatal(err)
			}
			var previous deliveryMergedPR
			if err := json.Unmarshal(raw, &previous); err != nil {
				t.Fatal(err)
			}
			if previous.InstalledOn[1].Version != tc.executor || confirmedRolloutRollback(previous, rollout.hosts) {
				t.Fatalf("fresh proof changed the observed version or was treated as a rollback: %+v", previous.InstalledOn)
			}
			if !confirmedRolloutRollback(previous, []taskDeliveryHostProof{{Target: "executor", Version: "1.261127.60"}}) {
				t.Fatal("a rollback below the executor floor must invalidate the retained proof")
			}
			if confirmedRolloutRollback(previous, []taskDeliveryHostProof{{Target: "executor", Version: "dev"}}) {
				t.Fatal("an unknown release version cannot confirm rollback")
			}
			if !confirmedRolloutRollback(previous, []taskDeliveryHostProof{{Target: "coordinator", Version: "1.261127.61"}}) {
				t.Fatal("the coordinator still has to meet the full release requirement")
			}
		})
	}
}

// Requirement: deliberately excluded executors do not block durable proof. An expired hold, stale required executor,
// or versionless required executor must not silently drop that executor from the rollout.
func TestNativeTaskRolloutHostEligibility(t *testing.T) {
	const target = "1.261127.62"
	now := time.Date(2026, 11, 28, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, auto, state, version string
		age                        time.Duration
		want                       bool
	}{
		{name: "off", auto: "off", version: "1.261127.61", want: true},
		{name: "held", auto: "2026-11-28T01:00:00Z", version: "1.261127.61", want: true},
		{name: "hold expired", auto: "2026-11-27T23:00:00Z", version: "1.261127.61"},
		{name: "removed", auto: "on", state: "removed", version: "1.261127.61", want: true},
		{name: "stale", auto: "on", version: target, age: 2 * time.Hour},
		{name: "versionless", auto: "on"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rollout := taskDeliveryRollout{coordinator: target, coordinatorHealthy: true}
			// Include a current required host when checking that a removed host contributes no evidence or veto.
			if tc.state == "removed" {
				rollout.observeExecutor(hostRecord{ID: "current"}, db.DeliveryHostsSeenRow{ID: "current", Version: target, LastSeenMs: float64(now.UnixMilli())}, 0, now)
			}
			host := hostRecord{ID: "other", State: tc.state, hostPayload: hostPayload{AutoUpdate: jsonBytes(tc.auto)}}
			seen := float64(now.Add(-tc.age).UnixMilli())
			rollout.observeExecutor(host, db.DeliveryHostsSeenRow{ID: host.ID, Version: tc.version, LastSeenMs: seen}, tc.age, now)
			if proof := rollout.installation("v" + target); (len(proof) != 0) != tc.want {
				t.Fatalf("installation = %+v, want proven = %v", proof, tc.want)
			}
			if tc.want && rollout.displayedInstallation("v"+target) {
				t.Fatal("a live older host counts toward display independently of durable proof")
			}
		})
	}
}
