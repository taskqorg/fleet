package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// Requirement: background renewal protects real transfer leases. An upload awaiting a reservation
// (including after recovery) must not send its journal ID and get an "Unknown artifact" response.
func TestTransferRenewalUsesReservedArtifact(t *testing.T) {
	reserved, empty := "reserved-artifact", ""
	cases := []struct {
		name, kind      string
		body            transferJob
		artifact, store string
	}{
		{name: "awaiting reservation", kind: "upload"},
		{name: "awaiting re-reservation", kind: "upload", body: transferJob{Recoveries: 1}},
		{name: "empty artifact ID", kind: "upload", body: transferJob{ArtifactId: &empty}},
		{name: "reserved upload", kind: "upload", body: transferJob{ArtifactId: &reserved, StoreId: "store"}, artifact: reserved},
		{name: "waiting for verification", kind: "upload", body: transferJob{ArtifactId: &reserved, StoreId: "store", Url: "verifying"}, artifact: reserved},
		{name: "copy", kind: "copy", body: transferJob{ArtifactId: &reserved, StoreId: "copy-store"}, artifact: reserved, store: "copy-store"},
		{name: "verify", kind: "verify", body: transferJob{ArtifactId: &reserved, StoreId: "verify-store"}, artifact: reserved, store: "verify-store"},
		{name: "download", kind: "get", body: transferJob{ArtifactId: &reserved}},
		{name: "list", kind: "list"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			artifact, store, ok := leased(tc.kind, tc.body)
			if artifact != tc.artifact || store != tc.store || ok != (tc.artifact != "") {
				t.Fatalf("renewal = (%q, %q, %t), want (%q, %q, %t)", artifact, store, ok, tc.artifact, tc.store, tc.artifact != "")
			}
		})
	}
}

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
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "h", "j1", "copy", readJob(enc(body)), 0, M{}, nil) }); err == nil {
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
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "h", "j1", "copy", readJob(enc(body)), 0, M{}, nil) }); err != nil {
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
	value, f := runJob(newTransferClient(server.URL, "fixture", "host", nil), db, "upload", "upload", readJob(enc(body)))
	if f == nil || !f.retry {
		t.Fatal("invalid typed reply did not retain retry")
	}
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "host", "upload", "upload", readJob(enc(body)), 0, value, f) }); err != nil {
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

// Requirement: FLT-6 artifact upload recovery from NoSuchKey staging verification failure and vanished multipart
// upload session (NoSuchUpload / Upload not found) using the local file copy.
// Risk: transient missing staging object or vanished multipart upload fails an optional/required upload permanently or
// retries infinitely with stale ArtifactId/Url/Done parts, blocking fleet-update.
func TestUploadRecoversFromNoSuchKeyAndVanishedUpload(t *testing.T) {
	payload := []byte(`{"status":"ok","value":42}`)
	wantHash := fmt.Sprintf("%x", sha256.Sum256(payload))

	t.Run("NoSuchKey_on_verify_recovers_from_local_copy", func(t *testing.T) {
		e, _ := scriptExecutor(t)
		db := e.db
		path := filepath.Join(t.TempDir(), "code_mode_result.json")
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
		body := M{"path": path, "spool": true, "name": "code_mode_result.json", "mediaType": "application/json", "executionId": "ex-1"}
		if err := enqueue(db, "job-nosuchkey", "upload", body); err != nil {
			t.Fatal(err)
		}

		var reserves atomic.Int32
		var verifiedCalls atomic.Int32
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/hosts/artifacts/reserve":
				n := reserves.Add(1)
				if n > 1 {
					// Stale ArtifactId/Url/Done must be cleared before re-reserving.
					var rawBody string
					if err := db.QueryRow("SELECT body FROM transfers WHERE id='job-nosuchkey'").Scan(&rawBody); err != nil {
						t.Fatal(err)
					}
					j := readJob(rawBody)
					if j.ArtifactId != nil || j.Url != "" || len(j.Done) != 0 {
						t.Fatalf("stale transfer fields not cleared before re-reserve: %s", rawBody)
					}
					if _, err := os.Stat(path); err != nil {
						t.Fatalf("local file copy missing during recovery: %v", err)
					}
				}
				fmt.Fprintf(w, `{"artifactId":"art-%d","state":"pending","storeId":"s1","partSize":33554432}`, n)
			case "/api/hosts/artifacts/parts":
				var req struct {
					ArtifactId string `json:"artifactId"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				fmt.Fprintf(w, `{"storeId":"s1","parts":[{"partNumber":1,"url":"%s/s3/part/%s"}]}`, server.URL, req.ArtifactId)
			case "/s3/part/art-1", "/s3/part/art-2":
				io.Copy(io.Discard, r.Body)
				w.Header().Set("ETag", `"etag-1"`)
				w.WriteHeader(http.StatusOK)
			case "/api/hosts/artifacts/commit":
				var req struct {
					ArtifactId string `json:"artifactId"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				fmt.Fprintf(w, `{"state":"verifying","verify":{"artifactId":%q,"storeId":"s1","url":"%s/s3/verify/%s"}}`, req.ArtifactId, server.URL, req.ArtifactId)
			case "/s3/verify/art-1":
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<Error><Code>NoSuchKey</Code><Message>The specified key does not exist.</Message></Error>`)
			case "/s3/verify/art-2":
				w.WriteHeader(http.StatusOK)
				w.Write(payload)
			case "/api/hosts/artifacts/verified":
				var req struct {
					ArtifactId string `json:"artifactId"`
					Sha256     string `json:"sha256"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				if req.ArtifactId != "art-2" || req.Sha256 != wantHash {
					t.Fatalf("unexpected verified payload: %+v", req)
				}
				verifiedCalls.Add(1)
				fmt.Fprint(w, `{"state":"stored"}`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer server.Close()

		var bodyText string
		if err := db.QueryRow("SELECT body FROM transfers WHERE id='job-nosuchkey'").Scan(&bodyText); err != nil {
			t.Fatal(err)
		}
		v, f := runJob(newTransferClient(server.URL, "tok", "h1", nil), db, "job-nosuchkey", "upload", readJob(bodyText))
		if f != nil {
			t.Fatalf("expected recovery to succeed, got failure: %+v", f)
		}
		var freshBody string
		db.QueryRow("SELECT body FROM transfers WHERE id='job-nosuchkey'").Scan(&freshBody)
		if err := (dbExec{db}).tx(func(x execer) error {
			return settleJob(x, "h1", "job-nosuchkey", "upload", readJob(freshBody), 0, v, f)
		}); err != nil {
			t.Fatal(err)
		}
		if reserves.Load() != 2 || verifiedCalls.Load() != 1 {
			t.Fatalf("expected 2 reserves and 1 verified call, got reserves=%d verified=%d", reserves.Load(), verifiedCalls.Load())
		}
		var state string
		db.QueryRow("SELECT state FROM transfers WHERE id='job-nosuchkey'").Scan(&state)
		if state != "done" || count(db, "outbox") != 0 {
			t.Fatalf("unexpected transfer state=%s outbox=%d", state, count(db, "outbox"))
		}
	})

	t.Run("Upload_not_found_and_NoSuchUpload_recover_from_local_copy", func(t *testing.T) {
		e, _ := scriptExecutor(t)
		db := e.db
		path := filepath.Join(t.TempDir(), "result.json")
		if err := os.WriteFile(path, payload, 0600); err != nil {
			t.Fatal(err)
		}
		body := M{"path": path, "spool": true, "name": "result.json", "mediaType": "application/json", "executionId": "ex-2"}
		if err := enqueue(db, "job-nosuchupload", "upload", body); err != nil {
			t.Fatal(err)
		}

		var reserves atomic.Int32
		var verifiedCalls atomic.Int32
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/hosts/artifacts/reserve":
				n := reserves.Add(1)
				if n > 1 {
					var rawBody string
					if err := db.QueryRow("SELECT body FROM transfers WHERE id='job-nosuchupload'").Scan(&rawBody); err != nil {
						t.Fatal(err)
					}
					j := readJob(rawBody)
					if j.ArtifactId != nil || j.Url != "" || len(j.Done) != 0 {
						t.Fatalf("stale transfer fields not cleared before re-reserve on attempt %d: %s", n, rawBody)
					}
				}
				fmt.Fprintf(w, `{"artifactId":"u-%d","state":"pending","storeId":"s1","partSize":33554432}`, n)
			case "/api/hosts/artifacts/parts":
				var req struct {
					ArtifactId string `json:"artifactId"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				fmt.Fprintf(w, `{"storeId":"s1","parts":[{"partNumber":1,"url":"%s/s3/part/%s"}]}`, server.URL, req.ArtifactId)
			case "/s3/part/u-1":
				io.Copy(io.Discard, r.Body)
				w.Header().Set("ETag", `"e1"`)
				w.WriteHeader(http.StatusOK)
			case "/s3/part/u-2":
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<Error><Code>NoSuchUpload</Code><Message>Upload not found</Message></Error>`)
			case "/s3/part/u-3":
				io.Copy(io.Discard, r.Body)
				w.Header().Set("ETag", `"e3"`)
				w.WriteHeader(http.StatusOK)
			case "/api/hosts/artifacts/commit":
				var req struct {
					ArtifactId string `json:"artifactId"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				if req.ArtifactId == "u-1" {
					// Simulate coordinator returning 500 Upload not found on vanished multipart session.
					w.WriteHeader(http.StatusInternalServerError)
					fmt.Fprint(w, `{"message":"Upload not found"}`)
					return
				}
				fmt.Fprintf(w, `{"state":"verifying","verify":{"artifactId":%q,"storeId":"s1","url":"%s/s3/verify/%s"}}`, req.ArtifactId, server.URL, req.ArtifactId)
			case "/s3/verify/u-3":
				w.WriteHeader(http.StatusOK)
				w.Write(payload)
			case "/api/hosts/artifacts/verified":
				var req struct {
					ArtifactId string `json:"artifactId"`
					Sha256     string `json:"sha256"`
				}
				json.NewDecoder(r.Body).Decode(&req)
				if req.ArtifactId != "u-3" || req.Sha256 != wantHash {
					t.Fatalf("unexpected verified payload: %+v", req)
				}
				verifiedCalls.Add(1)
				fmt.Fprint(w, `{"state":"stored"}`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer server.Close()

		var bodyText string
		db.QueryRow("SELECT body FROM transfers WHERE id='job-nosuchupload'").Scan(&bodyText)
		v, f := runJob(newTransferClient(server.URL, "tok", "h1", nil), db, "job-nosuchupload", "upload", readJob(bodyText))
		if f != nil {
			t.Fatalf("expected recovery to succeed, got failure: %+v", f)
		}
		var freshBody string
		db.QueryRow("SELECT body FROM transfers WHERE id='job-nosuchupload'").Scan(&freshBody)
		if err := (dbExec{db}).tx(func(x execer) error {
			return settleJob(x, "h1", "job-nosuchupload", "upload", readJob(freshBody), 0, v, f)
		}); err != nil {
			t.Fatal(err)
		}
		if reserves.Load() != 3 || verifiedCalls.Load() != 1 {
			t.Fatalf("expected 3 reserves and 1 verified call, got reserves=%d verified=%d", reserves.Load(), verifiedCalls.Load())
		}
		var state string
		db.QueryRow("SELECT state FROM transfers WHERE id='job-nosuchupload'").Scan(&state)
		if state != "done" || count(db, "outbox") != 0 {
			t.Fatalf("unexpected transfer state=%s outbox=%d", state, count(db, "outbox"))
		}
	})
}

// Requirement: FLT-6 unrecoverable upload failures (missing local file or exhausted recovery) record the exact
// terminal cause and terminate the transfers job so the host update queue (fleet-update) is released.
// Risk: failed or unrecoverable uploads remain queued/running in transfers and deadlock update drain indefinitely.
func TestUploadTerminalFailureReleasesUpdateQueue(t *testing.T) {
	t.Run("missing_local_file_terminates_and_releases_update_queue", func(t *testing.T) {
		e, _ := scriptExecutor(t)
		lastSignal = nil
		t.Cleanup(func() {
			lastSignal = nil
			if e.activity != nil {
				e.activity.Close()
				e.activity = nil
			}
		})
		target := "1.261113.20"
		lastSignal = &updateSignal{Target: &target, Running: "1.261113.19", AutoUpdate: "on"}

		missing := filepath.Join(t.TempDir(), "missing.json")
		body := M{"path": missing, "spool": true, "name": "result.json", "mediaType": "application/json", "executionId": "ex-missing"}
		if err := enqueue(e.db, "job-missing", "upload", body); err != nil {
			t.Fatal(err)
		}
		if err := e.drainUpdate(); err != nil {
			t.Fatal(err)
		}
		if e.updateReady.Load() != nil {
			t.Fatal("pending transfer must block update drain before failure settles")
		}

		v, f := runJob(newTransferClient("http://127.0.0.1:0", "tok", "h1", nil), e.db, "job-missing", "upload", readJob(enc(body)))
		if f == nil || f.retry {
			t.Fatalf("expected terminal non-retryable failure for missing file, got: %+v", f)
		}
		if err := (dbExec{e.db}).tx(func(x execer) error {
			return settleJob(x, "h1", "job-missing", "upload", readJob(enc(body)), 0, v, f)
		}); err != nil {
			t.Fatal(err)
		}
		var state, resp string
		if err := e.db.QueryRow("SELECT state,response FROM transfers WHERE id='job-missing'").Scan(&state, &resp); err != nil {
			t.Fatal(err)
		}
		if state != "failed" || !strings.Contains(resp, "no such file") && !strings.Contains(resp, "cannot find") {
			t.Fatalf("unexpected state=%s resp=%s", state, resp)
		}
		var route, outBody string
		if err := e.db.QueryRow("SELECT route,body FROM outbox LIMIT 1").Scan(&route, &outBody); err != nil {
			t.Fatal(err)
		}
		if route != "/api/hosts/artifacts/failed" || !strings.Contains(outBody, f.msg) {
			t.Fatalf("unexpected outbox route=%s body=%s", route, outBody)
		}
		if _, err := e.db.Exec("DELETE FROM outbox"); err != nil {
			t.Fatal(err)
		}
		if err := e.drainUpdate(); err != nil {
			t.Fatal(err)
		}
		if p := e.updateReady.Load(); p == nil || *p != target {
			t.Fatalf("update queue stayed blocked after terminal transfer failure: %v", p)
		}
	})

	t.Run("exhausted_recovery_terminates_and_releases_update_queue", func(t *testing.T) {
		e, _ := scriptExecutor(t)
		lastSignal = nil
		t.Cleanup(func() {
			lastSignal = nil
			if e.activity != nil {
				e.activity.Close()
				e.activity = nil
			}
		})
		target := "1.261113.20"
		lastSignal = &updateSignal{Target: &target, Running: "1.261113.19", AutoUpdate: "on"}

		path := filepath.Join(t.TempDir(), "result.json")
		if err := os.WriteFile(path, []byte(`{"ok":true}`), 0600); err != nil {
			t.Fatal(err)
		}
		body := M{"path": path, "spool": true, "name": "result.json", "mediaType": "application/json", "executionId": "ex-exhausted"}
		if err := enqueue(e.db, "job-exhausted", "upload", body); err != nil {
			t.Fatal(err)
		}

		var reserves atomic.Int32
		var server *httptest.Server
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/hosts/artifacts/reserve":
				n := reserves.Add(1)
				fmt.Fprintf(w, `{"artifactId":"art-%d","state":"pending","storeId":"s1","partSize":33554432}`, n)
			case "/api/hosts/artifacts/parts":
				fmt.Fprintf(w, `{"storeId":"s1","parts":[{"partNumber":1,"url":"%s/s3/missing"}]}`, server.URL)
			case "/s3/missing":
				io.Copy(io.Discard, r.Body)
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, `<Error><Code>NoSuchUpload</Code><Message>Upload not found</Message></Error>`)
			default:
				t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
			}
		}))
		defer server.Close()

		v, f := runJob(newTransferClient(server.URL, "tok", "h1", nil), e.db, "job-exhausted", "upload", readJob(enc(body)))
		if f == nil || f.retry {
			t.Fatalf("expected terminal failure after bounded recovery, got: %+v", f)
		}
		if !strings.Contains(f.msg, "Upload not found") {
			t.Fatalf("terminal cause lost exact error: %s", f.msg)
		}
		if reserves.Load() != maxUploadRecoveries+1 {
			t.Fatalf("expected %d bounded reserve attempts, got %d", maxUploadRecoveries+1, reserves.Load())
		}
		var freshBody string
		e.db.QueryRow("SELECT body FROM transfers WHERE id='job-exhausted'").Scan(&freshBody)
		if err := (dbExec{e.db}).tx(func(x execer) error {
			return settleJob(x, "h1", "job-exhausted", "upload", readJob(freshBody), 0, v, f)
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("local file copy prematurely removed: %v", err)
		}
		if _, err := e.db.Exec("DELETE FROM outbox"); err != nil {
			t.Fatal(err)
		}
		if err := e.drainUpdate(); err != nil {
			t.Fatal(err)
		}
		if p := e.updateReady.Load(); p == nil || *p != target {
			t.Fatalf("update queue stayed blocked after exhausted upload recovery: %v", p)
		}
	})
}

// Requirement: a refused reservation (409 purged) fails the transfer at once and reports artifacts/failed with the
// execution, the name and the coordinator's message; risk: the step waits a day for an upload that never starts
// (2026-11-26, DAM page images).
func TestRefusedReserveReportsExecutionAndName(t *testing.T) {
	db, err := openJournal(filepath.Join(t.TempDir(), "journal.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(t.TempDir(), "page.jpg")
	if err := os.WriteFile(path, []byte("page"), 0600); err != nil {
		t.Fatal(err)
	}
	body := M{"path": path, "name": "pages/p1.jpg", "executionId": "ex-1"}
	if err := enqueue(db, "up-1", "upload", body); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(409)
		fmt.Fprint(w, `{"statusCode":409,"error":"Conflict","message":"Content was purged"}`)
	}))
	defer server.Close()
	v, f := runJob(newTransferClient(server.URL, "fixture", "host", nil), db, "up-1", "upload", readJob(enc(body)))
	if f == nil || f.retry || !f.refused || f.msg != "409 Content was purged" {
		t.Fatalf("refusal %+v", f)
	}
	if err := (dbExec{db}).tx(func(x execer) error { return settleJob(x, "host", "up-1", "upload", readJob(enc(body)), 0, v, f) }); err != nil {
		t.Fatal(err)
	}
	var route, out string
	if err := db.QueryRow("SELECT route,body FROM outbox LIMIT 1").Scan(&route, &out); err != nil {
		t.Fatal(err)
	}
	var req struct {
		ExecutionId, Name string
		Refused           bool
	}
	json.Unmarshal([]byte(out), &req)
	if route != "/api/hosts/artifacts/failed" || req.ExecutionId != "ex-1" || req.Name != "pages/p1.jpg" || !req.Refused {
		t.Fatalf("report %s %s", route, out)
	}
}
