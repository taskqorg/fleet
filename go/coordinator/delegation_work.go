// Delegation website sources on the work kit (docs/designs/fleet-standard.md S6): a delegation whose sources a poll
// found unread has one delegation_source_work row (delegationReady); the queue's step (delegationSourcesStep in
// delegation.go) reads them outside its transaction and records them on the delegation.
package main

import (
	"time"

	"fleet/coordinator/db"
)

func init() {
	kitRegister(&kitQueue{Name: "delegation-sources", Period: time.Second, Limit: 8, Due: delegationSourcesDue, Step: delegationSourcesStep,
		Retry: kitRetry{Table: "delegation_source_work", Key: "delegation_id", Guard: `delegation_source_work.attempts=$5::int`, Class: func(error) string { return "sources_unreadable" }}})
}

func delegationSourcesDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().DelegationSourceWorkDue(q.ctx(), db.DelegationSourceWorkDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.DelegationID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}
