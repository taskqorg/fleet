package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"fleet/contract"
)

// Simulate a successful broker without namespaces, Chromium or elevated privileges.
func TestDesignCaptureBrokerFixedFrames(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fixed PDS18 host uses Linux anonymous descriptors")
	}
	input := t.TempDir()
	fields := []struct{ name, data string }{
		{"permit.json", "invented private authorization"},
		{"request.json", "invented guest request"}, {"base.bundle", "base"}, {"overlay.json", "overlay"}, {"fixture.json", "fixture"},
	}
	for _, f := range fields[1:] {
		if err := os.WriteFile(filepath.Join(input, f.name), []byte(f.data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.WriteString(server, "FDCA"); err != nil {
			done <- err
			return
		}
		magic := make([]byte, 4)
		if _, err := io.ReadFull(server, magic); err != nil || string(magic) != "FDC1" {
			done <- errCaptureBroker
			return
		}
		for _, f := range fields {
			var n uint32
			if binary.Read(server, binary.BigEndian, &n) != nil || n != uint32(len(f.data)) {
				done <- errCaptureBroker
				return
			}
			b := make([]byte, n)
			if _, err := io.ReadFull(server, b); err != nil || string(b) != f.data {
				done <- errCaptureBroker
				return
			}
		}
		var reply bytes.Buffer
		reply.WriteString("FDR1")
		_ = binary.Write(&reply, binary.BigEndian, uint32(7))
		_ = binary.Write(&reply, binary.BigEndian, uint32(7))
		reply.WriteString("receiptarchive")
		_, err := server.Write(reply.Bytes())
		done <- err
	}()
	receipt, archive, err := captureBrokerResponse(client, input, []byte(fields[0].data))
	if err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	err = collectPrivateCapture(client, receipt, archive, func(name, path string) error {
		target, err := os.Readlink(path)
		if err != nil || !strings.HasPrefix(target, "/memfd:fleet-capture") {
			t.Fatalf("raw artifact used a named host file: %q %v", target, err)
		}
		b, err := os.ReadFile(path)
		files[name] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if files["receipt.json"] != "receipt" || files["capture.tar"] != "archive" || len(files) != 2 {
		t.Fatal("fixed outputs were not transferred", files)
	}
	if _, err := os.Stat(filepath.Join(input, "permit.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("host permit escaped to a named input")
	}
}

func TestDesignCaptureMediatedDownloadChecksBytesBeforeMaterializing(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fixed PDS18 host uses Linux anonymous descriptors")
	}
	exact := "invented capture archive"
	artifactID, command := "capture-fixture", "origin-command"
	for _, tc := range []struct{ name, wire string }{
		{"exact", exact}, {"interrupted", exact[:7]}, {"wrong digest", strings.Repeat("z", len(exact))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case contract.RouteArtifactsGet.Path:
					var req contract.HostArtifactGetRequest
					if json.NewDecoder(r.Body).Decode(&req) != nil || str0(req.CommandId) != command || req.ArtifactId != artifactID {
						http.Error(w, "missing grant proof", http.StatusForbidden)
						return
					}
					_ = json.NewEncoder(w).Encode(contract.ArtifactGetReply{Id: artifactID, Name: "capture.tar",
						Size: int64(len(exact)), Sha256: captureTestSHA([]byte(exact)), Url: optStr("http://" + r.Host + "/archive")})
				case "/archive":
					_, _ = io.WriteString(w, tc.wire)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			value, f := runJob(newTransferClient(server.URL, "invented", contract.DesignCaptureHost, nil), nil, "fixture", "get",
				transferJob{ArtifactId: &artifactID, CaptureCommand: &command, ExecutionId: "invented-origin", InputDir: dir})
			files, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wire == exact {
				if f != nil || len(files) != 1 {
					t.Fatalf("granted exact archive did not materialize: %v %v", files, f)
				}
				if data, err := os.ReadFile(value.(M)["path"].(string)); err != nil || string(data) != exact {
					t.Fatalf("downloaded bytes differ: %v", err)
				}
			} else if f == nil || f.retry != (tc.name == "interrupted") || len(files) != 0 {
				t.Fatalf("invalid download was exposed or misclassified: %v %v", files, f)
			}
		})
	}
}

func TestDesignCapturePermitIsAbsentFromJournal(t *testing.T) {
	command := M{"kind": "script", "id": "invented", "executionId": "invented", "payload": M{
		"command": contract.DesignCaptureScript, "designCapturePermit": "invented single-use permit", "designCapture": M{"requestId": "synthetic"},
	}}
	recorded := captureJournalBody(command, typedCommand(command))
	if strings.Contains(recorded, "invented single-use permit") || !strings.Contains(recorded, "synthetic") ||
		!strings.Contains(enc(command), "invented single-use permit") {
		t.Fatal("journal did not remove only the transient permit")
	}
}

func TestDesignCaptureBrokerOutputCleanup(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the fixed PDS18 host uses Linux anonymous descriptors")
	}
	for _, test := range []struct{ name, wire string }{
		{"cancelled before archive completes", "receiptpart"},
		{"cancelled before receipt completes", "rec"},
		{"extra output after framed result", "receiptarchiveuntrusted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			if collectPrivateCapture(bytes.NewBufferString(test.wire), 7, 7, func(_, _ string) error {
				called = true
				return nil
			}) == nil || called {
				t.Fatal("unconfirmed broker result reached artifact upload")
			}
		})
	}
}

