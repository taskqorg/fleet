package main

import (
	"context"
	"crypto/sha256"
	"encoding"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// Operator uploads: message attachments (docs/designs/fleet-conversations.md, Attachments). artifact_upload_begin
// records the upload as an upload row in the person's scope (owner, project, session, size, the store's multipart
// upload on a staging key, the parts received and the running SHA-256); nothing is spooled on the coordinator's disk.
// PUT /api/uploads/{id} takes the parts in order on the part grid of the upload (uploadGrid); each part goes straight to the store
// as one multipart part and advances the record. The part that completes the file completes the multipart upload,
// copies the staging object to its content key (promote, as artifactVerified does) and records a stored artifact with
// role 'attachment'. Each step is a short transaction around one store call and repeats safely: a retried part after a
// failed finish finishes again. The file has no size limit beyond store capacity (data foundation §3).

// uploadPartBytes: 8 MiB parts: above S3's 5 MiB minimum for every part but the last, far under Cloudflare's 100 MB
// request limit, and small enough to resume cheaply on a slow link. A file of more than uploadMaxParts such parts gets
// larger parts (whole MiB), up to uploadMaxPartBytes, the most one request through Cloudflare carries.
const (
	uploadPartBytes    = 8 << 20
	uploadMaxParts     = 10000 // S3's part count limit
	uploadMaxPartBytes = 95 << 20
)

// uploadGrid: the part grid of a file of size bytes; 0 when even the largest parts cannot carry it.
func uploadGrid(size int64) int64 {
	p := max(int64(uploadPartBytes), ((size+uploadMaxParts-1)/uploadMaxParts+(1<<20)-1)>>20<<20)
	if p > uploadMaxPartBytes {
		return 0
	}
	return p
}

// partTime: how long one part may take to arrive, set only once the caller and the part are known: 30 s plus the part
// at 16 KiB/s (the server's 30 s ReadTimeout is for ordinary requests). A part cut off sooner is sent again.
func partTime(n int64) time.Duration { return 30*time.Second + time.Duration(n/(16<<10))*time.Second }

// uploadStaleAfter: an upload not finished in this time is aborted on the store and its record removed.
const uploadStaleAfter = 24 * time.Hour

var uploadIDPattern = regexp.MustCompile(`^[0-9a-f-]{36}$`)
var contentRangePattern = regexp.MustCompile(`^bytes (\d+)-(\d+)/(\d+)$`)
var contentProbePattern = regexp.MustCompile(`^bytes \*/(\d+)$`)

// finishInline: a finish of a file up to this size runs in the request (a complete and one small copy); a larger one
// runs in the background, so no finish waits on the proxy's request time.
const finishInline = 256 << 20

// uploadLocks: one store write per upload at a time in this coordinator. An entry lives while a request holds or waits
// for it (refs) and goes with the last one, so a held lock is never dropped and none outlives its requests. Across
// coordinators the record's conditional updates decide (saveUpload, the multipart id).
var (
	uploadLocksMu sync.Mutex
	uploadLocks   = map[string]*uploadLockEntry{}
)

type uploadLockEntry struct {
	mu   sync.Mutex
	refs int
}

func uploadLock(id string) func() {
	uploadLocksMu.Lock()
	e := uploadLocks[id]
	if e == nil {
		e = &uploadLockEntry{}
		uploadLocks[id] = e
	}
	e.refs++
	uploadLocksMu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		uploadLocksMu.Lock()
		if e.refs--; e.refs == 0 {
			delete(uploadLocks, id)
		}
		uploadLocksMu.Unlock()
	}
}

// uploadMemory bounds the bytes of part bodies held at once (FLEET_UPLOAD_MEMORY_BYTES, default 1 GiB, at least one
// part of the largest grid).
var uploadMemory = newByteSemaphore(max(int64(uploadMaxPartBytes), envInt64("FLEET_UPLOAD_MEMORY_BYTES", 1<<30)))

