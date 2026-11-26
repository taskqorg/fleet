// Sessions (docs/designs/fleet-operator-app.md §6): create, profile, read with its queue, questions, handoffs and
// delegations, archive, message submit, the Conversations list and list membership.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// sessionListDBRow: a sessions_list and list_membership row (sqlSessionListCols) with its group and sort keys.
type sessionListDBRow struct {
	ID            string   `db:"id"`
	Code          *string  `db:"code"`
	Title         *string  `db:"title"`
	FirstMessage  *string  `db:"first_message"`
	State         string   `db:"state"`
	Harness       *string  `db:"harness"`
	AgentHarness  *string  `db:"agent_harness"`
	ProfileID     *string  `db:"profile_id"`
	ProjectID     *string  `db:"project_id"`
	BotID         *string  `db:"bot_id"`
	Origin        string   `db:"origin"`
	Subject       *string  `db:"subject"`
	Listed        bool     `db:"listed"`
	CreatedAt     *float64 `db:"created_ms"`
	ArchivedAt    *float64 `db:"archived_ms"`
	ActivityAt    *string  `db:"activity_at"`
	Executions    int64    `db:"executions"`
	OpenQuestions int64    `db:"open_questions"`
	NowText       *string  `db:"now_text"`
	TaskTitle     *string  `db:"task_title"`
	WaitingSince  *string  `db:"waiting_since"`
	RunStartedAt  *string  `db:"run_started_at"`
	LastStartedAt *string  `db:"last_started_at"`
	FailedStarts  int64    `db:"failed_starts"`
	G             string   `db:"g"`
	Sk            float64  `db:"sk"`
}

func (x sessionListDBRow) row() contract.SessionRow {
	// The stored title (session_rename, or the generated one: session_titles.go); until it exists, the first message with
	// its links named, never raw (plainTitle).
	var title *string
	if t := []rune(firstString(plainTitle(deref(x.Title)), plainTitle(clip(deref(x.FirstMessage), 400)))); len(t) > 0 {
		title = ptr(string(t[:min(200, len(t))]))
	}
	harness := x.Harness
	if harness == nil {
		harness = x.AgentHarness
	}
	return contract.SessionRow{Id: x.ID, Code: x.Code, Title: title, State: x.State, Harness: harness, ProfileId: x.ProfileID, ProjectId: x.ProjectID, BotId: x.BotID,
		Origin: ptr(contract.SessionOrigin(x.Origin)), Listed: &x.Listed, Subject: x.Subject,
		CreatedAt: x.CreatedAt, ActivityAt: x.ActivityAt, Now: nowLine(deref(x.NowText), deref(x.TaskTitle)), Executions: x.Executions, OpenQuestions: x.OpenQuestions, ArchivedAt: x.ArchivedAt,
		WaitingSince: x.WaitingSince, RunStartedAt: x.RunStartedAt, LastStartedAt: x.LastStartedAt, FailedStarts: &x.FailedStarts,
		SortKey: sortKey(x.G, x.Sk, x.ID)}
}

// nowLineRunes: the longest now line; a longer sentence is cut at a word and ends with an ellipsis.
const nowLineRunes = 160

var (
	nowLink    = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	nowMarker  = regexp.MustCompile(`^(>\s*|[-*+]\s+|\d+[.)]\s+)+`)
	nowHeading = regexp.MustCompile(`^#{1,6}(\s|$)`)
	nowEnd     = regexp.MustCompile(`[.!?…](\s|$)`)
)

// nowLine: what a session does now, in one line (docs/design-system/pages/home.md "Session card"): the first sentence
// of the latest agent message, else the attached task's title; nil when there is neither. The agent's own words come
// first because every session has them (bots and PMs have no task) and they move with the work; a task title is set
// once. Markdown is reduced to its text: the first line outside a code fence that is no heading, without list or quote markers,
// emphasis, code ticks or link targets.
func nowLine(markdown, taskTitle string) *string {
	if l := firstSentence(markdown); l != "" {
		return &l
	}
	if l := clipRunes(strings.Join(strings.Fields(taskTitle), " "), nowLineRunes); l != "" {
		return &l
	}
	return nil
}

func firstSentence(markdown string) string {
	fence := false
	for _, line := range strings.Split(markdown, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = !fence
			continue
		}
		if fence || t == "" || nowHeading.MatchString(t) || strings.Trim(t, "-*_=|: ") == "" { // headings, rules and table separators say no sentence
			continue
		}
		t = nowMarker.ReplaceAllString(t, "")
		t = nowLink.ReplaceAllString(t, "$1")
		t = strings.NewReplacer("**", "", "__", "", "`", "").Replace(t)
		t = strings.Join(strings.Fields(t), " ")
		if loc := nowEnd.FindStringIndex(t); loc != nil {
			t = strings.TrimSpace(t[:loc[1]])
		}
		if t != "" {
			return clipRunes(t, nowLineRunes)
		}
	}
	return ""
}

// clipRunes: s cut to at most n runes, at the last space when there is one, ending with an ellipsis.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n-1])
	if i := strings.LastIndex(cut, " "); i > len(cut)/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:") + "…"
}