func TestDesignCaptureBrokerBusyIsRecognizedBeforeInput(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		_, _ = io.WriteString(server, "FDB1")
	}()
	if _, _, err := captureBrokerResponse(client, "/nonexistent", []byte("unused")); !errors.Is(err, errCaptureBusy) {
		t.Fatalf("busy renderer did not refuse before reading source: %v", err)
	}
}

func TestDesignCaptureToolOnlyAcceptsTerminalReturn(t *testing.T) {
	proof := contract.DesignCaptureReturn{ReceiptArtifact: "receipt", CaptureArtifact: "archive"}
	for _, state := range []string{"waiting", "failed", "cancelled", "unconfirmed"} {
		if _, ok := captureToolReturn(mcpText(M{"state": state, "result": proof}, nil)); ok {
			t.Fatalf("%s exposed outputs", state)
		}
	}
	got, ok := captureToolReturn(mcpText(M{"state": "succeeded", "result": proof}, nil))
	if !ok || got != proof {
		t.Fatalf("stored terminal result refused: %+v", got)
	}
	if _, ok := captureToolReturn(mcpText(M{"state": "succeeded", "result": proof}, &yes)); ok {
		t.Fatal("error result exposed outputs")
	}
}

func TestDesignCaptureVerificationDoesNotPublishAndChecksCurrentCaller(t *testing.T) {
	scope, job, measured, archive, _ := measuredCaptureFixture(t)
	raw := []byte(enc(measured))
	job.ReceiptSHA256 = captureTestSHA(raw)
	proof := contract.DesignCaptureReturn{Job: job, ReceiptArtifact: "receipt-id", CaptureArtifact: "archive-id"}
	for _, name := range []string{"verified", "cancelled after validation", "foreign execution"} {
		t.Run(name, func(t *testing.T) {
			request := captureStageRequest{scope.Execution, scope.Session, "test-stage"}
			if name == "foreign execution" {
				request.ExecutionID = scope.Task
			}
			authCount, downloads := 0, 0
			authorize := func() (contract.DesignCaptureReturn, *failure) {
				authCount++
				if authCount == 2 && name == "cancelled after validation" {
					return contract.DesignCaptureReturn{}, permanentf("cancelled")
				}
				return proof, nil
			}
			get := func(original captureStageRequest, pin captureTransferPin) (io.ReadCloser, *failure) {
				downloads++
				if original != request || pin.Job != job.Request.Job {
					t.Fatal("download belongs to another capture")
				}
				var data []byte
				switch pin.ID {
				case proof.ReceiptArtifact:
					data = raw
				case proof.CaptureArtifact:
					data = archive
				default:
					t.Fatal("foreign artifact")
				}
				if int64(len(data)) > pin.Limit || captureTestSHA(data) != pin.SHA256 {
					t.Fatal("download pin changed")
				}
				return io.NopCloser(bytes.NewReader(data)), nil
			}
			verified, f := executeCaptureStage(request, authorize, get, validateReplayFixture)
			switch name {
			case "foreign execution":
				if f == nil || downloads != 0 {
					t.Fatalf("foreign source became verified: %+v (%d downloads)", f, downloads)
				}
			case "cancelled after validation":
				if f == nil || downloads != 2 || verified.Capture.ManifestSHA256 != "" {
					t.Fatalf("cancelled result returned evidence: %v %+v", f, verified)
				}
			default:
				if f != nil || downloads != 2 || verified.Capture.Directory != "" || !sceneSHA.MatchString(verified.Capture.ManifestSHA256) {
					t.Fatalf("validated producer archive unavailable or exposed by a path: %v %+v", f, verified)
				}
			}
		})
	}
}

