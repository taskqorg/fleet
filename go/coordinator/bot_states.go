// Bot run state machine (docs/designs/fleet-standard.md S5, DESIGN_RULES.md): botRunTransitions is its table,
// botRunInsert its one insert and botRunTransition the only writer of bot_run.state.
package main

import (
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// botRunTransitions: the states (schema.sql, bot_run.state) a bot_run may move each state to.
var botRunTransitions = map[string][]string{
	"queued":    {"queued", "running", "waiting", "succeeded", "failed", "cancelled", "skipped"},
	"running":   {"running", "waiting", "succeeded", "failed", "cancelled"},
	"waiting":   {"waiting", "running", "succeeded", "failed", "cancelled"},
	"succeeded": nil,
	"failed":    nil,
	"cancelled": nil,
	"skipped":   nil,
}

func botRunEnds(state string) bool {
	targets, ok := botRunTransitions[state]
	return ok && len(targets) == 0
}

// botRunInsert: the one insert of a bot_run row; false when a conflict (active run or dedup_key) prevented insert.
func botRunInsert(q *Request, row db.BotRunInsertParams) (bool, error) {
	if row.State == "" {
		row.State = "queued"
	}
	if _, ok := botRunTransitions[row.State]; !ok {
		return false, fmt.Errorf("bot_run %s: unknown state %q", row.ID, row.State)
	}
	if row.Observation == "" {
		row.Observation = "confirmed"
	}
	if len(row.Payload) == 0 {
		row.Payload = []byte("{}")
	}
	n, err := q.db().BotRunInsert(q.ctx(), row)
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

type botRunEvent struct {
	To          string
	Observation *string
	Quiet       *bool
	Reason      *string
	StartedAt   *time.Time
	EndedAt     *time.Time
}

// botRunTransition moves bot_run id to ev.To; true when the state changed.
func botRunTransition(q *Request, id string, ev botRunEvent) (bool, error) {
	if _, ok := botRunTransitions[ev.To]; !ok {
		return false, fail(400, "Unknown bot_run state "+ev.To)
	}
	from, err := q.db().BotRunLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, errNotFound
	}
	if err != nil {
		return false, err
	}
	if from == ev.To {
		return false, nil
	}
	if !slices.Contains(botRunTransitions[from], ev.To) {
		return false, fail(409, fmt.Sprintf("Bot run %s cannot become %s", from, ev.To))
	}
	params := db.BotRunMoveParams{
		ID:          id,
		State:       ev.To,
		Observation: ev.Observation,
		SetStarted:  ev.To == "running" || ev.StartedAt != nil,
		StartedAt:   ev.StartedAt,
		SetEnded:    botRunEnds(ev.To) || ev.EndedAt != nil,
		EndedAt:     ev.EndedAt,
	}
	if ev.Quiet != nil {
		params.SetQuiet = true
		params.Quiet = *ev.Quiet
	}
	if ev.Reason != nil {
		params.SetReason = true
		params.Reason = *ev.Reason
	}
	return true, q.db().BotRunMove(q.ctx(), params)
}

func botRunStateForExecution(execState string) string {
	if execState == "preparing" {
		return "queued"
	}
	return execState
}

// syncBotRunFromExecution aligns the linked bot_run row (if any) with execution id's current state, observation,
// quiet-wake status and timestamps. Safe to call inside any transaction holding the execution row.
func syncBotRunFromExecution(q *Request, executionID string) error {
	if executionID == "" {
		return nil
	}
	br, err := unscoped(q, func() (db.BotRunByExecutionRow, error) {
		return q.db().BotRunByExecution(q.ctx(), &executionID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	e, err := unscoped(q, func() (*agentExecution, error) {
		return executionMaybe(q, executionID)
	})
	if err != nil || e == nil {
		return err
	}
	targetState := botRunStateForExecution(e.State)
	obs := firstString(e.Observation, "confirmed")
	var reason *string
	if msg := firstString(e.Error, e.Problem, e.StopDetail); msg != "" {
		reason = &msg
	}
	var quiet *bool
	if targetState == "succeeded" && br.Cause != "message" {
		mutated, err := unscoped(q, func() (bool, error) {
			return q.db().BotExecutionMutated(q.ctx(), executionID)
		})
		if err != nil {
			return err
		}
		qval := !mutated
		quiet = &qval
	}
	var startedAt, endedAt *time.Time
	if ms := nullInt(jsonValue(e.StartedAt)); ms != nil {
		startedAt = msTimeOf(ms)
	}
	if ms := nullInt(jsonValue(e.FinishedAt)); ms != nil {
		endedAt = msTimeOf(ms)
	}
	changed, err := unscoped(q, func() (bool, error) {
		if targetState != br.State && !botRunEnds(br.State) {
			return botRunTransition(q, br.ID, botRunEvent{
				To:          targetState,
				Observation: &obs,
				Quiet:       quiet,
				Reason:      reason,
				StartedAt:   startedAt,
				EndedAt:     endedAt,
			})
		}
		return false, nil
	})
	if err != nil {
		return err
	}
	if !changed {
		params := db.BotRunSyncFieldsParams{
			ID:          br.ID,
			Observation: &obs,
			SetStarted:  startedAt != nil,
			StartedAt:   startedAt,
			SetEnded:    endedAt != nil,
			EndedAt:     endedAt,
		}
		if quiet != nil {
			params.SetQuiet = true
			params.Quiet = *quiet
		}
		if reason != nil {
			params.SetReason = true
			params.Reason = *reason
		}
		if _, err := unscoped(q, func() (struct{}, error) { return struct{}{}, q.db().BotRunSyncFields(q.ctx(), params) }); err != nil {
			return err
		}
	}
	if botRunEnds(targetState) && q.app != nil {
		afterCommit(q, func() { q.app.wakeBots() })
	}
	return nil
}