func envInt64(k string, d int64) int64 {
	if v, err := strconv.ParseInt(os.Getenv(k), 10, 64); err == nil && v > 0 {
		return v
	}
	return d
}

// byteSemaphore: a weighted semaphore over bytes, first come first served.
type byteSemaphore struct {
	mu    sync.Mutex
	free  int64
	queue []chan struct{}
	want  []int64
}

func newByteSemaphore(n int64) *byteSemaphore { return &byteSemaphore{free: n} }

func (b *byteSemaphore) acquire(ctx context.Context, n int64) error {
	b.mu.Lock()
	if len(b.queue) == 0 && b.free >= n {
		b.free -= n
		b.mu.Unlock()
		return nil
	}
	ready := make(chan struct{})
	b.queue, b.want = append(b.queue, ready), append(b.want, n)
	b.mu.Unlock()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		b.mu.Lock()
		defer b.mu.Unlock()
		select {
		case <-ready: // granted meanwhile: give it back
			b.free += n
			b.wake()
		default:
			for i, c := range b.queue {
				if c == ready {
					b.queue, b.want = append(b.queue[:i], b.queue[i+1:]...), append(b.want[:i], b.want[i+1:]...)
					break
				}
			}
			b.wake()
		}
		return ctx.Err()
	}
}

func (b *byteSemaphore) release(n int64) {
	b.mu.Lock()
	b.free += n
	b.wake()
	b.mu.Unlock()
}

// wake grants waiters in order while the head fits (caller holds mu).
func (b *byteSemaphore) wake() {
	for len(b.queue) > 0 && b.free >= b.want[0] {
		b.free -= b.want[0]
		close(b.queue[0])
		b.queue, b.want = b.queue[1:], b.want[1:]
	}
}

// attachmentName: a file name safe for the store, the agent's input folder and the turn text: the base name only, no
// control or line-separator characters, no leading dots, at most 255 bytes; empty becomes "attachment".
func attachmentName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r >= 0x7f && r < 0xa0 || r == 0x2028 || r == 0x2029 {
			return -1
		}
		return r
	}, s)
	s = strings.ReplaceAll(s, "\\", "/")
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	s = strings.TrimLeft(strings.TrimSpace(s), ".")
	if len(s) > 255 {
		s = strings.ToValidUTF8(s[:255], "")
	}
	if s == "" {
		return "attachment"
	}
	return s
}

func mediaTypeOf(name, given string) string {
	if t, _, err := mime.ParseMediaType(given); err == nil && given != "" {
		return t
	}
	if t := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); t != "" {
		t, _, _ = strings.Cut(t, ";")
		return t
	}
	return "application/octet-stream"
}

// The running SHA-256 of an upload is kept in its record between parts.
func hashState(h hash.Hash) string {
	b, _ := h.(encoding.BinaryMarshaler).MarshalBinary()
	return base64.StdEncoding.EncodeToString(b)
}

func hashFrom(state string) (hash.Hash, error) {
	h := sha256.New()
	if state == "" {
		return h, nil
	}
	b, err := base64.StdEncoding.DecodeString(state)
	if err != nil {
		return nil, err
	}
	return h, h.(encoding.BinaryUnmarshaler).UnmarshalBinary(b)
}