type interruptedCaptureBody struct {
	bytes.Reader
	failure error
}

func (r *interruptedCaptureBody) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF {
		return n, r.failure
	}
	return n, err
}

func TestDesignCaptureInterruptedBodyRetriesTheSameDurableRow(t *testing.T) {
	scope, job, measured, archive, _ := measuredCaptureFixture(t)
	raw := []byte(enc(measured))
	job.ReceiptSHA256 = captureTestSHA(raw)
	proof := contract.DesignCaptureReturn{Job: job, ReceiptArtifact: "receipt-id", CaptureArtifact: "archive-id"}
	request := captureStageRequest{scope.Execution, scope.Session, "interrupt"}
	db := journal(t)
	if err := enqueue(db, request.key(), captureStageKind, request); err != nil {
		t.Fatal(err)
	}
	authorize := func() (contract.DesignCaptureReturn, *failure) { return proof, nil }
	get := func(source io.Reader) func(captureStageRequest, captureTransferPin) (io.ReadCloser, *failure) {
		return func(_ captureStageRequest, pin captureTransferPin) (io.ReadCloser, *failure) {
			if pin.ID == proof.ReceiptArtifact {
				return io.NopCloser(bytes.NewReader(raw)), nil
			}
			return io.NopCloser(source), nil
		}
	}
	for attempt, interrupt := range []error{syscall.ECONNRESET, os.ErrDeadlineExceeded} {
		interrupted := &interruptedCaptureBody{*bytes.NewReader(archive[:len(archive)/2]), interrupt}
		_, f := executeCaptureStage(request, authorize, get(interrupted), validateReplayFixture)
		if f == nil || !f.retry {
			t.Fatalf("mid-body %v was permanent: %v", interrupt, f)
		}
		if err := (dbExec{db}).tx(func(x execer) error {
			return settleJob(x, "fixture", request.key(), captureStageKind, readJob(enc(request)), int64(attempt), nil, f)
		}); err != nil {
			t.Fatal(err)
		}
		var state string
		if err := db.QueryRow("SELECT state FROM transfers WHERE id=?", request.key()).Scan(&state); err != nil || state != "queued" {
			t.Fatalf("interrupted transfer lost its row: %q %v", state, err)
		}
	}
	bad := bytes.Clone(archive)
	bad[len(bad)-1] ^= 1
	if _, f := executeCaptureStage(request, authorize, get(bytes.NewReader(bad)), validateReplayFixture); f == nil || f.retry {
		t.Fatalf("invalid bytes were retried: %v", f)
	}
	v, f := executeCaptureStage(request, authorize, get(bytes.NewReader(archive)), validateReplayFixture)
	if f != nil {
		t.Fatal(f)
	}
	if err := (dbExec{db}).tx(func(x execer) error {
		return settleJob(x, "fixture", request.key(), captureStageKind, readJob(enc(request)), 1, v, nil)
	}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM transfers WHERE kind=? AND state='done'", captureStageKind).Scan(&count); err != nil || count != 1 {
		t.Fatalf("retry did not finish the original row: %v %v", count, err)
	}
}
func TestDesignCaptureLateAgentToolResultResumesVerifiedStage(t *testing.T) {
	scope, job, measured, archive, _ := measuredCaptureFixture(t)
	raw := []byte(enc(measured))
	job.ReceiptSHA256 = captureTestSHA(raw)
	proof := contract.DesignCaptureReturn{Job: job, ReceiptArtifact: "receipt-id", CaptureArtifact: "archive-id"}
	request := captureStageRequest{scope.Execution, scope.Session, "late-stage"}
	root := t.TempDir()
	dir := runTempDir(root, scope.Execution)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	publication, f := executeCaptureStage(request, func() (contract.DesignCaptureReturn, *failure) {
		return proof, nil
	}, func(_ captureStageRequest, pin captureTransferPin) (io.ReadCloser, *failure) {
		if pin.ID == proof.ReceiptArtifact {
			return io.NopCloser(bytes.NewReader(raw)), nil
		}
		return io.NopCloser(bytes.NewReader(archive)), nil
	}, validateReplayFixture)
	if f != nil {
		t.Fatal(f)
	}
	db := journal(t)
	if _, err := db.Exec("INSERT INTO transfers(id,kind,body,state,response,created_at) VALUES(?,?,?,'done',?,?)",
		request.key(), captureStageKind, enc(request), enc(publication), nowMs()); err != nil {
		t.Fatal(err)
	}
	args := M{"action": "stage", "requestId": request.RequestID}
	rpc := M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": M{"name": "design_capture", "arguments": args}}
	result := enc(M{"jsonrpc": "2.0", "id": 1, "result": mcpText(M{"state": "succeeded", "result": proof}, nil)})
	oldKey := request.CommandID + ":late"
	if _, err := db.Exec("INSERT INTO delegation_requests(id,body,response) VALUES(?,?,?)", oldKey,
		enc(M{"commandId": request.CommandID, "executionId": scope.Execution, "request": rpc}), result); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var key string
				if db.QueryRow("SELECT id FROM delegation_requests WHERE response IS NULL LIMIT 1").Scan(&key) == nil {
					if _, err := db.Exec("UPDATE delegation_requests SET response=? WHERE id=?", result, key); err == nil {
						wake(db, key)
					}
				}
			}
		}
	}()
	defer func() { close(stop); <-stopped }()
	ctx := M{"hostId": "fixture", "commandId": request.CommandID, "executionId": scope.Execution,
		"projectId": "fleet", "tempDir": dir, "operations": []M{{"name": "design_capture"}}}
	answer, err := delegationCall("agent_tool_result", M{"requestKey": oldKey}, ctx, db)
	if err != nil || !bytes.Contains(captureToolText(answer), []byte("verified")) ||
		!bytes.Contains(captureToolText(answer), []byte(proof.CaptureArtifact)) ||
		bytes.Contains(captureToolText(answer), []byte(proof.ReceiptArtifact)) {
		t.Fatalf("late response bypassed verified staging: %v %v", answer, err)
	}
	c := contextOf(ctx)
	if captureDownloadCommand(db, c, proof.CaptureArtifact) != request.CommandID ||
		captureDownloadCommand(db, c, proof.ReceiptArtifact) != "" {
		t.Fatal("archive mediation was not scoped to the verified artifact")
	}
	c.CommandId = &scope.Task
	if captureDownloadCommand(db, c, proof.CaptureArtifact) != "" {
		t.Fatal("another command inherited a verified download")
	}
	var jobs, auth int
	if err := db.QueryRow("SELECT count(*) FROM transfers WHERE kind=?", captureStageKind).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("late response created another transfer: %d %v", jobs, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM delegation_requests").Scan(&auth); err != nil || auth != 3 {
		t.Fatalf("late response skipped the current authority checks: %d %v", auth, err)
	}
}
