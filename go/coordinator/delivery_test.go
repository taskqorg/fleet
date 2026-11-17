package main

import (
	"slices"
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
			{Number: 6, CreatedAt: "2026-11-12T10:00:00Z", Head: struct {
				SHA string `json:"sha"`
			}{"h6"}},
			{Number: 7, CreatedAt: "2026-11-12T10:00:00Z", Head: struct {
				SHA string `json:"sha"`
			}{"h7"}},
			// #8 failed in the queue and was removed; #9 failed in the queue, then got a new head (fixed);
			// #14 failed in the queue but is set to enter it again; #15 was only cancelled there; #16 passed there last;
			// #17 failed in one workflow while another passed later; #18's unchanged head finished a slow check after the failure.
			{Number: 8, CreatedAt: "2026-11-12T10:00:00Z", Head: struct {
				SHA string `json:"sha"`
			}{"h8"}},
			{Number: 9, CreatedAt: "2026-11-12T10:00:00Z", Head: struct {
				SHA string `json:"sha"`
			}{"h9"}},
			{Number: 14, CreatedAt: "2026-11-12T10:00:00Z", AutoMerge: map[string]any{}, Head: struct {
				SHA string `json:"sha"`
			}{"h14"}},
			{Number: 15, CreatedAt: "2026-11-12T10:00:00Z"},
			{Number: 16, CreatedAt: "2026-11-12T10:00:00Z"},
			{Number: 17, CreatedAt: "2026-11-12T10:00:00Z"},
			{Number: 18, CreatedAt: "2026-11-12T10:00:00Z", Head: struct {
				SHA string `json:"sha"`
			}{"h18"}},
			// #19: a new head pushed at 10:00 while a group of the old head ran; that group failed at 10:03.
			{Number: 19, CreatedAt: "2026-11-12T09:00:00Z", Head: struct {
				SHA string `json:"sha"`
			}{"h19"}},
		},
		queueDone: []ghRun{
			{Event: "merge_group", Conclusion: "success", Branch: "gh-readonly-queue/main/pr-8-a", UpdatedAt: "2026-11-12T10:40:00Z"},
			{Event: "merge_group", Conclusion: "failure", Branch: "gh-readonly-queue/main/pr-8-b", UpdatedAt: "2026-11-12T11:15:00Z"},
			{Event: "merge_group", Conclusion: "failure", Branch: "gh-readonly-queue/main/pr-9-a", UpdatedAt: "2026-11-12T10:50:00Z"},
			{Event: "merge_group", Conclusion: "timed_out", Branch: "gh-readonly-queue/main/pr-14-a", UpdatedAt: "2026-11-12T10:50:00Z"},
			{Event: "merge_group", Conclusion: "failure", Branch: "gh-readonly-queue/main/pr-15-a", UpdatedAt: "2026-11-12T10:20:00Z"},
			{Event: "merge_group", Conclusion: "cancelled", Branch: "gh-readonly-queue/main/pr-15-b", UpdatedAt: "2026-11-12T10:30:00Z"},
			{Event: "merge_group", Conclusion: "success", Branch: "gh-readonly-queue/main/pr-15-c", UpdatedAt: "2026-11-12T10:25:00Z"},
			{Event: "merge_group", Conclusion: "failure", Branch: "gh-readonly-queue/main/pr-16-a", UpdatedAt: "2026-11-12T10:20:00Z"},
			{Event: "merge_group", Conclusion: "success", Branch: "gh-readonly-queue/main/pr-16-b", UpdatedAt: "2026-11-12T10:45:00Z"},
			{Event: "merge_group", Conclusion: "failure", Branch: "gh-readonly-queue/main/pr-17-a", UpdatedAt: "2026-11-12T10:40:00Z", Workflow: 1},
			{Event: "merge_group", Conclusion: "success", Branch: "gh-readonly-queue/main/pr-17-a", UpdatedAt: "2026-11-12T10:45:00Z", Workflow: 2},
			{Event: "merge_group", Conclusion: "failure", Branch: "gh-readonly-queue/main/pr-18-a", UpdatedAt: "2026-11-12T11:00:00Z"},
			{Event: "merge_group", Conclusion: "failure", Branch: "gh-readonly-queue/main/pr-19-a", CreatedAt: "2026-11-12T09:50:00Z", UpdatedAt: "2026-11-12T10:03:00Z"},
		},
		// The latest finished run per head and workflow decides: #6 failed after an older success, #7 passed after an older failure.
		finished: []ghRun{
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h6", UpdatedAt: "2026-11-12T10:30:00Z"},
			{Event: "pull_request", Conclusion: "failure", HeadSHA: "h6", UpdatedAt: "2026-11-12T11:30:00Z"},
			// Another workflow passing later on the same head does not hide the failure.
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h6", UpdatedAt: "2026-11-12T11:40:00Z", Workflow: 2},
			{Event: "pull_request", Conclusion: "failure", HeadSHA: "h7", UpdatedAt: "2026-11-12T10:30:00Z"},
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h7", UpdatedAt: "2026-11-12T11:30:00Z"},
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h8", CreatedAt: "2026-11-12T10:20:00Z", UpdatedAt: "2026-11-12T10:30:00Z"},
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h9", CreatedAt: "2026-11-12T11:00:00Z", UpdatedAt: "2026-11-12T11:05:00Z"},
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h18", CreatedAt: "2026-11-12T10:05:00Z", UpdatedAt: "2026-11-12T11:30:00Z"},
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h14", CreatedAt: "2026-11-12T10:05:00Z", UpdatedAt: "2026-11-12T10:20:00Z"},
			{Event: "pull_request", Conclusion: "success", HeadSHA: "h19", CreatedAt: "2026-11-12T10:00:00Z", UpdatedAt: "2026-11-12T10:10:00Z"},
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
			// #20 and #21: UI-only, released as ui-<backend>-v<UI version>; the site serves UI 1.2.12, so #20's release is on
			// it and #21's newer one is not yet; #22 waits for a release.
			{Number: 20, MergedAt: s("2026-11-12T11:20:00Z"), MergeSHA: "m20"},
			{Number: 21, MergedAt: s("2026-11-12T11:30:00Z"), MergeSHA: "m21"},
			{Number: 22, MergedAt: s("2026-11-12T11:40:00Z"), MergeSHA: "m22"},
		},
		releases: []ghRelease{
			{Tag: "v1.2.10", Target: "m11", Published: "2026-11-12T10:10:00Z"},
			{Tag: "v1.2.9", Target: "m10", Published: "2026-11-12T09:10:00Z"},
			{Tag: "v1.2.11", Target: "m12", Published: "2026-11-12T11:10:00Z"},
			{Tag: "ui-1.2.11-v1.2.12", Target: "m20", Published: "2026-11-12T11:25:00Z"},
			{Tag: "ui-1.2.11-v1.2.13", Target: "m21", Published: "2026-11-12T11:35:00Z"},
			{Tag: "ui-ts-v1.2.14", Target: "m22", Published: "2026-11-12T11:45:00Z"}, // not ui-<backend>-v<UI version>: no release
		},
	}
	// Another repository's ui-* tags are no Fleet UI releases: its merges wait for a v* release.
	other, _ := deliveryStages("p", "o/r", g, []string{"1.2.9", "1.2.10"}, "1.2.12", now)
	for _, it := range other {
		if it.Number >= 20 && (it.Stage != "releasing" || it.Release != nil) {
			t.Errorf("o/r #%d: %s %v", it.Number, it.Stage, deref(it.Release))
		}
	}
	items, landing := deliveryStages("fleet", "taskqorg/fleet", g, []string{"1.2.9", "1.2.10"}, "1.2.12", now)
	want := map[int64]contract.DeliveryStage{1: "pr_open", 2: "pr_ci", 3: "mq_wait", 4: "mq_ci", 5: "mq_wait", 6: "pr_failed", 7: "pr_open", 8: "mq_failed", 9: "pr_open",
		14: "mq_wait", 15: "pr_open", 16: "pr_open", 17: "mq_failed", 18: "mq_failed", 19: "pr_open", 10: "installed", 11: "installing", 12: "installing",
		20: "installed", 21: "installing", 22: "releasing"}
	rel := map[int64]string{10: "v1.2.9", 11: "v1.2.10", 12: "v1.2.11", 20: "ui-1.2.11-v1.2.12", 21: "ui-1.2.11-v1.2.13"}
	if len(items) != len(want) {
		t.Fatalf("items = %+v", items)
	}
	for _, it := range items {
		if want[it.Number] != it.Stage {
			t.Errorf("#%d stage %s, want %s", it.Number, it.Stage, want[it.Number])
		}
		if deref(it.Release) != rel[it.Number] {
			t.Errorf("#%d release %q, want %q", it.Number, deref(it.Release), rel[it.Number])
		}
		if it.Number == 6 && it.Since != "2026-11-12T11:30:00Z" || it.Number == 8 && it.Since != "2026-11-12T11:15:00Z" || it.Number == 14 && it.Since != "2026-11-12T10:50:00Z" || it.Number == 5 && it.Since != "2026-11-12T10:00:00Z" {
			t.Errorf("#%d failed since %s", it.Number, it.Since)
		}
		if it.Number < 10 && deref(it.Opened) != "2026-11-12T10:00:00Z" && it.Number != 19 || it.Number == 12 && deref(it.Merged) != "2026-11-12T11:00:00Z" {
			t.Errorf("#%d opened %v merged %v", it.Number, deref(it.Opened), deref(it.Merged))
		}
	}
	if landing != 4 {
		t.Errorf("landing = %d", landing)
	}
	if !versionLess("1.261112.9", "1.261112.31") || versionLess("1.261112.31", "1.261112.31") {
		t.Error("versionLess")
	}
}

