package main

import (
	"context"
	"errors"
	"strconv"
	"time"

	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

const youtrackInbox = "youtrack-events"

func init() {
	kitRegister(&kitQueue{
		Name: youtrackInbox, Period: time.Second, Limit: 32, Workers: 4,
		Due: youtrackInboxDue, Step: youtrackInboxStep,
		Retry: kitRetry{Table: "youtrack_inbox", Key: "id",
			Guard: "youtrack_inbox.attempts=$5::int AND youtrack_inbox.disposition IS NULL",
			Class: func(error) string { return "youtrack-event-verification-pending" }},
	})
}

func (a *App) youtrackReceive(ctx context.Context, b db.YoutrackBinding, s youtrackSignal) error {
	if s.Revision < b.StartedMs || s.Revision > nowMs()+youtrackOverlap.Milliseconds() {
		return fail(400, "YouTrack event revision is outside the binding's active time")
	}
	return a.store.Write(ctx, nil, []string{"bot:" + b.BotID}, func(q *Request) error {
		live, err := q.db().YoutrackBindingGet(q.ctx(), b.ID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (live.Disconnected || live.Revision != b.Revision) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		if err := youtrackGrant(q, b.ProjectID, len(b.IssueIds) == 0); err != nil {
			return err
		}
		n, err := q.db().YoutrackInboxInsert(q.ctx(), db.YoutrackInboxInsertParams{ID: b.ID + ":" + strconv.Itoa(int(b.Revision)) + ":" + s.key(), BindingID: b.ID,
			ProjectID: b.ProjectID, IssueID: s.IssueID, Signal: s.Signal, RevisionMs: s.Revision, EventRef: s.EventRef, BindingRevision: b.Revision})
		if err != nil || n == 0 {
			return err
		}
		return kitNotify(q, youtrackInbox)
	})
}

func youtrackInboxDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rows, err := q.db().YoutrackInboxDue(q.ctx(), db.YoutrackInboxDueParams{At: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rows))
	for _, r := range rows {
		out = append(out, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

type youtrackInboxResult struct {
	err      error
	revision int32
}

func youtrackInboxStep(q *Request, _ time.Time, c *kitClaim) error {
	entry, err := q.db().YoutrackInboxGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && entry.Disposition != nil {
		return nil
	}
	if err != nil {
		return err
	}
	if result, ok := c.Result.(youtrackInboxResult); ok {
		if result.revision != entry.BindingRevision {
			return nil // a concurrent credential change carried the unprocessed receipt to the new revision
		}
		switch {
		case errors.Is(result.err, errNotFound):
			return q.db().YoutrackInboxFinish(q.ctx(), db.YoutrackInboxFinishParams{ID: entry.ID, BindingRevision: entry.BindingRevision, Disposition: strPtr("outside_scope")})
		case result.err != nil:
			return result.err
		default:
			return q.db().YoutrackInboxFinish(q.ctx(), db.YoutrackInboxFinishParams{ID: entry.ID, BindingRevision: entry.BindingRevision, Disposition: strPtr("processed")})
		}
	}
	b, err := q.db().YoutrackBindingGet(q.ctx(), entry.BindingID)
	if err != nil {
		return err
	}
	if b.Disconnected || b.Revision != entry.BindingRevision {
		return q.db().YoutrackInboxFinish(q.ctx(), db.YoutrackInboxFinishParams{ID: entry.ID, BindingRevision: entry.BindingRevision, Disposition: strPtr("superseded")})
	}
	s := youtrackSignal{IssueID: entry.IssueID, Signal: entry.Signal, Revision: entry.RevisionMs, EventRef: entry.EventRef}
	c.Call = func(ctx context.Context) any {
		return youtrackInboxResult{err: q.app.youtrackHandleSignal(ctx, b, s), revision: b.Revision}
	}
	return nil
}
