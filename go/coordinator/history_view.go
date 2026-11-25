package main

// History (history_view, docs/designs/fleet-history.md): the activity entries fleet_activity() writes, read as one list
// for every scope. The global feed, a section's history and an object's history differ only in the request's filters.
// Consecutive entries of one group (subject, verb, outcome) fold into one with n, so a bot that runs every five minutes
// is one row, not twelve an hour (OR-58).

import (
	"encoding/json"
	"slices"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("history_view", historyView)
}

const historyLimit, historyMaxLimit = 50, 200

// historyRaw: how many stored entries one page reads per folded entry it returns, at most.
const historyRaw = 4

func historyView(q *Request, r *contract.HistoryViewRequest) (contract.HistoryViewReply, error) {
	out := contract.HistoryViewReply{Entries: []contract.HistoryEntry{}}
	if slices.Contains(r.ProjectIds, "") {
		return out, fail(400, "projectIds: an empty id; '-' is work outside projects")
	}
	limit := historyLimit
	if r.Limit != nil {
		limit = min(max(int(*r.Limit), 1), historyMaxLimit)
	}
	var before int64
	if r.Before != nil {
		before = *r.Before
	}
	// The scope chip's name: the one object the request narrows to, read once on the first page.
	if before == 0 && (r.TaskId != nil || r.SessionId != nil || r.ExperimentId != nil || r.BotId != nil || r.HostId != nil) {
		t, err := q.db().HistoryObjectTitle(q.ctx(), db.HistoryObjectTitleParams{TaskID: r.TaskId, SessionID: r.SessionId,
			ExperimentID: r.ExperimentId, BotID: r.BotId, HostID: r.HostId})
		if err != nil {
			return out, err
		}
		if t != "" {
			out.ScopeTitle = &t
		}
	}
	raw := limit*historyRaw + 1
	sql, args := sqlHistoryPage(r, before, raw)
	rows, err := q.tx.Query(q.ctx(), sql, args...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	type stored struct {
		e     contract.HistoryEntry
		group string
	}
	var all []stored
	for rows.Next() {
		var s stored
		var detail []byte
		if err := rows.Scan(&s.e.Seq, &s.e.At, &s.e.Kind, &s.e.Verb, &s.e.Outcome, &s.e.Section, &s.e.Subject, &s.e.Title, &s.e.Actor,
			&s.e.ProjectId, &s.e.TaskId, &s.e.SessionId, &s.e.HostId, &s.e.BotId, &s.e.ExperimentId, &s.group, &detail); err != nil {
			return out, err
		}
		if len(detail) > 0 {
			if err := json.Unmarshal(detail, &s.e.Detail); err != nil {
				return out, err
			}
		}
		s.e.FirstAt, s.e.N = s.e.At, 1
		all = append(all, s)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	// Fold: an entry joins the previous one (newer) when both are of one group with one verb and outcome. A page ends
	// at a group boundary once it holds limit entries, so a group is never split by the page's own end.
	last := -1
	for i, s := range all {
		if n := len(out.Entries); n > 0 {
			p := &out.Entries[n-1]
			if all[i-1].group == s.group && p.Verb == s.e.Verb && p.Outcome == s.e.Outcome {
				p.N++
				p.FirstAt = s.e.At
				last = i
				continue
			}
			if n == limit {
				break
			}
		}
		out.Entries = append(out.Entries, s.e)
		last = i
	}
	if len(all) == raw || last < len(all)-1 {
		c := all[last].e.Seq
		out.NextCursor = &c
	}
	return out, nil
}
