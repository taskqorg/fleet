package main

import (
	"slices"
	"testing"
)

// Requirement: migration 67 (host calls that deliver and settle deleted work see deleted rows). Risk: a renamed
// executor route silently loses that visibility, so the stop of a deleted run is never delivered.
func TestDeletedHostRoutesAreRegistered(t *testing.T) {
	for path := range deletedHostRoutes {
		if !slices.ContainsFunc(routeTable, func(r registeredRoute) bool { return r.pattern == "POST "+path }) {
			t.Errorf("deletedHostRoutes names %s, which no route registers", path)
		}
	}
}
