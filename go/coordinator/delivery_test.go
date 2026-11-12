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

// Requirement: a person sees only the delivery data of repositories listed by projects they may see.
// Risk: pull requests, landing counts, errors or samples of another project's private repository reach the page.
func TestDeliveryReplyScoped(t *testing.T) {
	s := func(v string) *string { return &v }
	last := deliveryState{source: contract.DeliverySourceKey, repos: map[string]*deliveryRepo{
		"o/a": {projects: []string{"p1"}, items: []contract.DeliveryItem{{ProjectId: "p1", Repo: "o/a", Number: 1}}, landing: 1, problem: "o/a: slow"},
		"o/b": {projects: []string{"p2", "p3"}, items: []contract.DeliveryItem{{ProjectId: "p2", Repo: "o/b", Number: 2}}, landing: 5, problem: "o/b: 404"},
	}, samples: []contract.DeliverySample{{ProjectId: "p1", Repo: s("o/a")}, {ProjectId: "p2", Repo: s("o/b")}, {ProjectId: "p1"}, {ProjectId: "p2"}}}
	r := deliveryReply(last, func(p string) bool { return p == "p1" })
	if len(r.Items) != 1 || r.Items[0].Number != 1 || r.Landing != 1 || r.Error == nil || *r.Error != "o/a: slow" || len(r.Samples) != 2 {
		t.Fatalf("p1 sees %+v", r)
	}
	for _, x := range r.Samples {
		if x.ProjectId != "p1" {
			t.Fatalf("p1 sees sample %+v", x)
		}
	}
	if r := deliveryReply(last, func(p string) bool { return p == "p3" }); len(r.Items) != 1 || r.Items[0].Number != 2 || r.Landing != 5 {
		t.Fatalf("p3 (lists o/b) sees %+v", r)
	}
	if r := deliveryReply(last, func(string) bool { return false }); len(r.Items)+len(r.Samples) != 0 || r.Landing != 0 || r.Error != nil {
		t.Fatalf("no access sees %+v", r)
	}
}
