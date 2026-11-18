package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"fleet/contract"
)

const uploadLifetime = 24 * time.Hour

func uploadCreation(id string) (time.Time, bool) {
	stamp, nonce, ok := strings.Cut(id, "-")
	unix, err := strconv.ParseInt(stamp, 10, 64)
	return time.Unix(unix, 0), ok && err == nil && len(stamp) == 10 && isHex(nonce, 32)
}

func (g *gateway) uploadDir() string {
	return filepath.Join(g.config.StateDir, "resumable")
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func (g *gateway) serveResumableUpload(w http.ResponseWriter, r *http.Request, q *request) {
	refuse := func(status int, code, reason string) {
		if r.ContentLength != 0 {
			w.Header().Set("Connection", "close")
		}
		a := refusal(status, "error", code, reason)
		w.Header().Set("X-Fleet-Outcome", a.outcome)
		w.Header().Set("X-Fleet-Error", a.err)
		w.Header().Set("X-Fleet-Call-Id", q.id)
		writeJSON(w, a.status, a.body)
	}
	if a := admit(q); a != nil {
		refuse(a.status, a.err, "the upload is not admitted")
		return
	}
	if g.draining.Load() {
		refuse(503, "draining", "the gateway is stopping")
		return
	}
	mode := r.Header.Get(contract.UploadModeHeader)
	id := r.Header.Get(contract.UploadIDHeader)
	total, sizeErr := strconv.ParseInt(r.Header.Get(contract.UploadSizeHeader), 10, 64)
	born, ok := uploadCreation(id)
	now := time.Now()
	if !ok || born.After(now.Add(5*time.Minute)) || !born.Add(uploadLifetime).After(now) {
		refuse(400, "upload-id-invalid", "the upload ID is invalid or expired")
		return
	}
	if sizeErr != nil || total <= 0 || (mode != "chunk" && r.ContentLength != 0) {
		refuse(400, "bad-request", "the upload needs a positive whole-file size and only chunks carry a body")
		return
	}
	meta, a := parseUploadMetadata(r, q.t)
	if a != nil {
		refuse(a.status, a.err, "invalid upload metadata")
		return
	}
	// Bind the stage and commit tombstone to the exact token, destination and file description.
	binding := sha(strings.Join([]string{q.run.Sha256, q.connection, q.tool, r.Header.Get("X-Fleet-Args"), meta.sha, strconv.FormatInt(total, 10)}, "\x00"))
	key := id + "-" + binding
	if _, busy := g.uploadLocks.LoadOrStore(key, struct{}{}); busy {
		refuse(409, "upload-busy", "another request is using this upload")
		return
	}
	defer g.uploadLocks.Delete(key)
	if !born.Add(uploadLifetime).After(time.Now()) {
		refuse(400, "upload-id-invalid", "the upload ID expired")
		return
	}
	base := filepath.Join(g.uploadDir(), key)
	data, marker := base+".data", base+".commit"
	if call, err := os.ReadFile(marker); err == nil {
		// A crash may leave only the marker. Never replay the external side effect.
		w.Header().Set("X-Fleet-Call-Id", string(call))
		w.Header().Set("X-Fleet-Outcome", "unknown")
		w.Header().Set("X-Fleet-Error", "upload-committed")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "upload-committed", "reason": "read back the attachment before another upload"})
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		refuse(507, "staging-failed", "the commit record could not be read")
		return
	}

	cap, _, stall, _ := limits(q.c)
	if cap > 0 && total > cap {
		os.Remove(data)
		refuse(413, "file-too-large", "the whole file exceeds the connection's limit")
		return
	}
	var offset int64
	if info, err := os.Stat(data); err == nil {
		offset = info.Size()
	} else if !errors.Is(err, os.ErrNotExist) {
		refuse(507, "staging-failed", "the staged size could not be read")
		return
	}
	ack := func() {
		w.Header().Set(contract.UploadOffsetHeader, strconv.FormatInt(offset, 10))
		w.WriteHeader(http.StatusNoContent)
	}
	switch mode {
	case "status":
		ack()
	case "chunk":
		start, err := strconv.ParseInt(r.Header.Get(contract.UploadOffsetHeader), 10, 64)
		if err != nil || start < 0 || start > total || r.ContentLength <= 0 ||
			r.ContentLength > contract.UploadChunkBytes || r.ContentLength > total-start ||
			r.Header.Get("Content-Type") != "application/octet-stream" {
			os.Remove(data)
			refuse(400, "bad-request", "invalid upload chunk")
			return
		}
		if start < offset {
			w.Header().Set("Connection", "close")
			ack()
			return
		}
		if start != offset {
			refuse(409, "upload-offset", "the chunk is ahead of the staged bytes")
			return
		}
		g.transfers.Add(1)
		defer g.transfers.Add(-1)
		if a := g.stageChunk(w, r, q, data, offset, stall); a != nil {
			refuse(a.status, a.err, "the chunk was not acknowledged")
			return
		}
		offset += r.ContentLength
		ack()
	case "commit":
		if offset != total {
			refuse(409, "upload-incomplete", "stage the missing bytes before committing")
			return
		}
		g.transfers.Add(1)
		defer g.transfers.Add(-1)
		g.commitUpload(w, r, q, meta, data, marker, total)
	case "abort":
		if err := os.Remove(data); err != nil && !errors.Is(err, os.ErrNotExist) {
			refuse(507, "staging-failed", "the stage could not be removed")
			return
		}
		offset = 0
		ack()
	default:
		refuse(400, "bad-request", "unknown upload mode")
	}
}

