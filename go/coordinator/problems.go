// Problems (docs/designs/fleet-system-management.md, Problems): rule findings and direct signals as needs-you
// questions keyed by cause, on the dispatch cause mechanism. Keys start with problem: so the dispatch sweep never
// touches them. An open or answered question holds its cause: an answered one (Dismiss) is not asked again while the
// cause is present; when the cause goes, the cause is cleared and an open question withdrawn.
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
	Data              doc    // hostId, storeId, executionId, exemplars, value
}

func problemAsk(q *Request, source string, p Problem) error {
	data := doc{"cause": p.Cause, "source": source}
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
	qid, err := askQuestion(q, doc{"kind": "operator_decision", "text": p.Text, "options": problemOptions, "askedBy": "fleet", "projectId": pid, "data": data})
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
	h, err := problemHeld(q, cause)
	if err != nil || h != nil {
		return err
	}
	return problemAsk(q, "direct", Problem{Cause: cause, Text: text, ProjectID: astr(projectID)})
}

// problemClose: the cause is gone: an open question is withdrawn, an answered one lets go of the cause.
func problemClose(q *Request, cause string) error {
	return exec(q, "UPDATE question SET state=CASE WHEN state='open' THEN 'withdrawn'::question_state ELSE state END, cause=NULL WHERE cause=$1 AND state IN ('open','answered')", cause)
}

// problemsSync: the full current set of one source (rules): new causes are asked; a held cause absent for
// problemCleanRuns runs in a row is closed; a present one resets its miss count.
func problemsSync(q *Request, source string, current []Problem) error {
	now := map[string]bool{}
	for _, p := range current {
		now[p.Cause] = true
	}
	held, err := queryRows(q, "SELECT id,cause,coalesce(data->>'misses','0') AS misses FROM question WHERE cause LIKE 'problem:%' AND state IN ('open','answered') AND data->>'source'=$1", source)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, h := range held {
		c := astr(h["cause"])
		have[c] = true
		if now[c] {
			if err := exec(q, "UPDATE question SET data=data-'misses' WHERE id=$1 AND data ? 'misses'", h["id"]); err != nil {
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
