package main

import (
	"database/sql"
	"encoding/json"
	"fleet/contract"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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
	requeueUnauthorized(db)
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
		if p.Route == "/api/hosts/artifacts/declare" && (p.Location == nil || *p.Location != "https://login.example/cdn-cgi/access/login") {
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

// Requirement: the store registration and report are the host-protocol types, with the key and the configured capacity
// (W4). Risk: a renamed store field reaches the coordinator as a missing key.
func TestStoreSenderTyped(t *testing.T) {
	d := t.TempDir()
	if err := os.WriteFile(filepath.Join(d, "key"), []byte(`{"accessKeyId":"GK1","secretAccessKey":"s1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := M{"id": "st1", "zone": "z", "bucket": "b", "keyFile": filepath.Join(d, "key"), "capacityBytes": json.Number("1000")}
	r := storeRegistration(cfg, "h1")
	if r == nil || r.HostId != "h1" || r.Store.Id != "st1" || enc(r.Store.AccessKeyId) != `"GK1"` || enc(r.Store.CapacityBytes) != "1000" || enc(r.Store.Zone) != `"z"` {
		t.Fatalf("%+v", r)
	}
	var back contract.StoresRequest
	dec := json.NewDecoder(strings.NewReader(enc(r)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&back); err != nil {
		t.Fatal(err)
	}
	if rep := storeReport(cfg); len(rep) != 1 || rep[0].Id != "st1" || rep[0].Reachable || enc(rep[0].CapacityBytes) != "1000" {
		t.Fatalf("%+v", rep)
	}
	// An empty project-roots list is sent, so the coordinator clears the host's roots.
	if b := enc(contract.PollRequest{ProjectRoots: []string{}}); !strings.Contains(b, `"projectRoots":[]`) {
		t.Fatal(b)
	}
}

// Requirement: the journal schema has one source (journal.sql) applied by user_version, once per section, also when
// several processes open one journal (C20). Risk: a new column is a silent no-op on an existing journal, or a section
// runs twice and fails startup.
func TestJournalVersioned(t *testing.T) {
	// An existing pre-version journal (user_version 0, tables present): the first section applies over it.
	path := filepath.Join(t.TempDir(), "journal.sqlite")
	old, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec("CREATE TABLE outbox(route TEXT NOT NULL,body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	old.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			db, err := openJournal(path)
			if err == nil {
				db.Close()
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var v, tables int
	db.QueryRow("PRAGMA user_version").Scan(&v)
	if want := strings.Count(journalSQL, "\n-- version "); v != want {
		t.Fatalf("user_version %d, want %d", v, want)
	}
	db.QueryRow("SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('commands','sessions','delegation_requests','outbox','outbox_failed','transfers')").Scan(&tables)
	if tables != 6 {
		t.Fatalf("%d of 6 journal tables", tables)
	}
}

// Requirement: readiness data is the host-protocol Readiness, a missing checkout a failed check with its reason (W4).
// Risk: a renamed check field reaches the coordinator as a missing key and a broken host looks ready.
func TestReadinessTyped(t *testing.T) {
	r := readiness(M{"projectId": "fixture", "project": M{"environments": []any{M{"hostId": "h1", "checkoutPath": filepath.Join(t.TempDir(), "absent")}}}}, "h1", "")
	if r.Ok || r.HostId != "h1" || len(r.Checks) == 0 || r.Checks[0].Check != "checkout" || enc(r.Checks[0].Reason) != `"checkout-missing"` {
		t.Fatal(enc(r))
	}
	// The wire names the coordinator reads.
	if b := enc(r.Checks[0]); b != `{"check":"checkout","reason":"checkout-missing","status":"failed","target":"`+r.Checks[0].Target+`"}` {
		t.Fatal(b)
	}
	if b := enc(contract.CheckoutResult{CheckoutPath: "/c", Status: contract.CheckoutStatusSynced}); b != `{"checkoutPath":"/c","status":"synced"}` {
		t.Fatal(b)
	}
}

// Requirement: executor resource summary rides process.closed with accurate active/CPU/memory metrics and missing reasons without periodic resource.observation spam.
// Risk: 2s observation polling floods coordinator change logs, or missing ps samples/interrupted runs misreport or omit resource summary fields.
func TestProcessClosedCarriesResourceSummaryWithoutObservationSpam(t *testing.T) {
	e, _ := scriptExecutor(t)
	if err := e.run(M{"id": "c1", "executionId": "x1", "sessionId": "s1", "kind": "script", "payload": M{"command": "sleep 2.1; echo ok"}}); err != nil {
		t.Fatal(err)
	}
	settle(t, e, "x1")

	rs, err := e.db.Query("SELECT body FROM outbox WHERE route='/api/hosts/events'")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()

	var obsCount, closedCount int
	var closedData json.RawMessage
	for rs.Next() {
		var body string
		if err := rs.Scan(&body); err != nil {
			t.Fatal(err)
		}
		var env contract.HostEvents
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatal(err)
		}
		for _, ev := range env.Events {
			switch ev.Type {
			case string(contract.FleetEventTypeResourceObservation):
				obsCount++
			case string(contract.FleetEventTypeProcessClosed):
				closedCount++
				closedData = ev.Data
			}
		}
	}
	if obsCount != 0 {
		t.Fatalf("expected 0 resource.observation events, got %d", obsCount)
	}
	if closedCount != 1 {
		t.Fatalf("expected 1 process.closed event, got %d", closedCount)
	}

	var pc contract.ProcessClosed
	if err := json.Unmarshal(closedData, &pc); err != nil {
		t.Fatal(err)
	}
	if err := contract.Check(&pc, "ProcessClosed"); err != nil {
		t.Fatal(err)
	}
	if pc.Resources == nil {
		t.Fatal("expected ProcessClosed.Resources to be populated")
	}
	if pc.Resources.ActiveMs == nil || *pc.Resources.ActiveMs < 2000 {
		t.Fatalf("expected ActiveMs >= 2000, got %v", pc.Resources.ActiveMs)
	}
	if pc.Resources.CpuMs == nil || pc.Resources.CpuMissingReason != nil {
		t.Fatalf("expected CpuMs populated and CpuMissingReason nil, got cpu=%v reason=%v", pc.Resources.CpuMs, pc.Resources.CpuMissingReason)
	}
	if pc.Resources.PeakMemoryBytes == nil || *pc.Resources.PeakMemoryBytes <= 0 || pc.Resources.MemoryMissingReason != nil {
		t.Fatalf("expected PeakMemoryBytes > 0 and MemoryMissingReason nil, got rss=%v reason=%v", pc.Resources.PeakMemoryBytes, pc.Resources.MemoryMissingReason)
	}

	noSample := buildExecutionResourceSummary(&live{})
	if err := contract.Check(noSample, "ExecutionResourceSummary"); err != nil {
		t.Fatal(err)
	}
	if noSample.CpuMissingReason == nil || *noSample.CpuMissingReason != contract.ExecutionResourceMissingReasonNoSample {
		t.Fatalf("expected cpuMissingReason=no_sample, got %v", noSample.CpuMissingReason)
	}
	if noSample.MemoryMissingReason == nil || *noSample.MemoryMissingReason != contract.ExecutionResourceMissingReasonNoSample {
		t.Fatalf("expected memoryMissingReason=no_sample, got %v", noSample.MemoryMissingReason)
	}

	interrupted := buildExecutionResourceSummary(&live{stopped: true})
	if err := contract.Check(interrupted, "ExecutionResourceSummary"); err != nil {
		t.Fatal(err)
	}
	if interrupted.CpuMissingReason == nil || *interrupted.CpuMissingReason != contract.ExecutionResourceMissingReasonInterrupted {
		t.Fatalf("expected cpuMissingReason=interrupted, got %v", interrupted.CpuMissingReason)
	}
	if interrupted.MemoryMissingReason == nil || *interrupted.MemoryMissingReason != contract.ExecutionResourceMissingReasonInterrupted {
		t.Fatalf("expected memoryMissingReason=interrupted, got %v", interrupted.MemoryMissingReason)
	}
}