// sortKey: a list row's position, [group key, sort key, id].
func sortKey(g string, sk float64, id string) []contract.SortKeyPart {
	return []contract.SortKeyPart{jsonBytes(g), jsonBytes(sk), jsonBytes(id)}
}

// sessionFilter: a sessions list's filter as sent (rowFilterOf).
func sessionFilter(raw json.RawMessage) (rowFilter, error) {
	return rowFilterOf(raw, "projectId", "excludeProjectId", "state", "search", "archived", "botId", "subject", "room", "origin", "listed")
}

// sessionListRows: the session rows of an assembled list statement.
func sessionListRows(q *Request, sql string, p []any, err error) ([]contract.SessionRow, error) {
	if err != nil {
		return nil, err
	}
	rs, err := typedRows[sessionListDBRow](q, sql, p)
	out := make([]contract.SessionRow, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.row())
	}
	return out, err
}

// sessionSummary: counts per state (without the state filter, for the quick filter) and the groups of the filtered list,
// in one pass over the sessions: rows match the filter without states, and the groups count those that also match the states.
func sessionSummary(q *Request, f rowFilter, o sessionOrder) (map[string]int64, []contract.ListGroup, error) {
	sql, p, err := sqlSessionSummary(f, o)
	if err != nil {
		return nil, nil, err
	}
	type summaryRow struct {
		State string  `db:"state"`
		G     string  `db:"g"`
		Label *string `db:"label"`
		N     int64   `db:"n"`
		Ng    int64   `db:"ng"`
	}
	rs, err := typedRows[summaryRow](q, sql, p)
	if err != nil {
		return nil, nil, err
	}
	counts, groups := zeroCounts(sessionStates), []contract.ListGroup{}
	for _, r := range rs {
		counts[r.State] += r.N
		if o.name == "none" || r.Ng == 0 {
			continue
		}
		// Rows come ordered by group key, so a group's rows (one per state) are adjacent.
		if n := len(groups); n > 0 && groups[n-1].Key == r.G {
			groups[n-1].Count += r.Ng
			if groups[n-1].Label == nil {
				groups[n-1].Label = r.Label
			}
			continue
		}
		groups = append(groups, contract.ListGroup{Key: r.G, Label: r.Label, Count: r.Ng})
	}
	return counts, groups, nil
}

// listAfter: a page request's after, the sort key of the last row it has: [group key, sort key, id].
func listAfter(after []json.RawMessage) ([]any, error) {
	if after == nil {
		return nil, nil
	}
	var a []any
	if json.Unmarshal(jsonBytes(after), &a) != nil || len(a) != 3 {
		return nil, fail(400, "Invalid after")
	}
	return a, nil
}

func sessionsPage(q *Request, r *contract.SessionsListRequest) (contract.SessionsListReply, error) {
	// Read as sent: a null projectId item means sessions outside projects, which the typed list cannot hold.
	var raw struct {
		Filter json.RawMessage `json:"filter"`
	}
	json.Unmarshal(q.body, &raw)
	return sessionsPageOf(q, r, raw.Filter)
}

// sessionsPageOf: sessions_list with the filter's text given (a view composing it passes its own).
func sessionsPageOf(q *Request, r *contract.SessionsListRequest, filter json.RawMessage) (contract.SessionsListReply, error) {
	var out contract.SessionsListReply
	f, err := sessionFilter(filter)
	if err != nil {
		return out, err
	}
	o, err := sessionOrderOf(r.Group, r.Sort)
	if err != nil {
		return out, err
	}
	limit := 100
	if r.Limit != nil {
		limit = pageLimit(float64(*r.Limit), 100)
	}
	after, err := listAfter(r.After)
	if err != nil {
		return out, err
	}
	sql, p, err := sqlSessionPage(f, o, after, limit+1)
	rs, err := sessionListRows(q, sql, p, err)
	if err != nil {
		return out, err
	}
	if out.Counts, out.Groups, err = sessionSummary(q, f, o); err != nil {
		return out, err
	}
	out.Sessions = rs[:min(limit, len(rs))]
	if len(rs) > limit {
		out.NextAfter = jsonBytes(out.Sessions[len(out.Sessions)-1].SortKey)
	}
	out.Total = total(out.Counts)
	if after == nil { // the first page carries the Started by and Steps counts
		if out.Origins, out.HiddenCount, err = sessionOriginCounts(q, f); err != nil {
			return out, err
		}
	}
	return out, nil
}

// sessionOriginCounts: the conversations of the filter by origin (the origin filter left out) and the hidden ones.
func sessionOriginCounts(q *Request, f rowFilter) (map[string]int64, *int64, error) {
	sql, p, err := sqlSessionOrigins(f)
	if err != nil {
		return nil, nil, err
	}
	type row struct {
		Origin string `db:"origin"`
		N      int64  `db:"n"`
	}
	rs, err := typedRows[row](q, sql, p)
	if err != nil {
		return nil, nil, err
	}
	out, hidden := map[string]int64{}, int64(0)
	for _, r := range rs {
		if r.Origin == "" {
			hidden = r.N
		} else {
			out[r.Origin] = r.N
		}
	}
	return out, &hidden, nil
}

