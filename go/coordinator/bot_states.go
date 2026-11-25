// Bot runs (docs/designs/fleet-conversations.md, migration step 4): a bot run is the run (execution) of a turn in one of
// the bot's conversations, so its state is the execution's (execution_states.go). settleBotRun adds the one fact a run
// does not have by itself: whether a wake that succeeded changed nothing (quiet).
package main

import (
	"fleet/coordinator/db"
)

// settleBotRun records the quiet verdict of a bot wake's run once it succeeded (a message is never quiet) and wakes the
// bots loop when a bot run ended. Safe to call inside any transaction holding the execution row.
func settleBotRun(q *Request, executionID string) error {
	if executionID == "" {
		return nil
	}
	e, err := unscoped(q, func() (*agentExecution, error) {
		return executionMaybe(q, executionID)
	})
	if err != nil || e == nil || e.BotContext == nil {
		return err
	}
	if bc := e.BotContext; e.State == "succeeded" && bc.Cause != "" && bc.Quiet == nil {
		mutated, err := unscoped(q, func() (bool, error) {
			return q.db().BotExecutionMutated(q.ctx(), executionID)
		})
		if err != nil {
			return err
		}
		if _, err := unscoped(q, func() (struct{}, error) {
			return struct{}{}, q.db().BotRunSetQuiet(q.ctx(), db.BotRunSetQuietParams{ID: executionID, Quiet: !mutated})
		}); err != nil {
			return err
		}
	}
	if e.terminal() && q.app != nil {
		afterCommit(q, func() { q.app.wakeBots() })
	}
	return nil
}
