package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: a coordinator that starts answers the last delivery pass's pull requests (with their age) before its own
// first pass. Risk: after every release publishing_status, Delivery, Home and Changes show no pull requests for a minute
// (prod 2026-11-26: 10 of 12 reads empty while a release rolled out), and a task's pull request blinks away.
func TestDeliveryStateSurvivesRestart(t *testing.T) {
	e := openTestCoordinator(t, false)
	deliveryMu.Lock()
	saved := deliveryLast
	deliveryMu.Unlock()
	t.Cleanup(func() { deliveryMu.Lock(); deliveryLast = saved; deliveryMu.Unlock() })

	at := "2026-11-26T00:00:00Z"
	pass := deliveryState{source: contract.DeliverySourceKey, refreshedAt: &at, repos: map[string]*deliveryRepo{
		"taskqorg/fleet": {projects: []string{"fleet"}, landing: 3,
			items:     []contract.DeliveryItem{{ProjectId: "fleet", Repo: "taskqorg/fleet", Number: 1101, Stage: contract.DeliveryStagePrCi}},
			delivered: []deliveryMergedPR{{Repo: "taskqorg/fleet", Number: 1099, Body: "Closes T-FLT-66", Delivered: true}}},
	}, samples: []contract.DeliverySample{{ProjectId: "fleet", Repo: ptr("taskqorg/fleet")}}}
	if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error { return deliverySave(q, pass) }); err != nil {
		t.Fatal(err)
	}

	// A new process: nothing read yet.
	deliveryMu.Lock()
	deliveryLast = deliveryState{source: contract.DeliverySourceNone}
	deliveryMu.Unlock()
	deliveryRestore(e.ctx, e.app)

	out, err := deliveryStatus(&Request{}, &contract.DeliveryStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if out.Source != contract.DeliverySourceKey || out.RefreshedAt == nil || *out.RefreshedAt != at || len(out.Items) != 1 || out.Items[0].Number != 1101 {
		t.Fatalf("the restarted coordinator answers the stored pass: %+v", out)
	}
	prs := snapshotDeliveredPRs()
	if len(prs) != 1 || prs[0].Body != "Closes T-FLT-66" || !prs[0].Delivered {
		t.Fatalf("delivered pull requests keep their body and delivery: %+v", prs)
	}

	// A pass that already ran in this process is never replaced by the stored one.
	newer := "2026-11-26T00:05:00Z"
	deliveryMu.Lock()
	deliveryLast = deliveryState{source: contract.DeliverySourcePublic, refreshedAt: &newer}
	deliveryMu.Unlock()
	deliveryRestore(e.ctx, e.app)
	deliveryMu.Lock()
	got := deliveryLast.refreshedAt
	deliveryMu.Unlock()
	if got == nil || *got != newer {
		t.Fatalf("a live pass wins over the stored one: %v", got)
	}
}
