package main

import (
	"strings"
	"testing"
)

// Requirement: a valid body over the 1 MiB cap is answered 413, not "Invalid JSON body"; risk: the executor and the
// person reading its log took size for malformed JSON and the events were lost (2026-11-25).
func TestBodyOverCapIs413(t *testing.T) {
	e := openTestCoordinator(t, false)
	secret := e.registerHost(t, "h-cap")
	body := doc{"hostId": "h-cap", "executionId": "x", "events": []any{doc{"id": "e1", "seq": 1, "type": "item.completed", "data": doc{"t": strings.Repeat("x", 1<<20)}}}}
	if code, reply := e.callJSON(t, "POST", "/api/hosts/events", body, map[string]string{"Authorization": "Bearer " + secret}); code != 413 {
		t.Fatalf("over the cap: %d %v", code, reply)
	}
}
