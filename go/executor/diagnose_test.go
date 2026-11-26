package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: host_diagnose reads the executor log tail with credentials redacted and the journal's refused outbox
// rows and failed transfers; risk: an agent needs SSH and a person's eyes to see why a step hung (2026-11-26).
func TestDiagnosisReport(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	dir := t.TempDir()
	path := filepath.Join(dir, "executor.log")
	lines := "2026-11-26T07:39:00.000Z executor exchange failed: Authorization: Bearer abc.def.ghi\n2026-11-26T07:40:00.000Z token=glpat-0123456789abcdef ok\n"
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	old := logPath
	logPath = path
	t.Cleanup(func() { logPath = old })
	db.Exec("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES(?,?,?,?,?)", "/api/hosts/artifacts/failed",
		`{"artifactId":"a1","executionId":"x1"}`, 404, `coordinator 404 {"message":"Unknown artifact"}`, 1795701568115)
	db.Exec("INSERT INTO transfers(id,kind,body,state,response,created_at) VALUES(?,?,?,?,?,?)", "t1", "upload",
		`{"name":"pages/p1.jpg","executionId":"x1"}`, "failed", `{"state":"failed","error":"409 Content was purged"}`, nowMs())
	db.Exec("INSERT INTO outbox(route,body) VALUES(?,?)", "/api/hosts/events", `{"executionId":"x1"}`)
	d := diagnosis(db)
	tail := strings.Join(d.LogTail, "\n")
	if len(d.LogTail) != 2 || strings.Contains(tail, "abc.def") || strings.Contains(tail, "glpat-0123") || !strings.Contains(tail, "[REDACTED]") {
		t.Fatalf("tail %q", d.LogTail)
	}
	if len(d.OutboxFailed) != 1 || d.OutboxFailed[0].Status != 404 || deref(d.OutboxFailed[0].ExecutionId) != "x1" {
		t.Fatalf("outbox failed %+v", d.OutboxFailed)
	}
	if len(d.TransfersFailed) != 1 || d.TransfersFailed[0].Error != "409 Content was purged" || deref(d.TransfersFailed[0].Name) != "pages/p1.jpg" {
		t.Fatalf("transfers %+v", d.TransfersFailed)
	}
	if d.Outbox.Rows != 1 || len(d.Outbox.Routes) != 1 {
		t.Fatalf("outbox %+v", d.Outbox)
	}
}

// Requirement: every log line gets its UTC time and reaches the log file; the file rotates.
func TestLogSinkTimestampsAndRotates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "executor.log")
	f, _ := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	orig, _ := os.CreateTemp(dir, "stderr")
	l := &logSink{path: path, file: f, orig: orig}
	l.line("hello")
	b, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(b), "Z hello\n") || len(b) < 30 {
		t.Fatalf("line %q", b)
	}
	l.line(strings.Repeat("x", logRotateBytes))
	l.line("after")
	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatal("not rotated")
	}
	if b, _ := os.ReadFile(path); !strings.HasSuffix(string(b), "Z after\n") {
		t.Fatalf("after rotation %q", b)
	}
}
