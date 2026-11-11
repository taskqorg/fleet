package main

import (
	"regexp"
	"strconv"
	"testing"
)

// Requirement: each "-- migration N" section runs once, in order (store.migrate). Risk: two branches each add the next
// number, both pass CI alone, and after both merge one section is skipped on every database that applied the other.
func TestMigrationNumbersIncrease(t *testing.T) {
	last := 0
	for _, m := range regexp.MustCompile(`(?m)^-- migration (\d+)`).FindAllStringSubmatch(schemaSQL, -1) {
		n, _ := strconv.Atoi(m[1])
		if n <= last {
			t.Fatalf("migration %d follows %d: renumber it after the last one on main", n, last)
		}
		last = n
	}
}
