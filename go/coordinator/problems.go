// Problems (docs/designs/fleet-system-management.md, Problems): rule findings, dispatch stalls and direct signals as
// needs-you questions keyed by cause; the only writer of question.cause (DESIGN_RULES.md S6). One dedupe rule: an open
// or answered question holds its cause, so an answered one (Dismiss) is not asked again while the cause is present;
// when the cause goes, the cause is cleared and an open question withdrawn. A Bugsink issue that comes back
// (regression, unmute) asks again whatever the earlier answer (bugsink.go).
package main

import (
	"fmt"
	"strconv"
)

var problemOptions = []any{"investigate", "dismiss"}

// Problem is one current cause of a source.
type Problem struct {
	Cause, Text, Rule string
	ProjectID         string // "" for a system cause
	AddressedTo       string // "": the owner (a project cause names its director here)
	RefreshText       bool   // Refresh the text while this question is open.
	Data              doc    // hostId, storeId, executionId, exemplars, value
	Options           []any  // the answers offered; nil: investigate or dismiss
}

func problemAsk(q *Request, source string, p Problem) error {
	data := doc{"cause": p.Cause}
	if source != "" {
		data["source"] = source
	}
	if p.Rule != "" {
		data["rule"] = p.Rule
	}
	for k, v := range p.Data {
		data[k] = v
	}
	to := p.AddressedTo
	if to == "" {
		to = ownerEmail(q)
	}
	var pid any
	if p.ProjectID != "" {
		pid = p.ProjectID
	}
	options := p.Options
	if options == nil {
		options = problemOptions
	}
	qid, err := askQuestion(q, doc{"kind": "operator_decision", "text": p.Text, "options": options, "askedBy": "fleet", "projectId": pid, "data": data})
	if err != nil {
		return err
	}
	return exec(q, "UPDATE question SET cause=$2,addressed_to=$3 WHERE id=$1", qid, p.Cause, to)
}

// problemHeld: the open or answered question holding cause, if any.
func problemHeld(q *Request, cause string) (doc, error) {
	return first(q, "SELECT id,state,data FROM question WHERE cause=$1 AND state IN ('open','answered') ORDER BY seq DESC LIMIT 1", cause)
}

// problemOpen: a one-off sender's cause (span writer, backup); asks once while held.
func problemOpen(q *Request, cause string, projectID any, text string) error {
	return problemRaise(q, "direct", Problem{Cause: cause, Text: text, ProjectID: astr(projectID)})
}

// problemRaise asks p unless its cause is held.
func problemRaise(q *Request, source string, p Problem) error {
	h, err := problemHeld(q, p.Cause)
	if err != nil || h != nil {
		return err
	}
	return problemAsk(q, source, p)
}

// problemClose: the cause is gone: an open question is withdrawn, an answered one lets go of the cause.
func problemClose(q *Request, cause string) error {
	return problemsClose(q, []string{cause})
}

// problemsClose: problemClose for many causes.
func problemsClose(q *Request, causes []string) error {
	if len(causes) == 0 {
		return nil
	}
	held, err := queryRows(q, "SELECT id,state::text AS state FROM question WHERE cause=ANY($1) AND state IN ('open','answered') ORDER BY id FOR UPDATE", causes)
	if err != nil {
		return err
	}
	return problemsRelease(q, held)
}

// problemsClosePrefix: problemClose for every held cause starting with prefix.
func problemsClosePrefix(q *Request, prefix string) error {
	held, err := queryRows(q, "SELECT id,state::text AS state FROM question WHERE starts_with(cause,$1) AND state IN ('open','answered') ORDER BY id FOR UPDATE", prefix)
	if err != nil {
		return err
	}
	return problemsRelease(q, held)
}

// problemsRelease: held questions (id, state) let go of their cause; an open one is withdrawn.
func problemsRelease(q *Request, held []doc) error {
	open, answered := []string{}, []string{}
	for _, h := range held {
		if h["state"] == "open" {
			open = append(open, astr(h["id"]))
		} else {
			answered = append(answered, astr(h["id"]))
		}
	}
	if err := questionsWithdraw(q, open); err != nil {
		return err
	}
	return problemsLetGo(q, answered)
}

// problemsLetGo: questions ids no longer hold a cause (an ended question, a closed problem).
func problemsLetGo(q *Request, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	return exec(q, "UPDATE question SET cause=NULL WHERE id=ANY($1) AND cause IS NOT NULL", ids)
}

// problemsSet: the full current set of the causes matching pattern (a regular expression), one pass of a source that
// sees all of them at once (dispatch): a held cause no longer present closes at once, a new one is asked.
func problemsSet(q *Request, source, pattern string, current []Problem) error {
	held, err := queryRows(q, "SELECT id,state::text AS state,cause FROM question WHERE state IN ('open','answered') AND cause ~ $1 ORDER BY id FOR UPDATE", pattern)
	if err != nil {
		return err
	}
	now, have, gone := map[string]bool{}, map[string]bool{}, []doc{}
	for _, p := range current {
		now[p.Cause] = true
	}
	for _, h := range held {
		c := astr(h["cause"])
		have[c] = true
		if !now[c] {
			gone = append(gone, h)
		}
	}
	if err := problemsRelease(q, gone); err != nil {
		return err
	}
	for _, p := range current {
		if !have[p.Cause] {
			have[p.Cause] = true
			if err := problemAsk(q, source, p); err != nil {
				return fmt.Errorf("problem %s: %w", p.Cause, err)
			}
		}
	}
	return nil
}

// problemsSync: the full current set of one source (rules): new causes are asked; a held cause absent for
// problemCleanRuns runs in a row is closed; a present one resets its miss count.
func problemsSync(q *Request, source string, current []Problem) error {
	now := map[string]Problem{}
	for _, p := range current {
		now[p.Cause] = p
	}
	held, err := queryRows(q, "SELECT id,cause,coalesce(data->>'misses','0') AS misses FROM question WHERE cause LIKE 'problem:%' AND state IN ('open','answered') AND data->>'source'=$1", source)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, h := range held {
		c := astr(h["cause"])
		have[c] = true
		if p, present := now[c]; present {
			if p.RefreshText {
				if err := exec(q, `UPDATE question SET text=CASE WHEN state='open' THEN $2 ELSE text END, data=data-'misses'
					WHERE id=$1 AND (data ? 'misses' OR (state='open' AND text IS DISTINCT FROM $2))`, h["id"], p.Text); err != nil {
					return err
				}
			} else if err := exec(q, "UPDATE question SET data=data-'misses' WHERE id=$1 AND data ? 'misses'", h["id"]); err != nil {
				return err
			}
			continue
		}
		n, _ := strconv.Atoi(astr(h["misses"]))
		if n+1 >= problemCleanRuns {
			if err := problemClose(q, c); err != nil {
				return err
			}
			continue
		}
		if err := exec(q, "UPDATE question SET data=jsonb_set(data,'{misses}',to_jsonb($2::int)) WHERE id=$1", h["id"], n+1); err != nil {
			return err
		}
	}
	for _, p := range current {
		if have[p.Cause] {
			continue
		}
		if err := problemAsk(q, source, p); err != nil {
			return fmt.Errorf("problem %s: %w", p.Cause, err)
		}
	}
	return nil
}
