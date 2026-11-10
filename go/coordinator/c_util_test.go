package main

import "testing"

// Experiment.repeats is an integer column: pgx scans it as int32. Read as zero, a launched experiment planned no trials
// and ended at once as done (live stand, 2026-11-10).
func TestCIntReadsSQLIntegers(t *testing.T) {
	for _, v := range []any{int16(2), int32(2), int64(2), 2, 2.0} {
		if n, ok := cInt(v); !ok || n != 2 {
			t.Fatalf("cInt(%T) = %d, %v", v, n, ok)
		}
	}
	if _, ok := cInt("2"); ok {
		t.Fatal("a string is not a number")
	}
}
