package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"

	"fleet/contract"
)

// Requirement: contracts/host-protocol.schema.json PollRequest.activeExecutions: executor reports non-terminal commands
// and undelivered status receipts on every poll so the coordinator reconciles vanished runs promptly without dropping live ones.
// Risk: finished or failed-outbox runs stay omitted/retained incorrectly, or activeExecutions omits live runs during receipt retry.
func TestPollReportsActiveExecutions(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	var captured contract.PollRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &captured)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"commands":[]}`)
	}))
	defer srv.Close()

	e := &executor{db: db, ex: db, root: t.TempDir(), url: srv.URL, token: "tok", host: "h-active"}

	// 1. Empty journal: non-nil empty slice (marshals as [], distinct from omitted legacy hosts).
	if got := e.activeExecutions(); got == nil || len(got) != 0 {
		t.Fatalf("empty activeExecutions = %#v, want non-nil empty slice", got)
	}
	if _, err := e.pollWait(t.Context(), 0); err != nil {
		t.Fatalf("empty pollWait: %v", err)
	}
	if captured.ActiveExecutions == nil || len(captured.ActiveExecutions) != 0 {
		t.Fatalf("captured empty ActiveExecutions = %#v", captured.ActiveExecutions)
	}
	if captured.Load == nil || captured.Load.Cpus == nil || *captured.Load.Cpus < 1 {
		t.Fatalf("captured poll Load = %#v, want cpus >= 1", captured.Load)
	}

	// 2. Open command (terminal=0) and finished command whose receipt is still in outbox both appear, sorted.
	if _, err := db.Exec(`INSERT INTO commands(id,body,terminal) VALUES
		('cmd-live','{"id":"cmd-live","executionId":"exec-live","kind":"start"}',0),
		('cmd-outbox','{"id":"cmd-outbox","executionId":"exec-outbox","kind":"start"}',1),
		('cmd-done','{"id":"cmd-done","executionId":"exec-done","kind":"start"}',1)`); err != nil {
		t.Fatal(err)
	}
	if err := queueTo(db, contract.RouteReceipts, contract.Receipt{
		HostId: "h-active", CommandId: "cmd-outbox", ExecutionId: "exec-outbox", Status: contract.ReceiptStatusCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	if err := queueTo(db, contract.RouteEvents, contract.HostEvents{
		HostId: "h-active", ExecutionId: "exec-done", Events: []contract.HostEvent{},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := e.pollWait(t.Context(), 0); err != nil {
		t.Fatalf("active pollWait: %v", err)
	}
	want := []string{"exec-live", "exec-outbox"}
	if !reflect.DeepEqual(captured.ActiveExecutions, want) {
		t.Fatalf("captured ActiveExecutions = %#v, want %#v", captured.ActiveExecutions, want)
	}

	// 3. Once the receipt leaves outbox (delivered or poisoned to outbox_failed), exec-outbox is no longer reported active.
	if _, err := db.Exec(`DELETE FROM outbox WHERE route=?`, contract.RouteReceipts.Path); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pollWait(t.Context(), 0); err != nil {
		t.Fatalf("after-delete pollWait: %v", err)
	}
	wantAfter := []string{"exec-live"}
	if !reflect.DeepEqual(captured.ActiveExecutions, wantAfter) {
		t.Fatalf("captured ActiveExecutions after receipt leaves = %#v, want %#v", captured.ActiveExecutions, wantAfter)
	}
}
