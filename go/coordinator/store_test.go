package main

import (
	"testing"
	"time"
)

// Experiment.repeats is an integer column: pgx scans it as int32. Read as zero, a launched experiment planned no trials
// and ended at once as done (live stand, 2026-11-10).
func TestCIntReadsSQLIntegers(t *testing.T) {
	for _, v := range []any{int16(2), int32(2), int64(2), 2, 2.0} {
		if n, ok := intOK(v); !ok || n != 2 {
			t.Fatalf("cInt(%T) = %d, %v", v, n, ok)
		}
	}
	if _, ok := intOK("2"); ok {
		t.Fatal("a string is not a number")
	}
}

// Requirement: one number reader (C12, S2): num reads every SQL integer width and numeric text from ->> reads.
// Risk: an int64 count read as 0 (ff194c7, 39d6e35), a NaN reaching a LIMIT or a timer.
func TestNumReadsEveryNumericType(t *testing.T) {
	for _, v := range []any{int16(2), int32(2), int64(2), 2, 2.0, float32(2), "2"} {
		if num(v) != 2 {
			t.Fatalf("num(%T) = %v", v, num(v))
		}
	}
	for _, v := range []any{"NaN", "Inf", "x", true, nil} {
		if num(v) != 0 {
			t.Fatalf("num(%v) = %v, want 0", v, num(v))
		}
	}
	if msTime(int64(1000)) == nil || msTime(1000.0) == nil || msTime("1000") != nil {
		t.Fatal("msTime reads JSON and SQL millisecond numbers only")
	}
}

// Requirement: PR 2 scalar lineage extraction from definitions[] and config_ref, plus direct time/ms conversion.
// Risk: definition references or fallback profile IDs fail to populate indexed execution columns.
func TestDefinitionScalarsAndExecutionTime(t *testing.T) {
	defs := []any{
		doc{"kind": "agent", "id": "coder", "revision": int64(7)},
		doc{"kind": "pipeline", "id": "build-release", "revision": float64(3)},
	}
	pid, prev, plid, plrev := definitionScalars(defs, doc{"profileId": "fallback"})
	if pid != "coder" || prev != int64(7) || plid != "build-release" || plrev != int64(3) {
		t.Fatalf("unexpected scalars: (%v, %v, %v, %v)", pid, prev, plid, plrev)
	}
	pid2, prev2, plid2, plrev2 := definitionScalars(nil, doc{"profileId": "fallback"})
	if pid2 != "fallback" || prev2 != nil || plid2 != nil || plrev2 != nil {
		t.Fatalf("unexpected fallback scalars: (%v, %v, %v, %v)", pid2, prev2, plid2, plrev2)
	}
	now := time.Now().UTC()
	if got := executionTime(now); got != now {
		t.Fatalf("executionTime(time.Time) = %v, want %v", got, now)
	}
	if got := executionTime(int64(1700000000000)); got == nil {
		t.Fatal("executionTime(epochMs) returned nil")
	}
}
