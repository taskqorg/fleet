package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type seen struct {
	mu   sync.Mutex
	rows []string
}

// serve answers each request with answer(path, body) -> status (a 3xx carries an Access login Location).
func serve(t *testing.T, answer func(path string, body M) int) (string, *seen) {
	s := &seen{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body := decM(string(b))
		s.mu.Lock()
		s.rows = append(s.rows, r.URL.Path+" "+jStr(body, "executionId"))
		s.mu.Unlock()
		st := answer(r.URL.Path, body)
		if st >= 300 && st < 400 {
			w.Header().Set("Location", "https://login.example/cdn-cgi/access/login?token=x")
		}
		w.WriteHeader(st)
		w.Write([]byte("{}"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, s
}

func journal(t *testing.T) *sql.DB {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := outboxSchema(db); err != nil {
		t.Fatal(err)
	}
	return db
}

func count(db *sql.DB, table string) (n int) {
	db.QueryRow("SELECT count(*) FROM " + table).Scan(&n)
	return
}

// Requirement: a 4xx row is set aside and reported, never blocking the rows behind it. Risk: one poisoned receipt stops
// every later receipt of the host.
func TestRejectedRowIsSetAsideAndDoesNotBlock(t *testing.T) {
	url, s := serve(t, func(_ string, b M) int {
		if jStr(b, "commandId") == "bad" {
			return 400
		}
		return 200
	})
	db := journal(t)
	queue(db, "/api/hosts/receipts", M{"commandId": "bad", "executionId": "e1"})
	queue(db, "/api/hosts/receipts", M{"commandId": "good", "executionId": "e1"})
	for i := 0; i < 2; i++ {
		if err := flush(db, url, "t", "mac"); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.rows) != 3 || s.rows[2] != "/api/hosts/events e1" || count(db, "outbox") != 0 || count(db, "outbox_failed") != 1 {
		t.Fatalf("rows %v outbox %d failed %d", s.rows, count(db, "outbox"), count(db, "outbox_failed"))
	}
}

// Requirement: 401 (host token) keeps the row queued, and old 401 rows return to the queue. Risk: a token rotation drops
// receipts and leaves executions running forever.
func TestUnauthorizedRowStaysQueued(t *testing.T) {
	url, _ := serve(t, func(string, M) int { return 401 })
	db := journal(t)
	db.Exec("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES('/api/hosts/receipts','{}',401,'x',0),('/api/hosts/events','{}',400,'x',0)")
	outboxSchema(db)
	if count(db, "outbox") != 1 || count(db, "outbox_failed") != 1 {
		t.Fatal("only the 401 row is requeued")
	}
	db.Exec("DELETE FROM outbox")
	queue(db, "/api/hosts/tokens-test", M{"commandId": "c1", "executionId": "e1"})
	flush(db, url, "t", "mac")
	if count(db, "outbox") != 1 || count(db, "outbox_failed") != 1 {
		t.Fatal("401 row was not kept")
	}
}

// Requirement: an edge redirect holds its route and its execution only; order holds within each. Risk: one route behind
// an Access login blocks every execution, or a receipt overtakes its own declaration.
func TestRedirectHoldsOnlyItsRouteAndExecution(t *testing.T) {
	url, s := serve(t, func(p string, _ M) int {
		if p == "/api/hosts/artifacts/declare" {
			return 302
		}
		return 200
	})
	db := journal(t)
	queue(db, "/api/hosts/artifacts/declare", M{"name": "a", "executionId": "e1"})
	queue(db, "/api/hosts/artifacts/declare", M{"name": "b", "executionId": "e2"})
	queue(db, "/api/hosts/receipts", M{"commandId": "c3", "executionId": "e3"})
	queue(db, "/api/hosts/receipts", M{"commandId": "c1", "executionId": "e1"})
	queue(db, "/api/hosts/receipts", M{"commandId": "c4", "executionId": "e4"})
	queue(db, "/api/hosts/events", M{"executionId": "e3", "events": []any{M{"id": "1"}}})
	if err := flush(db, url, "t", "mac"); err != nil {
		t.Fatal(err)
	}
	want := []string{"/api/hosts/artifacts/declare e1", "/api/hosts/receipts e3", "/api/hosts/receipts e4", "/api/hosts/events e3"}
	if enc(s.rows) != enc(want) || count(db, "outbox") != 3 || count(db, "outbox_failed") != 0 {
		t.Fatalf("got %v, left %d", s.rows, count(db, "outbox"))
	}
	for _, p := range edgeProblems("") {
		if jStr(p, "route") == "/api/hosts/artifacts/declare" && jStr(p, "location") != "https://login.example/cdn-cgi/access/login" {
			t.Fatalf("location keeps its query: %v", p)
		}
	}
}

// Requirement: one status table; 401 is an edge condition for the coordinator only. Risk: a store 401 retried forever.
func TestOneStatusTable(t *testing.T) {
	for _, c := range []struct {
		s           int
		coord, stor class
	}{{200, classOK, classOK}, {302, classEdge, classEdge}, {401, classEdge, classReject}, {404, classReject, classReject}, {429, classRetry, classRetry}, {503, classRetry, classRetry}} {
		if statusClass(c.s, true) != c.coord || statusClass(c.s, false) != c.stor {
			t.Fatal(c.s)
		}
	}
}

// Requirement: Claude runs only where the placement setting matches the platform; native ids are safe argv. Risk: Claude
// started on an unplaced host (PDS18), or an id read as a flag.
func TestPlacementAndNativeID(t *testing.T) {
	for _, c := range []struct{ set, os, rel, want, err string }{
		{"", "darwin", "", "", "claude-placement-unset"}, {"mac", "darwin", "", "mac", ""}, {"mac", "linux", "", "", "claude-placement-platform-mismatch"},
		{"windows-wsl", "linux", "5.15.0-microsoft-standard-WSL2", "windows-wsl", ""}, {"windows-wsl", "linux", "6.1.0", "", "claude-placement-platform-mismatch"}, {"x", "linux", "", "", "claude-placement-invalid"},
	} {
		got, err := claudePlacement(c.set, c.os, c.rel)
		if got != c.want || (err == nil) != (c.err == "") || err != nil && err.Error() != c.err {
			t.Fatal(c, got, err)
		}
	}
	for n, ok := range map[string]bool{"abc-123": true, "": false, "-x": false, "a b": false, "a;b": false} {
		if safeNativeID(n) != ok {
			t.Fatal(n)
		}
	}
}

// Requirement: the store key file formats on the hosts all read. Risk: the store never registers.
func TestStoreKeyFormats(t *testing.T) {
	d := t.TempDir()
	for name, text := range map[string]string{"a": `{"accessKeyId":"GK1","secretAccessKey":"s1"}`, "b": "export AWS_ACCESS_KEY_ID=GK1\nAWS_SECRET_ACCESS_KEY='s1'\n", "c": "GK1\ns1\n",
		"d": "==== ACCESS KEY INFORMATION ====\nKey ID:              GK1\nKey name:            fleet-coordinator\nSecret key:          s1\n"} {
		os.WriteFile(filepath.Join(d, name), []byte(text), 0o600)
		if id, s, ok := readKey(filepath.Join(d, name)); !ok || id != "GK1" || s != "s1" {
			t.Fatal(name, id, s)
		}
	}
}
