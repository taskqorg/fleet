// Inputs by reference (docs/designs/fleet-project-apps.md#inputs-by-reference): a pipeline_run input that names a file
// on one of the project's files grants. Before the run is planned, outside any transaction, the coordinator reads the
// file through the gateway's files_get with a system read token of the run's project (the gateway records the read as
// a tool_run with its path in the spool) into a private temporary folder, and stores it as an artifact of the project
// through the operator-upload path (uploads.go), once per content: a stored artifact of the project with the same
// SHA-256 is reused.
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
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

// refInputError: why input path failed, as `input:<path>: <reason>`.
func refInputError(code int, input, reason string) error {
	return fail(code, "input:"+input+": "+reason)
}

// pipelineRefStage reads ref for input path of a run of projectID and answers the stored artifact holding its bytes.
func pipelineRefStage(q *Request, projectID, input string, ref contract.PipelineRunRef) (string, error) {
	if q.person == nil {
		return "", refInputError(403, input, "an input by reference is stored for a person")
	}
	if ref.Path == "" {
		return "", refInputError(400, input, "ref.path names the file on the server")
	}
	var g db.AccessGrantOfConnectionRow
	err := q.app.store.Read(q.ctx(), q, func(q *Request) (err error) {
		g, err = q.db().AccessGrantOfConnection(q.ctx(), ref.Connection)
		return err
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return "", refInputError(400, input, "unknown connection "+ref.Connection+": name a files grant connection of "+projectID)
	case err != nil:
		return "", err
	case g.ProjectID != projectID:
		return "", refInputError(400, input, ref.Connection+" is not a connection of project "+projectID)
	case g.Kind != "files":
		return "", refInputError(400, input, ref.Connection+" is a "+g.Kind+" grant, not a files grant")
	case g.State != "ready":
		return "", refInputError(409, input, "the grant of "+ref.Connection+" is "+g.State+", not ready")
	}
	dir, err := os.MkdirTemp("", "fleet-ref-") // 0700: only the coordinator reads the file
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	file, meta, err := pipelineRefRead(q.ctx(), q.app, projectID, input, ref, dir)
	if err != nil {
		return "", err
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
		return "", refFile{}, refInputError(409, input, err.Error())
	}
	defer release()
	line := contract.GatewayDownload(ctx, gatewayTarget(token, true), ref.Connection, "files_get", jsonBytes(struct {
		Path string `json:"path"`
	}{ref.Path}), contract.Download{Root: dir})
	grantRefused(ref.Connection, "files_get", line)
	if line.Outcome != contract.CallOutcomeOk {
		code, reason := 409, deref(line.ErrorCode)
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
		return "", refFile{}, refInputError(409, input, "the gateway answered no file")
	}
	if out.Name == "" {
		out.Name = path.Base(ref.Path)
	}
	return out.Path, out.refFile, nil
}

// pipelineRefStore stores file as an artifact of projectID: the stored one with the same content, else a new upload of
// q's person (uploadInsert, uploadWritePart, finishNow), part by part on the upload's grid.
func pipelineRefStore(q *Request, projectID, file string, meta refFile) (string, error) {
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	size := st.Size()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
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
		return found, err
	}
	defer uploadLock(u.ID)()
	for first := int64(0); first < size; first += u.PartBytes {
		n := min(u.PartBytes, size-first)
		wctx, cancel := context.WithTimeout(q.ctx(), time.Minute)
		err := uploadMemory.acquire(wctx, n)
		cancel()
		if err != nil {
			return "", fail(503, "Upload memory busy; call pipeline_run again")
		}
		body := make([]byte, n)
		_, err = f.ReadAt(body, first)
		if err == nil {
			err = a.uploadWritePart(ctx, p, u.ID, first, body, func() {})
		}
		uploadMemory.release(n)
		if err != nil {
			return "", err
		}
	}
	if u, err = a.uploadRecord(ctx, p, u.ID); err != nil {
		return "", err
	}
	if u.Received != size {
		return "", fail(409, "The stored input changed meanwhile")
	}
	v, err := a.finishNow(ctx, p, u)
	if err != nil {
		return "", err
	}
	view, ok := v.(contract.ArtifactView)
	if !ok || view.Id == "" {
		return "", fail(500, "The stored input has no artifact")
	}
	return view.Id, nil
}
