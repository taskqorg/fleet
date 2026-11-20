package main

import (
	"encoding/json"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// Requirement C14 (ChangeKind in contracts/ops.schema.json is every kind the change triggers write). Risk: a trigger
// writes a kind the app's typed subscribers never match, so a screen stops updating live without a type error.
func TestChangeKindsMatchTriggers(t *testing.T) {
	raw, err := os.ReadFile("../../contracts/ops.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		Defs struct {
			ChangeKind struct{ Enum []string } `json:"ChangeKind"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile("schema/functions.sql")
	if err != nil {
		t.Fatal(err)
	}
	functions := string(src)
	var kinds []string
	for _, fn := range []string{"fleet_change", "fleet_artifact_change", "fleet_resource_summary_change"} {
		at := strings.Index(functions, "FUNCTION public."+fn+"()\n")
		if at < 0 {
			t.Fatalf("schema/functions.sql has no %s", fn)
		}
		def := functions[at:]
		def = def[:strings.Index(def, "$function$;")]
		before := len(kinds)
		assigned := strings.Count(def, "k := ")
		for _, m := range regexp.MustCompile(`k := '([a-z_-]+)'`).FindAllStringSubmatch(def, -1) {
			kinds = append(kinds, m[1])
		}
		// k := r.kind copies the document kinds listed in the IN (...) right before it.
		for _, m := range regexp.MustCompile(`r\.kind IN \(([^)]*)\) THEN k := r\.kind`).FindAllStringSubmatch(def, -1) {
			for _, k := range strings.Split(m[1], ",") {
				kinds = append(kinds, strings.Trim(strings.TrimSpace(k), "'"))
			}
			before += strings.Count(m[1], ",") // the IN list is one assignment
		}
		// k := replace(TG_TABLE_NAME, '_', '-') names the change by the table: the tables listed in the IN (...) right before it.
		for _, m := range regexp.MustCompile(`TG_TABLE_NAME IN \(([^)]*)\) THEN k := replace\(TG_TABLE_NAME, '_', '-'\)`).FindAllStringSubmatch(def, -1) {
			for _, k := range strings.Split(m[1], ",") {
				kinds = append(kinds, strings.ReplaceAll(strings.Trim(strings.TrimSpace(k), "'"), "_", "-"))
			}
			before += strings.Count(m[1], ",")
		}
		if got := len(kinds) - before; got != assigned {
			t.Fatalf("%s assigns k %d times, the test reads %d: teach it the new form", fn, assigned, got)
		}
	}
	slices.Sort(kinds)
	kinds = slices.Compact(kinds)
	want := slices.Clone(s.Defs.ChangeKind.Enum)
	slices.Sort(want)
	if !slices.Equal(kinds, want) {
		t.Fatalf("triggers write %v, ChangeKind lists %v", kinds, want)
	}
}
