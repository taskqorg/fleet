// Problems (docs/designs/fleet-system-management.md, Problems): rule findings and direct signals as needs-you
// questions keyed by cause, on the dispatch cause mechanism. Keys start with problem: so the dispatch sweep never
// touches them. An open or answered question holds its cause: an answered one (Dismiss) is not asked again while the
// cause is present; when the cause goes, the cause is cleared and an open question withdrawn.
package main

var problemOptions = []any{"investigate", "dismiss"}

// Problem is one current cause of a source.
type Problem struct {
	Cause, Text, Rule string
	ProjectID         string // "" for a system cause: addressed to the owner
	AddressedTo       string // "" : the owner, or the project's director
	Data              doc    // hostId, storeId, exemplars, value
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
