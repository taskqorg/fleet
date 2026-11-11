package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
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
	if _, err := db.Exec("DROP TABLE outbox"); err != nil { // queuing the receipt fails
		t.Fatal(err)
	}
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
	if _, err := db.Exec("CREATE TABLE outbox(route TEXT NOT NULL,body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "h", "j1", "copy", body, 0, M{}, nil) }); err != nil {
		t.Fatal(err)
	}
	db.QueryRow("SELECT state FROM transfers WHERE id='j1'").Scan(&state)
	if state != "done" || count(db, "outbox") != 1 {
		t.Fatalf("state %s, outbox %d", state, count(db, "outbox"))
	}
}

// Requirement: typed artifact reply failures retain resumable transfers and never emit terminal receipts.
// Risk: a malformed success body advances an upload or retires accepted output work.
func TestTypedReserveMismatchRetainsTransfer(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(t.TempDir(), "output.txt")
	if err := os.WriteFile(path, []byte("retained output"), 0600); err != nil {
		t.Fatal(err)
	}
	body := M{"path": path, "name": "output.txt", "executionId": "x", "command": M{"id": "command", "executionId": "x"}}
	if err := enqueue(db, "upload", "upload", body); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/hosts/artifacts/reserve" {
			t.Error("advanced past malformed reserve")
		}
		fmt.Fprint(w, `{"artifactId":"upload","state":9}`)
	}))
	defer server.Close()
	value, f := runJob(newTransferClient(server.URL, "fixture", "host", nil), db, "upload", "upload", body)
	if f == nil || !f.retry {
		t.Fatal("invalid typed reply did not retain retry")
	}
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "host", "upload", "upload", body, 0, value, f) }); err != nil {
		t.Fatal(err)
	}
	var state string
	db.QueryRow("SELECT state FROM transfers WHERE id='upload'").Scan(&state)
	if state != "queued" || count(db, "outbox") != 0 {
		t.Fatal("decode failure retired work or emitted terminal receipt")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("accepted output bytes removed")
	}
}
