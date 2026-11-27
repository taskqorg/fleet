package main

// Session view (session_view, docs/designs/fleet-conversations.md "Session view"): one read for the conversation
// screen, composed here from summaries: the head, the composer target, open questions, the
// session's and its bot's triggers, the context panel, the last message and the newest turns. It reads no stored
// snapshot, dispatch or configuration of an execution (queries/session_view.sql); execution_get and tool_output load
// one item's details when it is opened.

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	op("session_view", sessionViewOp)
}

const (
	sessionViewRows    = 20
	sessionViewMaxRows = 200
)

// unixMs: a nullable time as nullable Unix milliseconds.
func unixMs(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	return ptr(t.UnixMilli())
}

func sessionViewOp(q *Request, r *contract.SessionViewRequest) (contract.SessionViewReply, error) {
	var out contract.SessionViewReply
	sid, err := sessionArg(r.SessionId)
	if err != nil {
		return out, err
	}
	s, err := sessionByID(q, sid)
	if err != nil {
		return out, err
	}
	limit := int64(sessionViewRows)
	if r.ExecutionsLimit != nil {
		limit = min(max(*r.ExecutionsLimit, 1), sessionViewMaxRows)
	}

	// Turns: the newest page, newest first; the head's state and active run come from the same rows.
	rows, err := q.db().SessionViewExecutions(q.ctx(), db.SessionViewExecutionsParams{SessionID: &sid, Before: r.ExecutionsBefore, Lim: int32(limit)})
	if err != nil {
		return out, err
	}
	total, err := q.db().SessionViewExecutionCount(q.ctx(), &sid)
	if err != nil {
		return out, err
	}
	out.Executions = contract.ExecutionsPage{Rows: make([]contract.ExecutionSummary, 0, len(rows)), Total: total}
	for _, e := range rows {
		out.Executions.Rows = append(out.Executions.Rows, contract.ExecutionSummary{
			Id: e.ID, Seq: e.Seq, Kind: e.Kind, State: e.State,
			StartedAt: unixMs(e.StartedAt), EndedAt: unixMs(e.EndedAt), Error: optStr(e.Error),
			Harness: e.Harness, Model: e.Model, HostId: e.HostID, Source: optStr(e.Source),
		})
	}
	if int64(len(rows)) == limit && len(rows) > 0 {
		out.Executions.NextBefore = &rows[len(rows)-1].Seq
	}

	// The head follows the newest turns whatever page is asked for.
	newest := rows
	if r.ExecutionsBefore != nil {
		if newest, err = q.db().SessionViewExecutions(q.ctx(), db.SessionViewExecutionsParams{SessionID: &sid, Lim: sessionViewRows}); err != nil {
			return out, err
		}
	}
	head, err := sessionViewHead(q, &s, newest)
	if err != nil {
		return out, err
	}
	out.Head = head

	target, err := composerTarget(q, &s)
	if err != nil && !errors.Is(err, errNotFound) {
		return out, err
	}
	out.Composer = contract.SessionComposer{SessionId: target.SessionId}
	if target.SessionId != nil {
		if *target.SessionId == sid {
			out.Composer.Agent = &head.Agent
		} else if ts, err := sessionByID(q, *target.SessionId); err == nil {
			a := sessionAgentOf(ts.AgentConfig)
			out.Composer.Agent = &a
		}
	}
	if target.Active != nil {
		out.Composer.ActiveExecutionId = &target.Active.Id
	}
	if target.SessionId == nil {
		out.Composer.Reason = optStr("No session can receive messages")
	}

	ids := []string{sid}
	if target.SessionId != nil && *target.SessionId != sid {
		ids = append(ids, *target.SessionId)
	}
	qs, err := q.db().SessionViewOpenQuestions(q.ctx(), ids)
	if err != nil {
		return out, err
	}
	out.Questions = make([]contract.SessionViewQuestion, 0, len(qs))
	for _, x := range qs {
		question := contract.SessionViewQuestion{Id: x.ID, SessionId: x.SessionID, Kind: x.Kind, Text: x.Text,
			Options: choiceLabels(x.Options), ExecutionId: x.ExecutionID}
		var authority deliveryAuthorityOptions
		if json.Unmarshal(x.Options, &authority) == nil && authority.DeliveryAuthority {
			question.Options, question.DeliveryAuthority = authority.Choices, ptr(true)
		}
		out.Questions = append(out.Questions, question)
	}

	if out.Triggers, err = triggerList(q, &contract.TriggerListRequest{SessionId: &sid}); err != nil {
		return out, err
	}
	if s.BotID != "" {
		bt, err := triggerList(q, &contract.TriggerListRequest{BotId: &s.BotID})
		if err == nil {
			out.BotTriggers = &bt
		} else if he := (*HTTPError)(nil); !errors.As(err, &he) || (he.Code != 403 && he.Code != 404) {
			return out, err
		}
	}
	if out.Context, err = sessionContext(q, &contract.SessionContextRequest{SessionId: sid}); err != nil {
		return out, err
	}

	m, err := q.db().SessionViewLastMessage(q.ctx(), sid)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return out, err
	default:
		lm := contract.SessionLastMessage{Id: m.ID, Text: m.Text, Attachments: []string{}}
		for _, a := range choiceLabels(m.Attachments, "artifactId") {
			lm.Attachments = append(lm.Attachments, a)
		}
		out.LastMessage = &lm
	}
	return out, nil
}

