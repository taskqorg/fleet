package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requirement: a Lab script check observes cancellation (docs/designs/fleet-lab.md, script evaluators). Risk: a hung check
// keeps the experiment busy forever and graceful shutdown cannot stop it; a cancelled check must not be recorded as failed.
func TestRunChecksStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	r, err := runChecks(ctx, []any{doc{"id": "hang", "run": "sleep 30"}}, nil)
	if !errors.Is(err, context.DeadlineExceeded) || r != nil {
		t.Fatalf("runChecks = %v, %v; want no result and the context error", r, err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("the check ran %v after cancellation", d)
	}
}

func TestRegistryLoadRefusesBadEntries(t *testing.T) {
	t.Setenv("FLEET_LAB_DIRECTORY", "../../config/lab")
	if _, err := useCases(); err != nil {
		t.Fatalf("repo registry: %v", err)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "use-cases"), 0o755)
	t.Setenv("FLEET_LAB_DIRECTORY", dir)
	base := `{"id":"x:1","source":{"repo":"r","path":"p","anchor":"a","commit":"c","sha256":"s"},"source_status":"current","actor":"a","goal":"g","expected_outcome":"e","checks":[],"lab_fit":"none","tags":[],"maturity":"m"`
	for name, entry := range map[string]string{"maps_to": base + `,"maps_to":["fleet:UC99"]}`, "field": base + `,"extra":1}`, "status": base + `,"status":"gone"}`} {
		os.WriteFile(filepath.Join(dir, "use-cases", "x.json"), []byte("["+entry+"]"), 0o644)
		if _, err := useCases(); err == nil || !strings.Contains(err.Error(), "x:1") {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
