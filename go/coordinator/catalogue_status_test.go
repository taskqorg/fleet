package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"fleet/contract"
)

// Requirement: CatalogueMethod and CatalogueMethodView carry optional status, capability, and description across config/catalogue.json, definition validation, and CatalogueReply projection.
// Risk: invalid statuses slip through definition validation, seed methods lose lifecycle statuses, or CatalogueReply drops optional method metadata.
func TestCatalogueSeedStatusesAndProjection(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "catalogue.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := validDefinition("catalogue", "catalogue", raw); err != nil {
		t.Fatalf("shipped catalogue.json failed validDefinition: %v", err)
	}
	var def contract.CatalogueDefinition
	if err := json.Unmarshal(raw, &def); err != nil {
		t.Fatal(err)
	}
	if def.Capability != "web-research" || deref(def.SharedDefault) != "search-basic" {
		t.Fatalf("unexpected catalogue defaults: capability=%q sharedDefault=%v", def.Capability, def.SharedDefault)
	}
	wantStatus := map[string]contract.CatalogueMethodStatus{
		"search-basic":         contract.CatalogueMethodStatusRecommended,
		"coding-task":          contract.CatalogueMethodStatusRecommended,
		"coding-task-reviewed": contract.CatalogueMethodStatusExperimental,
		"meshy-prop":           contract.CatalogueMethodStatusExperimental,
		"character-multiview":  contract.CatalogueMethodStatusExperimental,
		"image-brief":          contract.CatalogueMethodStatusExperimental,
		"lang-check-jev":       contract.CatalogueMethodStatusExperimental,
	}
	seen := map[string]bool{}
	var methodViews []any
	for _, m := range def.Methods {
		seen[m.Id] = true
		if want, ok := wantStatus[m.Id]; ok {
			if m.Status == nil || *m.Status != want {
				t.Errorf("method %q status = %v, want %q", m.Id, m.Status, want)
			}
			if deref(m.Capability) == "" || deref(m.Description) == "" {
				t.Errorf("method %q missing capability (%v) or description (%v)", m.Id, m.Capability, m.Description)
			}
		}
		mv := map[string]any{
			"id":             m.Id,
			"name":           m.Name,
			"scope":          m.Scope,
			"pipeline":       m.Pipeline,
			"assessment":     contract.MethodAssessment{Basis: contract.AssessmentBasisNone, CostBasis: "unavailable"},
			"recommendedFor": []string{},
		}
		if m.Status != nil {
			mv["status"] = *m.Status
		}
		if m.Capability != nil {
			mv["capability"] = *m.Capability
		}
		if m.Description != nil {
			mv["description"] = *m.Description
		}
		methodViews = append(methodViews, mv)
	}
	for id := range wantStatus {
		if !seen[id] {
			t.Errorf("catalogue.json missing seed method %q", id)
		}
	}
	reply, err := replyAs[contract.CatalogueReply](rawObject{
		"projectId":            jsonBytes("hello-world"),
		"capability":           jsonBytes(def.Capability),
		"sharedDefault":        jsonBytes(def.SharedDefault),
		"recommendedMethod":    jsonBytes("search-basic"),
		"recommendationSource": jsonBytes("shared"),
		"methods":              jsonBytes(methodViews),
	}, nil)
	if err != nil {
		t.Fatalf("replyAs CatalogueReply failed: %v", err)
	}
	if err := reply.Validate(); err != nil {
		t.Fatalf("CatalogueReply.Validate failed: %v", err)
	}
	for _, mv := range reply.Methods {
		if want, ok := wantStatus[mv.Id]; ok {
			if mv.Status == nil || *mv.Status != want {
				t.Errorf("CatalogueMethodView %q status = %v, want %q", mv.Id, mv.Status, want)
			}
			if deref(mv.Capability) == "" || deref(mv.Description) == "" {
				t.Errorf("CatalogueMethodView %q missing capability or description", mv.Id)
			}
		}
	}
	bad := `{"capability":"web-research","methods":[{"id":"x","name":"X","scope":"shared","pipeline":"search-basic.json","status":"invalid"}]}`
	if err := validDefinition("catalogue", "catalogue", []byte(bad)); err == nil {
		t.Fatal("expected invalid catalogue method status to fail validation")
	}
}
