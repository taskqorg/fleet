// Agent session tools (docs/agent-tools-inventory.md) and the message submit path: queue behind an
// active execution, steer, handoff, fork, transition, ask/reply, checked worker_done, stop outcome, reconcile, and the
// host-agent binding (/api/hosts/agent-tools) of every call to the caller's own session.
//
// The records this area reads and writes (session, execution, command, message, queued message, handoff, question,
// transition, fork) are Go structs: each is decoded once from its kind's typed queries and encoded once on the way back.
// A record keeps the fields its struct does not name as stored (recordRest), since other areas write them.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// recordRest: the fields of a stored record its struct does not name, kept as stored so a write through put keeps what
// other areas wrote; never read by key. It also keeps a named field whose stored value is zero, null or of another JSON
// type, so a write that leaves that field alone writes it back as it was.
type recordRest map[string]json.RawMessage

// restFields: the JSON names of a struct's exported fields and their indexes, once per type.
func restFields(t reflect.Type) map[string]int {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if m, ok := restFieldCache.Load(t); ok {
		return m.(map[string]int)
	}
	out := map[string]int{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		if !f.IsExported() || name == "-" {
			continue
		}
		if name == "" {
			name = f.Name
		}
		out[name] = i
	}
	restFieldCache.Store(t, out)
	return out
}

var restFieldCache sync.Map // reflect.Type -> map[string]int

// restDecode: a stored record b into v's named fields (a field of another JSON type reads as zero) and rest: the
// stored fields v does not name, and those it read as zero or as an empty list or object (which omitempty would drop
// on encode), as stored.
func restDecode(b []byte, v any, rest *recordRest) error {
	var stored recordRest
	if err := json.Unmarshal(b, &stored); err != nil {
		return err
	}
	named := restFields(reflect.TypeOf(v))
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	*rest = recordRest{}
	for k, raw := range stored {
		i, ok := named[k]
		if !ok {
			(*rest)[k] = raw
			continue
		}
		f := rv.Field(i)
		if err := json.Unmarshal(raw, f.Addr().Interface()); err != nil {
			if te := (*json.UnmarshalTypeError)(nil); !errors.As(err, &te) {
				return err
			}
		}
		if f.IsZero() || restEmpty(f) {
			(*rest)[k] = raw
		}
	}
	return nil
}

// restEmpty: an empty, non-nil slice or map (through pointers).
func restEmpty(v reflect.Value) bool {
	for v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	return (v.Kind() == reflect.Slice || v.Kind() == reflect.Map) && !v.IsNil() && v.Len() == 0
}

// restEncode: v's fields, then the stored fields v does not write.
func restEncode(v any, rest recordRest) ([]byte, error) {
	b, err := json.Marshal(v)
	if err != nil || len(rest) == 0 {
		return b, err
	}
	var out recordRest
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	for k, raw := range rest {
		if _, set := out[k]; !set {
			out[k] = raw
		}
	}
	return json.Marshal(out)
}

// agentView: the fields T names of an open value another area hands over (a project, a profile, a run); a field of
// another JSON type reads as zero.
func agentView[T any](v any) T {
	t, _ := asT[T](v)
	return t
}

// recordReplace: dst holds v's record afterwards (a caller's open record the typed code changed).
func recordReplace(dst rawObject, v any) error {
	var src rawObject
	if err := json.Unmarshal(jsonBytes(v), &src); err != nil {
		return err
	}
	for k := range dst {
		delete(dst, k)
	}
	maps.Copy(dst, src)
	return nil
}

// agentOpt: s, nil when empty.
func agentOpt(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// agentNullText: s as JSON, null when empty.
func agentNullText(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("null")
	}
	return jsonBytes(s)
}

// jsonGiven: the JSON value is present and not null.
func jsonGiven(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

// jsonIsObject: the JSON value is an object.
func jsonIsObject(raw json.RawMessage) bool { return len(raw) > 0 && raw[0] == '{' }

// jsonText: the JSON value as a string; ok false for another type.
func jsonText(raw json.RawMessage) (string, bool) {
	var s string
	if len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &s) != nil {
		return "", false
	}
	return s, true
}

// jsonValue: a JSON value decoded for the helpers that take any (frameHash, canonical, definitionRef).
func jsonValue(raw json.RawMessage) any {
	var v any
	if len(raw) > 0 {
		json.Unmarshal(raw, &v)
	}
	return v
}

func isTrue(b *bool) bool { return b != nil && *b }

// agentProfile: the fields of an agent profile (agentConfig) this area reads.
type agentProfile struct {
	ID      string            `json:"id"`
	Harness string            `json:"harness"`
	Model   string            `json:"model"`
	Effort  json.RawMessage   `json:"effort"`
	Skills  []json.RawMessage `json:"skills"`
}

// profileOf: an agentConfig's fields; ok false when there is no profile object.
func profileOf(raw json.RawMessage) (agentProfile, bool) {
	if !jsonIsObject(raw) {
		return agentProfile{}, false
	}
	var p agentProfile
	json.Unmarshal(raw, &p)
	return p, true
}

// agentNative: a session's native session of one continuation identity (nativeSessions).
type agentNative struct {
	NativeSessionID string `json:"nativeSessionId"`
	Seen            int    `json:"seen"`
	CapturedAt      int64  `json:"capturedAt,omitempty"`
	ExecutionID     string `json:"executionId,omitempty"`
	rest            recordRest
}

