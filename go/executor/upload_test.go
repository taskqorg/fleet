package main

import (
	"path/filepath"
	"testing"
)

// Requirement: a transfer's terminal state and its final receipt commit together (K2-1). Risk: a crash or failed write
// between them leaves the job done with no receipt, and the coordinator waits forever.
func TestTransferOutcomeAndReceiptCommitTogether(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	uploadSchema(db) // no outbox table yet: queuing the receipt fails
	body := M{"command": M{"id": "c1", "executionId": "x1"}}
	if err := enqueue(db, "j1", "copy", body); err != nil {
		t.Fatal(err)
	}
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "h", "j1", "copy", body, 0, M{}, nil) }); err == nil {
		t.Fatal("settled without an outbox")
	}
	var state string
	db.QueryRow("SELECT state FROM transfers WHERE id='j1'").Scan(&state)
	if state != "queued" {
		t.Fatalf("state %s without its receipt", state)
	}
	outboxSchema(db)
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "h", "j1", "copy", body, 0, M{}, nil) }); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT state FROM transfers WHERE id='j1'").Scan(&state)
	if state != "done" || count(db, "outbox") != 1 {
		t.Fatalf("state %s, outbox %d", state, count(db, "outbox"))
	}
}