// sessionViewHead: the session's own fields, its conversation state and the latest run's id and error.
func sessionViewHead(q *Request, s *agentSession, rows []db.SessionViewExecutionsRow) (contract.SessionHead, error) {
	h := contract.SessionHead{Id: s.ID, ArchivedAt: s.ArchivedAt, BotId: optStr(s.BotID), ProjectId: optStr(s.ProjectID), Role: optStr(s.Role), Cost: []contract.CostFigure{}}
	if s.CreatedAt != 0 {
		h.CreatedAt = &s.CreatedAt
	}
	h.Agent = sessionAgentOf(s.AgentConfig)
	st, err := q.db().SessionCodeAndState(q.ctx(), s.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return h, err
	}
	h.Code, h.State = st.Code, st.State
	if h.State == "" {
		h.State = "idle"
	}
	for _, e := range rows {
		if e.Kind != "agent_run" {
			continue
		}
		if e.State == "preparing" || e.State == "running" || e.State == "waiting" {
			h.ActiveExecutionId = &e.ID
		}
		h.Error = optStr(e.Error)
		break
	}
	title, err := q.db().SessionViewTitle(q.ctx(), s.ID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return h, err
	}
	h.Title = title.Title
	h.ProjectCode = optStr(title.ProjectCode)
	// The conversation model fields (fleet-conversations.md#entities): how it began, what it is about, its projects.
	type model struct {
		Subject *string `db:"subject"`
		Origin  string  `db:"origin"`
	}
	sql, p := sqlSessionModel(s.ID)
	ms, err := typedRows[model](q, sql, p)
	if err != nil {
		return h, err
	}
	if len(ms) == 1 {
		o := contract.SessionOrigin(ms[0].Origin)
		h.Origin, h.Subject = &o, ms[0].Subject
		if sub := deref(ms[0].Subject); sub != "" {
			t, err := subjectTitle(q, sub)
			if err != nil {
				return h, err
			}
			h.SubjectTitle = &t
		}
	}
	h.ProjectIds = sessionProjects(s)
	res, err := sessionResourceRollup(q, s.ID)
	if err != nil {
		return h, err
	}
	if res.Total.Cost != nil {
		h.Cost = res.Total.Cost
	}
	return h, nil
}

// sessionAgentOf: the profile fields the composer chips show, from the session's stored agent configuration.
func sessionAgentOf(raw json.RawMessage) contract.SessionAgent {
	var cfg struct {
		ID         string   `json:"id"`
		Harness    string   `json:"harness"`
		Model      string   `json:"model"`
		Effort     string   `json:"effort"`
		Tools      []string `json:"tools"`
		GitNetwork *bool    `json:"gitNetwork"`
		Settings   struct {
			Mode    string `json:"mode"`
			Sandbox *bool  `json:"sandbox"`
		} `json:"settings"`
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &cfg)
	}
	a := contract.SessionAgent{ProfileId: optStr(cfg.ID), Harness: optStr(cfg.Harness), Model: optStr(cfg.Model), Effort: optStr(cfg.Effort),
		Mode: optStr(cfg.Settings.Mode), Sandbox: cfg.Settings.Sandbox, Tools: []string{}, GitNetwork: cfg.GitNetwork}
	for _, t := range cfg.Tools {
		if t != "" {
			a.Tools = append(a.Tools, t)
		}
	}
	return a
}

// choiceLabels: the strings of a JSON array whose items are strings or objects; an object gives its first non-empty
// field of names (default label, then value).
func choiceLabels(raw []byte, names ...string) []string {
	if len(names) == 0 {
		names = []string{"label", "value"}
	}
	out := []string{}
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return out
	}
	for _, it := range items {
		var s string
		if json.Unmarshal(it, &s) == nil {
			out = append(out, s)
			continue
		}
		var o struct {
			Label      string `json:"label"`
			Value      string `json:"value"`
			ArtifactID string `json:"artifactId"`
		}
		if json.Unmarshal(it, &o) != nil {
			continue
		}
		by := map[string]string{"label": o.Label, "value": o.Value, "artifactId": o.ArtifactID}
		for _, n := range names {
			if v := by[n]; v != "" {
				out = append(out, v)
				break
			}
		}
	}
	return out
}

// threadTitle names a thread whose item is not a task or question (or not visible): the main thread, Fleet, a host,
// a delivery, else the first line a person wrote in it. A raw key is never a title (fleet-conversations.md#entities).
func threadTitle(key, firstLine string) string {
	switch {
	case key == "":
		return "Main"
	case key == "fleet":
		return "Fleet"
	case strings.HasPrefix(key, "fleet:"):
		return "Fleet · " + strings.TrimPrefix(key, "fleet:")
	case strings.HasPrefix(key, "host:"):
		return "Host " + strings.TrimPrefix(key, "host:")
	case strings.HasPrefix(key, "delivery:"):
		return "Delivery " + strings.TrimPrefix(key, "delivery:")
	case strings.HasPrefix(key, "pr:"):
		return "PR " + strings.TrimPrefix(key, "pr:")
	case firstLine != "":
		return firstLine
	}
	return "Conversation"
}
