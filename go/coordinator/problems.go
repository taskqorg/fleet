// Problems (docs/designs/fleet-system-management.md, Problems): rule findings, dispatch stalls and direct signals as
// needs-you questions keyed by cause; the only writer of question.cause (DESIGN_RULES.md S6). One dedupe rule: an open
// or answered question holds its cause, so an answered one (Dismiss) is not asked again while the cause is present;
// when the cause goes, the cause is cleared and an open question withdrawn. A Bugsink issue that comes back
// (regression, unmute) asks again whatever the earlier answer (bugsink.go).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

var problemOptions = []any{"investigate", "dismiss"}

// Problem is one current cause of a source.
type Problem struct {
	Cause, Text, Rule string
	ProjectID         string // "" for a system cause
	AddressedTo       string // "": the owner (a project cause names its director here)
	RefreshText       bool   // Refresh the text while this question is open.
	Data              any    // a struct whose JSON fields join the question's data: hostId, storeId, executionId, exemplars, value
	Options           []any  // the answers offered; nil: investigate or dismiss
}

func problemAsk(q *Request, source string, p Problem) error {
	// The question's data: cause, source and rule, then the problem's own fields over them.
	data := map[string]json.RawMessage{"cause": jsonBytes(p.Cause)}
	if source != "" {
		data["source"] = jsonBytes(source)
	}
	if p.Rule != "" {
		data["rule"] = jsonBytes(p.Rule)
	}
	own, err := openObject(jsonBytes(p.Data))
	if err != nil {
		return fmt.Errorf("problem %s data: %w", p.Cause, err)
	}
	for k, v := range own {
		data[k] = v
	}
	to := p.AddressedTo
	if to == "" {
		to = ownerEmail(q)
	}
	options := p.Options
	if options == nil {
		options = problemOptions
	}
	fleet := "fleet"
	qid, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", Text: p.Text, Options: jsonBytes(options), AskedBy: &fleet, ProjectID: p.ProjectID, Data: jsonBytes(data)})
	if err != nil {
		return err
	}
	return q.db().TaskProblemSetCause(q.ctx(), db.TaskProblemSetCauseParams{ID: qid, Cause: &p.Cause, AddressedTo: &to})
}

// problemHeld: the open or answered question holding cause (id and state), nil when none.
func problemHeld(q *Request, cause string) (*db.TaskProblemHeldRow, error) {
	h, err := q.db().TaskProblemHeld(q.ctx(), &cause)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

// problemOpen: a one-off sender's cause (span writer, backup); asks once while held.
func problemOpen(q *Request, cause string, projectID any, text string) error {
	pid, _ := projectID.(string)
	return problemRaise(q, "direct", Problem{Cause: cause, Text: text, ProjectID: pid})
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
	held, err := q.db().TaskProblemsHeld(q.ctx(), db.TaskProblemsHeldParams{Causes: causes})
	if err != nil {
		return err
	}
	return problemsRelease(q, heldOf(held))
}

// problemsClosePrefix: problemClose for every held cause starting with prefix.
func problemsClosePrefix(q *Request, prefix string) error {
	held, err := q.db().TaskProblemsHeld(q.ctx(), db.TaskProblemsHeldParams{Prefix: &prefix})
	if err != nil {
		return err
	}
	return problemsRelease(q, heldOf(held))
}

// heldQuestion: a question holding a cause.
type heldQuestion struct {
	ID    string
	State db.QuestionState
}

func heldOf(rows []db.TaskProblemsHeldRow) []heldQuestion {
	out := make([]heldQuestion, 0, len(rows))
	for _, r := range rows {
		out = append(out, heldQuestion{r.ID, r.State})
	}
	return out
}

// problemsRelease: held questions let go of their cause; an open one is withdrawn.
func problemsRelease(q *Request, held []heldQuestion) error {
	open, answered := []string{}, []string{}
	for _, h := range held {
		if h.State == db.QuestionStateOpen {
			open = append(open, h.ID)
		} else {
			answered = append(answered, h.ID)
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
	return q.db().TaskProblemsLetGo(q.ctx(), ids)
}

// problemsSet: the full current set of the causes matching pattern (a regular expression), one pass of a source that
// sees all of them at once (dispatch): a held cause no longer present closes at once, a new one is asked.
func problemsSet(q *Request, source, pattern string, current []Problem) error {
	held, err := q.db().TaskProblemsMatching(q.ctx(), pattern)
	if err != nil {
		return err
	}
	now, have, gone := map[string]bool{}, map[string]bool{}, []heldQuestion{}
	for _, p := range current {
		now[p.Cause] = true
	}
	for _, h := range held {
		have[h.Cause] = true
		if !now[h.Cause] {
			gone = append(gone, heldQuestion{h.ID, h.State})
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
	held, err := q.db().TaskProblemsOfSource(q.ctx(), source)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, h := range held {
		have[h.Cause] = true
		if p, present := now[h.Cause]; present {
			if p.RefreshText {
				err = q.db().TaskProblemRefresh(q.ctx(), db.TaskProblemRefreshParams{ID: h.ID, Text: p.Text})
			} else {
				err = q.db().TaskProblemPresent(q.ctx(), h.ID)
			}
			if err != nil {
				return err
			}
			continue
		}
		n, _ := strconv.Atoi(h.Misses)
		if n+1 >= problemCleanRuns {
			if err := problemClose(q, h.Cause); err != nil {
				return err
			}
			continue
		}
		if err := q.db().TaskProblemMissed(q.ctx(), db.TaskProblemMissedParams{ID: h.ID, Misses: int32(n + 1)}); err != nil {
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
