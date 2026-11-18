package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

type resumeRequest struct {
	gw    *fixture
	id    string
	sum   string
	name  string
	total int
}

func newResumeRequest(gw *fixture, payload []byte) resumeRequest {
	return resumeRequest{gw, strconv.FormatInt(time.Now().Unix(), 10) + "-" + randomHex(16), shaHex(payload), "slow.mov", len(payload)}
}

func (r resumeRequest) send(t *testing.T, mode string, offset int, payload []byte, status int, code string) http.Header {
	t.Helper()
	headers := map[string]string{
		contract.UploadModeHeader:   mode,
		contract.UploadIDHeader:     r.id,
		contract.UploadSizeHeader:   strconv.Itoa(r.total),
		contract.UploadOffsetHeader: strconv.Itoa(offset),
	}
	var body io.Reader
	if len(payload) != 0 {
		body = bytes.NewReader(payload)
	} else {
		headers["Content-Type"] = "application/json"
	}
	got, h, _, err := r.gw.upload(body, int64(len(payload)), r.sum, map[string]any{"issue": "TGT-12", "name": r.name}, headers)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, got, status, mode+" status")
	expect(t, h.Get("X-Fleet-Error"), code, mode+" error")
	return h
}

func (r resumeRequest) staged(t *testing.T, want int) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(r.gw.dir, "state/resumable", r.id+"-*.data"))
	if err != nil {
		t.Fatal(err)
	}
	expect(t, len(files), want, "stage files")
}

