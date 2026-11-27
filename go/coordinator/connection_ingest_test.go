package main

import (
	"os"
	"path/filepath"
	"testing"

	"fleet/contract"
)

// Confirmed lines generate no work on later passes, even after a malformed line or a missing token.
// Partial appends are retried and replacing the file starts verification from its first byte.
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
	check := func(wantAdded, wantEnd, wantRetries int) {
		t.Helper()
		added, err := connIngestPass(e.ctx, e.app)
		if pos := e.app.connSpool[path]; err != nil || added != wantAdded || pos.end != int64(wantEnd) || len(pos.retry) != wantRetries {
			t.Fatalf("ingest: added=%d end=%d retries=%d error=%v, want %d, %d, %d", added, pos.end, len(pos.retry), err, wantAdded, wantEnd, wantRetries)
		}
	}
	bad := "broken\n"
	write(first + bad + second[:5])
	check(1, len(first)+len(bad), 1)
	check(0, len(first)+len(bad), 1)
	third := line("spool-3", "invented-first")
	appendTail(second[5:] + third)
	complete := len(first) + len(bad) + len(second) + len(third)
	check(1, complete, 2)
	lines, _, err := readSpoolLines(path, e.app.connSpool[path], false)
	if err != nil || len(lines) != 2 || string(lines[0].body)+"\n" != bad || string(lines[1].body)+"\n" != second {
		t.Fatalf("read only unconfirmed lines; got %d lines, error %v", len(lines), err)
	}

	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO run_token(id, project_id) VALUES ('run-token:invented-second', 'a')`); err != nil {
		t.Fatal(err)
	}
	check(1, complete, 1)
	replacement := path + ".new"
	fourth := line("spool-4", "invented-first")
	if err := os.WriteFile(replacement, []byte(first+second+third+fourth), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
	check(1, complete-len(bad)+len(fourth), 0)
}
