package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/errtrack"

	"github.com/jackc/pgx/v5"
)

// Records and the execution model (docs/designs/fleet-data-foundation.md §1, §7). get/put/all read and write a record
// kind inside the request's transaction (q.tx): 'execution' and 'delegation' live on the execution table, 'question' on the
// question table, every other kind is a document.

func isNotFound(err error) bool { var he *HTTPError; return errors.As(err, &he) && he.Code == 404 }

// nonNil: v unless nil, else alt (JavaScript ??).
func nonNil(v ...any) any {
	for _, x := range v {
		if x != nil {
			return x
		}
	}
	return nil
}

// executionLabContext inherits the trial label, never the executor profile. Cross-project work keeps its own scope.
func executionLabContext(q *Request, parent, project string, experiment, variant any) (any, any, error) {
	if parent != "" {
		r, err := q.db().DispExecLab(q.ctx(), db.DispExecLabParams{ID: parent, ProjectID: strPtr(project)})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, err
		}
		if r.ExperimentID != nil {
			return *r.ExperimentID, r.Variant, nil
		}
	}
	return experiment, variant, nil
}

// executionRow: the insert of an execution record's fields (id, kind, state, observation, projectId, taskId,
// experimentId, variant, parentExecutionId, sessionId, accountId, hostId, harness, model, configRef, startedAt, endedAt
// as a time or milliseconds, data, directedBy, profileId, profileRevision, pipelineId, pipelineRevision, stepId,
// requestId, systemActor, definitions); JSON fields are encoded, nil is NULL.
func executionRow(v executionNew) db.ExecutionInsertParams {
	r := db.ExecutionInsertParams{ID: v.ID, Kind: db.ExecutionKind(v.Kind), State: db.ExecutionState(v.State),
		ProjectID: strPtr(v.ProjectID), TaskID: strPtr(v.TaskID), BotID: strPtr(v.BotID), ExperimentID: strPtr(v.ExperimentID), Variant: strPtr(v.Variant),
		ParentExecutionID: strPtr(v.ParentExecutionID), SessionID: strPtr(v.SessionID), AccountID: strPtr(v.AccountID), HostID: strPtr(v.HostID),
		Harness: strPtr(v.Harness), Model: strPtr(v.Model), StartedAt: msTimeOf(v.StartedAt), EndedAt: msTimeOf(v.EndedAt),
		DirectedBy: strPtr(v.DirectedBy), ProfileID: strPtr(v.ProfileID), ProfileRevision: v.ProfileRevision, PipelineID: strPtr(v.PipelineID),
		PipelineRevision: v.PipelineRevision, StepID: strPtr(v.StepID), RequestID: strPtr(v.RequestID), SystemActor: strPtr(v.SystemActor)}
	if v.Observation != "" {
		obs := db.Observation(v.Observation)
		r.Observation = &obs
	}
	if jsonGiven(v.ConfigRef) {
		r.ConfigRef = v.ConfigRef
	}
	if jsonGiven(v.Data) {
		r.Data = v.Data
	}
	if jsonGiven(v.Definitions) {
		r.Definitions = v.Definitions
	}
	return r
}

// executionNew: a new execution's fields as insertExecution (store.go) reads them from its caller's record.
type executionNew struct {
	ID                string          `json:"id"`
	Kind              string          `json:"kind"`
	State             string          `json:"state"`
	Observation       string          `json:"observation"`
	ProjectID         string          `json:"projectId"`
	TaskID            string          `json:"taskId"`
	BotID             string          `json:"botId"`
	ExperimentID      string          `json:"experimentId"`
	Variant           string          `json:"variant"`
	ParentExecutionID string          `json:"parentExecutionId"`
	PipelineStepID    string          `json:"pipelineStepId"`
	PipelineRunID     string          `json:"pipelineRunId"`
	SessionID         string          `json:"sessionId"`
	AccountID         string          `json:"accountId"`
	HostID            string          `json:"hostId"`
	Harness           string          `json:"harness"`
	Model             string          `json:"model"`
	StartedAt         *int64          `json:"startedAt"`
	EndedAt           *int64          `json:"endedAt"`
	DirectedBy        string          `json:"directedBy"`
	ProfileID         string          `json:"profileId"`
	ProfileRevision   *int64          `json:"profileRevision"`
	PipelineID        string          `json:"pipelineId"`
	PipelineRevision  *int64          `json:"pipelineRevision"`
	StepID            string          `json:"stepId"`
	RequestID         string          `json:"requestId"`
	SystemActor       string          `json:"systemActor"`
	ConfigRef         json.RawMessage `json:"configRef"`
	Data              json.RawMessage `json:"data"`
	Definitions       json.RawMessage `json:"definitions"`
}

// msTimeOf: milliseconds since the epoch as a nullable timestamp.
func msTimeOf(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}

func nullInt(v any) *int64 {
	if p, ok := v.(*int64); ok {
		return p
	}
	if n, ok := intOK(v); ok {
		return &n
	}
	return nil
}

// nullTime: a time, or milliseconds since the epoch, as a nullable timestamp.
func nullTime(v any) *time.Time {
	if t, ok := executionTime(v).(time.Time); ok {
		return &t
	}
	return nil
}

// markObservation: the execution's observation; a confirmed one is a contact now. id is the record's id (a string).
func markObservation(q *Request, id any, observation string) error {
	sid, _ := id.(string)
	taskID, err := q.db().DispMarkObservation(q.ctx(), db.DispMarkObservationParams{ID: sid, Column2: db.Observation(observation)})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err == nil && taskID != "" {
		_, err = settleIdleTask(q, taskID)
	}
	if err == nil {
		err = settleBotRun(q, sid)
	}
	return err
}

func setHarnessVersion(q *Request, id any, version string) error {
	sid, _ := id.(string)
	return q.db().DispHarnessVersion(q.ctx(), db.DispHarnessVersionParams{ID: sid, Version: version})
}

// childThread: one child thread a harness reports.
type childThread struct {
	ThreadID       *string         `json:"threadId"`
	ParentThreadID json.RawMessage `json:"parentThreadId"`
	Tool           json.RawMessage `json:"tool"`
	Binding        json.RawMessage `json:"binding"`
}

// childData: a child agent row's data before its trace fields.
type childData struct {
	ThreadID       string          `json:"threadId"`
	ParentThreadID json.RawMessage `json:"parentThreadId"`
	Tool           json.RawMessage `json:"tool"`
}

