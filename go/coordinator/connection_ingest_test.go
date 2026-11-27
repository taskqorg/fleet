package main

import (
	"os"
	"path/filepath"
	"testing"

	"fleet/contract"
)

// A confirmed prefix generates no work on the next pass. A partial append and a call whose token is
// missing are retried, and replacing a file starts verification from its first byte.
func TestConnectionSpoolResumesConfirmedPrefix(t *testing.T) {
	e := openTestCoordinator(t, false)
	dir := t.TempDir()
	t.Setenv("FLEET_GATEWAY_DIR", dir)
	spool := filepath.Join(dir, "state", "spool")
	if err := os.MkdirAll(spool, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO run_token(id, project_id) VALUES ('run-token:invented-first', 'a')`); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(spool, "9999-01-01.jsonl") // a day the gateway could still append to
	line := func(id, token string) string {
		return string(jsonBytes(contract.CallLine{CallId: id, TokenSha256: &token, ExecutionId: ptr("test-run"), Outcome: contract.CallOutcomeOk})) + "\n"
	}
	first, second := line("spool-1", "invented-first"), line("spool-2", "invented-second")
	write := func(data string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	appendTail := func(tail string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.WriteString(tail)
		if closeErr := f.Close(); err != nil || closeErr != nil {
			t.Fatalf("append: %v, close: %v", err, closeErr)
		}
	}
	check := func(wantAdded int, wantOffset int) {
		t.Helper()
		added, err := connIngestPass(e.ctx, e.app)
		if pos := e.app.connSpool[path]; err != nil || added != wantAdded || pos.offset != int64(wantOffset) {
			t.Fatalf("ingest: added=%d offset=%d error=%v; want %d, %d", added, pos.offset, err, wantAdded, wantOffset)
		}
	}
	write(first + second[:5])
	check(1, len(first))
	check(0, len(first))
	appendTail(second[5:])
	check(0, len(first))
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO run_token(id, project_id) VALUES ('run-token:invented-second', 'a')`); err != nil {
		t.Fatal(err)
	}
	check(1, len(first)+len(second))
	replacement := path + ".new"
	third := line("spool-3", "invented-first")
	if err := os.WriteFile(replacement, []byte(first+second+third), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	check(1, len(first)+len(second)+len(third))
}
