// The question state machine (docs/designs/fleet-standard.md S5, DESIGN_RULES.md): questionTransitions is its table and
// questionTransition the only writer of question.state. A question is asked open (askQuestion) and ends once, answered
// or withdrawn. Its cause belongs to problems.go.
package main

import (
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

var questionTransitions = map[string][]string{
	"open":      {"open", "answered", "withdrawn"},
	"answered":  nil,
	"withdrawn": nil,
}

// questionEvent: an answer (To answered: Answer, AnsweredBy, at AnsweredAt or, with AnsweredNow, now) or a withdrawal
// (To withdrawn, with its Withdrawal record when given).
type questionEvent struct {
	To          string
	Answer      *string
	AnsweredBy  *string
	AnsweredAt  *time.Time
	AnsweredNow bool
	Withdrawal  []byte
}

// questionTransition moves question id; true when it moved. Only an open question moves: an ended or missing one is
// left as it is (false), so a late answer or a repeated withdrawal changes nothing. A withdrawn question lets go of its
// cause (problems.go).
func questionTransition(q *Request, id string, ev questionEvent) (bool, error) {
	cur, err := q.db().QuestionLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if string(cur) == ev.To || !slices.Contains(questionTransitions[string(cur)], ev.To) {
		return false, nil
	}
	switch ev.To {
	case "answered":
		err = q.db().QuestionAnswer(q.ctx(), db.QuestionAnswerParams{ID: id, Answer: ev.Answer, AnsweredBy: ev.AnsweredBy, AnsweredAt: ev.AnsweredAt, AnsweredNow: ev.AnsweredNow})
	case "withdrawn":
		err = q.db().QuestionWithdraw(q.ctx(), db.QuestionWithdrawParams{ID: id, Withdrawal: ev.Withdrawal})
	}
	if err == nil { // an access card's answer moves its grant (access_grants.go)
		err = accessQuestionEnded(q, id, ev)
	}
	return err == nil, err
}

// questionsWithdraw withdraws the open questions of ids in one statement.
func questionsWithdraw(q *Request, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	withdrawn, err := q.db().QuestionsWithdrawOpen(q.ctx(), ids)
	if err != nil || len(withdrawn) == 0 {
		return err
	}
	for _, id := range withdrawn {
		if err := accessQuestionEnded(q, id, questionEvent{To: "withdrawn"}); err != nil {
			return err
		}
	}
	return nil
}