func (n *agentNative) UnmarshalJSON(b []byte) error {
	type plain agentNative
	return restDecode(b, (*plain)(n), &n.rest)
}

func (n agentNative) MarshalJSON() ([]byte, error) {
	type plain agentNative
	return restEncode(plain(n), n.rest)
}

// agentFocus: a session's focus, a list of projects or "*" (docs/designs/fleet-pm-agent.md §2).
type agentFocus struct {
	ProjectIDs json.RawMessage `json:"projectIds"`
}

// agentForkOf: the session and execution a fork started from.
type agentForkOf struct {
	SessionID   string  `json:"sessionId"`
	ExecutionID *string `json:"executionId"`
	SourceState *string `json:"sourceState"`
	Strategy    string  `json:"strategy,omitempty"`
	Reason      string  `json:"reason,omitempty"`
}

// agentTransitionArgs: a session_transition request, and a transition queued behind an active execution
// (pendingTransition). projectId and account are kept as sent: present (even null) selects or clears.
type agentTransitionArgs struct {
	SessionID        string          `json:"sessionId,omitempty"`
	ProfileID        string          `json:"profileId,omitempty"`
	ProjectID        json.RawMessage `json:"projectId,omitempty"`
	Account          json.RawMessage `json:"account,omitempty"`
	WorkingDirectory *string         `json:"workingDirectory,omitempty"`
	CallerSessionID  string          `json:"callerSessionId,omitempty"`
}

// agentSession: the session row (session columns + payload).
type agentSession struct {
	ID                      string                 `json:"id"`
	CreatedAt               int64                  `json:"createdAt,omitempty"`
	ArchivedAt              *int64                 `json:"archivedAt,omitempty"`
	ProjectID               string                 `json:"projectId,omitempty"`
	TaskID                  string                 `json:"taskId,omitempty"`
	DeliveryID              string                 `json:"deliveryId,omitempty"`
	HostID                  string                 `json:"hostId,omitempty"`
	Actor                   string                 `json:"actor,omitempty"`
	DirectedBy              string                 `json:"directedBy,omitempty"`
	DirectorAccessRevokedAt *time.Time             `json:"-"`
	CreatedByExecutionID    string                 `json:"createdByExecutionId,omitempty"`
	ParentExecutionID       string                 `json:"parentExecutionId,omitempty"`
	CreatedBySessionID      string                 `json:"createdBySessionId,omitempty"`
	SourceSessionID         string                 `json:"-"`
	PipelineRunID           string                 `json:"pipelineRunId,omitempty"`
	BotID                   string                 `json:"botId,omitempty"`
	AgentConfig             json.RawMessage        `json:"agentConfig,omitempty"`
	StartRequest            string                 `json:"startRequest,omitempty"`
	Role                    string                 `json:"role,omitempty"`
	Preset                  string                 `json:"preset,omitempty"`
	Starter                 string                 `json:"starter,omitempty"`
	Account                 string                 `json:"account,omitempty"`
	Access                  string                 `json:"access,omitempty"`
	Branch                  *bool                  `json:"branch,omitempty"`
	Setup                   string                 `json:"setup,omitempty"`
	WorkingDirectory        string                 `json:"workingDirectory,omitempty"`
	WorkspaceKey            string                 `json:"workspaceKey,omitempty"`
	NativeSessionID         string                 `json:"nativeSessionId,omitempty"`
	NativeSessions          map[string]agentNative `json:"nativeSessions,omitempty"`
	ForkNativeSessionID     string                 `json:"forkNativeSessionId,omitempty"`
	ForkOf                  *agentForkOf           `json:"forkOf,omitempty"`
	PendingHandoffID        string                 `json:"pendingHandoffId,omitempty"`
	PendingTransition       *agentTransitionArgs   `json:"pendingTransition,omitempty"`
	TransitionCleanup       json.RawMessage        `json:"transitionCleanup,omitempty"`
	Focus                   *agentFocus            `json:"focus,omitempty"`
	ReadinessOfSessionID    string                 `json:"readinessOfSessionId,omitempty"`
	rest                    recordRest
}

func (s *agentSession) UnmarshalJSON(b []byte) error {
	type plain agentSession
	return restDecode(b, (*plain)(s), &s.rest)
}

func (s agentSession) MarshalJSON() ([]byte, error) {
	type plain agentSession
	return restEncode(plain(s), s.rest)
}

func (s *agentSession) profile() (agentProfile, bool) { return profileOf(s.AgentConfig) }

// agentDispatch: an execution's dispatch record: where and how it starts (dispatch.go reads it as dispatchSpec).
type agentDispatch struct {
	Harness          string          `json:"harness"`
	ProjectID        *string         `json:"projectId"`
	WorkingDirectory *string         `json:"workingDirectory"`
	WorkspaceKey     *string         `json:"workspaceKey"`
	Account          *string         `json:"account"`
	Access           *string         `json:"access"`
	Branch           *bool           `json:"branch"`
	Host             *string         `json:"host"`
	Last             *string         `json:"last"`
	Input            string          `json:"input"`
	Payload          json.RawMessage `json:"payload"`
	rest             recordRest
}