// uploadBegin: the session's project when sessionId names one the caller sees, else projectId under the caller's grant.
func uploadBegin(q *Request, b *contract.ArtifactUploadBeginRequest) (contract.ArtifactUploadBeginReply, error) {
	var none contract.ArtifactUploadBeginReply
	if q.person == nil || q.agent != "" {
		return none, fail(403, "Uploads are made by a person; an agent attaches its artifact_save outputs")
	}
	if b.Size < 0 {
		return none, fail(400, "Invalid size")
	}
	part := uploadGrid(b.Size)
	if part == 0 {
		return none, fail(400, "A file this large is more than the transfer protocol carries (10,000 parts of 95 MiB)")
	}
	name := attachmentName(b.Name)
	u := upload{ID: newID(), Person: q.person.Email, uploadPayload: uploadPayload{Name: name, MediaType: mediaTypeOf(name, deref(b.MediaType)),
		Size: b.Size, PartBytes: part, Parts: []S3Part{}, CreatedAt: nowMs()}}
	var projectID *string
	if sid := deref(b.SessionId); sid != "" {
		s, err := q.db().UploadSessionProject(q.ctx(), sid) // RLS: a session outside the caller's grant is unknown
		if errors.Is(err, pgx.ErrNoRows) {
			return none, errNotFound
		}
		if err != nil {
			return none, err
		}
		projectID = s.ProjectID
		if projectID == nil {
			var p string
			if json.Unmarshal(s.PayloadProject, &p) == nil && p != "" {
				projectID = &p
			}
		}
		u.SessionID = &sid
	} else if b.ProjectId != nil && *b.ProjectId != "" {
		projectID = b.ProjectId
	}
	if !mayAccessProject(q.person, projectID) {
		return none, fail(404, "Unknown project")
	}
	u.ProjectID = projectID
	target, err := selectStore(withTx(q), q.app, artifactLocations(q.app, projectID), "")
	if err != nil {
		return none, err
	}
	if target == nil {
		return none, failCode(409, "No reachable store in the project locations", "no_store")
	}
	// The store's multipart upload starts with the first part (uploadPart), never in this transaction, so a begin that
	// does not commit leaves nothing on the store.
	u.StoreID, u.Staging = target.ID, projectKey(projectID)+"/prod/.pending/upload-"+u.ID
	if err := q.db().UploadInsert(q.ctx(), db.UploadInsertParams{ID: u.ID, Person: u.Person, SessionID: u.SessionID, ProjectID: u.ProjectID,
		Payload: jsonBytes(u.uploadPayload)}); err != nil {
		return none, err
	}
	sweepUploads(q.app)
	return contract.ArtifactUploadBeginReply{UploadId: u.ID, Url: "/api/uploads/" + u.ID, PartBytes: part}, nil
}

// upload: the upload row (migration 82, queries/uploads.sql): the fields a write guards or a reference names are
// columns, uploadPayload the rest.
type upload struct {
	ID, Person           string
	SessionID, ProjectID *string
	Received             int64
	Multipart            *string // the store's multipart upload, once the first part started it
	ArtifactID           *string // the stored attachment, once finished
	uploadPayload
}

type uploadPayload struct {
	Name      string   `json:"name"`
	MediaType string   `json:"mediaType"`
	Size      int64    `json:"size"`
	PartBytes int64    `json:"partBytes"`
	Parts     []S3Part `json:"parts"`
	CreatedAt int64    `json:"createdAt"`
	StoreID   string   `json:"storeId"`
	Staging   string   `json:"staging"`
	Sha       string   `json:"sha,omitempty"`       // the running SHA-256 of the parts received (hashState)
	Completed bool     `json:"completed,omitempty"` // the store's upload is complete: the staging object holds the file
}

func uploadOf(r db.UploadGetRow) (upload, error) {
	u := upload{ID: r.ID, Person: r.Person, SessionID: r.SessionID, ProjectID: r.ProjectID, Received: r.Received, Multipart: r.Multipart, ArtifactID: r.ArtifactID}
	return u, json.Unmarshal(r.Payload, &u.uploadPayload)
}

// uploadProgress: the answer of a part, a probe and a finish that runs in the background, and the extra fields of a
// conflict: how much of the file the coordinator has.
type uploadProgress struct {
	Received  int64 `json:"received"`
	Finishing bool  `json:"finishing,omitempty"`
}

func uploadConflict(code int, msg string, received int64) error {
	return &HTTPError{Code: code, Msg: msg, Extra: docOf(uploadProgress{Received: received})}
}

// sweepUploads, in the background after a begin: aborts uploads left unfinished for uploadStaleAfter (the store's
// multipart upload and the record) and drops finished records that old (they only answer a retried last part).
var sweepUploadsSoon = coalesced("upload-sweep", func(ctx context.Context, a *App) error {
	sweepUploadsNow(ctx, a)
	return nil
})

