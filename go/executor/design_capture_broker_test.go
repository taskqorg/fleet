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

func TestDesignCaptureStageRechecksCallerAndCleansPrivateDownloads(t *testing.T) {
	scope, job, measured, archive, _ := measuredCaptureFixture(t)
	raw := []byte(enc(measured))
	job.ReceiptSHA256 = captureTestSHA(raw)
	proof := contract.DesignCaptureReturn{Job: job, ReceiptArtifact: "receipt-id", CaptureArtifact: "archive-id"}
	terminal := mcpText(M{"state": "succeeded", "result": proof}, nil)
	for _, test := range []struct {
		name      string
		latest    any
		downloads int
		staged    bool
	}{
		{"ready", terminal, 2, true},
		{"cancelled during transfer", mcpText(M{"state": "cancelled"}, nil), 2, false},
		{"foreign execution", terminal, 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			execution := scope.Execution
			if test.downloads == 0 {
				execution = scope.Task
			}
			ctx := &delegationContext{ProjectId: &scope.Project, TempDir: &dir, ExecutionId: jsonValue{Set: true, V: execution}}
			downloaded := 0
			get := func(id, incoming, artifact string, limit int64) (*os.File, bool, error) {
				downloaded++
				if id != scope.Execution || filepath.Dir(filepath.Dir(incoming)) != dir {
					t.Fatal("download left the requesting execution's private area")
				}
				data := raw
				if artifact == proof.CaptureArtifact {
					data = archive
				} else if artifact != proof.ReceiptArtifact {
					t.Fatal("foreign artifact")
				}
				if int64(len(data)) > limit {
					t.Fatal("test artifact exceeds its bound")
				}
				name := filepath.Join(incoming, artifact)
				if err := os.WriteFile(name, data, 0600); err != nil {
					return nil, false, err
				}
				f, err := os.Open(name)
				return f, false, err
			}
			result, err := stageCaptureWithTransfer(terminal, ctx, get, func() (any, error) { return test.latest, nil })
			if err != nil || downloaded != test.downloads {
				t.Fatalf("stage = %v downloads=%d", err, downloaded)
			}
			entries, err := os.ReadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if !test.staged {
				if len(entries) != 0 || len(captureToolText(result)) != 0 {
					t.Fatalf("refused capture left output: %v", entries)
				}
				return
			}
			if len(entries) != 1 || !bytes.Contains(captureToolText(result), []byte("staged")) {
				t.Fatalf("staged original unavailable: %v", result)
			}
			contents, err := os.ReadDir(filepath.Join(dir, entries[0].Name()))
			if err != nil || len(contents) != 1 || contents[0].Name() != "originals" {
				t.Fatalf("private input was not removed: %v %v", contents, err)
			}
			replayed, err := stageCaptureWithTransfer(terminal, ctx, get, func() (any, error) { return terminal, nil })
			if err != nil || downloaded != test.downloads || !bytes.Contains(captureToolText(replayed), []byte("already-staged")) {
				t.Fatalf("stage replay downloaded another copy: %v %v", replayed, err)
			}
		})
	}
}