func (d *agentDispatch) UnmarshalJSON(b []byte) error {
	type plain agentDispatch
	return restDecode(b, (*plain)(d), &d.rest)
}

func (d agentDispatch) MarshalJSON() ([]byte, error) {
	type plain agentDispatch
	if d.Payload == nil {
		d.Payload = json.RawMessage("null")
	}
	return restEncode(plain(d), d.rest)
}

// agentExecution: the execution record (agent_run, host_job; executionPutRaw writes it).
type agentExecution struct {
	ID                    string                 `json:"id"`
	ExecutionID           string                 `json:"executionId,omitempty"`
	SessionID             string                 `json:"sessionId,omitempty"`
	HostID                *string                `json:"hostId"`
	State                 string                 `json:"state,omitempty"`
	Observation           string                 `json:"observation,omitempty"`
	CreatedAt             int64                  `json:"createdAt,omitempty"`
	UpdatedAt             int64                  `json:"updatedAt,omitempty"`
	StartedAt             json.RawMessage        `json:"startedAt,omitempty"`
	FinishedAt            json.RawMessage        `json:"finishedAt,omitempty"`
	Actor                 string                 `json:"actor,omitempty"`
	ProjectID             string                 `json:"projectId,omitempty"`
	TaskID                string                 `json:"taskId,omitempty"`
	DeliveryID            string                 `json:"deliveryId,omitempty"`
	DirectedBy            string                 `json:"directedBy,omitempty"`
	TakenFrom             string                 `json:"takenFrom,omitempty"`
	Model                 string                 `json:"model,omitempty"`
	Effort                json.RawMessage        `json:"effort,omitempty"`
	AgentConfig           json.RawMessage        `json:"agentConfig,omitempty"`
	ProfileSha256         string                 `json:"profileSha256,omitempty"`
	Definitions           json.RawMessage        `json:"definitions,omitempty"`
	ProjectSnapshot       json.RawMessage        `json:"projectSnapshot,omitempty"`
	Dispatch              *agentDispatch         `json:"dispatch,omitempty"`
	Traceparent           string                 `json:"traceparent,omitempty"`
	TraceActor            string                 `json:"traceActor,omitempty"`
	ContinuationKey       string                 `json:"continuationKey,omitempty"`
	ForkOfNativeSessionID string                 `json:"forkOfNativeSessionId,omitempty"`
	HandoffID             string                 `json:"handoffId,omitempty"`
	Report                *contract.WorkerReport `json:"report,omitempty"`
	StopRequested         *bool                  `json:"stopRequested,omitempty"`
	TaskStopRequested     *bool                  `json:"taskStopRequested,omitempty"`
	StopDetail            string                 `json:"stopDetail,omitempty"`
	Error                 string                 `json:"error,omitempty"`
	Problem               string                 `json:"problem,omitempty"`
	BotContext            *botExecutionContext   `json:"botContext,omitempty"`
	rest                  recordRest
}

func (e *agentExecution) UnmarshalJSON(b []byte) error {
	type plain agentExecution
	return restDecode(b, (*plain)(e), &e.rest)
}

func (e agentExecution) MarshalJSON() ([]byte, error) {
	type plain agentExecution
	return restEncode(plain(e), e.rest)
}

func (e *agentExecution) terminal() bool    { return executionEnds(e.State) }
func (e *agentExecution) unconfirmed() bool { return e.Observation == "unconfirmed" }

