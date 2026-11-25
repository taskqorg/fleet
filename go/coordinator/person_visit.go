package main

// Page visits (docs/design-system/pages/home.md "Quiet"): the last time a person opened a page, kept per person on the
// server (person_visit, migration 100). Home's quiet summary counts what happened since the visit before this one.

import (
	"errors"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	op("person_visit", personVisit)
}

// personVisit: the caller's visit before this one; a person's call records this one unless record is false. An agent
// reads its director's last visit and never records one: its call is not the person looking at the page.
func personVisit(q *Request, r *contract.PersonVisitRequest) (contract.PersonVisitReply, error) {
	out := contract.PersonVisitReply{Page: r.Page}
	if q.person == nil {
		return out, fail(403, "An agent without a director has no visits")
	}
	email, page := q.person.Email, string(r.Page)
	if q.agent != "" || (r.Record != nil && !*r.Record) {
		at, err := q.db().PersonVisitGet(q.ctx(), db.PersonVisitGetParams{Email: email, Page: page})
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
		out.PreviousAt = ptr(at.UnixMilli())
		return out, err
	}
	now := time.UnixMilli(time.Now().UnixMilli())
	prev, err := q.db().PersonVisitRecord(q.ctx(), db.PersonVisitRecordParams{Email: email, Page: page, At: pgtype.Timestamptz{Time: now, Valid: true}})
	if err != nil {
		return out, err
	}
	if prev.Valid {
		out.PreviousAt = ptr(prev.Time.UnixMilli())
	}
	out.RecordedAt = ptr(now.UnixMilli())
	return out, nil
}