func sweepUploads(a *App) {
	if a != nil {
		sweepUploadsSoon(a)
	}
}

var sweeping sync.Mutex

// sweepUploadsNow: one sweep, at most one at a time in this coordinator.
func sweepUploadsNow(ctx context.Context, a *App) {
	if !sweeping.TryLock() {
		return
	}
	defer sweeping.Unlock()
	age := uploadStaleAfter.Seconds()
	var stale []upload
	err := a.store.Tx(ctx, nil, false, "", func(tx pgx.Tx) error {
		rs, err := db.New(tx).UploadsStale(ctx, age)
		stale = nil
		for _, r := range rs {
			if u, err := uploadOf(db.UploadGetRow(r)); err == nil {
				stale = append(stale, u)
			}
		}
		return err
	})
	if err != nil {
		return
	}
	for _, u := range stale {
		// The store lets go of an unfinished upload first; a record whose abort failed stays for the next sweep.
		if u.ArtifactID == nil && deref(u.Multipart) != "" {
			s, err := storeGet(ctx, a, u.StoreID)
			if err != nil {
				continue
			}
			if s == nil { // the store is gone, and its uploads with it: only the record is left
				goto drop
			}
			t, err := tunnel(s)
			if err != nil {
				continue
			}
			if err := t.AbortMultipartUpload(s.Bucket, u.Staging, *u.Multipart); err != nil {
				var se *S3Error
				if !errors.As(err, &se) || se.Status != 404 {
					continue
				}
			}
			t.Delete(s.Bucket, u.Staging)
		}
	drop:
		a.store.Tx(ctx, nil, true, NOLOCK, func(tx pgx.Tx) error {
			return db.New(tx).UploadDrop(ctx, db.UploadDropParams{ID: u.ID, AgeSeconds: age})
		})
	}
}

