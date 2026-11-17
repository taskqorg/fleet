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
	"log"
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

	"github.com/jackc/pgx/v5"
)

// Operator uploads: message attachments (docs/designs/fleet-conversations.md, Attachments). artifact_upload_begin
// records the upload as an 'upload' document in the person's scope (owner, project, session, size, the store's multipart
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

// finishing: uploads whose finish runs in the background (a copy too long for one request); a retried part answers
// {received, finishing} until the record names the artifact.
var finishing sync.Map

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
	u := doc{"id": newID(), "person": q.person.Email, "name": name, "mediaType": mediaTypeOf(name, deref(b.MediaType)), "size": b.Size, "partBytes": part, "received": 0, "parts": []any{}, "createdAt": nowMs()}
	var projectID *string
	if sid := deref(b.SessionId); sid != "" {
		s, err := get(q, sid, "session") // RLS: a session outside the caller's grant is unknown
		if err != nil {
			return none, err
		}
		u["sessionId"], projectID = sid, strPtr(s["projectId"])
	} else if b.ProjectId != nil && *b.ProjectId != "" {
		projectID = b.ProjectId
	}
	if !mayAccessProject(q.person, projectID) {
		return none, fail(404, "Unknown project")
	}
	if projectID != nil {
		u["projectId"] = *projectID
	}
	target, err := selectStore(withTx(q), q.app, artifactLocations(q.app, projectID), "")
	if err != nil {
		return none, err
	}
	if target == nil {
		return none, failCode(409, "No reachable store in the project locations", "no_store")
	}
	// The store's multipart upload starts with the first part (uploadPart), never in this transaction, so a begin that
	// does not commit leaves nothing on the store.
	u["storeId"], u["staging"] = target.ID, projectKey(projectID)+"/prod/.pending/upload-"+astr(u["id"])
	if err := put(q, "upload", u); err != nil {
		return none, err
	}
	sweepUploads(q.app)
	return contract.ArtifactUploadBeginReply{UploadId: astr(u["id"]), Url: "/api/uploads/" + astr(u["id"]), PartBytes: part}, nil
}

var sweeping sync.Mutex

// sweepUploads, in the background after a begin: aborts uploads left unfinished for uploadStaleAfter (the store's
// multipart upload and the record) and drops finished records that old (they only answer a retried last part).
func sweepUploads(a *App) {
	if a == nil || !sweeping.TryLock() {
		return
	}
	go func() {
		defer sweeping.Unlock()
		sweepUploadsNow(context.Background(), a)
	}()
}

// sweepUploadsNow: one sweep (the caller holds sweeping).
func sweepUploadsNow(ctx context.Context, a *App) {
	{
		age := strconv.Itoa(int(uploadStaleAfter.Seconds())) + " seconds"
		var stale []doc
		err := a.store.Tx(ctx, nil, false, "", func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "SELECT data FROM document WHERE kind='upload' AND updated_at < now() - $1::interval", age)
			if err != nil {
				return err
			}
			for rows.Next() {
				var raw []byte
				var u doc
				if rows.Scan(&raw) == nil && json.Unmarshal(raw, &u) == nil {
					stale = append(stale, u)
				}
			}
			return rows.Err()
		})
		if err != nil {
			return
		}
		for _, u := range stale {
			// The store lets go of an unfinished upload first; a record whose abort failed stays for the next sweep.
			if u["artifactId"] == nil && astr(u["multipart"]) != "" {
				s, err := storeGet(ctx, a, astr(u["storeId"]))
				if err != nil || s == nil {
					continue
				}
				t, err := tunnel(s)
				if err != nil {
					continue
				}
				if err := t.AbortMultipartUpload(s.Bucket, astr(u["staging"]), astr(u["multipart"])); err != nil {
					var se *S3Error
					if !errors.As(err, &se) || se.Status != 404 {
						continue
					}
				}
				t.Delete(s.Bucket, astr(u["staging"]))
			}
			a.store.Tx(ctx, nil, true, NOLOCK, func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "DELETE FROM document WHERE kind='upload' AND id=$1 AND updated_at < now() - $2::interval", u["id"], age)
				return err
			})
		}
	}
}

// uploadRecord reads the upload in the person's scope; only the person who began it may write it.
func (a *App) uploadRecord(ctx context.Context, p *Person, id string) (doc, error) {
	var u doc
	err := a.store.Tx(ctx, scopeOf(p), false, "", func(tx pgx.Tx) error {
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT data FROM document WHERE kind='upload' AND id=$1", id).Scan(&raw); err != nil {
			return err
		}
		return json.Unmarshal(raw, &u)
	})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err // a database failure is the server's, and the client sends the part again
	}
	if err != nil || astr(u["person"]) != p.Email {
		return nil, fail(404, "Unknown upload")
	}
	return u, nil
}

