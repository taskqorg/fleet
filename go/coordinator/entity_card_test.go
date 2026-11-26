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
	if c, err := cardPullRequest(&Request{}, "taskqorg/other#643"); err != nil || c != nil { // not in the pass; history covers only the fleet repository (tests/go/entity-card.test.ts)
		t.Fatalf("unknown number: %+v %v", c, err)
	}
	for _, bad := range []string{"643", "fleet#643", "a/b/c#1", "taskqorg/fleet#x"} {
		if _, err := cardPullRequest(&Request{}, bad); err == nil {
			t.Fatalf("%s accepted", bad)
		}
	}
}

// Requirement: entity_refs answers the live state of many references in one read (entity-card.md "Live state of many
// references"): each visible one with its request index, kind, code, state and title; a malformed or unknown one is left
// out instead of failing the batch; the same entity twice is read once; more than 200 is refused.
func TestEntityRefs(t *testing.T) {
	deliveryMu.Lock()
	saved := deliveryLast
	deliveryLast = deliveryState{source: contract.DeliverySourceKey, repos: map[string]*deliveryRepo{
		"taskqorg/fleet": {projects: []string{"fleet"}, items: []contract.DeliveryItem{{ProjectId: "fleet", Repo: "taskqorg/fleet", Number: 643, Title: "Code mode",
			Url: "https://github.com/taskqorg/fleet/pull/643", Stage: contract.DeliveryStageMerged, Since: "2026-11-17T10:00:00Z"}}},
	}}
	deliveryMu.Unlock()
	t.Cleanup(func() { deliveryMu.Lock(); deliveryLast = saved; deliveryMu.Unlock() })
	pr := contract.EntityKindPullRequest
	refs := []contract.EntityCardRequest{{Kind: &pr, Id: ptr("taskqorg/fleet#643")}, {Kind: &pr, Id: ptr("bad")}, {Kind: &pr, Id: ptr("taskqorg/other#643")}, {Kind: &pr, Id: ptr("taskqorg~fleet~643")}}
	r, err := entityRefs(&Request{}, &contract.EntityRefsRequest{Refs: refs})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Items) != 2 || r.Items[0].Ref != 0 || r.Items[1].Ref != 3 || *r.Items[0].State != "merged" || *r.Items[0].Code != "#643" || r.Items[0].Title != "Code mode" || r.Items[1].Id != "taskqorg/fleet#643" {
		t.Fatalf("items: %+v", r.Items)
	}
	if _, err := entityRefs(&Request{}, &contract.EntityRefsRequest{Refs: make([]contract.EntityCardRequest, maxEntityRefs+1)}); err == nil {
		t.Fatal("201 references accepted")
	}
}

// Requirement: a result card shows its stored thumbnail or poster, else a small image itself, never another file's bytes.
func TestCardPreview(t *testing.T) {
	for _, c := range []struct {
		media, preview string
		size           int64
		want           string
	}{{"image/png", "", 1000, "self:image"}, {"image/png", "th", 1000, "th:image"}, {"application/pdf", "th", 10, "th:pdf"}, {"application/pdf", "", 10, ""},
		{"video/mp4", "po", 10, "po:video"}, {"image/png", "", previewMaxBytes + 1, ""}, {"image/svg+xml", "", 10, ""}, {"text/plain", "th", 10, ""}} {
		p, got := cardPreview("self", c.media, c.size, c.preview), ""
		if p != nil {
			got = p.ResultId + ":" + string(p.View)
		}
		if got != c.want {
			t.Fatalf("%+v: %q", c, got)
		}
	}
}