// Requirement: a UI-only release the live read of the release list missed (GitHub sorts a ui-* tag among older native
// ones, past the page read) still delivers its pull request, from the release the history recorded.
// Risk: #626 matched to a later native release (or none) although the site served its UI release minutes after merge.
func TestDeliveryRecordedUIRelease(t *testing.T) {
	s := func(v string) *string { return &v }
	now, _ := time.Parse(time.RFC3339, "2026-11-17T18:20:00Z")
	read := []ghRelease{{Tag: "v1.261117.28", Target: "m627", Published: "2026-11-17T16:33:37Z"}}
	ui := ghRelease{Tag: "ui-1.261117.26-v1.261117.27", Target: "m626", Published: "2026-11-17T16:09:46Z"}
	g := ghRepo{closed: []ghPull{{Number: 626, MergedAt: s("2026-11-17T16:08:57Z"), MergeSHA: "m626"}, {Number: 627, MergedAt: s("2026-11-17T16:30:00Z"), MergeSHA: "m627"}},
		releases: withReleases(read, []ghRelease{ui, read[0]})}
	if len(g.releases) != 2 {
		t.Fatalf("a recorded release the read has is not added twice: %+v", g.releases)
	}
	items, _ := deliveryStages("fleet", githubPublicRepo, g, []string{"1.261117.28"}, "1.261117.28", now)
	i := slices.IndexFunc(items, func(it contract.DeliveryItem) bool { return it.Number == 626 })
	if i < 0 || items[i].Stage != "installed" || deref(items[i].Release) != ui.Tag {
		t.Fatalf("#626: %+v", items)
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

// Requirement: OR-34 ignore is a server-side exclusion: delivery_status leaves out repositories listed only by excluded
// projects, keeps one another listed project still shows, and applies after projectIds.
// Risk: an ignored project's pull requests stay on Delivery with All projects, or a repository shared with a shown project vanishes.
func TestDeliveryStatusExclude(t *testing.T) {
	deliveryMu.Lock()
	saved := deliveryLast
	deliveryLast = deliveryState{source: contract.DeliverySourceKey, repos: map[string]*deliveryRepo{
		"o/a": {projects: []string{"p1"}, items: []contract.DeliveryItem{{ProjectId: "p1", Repo: "o/a", Number: 1}}},
		"o/b": {projects: []string{"p2", "p3"}, items: []contract.DeliveryItem{{ProjectId: "p2", Repo: "o/b", Number: 2}}},
	}, samples: []contract.DeliverySample{{ProjectId: "p2", Repo: ptr("o/b")}}}
	deliveryMu.Unlock()
	t.Cleanup(func() { deliveryMu.Lock(); deliveryLast = saved; deliveryMu.Unlock() })
	numbers := func(r contract.DeliveryStatusRequest) []int64 {
		out, err := deliveryStatus(&Request{}, &r)
		if err != nil {
			t.Fatal(err)
		}
		ns := []int64{}
		for _, it := range out.Items {
			ns = append(ns, it.Number)
		}
		slices.Sort(ns)
		return ns
	}
	for _, c := range []struct {
		r    contract.DeliveryStatusRequest
		want []int64
	}{
		{contract.DeliveryStatusRequest{}, []int64{1, 2}},
		{contract.DeliveryStatusRequest{ExcludeProjectIds: []string{"p1"}}, []int64{2}},
		{contract.DeliveryStatusRequest{ExcludeProjectIds: []string{"p2"}}, []int64{1, 2}}, // p3 lists o/b too
		{contract.DeliveryStatusRequest{ExcludeProjectIds: []string{"p2", "p3", "-"}}, []int64{1}},
		{contract.DeliveryStatusRequest{ProjectIds: []string{"p1", "p2"}, ExcludeProjectIds: []string{"p1"}}, []int64{2}},
	} {
		if got := numbers(c.r); !slices.Equal(got, c.want) {
			t.Errorf("%+v: %v, want %v", c.r, got, c.want)
		}
	}
	// o/b's items name p2; with p2 left out they name p3, which lists o/b too, so no row shows the ignored project.
	out, _ := deliveryStatus(&Request{}, &contract.DeliveryStatusRequest{ExcludeProjectIds: []string{"p2"}})
	for _, it := range out.Items {
		if it.ProjectId == "p2" {
			t.Fatalf("an item names the excluded project: %+v", it)
		}
	}
	for _, s := range out.Samples {
		if s.ProjectId == "p2" {
			t.Fatalf("a sample names the excluded project: %+v", s)
		}
	}
}

// Requirement: a delivery pass that changes a project's pull requests reaches watchers through the change stream.
// Risk: the Delivery page keeps showing old rows until its slow poll, because the trigger drops delivery documents.
func TestDeliveryStateChange(t *testing.T) {
	env := openTestCoordinator(t, false)
	var kind string
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if err := exec(q, `INSERT INTO document(kind,id,project_id,data) VALUES('delivery-state','delivery-state:fleet','fleet','{"refreshedAt":"x"}')`); err != nil {
			return err
		}
		return q.tx.QueryRow(env.ctx, "SELECT kind FROM change WHERE id='delivery-state:fleet' ORDER BY seq DESC LIMIT 1").Scan(&kind)
	}); err != nil {
		t.Fatal(err)
	}
	if kind != "delivery" {
		t.Fatalf("change kind %q", kind)
	}
}
