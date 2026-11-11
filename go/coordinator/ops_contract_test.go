package main

import (
	"testing"

	"fleet/contract"
)

// Requirement C13 (contracts/ops-<domain>.schema.json is the list of operations the app and agents call). Risk: an operation
// renamed, dropped or added in Go while the app's typed op calls still compile against the old name.
func TestOperationsMatchContract(t *testing.T) {
	for name, op := range operations {
		c, ok := contract.Operations[name]
		_, long := longOperations[name]
		switch {
		case !ok:
			t.Errorf("%s is registered but missing from contracts/ops-<domain>.schema.json", name)
		case c.Long != long || c.Read != op.read:
			t.Errorf("%s: contract read=%v long=%v, registry read=%v long=%v", name, c.Read, c.Long, op.read, long)
		}
	}
	for name := range contract.Operations {
		if _, ok := operations[name]; !ok {
			t.Errorf("%s is in contracts/ops-<domain>.schema.json but not registered", name)
		}
	}
}
