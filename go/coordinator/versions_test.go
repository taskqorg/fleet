package main

import (
	"testing"
)

// Requirement: installed and offered releases are distinct observations; risk: unknown becomes current or behind.
func TestVersionBehindTarget(t *testing.T) {
	target := "1.261115.5"
	for _, tc := range []struct {
		installed string
		target    *string
		want      any
	}{
		{"1.261114.2", &target, true}, {target, &target, false}, {"dev", &target, nil}, {target, nil, nil},
	} {
		if got := versionBehindTarget(tc.installed, tc.target); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.installed, got, tc.want)
		}
	}
}
