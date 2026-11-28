package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"fleet/contract"
)

// Simulate a successful broker without namespaces, Chromium or elevated privileges.
func TestDesignCaptureBrokerFixedFrames(t *testing.T) {
	input, output := t.TempDir(), t.TempDir()
	fields := []struct{ name, data string }{
		{"permit.json", "invented private authorization"},
		{"request.json", "invented guest request"}, {"base.bundle", "base"}, {"overlay.json", "overlay"}, {"fixture.json", "fixture"},
	}
	for _, f := range fields {
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
	if err := captureBrokerExchange(client, input, output); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"receipt.json": "receipt", "capture.tar": "archive"} {
		b, err := os.ReadFile(filepath.Join(output, name))
		if err != nil || string(b) != want {
			t.Fatalf("fixed output %s: %q %v", name, b, err)
		}
	}
	if _, err := os.Stat(filepath.Join(output, "permit.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("host permit escaped to an output")
	}
}

func TestDesignCaptureBrokerOutputCleanup(t *testing.T) {
	for _, test := range []struct{ name, wire string }{
		{"cancelled before archive completes", "receiptpart"},
		{"cancelled before receipt completes", "rec"},
		{"extra output after framed result", "receiptarchiveuntrusted"},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			if receiveCaptureOutputs(bytes.NewBufferString(test.wire), dir, 7, 7) == nil {
				t.Fatal("unconfirmed broker result accepted")
			}
			if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
				t.Fatalf("partial output survived: %v %v", entries, err)
			}
		})
	}
	t.Run("existing owner file survives refusal", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "capture.tar")
		if err := os.WriteFile(path, []byte("owned"), 0600); err != nil {
			t.Fatal(err)
		}
		if receiveCaptureOutputs(bytes.NewBufferString("receiptarchive"), dir, 7, 7) == nil {
			t.Fatal("existing archive replaced")
		}
		if b, err := os.ReadFile(path); err != nil || string(b) != "owned" {
			t.Fatalf("owner file changed: %q %v", b, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "receipt.json")); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("partial receipt survived")
		}
	})
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

func TestDesignCaptureStageRechecksCallerAndKeepsVerifiedBytesPrivate(t *testing.T) {
	scope, job, measured, archive, _ := measuredCaptureFixture(t)
	raw := []byte(enc(measured))
	job.ReceiptSHA256 = captureTestSHA(raw)
	proof := contract.DesignCaptureReturn{Job: job, ReceiptArtifact: "receipt-id", CaptureArtifact: "archive-id"}
	for _, name := range []string{"ready and replay", "cancelled during transfer", "foreign execution", "preexisting designer directory"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			request := captureStageRequest{scope.Execution, scope.Session, "test-stage"}
			if name == "foreign execution" {
				request.ExecutionID = scope.Task
			}
			designer := runTempDir(root, request.ExecutionID)
			if err := os.MkdirAll(designer, 0700); err != nil {
				t.Fatal(err)
			}
			if name == "preexisting designer directory" {
				if err := os.Mkdir(filepath.Join(designer, "design-originals-"+job.Request.Job), 0700); err != nil {
					t.Fatal(err)
				}
			}
			authCount, downloads := 0, 0
			authorize := func() (contract.DesignCaptureReturn, *failure) {
				authCount++
				if authCount == 2 {
					entries, err := os.ReadDir(designer)
					if err != nil || len(entries) != 0 {
						t.Fatal("verified bytes exposed before authorization", entries, err)
					}
					if name == "cancelled during transfer" {
						return contract.DesignCaptureReturn{}, permanentf("cancelled")
					}
				}
				return proof, nil
			}
			get := func(id string, pin captureTransferPin) (io.ReadCloser, *failure) {
				downloads++
				if id != scope.Execution || pin.Job != job.Request.Job {
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
			published, f := executeCaptureStage(root, request, authorize, get)
			if name == "foreign execution" || name == "preexisting designer directory" {
				if f == nil || downloads != 0 {
					t.Fatalf("foreign source became staged: %+v (%d downloads)", f, downloads)
				}
				return
			}
			if _, err := os.Stat(filepath.Join(capturePrivateDir(root, request.ExecutionID), "verify")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("verification scratch survived", err)
			}
			if name == "cancelled during transfer" {
				entries, err := os.ReadDir(designer)
				if f == nil || downloads != 2 || err != nil || len(entries) != 0 {
					t.Fatalf("cancelled result became visible: %v %v %v", f, entries, err)
				}
				return
			}
			if f != nil || downloads != 2 || !checkCapturePublication(published.Capture) {
				t.Fatalf("verified archive unavailable: %v", f)
			}
			// The durable marker, not a new download, recovers a crash after rename.
			replayed, f := executeCaptureStage(root, request, authorize, get)
			if f != nil || downloads != 2 || replayed.Capture.ManifestSHA256 != published.Capture.ManifestSHA256 {
				t.Fatalf("stage replay created a new transfer: %v", f)
			}
			if err := os.WriteFile(filepath.Join(published.Capture.Directory, "case-1-recipe.json"), []byte("{}"), 0600); err != nil {
				t.Fatal(err)
			}
			if _, f := executeCaptureStage(root, request, authorize, get); f == nil || checkCapturePublication(published.Capture) {
				t.Fatal("mutated staged files were accepted on replay")
			}
		})
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
	publication, f := executeCaptureStage(root, request, func() (contract.DesignCaptureReturn, *failure) {
		return proof, nil
	}, func(_ string, pin captureTransferPin) (io.ReadCloser, *failure) {
		if pin.ID == proof.ReceiptArtifact {
			return io.NopCloser(bytes.NewReader(raw)), nil
		}
		return io.NopCloser(bytes.NewReader(archive)), nil
	})
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
	if err != nil || !bytes.Contains(captureToolText(answer), []byte("staged")) ||
		bytes.Contains(captureToolText(answer), []byte(proof.CaptureArtifact)) {
		t.Fatalf("late response bypassed verified staging: %v %v", answer, err)
	}
	var jobs, auth int
	if err := db.QueryRow("SELECT count(*) FROM transfers WHERE kind=?", captureStageKind).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("late response created another transfer: %d %v", jobs, err)
	}
	if err := db.QueryRow("SELECT count(*) FROM delegation_requests").Scan(&auth); err != nil || auth != 3 {
		t.Fatalf("late response skipped the current authority checks: %d %v", auth, err)
	}
}
