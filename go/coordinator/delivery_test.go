package main

import (
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: each pull request shows in the one stage the delivery scripts would put it in.
// Risk: a queued merge-group run, a release boundary or an older host version moves a pull request to the wrong stage.
func TestDeliveryStages(t *testing.T) {
	s := func(v string) *string { return &v }
	now, _ := time.Parse(time.RFC3339, "2026-11-12T12:00:00Z")
	g := ghRepo{
		open: []ghPull{
			{Number: 1, CreatedAt: "2026-11-12T10:00:00Z"},
			{Number: 2, CreatedAt: "2026-11-12T10:00:00Z", Head: struct {
				SHA string `json:"sha"`
			}{"h2"}},
			{Number: 3, CreatedAt: "2026-11-12T10:00:00Z"},
			{Number: 4, CreatedAt: "2026-11-12T10:00:00Z"},
			{Number: 5, CreatedAt: "2026-11-12T10:00:00Z", AutoMerge: map[string]any{}},
		},
		runs: []ghRun{
			{Event: "pull_request", Status: "in_progress", HeadSHA: "h2", CreatedAt: "2026-11-12T11:00:00Z"},
			{Event: "merge_group", Status: "queued", Branch: "gh-readonly-queue/main/pr-3-abc", CreatedAt: "2026-11-12T11:10:00Z"},
			{Event: "merge_group", Status: "queued", Branch: "gh-readonly-queue/main/pr-4-abc", CreatedAt: "2026-11-12T11:10:00Z"},
			{Event: "merge_group", Status: "in_progress", Branch: "gh-readonly-queue/main/pr-4-abc", CreatedAt: "2026-11-12T11:20:00Z"},
		},
		closed: []ghPull{
			{Number: 10, MergedAt: s("2026-11-12T09:00:00Z"), MergeSHA: "m10"},
			{Number: 11, MergedAt: s("2026-11-12T10:00:00Z"), MergeSHA: "m11"},
			{Number: 12, MergedAt: s("2026-11-12T11:00:00Z"), MergeSHA: "m12"},
			{Number: 13},
		},
		releases: []ghRelease{
			{Tag: "v1.2.10", Target: "m11", Published: "2026-11-12T10:10:00Z"},
			{Tag: "v1.2.9", Target: "m10", Published: "2026-11-12T09:10:00Z"},
			{Tag: "ui-ts-v1.2.11", Target: "m12", Published: "2026-11-12T11:10:00Z"},
		},
	}
	items, landing := deliveryStages("fleet", "o/r", g, []string{"1.2.9", "1.2.10"}, now)
	want := map[int64]contract.DeliveryStage{1: "pr_open", 2: "pr_ci", 3: "mq_wait", 4: "mq_ci", 5: "mq_wait", 10: "installed", 11: "installing", 12: "releasing"}
	if len(items) != len(want) {
		t.Fatalf("items = %+v", items)
	}
	for _, it := range items {
		if want[it.Number] != it.Stage {
			t.Errorf("#%d stage %s, want %s", it.Number, it.Stage, want[it.Number])
		}
	}
	if landing != 3 {
		t.Errorf("landing = %d", landing)
	}
	if !versionLess("1.261112.9", "1.261112.31") || versionLess("1.261112.31", "1.261112.31") {
		t.Error("versionLess")
	}
}