// uploadRecord reads the upload in the person's scope; only the person who began it may write it.
func (a *App) uploadRecord(ctx context.Context, p *Person, id string) (upload, error) {
	var u upload
	err := a.store.Tx(ctx, scopeOf(p), false, "", func(tx pgx.Tx) error {
		r, err := db.New(tx).UploadGet(ctx, id)
		if err != nil {
			return err
		}
		u, err = uploadOf(r)
		return err
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return u, err // a database failure is the server's, and the client sends the part again
	}
	if err != nil || u.Person != p.Email {
		return u, fail(404, "Unknown upload")
	}
	return u, nil
}

// saveUpload writes the record in the person's scope if it still has the received count it was read with (another
// coordinator may have taken the same part meanwhile: 409, the client resumes); extra runs in the same transaction.
// The record of a finished upload (artifactId) takes no more writes, so two finishes record one artifact.
func (a *App) saveUpload(ctx context.Context, p *Person, u upload, received int64, extra func(pgx.Tx) error) error {
	return a.store.Tx(ctx, scopeOf(p), true, NOLOCK, func(tx pgx.Tx) error {
		n, err := db.New(tx).UploadSave(ctx, db.UploadSaveParams{ID: u.ID, Received: u.Received, Multipart: u.Multipart, ArtifactID: u.ArtifactID,
			Payload: jsonBytes(u.uploadPayload), WasReceived: received})
		if err != nil {
			return err
		}
		if n == 0 {
			return uploadConflict(409, "Upload changed meanwhile", received)
		}
		if extra != nil {
			return extra(tx)
		}
		return nil
	})
}

// uploadPart: PUT /api/uploads/{id} with Content-Range "bytes first-last/size" on the part grid. A part that does not
// start where the file ends answers 409 with received, so a client resumes; a part already received answers received
// (and finishes the upload when the file is complete, so a retry after a failed finish succeeds). The part that
// completes the file answers the stored artifact.
func (a *App) uploadPart(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := &Request{r: r, app: a}
	out, err := func() (any, error) {
		if err := a.authorize(q); err != nil {
			return nil, err
		}
		if q.person == nil || q.agent != "" { // the gate of artifact_upload_begin
			return nil, fail(403, "Uploads are made by a person")
		}
		id := r.PathValue("id")
		if !uploadIDPattern.MatchString(id) {
			return nil, fail(404, "Unknown upload")
		}
		ctx := context.WithoutCancel(r.Context())
		u, err := a.uploadRecord(ctx, q.person, id)
		if err != nil {
			return nil, err
		}
		if aid := deref(u.ArtifactID); aid != "" {
			return a.uploadView(ctx, q.person, aid)
		}
		size, have, part := u.Size, u.Received, u.PartBytes
		if size == 0 {
			return a.finishLocked(ctx, q.person, id)
		}
		// A bodyless probe ("bytes */size") asks where the upload stands, and finishes a whole file.
		if g := contentProbePattern.FindStringSubmatch(r.Header.Get("Content-Range")); g != nil {
			if total, _ := strconv.ParseInt(g[1], 10, 64); total != size {
				return nil, fail(400, "Content-Range does not fit the upload")
			}
			if have == size {
				return a.finishLocked(ctx, q.person, id)
			}
			return uploadProgress{Received: have}, nil
		}
		g := contentRangePattern.FindStringSubmatch(r.Header.Get("Content-Range"))
		if g == nil {
			return nil, fail(400, "Expected Content-Range: bytes first-last/size")
		}
		first, _ := strconv.ParseInt(g[1], 10, 64)
		last, _ := strconv.ParseInt(g[2], 10, 64)
		total, _ := strconv.ParseInt(g[3], 10, 64)
		if total != size || part <= 0 || first%part != 0 || last != min(first+part, size)-1 {
			return nil, fail(400, "Content-Range does not fit the upload's parts of "+strconv.FormatInt(part, 10)+" bytes")
		}
		if last < have { // already received: a retried part
			if have == size {
				return a.finishLocked(ctx, q.person, id)
			}
			return uploadProgress{Received: have}, nil
		}
		if first != have {
			return nil, uploadConflict(409, "Part out of order", have)
		}
		// Only now, for the person who began this upload and a part on its grid, may the request body take longer. The
		// body is read before the upload's lock, so a stalled request never holds back a retry of the same part.
		http.NewResponseController(w).SetReadDeadline(time.Now().Add(partTime(last - first + 1)))
		// The part's bytes are reserved from the coordinator's upload memory before its buffer exists, so slow or empty
		// requests cannot hold more than that between them; a request that waits past its deadline is sent again (503).
		need := last - first + 1
		wctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		err = uploadMemory.acquire(wctx, need)
		cancel()
		if err != nil {
			return nil, uploadConflict(503, "Upload memory busy; send the part again", have)
		}
		held := true
		free := func() {
			if held {
				held = false
				uploadMemory.release(need)
			}
		}
		defer free()
		body := make([]byte, need)
		if _, err := io.ReadFull(r.Body, body); err != nil {
			// A cut-off body (slow link, deadline, proxy): nothing was taken; the client sends the part again.
			return nil, uploadConflict(409, "Part cut off; send it again", have)
		}
		defer uploadLock(id)()
		if u, err = a.uploadRecord(ctx, q.person, id); err != nil {
			return nil, err
		}
		if u.Received != first || u.ArtifactID != nil {
			return nil, uploadConflict(409, "Upload changed meanwhile", u.Received)
		}
		h, err := hashFrom(u.Sha)
		if err != nil {
			return nil, err
		}
		h.Write(body)
		s, t, err := a.uploadStore(ctx, u)
		if err != nil {
			return nil, err
		}
		if deref(u.Multipart) == "" {
			mp, err := a.startMultipart(ctx, q.person, id, s, t, u)
			if err != nil {
				return nil, err
			}
			u.Multipart = &mp
		}
		n := int(first/part) + 1
		etag, err := t.UploadPart(s.Bucket, u.Staging, *u.Multipart, n, body)
		body = nil
		free() // the store has the bytes: the reservation is not held through the record write and a finish
		if err != nil {
			return nil, err
		}
		u.Parts = append(u.Parts, S3Part{PartNumber: n, ETag: etag})
		u.Received, u.Sha = last+1, hashState(h)
		if err := a.saveUpload(ctx, q.person, u, first, nil); err != nil {
			return nil, err
		}
		if last+1 < size {
			return uploadProgress{Received: last + 1}, nil
		}
		return a.finishUpload(ctx, q.person, id)
	}()
	if err != nil {
		writeError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(out)
}

// startMultipart starts the store's multipart upload and records its id only if the record has none; a request that
// loses that race (another coordinator) aborts its own and uses the recorded one, so one id is ever used.
func (a *App) startMultipart(ctx context.Context, p *Person, id string, s *StoreRow, t *S3, u upload) (string, error) {
	mp, err := t.CreateMultipartUpload(s.Bucket, u.Staging, u.MediaType)
	if err != nil {
		return "", err
	}
	won := false
	err = a.store.Tx(ctx, scopeOf(p), true, NOLOCK, func(tx pgx.Tx) error {
		n, err := db.New(tx).UploadMultipartSet(ctx, db.UploadMultipartSetParams{ID: id, Multipart: &mp})
		won = err == nil && n == 1
		return err
	})
	if err != nil || !won {
		t.AbortMultipartUpload(s.Bucket, u.Staging, mp)
		if err != nil {
			return "", err
		}
		fresh, err := a.uploadRecord(ctx, p, id)
		if err != nil || deref(fresh.Multipart) == "" {
			return "", uploadConflict(409, "Upload changed meanwhile", u.Received)
		}
		return *fresh.Multipart, nil
	}
	return mp, nil
}

func (a *App) uploadStore(ctx context.Context, u upload) (*StoreRow, *S3, error) {
	s, err := storeGet(ctx, a, u.StoreID)
	if err != nil || s == nil {
		return nil, nil, failCode(409, "The upload's store is gone; begin again", "no_store")
	}
	t, err := tunnel(s)
	return s, t, err
}

// finishLocked: a finish running in the background is answered at once, never queued behind its lock (a probe would
// wait past the proxy's request time); otherwise the finish runs under the upload's lock.
func (a *App) finishLocked(ctx context.Context, p *Person, id string) (any, error) {
	if busy, err := a.uploadFinishing(ctx, p, id); err != nil {
		return nil, err
	} else if busy {
		if u, err := a.uploadRecord(ctx, p, id); err == nil {
			return uploadProgress{Received: u.Size, Finishing: true}, nil
		}
	}
	defer uploadLock(id)()
	return a.finishUpload(ctx, p, id)
}

// uploadFinishing: the upload's finish runs in the background (its upload_finish_work row, upload_work.go).
func (a *App) uploadFinishing(ctx context.Context, p *Person, id string) (bool, error) {
	var busy bool
	err := a.store.Tx(ctx, scopeOf(p), false, "", func(tx pgx.Tx) error {
		var err error
		busy, err = db.New(tx).UploadFinishing(ctx, id)
		return err
	})
	return busy, err
}

// finishUpload (under the upload's lock) answers the artifact when it is recorded, {received, finishing} while a long
// finish runs, and otherwise finishes: in this request up to finishInline, else on the work kit (upload_work.go: completing many parts
// and copying gigabytes could outlast one request through the proxy); the client then probes with "bytes */size".
func (a *App) finishUpload(ctx context.Context, p *Person, id string) (any, error) {
	u, err := a.uploadRecord(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if aid := deref(u.ArtifactID); aid != "" {
		return a.uploadView(ctx, p, aid)
	}
	if busy, err := a.uploadFinishing(ctx, p, id); err != nil || busy {
		return uploadProgress{Received: u.Size, Finishing: true}, err
	}
	if u.Size <= finishInline {
		return a.finishNow(ctx, p, u)
	}
	// The work row is this finish's exclusion: every finish path answers {finishing} while it is there, on any
	// coordinator, and the kit's call takes no lock a probe could queue behind.
	if err := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
		if err := q.db().UploadFinishWorkAdd(q.ctx(), id); err != nil {
			return err
		}
		return kitNotify(q, "upload-finish")
	}); err != nil {
		return nil, err
	}
	return uploadProgress{Received: u.Size, Finishing: true}, nil
}

// finishNow completes the store upload once, copies the staging object to its content key and records the stored
// attachment with its verified copy; each step repeats safely.
func (a *App) finishNow(ctx context.Context, p *Person, u upload) (any, error) {
	s, t, err := a.uploadStore(ctx, u)
	if err != nil {
		return nil, err
	}
	size, staging := u.Size, u.Staging
	if !u.Completed {
		// A finish that completed the store upload but failed to record it completes nothing twice: the staging object
		// of the whole size is there.
		if n, err := t.Head(s.Bucket, staging); err != nil {
			return nil, err
		} else if size > 0 && n == size {
			u.Completed = true
		}
	}
	if !u.Completed {
		if size == 0 {
			err = t.Put(s.Bucket, staging, []byte{}, u.MediaType)
		} else {
			err = t.CompleteMultipartUpload(s.Bucket, staging, deref(u.Multipart), u.Parts)
		}
		if err != nil {
			return nil, err
		}
		u.Completed = true
		if err := a.saveUpload(ctx, p, u, size, nil); err != nil {
			return nil, err
		}
	}
	h, err := hashFrom(u.Sha)
	if err != nil {
		return nil, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	projectID := u.ProjectID
	// As artifactReserve: purged content is never stored again.
	if purged, err := connDB(ctx, a).UploadContentPurged(ctx, db.UploadContentPurgedParams{ProjectKey: projectKey(projectID), ContentHash: sum}); err != nil {
		return nil, err
	} else if purged {
		t.Delete(s.Bucket, staging)
		return nil, failCode(409, "Content was purged", "purged")
	}
	key := keyOf(&Artifact{ProjectID: projectID, Scope: "prod", ContentHash: sum})
	id := newID()
	u.ArtifactID = &id
	err = a.saveUpload(ctx, p, u, size, func(tx pgx.Tx) error {
		// Under the object lock until the copy row commits, as writeObject: a purge of this content either finished
		// before the Head in promote (the object is written again) or waits and then sees the copy (cleanup.go).
		if err := objectLock(ctx, tx, s.ID, key); err != nil {
			return err
		}
		if err := promote(t, s.Bucket, staging, key, size); err != nil {
			return err
		}
		d := db.New(tx)
		if err := d.UploadArtifactInsert(ctx, db.UploadArtifactInsertParams{ID: id, ProjectID: projectID, ContentHash: sum, Size: size,
			MediaType: u.MediaType, Name: u.Name, SessionID: u.SessionID}); err != nil {
			return err
		}
		return d.UploadCopyInsert(ctx, db.UploadCopyInsertParams{ArtifactID: id, StoreID: s.ID, ProjectID: projectID})
	})
	if err != nil {
		return nil, err
	}
	t.Delete(s.Bucket, staging) // the content key holds the bytes; a staging object left here is only space
	return a.uploadView(ctx, p, id)
}

func (a *App) uploadView(ctx context.Context, p *Person, id string) (doc, error) {
	var view doc
	err := a.store.Tx(ctx, scopeOf(p), false, "", func(tx pgx.Tx) error {
		var err error
		view, err = artifactGetView(context.WithValue(ctx, txKey{}, tx), a, id, "")
		return err
	})
	return view, err
}

func init() {
	onStart(func(ctx context.Context, a *App) { // stale uploads go also when nobody begins a new one
		every(ctx, "upload-sweep", time.Hour, func(ctx context.Context) error {
			sweepUploadsNow(ctx, a)
			return nil
		})
	})
	op("artifact_upload_begin", uploadBegin)
	rawRoute("PUT /api/uploads/{id}", (*App).uploadPart)
}
