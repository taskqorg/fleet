// Background finishes of large uploads on the work kit (docs/designs/fleet-standard.md S6): an upload larger than
// finishInline that is complete has one upload_finish_work row (finishUpload, uploads.go); the queue's step leaves the
// finish (completing the store upload, the copy to its content key, the artifact record: finishNow) as its call and
// drops the row once the upload names its artifact. Every finish step repeats safely, so a crash or a failure retries it.
package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

func init() {
	kitRegister(&kitQueue{Name: "upload-finish", Period: time.Second, Limit: 4, Workers: 4, Due: uploadFinishDue, Step: uploadFinishStep,
		Retry: kitRetry{Table: "upload_finish_work", Key: "upload_id", Guard: `upload_finish_work.attempts=$5::int`, Class: func(error) string { return "upload_finish_failed" }}})
}

func uploadFinishDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().UploadFinishWorkDue(q.ctx(), db.UploadFinishWorkDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.UploadID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

// uploadFinishStep: a finished, gone or orphaned upload (its person is gone) drops its row; otherwise the finish runs as
// the claim's call, in the person's scope, and its outcome is recorded here: done drops the row, a failure retries.
func uploadFinishStep(q *Request, _ time.Time, c *kitClaim) error {
	if c.Result != nil {
		if err, _ := c.Result.(error); err != nil {
			return err
		}
		return q.db().UploadFinishWorkDelete(q.ctx(), c.Key)
	}
	r, err := q.db().UploadGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.db().UploadFinishWorkDelete(q.ctx(), c.Key)
	}
	if err != nil {
		return err
	}
	u, err := uploadOf(r)
	if err != nil {
		return err
	}
	a := q.app
	p := a.peopleSnapshot().person(u.Person)
	if u.ArtifactID != nil || p == nil {
		return q.db().UploadFinishWorkDelete(q.ctx(), c.Key)
	}
	c.Call = func(ctx context.Context) any {
		defer uploadLock(u.ID)()
		if _, err := a.finishNow(ctx, p, u); err != nil {
			return err
		}
		return true
	}
	return nil
}