// related: a delegation record with the states of its parent, child and continuation runs.
func related(q *Request, d rawObject) (rawObject, error) {
	out := rawObject{}
	maps.Copy(out, d)
	for _, r := range []struct{ key, field string }{{"parentState", "parentExecutionId"}, {"childState", "childExecutionId"}, {"continuationState", "continuationExecutionId"}} {
		e, err := executionMaybe(q, d.text(r.field))
		if err != nil {
			return nil, err
		}
		out[r.key] = json.RawMessage("null")
		if e != nil {
			if e.State != "" {
				out[r.key] = jsonBytes(e.State)
			}
		} else if r.key != "continuationState" && d.has(r.field) {
			return nil, errNotFound
		}
	}
	return out, nil
}

// delegationsOf: the delegations read (SessionDelegations, ExecutionDelegations), each with the states of its runs.
func delegationsOf(q *Request, ds [][]byte, err error) ([]rawObject, error) {
	if err != nil {
		return nil, err
	}
	out := []rawObject{}
	for _, raw := range ds {
		var d rawObject
		if json.Unmarshal(raw, &d) != nil {
			continue
		}
		r, err := related(q, d)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

type sessionPayload struct {
	DeliveryID           string                 `json:"deliveryId,omitempty"`
	AgentConfig          json.RawMessage        `json:"agentConfig,omitempty"`
	StartRequest         string                 `json:"startRequest,omitempty"`
	Role                 string                 `json:"role,omitempty"`
	Preset               string                 `json:"preset,omitempty"`
	Starter              string                 `json:"starter,omitempty"`
	Account              string                 `json:"account,omitempty"`
	Access               string                 `json:"access,omitempty"`
	Branch               *bool                  `json:"branch,omitempty"`
	Setup                string                 `json:"setup,omitempty"`
	WorkingDirectory     string                 `json:"workingDirectory,omitempty"`
	WorkspaceKey         string                 `json:"workspaceKey,omitempty"`
	NativeSessionID      string                 `json:"nativeSessionId,omitempty"`
	NativeSessions       map[string]agentNative `json:"nativeSessions,omitempty"`
	ForkNativeSessionID  string                 `json:"forkNativeSessionId,omitempty"`
	ForkOf               *agentForkOf           `json:"forkOf,omitempty"`
	PendingHandoffID     string                 `json:"pendingHandoffId,omitempty"`
	PendingTransition    *agentTransitionArgs   `json:"pendingTransition,omitempty"`
	TransitionCleanup    json.RawMessage        `json:"transitionCleanup,omitempty"`
	Focus                *agentFocus            `json:"focus,omitempty"`
	ReadinessOfSessionID string                 `json:"readinessOfSessionId,omitempty"`
}

func sessionOf(r db.Session) (agentSession, error) {
	var s agentSession
	if len(r.Payload) > 0 {
		if err := restDecode(r.Payload, &s, &s.rest); err != nil {
			return s, err
		}
	}
	s.ID = r.ID
	s.ProjectID = deref(r.ProjectID)
	s.TaskID = deref(r.TaskID)
	s.HostID = deref(r.HostID)
	s.Actor = deref(r.Actor)
	s.DirectedBy = deref(r.DirectedBy)
	s.DirectorAccessRevokedAt = r.DirectorAccessRevokedAt
	s.ParentExecutionID = deref(r.ParentExecutionID)
	s.CreatedByExecutionID = deref(r.CreatedByExecutionID)
	s.CreatedBySessionID = deref(r.CreatedBySessionID)
	s.SourceSessionID = deref(r.SourceSessionID)
	s.PipelineRunID = deref(r.PipelineRunID)
	s.BotID = deref(r.BotID)
	s.CreatedAt = int64Ms(r.CreatedAt)
	s.ArchivedAt = int64MsPtr(r.ArchivedAt)
	return s, nil
}

func sessionByID(q *Request, id string) (agentSession, error) {
	if id == "" {
		return agentSession{}, errNotFound
	}
	r, err := q.db().SessionByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentSession{}, errNotFound
	}
	if err != nil {
		return agentSession{}, err
	}
	return sessionOf(r)
}

func sessionLocked(q *Request, id string) (agentSession, error) {
	if id == "" {
		return agentSession{}, errNotFound
	}
	r, err := q.db().SessionByIDLocked(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentSession{}, errNotFound
	}
	if err != nil {
		return agentSession{}, err
	}
	return sessionOf(r)
}

func sessionMaybe(q *Request, id string) (*agentSession, error) {
	s, err := sessionByID(q, id)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

func sessionsByStartRequest(q *Request, key string) ([]agentSession, error) {
	rs, err := q.db().SessionsByStartRequest(q.ctx(), key)
	if err != nil {
		return nil, err
	}
	out := make([]agentSession, 0, len(rs))
	for _, r := range rs {
		s, err := sessionOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func sessionRecordOf(r db.Session) (rawObject, error) {
	out, err := recordRaw(r.Payload)
	if err != nil {
		return nil, err
	}
	out["id"] = jsonBytes(r.ID)
	for k, v := range map[string]*string{
		"projectId":            r.ProjectID,
		"taskId":               r.TaskID,
		"hostId":               r.HostID,
		"actor":                r.Actor,
		"directedBy":           r.DirectedBy,
		"parentExecutionId":    r.ParentExecutionID,
		"createdByExecutionId": r.CreatedByExecutionID,
		"createdBySessionId":   r.CreatedBySessionID,
		"sourceSessionId":      r.SourceSessionID,
		"pipelineRunId":        r.PipelineRunID,
		"botId":                r.BotID,
		"title":                r.Title,
		"titleBy":              r.TitleBy,
		"titleCall":            r.TitleCall,
		"titleUnknownCall":     r.TitleUnknownCall,
	} {
		if v != nil {
			out[k] = jsonBytes(*v)
		}
	}
	for k, v := range map[string]*time.Time{
		"directorAccessRevokedAt": r.DirectorAccessRevokedAt,
		"createdAt":               r.CreatedAt,
		"archivedAt":              r.ArchivedAt,
		"titleAttemptAt":          r.TitleAttemptAt,
	} {
		if v != nil {
			out[k] = jsonBytes(int64Ms(v))
		}
	}
	if r.TitleAttempts != 0 {
		out["titleAttempts"] = jsonBytes(r.TitleAttempts)
	}
	return out, nil
}

func sessionRecordByID(q *Request, id string) (rawObject, error) {
	if id == "" {
		return nil, errNotFound
	}
	r, err := q.db().SessionByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return sessionRecordOf(r)
}

func sessionPutRow(q *Request, row db.Session, payload []byte) error {
	if row.ID == "" {
		return fmt.Errorf("session record without an id")
	}
	return q.db().SessionPut(q.ctx(), db.SessionPutParams{
		ID:                      row.ID,
		ProjectID:               row.ProjectID,
		TaskID:                  row.TaskID,
		HostID:                  row.HostID,
		Actor:                   row.Actor,
		DirectedBy:              row.DirectedBy,
		DirectorAccessRevokedAt: row.DirectorAccessRevokedAt,
		ParentExecutionID:       row.ParentExecutionID,
		CreatedByExecutionID:    row.CreatedByExecutionID,
		CreatedBySessionID:      row.CreatedBySessionID,
		SourceSessionID:         row.SourceSessionID,
		PipelineRunID:           row.PipelineRunID,
		BotID:                   row.BotID,
		CreatedAt:               row.CreatedAt,
		ArchivedAt:              row.ArchivedAt,
		Title:                   row.Title,
		TitleBy:                 row.TitleBy,
		TitleCall:               row.TitleCall,
		TitleAttemptAt:          row.TitleAttemptAt,
		TitleAttempts:           row.TitleAttempts,
		TitleUnknownCall:        row.TitleUnknownCall,
		Payload:                 payload,
	})
}

func sessionPut(q *Request, s *agentSession) error {
	p := sessionPayload{
		DeliveryID:           s.DeliveryID,
		AgentConfig:          s.AgentConfig,
		StartRequest:         s.StartRequest,
		Role:                 s.Role,
		Preset:               s.Preset,
		Starter:              s.Starter,
		Account:              s.Account,
		Access:               s.Access,
		Branch:               s.Branch,
		Setup:                s.Setup,
		WorkingDirectory:     s.WorkingDirectory,
		WorkspaceKey:         s.WorkspaceKey,
		NativeSessionID:      s.NativeSessionID,
		NativeSessions:       s.NativeSessions,
		ForkNativeSessionID:  s.ForkNativeSessionID,
		ForkOf:               s.ForkOf,
		PendingHandoffID:     s.PendingHandoffID,
		PendingTransition:    s.PendingTransition,
		TransitionCleanup:    s.TransitionCleanup,
		Focus:                s.Focus,
		ReadinessOfSessionID: s.ReadinessOfSessionID,
	}
	payload, err := restEncode(p, s.rest)
	if err != nil {
		return err
	}
	return sessionPutRow(q, db.Session{
		ID:                      s.ID,
		ProjectID:               strPtr(s.ProjectID),
		TaskID:                  strPtr(s.TaskID),
		HostID:                  strPtr(s.HostID),
		Actor:                   strPtr(s.Actor),
		DirectedBy:              strPtr(s.DirectedBy),
		DirectorAccessRevokedAt: s.DirectorAccessRevokedAt,
		ParentExecutionID:       strPtr(s.ParentExecutionID),
		CreatedByExecutionID:    strPtr(s.CreatedByExecutionID),
		CreatedBySessionID:      strPtr(s.CreatedBySessionID),
		SourceSessionID:         strPtr(s.SourceSessionID),
		PipelineRunID:           strPtr(s.PipelineRunID),
		BotID:                   strPtr(s.BotID),
		CreatedAt:               intMsPtr(s.CreatedAt),
		ArchivedAt:              optMsPtr(s.ArchivedAt),
	}, payload)
}

type messagePayload struct {
	TriggerID         string          `json:"triggerId,omitempty"`
	Attachments       json.RawMessage `json:"attachments,omitempty"`
	AttachmentRequest []string        `json:"attachmentRequest,omitempty"`
}

func messageOf(r db.Message) (agentMessage, error) {
	var m agentMessage
	if len(r.Payload) > 0 {
		if err := restDecode(r.Payload, &m, &m.rest); err != nil {
			return m, err
		}
	}
	m.ID = r.ID
	m.SessionID = r.SessionID
	m.ExecutionID = deref(r.ExecutionID)
	m.ProjectID = deref(r.ProjectID)
	m.RequestID = deref(r.RequestID)
	m.Text = deref(r.Text)
	m.SentBy = deref(r.SentBy)
	m.Source = deref(r.Source)
	return m, nil
}

func messagesBySession(q *Request, sid string, reqID *string) ([]agentMessage, error) {
	rs, err := q.db().Messages(q.ctx(), db.MessagesParams{SessionID: sid, RequestID: reqID})
	if err != nil {
		return nil, err
	}
	out := make([]agentMessage, 0, len(rs))
	for _, r := range rs {
		m, err := messageOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func messageRecordsOf(q *Request, sid string) ([]rawObject, error) {
	rs, err := q.db().Messages(q.ctx(), db.MessagesParams{SessionID: sid})
	if err != nil {
		return nil, err
	}
	out := make([]rawObject, 0, len(rs))
	for _, r := range rs {
		m, err := recordRaw(r.Payload)
		if err != nil {
			return nil, err
		}
		m["id"], m["sessionId"] = jsonBytes(r.ID), jsonBytes(r.SessionID)
		for k, v := range map[string]*string{
			"executionId": r.ExecutionID,
			"projectId":   r.ProjectID,
			"requestId":   r.RequestID,
			"text":        r.Text,
			"sentBy":      r.SentBy,
			"source":      r.Source,
		} {
			if v != nil {
				m[k] = jsonBytes(*v)
			}
		}
		out = append(out, m)
	}
	return out, nil
}

func messagePut(q *Request, m *agentMessage) error {
	if m.ID == "" {
		return fmt.Errorf("message record without an id")
	}
	payload, err := restEncode(messagePayload{TriggerID: m.TriggerID, Attachments: m.Attachments, AttachmentRequest: m.AttachmentRequest}, m.rest)
	if err != nil {
		return err
	}
	return q.db().MessagePut(q.ctx(), db.MessagePutParams{
		ID:          m.ID,
		SessionID:   m.SessionID,
		ExecutionID: strPtr(m.ExecutionID),
		ProjectID:   strPtr(m.ProjectID),
		RequestID:   strPtr(m.RequestID),
		Text:        &m.Text,
		SentBy:      strPtr(m.SentBy),
		Source:      strPtr(m.Source),
		Payload:     payload,
	})
}

func sessionGet(q *Request, sid string) (rawObject, error) {
	s, err := sessionRecordByID(q, sid)
	if err != nil {
		return nil, err
	}
	if q.person != nil && !fullAccess(q.person) {
		if raw, ok := s["nativeSessions"]; ok {
			s["nativeSessions"] = filterNativeSessionsByAccess(q, raw)
		}
		if raw, ok := s["workspaceSnapshots"]; ok {
			var ws map[string]json.RawMessage
			if json.Unmarshal(raw, &ws) == nil && len(ws) > 0 {
				filtered := map[string]json.RawMessage{}
				for k, v := range ws {
					parts := strings.SplitN(k, ":", 3)
					if len(parts) >= 2 && parts[1] != "" && !mayAccessProjectID(q.person, parts[1]) {
						continue
					}
					filtered[k] = v
				}
				s["workspaceSnapshots"] = jsonBytes(filtered)
			}
		}
	}
	v, err := sessionView(q, sid)
	if err != nil {
		return nil, err
	}
	add, err := asT[rawObject](v)
	if err != nil {
		return nil, err
	}
	maps.Copy(s, add)
	s["sessionId"] = jsonBytes(sid)
	status, err := q.db().SessionCodeAndState(q.ctx(), sid)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	s["code"] = jsonBytes(status.Code)
	s["state"] = jsonBytes(status.State)
	ds, err := q.db().SessionDelegations(q.ctx(), sid)
	delegations, err := delegationsOf(q, ds, err)
	if err != nil {
		return nil, err
	}
	s["delegations"] = jsonBytes(delegations)
	messages, err := messageRecordsOf(q, sid)
	if err != nil {
		return nil, err
	}
	s["messages"] = jsonBytes(messages)
	executions, err := executionsRawBySession(q, sid)
	if err != nil {
		return nil, err
	}
	s["executions"] = jsonBytes(executions)
	res, err := sessionResourceRollup(q, sid)
	if err != nil {
		return nil, err
	}
	s["cost"], s["resources"] = jsonBytes(res.Total.Cost), jsonBytes(res)
	return s, nil
}

func sessionProfile(q *Request, r *contract.SessionProfileRequest) (contract.SessionProfileReply, error) {
	var none contract.SessionProfileReply
	sid := r.SessionId
	s, err := sessionByID(q, sid)
	if err != nil {
		return none, err
	}
	pid := r.ProfileId
	if a, err := agentActive(q, sid); err != nil || a != nil {
		if err == nil {
			err = fail(409, "Execution active")
		}
		return none, err
	}
	if r.Account != nil && !accountPattern.MatchString(*r.Account) {
		return none, fail(400, "Invalid named host account")
	}
	if w := r.WorkingDirectory; w != nil {
		if !strings.HasPrefix(*w, "/") || strings.Contains(*w, "..") {
			return none, fail(400, "Invalid working directory")
		}
	}
	if s.NativeSessionID != "" {
		args := agentTransitionArgs{SessionID: sid, ProfileID: pid, WorkingDirectory: r.WorkingDirectory}
		if r.Account != nil {
			args.Account = jsonBytes(*r.Account)
		}
		t, err := agentTransition(q, args)
		if err != nil {
			return none, err
		}
		return asT[contract.SessionProfileReply](t)
	}
	if w := r.WorkingDirectory; w != nil {
		s.WorkingDirectory = *w
	}
	p, err := readTrustedProfile(q, pid)
	if err != nil {
		return none, err
	}
	s.Account = deref(r.Account)
	if r.Account == nil {
		delete(s.rest, "account")
	}
	s.AgentConfig = jsonBytes(p)
	if err := sessionPut(q, &s); err != nil {
		return none, err
	}
	profileID := p.text("id")
	return contract.SessionProfileReply{SessionId: sid, ProfileId: profileID, Account: r.Account}, nil
}

// teardownPending: a session's teardown commands no host has fetched yet.
func teardownPending(q *Request, sid string) ([]agentCommand, error) {
	return commandsList(q, db.CommandsParams{SessionID: &sid, Kind: ptr("teardown"), Status: ptr("pending")})
}

// lastOnHost: the last of a session's executions that ran on a host, nil when none did.
func lastOnHost(es []agentExecution) *agentExecution {
	var last *agentExecution
	for i := range es {
		if deref(es[i].HostID) != "" {
			last = &es[i]
		}
	}
	return last
}

// sessionArchived: session_archive's answer when no execution ran on a host.
type sessionArchived struct {
	SessionID  string    `json:"sessionId"`
	ArchivedAt int64     `json:"archivedAt"`
	Teardown   *struct{} `json:"teardown"`
}

// sessionArchive: one teardown command on the last execution's host.
func sessionArchive(q *Request, sid string) (any, error) {
	s, err := sessionByID(q, sid)
	if err != nil {
		return nil, err
	}
	es, err := agentExecutions(q, sid)
	if err != nil {
		return nil, err
	}
	for i := range es {
		if !es[i].terminal() && !es[i].unconfirmed() {
			return nil, fail(409, "Execution active")
		}
	}
	last := lastOnHost(es)
	// A teardown still on its way is the answer; a finished one (kept, or sent on delivery) does not stop a new one.
	if old, err := teardownPending(q, sid); err != nil || len(old) > 0 {
		if err != nil {
			return nil, err
		}
		return old[0], nil
	}
	at := nowMs()
	s.ArchivedAt = &at
	if err := sessionPut(q, &s); err != nil {
		return nil, err
	}
	if err := audit(q, "session:"+sid, "archivedAt", nil, at, auditExtra{}); err != nil {
		return nil, err
	}
	if last == nil {
		return sessionArchived{SessionID: sid, ArchivedAt: at}, nil
	}
	return teardownCommand(q, s, *last, false)
}

// sessionDeleteArchive: a deleted session is archived (object_delete, cleanup.go tombstone) and its worktree torn down
// on the host of its last execution, unless it is archived already or a teardown is on its way; torn: one was queued.
func sessionDeleteArchive(q *Request, sid string) (bool, error) {
	s, err := sessionByID(q, sid)
	if err != nil {
		return false, err
	}
	if s.ArchivedAt != nil {
		return false, nil
	}
	if pending, err := teardownPending(q, sid); err != nil || len(pending) > 0 {
		return false, err
	}
	es, err := agentExecutions(q, sid)
	if err != nil {
		return false, err
	}
	last := lastOnHost(es)
	at := nowMs()
	s.ArchivedAt = &at
	if err := sessionPut(q, &s); err != nil {
		return false, err
	}
	if last == nil {
		return false, nil
	}
	// Queued after the stop: the executor keeps a worktree with uncommitted or unpushed work (cleanup.keepWhen).
	t, err := teardownCommand(q, s, *last, false)
	_, torn := t.(agentCommand)
	return err == nil && torn, err
}

// teardownSkipped: no teardown was queued (cleanup skipped or unknown) and why.
type teardownSkipped struct {
	SessionID string `json:"sessionId"`
	Cleanup   string `json:"cleanup"`
	Reason    string `json:"reason"`
}

// teardownPayload: a teardown command's payload: the worktree to remove and how.
type teardownPayload struct {
	WorkspaceKey     json.RawMessage `json:"workspaceKey,omitempty"`
	DeleteBranch     bool            `json:"deleteBranch,omitempty"`
	OutputsStored    bool            `json:"outputsStored,omitempty"`
	Step             bool            `json:"step,omitempty"`
	ProjectID        string          `json:"projectId,omitempty"`
	Project          json.RawMessage `json:"project,omitempty"`
	WorkingDirectory string          `json:"workingDirectory,omitempty"`
	Branch           *bool           `json:"branch,omitempty"`
}

// teardownCommand: the teardown of a session's worktree on the host of its last execution (an agentCommand), or why
// none was queued (teardownSkipped). deleteBranch: the work was delivered (its task accepted), so the local branch goes
// even when squash-merged (git branch -D); the executor still keeps the worktree for any cleanup.keepWhen reason
// (uncommitted, unpushed, teardown-failed).
func teardownCommand(q *Request, s agentSession, last agentExecution, deleteBranch bool) (any, error) {
	return teardownCommandWith(q, s, last, teardownFlags{deleteBranch: deleteBranch})
}

// teardownFlags: how the host treats the worktree (docs/designs/fleet-host-cleanup.md#10-worktree-lifecycle-for-every-run-kind):
// outputsStored lets worktree/output go before keepWhen; step marks a pipeline step session.
type teardownFlags struct {
	deleteBranch, outputsStored, step bool
}

func teardownCommandWith(q *Request, s agentSession, last agentExecution, f teardownFlags) (any, error) {
	deleteBranch := f.deleteBranch
	sid, host := s.ID, deref(last.HostID)
	// A removed host never fetches it: its worktrees went with it, and a pending command would keep purge waiting.
	if live, err := knownHost(q, host); err != nil || !live {
		return teardownSkipped{SessionID: sid, Cleanup: "skipped", Reason: "host-gone"}, err
	}
	var payload teardownPayload
	d := rawObject{}
	if last.Dispatch != nil {
		json.Unmarshal(jsonBytes(last.Dispatch), &d)
	}
	if d.has("workspaceKey") {
		payload.WorkspaceKey = d["workspaceKey"]
	}
	workingDirectory := d.val("workingDirectory")
	payload.DeleteBranch, payload.OutputsStored, payload.Step = deleteBranch, f.outputsStored, f.step
	if p := d.text("projectId"); p != "" {
		workspace := rawObject(last.rest).object("workspaceSnapshot")
		snapshots := rawObject(s.rest).object("workspaceSnapshots")
		if workspace == nil {
			workspace = snapshots.object(host + ":" + p + ":" + d.text("workspaceKey"))
		}
		if workspace == nil {
			workspace = snapshots.object(host)
		}
		if workspace == nil || (workspace.has("projectId") && workspace.val("projectId") != any(p)) {
			return teardownSkipped{SessionID: sid, Cleanup: "unknown", Reason: "cleanup_context_unknown: original workspace settings unavailable"}, nil
		}
		payload.ProjectID, payload.Project = p, json.RawMessage("null")
		if workspace.has("project") {
			payload.Project = workspace["project"]
		}
		workingDirectory = workspace.val("workingDirectory")
	}
	if w, _ := workingDirectory.(string); w != "" {
		payload.WorkingDirectory = w
	}
	if s.Branch != nil && !*s.Branch { // a session started without a branch (presets.go): its worktree was detached
		payload.Branch = s.Branch
	}
	c := agentCommand{ID: newID(), ExecutionID: last.ID, SessionID: sid, HostID: host, ProjectID: firstString(last.ProjectID, s.ProjectID), Kind: "teardown", Payload: jsonBytes(payload), Status: "pending"}
	err := commandPut(q, &c)
	return c, err
}

// deliveredTeardown: an accepted task's sessions get their worktree teardown (fleet-system-management.md, Worktree on
// delivery). A session with a live execution, or a teardown still pending, is left alone; archiving it later sends one.
func deliveredTeardown(q *Request, taskID string) error {
	sids, err := q.db().TaskSessionIDs(q.ctx(), &taskID)
	if err != nil {
		return err
	}
	for _, sid := range sids {
		s, err := sessionByID(q, sid)
		if errors.Is(err, errNotFound) {
			continue
		} else if err != nil {
			return err
		}
		if old, err := teardownPending(q, sid); err != nil || len(old) > 0 {
			if err != nil {
				return err
			}
			continue
		}
		es, err := agentExecutions(q, sid)
		if err != nil {
			return err
		}
		active := false
		for i := range es {
			active = active || (!es[i].terminal() && !es[i].unconfirmed())
		}
		last := lastOnHost(es)
		if active || last == nil {
			continue
		}
		if _, err := teardownCommand(q, s, *last, true); err != nil {
			return err
		}
	}
	return nil
}

// errSessionCreateKeys: session_create takes an empty object.
var errSessionCreateKeys = fail(400, "session_create takes no identity or linkage fields")

func sessionCreate(q *Request) (contract.SessionCreateReply, error) {
	s := agentSession{ID: newID(), CreatedAt: nowMs(), DirectedBy: director(q)}
	if callerReadOnly(q) { // an agent of a read-only conversation creates read-only sessions (presets.go)
		s.Access, s.Branch = "read", ptr(false)
	}
	if err := sessionPut(q, &s); err != nil {
		return contract.SessionCreateReply{}, err
	}
	out, err := asT[contract.SessionCreateReply](s)
	out.SessionId = s.ID
	return out, err
}

// sessionArg: a required sessionId, not empty.
func sessionArg(sid string) (string, error) {
	if sid == "" {
		return "", fail(400, "Expected sessionId")
	}
	return sid, nil
}

func init() {
	route("GET /api/executions/{id}/delegations", func(q *Request) (any, error) {
		id := q.r.PathValue("id")
		if _, err := getExecution(q, id); err != nil {
			return nil, err
		}
		raw, err := q.db().ExecutionDelegations(q.ctx(), id)
		ds, err := delegationsOf(q, raw, err)
		return struct {
			Delegations []rawObject `json:"delegations"`
		}{ds}, err
	})
	op("session_create", func(q *Request, r *contract.SessionCreateRequest) (contract.SessionCreateReply, error) {
		if len(*r) > 0 {
			return contract.SessionCreateReply{}, errSessionCreateKeys
		}
		return sessionCreate(q)
	})
	op("session_get", func(q *Request, r *contract.SessionGetRequest) (contract.SessionGetReply, error) {
		sid, err := sessionArg(r.SessionId)
		if err != nil {
			return contract.SessionGetReply{}, err
		}
		return replyAs[contract.SessionGetReply](sessionGet(q, sid))
	})
	op("session_profile", func(q *Request, r *contract.SessionProfileRequest) (contract.SessionProfileReply, error) {
		sid, err := sessionArg(r.SessionId)
		if err != nil {
			return contract.SessionProfileReply{}, err
		}
		r.SessionId = sid
		return sessionProfile(q, r)
	})
	op("session_archive", func(q *Request, r *contract.SessionArchiveRequest) (contract.SessionArchiveReply, error) {
		sid, err := sessionArg(r.SessionId)
		if err != nil {
			return contract.SessionArchiveReply{}, err
		}
		return replyAs[contract.SessionArchiveReply](sessionArchive(q, sid))
	})
	op("session_unarchive", sessionUnarchive)
	op("sessions_list", func(q *Request, r *contract.SessionsListRequest) (contract.SessionsListReply, error) {
		return sessionsPage(q, r)
	})
	op("list_membership", func(q *Request, b *contract.ListMembershipRequest) (contract.ListMembershipReply, error) {
		return listMembership(q, b)
	})
	// /api/tools: the operation table as MCP tools, each call in its own transaction.
	longRoute("POST /api/tools", func(q *Request) (any, error) { return q.app.mcp(q, "fleet", operations) })
}

// listMembership: which of ids each named list (tasks, sessions) holds, with the list's counts and groups.
func listMembership(q *Request, b *contract.ListMembershipRequest) (contract.ListMembershipReply, error) {
	out := contract.ListMembershipReply{Lists: []contract.MembershipAnswer{}}
	if len(b.Ids) > 1000 {
		return out, fail(400, "Expected at most 1000 ids")
	}
	if len(b.Lists) < 1 || len(b.Lists) > 2 {
		return out, fail(400, "Expected one or two lists")
	}
	// Each filter read as sent: a null projectId item means rows outside projects, which the typed list cannot hold.
	var raw struct {
		Lists []struct {
			Filter json.RawMessage `json:"filter"`
		} `json:"lists"`
	}
	json.Unmarshal(q.body, &raw)
	for i, l := range b.Lists {
		var filter json.RawMessage
		if i < len(raw.Lists) {
			filter = raw.Lists[i].Filter
		}
		a := contract.MembershipAnswer{Name: l.Name, Rows: []contract.ListRow{}}
		switch l.Name {
		case "tasks":
			f, err := taskFilter(filter)
			if err != nil {
				return out, err
			}
			rs, err := taskMembership(q, f, l.Group, l.Sort, b.Ids)
			if err != nil {
				return out, err
			}
			for _, r := range rs {
				a.Rows = append(a.Rows, jsonBytes(r))
			}
			if a.Counts, a.Groups, err = taskSummary(q, f, l.Group, false); err != nil {
				return out, err
			}
		case "sessions":
			f, err := sessionFilter(filter)
			if err != nil {
				return out, err
			}
			o, err := sessionOrderOf(l.Group, l.Sort)
			if err != nil {
				return out, err
			}
			sql, p, err := sqlSessionRows(f, o, b.Ids)
			rs, err := sessionListRows(q, sql, p, err)
			if err != nil {
				return out, err
			}
			for _, r := range rs {
				a.Rows = append(a.Rows, jsonBytes(r))
			}
			if a.Counts, a.Groups, err = sessionSummary(q, f, o); err != nil {
				return out, err
			}
		default:
			return out, fail(400, "Unknown list")
		}
		out.Lists = append(out.Lists, a)
	}
	return out, nil
}

// sessionUnarchive: undo session_archive (owner or full access). The teardown already sent is not undone: a removed
// worktree is prepared again at the next start.
func sessionUnarchive(q *Request, r *contract.SessionUnarchiveRequest) (contract.SessionUnarchiveReply, error) {
	var none contract.SessionUnarchiveReply
	sid, reason := r.SessionId, strings.TrimSpace(r.Reason)
	if reason == "" {
		return none, fail(400, "Expected reason")
	}
	s, err := sessionByID(q, sid)
	if err != nil {
		return none, err
	}
	if s.ArchivedAt == nil {
		return none, fail(409, "Session is not archived")
	}
	at := *s.ArchivedAt
	s.ArchivedAt, s.ID = nil, sid
	if err := sessionPut(q, &s); err != nil {
		return none, err
	}
	// A teardown no host has fetched yet would remove the worktree of a live session again.
	pending, err := teardownPending(q, sid)
	if err != nil {
		return none, err
	}
	for _, x := range pending {
		x.Status = "stopped"
		if err := commandPut(q, &x); err != nil {
			return none, err
		}
	}
	if err := audit(q, "session:"+sid, "archivedAt", at, nil, auditExtra{Reason: reason}); err != nil {
		return none, err
	}
	return contract.SessionUnarchiveReply{SessionId: sid, TeardownsWithdrawn: int64(len(pending))}, nil
}
