package main

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

type resumeRequest struct {
	gw      *fixture
	id, sum string
	total   int
}

func newResumeRequest(gw *fixture, payload []byte) resumeRequest {
	return resumeRequest{gw, strconv.FormatInt(time.Now().Unix(), 10) + "-" + randomHex(16), shaHex(payload), len(payload)}
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
	got, h, _, err := r.gw.upload(body, int64(len(payload)), r.sum, map[string]any{"issue": "TGT-12", "name": "slow.mov"}, headers)
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
	gw.start()
	offset(r.send(t, "status", 0, nil, 204, ""), len(first))
	offset(r.send(t, "chunk", len(first), data[len(first):], 204, ""), len(data))
	call := r.send(t, "commit", 0, nil, 200, "").Get("X-Fleet-Call-Id")
	if call == "" {
		t.Fatal("commit returned no call ID")
	}
	gw.halt()
	gw.start()
	replay := r.send(t, "commit", 0, nil, 409, "upload-committed")
	expect(t, replay.Get("X-Fleet-Call-Id"), call, "original call")
	expect(t, replay.Get("X-Fleet-Outcome"), "unknown", "repeat outcome")
	r.staged(t, 0)
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 1, "provider POSTs")
	expect(t, uploads[0].sha, shaHex(data), "provider hash")
	lines, _ := gw.spool()
	expect(t, len(lines), 1, "recorded calls")
	expect(t, lines[0]["callId"], call, "spool call ID")
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