// Requirement: TGT-3 resumes at the confirmed offset across a restart and duplicate chunks and commits are safe.
// Risk: the replay appends bytes twice, triggers a second provider POST, or produces extra usage spool lines.
func TestResumableUploadSurvivesRestart(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	data := []byte("one MOV: the exact bytes")
	first := data[:8]
	r := newResumeRequest(gw, data)
	offset := func(h http.Header, want int) {
		t.Helper()
		expect(t, h.Get(contract.UploadOffsetHeader), strconv.Itoa(want), "confirmed offset")
	}
	offset(r.send(t, "status", 0, nil, 204, ""), 0)
	offset(r.send(t, "chunk", 0, first, 204, ""), len(first))
	offset(r.send(t, "chunk", 0, first, 204, ""), len(first))
	r.send(t, "commit", 0, nil, 409, "upload-incomplete")
	gw.halt()
	// Simulate a writer that stopped after writing bytes but before recording their offset.
	files, err := filepath.Glob(filepath.Join(gw.dir, "state/resumable", r.id+"-*.data"))
	if err != nil || len(files) != 1 {
		t.Fatalf("expected one staged file, got %v: %v", files, err)
	}
	f, err := os.OpenFile(files[0], os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("unconfirmed"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	gw.start()
	offset(r.send(t, "status", 0, nil, 204, ""), len(first))
	offset(r.send(t, "chunk", len(first), data[len(first):], 204, ""), len(data))
	gw.halt()
	// Recover the same call after the intent ID was saved but the provider POST was not armed.
	statePath := strings.TrimSuffix(files[0], ".data") + ".state"
	saved, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var pending uploadState
	if err := json.Unmarshal(saved, &pending); err != nil {
		t.Fatal(err)
	}
	pending.CallID, pending.StartedAt = "pending-intent", nowMs()
	if err := writeUploadState(statePath, pending); err != nil {
		t.Fatal(err)
	}
	gw.start()
	call := r.send(t, "commit", 0, nil, 200, "").Get("X-Fleet-Call-Id")
	expect(t, call, pending.CallID, "recovered call ID")
	gw.halt()
	gw.start()
	gw.write(fileSnapshot(p.url, nil, "paused"))
	gw.adoptWritten()
	replay := r.send(t, "commit", 0, nil, 409, "upload-committed")
	expect(t, replay.Get("X-Fleet-Call-Id"), call, "original call")
	expect(t, replay.Get("X-Fleet-Outcome"), "unknown", "repeat outcome")
	changed := r
	changed.name = "renamed.mov"
	changed.send(t, "chunk", 0, first, 409, "upload-binding")
	changed.send(t, "commit", 0, nil, 409, "upload-binding")
	r.staged(t, 0)
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 1, "provider POSTs")
	expect(t, uploads[0].sha, shaHex(data), "provider hash")
	lines, _ := gw.spool()
	expect(t, len(lines), 2, "recorded phases")
	expect(t, lines[0]["callId"], call, "started call ID")
	expect(t, lines[0]["phase"], "started", "intent phase")
	expect(t, lines[0]["errorCode"], "upload-unfinished", "intent problem")
	expect(t, lines[1]["callId"], call, "completed call ID")
	expect(t, lines[1]["phase"], "completed", "final phase")
}

// Requirement: TGT-3 retains authorization and verifies the whole file before the provider gets a commit.
// Risk: a partial, oversized, changed or secret-bearing payload reaches the provider or stays staged after refusal.
func TestResumableUploadRefusals(t *testing.T) {
	gw, p := newFileFixture(t, map[string]any{"fileBytes": 1024})
	t.Run("incomplete and abort", func(t *testing.T) {
		r := newResumeRequest(gw, []byte("whole"))
		r.send(t, "chunk", 0, []byte("part"), 204, "")
		r.send(t, "commit", 0, nil, 409, "upload-incomplete")
		r.staged(t, 1)
		r.send(t, "abort", 0, nil, 204, "")
		r.staged(t, 0)
	})
	t.Run("paused intake still permits cleanup", func(t *testing.T) {
		r := newResumeRequest(gw, []byte("whole"))
		r.send(t, "chunk", 0, []byte("part"), 204, "")
		gw.write(fileSnapshot(p.url, nil, "paused"))
		gw.adoptWritten()
		r.send(t, "chunk", 4, []byte("e"), 403, "intake-paused")
		r.send(t, "abort", 0, nil, 204, "")
		r.staged(t, 0)
		gw.write(fileSnapshot(p.url, map[string]any{"fileBytes": 1024}, "all"))
		gw.adoptWritten()
	})
	t.Run("oversized", func(t *testing.T) {
		r := newResumeRequest(gw, bytes.Repeat([]byte("x"), 1025))
		r.send(t, "chunk", 0, []byte("x"), 413, "file-too-large")
		r.staged(t, 0)
	})
	t.Run("corrupt", func(t *testing.T) {
		r := newResumeRequest(gw, []byte("expected"))
		r.send(t, "chunk", 0, []byte("modified"), 204, "")
		r.send(t, "commit", 0, nil, 400, "sha-mismatch")
		r.staged(t, 0)
	})
	t.Run("secret across chunks", func(t *testing.T) {
		input := "some invented-key-a bytes"
		r := newResumeRequest(gw, []byte(input))
		i := strings.Index(input, "key")
		r.send(t, "chunk", 0, []byte(input[:i]), 204, "")
		r.send(t, "chunk", i, []byte(input[i:]), 204, "")
		r.send(t, "commit", 0, nil, 422, "secret-found")
		r.staged(t, 0)
	})
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider POSTs")
}

// Requirement: §9 drain before the provider handoff leaves a resumable stage.
// Risk: shutdown begins while syncing the commit, then a new provider POST starts in the drain window.
func TestResumableUploadDrainDuringCommitRecording(t *testing.T) {
	f, p := newFileFixture(t, nil)
	g, err := newGateway(f.config)
	if err != nil {
		t.Fatal(err)
	}
	q := &request{connection: "tavily", tool: "attach", id: "intent", begun: time.Now(), bearer: testToken, run: g.authenticate(testToken)}
	req, err := http.NewRequest("POST", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	undone := false
	meta := uploadMetadata{pathArgs: map[string]json.RawMessage{"issue": json.RawMessage(`"TGT-12"`)}, name: "slow.mov", mediaType: "video/quicktime"}
	transfer := &contract.CallTransfer{Direction: contract.CallTransferDirectionUpload, Bytes: 16, Name: meta.name, MediaType: meta.mediaType}
	keep := g.postUpload(w, req, q, meta, nil, transfer, func(*request) (func() error, *answer) {
		g.draining.Store(true)
		return func() error { undone = true; return nil }, nil
	})
	if !keep || !undone || w.Code != 503 || w.Header().Get("X-Fleet-Error") != "draining" {
		t.Fatalf("kept=%v undone=%v status=%d headers=%v", keep, undone, w.Code, w.Header())
	}
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider POSTs")
}

// Requirement: §9 current admission at handoff, including after commit state is synced.
// Risk: an intake pause while syncing the intent fails to stop a staged file from reaching the provider.
func TestResumableUploadRechecksAdmissionAfterCommitRecording(t *testing.T) {
	f, p := newFileFixture(t, nil)
	g, err := newGateway(f.config)
	if err != nil {
		t.Fatal(err)
	}
	q := &request{connection: "tavily", tool: "attach", id: "intent", begun: time.Now(), bearer: testToken, run: g.authenticate(testToken)}
	req, err := http.NewRequest("POST", "/", nil)
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	meta := uploadMetadata{pathArgs: map[string]json.RawMessage{"issue": json.RawMessage(`"TGT-12"`)}, name: "slow.mov", mediaType: "video/quicktime"}
	transfer := &contract.CallTransfer{Direction: contract.CallTransferDirectionUpload, Bytes: 16, Name: meta.name, MediaType: meta.mediaType}
	keep := g.postUpload(w, req, q, meta, nil, transfer, func(*request) (func() error, *answer) {
		snapshot := fileSnapshot(p.url, nil, "paused")
		f.write(snapshot)
		g.refresh(true)
		return func() error { return nil }, nil
	})
	if keep || w.Code != 403 || w.Header().Get("X-Fleet-Error") != "intake-paused" {
		t.Fatalf("kept=%v status=%d headers=%v", keep, w.Code, w.Header())
	}
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider POSTs")
}
