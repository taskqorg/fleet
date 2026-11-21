// Host commands (docs/designs/fleet-standard.md S5): a command's status is a state machine; commandTransition is the only
// writer of command.status after the command is created, and commandStop stops open commands in bulk through the same
// table. commandGet, commandRawByID, commandsList and commandPut read and write command rows over typed queries.
package main

import (
	"encoding/json"
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

// commandCreate writes a command; a new one starts with its status, an existing one keeps it (commandPut
// moves it through commandTransition).
func commandCreate(q *Request, p db.CommandUpsertParams) error {
	return q.db().CommandUpsert(q.ctx(), p)
}

type commandBody struct {
	Payload json.RawMessage `json:"payload,omitempty"`
	Run     json.RawMessage `json:"run,omitempty"`
}

func commandOfRow(r db.Command) (agentCommand, error) {
	var c agentCommand
	if len(r.Payload) > 0 {
		if err := restDecode(r.Payload, &c, &c.rest); err != nil {
			return c, err
		}
	}
	c.ID = r.ID
	c.ExecutionID = deref(r.ExecutionID)
	c.SessionID = deref(r.SessionID)
	c.HostID = deref(r.HostID)
	c.ProjectID = deref(r.ProjectID)
	c.Kind = r.Kind
	c.Status = string(r.Status)
	c.StopDetail = deref(r.StopDetail)
	c.HandedOut = r.HandedOut
	c.Traceparent = r.Traceparent
	return c, nil
}

func commandRawOf(r db.Command) (rawObject, error) {
	out, err := recordRaw(r.Payload)
	if err != nil {
		return nil, err
	}
	out["id"] = jsonBytes(r.ID)
	for k, v := range map[string]*string{
		"executionId": r.ExecutionID,
		"sessionId":   r.SessionID,
		"hostId":      r.HostID,
		"projectId":   r.ProjectID,
		"stopDetail":  r.StopDetail,
		"traceparent": r.Traceparent,
	} {
		if v != nil {
			out[k] = jsonBytes(*v)
		}
	}
	out["kind"] = jsonBytes(r.Kind)
	out["status"] = jsonBytes(string(r.Status))
	if r.HandedOut != nil {
		out["handedOut"] = jsonBytes(*r.HandedOut)
	}
	return out, nil
}

func commandGet(q *Request, id string) (agentCommand, error) {
	if id == "" {
		return agentCommand{}, errNotFound
	}
	r, err := q.db().CommandByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentCommand{}, errNotFound
	}
	if err != nil {
		return agentCommand{}, err
	}
	return commandOfRow(r)
}

func commandRawByID(q *Request, id string) (rawObject, error) {
	if id == "" {
		return nil, errNotFound
	}
	r, err := q.db().CommandByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return commandRawOf(r)
}

func commandsList(q *Request, p db.CommandsParams) ([]agentCommand, error) {
	rs, err := q.db().Commands(q.ctx(), p)
	if err != nil {
		return nil, err
	}
	out := make([]agentCommand, 0, len(rs))
	for _, r := range rs {
		c, err := commandOfRow(r)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// commandPut writes a command through its state owner: a new command is created with its status, an existing one keeps
// its status unless c.Status names another, which commandTransition moves it to.
func commandPut(q *Request, c *agentCommand) error {
	if c.ID == "" {
		return fmt.Errorf("command record without an id")
	}
	if c.Kind == "" {
		return fmt.Errorf("command %s without a kind", c.ID)
	}
	tp, run, err := commandTrace(q, c.ID, c.ExecutionID, c.Kind, c.Traceparent, c.Run)
	if err != nil {
		return err
	}
	c.Traceparent, c.Run = tp, run
	prev, err := commandStatus(q, c.ID)
	if err != nil {
		return err
	}
	status := c.Status
	if status == "" {
		status = firstString(prev, "pending")
		c.Status = status
	}
	if _, ok := commandTransitions[status]; !ok {
		return fmt.Errorf("command %s: unknown status %q", c.ID, status)
	}
	body, err := restEncode(commandBody{Payload: c.Payload, Run: c.Run}, c.rest)
	if err != nil {
		return err
	}
	if err := commandCreate(q, db.CommandUpsertParams{
		ID:          c.ID,
		ExecutionID: strPtr(c.ExecutionID),
		SessionID:   strPtr(c.SessionID),
		HostID:      strPtr(c.HostID),
		ProjectID:   strPtr(c.ProjectID),
		Kind:        c.Kind,
		Status:      db.CommandStatus(status),
		StopDetail:  strPtr(c.StopDetail),
		HandedOut:   c.HandedOut,
		Traceparent: c.Traceparent,
		Payload:     body,
	}); err != nil {
		return err
	}
	if prev != "" && prev != status {
		if err := commandTransition(q, c.ID, status, strPtr(c.StopDetail)); err != nil {
			return err
		}
	} else if prev != "" {
		if err := commandDetail(q, c.ID, strPtr(c.StopDetail)); err != nil {
			return err
		}
	}
	if status == "pending" && c.HostID != "" && q.app != nil {
		hid := c.HostID
		afterCommit(q, func() { q.app.wakeHost(hid) })
	}
	return nil
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
