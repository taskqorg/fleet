package main

import (
	"encoding/json"
	"fmt"
	"testing"

	"fleet/contract"
)

// Requirement: every project gets an automatic identity colour, stored once, the least-used one of its kind, so two
// projects never share a colour while another is free (OR-72, owner 2026-11-27). Risk: a hash of the code gives two
// projects the same colour (ABT and CBZ were both sand).
func TestProjectAutoColourIsDistinct(t *testing.T) {
	e := openTestCoordinator(t, false)
	for i := range 4 {
		id := fmt.Sprintf("tint-%d", i)
		e.opInto(t, "project_create", map[string]any{"requestId": "tint-" + id, "id": id, "content": map[string]any{
			"settings":     map[string]any{"id": id, "name": id, "contextFiles": []any{"rules.md"}},
			"contextFiles": []any{map[string]any{"path": "rules.md", "text": "Rules."}}}}, e.ownerHeaders(), &map[string]any{})
	}
	var list contract.ProjectListReply
	e.opInto(t, "project_list", map[string]any{}, e.ownerHeaders(), &list)
	str := func(v json.RawMessage) string { var s string; json.Unmarshal(v, &s); return s }
	colourOf, used := map[string]string{}, map[string]int{}
	for _, p := range list.Projects {
		c := str(p["autoColor"])
		if c == "" {
			t.Fatalf("project %s has no automatic colour", str(p["id"]))
		}
		colourOf[str(p["id"])] = c
		used[c]++
	}
	// Least-used: the counts of any two colours differ by at most one.
	lo, hi := len(list.Projects), 0
	for _, c := range contract.Vocabulary.Identity.Colors {
		lo, hi = min(lo, used[c]), max(hi, used[c])
	}
	if hi-lo > 1 {
		t.Fatalf("colours not spread: %v", used)
	}
	// Stable: a later project does not recolour the earlier ones.
	e.opInto(t, "project_create", map[string]any{"requestId": "tint-late", "id": "tint-late", "content": map[string]any{
		"settings":     map[string]any{"id": "tint-late", "name": "tint-late", "contextFiles": []any{"rules.md"}},
		"contextFiles": []any{map[string]any{"path": "rules.md", "text": "Rules."}}}}, e.ownerHeaders(), &map[string]any{})
	e.opInto(t, "project_list", map[string]any{}, e.ownerHeaders(), &list)
	for _, p := range list.Projects {
		if id, c := str(p["id"]), str(p["autoColor"]); colourOf[id] != "" && colourOf[id] != c {
			t.Fatalf("%s changed colour from %s to %s", id, colourOf[id], c)
		}
	}
}

// Requirement: archived projects do not hold colours: a new project takes a colour no live project has while one is
// free. Risk: prod had 20 projects, 14 archived, and two live ones shared rose (ABT, CBZ).
func TestProjectAutoColourSkipsArchived(t *testing.T) {
	e := openTestCoordinator(t, false)
	create := func(id string) {
		e.opInto(t, "project_create", map[string]any{"requestId": "arch-" + id, "id": id, "content": map[string]any{
			"settings":     map[string]any{"id": id, "name": id, "contextFiles": []any{"rules.md"}},
			"contextFiles": []any{map[string]any{"path": "rules.md", "text": "Rules."}}}}, e.ownerHeaders(), &map[string]any{})
	}
	for i := range 10 {
		id := fmt.Sprintf("old-%d", i)
		create(id)
		e.opInto(t, "project_archive", map[string]any{"projectId": id}, e.ownerHeaders(), &map[string]any{})
	}
	create("fresh")
	var list contract.ProjectListReply
	e.opInto(t, "project_list", map[string]any{}, e.ownerHeaders(), &list)
	str := func(v json.RawMessage) string { var s string; json.Unmarshal(v, &s); return s }
	used := map[string]string{}
	for _, p := range list.Projects {
		c, id := str(p["autoColor"]), str(p["id"])
		if len(list.Projects) <= len(contract.Vocabulary.Identity.Colors) && used[c] != "" {
			t.Fatalf("live %s and %s share %s while colours are free", used[c], id, c)
		}
		used[c] = id
	}
}
