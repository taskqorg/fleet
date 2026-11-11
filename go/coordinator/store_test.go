package main

import "testing"

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

// One number reader (C12, S2): num reads every SQL integer width (int64 counts were 0) and numeric text from ->> reads.
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
