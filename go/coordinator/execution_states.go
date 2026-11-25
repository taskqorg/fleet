// The execution state machine (docs/designs/fleet-standard.md S5, DESIGN_RULES.md): executionTransitions is its table,
// executionInsert its one insert and executionTransition the only writer of execution.state, running a move's effects
// (the totals and the ended event of an end) in the caller's transaction.
package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// executionTransitions: the states (schema.sql, execution_state) an execution may move each state to. The open states
// move among each other (a host's receipt may re-accept a running execution); an end is final.
var executionTransitions = map[string][]string{
	"preparing": {"preparing", "waiting", "running", "succeeded", "failed", "cancelled"},
	"waiting":   {"waiting", "preparing", "running", "succeeded", "failed", "cancelled"},
	"running":   {"running", "preparing", "waiting", "succeeded", "failed", "cancelled"},
	"succeeded": nil,
	"failed":    nil,
	"cancelled": nil,
}

// pipelineStepTransitions: a pipeline step row is the logical step of its run, not one attempt: its native executions
// are their own rows. It moves as an execution, and an ended step reopens when its run takes it up again (the observed
// end is cleared, go/coordinator/README.md).
var pipelineStepTransitions = map[string][]string{
	"preparing": executionTransitions["preparing"],
	"waiting":   executionTransitions["waiting"],
	"running":   executionTransitions["running"],
	"succeeded": {"preparing", "waiting", "running"},
	"failed":    {"preparing", "waiting", "running"},
	"cancelled": {"preparing", "waiting", "running"},
}

func executionEnds(state string) bool {
	_, ok := executionTransitions[state]
	return ok && executionTransitions[state] == nil
}

// executionInsert: the one insert of an execution row, in any state; false when its id or request_id exists.
func executionInsert(q *Request, row db.ExecutionInsertParams) (bool, error) {
	if row.State == "" {
		row.State = db.ExecutionStatePreparing
	}
	if _, ok := executionTransitions[string(row.State)]; !ok {
		return false, fmt.Errorf("execution %s: unknown state %q", row.ID, row.State)
	}
	_, err := q.db().ExecutionInsert(q.ctx(), row)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// executionEvent: one move of an execution. From, when set, names the states the move applies to; from any other
// state the execution stays as it is. Problem, when set, is recorded with the move (a success clears it). An end keeps
// a known ended_at, else now; KeepEnd leaves ended_at as its writer set it. Correct lets a tool run's recorder replace
// the provisional end of a transfer's started line (failed, unfinished) with the end its completed line reports.
type executionEvent struct {
	To          string
	From        []string
	Observation string
	Problem     *string
	KeepEnd     bool
	Correct     bool
}

// executionTransition moves execution id to ev.To; true when the state changed. A move the table refuses fails (409);
// one to the current state changes nothing. An end writes the execution's totals (a tool run's come from its
// connection accounting) and its ended event.
func executionTransition(q *Request, id string, ev executionEvent) (bool, error) {
	if _, ok := executionTransitions[ev.To]; !ok {
		return false, fail(400, "Unknown execution state "+ev.To)
	}
	cur, err := q.db().ExecutionLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errNotFound
	}
	if err != nil {
		return false, err
	}
	from := string(cur.State)
	if ev.From != nil && !slices.Contains(ev.From, from) || from == ev.To {
		return false, nil
	}
	correction := ev.Correct && cur.Kind == db.ExecutionKindToolRun && executionEnds(from) && executionEnds(ev.To)
	table := executionTransitions
	if cur.Kind == db.ExecutionKindPipelineStep {
		table = pipelineStepTransitions
	}
	if !slices.Contains(table[from], ev.To) && !correction {
		return false, fail(409, fmt.Sprintf("Execution %s cannot become %s", from, ev.To))
	}
	var obs *db.Observation
	if ev.Observation != "" {
		o := db.Observation(ev.Observation)
		obs = &o
	}
	ends := executionEnds(ev.To)
	if err := q.db().ExecutionMove(q.ctx(), db.ExecutionMoveParams{ID: id, State: db.ExecutionState(ev.To), Observation: obs,
		SetProblem: ev.Problem != nil, Problem: ev.Problem, Ends: ends && !ev.KeepEnd, Reopens: executionEnds(from) && !ends}); err != nil {
		return false, err
	}
	if ends {
		if cur.Kind != db.ExecutionKindToolRun {
			if err := writeTotals(q, id); err != nil {
				return false, err
			}
		}
		if err := writeEnded(q, id); err != nil {
			return false, err
		}
		if cur.Kind != db.ExecutionKindToolRun && cur.Kind != db.ExecutionKindPipelineStep && cur.Kind != db.ExecutionKindPipelineRun {
			if err := pipelineOnEnded(q, id); err != nil { // an agent or script step's run moves on
				return false, err
			}
		}
	}
	if err := settleBotRun(q, id); err != nil {
		return false, err
	}
	return true, nil
}
