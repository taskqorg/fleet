package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

const artifactVerifyQueue = "artifact-verify"

func init() {
	kitRegister(&kitQueue{Name: artifactVerifyQueue, Period: time.Second, Limit: 4, Workers: 4, Due: artifactVerifyDue, Step: artifactVerifyStep,
		Retry: kitRetry{Table: "artifact_verify_work", Key: "id", Guard: `artifact_verify_work.attempts=$5::int`, Class: func(error) string { return "artifact_verify_failed" }}})
}

// An unkeyed receipt is a request to check the active reservation. Commit that work before
// replying to the executor. Duplicate receipts for the same digest share the work.
func queueLegacyVerification(ctx context.Context, a *App, x *Artifact, s *StoreRow, c *copyRow, digest string) (contract.ArtifactStateReply, error) {
	state := "unknown"
	err := txCtx(ctx, a, func(tx pgx.Tx) error {
		q := backgroundRequest(ctx, a, tx)
		current, err := copyOf(q.ctx(), a, x.ID, s.ID)
		if err != nil || c.StagingKey == nil || !c.sameReservation(current) {
			return err
		}
		added, err := q.db().ArtVerifyWorkAdd(q.ctx(), db.ArtVerifyWorkAddParams{
			ID: newID(), ArtifactID: x.ID, StoreID: s.ID, StagingKey: *c.StagingKey, Digest: digest,
		})
		if err != nil {
			return err
		}
		state = "verifying"
		if added == 0 {
			return nil
		}
		return kitNotify(q, artifactVerifyQueue)
	})
	return contract.ArtifactStateReply{State: state}, err
}

func artifactVerifyDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rows, err := q.db().ArtVerifyWorkDue(q.ctx(), db.ArtVerifyWorkDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rows))
	for _, row := range rows {
		out = append(out, &kitClaim{Key: row.ID, Attempts: int(row.Attempts), Guard: []any{row.Attempts}})
	}
	return out, err
}

func artifactVerifyStep(q *Request, _ time.Time, claim *kitClaim) error {
	ctx, a, d := q.ctx(), q.app, q.db()
	if claim.Result != nil {
		if err, ok := claim.Result.(error); ok {
			return err
		}
		return d.ArtVerifyWorkDelete(ctx, claim.Key)
	}
	work, err := d.ArtVerifyWorkGet(ctx, claim.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := copyOf(ctx, a, work.ArtifactID, work.StoreID)
	if err != nil {
		return err
	}
	if c == nil || (c.State != "pending" && c.State != "copying") || c.UploadID != nil || deref(c.StagingKey) != work.StagingKey {
		return d.ArtVerifyWorkDelete(ctx, claim.Key)
	}
	row, err := d.ArtByID(ctx, work.ArtifactID)
	if errors.Is(err, pgx.ErrNoRows) {
		return d.ArtVerifyWorkDelete(ctx, claim.Key)
	}
	if err != nil {
		return err
	}
	x := artifactOf(row)
	s, err := storeGet(ctx, a, work.StoreID)
	if err != nil {
		return err
	}
	if s.State == "unreachable" {
		// A new reservation on an eligible store may proceed after this work closes.
		return d.ArtVerifyWorkDelete(ctx, claim.Key)
	}
	claim.Call = func(ctx context.Context) any {
		if err := verifyLegacyArtifact(ctx, a, &x, s, c, work.Digest); err != nil {
			return err
		}
		return true
	}
	return nil
}

func verifyLegacyArtifact(ctx context.Context, a *App, x *Artifact, s *StoreRow, c *copyRow, reported string) error {
	t, err := tunnel(s)
	if err != nil {
		return err
	}
	r, err := t.get(ctx, s.Bucket, *c.StagingKey, nil)
	if confirmedMissingObject(err) {
		r, err = t.get(ctx, s.Bucket, keyOf(x), nil)
	}
	if confirmedMissingObject(err) {
		return nil // the uploader's next renewal checks both keys and can re-reserve
	}
	if err != nil {
		return err
	}
	h := sha256.New()
	_, err = io.Copy(h, r)
	r.Close()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if hex.EncodeToString(h.Sum(nil)) != reported {
		return nil // the digest may belong to a superseded reservation
	}
	// The stream may have outlasted the reservation. Check again before touching either key.
	current, err := copyOf(ctx, a, x.ID, s.ID)
	if err != nil || !c.sameReservation(current) {
		return err
	}
	_, err = finishArtifactVerification(ctx, a, x, s, c, reported)
	if confirmedMissingObject(err) {
		return nil
	}
	return err
}

func confirmedMissingObject(err error) bool {
	var se *S3Error
	return errors.As(err, &se) && se.Status == 404 && se.Code == "NoSuchKey"
}