// recordChildren: one child_agent row per child thread a harness reports, under the parent.
func recordChildren(q *Request, parentID string, children []any) error {
	p, err := q.db().DispExecParent(q.ctx(), parentID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	experiment, variant, err := executionLabContext(q, parentID, deref(p.ProjectID), nil, nil)
	if err != nil {
		return err
	}
	for _, x := range children {
		c, err := asT[childThread](x)
		if err != nil || c.ThreadID == nil {
			continue
		}
		thread := *c.ThreadID
		var binding struct {
			Model any `json:"model"`
		}
		json.Unmarshal(c.Binding, &binding)
		model, _ := binding.Model.(string)
		data, err := executionTraceData(q, parentID, jsonBytes(childData{ThreadID: thread, ParentThreadID: c.ParentThreadID, Tool: c.Tool}))
		if err != nil {
			return err
		}
		row := db.ExecutionInsertParams{ID: parentID + ":child:" + thread, Kind: db.ExecutionKindChildAgent, State: db.ExecutionStateRunning,
			ProjectID: strPtr(p.ProjectID), TaskID: strPtr(p.TaskID), BotID: strPtr(p.BotID), ExperimentID: strPtr(experiment), Variant: strPtr(variant), ParentExecutionID: &parentID,
			SessionID: strPtr(p.SessionID), HostID: strPtr(p.HostID), Harness: strPtr(p.Harness), Model: strPtr(model), DirectedBy: strPtr(p.DirectedBy),
			PipelineID: strPtr(p.PipelineID), PipelineRevision: p.PipelineRevision, StepID: strPtr(p.StepID), ProfileID: strPtr(p.ProfileID),
			ProfileRevision: p.ProfileRevision, Data: jsonBytes(data)}
		if len(c.Binding) > 0 && string(c.Binding) != "null" {
			row.ConfigRef = c.Binding
		}
		if _, err := executionInsert(q, row); err != nil {
			return err
		}
	}
	return nil
}

func executionEnded(q *Request, id any) (bool, error) {
	sid, _ := id.(string)
	return q.db().DispExecEnded(q.ctx(), sid)
}

// detectProblems refreshes registration problems on open executions without replacing retained runtime causes.
func detectProblems(q *Request, hosts []string, environments map[string][]string) error {
	return q.db().DispDetectProblems(q.ctx(), db.DispDetectProblemsParams{Hosts: hosts, Environments: jsonBytes(environments)})
}

func openDelegations(q *Request) ([]delegationRec, error) {
	bs, err := q.db().DispOpenDelegations(q.ctx())
	if err != nil {
		return nil, err
	}
	out := []delegationRec{}
	for _, b := range bs {
		var d delegationRec
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func strs(v any) []string {
	xs, _ := v.([]any)
	out := []string{}
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func inList(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func nilIf(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// definitionScalars extracts typed agent profile and pipeline definition identifiers for indexed columns; profile is the
// profile id when the definitions name none.
func definitionScalars(defs any, profile string) (profileID, profileRev, pipelineID, pipelineRev any) {
	var items []rawObject
	if b := jsonBytes(defs); len(b) > 0 {
		_ = json.Unmarshal(b, &items)
	}
	for _, d := range items {
		id := d.text("id")
		if id == "" {
			continue
		}
		rev, hasRev := intOK(d.val("revision"))
		switch d.text("kind") {
		case "agent":
			if profileID == nil {
				profileID = id
				if hasRev {
					profileRev = rev
				}
			}
		case "pipeline":
			if pipelineID == nil {
				pipelineID = id
				if hasRev {
					pipelineRev = rev
				}
			}
		}
	}
	if profileID == nil && profile != "" {
		profileID = profile
	}
	return profileID, profileRev, pipelineID, pipelineRev
}

// executionConfig: an execution's config_ref (its keys sorted, as stored).
type executionConfig struct {
	Effort        json.RawMessage `json:"effort"`
	ProfileID     *string         `json:"profileId"`
	ProfileSha256 json.RawMessage `json:"profileSha256"`
}

func executionRawOf(r db.Execution) (rawObject, error) {
	out, err := recordRaw(r.Data)
	if err != nil {
		return nil, err
	}
	// Routing uses the execution columns even when the retained payload omits or predates them.
	out["id"] = jsonBytes(r.ID)
	out["sessionId"] = jsonBytes(r.SessionID)
	out["projectId"] = jsonBytes(r.ProjectID)
	out["hostId"] = jsonBytes(r.HostID)
	out["state"] = jsonBytes(string(r.State))
	out["observation"] = jsonBytes(string(r.Observation))
	if r.LastSeenAt != nil {
		out["lastSeenAt"] = jsonBytes(r.LastSeenAt.UTC())
	} else {
		out["lastSeenAt"] = json.RawMessage("null")
	}
	return out, nil
}

func executionAs[T any](r db.Execution) (T, error) {
	var out T
	raw, err := executionRawOf(r)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(jsonBytes(raw), &out)
	return out, err
}

func executionGetAs[T any](q *Request, id string) (T, error) {
	var none T
	if id == "" {
		return none, errNotFound
	}
	r, err := q.db().ExecutionByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return none, errNotFound
	}
	if err != nil {
		return none, err
	}
	return executionAs[T](r)
}

func executionMaybeAs[T any](q *Request, id string) (*T, error) {
	v, err := executionGetAs[T](q, id)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func executionsAs[T any](q *Request, sessionID *string) ([]T, error) {
	rs, err := q.db().Executions(q.ctx(), sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]T, 0, len(rs))
	for _, r := range rs {
		v, err := executionAs[T](r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func getExecution(q *Request, id string) (agentExecution, error) {
	return executionGetAs[agentExecution](q, id)
}

func executionMaybe(q *Request, id string) (*agentExecution, error) {
	return executionMaybeAs[agentExecution](q, id)
}

func executionRawGet(q *Request, id string) (rawObject, error) {
	if id == "" {
		return nil, errNotFound
	}
	r, err := q.db().ExecutionByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	return executionRawOf(r)
}

func executionRawMaybe(q *Request, id string) (rawObject, error) {
	out, err := executionRawGet(q, id)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return out, err
}

func executionsRawBySession(q *Request, sid string) ([]rawObject, error) {
	rs, err := q.db().Executions(q.ctx(), &sid)
	if err != nil {
		return nil, err
	}
	out := make([]rawObject, 0, len(rs))
	for _, r := range rs {
		v, err := executionRawOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// executionPut writes *v as an execution record (agent_run or host_job) and reads back what the write completed
// (a trace) into it.
func executionPut[T any](q *Request, v *T) error {
	var r rawObject
	if err := json.Unmarshal(jsonBytes(*v), &r); err != nil || r == nil {
		return fmt.Errorf("execution record is not an object")
	}
	if err := executionPutRaw(q, r); err != nil {
		return err
	}
	return json.Unmarshal(jsonBytes(r), v)
}

// executionWrite writes v as an execution record without reading back completed fields.
func executionWrite(q *Request, v any) error {
	var r rawObject
	if err := json.Unmarshal(jsonBytes(v), &r); err != nil || r == nil {
		return fmt.Errorf("execution record is not an object")
	}
	return executionPutRaw(q, r)
}

// executionBotAttr resolves bot attribution from an ancestor execution or originating session.
func executionBotAttr(q *Request, parentID, sessionID string) (string, error) {
	if parentID == "" && sessionID == "" {
		return "", nil
	}
	return unscoped(q, func() (string, error) {
		if parentID != "" {
			id, err := q.db().ExecutionBotID(q.ctx(), parentID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
			if id != "" {
				return id, nil
			}
		}
		if sessionID != "" {
			id, err := q.db().SessionBotID(q.ctx(), sessionID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
			if id != "" {
				return id, nil
			}
			id, err = q.db().SessionLatestExecutionBotID(q.ctx(), &sessionID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return "", err
			}
			if id != "" {
				return id, nil
			}
		}
		return "", nil
	})
}

func resolveExecutionBotID(q *Request, e rawObject, sess *agentSession, handoff *agentHandoff, parent string) (string, error) {
	if id := firstString(e.text("botId"), e.object("botContext").text("botId"), sess.BotID); id != "" {
		return id, nil
	}
	if id, err := executionBotAttr(q, parent, sess.ID); err != nil || id != "" {
		return id, err
	}
	if handoff != nil && handoff.ExecutionID != "" {
		if id, err := executionBotAttr(q, handoff.ExecutionID, ""); err != nil || id != "" {
			return id, err
		}
	}
	if sess.ForkOf != nil {
		if id, err := executionBotAttr(q, deref(sess.ForkOf.ExecutionID), sess.ForkOf.SessionID); err != nil || id != "" {
			return id, err
		}
	}
	return executionBotAttr(q, sess.CreatedByExecutionID, sess.CreatedBySessionID)
}

// executionPutRaw: the one writer of an execution record (kind agent_run or host_job). state is the typed execution state
// (executionTransitions, moved by executionTransition); observation stays 'unconfirmed' only while the state does not move.
// kind is fixed by the first write. Both live in their columns only, then the record's task settles.
func executionPutRaw(q *Request, e rawObject) error {
	id := e.text("id")
	previous, err := q.db().ExecutionStateOf(q.ctx(), id)
	if err != nil {
		return err
	}
	prev := previous.State
	state := firstString(e.text("state"), prev, "preparing")
	if _, ok := executionTransitions[state]; !ok {
		return fmt.Errorf("execution %s: unknown state %q", id, state)
	}
	observation := "confirmed" // a writer that moves the state knows it
	if e.text("observation") == "unconfirmed" && state == prev {
		observation = "unconfirmed"
	}
	kind := "agent_run"
	if e.text("actor") == "program-worker" {
		kind = "host_job"
	}
	sp, err := sessionMaybe(q, e.text("sessionId"))
	if err != nil {
		return err
	}
	var sess agentSession
	if sp != nil {
		sess = *sp
	}
	handoff, err := handoffMaybe(q, e.text("handoffId"))
	if err != nil {
		return err
	}
	parent := firstString(e.text("parentExecutionId"), e.text("continuationOf"), e.text("pipelineStepId"), e.text("pipelineRunId"))
	if parent == "" && handoff != nil && len(handoff.Turns) > 0 {
		parent = handoff.Turns[len(handoff.Turns)-1]
	}
	if parent == "" {
		parent = sess.CreatedByExecutionID
	}
	if parent == "" && sess.ForkOf != nil && deref(sess.ForkOf.ExecutionID) != "" {
		other, err := q.db().ExecutionSessionHasOtherRun(q.ctx(), db.ExecutionSessionHasOtherRunParams{SessionID: ptr(sess.ID), ID: id})
		if err != nil {
			return err
		}
		if !other {
			parent = deref(sess.ForkOf.ExecutionID)
		}
	}
	if prev == "" && !validTraceparent(e.text("traceparent")) {
		tp, err := executionOrigin(q, parent)
		if err != nil {
			return err
		}
		e["traceparent"], e["traceActor"] = jsonBytes(tp), jsonBytes(actor(q, "fleet"))
	}
	project := firstString(e.text("projectId"), sess.ProjectID)
	task := firstString(e.text("deliveryId"), sess.DeliveryID, sess.TaskID)
	profile := e.object("agentConfig").text("id")
	harness := ""
	if kind == "agent_run" {
		harness = harnessOfProfile(e["agentConfig"])
	}
	experiment, variant, err := executionLabContext(q, parent, project, e.val("experimentId"), e.val("variant"))
	if err != nil {
		return err
	}
	account := firstString(e.object("effectiveAccount").text("accountId"), e.text("accountId"))
	cfg := jsonBytes(executionConfig{Effort: e["effort"], ProfileID: strPtr(profile), ProfileSha256: e["profileSha256"]})
	profileID, profileRev, pipelineID, pipelineRev := definitionScalars(e.val("definitions"), profile)
	stepID := nilIf(e.text("stepId"))
	if stepRowID := firstString(e.text("pipelineStepId"), e.text("pipelineRunId")); stepRowID != "" && (pipelineID == nil || stepID == nil) {
		p, err := q.db().ExecutionPipelineRef(q.ctx(), stepRowID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		pPipe, pRev, pStep := p.PipelineID, p.PipelineRevision, p.StepID
		if pipelineID == nil && pPipe != nil {
			pipelineID = *pPipe
			if pRev != nil {
				pipelineRev = *pRev
			}
		}
		if stepID == nil && pStep != nil {
			stepID = *pStep
		}
	}
	data := rawObject{}
	for k, v := range e {
		if k != "state" && k != "observation" {
			data[k] = v
		}
	}
	directed := firstString(e.text("directedBy"), sess.DirectedBy)
	botID, err := resolveExecutionBotID(q, e, &sess, handoff, parent)
	if err != nil {
		return err
	}
	inserted := false
	if prev == "" {
		obs := db.Observation(observation)
		inserted, err = executionInsert(q, db.ExecutionInsertParams{ID: id, Kind: db.ExecutionKind(kind), State: db.ExecutionState(state), Observation: &obs,
			ProjectID: strPtr(project), TaskID: strPtr(task), BotID: strPtr(botID), ExperimentID: strPtr(experiment), Variant: strPtr(variant), ParentExecutionID: strPtr(parent),
			SessionID: strPtr(e.val("sessionId")), AccountID: strPtr(account), HostID: strPtr(e.val("hostId")), Harness: strPtr(harness), Model: strPtr(e.val("model")),
			ConfigRef: cfg, StartedAt: nullTime(e.val("startedAt")), EndedAt: nullTime(e.val("finishedAt")), Data: jsonBytes(data), DirectedBy: strPtr(directed),
			ProfileID: strPtr(profileID), ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev), StepID: strPtr(stepID)})
		if err != nil {
			return err
		}
	}
	if !inserted {
		if err := q.db().ExecutionUpdate(q.ctx(), db.ExecutionUpdateParams{ID: id, Observation: db.Observation(observation), ProjectID: strPtr(project),
			TaskID: strPtr(task), BotID: strPtr(botID), ExperimentID: strPtr(experiment), Variant: strPtr(variant), ParentExecutionID: strPtr(parent), SessionID: e.ptr("sessionId"),
			AccountID: strPtr(account), HostID: e.ptr("hostId"), Harness: strPtr(harness), Model: e.ptr("model"), ConfigRef: cfg,
			StartedAt: nullTime(e.val("startedAt")), EndedAt: nullTime(e.val("finishedAt")), Data: jsonBytes(data), DirectedBy: strPtr(directed), ProfileID: strPtr(profileID),
			ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev), StepID: strPtr(stepID)}); err != nil {
			return err
		}
	}
	if e.has("definitions") {
		if err := q.db().ExecutionDefinitions(q.ctx(), db.ExecutionDefinitionsParams{ID: id, Definitions: e["definitions"], ProfileID: strPtr(profileID),
			ProfileRevision: nullInt(profileRev), PipelineID: strPtr(pipelineID), PipelineRevision: nullInt(pipelineRev)}); err != nil {
			return err
		}
	}
	if d := e.text("directedBy"); d != "" {
		if err := q.db().ExecutionDirectedBy(q.ctx(), db.ExecutionDirectedByParams{ID: id, DirectedBy: d}); err != nil {
			return err
		}
	}
	if !inserted {
		if state != prev {
			if _, err := executionTransition(q, id, executionEvent{To: state, KeepEnd: true}); err != nil {
				return err
			}
		}
	} else if executionEnds(state) {
		if err := writeTotals(q, id); err != nil {
			return err
		}
	}
	if !inserted {
		if err := settleBotRun(q, id); err != nil {
			return err
		}
	}
	t, err := q.db().ExecutionTaskOf(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		t, err = nil, nil
	}
	if err != nil {
		return err
	}
	if t != nil {
		if prev == "" || executionEnds(state) && (state != prev || observation != previous.Observation) {
			if q.tx != nil {
				if serr := exec(q, "SAVEPOINT settle_idle_task"); serr != nil {
					return serr
				}
				if _, serr := settleIdleTask(q, *t); serr != nil {
					if pgCode(serr) != "" {
						return serr
					}
					if rerr := exec(q, "ROLLBACK TO SAVEPOINT settle_idle_task"); rerr != nil {
						return rerr
					}
					if rerr := exec(q, "RELEASE SAVEPOINT settle_idle_task"); rerr != nil {
						return rerr
					}
					log.Printf("execution %s settleIdleTask(%s): %v", id, *t, serr)
					capErr, taskID, execID := serr, *t, id
					afterCommit(q, func() {
						errtrack.Capture(capErr, map[string]string{"task": taskID, "execution": execID})
					})
				} else if rerr := exec(q, "RELEASE SAVEPOINT settle_idle_task"); rerr != nil {
					return rerr
				}
			} else {
				_, err = settleIdleTask(q, *t)
			}
		} else {
			_, err = settleTask(q, *t, "")
		}
	}
	if err == nil && q.app != nil {
		needDispatch := state == "preparing" && !e.has("hostId") && kind == "agent_run"
		newlySucceeded := state == "succeeded" && !executionEnds(prev)
		if needDispatch || newlySucceeded {
			afterCommit(q, func() {
				q.app.wakeDispatch()
				if newlySucceeded {
					q.app.wakeAllHosts()
				}
			})
		}
	}
	return err
}

// lockExecutionForTotals establishes parent-before-totals row order for every totals writer.
// Execution ownership is authoritative, including a projectless NULL owner.
func lockExecutionForTotals(q *Request, id string) (string, error) {
	kind, err := q.db().ExecutionLockKind(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return kind, err
}

// writeConfirmedUsage projects accepted native usage without inventing completion or process resources.
func writeConfirmedUsage(q *Request, id string) error {
	kind, err := lockExecutionForTotals(q, id)
	if err != nil || kind == "" || kind == "pipeline_run" || kind == "pipeline_step" {
		return err
	}
	if err := q.db().TotalsConfirmedUsage(q.ctx(), id); err != nil {
		return err
	}
	return writeUsage(q, id)
}

// writeUsage: the model, tokens and native observations the harness reported, into the execution's totals row.
func writeUsage(q *Request, id string) error {
	if err := q.db().TotalsModel(q.ctx(), id); err != nil {
		return err
	}
	if err := q.db().TotalsTokens(q.ctx(), id); err != nil {
		return err
	}
	return q.db().TotalsNativeObservations(q.ctx(), id)
}

// writeTotals persists and merges leaf-execution totals so receipt and process.closed converge regardless of arrival order.
func writeTotals(q *Request, id string) error {
	kind, err := lockExecutionForTotals(q, id)
	if err != nil || kind == "" || kind == "pipeline_run" || kind == "pipeline_step" {
		return err
	}
	if err := q.db().TotalsWrite(q.ctx(), id); err != nil {
		return err
	}
	if err := writeUsage(q, id); err != nil {
		return err
	}
	if err := q.db().TotalsRateLimit(q.ctx(), id); err != nil {
		return err
	}
	return q.db().TotalsProcessClosed(q.ctx(), db.TotalsProcessClosedParams{ID: id, Kind: kind})
}

// writeEnded: the end of an agent run in its thread, one event with a fixed id.
func writeEnded(q *Request, id string) error {
	return q.db().ExecutionEnded(q.ctx(), id)
}

// Execution seams: execution_stop, budget-blocked connections, the budget answer hook, the artifact gate on
// a completed start, and settling an execution that waits for its artifacts.

func init() {
	op("execution_stop", func(q *Request, r *contract.ExecutionStopRequest) (contract.ExecutionStopReply, error) {
		return executionStop(q, r.ExecutionId)
	})
}

// lockExecutionTask keeps execution writers in task-before-execution order.
func lockExecutionTask(q *Request, id string) error {
	return q.db().DispLockExecutionTask(q.ctx(), id)
}

// executionCommands: the command records of an execution, optionally of one kind and one status, in order.
func executionCommands(q *Request, id string, kind, status *string) ([]agentCommand, error) {
	return commandsList(q, db.CommandsParams{ExecutionID: &id, Kind: kind, Status: status})
}

// executionArtifactError: the error of an execution its artifact gate fails.
type executionArtifactError struct {
	Code    string    `json:"code"`
	Message string    `json:"message"`
	Missing any       `json:"missing"`
	Failed  any       `json:"failed"`
	Pending *[]string `json:"pending,omitempty"` // only a settled wait names what was still pending
}

// executionStop: one stop command; an execution no host can confirm is settled here.
func executionStop(q *Request, id string) (contract.ExecutionStopReply, error) {
	return requestExecutionStop(q, id, false)
}

func requestExecutionStop(q *Request, id string, taskCancellation bool) (contract.ExecutionStopReply, error) {
	var rep contract.ExecutionStopReply
	if err := lockExecutionTask(q, id); err != nil {
		return rep, err
	}
	// The execution row, locked: the dispatch claim locks it too (claimRow), so a claim and a stop never interleave
	// between read and write, whatever advisory locks either holds. Unlocked, a claim committed between this read and the
	// write below was overwritten (cancelled, yet preparing on a host) or overwrote the stop.
	if err := q.db().DispExecLock(q.ctx(), id); err != nil {
		return rep, err
	}
	e, err := getExecution(q, id)
	if err != nil {
		return rep, err
	}
	stopKind, pendingStatus := "stop", "pending"
	stops, err := executionCommands(q, id, &stopKind, nil)
	if err != nil {
		return rep, err
	}
	var c agentCommand
	if len(stops) > 0 {
		c = stops[0]
	} else {
		c = agentCommand{ID: newID(), ExecutionID: e.ID, SessionID: e.SessionID, HostID: deref(e.HostID), ProjectID: firstString(e.ProjectID, e.dispatchProject()), Kind: stopKind, Payload: json.RawMessage("{}"), Status: pendingStatus}
		if err := commandPut(q, &c); err != nil {
			return rep, err
		}
	}
	firstStop := !isTrue(e.StopRequested) && !e.terminal() // a stop of a finished run cancels nothing queued
	e.StopRequested = ptr(true)
	if taskCancellation {
		e.TaskStopRequested = ptr(true)
	}
	known := false
	if h := deref(e.HostID); h != "" {
		if known, err = knownHost(q, h); err != nil {
			return rep, err
		}
	}
	neverHandedOut := false
	if taskCancellation && known && e.State == "preparing" {
		neverHandedOut, err = q.db().DispStartNeverHandedOut(q.ctx(), &id)
		if err != nil {
			return rep, err
		}
	}
	if (!known || neverHandedOut) && !e.terminal() {
		pending, err := executionCommands(q, id, nil, &pendingStatus)
		if err != nil {
			return rep, err
		}
		for _, x := range pending {
			x.Status = "stopped"
			if err := commandPut(q, &x); err != nil {
				return rep, err
			}
		}
		e.State, e.StopDetail = "cancelled", "stopped before dispatch"
		if !known && e.HostID != nil {
			e.StopDetail = "host never registered"
		}
		if c, err = commandGet(q, c.ID); err != nil {
			return rep, err
		}
	}
	if err := executionPut(q, &e); err != nil {
		return rep, err
	}
	if firstStop {
		if err := cancelQueuedBehind(q, &e); err != nil {
			return rep, err
		}
	}
	out, err := agentStopOutcome(q, &e)
	if err != nil {
		return rep, err
	}
	if rep, err = asT[contract.ExecutionStopReply](c); err != nil {
		return rep, err
	}
	rep.SessionId, rep.HostId, rep.Payload, rep.Stop = strPtr(rep.SessionId), strPtr(rep.HostId), recOf(c.Payload), out
	return rep, nil
}

// declaredNames: the declared artifact names, each once, in declaration order; non-strings are not names.
func declaredNames(declared []string) []string {
	out, seen := []string{}, map[string]bool{}
	for _, s := range declared {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// artifactGate: a completed start waits for its required artifacts; a missing or failed one fails it.
// The fields it sets on the execution record: its state and error, or the wait. Nothing set: no field.
func artifactGate(q *Request, id string, declared []string) (rawObject, error) {
	names := declaredNames(declared)
	r, err := artifactRequirement(withTx(q), q.app, id, names) // the receipt transaction's connection, not a second one
	if err != nil {
		return nil, err
	}
	var gate any = struct{}{}
	if len(r.Missing) > 0 || len(r.Failed) > 0 {
		gate = struct {
			State string                 `json:"state"`
			Error executionArtifactError `json:"error"`
		}{"failed", executionArtifactError{Code: "artifact-required", Message: "Required artifacts missing or failed", Missing: r.Missing, Failed: r.Failed}}
	} else if len(r.Pending) > 0 {
		gate = executionWait{State: "waiting", WaitReason: "artifacts", WaitingSince: nowMs(), PendingArtifacts: r.Pending, DeclaredArtifacts: names}
	}
	var out rawObject
	return out, json.Unmarshal(jsonBytes(gate), &out)
}

// executionWait: the fields of an execution record that waits for its artifacts (artifactGate, settleWaitingAs).
type executionWait struct {
	State             string   `json:"state"`
	WaitReason        string   `json:"waitReason"`
	WaitingSince      int64    `json:"waitingSince"`
	PendingArtifacts  []string `json:"pendingArtifacts"`
	DeclaredArtifacts []string `json:"declaredArtifacts"`
}

// executionWaitOf: the wait fields of a stored execution record; a list item that is not text is left out.
type executionWaitOf struct {
	State             string          `json:"state"`
	WaitReason        string          `json:"waitReason"`
	DeclaredArtifacts json.RawMessage `json:"declaredArtifacts"`
}

// settleWaiting: a waiting execution finishes once its artifacts are settled.
func settleWaiting(ctx context.Context, a *App, id string) error {
	return settleWaitingAs(ctx, a, id, "")
}

// settleWaitingAs: after the retry window, a still-pending artifact fails the execution.
func settleWaitingAs(ctx context.Context, a *App, id, expired string) error {
	err := a.store.Write(ctx, nil, []string{GLOBAL, DISPATCH}, func(q *Request) error {
		if err := lockExecutionTask(q, id); err != nil {
			return err
		}
		if err := q.db().HostReceiptLockExecution(q.ctx(), id); err != nil {
			return err
		}
		e, err := executionRawMaybe(q, id) // open: the wait's fields are removed and an error object set
		if err != nil || e == nil {
			return err
		}
		var w executionWaitOf
		lenientDecode(jsonBytes(e), &w)
		if w.State != "waiting" || w.WaitReason != "artifacts" {
			return nil
		}
		r, err := artifactRequirement(withTx(q), a, id, declaredNames(jsonStrings(w.DeclaredArtifacts)))
		if err != nil {
			return err
		}
		if expired == "" && r.stillUploading() {
			return nil
		}
		failed := len(r.Failed) > 0 || len(r.Missing) > 0 || len(r.Pending) > 0
		for _, k := range []string{"waitReason", "waitingSince", "pendingArtifacts", "declaredArtifacts"} {
			delete(e, k)
		}
		e["state"], e["updatedAt"] = jsonBytes("succeeded"), jsonBytes(nowMs())
		status := "completed" // the receipt the start command settles with
		if failed {
			e["state"], status = jsonBytes("failed"), "failed"
			msg := "Required artifacts missing or failed"
			if expired != "" && len(r.Pending) > 0 {
				msg = expired
			}
			e["error"] = jsonBytes(executionArtifactError{Code: "artifact-required", Message: msg, Missing: r.Missing, Failed: r.Failed, Pending: &r.Pending})
		}
		if err := executionPut(q, &e); err != nil {
			return err
		}
		start := "start"
		cs, err := executionCommands(q, id, &start, nil)
		if err != nil {
			return err
		}
		if len(cs) > 0 {
			ex, err := asT[agentExecution](e)
			if err != nil {
				return err
			}
			if err := agentCommandReceipt(q, &cs[0], &ex, status); err != nil {
				return err
			}
		}
		return agentPromote(q)
	})
	return err
}

// stopExecution: execution_stop in its own transaction (Lab cell stop).
func stopExecution(ctx context.Context, a *App, id string) error {
	return a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		_, err := executionStop(q, id)
		return err
	})
}

// Routes and operations the first Go port left out:
// execution_differences with GET /api/executions/{id}/differences and GET /api/reconciliation.

const differenceTextLimit = 131072

// executionDifferences compares the stored files of two executions of one session.
func executionDifferences(q *Request, id, previousID string) (contract.ExecutionDifferencesReply, error) {
	var rep contract.ExecutionDifferencesReply
	if len(previousID) > 300 {
		previousID = previousID[:300]
	}
	current, err := getExecution(q, id)
	if err != nil {
		return rep, err
	}
	previous, err := getExecution(q, previousID)
	if err != nil {
		return rep, err
	}
	if current.SessionID != previous.SessionID {
		return rep, fail(403, "Different session")
	}
	currentID, previousID := current.ID, previous.ID
	files := func(id string) (map[string]*storedFile, error) {
		xs, err := executionArtifacts(q, id)
		if err != nil {
			return nil, err
		}
		list, err := asT[[]storedFile](xs)
		m := map[string]*storedFile{}
		for i := range list {
			m[list[i].Path] = &list[i]
		}
		return m, err
	}
	left, err := files(previousID)
	if err != nil {
		return rep, err
	}
	right, err := files(currentID)
	if err != nil {
		return rep, err
	}
	var paths []string
	for p := range left {
		paths = append(paths, p)
	}
	for p := range right {
		if _, ok := left[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	lines := func(x *storedFile) ([]string, error) {
		if x == nil {
			return []string{}, nil
		}
		b, err := artifactBytes(q.ctx(), q.app, x.ArtifactID, differenceTextLimit)
		if err != nil {
			return nil, err
		}
		return strings.Split(string(b), "\n"), nil
	}
	small := func(x *storedFile) bool { return x == nil || x.Size <= differenceTextLimit }
	ref := func(x *storedFile) *contract.FileRef {
		if x == nil {
			return nil
		}
		return &contract.FileRef{Id: x.ID, Sha256: strPtr(x.Sha256)}
	}
	textual := regexp.MustCompile(`(?i)\.(html|css)$`)
	diffs := []contract.DiffFile{}
	for _, p := range paths {
		l, r := left[p], right[p]
		var change *contract.TextChange
		if textual.MatchString(p) && small(l) && small(r) {
			b, err := lines(l)
			if err != nil {
				return rep, err
			}
			a, err := lines(r)
			if err != nil {
				return rep, err
			}
			start, end := 0, 0
			for start < len(b) && start < len(a) && b[start] == a[start] {
				start++
			}
			for end < len(b)-start && end < len(a)-start && b[len(b)-1-end] == a[len(a)-1-end] {
				end++
			}
			change = &contract.TextChange{FirstLine: int64(start + 1), Removed: b[start : len(b)-end], Added: a[start : len(a)-end]}
		}
		state := "modified"
		switch {
		case l == nil:
			state = "added"
		case r == nil:
			state = "removed"
		case l.Sha256 == r.Sha256:
			state = "unchanged"
		}
		diffs = append(diffs, contract.DiffFile{Path: p, TextChange: change, State: state, Previous: ref(l), Current: ref(r)})
	}
	return contract.ExecutionDifferencesReply{SessionId: strPtr(current.SessionID), PreviousExecutionId: strPtr(previousID), ExecutionId: currentID, Files: diffs}, nil
}

// storedFile: one stored file of an execution (executionArtifacts).
type storedFile struct {
	ID         string  `json:"id"`
	ArtifactID string  `json:"artifactId"`
	Path       string  `json:"path"`
	Size       float64 `json:"size"`
	Sha256     string  `json:"sha256"`
}

func init() {
	op("execution_differences", func(q *Request, r *contract.ExecutionDifferencesRequest) (contract.ExecutionDifferencesReply, error) {
		return executionDifferences(q, r.ExecutionId, r.Previous)
	})
	route("GET /api/executions/{id}/differences", func(q *Request) (any, error) {
		return executionDifferences(q, q.r.PathValue("id"), q.r.URL.Query().Get("previous"))
	})
	route("GET /api/reconciliation", func(q *Request) (any, error) {
		if err := requireArchitect(q); err != nil {
			return nil, err
		}
		var out struct {
			Reconciliation json.RawMessage `json:"reconciliation"`
		}
		r, err := q.db().DispLatestReconciliation(q.ctx())
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		out.Reconciliation = r
		return out, nil
	})
}

// lostAfter: an unfinished run whose host lost track of it (observation unconfirmed) or that has not answered its
// stop is ended after this long without a receipt; no host will finish it and its questions cannot be acted on.
var lostAfter = envMs("FLEET_LOST_AFTER_MS", int(time.Hour/time.Millisecond))

func init() {
	op("executions_abandon", func(q *Request, r *contract.ExecutionsAbandonRequest) (contract.ExecutionsAbandonReply, error) {
		out := contract.ExecutionsAbandonReply{Abandoned: []string{}, Skipped: []contract.ExecutionSkipped{}}
		if strings.TrimSpace(r.Reason) == "" || len([]rune(r.Reason)) > 4000 {
			return out, fail(400, "Expected reason")
		}
		if len(r.Ids) == 0 || len(r.Ids) > 1000 {
			return out, fail(400, "Expected 1 to 1000 ids")
		}
		for _, id := range r.Ids {
			if !idOK(id) {
				return out, fail(400, "Expected execution ids")
			}
		}
		sessions, err := q.db().DispExecSessions(q.ctx(), r.Ids)
		if err != nil {
			return out, err
		}
		var sessLocks []string
		for _, sid := range sessions {
			sessLocks = append(sessLocks, "session:"+sid)
		}
		if err := acquireTxLock(q, sessLocks...); err != nil {
			return out, err
		}
		for _, id := range r.Ids {
			state, err := executionAbandon(q, id, r.Reason)
			if err != nil {
				return out, err
			}
			if state != "" {
				out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: id, State: state})
				continue
			}
			out.Abandoned = append(out.Abandoned, id)
		}
		publish(q)
		return out, agentPromote(q)
	})
	op("executions_dismiss", func(q *Request, r *contract.ExecutionsDismissRequest) (contract.ExecutionsDismissReply, error) {
		out := contract.ExecutionsDismissReply{Dismissed: []string{}, Skipped: []contract.ExecutionSkipped{}}
		if strings.TrimSpace(r.Reason) == "" || len([]rune(r.Reason)) > 4000 {
			return out, fail(400, "Expected reason")
		}
		if len(r.Ids) == 0 || len(r.Ids) > 1000 {
			return out, fail(400, "Expected 1 to 1000 ids")
		}
		for _, id := range r.Ids {
			if !idOK(id) {
				return out, fail(400, "Expected execution ids")
			}
		}
		by := actor(q, "fleet") // as its audit row
		for _, id := range r.Ids {
			row, err := q.db().DispExecDismissRow(q.ctx(), id)
			found := err == nil
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return out, err
			}
			if !found || !executionEnds(row.State) || row.Problem == nil || row.Dismissed {
				state := "unknown"
				if found {
					state = row.State
				}
				if found && row.Dismissed {
					state = "dismissed"
				}
				out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: id, State: state})
				continue
			}
			// Its own row: engines put execution data wholesale, which would drop a field there.
			if err := q.db().DispProblemDismiss(q.ctx(), db.DispProblemDismissParams{ExecutionID: id, ProjectID: row.ProjectID, Problem: *row.Problem, Reason: r.Reason, Actor: by}); err != nil {
				return out, err
			}
			if err := audit(q, "execution:"+id, "problem", *row.Problem, "dismissed", auditExtra{ProjectID: row.ProjectID, Reason: r.Reason}); err != nil {
				return out, err
			}
			out.Dismissed = append(out.Dismissed, id)
		}
		publish(q)
		return out, nil
	})
	op("executions_reconcile", executionsReconcile)
	onStart(func(ctx context.Context, a *App) {
		a.ensureRuntime()
		grace := lostClosedGrace()
		period := lostAfter / 12
		if grace > 0 && (period <= 0 || grace/2 < period) {
			period = grace / 2
		}
		if lostAfter <= 0 {
			period = 0
		}
		everyWake(ctx, "lost", period, a.lostCh, true, func(ctx context.Context, _ bool) error {
			return reconcileLostExecutions(ctx, a)
		})
	})
}

func (a *App) wakeLost() {
	if a == nil {
		return
	}
	a.ensureRuntime()
	select {
	case a.lostCh <- struct{}{}:
	default:
	}
}

func lostClosedGrace() time.Duration {
	defMs := 60000
	if lostAfter > 0 && int(lostAfter/time.Millisecond) < defMs {
		defMs = int(lostAfter / time.Millisecond)
	}
	return envMs("FLEET_PROCESS_CLOSED_GRACE_MS", defMs)
}

func reconcileLostReason(cause string) string {
	switch cause {
	case "outbox_failed_receipt":
		return "reconciled (outbox_failed_receipt): host terminal receipt failed delivery"
	case "process_closed_no_receipt":
		return "reconciled (process_closed_no_receipt): worker process exited without terminal receipt"
	case "terminal_command_open_execution":
		return "reconciled (terminal_command_open_execution): start or script command already terminal"
	case "workspace_teardown":
		return "reconciled (workspace_teardown): workspace already torn down"
	case "host_removed":
		return "reconciled (host_removed): owning host was removed"
	case "missing_from_host_active_executions":
		return "reconciled (missing_from_host_active_executions): live host no longer reports execution active"
	case "silent_host_job":
		return "reconciled (silent_host_job): host job silent past timeout"
	case "unconfirmed_expired":
		return "reconciled (unconfirmed_expired): unconfirmed execution or unanswered stop expired"
	case "orphan_pipeline_step":
		return "reconciled (orphan_pipeline_step): parent pipeline run no longer exists"
	default:
		return "reconciled: no host terminal receipt after process closed, host stopped reporting the run, or host lost track"
	}
}

func executionsReconcile(q *Request, r *contract.ExecutionsReconcileRequest) (contract.ExecutionsReconcileReply, error) {
	preview := isTrue(r.Preview)
	out := contract.ExecutionsReconcileReply{
		Preview:    preview,
		Reconciled: []string{},
		Candidates: []contract.ExecutionsReconcileItem{},
		Skipped:    []contract.ExecutionSkipped{},
	}
	if r.Ids != nil {
		if len(r.Ids) == 0 || len(r.Ids) > 1000 {
			return out, fail(400, "Expected 1 to 1000 ids")
		}
		for _, id := range r.Ids {
			if !idOK(id) {
				return out, fail(400, "Expected execution ids")
			}
		}
	}
	if r.Reason != nil && (strings.TrimSpace(*r.Reason) == "" || len([]rune(*r.Reason)) > 4000) {
		return out, fail(400, "Expected reason")
	}
	grace := lostClosedGrace()
	candidates, err := q.db().DispLostCandidates(q.ctx(), db.DispLostCandidatesParams{
		Secs:            lostAfter.Seconds(),
		ClosedGraceSecs: grace.Seconds(),
		Ids:             r.Ids,
	})
	if err != nil {
		return out, err
	}
	eligible := make(map[string]db.DispLostCandidatesRow, len(candidates))
	candidateIDs := make([]string, 0, len(candidates))
	for _, c := range candidates {
		eligible[c.ID] = c
		candidateIDs = append(candidateIDs, c.ID)
		out.Candidates = append(out.Candidates, contract.ExecutionsReconcileItem{
			Id:    c.ID,
			Kind:  c.Kind,
			State: c.State,
			Cause: c.Cause,
		})
	}
	if r.Ids != nil {
		rows, err := q.db().DispExecStates(q.ctx(), r.Ids)
		if err != nil {
			return out, err
		}
		byID := make(map[string]db.DispExecStatesRow, len(rows))
		for _, row := range rows {
			byID[row.ID] = row
		}
		seen := make(map[string]bool, len(r.Ids))
		for _, id := range r.Ids {
			if seen[id] {
				continue
			}
			seen[id] = true
			if _, ok := eligible[id]; ok {
				continue
			}
			row, found := byID[id]
			if !found {
				out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: id, State: "unknown", Reason: ptr("unknown execution")})
				continue
			}
			why := "active or unverified: no dead-worker evidence"
			if executionEnds(row.State) {
				why = "already terminal"
			} else if row.State == "waiting" && row.WaitReason == "artifacts" {
				why = "waiting for artifacts"
			}
			out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: id, State: row.State, Reason: &why})
		}
	}
	if preview || len(candidates) == 0 {
		return out, nil
	}
	if err := acquireTxLock(q, DISPATCH); err != nil {
		return out, err
	}
	sessions, err := q.db().DispExecSessions(q.ctx(), candidateIDs)
	if err != nil {
		return out, err
	}
	sessLocks := make([]string, 0, len(sessions))
	for _, sid := range sessions {
		sessLocks = append(sessLocks, "session:"+sid)
	}
	if err := acquireTxLock(q, sessLocks...); err != nil {
		return out, err
	}
	for _, c := range candidates {
		still, err := q.db().DispLostStill(q.ctx(), db.DispLostStillParams{
			ID:              c.ID,
			Secs:            lostAfter.Seconds(),
			ClosedGraceSecs: grace.Seconds(),
		})
		if err != nil {
			return out, err
		}
		if !still {
			rows, err := q.db().DispExecStates(q.ctx(), []string{c.ID})
			if err != nil {
				return out, err
			}
			state := "unknown"
			if len(rows) > 0 {
				state = rows[0].State
			}
			out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: c.ID, State: state, Reason: ptr("state changed before lock")})
			continue
		}
		reason := reconcileLostReason(c.Cause)
		if r.Reason != nil {
			reason = *r.Reason
		}
		state, err := executionAbandon(q, c.ID, reason)
		if err != nil {
			return out, err
		}
		if state != "" {
			out.Skipped = append(out.Skipped, contract.ExecutionSkipped{Id: c.ID, State: state, Reason: ptr("already terminal")})
			continue
		}
		out.Reconciled = append(out.Reconciled, c.ID)
	}
	if len(out.Reconciled) > 0 {
		publish(q)
		if err := agentPromote(q); err != nil {
			return out, err
		}
	}
	return out, nil
}

func reconcileLostExecutions(ctx context.Context, a *App) error {
	grace := lostClosedGrace()
	var candidates []db.DispLostCandidatesRow
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		var err error
		candidates, err = q.db().DispLostCandidates(q.ctx(), db.DispLostCandidatesParams{
			Secs:            lostAfter.Seconds(),
			ClosedGraceSecs: grace.Seconds(),
		})
		return err
	}); err != nil || len(candidates) == 0 {
		return err
	}
	for _, c := range candidates {
		if err := a.store.Write(ctx, nil, []string{GLOBAL, DISPATCH}, func(q *Request) error {
			// Re-check under row lock so a concurrent receipt wins.
			still, err := q.db().DispLostStill(q.ctx(), db.DispLostStillParams{
				ID:              c.ID,
				Secs:            lostAfter.Seconds(),
				ClosedGraceSecs: grace.Seconds(),
			})
			if err != nil || !still {
				return err
			}
			if _, err := executionAbandon(q, c.ID, reconcileLostReason(c.Cause)); err != nil {
				return err
			}
			publish(q)
			if err := agentPromote(q); err != nil {
				return err
			}
			afterCommit(q, func() {
				a.wakeDispatch()
			})
			return nil
		}); err != nil {
			return err
		}
	}
	return nil
}

// executionAbandon ends an unfinished agent run or host job no host will finish: failed with problem unknown:abandoned
// (the outcome is unknown), pending commands settled, the parent woken, its open questions withdrawn, one audit row;
// the caller promotes queued work once after its batch. It returns the state of an
// execution it leaves alone ("unknown": not an agent run or host job), "" when it ended it.
func executionAbandon(q *Request, id, reason string) (string, error) {
	if err := lockExecutionTask(q, id); err != nil {
		return "", err
	}
	row, err := q.db().DispAbandonRow(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "unknown", nil
	}
	if err != nil {
		return "unknown", err
	}
	if k := row.Kind; k != "agent_run" && k != "host_job" {
		// Only a pipeline step whose run row is gone: no engine will put it again, so the typed transition holds.
		// Other kinds belong to their engine (a run's steps, child agents, tool calls) and are left alone.
		orphan, err := q.db().DispOrphanStep(q.ctx(), id)
		if err != nil {
			return "", err
		}
		if !orphan {
			return "unknown", nil
		}
		if executionEnds(row.State) {
			return row.State, nil
		}
		abandoned := "unknown:abandoned"
		if _, err := executionTransition(q, id, executionEvent{To: "failed", Problem: &abandoned, Observation: "unconfirmed"}); err != nil {
			return "", err
		}
		if err := q.db().DispAbandonData(q.ctx(), db.DispAbandonDataParams{ID: id, Reason: reason}); err != nil {
			return "", err
		}
		return "", audit(q, "execution:"+id, "state", row.State, "failed", auditExtra{ProjectID: row.ProjectID, Reason: reason})
	}
	e, err := getExecution(q, id)
	if err != nil {
		return "", err
	}
	prev := e.State
	if e.terminal() {
		return prev, nil
	}
	abandoned := "abandoned"
	if _, err := commandStop(q, commandStopFilter{executionID: &id, statuses: []db.CommandStatus{db.CommandStatusPending, db.CommandStatusUnknown}, detail: &abandoned}); err != nil {
		return "", err
	}
	// Record the loss and withdraw questions that need the old turn before executionPut settles its task.
	asked, err := q.db().DispOpenQuestions(q.ctx(), &id)
	if err != nil {
		return "", err
	}
	if err := questionsWithdraw(q, asked); err != nil {
		return "", err
	}
	if err := q.db().DispAbandonProblem(q.ctx(), id); err != nil {
		return "", err
	}
	for _, k := range []string{"waitReason", "waitDetail", "waitingSince", "pendingArtifacts", "declaredArtifacts"} {
		delete(e.rest, k)
	}
	if err := q.db().DispWaitReason(q.ctx(), db.DispWaitReasonParams{ID: id, WaitReason: nil}); err != nil {
		return "", err
	}
	now := nowMs()
	e.State, e.StopRequested, e.StopDetail, e.Error, e.UpdatedAt, e.FinishedAt = "failed", ptr(true), "abandoned", "abandoned: "+reason, now, jsonBytes(now)
	if err := executionPut(q, &e); err != nil {
		return "", err
	}
	if err := markObservation(q, id, "unconfirmed"); err != nil {
		return "", err
	}
	// Its end is when it was given up, not when it stopped running: the run's time is unknown, never start-to-abandon.
	if err := q.db().DispTotalsWallUnknown(q.ctx(), id); err != nil {
		return "", err
	}
	if err := audit(q, "execution:"+id, "state", prev, "failed", auditExtra{ProjectID: strPtr(e.ProjectID), Reason: reason}); err != nil {
		return "", err
	}
	start := "start"
	starts, err := executionCommands(q, id, &start, nil)
	if err != nil {
		return "", err
	}
	if len(starts) > 0 {
		if err := agentCommandReceipt(q, &starts[0], &e, "failed"); err != nil {
			return "", err
		}
	}
	return "", nil
}
