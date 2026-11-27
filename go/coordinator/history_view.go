package main

// History (history_view, docs/designs/fleet-history.md): the activity entries fleet_activity() writes, read as one list
// for every scope. The global feed, a section's history and an object's history differ only in the request's filters.
// Consecutive entries of one group (subject, verb, outcome) fold into one with n, so a bot that runs every five minutes
// is one row, not twelve an hour (OR-58).

import (
	"encoding/json"
	"slices"
	"strings"

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
	// An experiment's trial runs are one entry per experiment in the page, whatever their order: their starts and ends
	// alternate for hours, so consecutive folding would keep them apart. The entry counts trials by their latest state.
	last := -1
	trials := map[string]*historyTrials{}
	for i, s := range all {
		if s.e.Kind == "run" && s.e.ExperimentId != nil {
			if t := trials[*s.e.ExperimentId]; t != nil {
				t.add(s.e)
				last = i
				continue
			}
			if len(out.Entries) == limit {
				break
			}
			t := &historyTrials{index: len(out.Entries), seen: map[string]bool{}}
			trials[*s.e.ExperimentId] = t
			e := s.e
			e.Verb, e.N, e.Detail = "trials", 0, nil
			out.Entries = append(out.Entries, e)
			t.add(s.e)
			last = i
			continue
		}
		if n := len(out.Entries); n > 0 {
			p := &out.Entries[n-1]
			if i > 0 && all[i-1].group == s.group && p.Verb == s.e.Verb && p.Outcome == s.e.Outcome {
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
	for _, t := range trials {
		t.finish(&out.Entries[t.index])
	}
	if len(all) == raw || last < len(all)-1 {
		c := all[last].e.Seq
		out.NextCursor = &c
	}
	if err := resolveHistoryBotActors(q, out.Entries); err != nil {
		return out, err
	}
	return out, nil
}

// resolveHistoryBotActors replaces raw bot UUID actors with the bot's current name (OR-38), keeping activity.actor
// intact in storage so actor=<bot_id> filters continue to work.
func resolveHistoryBotActors(q *Request, entries []contract.HistoryEntry) error {
	seen := map[string]bool{}
	var botIDs []string
	add := func(id string) {
		if id != "" && !seen[id] {
			seen[id] = true
			botIDs = append(botIDs, id)
		}
	}
	for _, e := range entries {
		if e.BotId != nil {
			add(*e.BotId)
		}
		if e.Actor != nil {
			a := strings.TrimPrefix(*e.Actor, "bot:")
			if idOK(a) && len(a) == 36 && strings.Count(a, "-") == 4 {
				add(a)
			}
		}
	}
	if len(botIDs) == 0 {
		return nil
	}
	rows, err := q.db().HistoryBotNames(q.ctx(), botIDs)
	if err != nil {
		return err
	}
	names := make(map[string]string, len(rows))
	for _, r := range rows {
		if r.Name != "" {
			names[r.ID] = r.Name
		}
	}
	for i := range entries {
		e := &entries[i]
		var bid string
		if e.BotId != nil && (e.Actor == nil || *e.Actor == "" || *e.Actor == *e.BotId || *e.Actor == "bot:"+*e.BotId) {
			bid = *e.BotId
		} else if e.Actor != nil {
			a := strings.TrimPrefix(*e.Actor, "bot:")
			if _, ok := names[a]; ok {
				bid = a
			}
		}
		if name := names[bid]; name != "" {
			e.Actor = &name
		}
	}
	return nil
}

// historyTrials: one experiment's trial runs in a page. Entries come newest first, so a trial's first entry is its
// latest state: a finished, failed or stopped run, or a start not yet ended.
type historyTrials struct {
	index                              int
	seen                               map[string]bool
	oldest                             float64
	finished, failed, stopped, running int64
}

func (t *historyTrials) add(e contract.HistoryEntry) {
	t.oldest = e.At
	if t.seen[e.Subject] {
		return
	}
	t.seen[e.Subject] = true
	switch e.Verb {
	case "finished":
		t.finished++
	case "failed":
		t.failed++
	case "stopped":
		t.stopped++
	default:
		t.running++
	}
}

// finish writes the counts on the experiment's entry: n trials, the outcome of the worst news (running while any run,
// else failed while any failed) and the counts in detail.
func (t *historyTrials) finish(e *contract.HistoryEntry) {
	e.N = int64(len(t.seen))
	e.FirstAt = t.oldest
	switch {
	case t.running > 0:
		e.Outcome = "running"
	case t.failed > 0:
		e.Outcome = "failed"
	case t.finished == 0:
		e.Outcome = "stopped"
	default:
		e.Outcome = "ok"
	}
	e.Detail = map[string]json.RawMessage{"trials": jsonBytes(e.N), "finished": jsonBytes(t.finished), "failed": jsonBytes(t.failed),
		"stopped": jsonBytes(t.stopped), "running": jsonBytes(t.running)}
}
