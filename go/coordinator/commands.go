// Host commands (docs/designs/fleet-standard.md S5): a command's status is a state machine; commandTransition is the only
// writer of command.status after the command is created, and commandStop stops open commands in bulk through the same
// table. The record shape callers read and write (all/get/put of "command") is in records.go (recCommand, commandRecPut).
package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// commandTransitions: the statuses a command may move each status to (host receipts: accepted, started, unknown and a
// final one; a stop or a refused poll: stopped). started and unknown are one rank: either may follow the other.
var commandTransitions = map[string][]string{
	"pending":   {"accepted", "started", "unknown", "completed", "failed", "stopped"},
	"accepted":  {"started", "unknown", "completed", "failed", "stopped"},
	"started":   {"unknown", "completed", "failed", "stopped"},
	"unknown":   {"started", "completed", "failed", "stopped"},
	"completed": nil,
	"failed":    nil,
	"stopped":   nil,
}

// commandOpen: the statuses a stop moves to stopped.
var commandOpen = []db.CommandStatus{db.CommandStatusPending, db.CommandStatusAccepted, db.CommandStatusStarted, db.CommandStatusUnknown}

// commandCreate writes a command; a new one starts with its status, an existing one keeps it (commandRecPut, records.go,
// moves it through commandTransition).
func commandCreate(q *Request, p db.CommandUpsertParams) error {
	return q.db().CommandUpsert(q.ctx(), p)
}

// commandStatus: the command's status, locked for this transaction; "" when there is none.
func commandStatus(q *Request, id string) (string, error) {
	s, err := q.db().CommandStatusOf(q.ctx(), id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return string(s), nil
}

// commandTransition moves command id to status to (detail: the stop detail, nil keeps it). The same status is no move; a
// move the table does not allow is a 409.
func commandTransition(q *Request, id, to string, detail *string) error {
	from, err := commandStatus(q, id)
	if err != nil {
		return err
	}
	if from == "" {
		return errNotFound
	}
	if from == to {
		return nil
	}
	if !slices.Contains(commandTransitions[from], to) {
		return fail(409, fmt.Sprintf("command %s cannot move from %s to %s", id, from, to))
	}
	return q.db().CommandMove(q.ctx(), db.CommandMoveParams{ID: id, Status: db.CommandStatus(to), StopDetail: detail})
}

// commandDetail records a new stop detail of a command that keeps its status.
func commandDetail(q *Request, id string, detail *string) error {
	from, err := commandStatus(q, id)
	if err != nil || from == "" || detail == nil {
		return err
	}
	return q.db().CommandMove(q.ctx(), db.CommandMoveParams{ID: id, Status: db.CommandStatus(from), StopDetail: detail})
}

// commandStop stops the commands in one of statuses (none: every open one) of an execution, a host or one command,
// optionally only of kinds; it returns the stopped ids.
type commandStopFilter struct {
	executionID, hostID, id *string
	kinds                   []string
	statuses                []db.CommandStatus
	detail                  *string
}

func commandStop(q *Request, f commandStopFilter) ([]string, error) {
	if f.executionID == nil && f.hostID == nil && f.id == nil {
		return nil, fmt.Errorf("commandStop without a filter")
	}
	statuses := f.statuses
	if len(statuses) == 0 {
		statuses = commandOpen
	}
	names := make([]string, len(statuses))
	for i, x := range statuses {
		names[i] = string(x)
	}
	return q.db().CommandsStopOpen(q.ctx(), db.CommandsStopOpenParams{Statuses: names, StopDetail: f.detail, ExecutionID: f.executionID, HostID: f.hostID, ID: f.id, Kinds: f.kinds})
}