type chunkReader struct {
	r       io.Reader
	control *http.ResponseController
	stall   time.Duration
	drained *atomic.Bool
}

func (r chunkReader) Read(p []byte) (int, error) {
	r.control.SetReadDeadline(time.Now().Add(r.stall))
	if r.drained.Load() {
		return 0, io.ErrClosedPipe
	}
	return r.r.Read(p)
}

func (g *gateway) stageChunk(w http.ResponseWriter, r *http.Request, q *request, path string, offset int64, stall time.Duration) *answer {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return refusal(507, "error", "staging-failed", "cannot open upload stage")
	}
	defer file.Close()
	var drained atomic.Bool
	control := http.NewResponseController(w)
	g.stagingLock.Lock()
	g.staging[q.id] = func() { drained.Store(true); control.SetReadDeadline(time.Now()) }
	drained.Store(g.draining.Load())
	g.stagingLock.Unlock()
	n, err := io.Copy(file, chunkReader{r.Body, control, stall, &drained})
	g.stagingLock.Lock()
	delete(g.staging, q.id)
	g.stagingLock.Unlock()
	control.SetReadDeadline(time.Time{})
	if err == nil && n == r.ContentLength && file.Sync() == nil && syncDir(g.uploadDir()) == nil {
		return nil
	}
	if err := file.Truncate(offset); err != nil {
		os.Remove(path)
		return refusal(507, "error", "staging-failed", "cannot restore the confirmed offset")
	}
	if drained.Load() {
		return refusal(503, "error", "draining", "the gateway is stopping")
	}
	return refusal(408, "error", "upload-interrupted", "retry the interrupted chunk")
}

func (g *gateway) commitUpload(w http.ResponseWriter, r *http.Request, q *request, meta uploadMetadata, data, marker string, total int64) {
	file, err := os.Open(data)
	if err != nil {
		g.finish(w, q, refusal(507, "error", "staging-failed", "cannot read staged upload"), nil)
		return
	}
	defer file.Close()
	// Keep the stage if drain starts before the commit marker is written.
	discard := true
	defer func() {
		if discard {
			os.Remove(data)
		}
	}()
	patterns := g.uploadSecrets(q)
	hash, scan := sha256.New(), newScanner(patterns)
	buf := make([]byte, copyBytes)
	reader := io.NewSectionReader(file, 0, total)
	for {
		if g.draining.Load() || r.Context().Err() != nil {
			discard = false
			g.finish(w, q, refusal(503, "error", "draining", "the file was not sent"), nil)
			return
		}
		n, err := reader.Read(buf)
		hash.Write(buf[:n])
		if scan.write(buf[:n]) {
			break
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			g.finish(w, q, refusal(507, "error", "staging-failed", "cannot read staged upload"), nil)
			return
		}
	}
	if scan.found || g.uploadHasSecret(q, meta, file, patterns) {
		g.finish(w, q, refusal(422, "error", "secret-found", "the file, its name or media type contains a key value or the caller's token"), nil)
		return
	}
	if hex.EncodeToString(hash.Sum(nil)) != meta.sha {
		g.finish(w, q, refusal(400, "error", "sha-mismatch", "the staged bytes do not match X-Fleet-Sha256"), nil)
		return
	}
	transfer := &contract.CallTransfer{Direction: contract.CallTransferDirectionUpload, Bytes: total, MediaType: meta.mediaType, Name: meta.name, Sha256: &meta.sha}
	discard = !g.postUpload(w, r, q, meta, file, transfer, func() error {
		return recordUploadCommit(marker, q.id)
	})
}

func recordUploadCommit(path, callID string) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString(callID); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (g *gateway) expireUploads(now time.Time) {
	entries, err := os.ReadDir(g.uploadDir())
	if err != nil {
		logf("read resumable uploads: %v", err)
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".data" && ext != ".commit" {
			continue
		}
		key := strings.TrimSuffix(e.Name(), ext)
		const idLength = 10 + 1 + 32
		if len(key) != idLength+1+64 || key[idLength] != '-' || !isHex(key[idLength+1:], 64) {
			continue
		}
		created, valid := uploadCreation(key[:idLength])
		if !valid || created.Add(uploadLifetime).After(now) {
			continue
		}
		if _, busy := g.uploadLocks.LoadOrStore(key, struct{}{}); !busy {
			if err := os.Remove(filepath.Join(g.uploadDir(), e.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
				logf("expire resumable upload: %v", err)
			}
			g.uploadLocks.Delete(key)
		}
	}
}