// saveUpload writes the record in the person's scope if it still has the received count it was read with (another
// coordinator may have taken the same part meanwhile: 409, the client resumes); extra runs in the same transaction.
// The record of a finished upload (artifactId) takes no more writes, so two finishes record one artifact.
func (a *App) saveUpload(ctx context.Context, p *Person, u doc, received int64, extra func(pgx.Tx) error) error {
	return a.store.Tx(ctx, scopeOf(p), true, NOLOCK, func(tx pgx.Tx) error {
		raw, _ := json.Marshal(u)
		tag, err := tx.Exec(ctx, "UPDATE document SET data=$2,updated_at=now() WHERE kind='upload' AND id=$1 AND (data->>'received')::bigint=$3 AND NOT (data ? 'artifactId')", u["id"], raw, received)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return &HTTPError{Code: 409, Msg: "Upload changed meanwhile", Extra: map[string]any{"received": received}}
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
		if aid := astr(u["artifactId"]); aid != "" {
			return a.uploadView(ctx, q.person, aid)
		}
		size, have, part := int64(num(u["size"])), int64(num(u["received"])), int64(num(u["partBytes"]))
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
			return doc{"received": have}, nil
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
			return doc{"received": have}, nil
		}
		if first != have {
			return nil, &HTTPError{Code: 409, Msg: "Part out of order", Extra: map[string]any{"received": have}}
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
			return nil, &HTTPError{Code: 503, Msg: "Upload memory busy; send the part again", Extra: map[string]any{"received": have}}
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
			return nil, &HTTPError{Code: 409, Msg: "Part cut off; send it again", Extra: map[string]any{"received": have}}
		}
		defer uploadLock(id)()
		if u, err = a.uploadRecord(ctx, q.person, id); err != nil {
			return nil, err
		}
		if int64(num(u["received"])) != first || u["artifactId"] != nil {
			return nil, &HTTPError{Code: 409, Msg: "Upload changed meanwhile", Extra: map[string]any{"received": int64(num(u["received"]))}}
		}
		h, err := hashFrom(astr(u["sha"]))
		if err != nil {
			return nil, err
		}
		h.Write(body)
		s, t, err := a.uploadStore(ctx, u)
		if err != nil {
			return nil, err
		}
		if astr(u["multipart"]) == "" {
			if u["multipart"], err = a.startMultipart(ctx, q.person, id, s, t, u); err != nil {
				return nil, err
			}
		}
		n := int(first/part) + 1
		etag, err := t.UploadPart(s.Bucket, astr(u["staging"]), astr(u["multipart"]), n, body)
		body = nil
		free() // the store has the bytes: the reservation is not held through the record write and a finish
		if err != nil {
			return nil, err
		}
		u["parts"] = append(anys(u["parts"]), doc{"partNumber": n, "etag": etag})
		u["received"], u["sha"] = last+1, hashState(h)
		if err := a.saveUpload(ctx, q.person, u, first, nil); err != nil {
			return nil, err
		}
		if last+1 < size {
			return doc{"received": last + 1}, nil
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
func (a *App) startMultipart(ctx context.Context, p *Person, id string, s *StoreRow, t *S3, u doc) (string, error) {
	mp, err := t.CreateMultipartUpload(s.Bucket, astr(u["staging"]), astr(u["mediaType"]))
	if err != nil {
		return "", err
	}
	won := false
	err = a.store.Tx(ctx, scopeOf(p), true, NOLOCK, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE document SET data=jsonb_set(data,'{multipart}',to_jsonb($2::text)),updated_at=now() WHERE kind='upload' AND id=$1 AND NOT (data ? 'multipart')", id, mp)
		won = err == nil && tag.RowsAffected() == 1
		return err
	})
	if err != nil || !won {
		t.AbortMultipartUpload(s.Bucket, astr(u["staging"]), mp)
		if err != nil {
			return "", err
		}
		fresh, err := a.uploadRecord(ctx, p, id)
		if err != nil || astr(fresh["multipart"]) == "" {
			return "", &HTTPError{Code: 409, Msg: "Upload changed meanwhile", Extra: map[string]any{"received": int64(num(u["received"]))}}
		}
		return astr(fresh["multipart"]), nil
	}
	return mp, nil
}

func (a *App) uploadStore(ctx context.Context, u doc) (*StoreRow, *S3, error) {
	s, err := storeGet(ctx, a, astr(u["storeId"]))
	if err != nil || s == nil {
		return nil, nil, failCode(409, "The upload's store is gone; begin again", "no_store")
	}
	t, err := tunnel(s)
	return s, t, err
}

// finishLocked: a finish running in the background is answered at once, never queued behind its lock (a probe would
// wait past the proxy's request time); otherwise the finish runs under the upload's lock.
func (a *App) finishLocked(ctx context.Context, p *Person, id string) (any, error) {
	if _, busy := finishing.Load(id); busy {
		if u, err := a.uploadRecord(ctx, p, id); err == nil {
			return doc{"finishing": true, "received": int64(num(u["size"]))}, nil
		}
	}
	defer uploadLock(id)()
	return a.finishUpload(ctx, p, id)
}

// finishUpload (under the upload's lock) answers the artifact when it is recorded, {received, finishing} while a long
// finish runs, and otherwise finishes: in this request up to finishInline, else in the background (completing many parts
// and copying gigabytes could outlast one request through the proxy); the client then probes with "bytes */size".
func (a *App) finishUpload(ctx context.Context, p *Person, id string) (any, error) {
	u, err := a.uploadRecord(ctx, p, id)
	if err != nil {
		return nil, err
	}
	if aid := astr(u["artifactId"]); aid != "" {
		return a.uploadView(ctx, p, aid)
	}
	size := int64(num(u["size"]))
	if _, busy := finishing.Load(id); busy {
		return doc{"received": size, "finishing": true}, nil
	}
	if size <= finishInline {
		return a.finishNow(ctx, p, u)
	}
	finishing.Store(id, true)
	// The entry in finishing is this finish's exclusion: every finish path answers {finishing} while it is there, so
	// the goroutine takes no lock a probe could queue behind.
	go func() {
		defer finishing.Delete(id)
		if _, err := a.finishNow(context.Background(), p, u); err != nil {
			log.Printf("upload %s: finish: %v", id, err) // the next retried part finishes again
		}
	}()
	return doc{"received": size, "finishing": true}, nil
}

// finishNow completes the store upload once, copies the staging object to its content key and records the stored
// attachment with its verified copy; each step repeats safely.
func (a *App) finishNow(ctx context.Context, p *Person, u doc) (any, error) {
	s, t, err := a.uploadStore(ctx, u)
	if err != nil {
		return nil, err
	}
	size, staging := int64(num(u["size"])), astr(u["staging"])
	if u["completed"] != true {
		// A finish that completed the store upload but failed to record it completes nothing twice: the staging object
		// of the whole size is there.
		if n, err := t.Head(s.Bucket, staging); err != nil {
			return nil, err
		} else if size > 0 && n == size {
			u["completed"] = true
		}
	}
	if u["completed"] != true {
		if size == 0 {
			err = t.Put(s.Bucket, staging, []byte{}, astr(u["mediaType"]))
		} else {
			parts := []S3Part{}
			for _, x := range anys(u["parts"]) {
				parts = append(parts, S3Part{PartNumber: int(num(mapOf(x)["partNumber"])), ETag: astr(mapOf(x)["etag"])})
			}
			err = t.CompleteMultipartUpload(s.Bucket, staging, astr(u["multipart"]), parts)
		}
		if err != nil {
			return nil, err
		}
		u["completed"] = true
		if err := a.saveUpload(ctx, p, u, size, nil); err != nil {
			return nil, err
		}
	}
	h, err := hashFrom(astr(u["sha"]))
	if err != nil {
		return nil, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	projectID := strPtr(u["projectId"])
	if err := promote(t, s.Bucket, staging, keyOf(&Artifact{ProjectID: projectID, Scope: "prod", ContentHash: sum}), size); err != nil {
		return nil, err
	}
	id := newID()
	u["artifactId"] = id
	err = a.saveUpload(ctx, p, u, size, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,session_id,role,state) VALUES($1,$2,'prod',$3,$4,$5,$6,$7,'attachment','stored')", id, projectID, sum, size, astr(u["mediaType"]), astr(u["name"]), strPtr(u["sessionId"])); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,verified_at) VALUES($1,$2,$3,'verified',now())", id, s.ID, projectID)
		return err
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
			if sweeping.TryLock() {
				defer sweeping.Unlock()
				sweepUploadsNow(ctx, a)
			}
			return nil
		})
	})
	operation("artifact_upload_begin", false, typedOp(uploadBegin))
	rawRoute("PUT /api/uploads/{id}", (*App).uploadPart)
}
