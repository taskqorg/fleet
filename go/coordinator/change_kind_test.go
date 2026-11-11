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
	var kinds []string
	for _, fn := range []string{"fleet_change", "fleet_artifact_change"} {
		at := strings.LastIndex(schemaSQL, "FUNCTION "+fn+"()")
		if at < 0 {
			t.Fatalf("schema.sql has no %s", fn)
		}
		def := schemaSQL[at:]
		def = def[:strings.Index(def, "$$;")]
		before := len(kinds)
		assigned := strings.Count(def, "k := ")
		for _, m := range regexp.MustCompile(`k := '([a-z-]+)'`).FindAllStringSubmatch(def, -1) {
			kinds = append(kinds, m[1])
		}
		// k := r.kind copies the document kinds listed in the IN (...) right before it.
		for _, m := range regexp.MustCompile(`r\.kind IN \(([^)]*)\) THEN k := r\.kind`).FindAllStringSubmatch(def, -1) {
			for _, k := range strings.Split(m[1], ",") {
				kinds = append(kinds, strings.Trim(strings.TrimSpace(k), "'"))
			}
			before += strings.Count(m[1], ",") // the IN list is one assignment
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
