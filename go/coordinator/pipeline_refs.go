// Inputs by reference (docs/designs/fleet-project-apps.md#inputs-by-reference): a pipeline_run input that names a file
// on one of the project's files grants. pipeline_run checks only what needs no network (the connection is a files grant
// of the project) and answers a run in preparing whose record names each ref as asked (inputRefs); each ref waits as one
// pipeline_input_ref row on the work kit's input-refs queue. Its step leaves the read as its call, outside any
// transaction: the gateway's files_get with a system read token of the run's project (the gateway records it as a
// tool_run with its path in the spool) into a private temporary folder, then the operator-upload path (uploads.go) as
// the run's director, once per content (a stored artifact of the project with the same SHA-256 is reused). The step
// after the call records the artifact, its size and the staging time on the run; once every ref is stored the run
// starts its steps. A ref that cannot be read (a grant not ready, a path outside it, a missing file) fails the run with
// input:<path>: <reason>; a read that may work later (no read token, the gateway or server unreachable) is retried with
// the kit's backoff until its retries reach the failing pause, then fails the run the same way; any other failure (the
// object store, the database) is retried.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	kitRegister(&kitQueue{Name: "input-refs", Period: 5 * time.Second, Limit: 16, Workers: 8, Due: inputRefDue, Step: inputRefStep,
		Retry: kitRetry{Table: "pipeline_input_ref", Key: "id", Guard: `pipeline_input_ref.attempts=$5::int`, Class: func(error) string { return "input_ref_failed" }}})
}

// pipelineInputRef: an input by reference of a run as asked (its input path, connection and file on the server) and,
// once read, its artifact, size and how long the read and store took (stageMs), or why it could not be read.
type pipelineInputRef struct {
	Path       string `json:"path"`
	Connection string `json:"connection"`
	File       string `json:"file"`
	ArtifactID string `json:"artifactId,omitempty"`
	Bytes      int64  `json:"bytes,omitempty"`
	StageMs    *int64 `json:"stageMs,omitempty"`
	Error      string `json:"error,omitempty"`
}

// refInputError: why input path failed, as `input:<path>: <reason>`. A 503 (refInputRetry) is a read that may work
// later (no read token, the gateway or the server unreachable): the kit reads it again until its retries reach the
// failing pause (kitFailingAttempts), then the run fails with it.
func refInputError(code int, input, reason string) error {
	return fail(code, "input:"+input+": "+reason)
}

func refInputRetry(input, reason string) error { return refInputError(503, input, reason) }

// pipelineRefCheck: what an input by reference needs that no read decides, checked before the run is created: a person
// to store it for, a file path, and a files grant connection of projectID. Whether the grant is ready and the file
// readable is the staging's (inputRefStep).
func pipelineRefCheck(q *Request, projectID, input string, ref contract.PipelineRunRef) error {
	if q.person == nil {
		return refInputError(403, input, "an input by reference is stored for a person")
	}
	if ref.Path == "" {
		return refInputError(400, input, "ref.path names the file on the server")
	}
	_, err := pipelineRefGrant(q, projectID, input, ref.Connection)
	return err
}

