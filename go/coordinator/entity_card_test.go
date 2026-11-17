package main

import (
	"encoding/json"
	"testing"

	"fleet/contract"
)

// Requirement: a pull request reference shows its card from the delivery pass (entity-card.md): stage, title, release,
// the GitHub link and the Delivery path, by owner/repo#number or the Delivery list's owner~repo~number.
// Risk: a pull request of a repository only another project lists shows to a person without that project.
func TestCardPullRequest(t *testing.T) {
	deliveryMu.Lock()
	saved := deliveryLast
	deliveryLast = deliveryState{source: contract.DeliverySourceKey, repos: map[string]*deliveryRepo{
		"taskqorg/fleet": {projects: []string{"fleet"}, items: []contract.DeliveryItem{{ProjectId: "fleet", Repo: "taskqorg/fleet", Number: 643, Title: "Code mode",
			Url: "https://github.com/taskqorg/fleet/pull/643", Stage: contract.DeliveryStageMerged, Since: "2026-11-17T10:00:00Z", Release: ptr("v1.2.3")}}},
	}}
	deliveryMu.Unlock()
	t.Cleanup(func() { deliveryMu.Lock(); deliveryLast = saved; deliveryMu.Unlock() })
	for _, id := range []string{"taskqorg/fleet#643", "TaskQorg/Fleet#643", "taskqorg~fleet~643"} {
		c, err := cardPullRequest(&Request{}, id)
		if err != nil || c == nil {
			t.Fatalf("%s: %v %v", id, c, err)
		}
		if *c.Code != "#643" || *c.State != "merged" || *c.Url != "https://github.com/taskqorg/fleet/pull/643" || *c.Path != "/delivery/taskqorg~fleet~643" || c.Id != "taskqorg/fleet#643" {
			t.Fatalf("%s: %+v", id, c)
		}
		if len(c.Facts) != 2 || c.Facts[1].Label != "Release" || c.Facts[1].Value != "v1.2.3" {
			t.Fatalf("%s facts: %+v", id, c.Facts)
		}
	}
	var member Person
	_ = json.Unmarshal([]byte(`{"email":"m@x","roles":["operator"],"grants":[{"kind":"project","id":"other"}]}`), &member)
	if c, err := cardPullRequest(&Request{person: &member}, "taskqorg/fleet#643"); err != nil || c != nil {
		t.Fatalf("a person without the project sees %+v %v", c, err)
	}
	if c, err := cardPullRequest(&Request{}, "taskqorg/fleet#644"); err != nil || c != nil {
		t.Fatalf("unknown number: %+v %v", c, err)
	}
	for _, bad := range []string{"643", "fleet#643", "a/b/c#1", "taskqorg/fleet#x"} {
		if _, err := cardPullRequest(&Request{}, bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}
