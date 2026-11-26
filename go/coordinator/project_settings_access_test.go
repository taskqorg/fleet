package main

import (
	"encoding/json"
	"testing"
)

// Requirement: the project settings the approval card and project access read (data, risks, approvals) survive
// parsing; risk (2026-11-26, DAM pilot): projectSettings kept only its known keys, so settings.data never reached the
// access card (it said "internal data" for personal data) and settings.risks and settings.approvals never applied.
func TestProjectSettingsAccessKeys(t *testing.T) {
	in := map[string]any{"data": "personal", "risks": map[string]any{"read": "radical"}, "approvals": map[string]any{"live-deploy": []any{"a@b.c"}}}
	raw := func(v any) rawObject { var r rawObject; _ = json.Unmarshal(jsonBytes(v), &r); return r }
	out, unknown, err := projectSettings(raw(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown keys %v", unknown)
	}
	var got struct {
		Data      string              `json:"data"`
		Risks     map[string]string   `json:"risks"`
		Approvals map[string][]string `json:"approvals"`
	}
	if err := json.Unmarshal(jsonBytes(out), &got); err != nil {
		t.Fatal(err)
	}
	if got.Data != "personal" || got.Risks["read"] != "radical" || got.Approvals["live-deploy"][0] != "a@b.c" {
		t.Fatalf("parsed %+v", got)
	}
	def, _, err := projectSettings(raw(map[string]any{}))
	if err != nil || string(def["data"]) != `"internal"` {
		t.Fatalf("default data %s %v", def["data"], err)
	}
	// The project head's description and domain (board P-A1) are known keys: kept, checked, never unknownKeys.
	if _, unknown, err := projectSettings(raw(map[string]any{"description": "Digital Assets Management", "domain": "revilio.com"})); err != nil || len(unknown) != 0 {
		t.Fatalf("description and domain: unknown %v, %v", unknown, err)
	}
	for _, bad := range []map[string]any{{"data": "secret"}, {"risks": map[string]any{"read": "maybe"}}, {"approvals": map[string]any{"keys": "owner"}},
		{"description": 7}, {"description": "two\nlines"}, {"domain": "https://revilio.com/"}} {
		if _, _, err := projectSettings(raw(bad)); err == nil {
			t.Errorf("%v accepted", bad)
		}
	}
}