// pipelineRefGrant: the files grant of projectID that connection names.
func pipelineRefGrant(q *Request, projectID, input, connection string) (db.AccessGrantOfConnectionRow, error) {
	var g db.AccessGrantOfConnectionRow
	err := q.app.store.Read(q.ctx(), q, func(q *Request) (err error) {
		g, err = q.db().AccessGrantOfConnection(q.ctx(), connection)
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return g, refInputError(400, input, "unknown connection "+connection+": name a files grant connection of "+projectID)
	case err != nil:
		return g, err
	case g.ProjectID != projectID:
		return g, refInputError(400, input, connection+" is not a connection of project "+projectID)
	case g.Kind != "files":
		return g, refInputError(400, input, connection+" is a "+g.Kind+" grant, not a files grant")
	}
	return g, nil
}

// pipelineRefsEnqueue: a new run's inputs by reference wait on the input-refs queue, in the run's creating transaction.
func pipelineRefsEnqueue(q *Request, run *runRecord) error {
	for _, in := range run.InputRefs {
		if err := q.db().PipelineInputRefAdd(q.ctx(), db.PipelineInputRefAddParams{ID: newID(), RunID: run.ID, Path: in.Path}); err != nil {
			return err
		}
	}
	if len(run.InputRefs) == 0 {
		return nil
	}
	return kitNotify(q, "input-refs")
}

func inputRefDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().PipelineInputRefDue(q.ctx(), db.PipelineInputRefDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

// inputRefStaged: what the read of one input by reference found: its artifact, size and time, or why it failed.
type inputRefStaged struct {
	artifactID string
	bytes      int64
	ms         int64
	err        error
}

// pipelineRefFetch reads a ref into a folder (pipelineRefRead; a test stands in for the gateway).
var pipelineRefFetch = pipelineRefRead

// inputRefStep: one input by reference of a preparing run (the input-refs queue's step, in the kit's transaction). The
// read is the claim's call; its result is recorded on the run under GLOBAL, as the run's other writes are.
func inputRefStep(q *Request, _ time.Time, c *kitClaim) error {
	r, recording := c.Result.(inputRefStaged)
	if recording {
		if err := acquireTxLock(q, GLOBAL); err != nil {
			return err
		}
	}
	row, err := q.db().PipelineInputRefGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	run, err := getRun(q, row.RunID)
	if err != nil {
		return err
	}
	if run == nil || run.State != "preparing" { // gone
		return q.db().PipelineInputRefDrop(q.ctx(), row.RunID)
	}
	i := slices.IndexFunc(run.InputRefs, func(r pipelineInputRef) bool { return r.Path == row.Path })
	if i < 0 { // a row the run does not name: it goes, and the run starts if it was the last
		if !recording {
			if err := acquireTxLock(q, GLOBAL); err != nil {
				return err
			}
		}
		if err := q.db().PipelineInputRefDone(q.ctx(), c.Key); err != nil {
			return err
		}
		return inputRefsStored(q, run)
	}
	in := &run.InputRefs[i]
	if !recording {
		a, p, project, ref := q.app, q.peopleSnapshot().person(run.director()), run.project(), *in
		c.Call = func(ctx context.Context) any {
			start := time.Now()
			id, n, err := pipelineRefStage(ctx, a, p, project, ref)
			return inputRefStaged{artifactID: id, bytes: n, ms: time.Since(start).Milliseconds(), err: err}
		}
		return nil
	}
	in.StageMs = &r.ms
	if r.err != nil {
		var he *HTTPError
		if !errors.As(r.err, &he) || !strings.HasPrefix(he.Msg, "input:") || he.Code == 503 && c.Attempts+1 < kitFailingAttempts() {
			return r.err // not the input's fault, or a read that may work later: the kit reads it again after its backoff
		}
		in.Error = he.Msg
		run.failure = jsonBytes(pipelineStepError{Code: "input_unavailable", Message: he.Msg})
		return run.finish(q, "failed", "input_unavailable") // its end drops the run's other refs (pipelineEnded)
	}
	in.ArtifactID, in.Bytes = r.artifactID, r.bytes
	if err := q.db().PipelineInputRefDone(q.ctx(), c.Key); err != nil {
		return err
	}
	return inputRefsStored(q, run)
}

// inputRefsStored writes run after one of its refs left the queue; once none is left, every ref is stored and the run's
// steps start (put gives it its pipeline queue row); a ref the queue no longer holds unstored fails it.
func inputRefsStored(q *Request, run *runRecord) error {
	left, err := q.db().PipelineInputRefLeft(q.ctx(), run.ID)
	if err != nil {
		return err
	}
	if left == 0 {
		inputs := []fileRef{}
		if run.ExampleInputs != nil {
			inputs = append(inputs, *run.ExampleInputs...)
		}
		for _, in := range run.InputRefs {
			if in.ArtifactID == "" {
				msg := "input:" + in.Path + ": the read was lost; run pipeline_rerun"
				run.failure = jsonBytes(pipelineStepError{Code: "input_unavailable", Message: msg})
				return run.finish(q, "failed", "input_unavailable")
			}
			inputs = append(inputs, fileRef{ArtifactID: in.ArtifactID, Path: "example/" + in.Path})
		}
		run.ExampleInputs, run.State = &inputs, "running"
	}
	return run.put(q)
}

// pipelineRefStage reads input in of a run of projectID as person p, the run's director, and answers the stored
// artifact holding its bytes and their size.
func pipelineRefStage(ctx context.Context, a *App, p *Person, projectID string, in pipelineInputRef) (string, int64, error) {
	if p == nil {
		return "", 0, refInputError(403, in.Path, "the run's director is not a person of this Fleet")
	}
	q := scopedRequest(ctx, a, p)
	g, err := pipelineRefGrant(q, projectID, in.Path, in.Connection)
	if err != nil {
		return "", 0, err
	}
	if g.State != "ready" {
		return "", 0, refInputError(409, in.Path, "the grant of "+in.Connection+" is "+g.State+", not ready")
	}
	dir, err := os.MkdirTemp("", "fleet-ref-") // 0700: only the coordinator reads the file
	if err != nil {
		return "", 0, err
	}
	defer os.RemoveAll(dir)
	file, meta, err := pipelineRefFetch(ctx, a, projectID, in.Path, contract.PipelineRunRef{Connection: in.Connection, Path: in.File}, dir)
	if err != nil {
		return "", 0, err
	}
	return pipelineRefStore(q, projectID, file, meta)
}

// refFile: what files_get answered about the file it wrote.
type refFile struct {
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Bytes    int64  `json:"bytes"`
	Sha256   string `json:"sha256"`
}

// pipelineRefRead downloads ref into dir with a read token of projectID and answers the local file.
func pipelineRefRead(ctx context.Context, a *App, projectID, input string, ref contract.PipelineRunRef, dir string) (string, refFile, error) {
	token, release, err := readTokenFor(ctx, a, ref.Connection, projectID)
	if err != nil {
		return "", refFile{}, refInputRetry(input, err.Error())
	}
	defer release()
	line := contract.GatewayDownload(ctx, gatewayTarget(token, true), ref.Connection, "files_get", jsonBytes(struct {
		Path string `json:"path"`
	}{ref.Path}), contract.Download{Root: dir})
	grantRefused(ref.Connection, "files_get", line)
	if line.Outcome != contract.CallOutcomeOk {
		code, reason := 503, deref(line.ErrorCode) // may work later
		switch reason {
		case "bad-path", "file-refused":
			code = 400 // the gateway's: outside the grant, or no such file
		}
		var text struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(line.Output, &text) == nil && text.Text != "" {
			reason += ": " + text.Text
		}
		if reason == "" {
			reason = "the read failed"
		}
		return "", refFile{}, refInputError(code, input, reason)
	}
	var out struct {
		Path string `json:"path"`
		refFile
	}
	if err := json.Unmarshal(line.Output, &out); err != nil || out.Path == "" {
		return "", refFile{}, refInputRetry(input, "the gateway answered no file")
	}
	if out.Name == "" {
		out.Name = path.Base(ref.Path)
	}
	return out.Path, out.refFile, nil
}

// pipelineRefStore stores file as an artifact of projectID: the stored one with the same content, else a new upload of
// q's person (uploadInsert, uploadWritePart, finishNow), part by part on the upload's grid.
func pipelineRefStore(q *Request, projectID, file string, meta refFile) (string, int64, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", 0, err
	}
	size := st.Size()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", 0, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	ctx, a, p := context.WithoutCancel(q.ctx()), q.app, q.person
	var found string
	var u upload
	if err := a.store.Write(ctx, q, nil, func(q *Request) error {
		id, err := q.db().ArtifactStoredByHash(q.ctx(), db.ArtifactStoredByHashParams{ProjectID: &projectID, ContentHash: sum})
		if err == nil {
			found = id
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		u, err = uploadInsert(q, p.Email, meta.Name, meta.MimeType, size, nil, &projectID)
		return err
	}); err != nil || found != "" {
		return found, size, err
	}
	defer uploadLock(u.ID)()
	for first := int64(0); first < size; first += u.PartBytes {
		n := min(u.PartBytes, size-first)
		wctx, cancel := context.WithTimeout(q.ctx(), time.Minute)
		err := uploadMemory.acquire(wctx, n)
		cancel()
		if err != nil {
			return "", 0, fail(503, "Upload memory busy; the input is read again")
		}
		body := make([]byte, n)
		_, err = f.ReadAt(body, first)
		if err == nil {
			err = a.uploadWritePart(ctx, p, u.ID, first, body, func() {})
		}
		uploadMemory.release(n)
		if err != nil {
			return "", 0, err
		}
	}
	if u, err = a.uploadRecord(ctx, p, u.ID); err != nil {
		return "", 0, err
	}
	if u.Received != size {
		return "", 0, fail(409, "The stored input changed meanwhile")
	}
	v, err := a.finishNow(ctx, p, u)
	if err != nil {
		return "", 0, err
	}
	view, ok := v.(contract.ArtifactView)
	if !ok || view.Id == "" {
		return "", 0, fail(500, "The stored input has no artifact")
	}
	return view.Id, size, nil
}
