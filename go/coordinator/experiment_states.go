// The experiment state machine (docs/designs/fleet-standard.md S5, DESIGN_RULES.md): experimentTransitions is its table
// and experimentTransition the only writer of experiment.state.
package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// experimentTransitions: the states (schema.sql, experiment.state) an experiment may move each state to. A draft
// launches; a running one pauses at a budget, stops or is done; a done one runs again for a retried trial; stopped is
// final.
var experimentTransitions = map[string][]string{
	"draft":   {"draft", "running"},
	"running": {"running", "paused", "stopped", "done"},
	"paused":  {"paused", "running", "stopped"},
	"done":    {"done", "running"},
	"stopped": nil,
}

// experimentEvent: one move. From, when set, names the states it applies to: from any other state the experiment stays
// as it is, or, with Strict, the move fails (409). StopReason, when set, replaces the recorded reason.
type experimentEvent struct {
	To         string
	From       []string
	Strict     bool
	StopReason *string
}

// experimentTransition moves experiment id; true when it moved. A move the table refuses fails (409). A launched
// experiment's move to any state but paused withdraws its open budget questions, so a late answer never moves a later
// pause (a budget pause asks its question before it moves).
func experimentTransition(q *Request, id string, ev experimentEvent) (bool, error) {
	cur, err := q.db().ExperimentLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errNotFound
	}
	if err != nil {
		return false, err
	}
	if ev.From != nil && !slices.Contains(ev.From, cur) {
		if ev.Strict {
			return false, fail(409, "Experiment is "+cur)
		}
		return false, nil
	}
	if cur == ev.To {
		if ev.StopReason != nil {
			return false, q.db().ExperimentMove(q.ctx(), db.ExperimentMoveParams{ID: id, State: cur, StopReason: ev.StopReason})
		}
		return false, nil
	}
	if !slices.Contains(experimentTransitions[cur], ev.To) {
		return false, fail(409, fmt.Sprintf("Experiment %s cannot become %s", cur, ev.To))
	}
	if err := q.db().ExperimentMove(q.ctx(), db.ExperimentMoveParams{ID: id, State: ev.To, StopReason: ev.StopReason}); err != nil {
		return false, err
	}
	if cur != "draft" && ev.To != "paused" {
		open, err := q.db().ExperimentOpenBudgetQuestions(q.ctx(), []string{id})
		if err != nil {
			return false, err
		}
		if err := questionsWithdraw(q, open); err != nil {
			return false, err
		}
	}
	if ev.To == "done" || ev.To == "stopped" { // a gate decides itself at close (lab_decider.go)
		if err := labGateDecide(q, id); err != nil {
			return false, err
		}
	}
	return true, nil
}

// experimentsStop stops the running or paused experiments of ids with reason in one statement, withdrawing their open
// budget questions as experimentTransition does.
func experimentsStop(q *Request, ids []string, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	stopped, err := q.db().ExperimentsStop(q.ctx(), db.ExperimentsStopParams{Ids: ids, Reason: reason})
	if err != nil || len(stopped) == 0 {
		return err
	}
	open, err := q.db().ExperimentOpenBudgetQuestions(q.ctx(), stopped)
	if err != nil {
		return err
	}
	for _, id := range stopped {
		if err := labGateDecide(q, id); err != nil {
			return err
		}
	}
	return questionsWithdraw(q, open)
}
