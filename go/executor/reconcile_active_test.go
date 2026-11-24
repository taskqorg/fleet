package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// Requirement: fleet.md Agent execution & executor_process.go: clean script/agent exits still collect and enqueue
// diagnostic mcp-lifecycle-* files in transfers without declaring them in Receipt.Artifacts.
// Risk: declaring optional mcp-lifecycle-* logs in Receipt.Artifacts forces clean exits into waiting on the coordinator.
func TestCleanExitOmitsMCPDiagnosticsFromDeclaredArtifacts(t *testing.T) {
	e, _ := scriptExecutor(t)
	execution := "b1b2c3d4-5678-4abc-8def-0123456789ab"
	session := "clean-mcp-session"
	dir := e.sessionDir(session)
	outDir := filepath.Join(dir, "output")
	mcpDir := filepath.Join(dir, execution+".mcp")
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(mcpDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "deliverable.txt"), []byte("ok\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mcpDir, "mcp-lifecycle-trace.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := e.run(M{"id": "clean-mcp-cmd", "executionId": execution, "sessionId": session, "kind": "script", "payload": M{"command": "exit 0"}}); err != nil {
		t.Fatal(err)
	}
	settle(t, e, execution)

	var receiptBody string
	if err := e.db.QueryRow("SELECT body FROM outbox WHERE route=? ORDER BY rowid DESC LIMIT 1", contract.RouteReceipts.Path).Scan(&receiptBody); err != nil {
		t.Fatalf("read receipt from outbox: %v", err)
	}
	var receipt contract.Receipt
	if err := json.Unmarshal([]byte(receiptBody), &receipt); err != nil {
		t.Fatalf("decode receipt: %v", err)
	}
	if receipt.Status != contract.ReceiptStatusCompleted {
		t.Fatalf("receipt status = %q, want completed", receipt.Status)
	}
	if !contains(receipt.Artifacts, "deliverable.txt") || contains(receipt.Artifacts, "mcp-lifecycle-trace.jsonl") {
		t.Fatalf("receipt artifacts = %#v, want deliverable.txt included and mcp-lifecycle-trace.jsonl excluded", receipt.Artifacts)
	}

	var mcpUploads int
	if err := e.db.QueryRow("SELECT count(*) FROM transfers WHERE kind='upload' AND json_extract(body,'$.name')='mcp-lifecycle-trace.jsonl'").Scan(&mcpUploads); err != nil {
		t.Fatal(err)
	}
	if mcpUploads != 1 {
		t.Fatalf("queued mcp-lifecycle-trace.jsonl upload count = %d, want 1", mcpUploads)
	}
}
