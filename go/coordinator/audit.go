// The audit (docs/designs/fleet-standard.md S4, migration 82): every changed setting or object writes one audit_log
// row through audit(), the only writer. The actor is always actor(q): the calling agent execution (acting for its
// director, onBehalfOf), else the request person, else 'fleet' (Fleet's own background pass). setting_changes and the
// account and connection histories read the rows as records (queries/audit.sql).
package main

import (
	"encoding/json"

	"fleet/coordinator/db"
)

// auditExtra: what a change says beyond its target, field and values. ProjectID scopes the row (none: it outlives every
// project); Holder is the person whose object it is, who sees the row without the project; the rest is payload.
type auditExtra struct {
	ProjectID  *string
	Holder     string
	Reason     string
	Note       string
	AskedBy    *string // a budget raise: who asked and who answered
	AnsweredBy *string
}

// audit records that target's field changed from prev to next.
func audit(q *Request, target, field string, prev, next any, extra auditExtra) error {
	var onBehalfOf *string
	if q.agent != "" && q.person != nil { // an agent's change: the execution acts for its director (fleet-pm-agent.md §1)
		onBehalfOf = &q.person.Email
	}
	payload, _ := json.Marshal(struct {
		Reason     string  `json:"reason,omitempty"`
		Note       string  `json:"note,omitempty"`
		AskedBy    *string `json:"askedBy,omitempty"`
		AnsweredBy *string `json:"answeredBy,omitempty"`
	}{extra.Reason, extra.Note, extra.AskedBy, extra.AnsweredBy})
	return q.db().AuditInsert(q.ctx(), db.AuditInsertParams{ID: newID(), Target: target, Field: field, Previous: auditValue(prev), Value: auditValue(next),
		Actor: actor(q, "fleet"), OnBehalfOf: onBehalfOf, Holder: optStr(extra.Holder), ProjectID: extra.ProjectID, Payload: payload})
}

// auditValue: v as stored; an absent value (nil, JSON null) is NULL.
func auditValue(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return nil
	}
	return b
}

// auditOfKind: the audit records of every target of a kind (the target's part before ':'), oldest first.
func auditOfKind(q *Request, kind string) ([]doc, error) {
	rs, err := q.db().AuditOfKind(q.ctx(), kind)
	out := make([][]byte, len(rs))
	for i, r := range rs {
		out[i] = r.Data
	}
	return records(out, err)
}
