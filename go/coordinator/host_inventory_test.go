package main

import (
	"testing"

	"fleet/coordinator/db"
)

// Requirement: docs/designs/fleet-host-cleanup.md#11-inventory-reconcile: an inventory tears down only settled
// sessions of the kinds that end (pipeline steps, archived or delivered sessions), never one with work in flight, and
// answers unknown for a session with no live row. Risk: a running chat loses its worktree, or a leftover is kept.
func TestClassifyInventory(t *testing.T) {
	for _, c := range []struct {
		name string
		row  db.InventorySessionsRow
		want inventoryVerdict
	}{
		{"no row", db.InventorySessionsRow{}, inventoryUnknown},
		{"open chat", db.InventorySessionsRow{Known: true}, inventoryKeep},
		{"pipeline step ended", db.InventorySessionsRow{Known: true, Pipeline: true}, inventoryTeardown},
		{"pipeline step uploading", db.InventorySessionsRow{Known: true, Pipeline: true, Uploading: true}, inventoryKeep},
		{"archived, live run", db.InventorySessionsRow{Known: true, Archived: true, Live: true}, inventoryKeep},
		{"delivered, question open", db.InventorySessionsRow{Known: true, Accepted: true, Asking: true}, inventoryKeep},
		{"delivered, command pending", db.InventorySessionsRow{Known: true, Accepted: true, Busy: true}, inventoryKeep},
		{"delivered", db.InventorySessionsRow{Known: true, Accepted: true}, inventoryTeardown},
		{"archived, kept by keepWhen", db.InventorySessionsRow{Known: true, Archived: true, Torn: true}, inventoryKept},
	} {
		if got := classifyInventory(c.row); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}
