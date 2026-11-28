package main

import (
	"context"
	"encoding/json"
	"errors"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

var errCaptureTransport = errors.New("private capture transport")

type captureReadKey struct{}

func isCaptureTransport(ctx context.Context, a *App, x *Artifact) (bool, error) {
	if deref(x.ProjectID) != "fleet" || deref(x.ExecutionID) == "" ||
		x.Name != "receipt.json" && x.Name != "capture.tar" {
		return false, nil
	}
	c, err := connDB(ctx, a).ArtCommandOf(ctx, *x.ExecutionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var kind struct {
		Command string `json:"command"`
	}
	return c.Kind == "script" && c.HostID == contract.DesignCaptureHost &&
		json.Unmarshal(c.Payload, &kind) == nil && kind.Command == contract.DesignCaptureScript, nil
}

func checkCaptureTransportRead(ctx context.Context, a *App, x *Artifact) error {
	if ctx.Value(captureReadKey{}) == x.ID {
		return nil
	}
	private, err := isCaptureTransport(ctx, a, x)
	if private {
		return fail(403, "Capture transport is private to its originating execution")
	}
	return err
}

// The trusted executor supplies the owning native start command. Ordinary
// result requests cannot speak for it. The authority check holds the parent,
// child and membership revocation locks through the immutable read grant.
func captureTransportView(q *Request, b *contract.HostArtifactGetRequest, artifact *Artifact) (contract.ArtifactView, error) {
	var view contract.ArtifactView
	if q.host == nil || deref(b.CommandId) == "" || deref(b.ExecutionId) == "" {
		return view, fail(403, "Capture transport requires a current native execution")
	}
	err := q.app.store.Write(q.ctx(), nil, nil, func(w *Request) error {
		_, err := captureTransportAuthority(w, q.hostID(), deref(b.ExecutionId), deref(b.CommandId), artifact)
		if err != nil {
			return err
		}
		// Presigning is local. No data read or transfer takes place under this lock.
		ctx := context.WithValue(withTx(w), captureReadKey{}, artifact.ID)
		view, err = artifactView(ctx, q.app, artifact.ID, q.hostID())
		return err
	})
	return view, err
}

func captureTransportAuthority(q *Request, host, execution, startID string, artifact *Artifact) (contract.DesignCaptureScope, error) {
	var empty contract.DesignCaptureScope
	start, err := commandGet(q, startID)
	if err != nil || start.Kind != "start" || start.ExecutionID != execution || start.HostID != host || start.Status != "started" {
		return empty, fail(403, "Capture transport requires the active native start")
	}
	scope, err := designCaptureAuthority(q, execution, host)
	if err != nil {
		return empty, err
	}
	if err := requireDesignCaptureOrigin(q, host); err != nil {
		return empty, err
	}
	if deref(artifact.ExecutionID) != designCaptureJobID(execution) {
		return empty, fail(403, "Capture transport belongs to another execution")
	}
	job, err := commandGet(q, *artifact.ExecutionID)
	if err != nil {
		return empty, err
	}
	payload, ok := captureCommandPayload(job)
	if !ok || payload.DesignCapture.Request.DesignCaptureScope != scope || job.Status != "completed" {
		return empty, fail(403, "Capture transport has no confirmed matching job")
	}
	e, err := getExecution(q, job.ExecutionID)
	if err != nil {
		return empty, err
	}
	if e.State != "succeeded" || e.unconfirmed() || isTrue(e.StopRequested) || artifact.State != "stored" {
		return empty, fail(403, "Capture transport is unavailable or revoked")
	}
	return scope, nil
}
