package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"golang.org/x/sys/unix"
)

const uploadLifetime = 24 * time.Hour

type uploadState struct {
	Binding   string
	Offset    int64
	CallID    string
	StartedAt int64
	Committed bool
}

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
	mode := r.Header.Get(contract.UploadModeHeader)
	admission := admit(q)
	if q.run == nil || (mode != "abort" && admission != nil && admission.err == "not-granted") {
		refuse(admission.status, admission.err, "the upload is not admitted")
		return
	}
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
	// The token and ID select one state record regardless of the request's metadata.
	binding := sha(strings.Join([]string{q.connection, q.tool, r.Header.Get("X-Fleet-Args"), r.Header.Get("X-Fleet-Sha256"), strconv.FormatInt(total, 10)}, "\x00"))
	key := id + "-" + sha(q.run.Sha256)
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
	data, statePath := base+".data", base+".state"
	state := uploadState{Binding: binding}
	if saved, err := os.ReadFile(statePath); err == nil {
		if json.Unmarshal(saved, &state) != nil || state.Binding == "" || state.Offset < 0 || state.Committed && state.CallID == "" {
			refuse(507, "staging-failed", "the upload record is invalid")
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		refuse(507, "staging-failed", "the upload record could not be read")
		return
	}
	if state.Binding != binding {
		refuse(409, "upload-binding", "the upload metadata changed")
		return
	}
	if state.Committed {
		// A crash may leave staged bytes behind the commit record.
		os.Remove(data)
		w.Header().Set("X-Fleet-Call-Id", state.CallID)
		w.Header().Set("X-Fleet-Outcome", "unknown")
		w.Header().Set("X-Fleet-Error", "upload-committed")
		writeJSON(w, http.StatusConflict, map[string]string{"error": "upload-committed", "reason": "read back the attachment before another upload"})
		return
	}

	if mode != "abort" {
		if admission != nil {
			refuse(admission.status, admission.err, "the upload is not admitted")
			return
		}
		if g.draining.Load() {
			refuse(503, "draining", "the gateway is stopping")
			return
		}
	}
	var meta uploadMetadata
	if mode != "abort" {
		var a *answer
		meta, a = parseUploadMetadata(r, q.t)
		if a != nil {
			refuse(a.status, a.err, "invalid upload metadata")
			return
		}
	}
	cap, _, stall, _ := limits(q.c)
	if mode != "abort" && cap > 0 && total > cap {
		a := refusal(413, "error", "file-too-large", "the whole file exceeds the connection's limit")
		if err := g.abandonUpload(q, data, statePath, state, a); err != nil {
			refuse(507, "staging-failed", "the stage could not be removed")
			return
		}
		refuse(a.status, a.err, "the whole file exceeds the connection's limit")
		return
	}
	offset := state.Offset
	ack := func() {
		w.Header().Set(contract.UploadOffsetHeader, strconv.FormatInt(offset, 10))
		w.WriteHeader(http.StatusNoContent)
	}
	switch mode {
	case "status":
		if offset != 0 && syncDir(g.uploadDir()) != nil {
			refuse(507, "staging-failed", "the confirmed offset could not be synced")
			return
		}
		ack()
	case "chunk":
		start, err := strconv.ParseInt(r.Header.Get(contract.UploadOffsetHeader), 10, 64)
		if err != nil || start < 0 || start > total || r.ContentLength <= 0 ||
			r.ContentLength > contract.UploadChunkBytes || r.ContentLength > total-start ||
			r.Header.Get("Content-Type") != "application/octet-stream" {
			refuse(400, "bad-request", "invalid upload chunk")
			return
		}
		if start < offset {
			w.Header().Set("Connection", "close")
			if syncDir(g.uploadDir()) != nil {
				refuse(507, "staging-failed", "the confirmed offset could not be synced")
				return
			}
			ack()
			return
		}
		if start != offset {
			refuse(409, "upload-offset", "the chunk is ahead of the staged bytes")
			return
		}
		g.transfers.Add(1)
		defer g.transfers.Add(-1)
		if !g.reserveUploadSpace(r.ContentLength) {
			refuse(507, "staging-failed", "insufficient space for this upload chunk")
			return
		}
		defer g.releaseUploadSpace(r.ContentLength)
		if a := g.stageChunk(w, r, q, data, offset, stall); a != nil {
			refuse(a.status, a.err, "the chunk was not acknowledged")
			return
		}
		offset += r.ContentLength
		state.Offset = offset
		if err := writeUploadState(statePath, state); err != nil {
			refuse(507, "staging-failed", "the confirmed offset could not be recorded")
			return
		}
		ack()
	case "commit":
		if offset != total {
			refuse(409, "upload-incomplete", "stage the missing bytes before committing")
			return
		}
		g.transfers.Add(1)
		defer g.transfers.Add(-1)
		g.commitUpload(w, r, q, meta, data, statePath, state)
	case "abort":
		a := refusal(409, "error", "upload-aborted", "the staged upload was removed without a provider POST")
		if err := g.abandonUpload(q, data, statePath, state, a); err != nil {
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
	info, err := file.Stat()
	if err != nil || info.Size() < offset || (info.Size() > offset && file.Truncate(offset) != nil) {
		return refusal(507, "error", "staging-failed", "cannot restore the confirmed offset")
	}
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
	if err == nil && n == r.ContentLength {
		if err := file.Sync(); err != nil {
			return refusal(507, "error", "staging-failed", "the chunk could not be synced")
		}
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

func (g *gateway) commitUpload(w http.ResponseWriter, r *http.Request, q *request, meta uploadMetadata, data, statePath string, state uploadState) {
	if state.CallID != "" {
		q.id, q.startedMs = state.CallID, state.StartedAt
		q.begun = time.UnixMilli(q.startedMs)
		q.phase = ptr(contract.CallPhaseCompleted)
	}
	total := state.Offset
	file, err := os.Open(data)
	if err != nil {
		discardUpload(data, statePath, state)
		g.finish(w, q, refusal(507, "error", "staging-failed", "cannot read staged upload"), nil)
		return
	}
	defer file.Close()
	// Keep the stage if drain starts before commit.
	discard := true
	defer func() {
		if discard {
			discardUpload(data, statePath, state)
		}
	}()
	patterns := g.uploadSecrets(q)
	hash, scan := sha256.New(), newScanner(patterns)
	buf := make([]byte, copyBytes)
	reader := io.NewSectionReader(file, 0, total)
	for {
		if g.draining.Load() || r.Context().Err() != nil {
			discard = false
			replyCall(w, q, refusal(503, "error", "draining", "the file was not sent"))
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
	discard = !g.postUpload(w, r, q, meta, file, transfer, func(admitted *request) (func() error, *answer) {
		// Persist the call ID before spooling intent. Replaying a pending intent reuses the same accounting row.
		if state.CallID == "" {
			state.CallID, state.StartedAt = admitted.id, admitted.startedMs
			if err := writeUploadState(statePath, state); err != nil {
				return nil, refusal(507, "error", "staging-failed", "the intent could not be recorded")
			}
		}
		started := g.line(admitted, &answer{outcome: "unknown", err: "upload-unfinished"})
		started.Phase, started.Transfer = ptr(contract.CallPhaseStarted), transfer
		admitted.phase = ptr(contract.CallPhaseCompleted)
		if !g.spool.append(started) {
			return nil, refusal(503, "error", "spool-failed", "the call could not be recorded; the file was not sent")
		}
		committed := state
		committed.Committed = true
		if err := writeUploadState(statePath, committed); err != nil {
			return nil, refusal(507, "error", "staging-failed", "the commit could not be recorded")
		}
		pending := state
		state = committed
		return func() error {
			if err := writeUploadState(statePath, pending); err != nil {
				return err
			}
			state = pending
			return nil
		}, nil
	})
}

// The data is synced before this record advances. A process crash can leave a tail in the data file or a .next
// file. Neither advances the offset. A commit's call ID is published with the same atomic replacement.
func writeUploadState(path string, state uploadState) error {
	next := path + ".next"
	f, err := os.OpenFile(next, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer os.Remove(next)
	err = json.NewEncoder(f).Encode(state)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(next, path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (g *gateway) abandonUpload(q *request, data, statePath string, state uploadState, a *answer) error {
	if state.CallID != "" {
		// Remove the data durably before publishing a terminal outcome. Retain the intent until it is recorded.
		if err := removeUploadData(data); err != nil {
			return err
		}
		if err := syncDir(g.uploadDir()); err != nil {
			return err
		}
		original := *q
		original.id, original.startedMs = state.CallID, state.StartedAt
		original.begun = time.UnixMilli(original.startedMs)
		line := g.line(&original, a)
		line.Phase = ptr(contract.CallPhaseCompleted)
		if !g.spool.append(line) {
			return errors.New("cannot record discarded upload")
		}
	}
	return discardUpload(data, statePath, state)
}

func discardUpload(data, statePath string, state uploadState) error {
	if state.Offset != 0 && !state.Committed {
		state.Offset, state.CallID, state.StartedAt = 0, "", 0
		if err := writeUploadState(statePath, state); err != nil {
			return err
		}
	}
	return removeUploadData(data)
}

func removeUploadData(data string) error {
	err := os.Remove(data)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// Staged and abandoned bytes already count against statfs. Also reserve space for concurrent raw uploads and
// chunks that have not hit disk yet, leaving filesystem headroom for the spool and other services.
func (g *gateway) reserveUploadSpace(bytes int64) bool {
	g.uploadSpaceLock.Lock()
	defer g.uploadSpaceLock.Unlock()
	var disk unix.Statfs_t
	if err := unix.Statfs(g.uploadDir(), &disk); err != nil {
		return false
	}
	blockSize := uint64(disk.Bsize)
	free := uint64(disk.Bavail) * blockSize
	const reserve = 64 << 20
	if free < reserve || free-reserve < g.uploadReserved || free-reserve-g.uploadReserved < uint64(bytes) {
		return false
	}
	g.uploadReserved += uint64(bytes)
	return true
}

func (g *gateway) releaseUploadSpace(bytes int64) {
	g.uploadSpaceLock.Lock()
	g.uploadReserved -= uint64(bytes)
	g.uploadSpaceLock.Unlock()
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
		const idLength = 10 + 1 + 32
		const keyLength = idLength + 1 + 64
		name := e.Name()
		if len(name) <= keyLength {
			continue
		}
		key, suffix := name[:keyLength], name[keyLength:]
		if suffix != ".data" && suffix != ".state" && suffix != ".state.next" {
			continue
		}
		if key[idLength] != '-' || !isHex(key[idLength+1:], 64) {
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
