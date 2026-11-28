package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// The card and its batched projection use the same authorized result metadata. An out-of-scope result
// must be absent from the batch and unreachable through the individual card and attachment routes.
func TestEntityResultMetadata(t *testing.T) {
	e := openTestCoordinator(t, false)
	resultHash, thumbnailHash := strings.Repeat("ab", 32), strings.Repeat("ac", 32)
	lifecycleSQL(t, e, `INSERT INTO execution (id, kind, state, project_id) VALUES ('result-run', 'agent_run', 'running', 'desiign')`)
	lifecycleSQL(t, e, `INSERT INTO artifact
		(id, project_id, scope, content_hash, size, media_type, name, state, execution_id, role, derived_from_artifact_id) VALUES
		('result-image', 'desiign', 'prod', $1, 5, 'image/png', 'result.png', 'stored', 'result-run', 'original', NULL),
		('result-thumbnail', 'desiign', 'prod', $2, 5, 'image/png', 'thumb.png', 'stored', 'result-run', 'thumbnail', 'result-image'),
		('result-legacy', 'desiign', 'prod', repeat('ad',32), 5, '', 'legacy', 'stored', 'result-run', 'original', NULL)`, resultHash, thumbnailHash)
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/fleet/desiign/prod/" + resultHash, "/fleet/desiign/prod/" + thumbnailHash:
			_, _ = w.Write([]byte("image"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(store.Close)
	old := storeSecretsDir
	storeSecretsDir = t.TempDir()
	t.Cleanup(func() { storeSecretsDir = old })
	if err := os.WriteFile(filepath.Join(storeSecretsDir, "result-store.key"), []byte(`{"accessKeyId":"test","secretAccessKey":"test"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.registerHost(t, "result-store-host")
	lifecycleSQL(t, e, `INSERT INTO store (id,host_id,bucket,endpoint,state) VALUES ('result-store','result-store-host','fleet',$1,'reachable')`, store.URL)
	lifecycleSQL(t, e, `INSERT INTO artifact_copy (artifact_id,store_id,project_id,state)
		VALUES ('result-image','result-store','desiign','verified'), ('result-thumbnail','result-store','desiign','verified')`)

	member := e.ownerHeaders()
	member["X-Fleet-Person"] = "member@trial.test"
	outsider := e.ownerHeaders()
	outsider["X-Fleet-Person"] = "outsider@trial.test"
	kind := contract.EntityKindResult
	image := contract.EntityCardRequest{Kind: &kind, Id: ptr("result-image")}
	legacy := contract.EntityCardRequest{Kind: &kind, Id: ptr("result-legacy")}
	preview := contract.EntityPreview{ResultId: "result-thumbnail", View: contract.EntityPreviewViewImage}
	var card contract.EntityCardReply
	e.opInto(t, "entity_card", image, member, &card)
	if deref(card.MediaType) != "image/png" || card.Preview == nil || *card.Preview != preview {
		t.Fatalf("card media type %q, preview %v", deref(card.MediaType), card.Preview)
	}
	refs := contract.EntityRefsRequest{Refs: []contract.EntityCardRequest{image, legacy, image}}
	var batch contract.EntityRefsReply
	e.opInto(t, "entity_refs", refs, member, &batch)
	if len(batch.Items) != 3 {
		t.Fatalf("batch items: %+v", batch.Items)
	}
	for _, i := range []int{0, 2} {
		item := batch.Items[i]
		if item.Ref != int64(i) || item.Id != card.Id || deref(item.MediaType) != "image/png" || item.Preview == nil || *item.Preview != preview {
			t.Errorf("batch item %d: %+v", i, item)
		}
	}
	var legacyCard contract.EntityCardReply
	e.opInto(t, "entity_card", legacy, member, &legacyCard)
	if legacyCard.MediaType != nil || legacyCard.Preview != nil || batch.Items[1].Id != "result-legacy" || batch.Items[1].MediaType != nil || batch.Items[1].Preview != nil {
		t.Errorf("legacy result has media metadata: card %+v, ref %+v", legacyCard, batch.Items[1])
	}
	e.opInto(t, "entity_refs", refs, outsider, &batch)
	if len(batch.Items) != 0 {
		t.Errorf("outside the grant: batch items %+v", batch.Items)
	}
	if code, _ := e.callJSON(t, http.MethodPost, "/api/ops/entity_card", image, outsider); code != http.StatusNotFound {
		t.Errorf("outside the grant: entity_card = %d", code)
	}
	for _, id := range []string{"result-image", preview.ResultId} {
		path := "/api/results/" + id
		if code, _ := e.callJSON(t, http.MethodGet, path, nil, member); code != http.StatusOK {
			t.Errorf("member GET %s = %d", path, code)
		}
		if code, _ := e.callJSON(t, http.MethodGet, path, nil, outsider); code != http.StatusNotFound {
			t.Errorf("outside the grant: GET %s = %d", path, code)
		}
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
