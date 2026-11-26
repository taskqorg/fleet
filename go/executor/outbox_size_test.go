package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// Requirement: an event over the coordinator's body cap is cut to fit and says so; risk: 1-4 MB events (a long context
// handoff, a long tool output) were refused 400 and lost (2026-11-25, owner-mac and windows).
func TestShrinkEventsFitsTheCap(t *testing.T) {
	big := strings.Repeat("контекст ", 300_000) // about 5 MB of two-byte runes
	value := M{"hostId": "h", "executionId": "x", "events": []any{
		map[string]any{"id": "e1", "type": "thread.started", "data": map[string]any{"native": map[string]any{"text": big}, "fleetNormalized": map[string]any{"lifecycle": "thread.started"}}},
		map[string]any{"id": "e2", "type": "item.completed", "data": map[string]any{"text": "small"}},
	}}
	shrinkEvents(value, maxRequestBytes)
	out := enc(value)
	if len(out) > maxRequestBytes || !utf8.ValidString(out) {
		t.Fatalf("batch %d bytes, valid utf8 %v", len(out), utf8.ValidString(out))
	}
	var back struct {
		Events []struct {
			Data map[string]any `json:"data"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(out), &back); err != nil || back.Events[0].Data["fleetTruncated"] == nil || back.Events[1].Data["text"] != "small" {
		t.Fatalf("events %v %v", err, back)
	}
	if !strings.Contains(out, "Fleet cut") {
		t.Fatal("no cut marker")
	}
}

// Requirement: event rows refused for their size go again at start; risk: they stay set aside for ever.
func TestRequeueOversizeEvents(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	big := `{"events":[{"data":{"t":"` + strings.Repeat("x", maxRequestBytes) + `"}}]}`
	db.Exec("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES(?,?,?,?,?)", "/api/hosts/events", big, 400, `coordinator 400 {"message":"Invalid JSON body"}`, 1)
	db.Exec("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES(?,?,?,?,?)", "/api/hosts/events", `{"events":[]}`, 400, "coordinator 400 bad", 1)
	if err := requeueUnauthorized(db); err != nil {
		t.Fatal(err)
	}
	if n := count(db, "outbox"); n != 1 {
		t.Fatalf("requeued %d, want the oversize row only", n)
	}
	if n := count(db, "outbox_failed"); n != 1 {
		t.Fatalf("left %d, want the small refused row", n)
	}
}

// Requirement: an event large from many small items still fits; risk: 413 and a requeue on every start.
func TestShrinkEventsManySmallItems(t *testing.T) {
	items := make([]any, 200_000)
	for i := range items {
		items[i] = "abcdefgh"
	}
	value := M{"events": []any{map[string]any{"id": "e1", "type": "item.completed", "data": map[string]any{"items": items}}}}
	shrinkEvents(value, maxRequestBytes)
	if n := len(enc(value)); n > maxRequestBytes {
		t.Fatalf("batch %d bytes", n)
	}
}