// agentCommand: a host command row (command columns + payload; commandPut writes it via commands.go).
type agentCommand struct {
	ID          string          `json:"id"`
	ExecutionID string          `json:"executionId,omitempty"`
	SessionID   string          `json:"sessionId,omitempty"`
	HostID      string          `json:"hostId,omitempty"`
	ProjectID   string          `json:"projectId,omitempty"`
	Kind        string          `json:"kind,omitempty"`
	Status      string          `json:"status,omitempty"`
	StopDetail  string          `json:"stopDetail,omitempty"`
	HandedOut   *bool           `json:"handedOut,omitempty"`
	Traceparent *string         `json:"traceparent,omitempty"`
	Run         json.RawMessage `json:"run,omitempty"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	rest        recordRest
}

func (c *agentCommand) UnmarshalJSON(b []byte) error {
	type plain agentCommand
	return restDecode(b, (*plain)(c), &c.rest)
}

func (c agentCommand) MarshalJSON() ([]byte, error) {
	type plain agentCommand
	return restEncode(plain(c), c.rest)
}

// agentMessage: a sent message row (message columns + payload).
type agentMessage struct {
	ID                string          `json:"id"`
	SessionID         string          `json:"sessionId,omitempty"`
	ExecutionID       string          `json:"executionId,omitempty"`
	ProjectID         string          `json:"projectId,omitempty"`
	RequestID         string          `json:"requestId,omitempty"`
	Text              string          `json:"text"`
	SentBy            string          `json:"sentBy,omitempty"`
	Source            string          `json:"source,omitempty"`
	Attachments       json.RawMessage `json:"attachments,omitempty"`
	AttachmentRequest []string        `json:"attachmentRequest,omitempty"`
	rest              recordRest
}

func (m *agentMessage) UnmarshalJSON(b []byte) error {
	type plain agentMessage
	return restDecode(b, (*plain)(m), &m.rest)
}

func (m agentMessage) MarshalJSON() ([]byte, error) {
	type plain agentMessage
	return restEncode(plain(m), m.rest)
}

// agentQueued: a queued message row (queued_message columns + payload): waiting, steering or started.
type agentQueued struct {
	ID                string          `json:"id"`
	SessionID         string          `json:"sessionId,omitempty"`
	ProjectID         string          `json:"projectId,omitempty"`
	TaskID            string          `json:"taskId,omitempty"`
	RequestID         string          `json:"requestId,omitempty"`
	Text              string          `json:"text"`
	State             string          `json:"state,omitempty"`
	CreatedAt         int64           `json:"createdAt,omitempty"`
	ExecutionID       string          `json:"executionId,omitempty"`
	SteerCommandID    string          `json:"steerCommandId,omitempty"`
	SentBy            string          `json:"sentBy,omitempty"`
	Source            string          `json:"source,omitempty"`
	Traceparent       string          `json:"traceparent,omitempty"`
	TraceActor        string          `json:"traceActor,omitempty"`
	Attachments       json.RawMessage `json:"attachments,omitempty"`
	AttachmentRequest []string        `json:"attachmentRequest,omitempty"`
	Error             string          `json:"error,omitempty"`
	rest              recordRest
}

func (m *agentQueued) UnmarshalJSON(b []byte) error {
	type plain agentQueued
	return restDecode(b, (*plain)(m), &m.rest)
}

func (m agentQueued) MarshalJSON() ([]byte, error) {
	type plain agentQueued
	return restEncode(plain(m), m.rest)
}

// agentHandoff: a context handoff row (handoff columns + payload).
type agentHandoff struct {
	ID          string   `json:"id"`
	SessionID   string   `json:"sessionId,omitempty"`
	ProjectID   *string  `json:"projectId,omitempty"`
	Strategy    string   `json:"strategy,omitempty"`
	Source      string   `json:"source,omitempty"`
	ToKey       string   `json:"toKey,omitempty"`
	Turns       []string `json:"turns,omitempty"`
	Text        string   `json:"text,omitempty"`
	State       string   `json:"state,omitempty"`
	CreatedAt   int64    `json:"createdAt,omitempty"`
	ExecutionID string   `json:"executionId,omitempty"`
	rest        recordRest
}

func (h *agentHandoff) UnmarshalJSON(b []byte) error {
	type plain agentHandoff
	return restDecode(b, (*plain)(h), &h.rest)
}

func (h agentHandoff) MarshalJSON() ([]byte, error) {
	type plain agentHandoff
	return restEncode(plain(h), h.rest)
}

// agentQuestionDelivery: where a reply to a question went (a queued message or a started execution).
type agentQuestionDelivery struct {
	QueueID     string `json:"queueId,omitempty"`
	State       string `json:"state,omitempty"`
	ExecutionID string `json:"executionId,omitempty"`
}

// agentQuestion: an agent question record ('question', agentQuestionPut).
type agentQuestion struct {
	ID          string                 `json:"id"`
	ExecutionID string                 `json:"executionId,omitempty"`
	SessionID   string                 `json:"sessionId,omitempty"`
	ProjectID   *string                `json:"projectId,omitempty"`
	TaskID      *string                `json:"taskId,omitempty"`
	RequestID   string                 `json:"requestId,omitempty"`
	Question    string                 `json:"question,omitempty"`
	State       string                 `json:"state,omitempty"`
	CreatedAt   int64                  `json:"createdAt,omitempty"`
	Answer      string                 `json:"answer,omitempty"`
	AnsweredBy  string                 `json:"answeredBy,omitempty"`
	AnsweredAt  int64                  `json:"answeredAt,omitempty"`
	Delivery    *agentQuestionDelivery `json:"delivery,omitempty"`
	rest        recordRest
}

func (x *agentQuestion) UnmarshalJSON(b []byte) error {
	type plain agentQuestion
	return restDecode(b, (*plain)(x), &x.rest)
}

func (x agentQuestion) MarshalJSON() ([]byte, error) {
	type plain agentQuestion
	return restEncode(plain(x), x.rest)
}

// agentTransitionRecord: a session transition record ("session-transition").
type agentTransitionRecord struct {
	ID        string  `json:"id"`
	SessionID string  `json:"sessionId"`
	ProjectID *string `json:"projectId,omitempty"`
	Type      string  `json:"type"`
	FromKey   string  `json:"fromKey"`
	ToKey     string  `json:"toKey"`
	ProfileID string  `json:"profileId"`
	HandoffID *string `json:"handoffId"`
	At        int64   `json:"at"`
}

// agentForkRecord: a session fork record ("session-fork"), also the session_fork reply.
type agentForkRecord struct {
	ID              string  `json:"id"`
	RequestID       string  `json:"requestId"`
	SourceSessionID string  `json:"sourceSessionId"`
	SessionID       string  `json:"sessionId"`
	Strategy        string  `json:"strategy"`
	Reason          string  `json:"reason,omitempty"`
	HandoffID       *string `json:"handoffId"`
	At              int64   `json:"at"`
}

func harnessOfProfile(raw json.RawMessage) string {
	p, _ := profileOf(raw)
	return firstString(p.Harness, "codex")
}

// harnessOf: the harness of a session or execution record another area holds open.
func harnessOf(s any) string {
	return harnessOfProfile(agentView[struct {
		AgentConfig json.RawMessage `json:"agentConfig"`
	}](s).AgentConfig)
}

func (s *agentSession) harness() string { return harnessOfProfile(s.AgentConfig) }

func (s *agentSession) continuationKey() string {
	return s.harness() + ":" + s.Account + ":" + s.ProjectID + ":" + s.WorkingDirectory + ":" + s.WorkspaceKey
}

// dispatchProject: the project an execution was dispatched in.
func (e *agentExecution) dispatchProject() string {
	if e.Dispatch == nil {
		return ""
	}
	return deref(e.Dispatch.ProjectID)
}

// bodyKeys: the keys the request body names. An explicit null is named: the contract types read it as absent, while
// these handlers tell "clear" from "not given".
func bodyKeys(q *Request) map[string]bool {
	var m map[string]json.RawMessage
	json.Unmarshal(q.body, &m)
	out := map[string]bool{}
	for k := range m {
		out[k] = true
	}
	return out
}

// sessionQueues: what session_get adds to a session record (sessionView).
type sessionQueues struct {
	Queued      []map[string]json.RawMessage `json:"queued"`
	Questions   []map[string]json.RawMessage `json:"questions"`
	Handoffs    []map[string]json.RawMessage `json:"handoffs"`
	Transitions []map[string]json.RawMessage `json:"transitions"`
	Forks       []map[string]json.RawMessage `json:"forks"`
}

type queuedMessagePayload struct {
	Traceparent       string          `json:"traceparent,omitempty"`
	TraceActor        string          `json:"traceActor,omitempty"`
	Attachments       json.RawMessage `json:"attachments,omitempty"`
	AttachmentRequest []string        `json:"attachmentRequest,omitempty"`
	Error             string          `json:"error,omitempty"`
}

func queuedOf(r db.QueuedMessage) (agentQueued, error) {
	var m agentQueued
	if len(r.Payload) > 0 {
		if err := restDecode(r.Payload, &m, &m.rest); err != nil {
			return m, err
		}
	}
	m.ID = r.ID
	m.SessionID = r.SessionID
	m.ProjectID = deref(r.ProjectID)
	m.TaskID = deref(r.TaskID)
	m.RequestID = deref(r.RequestID)
	m.Text = deref(r.Text)
	m.State = deref(r.State)
	m.ExecutionID = deref(r.ExecutionID)
	m.SteerCommandID = deref(r.SteerCommandID)
	m.SentBy = deref(r.SentBy)
	m.Source = deref(r.Source)
	m.CreatedAt = int64Ms(r.CreatedAt)
	return m, nil
}

func queuedMessages(q *Request, p db.QueuedMessagesParams) ([]agentQueued, error) {
	rs, err := q.db().QueuedMessages(q.ctx(), p)
	if err != nil {
		return nil, err
	}
	out := make([]agentQueued, 0, len(rs))
	for _, r := range rs {
		m, err := queuedOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

func queuedMessagePut(q *Request, m *agentQueued) error {
	if m.ID == "" {
		return fmt.Errorf("queued-message record without an id")
	}
	if !validTraceparent(m.Traceparent) {
		m.Traceparent, m.TraceActor = originTrace(q), actor(q, "fleet")
	}
	p := queuedMessagePayload{
		Traceparent:       m.Traceparent,
		TraceActor:        m.TraceActor,
		Attachments:       m.Attachments,
		AttachmentRequest: m.AttachmentRequest,
		Error:             m.Error,
	}
	payload, err := restEncode(p, m.rest)
	if err != nil {
		return err
	}
	if err := q.db().QueuedMessagePut(q.ctx(), db.QueuedMessagePutParams{
		ID:             m.ID,
		SessionID:      m.SessionID,
		ProjectID:      strPtr(m.ProjectID),
		TaskID:         strPtr(m.TaskID),
		RequestID:      strPtr(m.RequestID),
		Text:           &m.Text,
		State:          strPtr(m.State),
		ExecutionID:    strPtr(m.ExecutionID),
		SteerCommandID: strPtr(m.SteerCommandID),
		SentBy:         strPtr(m.SentBy),
		Source:         strPtr(m.Source),
		CreatedAt:      intMsPtr(m.CreatedAt),
		Payload:        payload,
	}); err != nil {
		return err
	}
	if q.app != nil {
		afterCommit(q, func() { q.app.wakeDispatch() })
	}
	return nil
}

type handoffPayload struct {
	Strategy    string   `json:"strategy,omitempty"`
	Source      string   `json:"source,omitempty"`
	ToKey       string   `json:"toKey,omitempty"`
	Turns       []string `json:"turns,omitempty"`
	Text        string   `json:"text,omitempty"`
	CreatedAt   int64    `json:"createdAt,omitempty"`
	ExecutionID string   `json:"executionId,omitempty"`
}

func handoffOf(r db.Handoff) (agentHandoff, error) {
	var h agentHandoff
	if len(r.Payload) > 0 {
		if err := restDecode(r.Payload, &h, &h.rest); err != nil {
			return h, err
		}
	}
	h.ID = r.ID
	h.SessionID = r.SessionID
	h.ProjectID = r.ProjectID
	h.State = deref(r.State)
	return h, nil
}

func handoffGet(q *Request, id string) (agentHandoff, error) {
	if id == "" {
		return agentHandoff{}, errNotFound
	}
	r, err := q.db().HandoffByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentHandoff{}, errNotFound
	}
	if err != nil {
		return agentHandoff{}, err
	}
	return handoffOf(r)
}

func handoffMaybe(q *Request, id string) (*agentHandoff, error) {
	h, err := handoffGet(q, id)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &h, nil
}

func handoffsByState(q *Request, state string) ([]agentHandoff, error) {
	rs, err := q.db().HandoffsByState(q.ctx(), state)
	if err != nil {
		return nil, err
	}
	out := make([]agentHandoff, 0, len(rs))
	for _, r := range rs {
		h, err := handoffOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

func handoffPut(q *Request, h *agentHandoff) error {
	if h.ID == "" {
		return fmt.Errorf("handoff record without an id")
	}
	p := handoffPayload{
		Strategy:    h.Strategy,
		Source:      h.Source,
		ToKey:       h.ToKey,
		Turns:       h.Turns,
		Text:        h.Text,
		CreatedAt:   h.CreatedAt,
		ExecutionID: h.ExecutionID,
	}
	payload, err := restEncode(p, h.rest)
	if err != nil {
		return err
	}
	return q.db().HandoffPut(q.ctx(), db.HandoffPutParams{
		ID:        h.ID,
		SessionID: h.SessionID,
		ProjectID: h.ProjectID,
		State:     strPtr(h.State),
		Payload:   payload,
	})
}

type sessionForkPayload struct {
	Strategy  string  `json:"strategy"`
	Reason    string  `json:"reason,omitempty"`
	HandoffID *string `json:"handoffId"`
	At        int64   `json:"at"`
}

func sessionForks(q *Request, sourceSessionID string, requestID *string) ([]agentForkRecord, error) {
	rs, err := q.db().SessionForks(q.ctx(), db.SessionForksParams{SourceSessionID: sourceSessionID, RequestID: requestID})
	if err != nil {
		return nil, err
	}
	out := make([]agentForkRecord, 0, len(rs))
	for _, r := range rs {
		var p sessionForkPayload
		if err := lenientDecode(r.Payload, &p); err != nil {
			return nil, err
		}
		out = append(out, agentForkRecord{
			ID:              r.ID,
			RequestID:       deref(r.RequestID),
			SourceSessionID: r.SourceSessionID,
			SessionID:       r.SessionID,
			Strategy:        p.Strategy,
			Reason:          p.Reason,
			HandoffID:       p.HandoffID,
			At:              p.At,
		})
	}
	return out, nil
}

func sessionForkMaps(q *Request, sourceSessionID string) ([]map[string]json.RawMessage, error) {
	rs, err := q.db().SessionForks(q.ctx(), db.SessionForksParams{SourceSessionID: sourceSessionID})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]json.RawMessage, 0, len(rs))
	for _, r := range rs {
		m, err := recordMap(r.Payload)
		if err != nil {
			return nil, err
		}
		m["id"], m["sessionId"], m["sourceSessionId"] = jsonBytes(r.ID), jsonBytes(r.SessionID), jsonBytes(r.SourceSessionID)
		if r.ProjectID != nil {
			m["projectId"] = jsonBytes(*r.ProjectID)
		}
		if r.RequestID != nil {
			m["requestId"] = jsonBytes(*r.RequestID)
		}
		out = append(out, m)
	}
	return out, nil
}

func sessionForkPut(q *Request, f *agentForkRecord, projectID string) error {
	if f.ID == "" {
		return fmt.Errorf("session-fork record without an id")
	}
	return q.db().SessionForkPut(q.ctx(), db.SessionForkPutParams{
		ID:              f.ID,
		SessionID:       f.SessionID,
		SourceSessionID: f.SourceSessionID,
		RequestID:       strPtr(f.RequestID),
		ProjectID:       agentOpt(projectID),
		Payload:         jsonBytes(sessionForkPayload{Strategy: f.Strategy, Reason: f.Reason, HandoffID: f.HandoffID, At: f.At}),
	})
}

type sessionTransitionPayload struct {
	Type      string  `json:"type"`
	FromKey   string  `json:"fromKey"`
	ToKey     string  `json:"toKey"`
	ProfileID string  `json:"profileId"`
	HandoffID *string `json:"handoffId"`
	At        int64   `json:"at"`
}

func sessionTransitionMaps(q *Request, sessionID string) ([]map[string]json.RawMessage, error) {
	rs, err := q.db().SessionTransitions(q.ctx(), sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]json.RawMessage, 0, len(rs))
	for _, r := range rs {
		m, err := recordMap(r.Payload)
		if err != nil {
			return nil, err
		}
		m["id"], m["sessionId"] = jsonBytes(r.ID), jsonBytes(r.SessionID)
		if r.ProjectID != nil {
			m["projectId"] = jsonBytes(*r.ProjectID)
		}
		out = append(out, m)
	}
	return out, nil
}

func sessionTransitionPut(q *Request, t *agentTransitionRecord) error {
	if t.ID == "" {
		return fmt.Errorf("session-transition record without an id")
	}
	return q.db().SessionTransitionPut(q.ctx(), db.SessionTransitionPutParams{
		ID:        t.ID,
		SessionID: t.SessionID,
		ProjectID: t.ProjectID,
		Payload:   jsonBytes(sessionTransitionPayload{Type: t.Type, FromKey: t.FromKey, ToKey: t.ToKey, ProfileID: t.ProfileID, HandoffID: t.HandoffID, At: t.At}),
	})
}

type agentQuestionData struct {
	Delivery *agentQuestionDelivery `json:"delivery,omitempty"`
}

// agentQuestionFields: the record fields built from question columns (stripped from question.data).
var agentQuestionFields = []string{"taskId", "id", "executionId", "sessionId", "projectId", "requestId", "question", "state", "answer", "answeredBy", "answeredAt", "createdAt"}

func agentQuestionOf(r db.Question) (agentQuestion, error) {
	var x agentQuestion
	if len(r.Data) > 0 {
		if err := restDecode(r.Data, &x, &x.rest); err != nil {
			return x, err
		}
	}
	x.ID = r.ID
	x.ExecutionID = deref(r.ExecutionID)
	x.SessionID = deref(r.SessionID)
	x.ProjectID = r.ProjectID
	x.TaskID = r.TaskID
	x.RequestID = deref(r.RequestID)
	x.Question = r.Text
	x.State = string(r.State)
	x.Answer = deref(r.Answer)
	x.AnsweredBy = deref(r.AnsweredBy)
	x.AnsweredAt = int64Ms(r.AnsweredAt)
	x.CreatedAt = int64Ms(&r.AskedAt)
	return x, nil
}

func agentQuestionRawOf(r db.Question) (map[string]json.RawMessage, error) {
	out, err := recordMap(r.Data)
	if err != nil {
		return nil, err
	}
	out["id"] = jsonBytes(r.ID)
	for k, v := range map[string]*string{
		"executionId": r.ExecutionID,
		"sessionId":   r.SessionID,
		"projectId":   r.ProjectID,
		"taskId":      r.TaskID,
		"requestId":   r.RequestID,
		"answer":      r.Answer,
		"answeredBy":  r.AnsweredBy,
	} {
		if v != nil {
			out[k] = jsonBytes(*v)
		}
	}
	out["question"] = jsonBytes(r.Text)
	out["state"] = jsonBytes(string(r.State))
	if r.AnsweredAt != nil {
		out["answeredAt"] = jsonBytes(int64Ms(r.AnsweredAt))
	}
	out["createdAt"] = jsonBytes(int64Ms(&r.AskedAt))
	return out, nil
}

func agentQuestionGet(q *Request, id string) (agentQuestion, error) {
	if id == "" {
		return agentQuestion{}, errNotFound
	}
	r, err := q.db().AgentQuestionByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return agentQuestion{}, errNotFound
	}
	if err != nil {
		return agentQuestion{}, err
	}
	return agentQuestionOf(r)
}

func agentQuestionsList(q *Request, p db.AgentQuestionsParams) ([]agentQuestion, error) {
	rs, err := q.db().AgentQuestions(q.ctx(), p)
	if err != nil {
		return nil, err
	}
	out := make([]agentQuestion, 0, len(rs))
	for _, r := range rs {
		x, err := agentQuestionOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, nil
}

func agentQuestionMaps(q *Request, p db.AgentQuestionsParams) ([]map[string]json.RawMessage, error) {
	rs, err := q.db().AgentQuestions(q.ctx(), p)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]json.RawMessage, 0, len(rs))
	for _, r := range rs {
		m, err := agentQuestionRawOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// agentQuestionPut writes *qu through question state owners (askQuestionAs / questionTransition / answerQuestion);
// fields without a column live in question.data.
func agentQuestionPut(q *Request, qu *agentQuestion) error {
	if qu.ID == "" {
		return fmt.Errorf("agent question record without an id")
	}
	found, err := q.db().QuestionExists(q.ctx(), qu.ID)
	if err != nil {
		return err
	}
	dataBytes, err := restEncode(agentQuestionData{Delivery: qu.Delivery}, qu.rest)
	if err != nil {
		return err
	}
	var restObj rawObject
	if err := json.Unmarshal(dataBytes, &restObj); err == nil && restObj != nil {
		for _, k := range agentQuestionFields {
			delete(restObj, k)
		}
		dataBytes = jsonBytes(restObj)
	}
	answered := qu.State == "answered"
	if !found {
		askedBy := "agent"
		if qu.ExecutionID != "" {
			askedBy = "execution:" + qu.ExecutionID
		}
		a := questionAsk{
			ID:          qu.ID,
			Kind:        "agent_question",
			Text:        qu.Question,
			TaskID:      deref(qu.TaskID),
			ProjectID:   deref(qu.ProjectID),
			ExecutionID: strPtr(qu.ExecutionID),
			SessionID:   strPtr(qu.SessionID),
			AskedBy:     &askedBy,
			RequestID:   strPtr(qu.RequestID),
			Data:        dataBytes,
			AskedAt:     intMsPtr(qu.CreatedAt),
		}
		if _, err := askQuestionAs(q, a); err != nil {
			return err
		}
		if answered {
			by := firstString(qu.AnsweredBy, "operator")
			_, err := questionTransition(q, qu.ID, questionEvent{To: "answered", Answer: strPtr(qu.Answer), AnsweredBy: &by, AnsweredAt: intMsPtr(qu.AnsweredAt)})
			return err
		}
		return nil
	}
	if answered {
		if _, err := answerQuestion(q, qu.ID, qu.Answer, firstString(qu.AnsweredBy, "operator"), nil); err != nil {
			return err
		}
	}
	return q.db().QuestionRestPut(q.ctx(), db.QuestionRestPutParams{ID: qu.ID, Data: dataBytes, ProjectID: strPtr(deref(qu.ProjectID))})
}

// sessionView: a session's queue, questions, handoffs, transitions and forks (session_get, sessions.go).
func sessionView(q *Request, sid string) (sessionQueues, error) {
	var out sessionQueues
	ms, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &sid})
	if err != nil {
		return out, err
	}
	out.Queued = []map[string]json.RawMessage{}
	for _, m := range ms {
		v, err := queueView(q, m)
		if err != nil {
			return out, err
		}
		out.Queued = append(out.Queued, v)
	}
	if out.Questions, err = agentQuestionMaps(q, db.AgentQuestionsParams{SessionID: &sid}); err != nil {
		return out, err
	}
	if out.Handoffs, err = sessionHandoffs(q, sid, false); err != nil {
		return out, err
	}
	if out.Transitions, err = sessionTransitionMaps(q, sid); err != nil {
		return out, err
	}
	out.Forks, err = sessionForkMaps(q, sid)
	return out, err
}

// sortedJSON: stored JSON with object keys in the order a decoded record writes them; SQL NULL is null.
func sortedJSON(b []byte) (json.RawMessage, error) {
	if b == nil {
		return json.RawMessage("null"), nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// The session tools decode into their request types; where an explicit null (clear the project, the account) and an
// absent key mean different things, the handler also reads which keys the body names (bodyKeys).
func init() {
	op("session_start", sessionStart)
	op("session_task_attach", sessionAttach)
	op("project_readiness", projectReadiness)
	op("session_transition", func(q *Request, r *contract.SessionTransitionRequest) (contract.SessionTransitionReply, error) {
		given := bodyKeys(q)
		args := agentTransitionArgs{SessionID: deref(r.SessionId), ProfileID: deref(r.ProfileId), CallerSessionID: deref(r.CallerSessionId)}
		if given["projectId"] {
			args.ProjectID = jsonBytes(r.ProjectId)
		}
		if given["account"] {
			args.Account = jsonBytes(r.Account)
		}
		return agentTransition(q, args)
	})
	op("session_fork", func(q *Request, r *contract.SessionForkRequest) (contract.SessionForkReply, error) {
		f, err := sessionFork(q, r)
		if err != nil {
			return contract.SessionForkReply{}, err
		}
		return asT[contract.SessionForkReply](f)
	})
	op("session_resume", func(q *Request, r *contract.SessionResumeRequest) (contract.SessionResumeReply, error) {
		return sessionResume(q, deref(r.SessionId))
	})
	op("session_handoffs", func(q *Request, b *contract.SessionHandoffsRequest) (contract.SessionHandoffsReply, error) {
		if b.SessionId == "" {
			return contract.SessionHandoffsReply{}, fail(400, "Expected sessionId")
		}
		if _, err := sessionByID(q, b.SessionId); err != nil {
			return contract.SessionHandoffsReply{}, err
		}
		hs, err := sessionHandoffs(q, b.SessionId, true)
		return contract.SessionHandoffsReply{Handoffs: hs}, err
	})
	op("message_send", func(q *Request, r *contract.MessageSendRequest) (contract.MessageSendReply, error) {
		if len([]rune(deref(r.RequestId))) > 200 || len([]rune(deref(r.Text))) > 32000 {
			return nil, fail(400, "Invalid text")
		}
		out, err := agentSubmit(q, r, "")
		if err != nil {
			return nil, err
		}
		return out.reply(), nil
	})
	op("queue_list", func(q *Request, b *contract.QueueListRequest) (contract.QueueListReply, error) {
		ms, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: agentOpt(deref(b.SessionId))})
		out := []map[string]json.RawMessage{}
		for _, m := range ms {
			v, err := queueView(q, m)
			if err != nil {
				return contract.QueueListReply{}, err
			}
			out = append(out, v)
		}
		return contract.QueueListReply{Queued: out}, err
	})
	op("ask", agentAsk)
	op("reply", agentReplyTo)
	op("questions", func(q *Request, b *contract.QuestionsRequest) (contract.QuestionsReply, error) {
		qs, err := agentQuestionMaps(q, db.AgentQuestionsParams{SessionID: agentOpt(deref(b.SessionId)), State: agentOpt(deref(b.State))})
		return contract.QuestionsReply{Questions: qs}, err
	})
	op("worker_done", workerDone)
	op("execution_status", executionStatus)
	op("focus_set", focusSet)
	op("reconcile", func(q *Request, _ *contract.ReconcileRequest) (contract.ReconcileReply, error) {
		return agentReconcile(q)
	})
}
