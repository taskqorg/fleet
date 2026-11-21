package main

// delegation. A running agent asks for one child execution through a child receipt; the child
// starts when its parent completed, with the parent's retained observations bound into its start command; its result goes
// back to the parent session as a continuation.

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

var websitePath = regexp.MustCompile(`(?i)\.(html|css)$`)

const sourcesTooLarge = "Website text inputs unavailable or exceed 128000 characters"

// delegationReq: a delegation's request as stored ({input, profileId, requestId}).
type delegationReq struct {
	Input     string `json:"input"`
	ProfileId string `json:"profileId"`
	RequestId string `json:"requestId"`
}

// delegationRoute: where a delegation's result goes.
type delegationRoute struct {
	SessionID string `json:"sessionId"`
}

// delegationRec: the delegation record (put "delegation", keyed by its child execution); report and any other field
// another area writes stay in rest.
type delegationRec struct {
	ID                      string              `json:"id"`
	ParentExecutionID       string              `json:"parentExecutionId"`
	ParentSessionID         string              `json:"parentSessionId"`
	ChildExecutionID        string              `json:"childExecutionId"`
	ChildSessionID          string              `json:"childSessionId"`
	Request                 delegationReq       `json:"request"`
	ResultRoute             delegationRoute     `json:"resultRoute"`
	State                   string              `json:"state"`
	RoutingError            string              `json:"routingError,omitempty"`
	SourceTexts             map[string]string   `json:"sourceTexts,omitempty"`
	SourceInputs            *[]delegationSource `json:"sourceInputs,omitempty"`
	Result                  json.RawMessage       `json:"result,omitempty"` // delegationResult as stored
	ContinuationExecutionID string                `json:"continuationExecutionId,omitempty"`
	Report                  *contract.WorkerReport `json:"report,omitempty"`
	rest                    recordRest
}

func (d *delegationRec) UnmarshalJSON(b []byte) error {
	type plain delegationRec
	return restDecode(b, (*plain)(d), &d.rest)
}

func (d delegationRec) MarshalJSON() ([]byte, error) {
	type plain delegationRec
	return restEncode(plain(d), d.rest)
}

// delegationResult: a finished child's retained result; fields in byte order, as the continuation text has them.
type delegationResult struct {
	Artifacts   json.RawMessage `json:"artifacts"` // the child's artifact records, passed through
	Events      json.RawMessage `json:"events"`    // the child's event records, passed through
	ExecutionID string          `json:"executionId"`
	State       string          `json:"state"`
}

func delegationGet(q *Request, id string) (delegationRec, error) {
	var d delegationRec
	if id == "" {
		return d, errNotFound
	}
	b, err := q.db().DelegationByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, errNotFound
	}
	if err != nil {
		return d, err
	}
	err = json.Unmarshal(b, &d)
	return d, err
}

func delegationsList(q *Request, parentExecutionID, childExecutionID *string) ([]delegationRec, error) {
	bs, err := q.db().Delegations(q.ctx(), db.DelegationsParams{ParentExecutionID: parentExecutionID, ChildExecutionID: childExecutionID})
	if err != nil {
		return nil, err
	}
	out := make([]delegationRec, 0, len(bs))
	for _, b := range bs {
		var d delegationRec
		if err := json.Unmarshal(b, &d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func delegationPut(q *Request, d *delegationRec) error {
	if d.ChildExecutionID == "" {
		return fail(409, "Delegation child execution not found: ")
	}
	n, err := q.db().DelegationPut(q.ctx(), db.DelegationPutParams{
		ID:                d.ChildExecutionID,
		Delegation:        jsonBytes(d),
		ParentExecutionID: strPtr(d.ParentExecutionID),
	})
	if err == nil && n == 0 {
		return fail(409, "Delegation child execution not found: "+d.ChildExecutionID)
	}
	return err
}

// delegationExecution: the fields of an execution record a delegation reads and writes; the rest stays as stored.
type delegationExecution struct {
	ID                string          `json:"id"`
	SessionID         string          `json:"sessionId,omitempty"`
	State             string          `json:"state,omitempty"`
	AgentConfig       json.RawMessage `json:"agentConfig,omitempty"`
	StopRequested     bool            `json:"stopRequested,omitempty"`
	ParentExecutionID *string         `json:"parentExecutionId,omitempty"`
	ContinuationOf    *string         `json:"continuationOf,omitempty"`
	ChildExecutionID  string          `json:"childExecutionId,omitempty"`
	InputsBound       bool            `json:"inputsBound,omitempty"`
	rest              recordRest
}

func (e *delegationExecution) UnmarshalJSON(b []byte) error {
	type plain delegationExecution
	return restDecode(b, (*plain)(e), &e.rest)
}

func (e delegationExecution) MarshalJSON() ([]byte, error) {
	type plain delegationExecution
	return restEncode(plain(e), e.rest)
}

// delegationStartPayload: the text of a child's start command payload; the rest of the payload stays as stored.
type delegationStartPayload struct {
	Text string `json:"text"`
	rest recordRest
}

func (p *delegationStartPayload) UnmarshalJSON(b []byte) error {
	type plain delegationStartPayload
	return restDecode(b, (*plain)(p), &p.rest)
}

func (p delegationStartPayload) MarshalJSON() ([]byte, error) {
	type plain delegationStartPayload
	return restEncode(plain(p), p.rest)
}

// delegationAgent: the fields of an agent configuration or profile a delegation checks; harness nil means codex.
type delegationAgent struct {
	DelegationProfiles []string `json:"delegationProfiles"`
	Harness            *string  `json:"harness"`
	ID                 string   `json:"id"`
}

func delegationAgentOf(raw json.RawMessage) delegationAgent {
	var a delegationAgent
	json.Unmarshal(raw, &a)
	return a
}

func (a delegationAgent) harness() string {
	if a.Harness == nil {
		return "codex"
	}
	return *a.Harness
}

// delegationSourceFile: one website source to read (the delegation_source_work payload).
type delegationSourceFile struct {
	ArtifactID string `json:"artifactId"`
	Path       string `json:"path"`
}

// delegationSource: a bound website source; the fields in byte order, as the start command text has them.
type delegationSource struct {
	ExecutionID *string `json:"executionId"`
	Path        string  `json:"path"`
	Sha256      string  `json:"sha256"`
	Text        *string `json:"text,omitempty"`
}

// delegationArtifact: an execution artifact (executionArtifacts); the fields in byte order, as the start command text has them.
type delegationArtifact struct {
	ArtifactID  string  `json:"artifactId"`
	ExecutionID *string `json:"executionId"`
	ID          string  `json:"id"`
	MediaType   string  `json:"mediaType"`
	Path        string  `json:"path"`
	Sha256      string  `json:"sha256"`
	Size        int64   `json:"size"`
}

// delegationArtifacts: executionArtifacts of one execution, decoded once.
func delegationArtifacts(q *Request, executionID string) ([]delegationArtifact, error) {
	as, err := executionArtifacts(q, executionID)
	if err != nil {
		return nil, err
	}
	return asT[[]delegationArtifact](as)
}

type delegationMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type delegationObservations struct {
	Artifacts []delegationArtifact `json:"artifacts"`
	Messages  []delegationMessage  `json:"messages"`
}

func (a *App) delegationChild(q *Request, req *contract.ChildRequest, c agentCommand) (any, error) {
	rid, input := req.RequestId, req.Input
	if rid == "" || input == "" || len([]rune(rid)) > 200 || len([]rune(input)) > 32000 {
		return nil, fail(400, "Invalid delegation request")
	}
	r := delegationReq{Input: input, ProfileId: req.ProfileId, RequestId: rid}
	parent, err := executionGetAs[delegationExecution](q, c.ExecutionID)
	if err != nil {
		return nil, err
	}
	existing, err := delegationsList(q, &parent.ID, nil)
	if err != nil {
		return nil, err
	}
	for _, d := range existing {
		if d.Request.RequestId == rid {
			if d.Request != r {
				return nil, fail(409, "Delegation identity conflict")
			}
			return d, nil
		}
	}
	if c.Kind != "start" || c.Status != "started" || parent.StopRequested || parent.ParentExecutionID != nil || parent.ContinuationOf != nil {
		return nil, fail(403, "Delegation unavailable")
	}
	pcfg := delegationAgentOf(parent.AgentConfig)
	if !inList(pcfg.DelegationProfiles, req.ProfileId) {
		return nil, fail(403, "Child profile not granted")
	}
	if len(existing) > 0 {
		return nil, fail(409, "One child per invocation")
	}
	trusted, err := readTrustedProfile(q, req.ProfileId)
	if err != nil {
		return nil, err
	}
	profile := json.RawMessage(jsonBytes(trusted))
	ps, err := sessionByID(q, parent.SessionID)
	if err != nil {
		return nil, err
	}
	if delegationAgentOf(profile).harness() != pcfg.harness() {
		return nil, fail(403, "Cross-harness child needs an explicit account binding")
	}
	session := agentSession{ID: newID(), CreatedAt: nowMs(), AgentConfig: profile, ParentExecutionID: parent.ID, Account: ps.Account}
	if ps.Access == "read" { // a read-only conversation's child is read-only too, with no branch (presets.go)
		branch := false
		session.Access, session.Branch = "read", &branch
	}
	if err := sessionPut(q, &session); err != nil {
		return nil, err
	}
	queued, err := agentEnqueue(q, &session, input, "delegation:"+rid, nil, nil, "", nil)
	if err != nil {
		return nil, err
	}
	child, err := asT[delegationExecution](queued)
	if err != nil {
		return nil, err
	}
	child.ParentExecutionID = &parent.ID
	if err := executionPut(q, &child); err != nil {
		return nil, err
	}
	d := delegationRec{ID: newID(), ParentExecutionID: parent.ID, ParentSessionID: parent.SessionID, ChildExecutionID: child.ID, ChildSessionID: session.ID,
		Request: r, ResultRoute: delegationRoute{SessionID: parent.SessionID}, State: "pending"}
	err = delegationPut(q, &d)
	return d, err
}

// delegationSources: the website sources of a delegation read outside the step's transaction: their texts, the reason
// they cannot be bound, or that no store is reachable now (tried again later).
type delegationSources struct {
	texts       map[string]string
	errText     string
	unreachable error
}

// delegationSourcesStep reads the website sources of delegation c.Key outside the transaction (the delegation-sources
// queue's step, delegation_work.go) and records them on the delegation; a later poll binds them. A crash before the
// record reads them again.
func delegationSourcesStep(q *Request, now time.Time, c *kitClaim) error {
	d, err := delegationGet(q, c.Key)
	if err != nil || d.SourceTexts != nil || d.RoutingError != "" {
		if err != nil && !errors.Is(err, errNotFound) {
			var he *HTTPError
			if !asHTTP(err, &he) || he.Code != 404 {
				return err
			}
		}
		return q.db().DelegationSourceWorkDelete(q.ctx(), c.Key)
	}
	if r, ok := c.Result.(delegationSources); ok {
		if r.unreachable != nil {
			return r.unreachable
		}
		if r.errText != "" {
			d.RoutingError = r.errText
		} else {
			d.SourceTexts = r.texts
		}
		if err := delegationPut(q, &d); err != nil {
			return err
		}
		return q.db().DelegationSourceWorkDelete(q.ctx(), c.Key)
	}
	payload, err := q.db().DelegationSourceWorkFiles(q.ctx(), c.Key)
	if err != nil {
		return err
	}
	var files []delegationSourceFile
	if err := json.Unmarshal(payload, &files); err != nil {
		return err
	}
	a := q.app
	c.Call = func(context.Context) any {
		r := delegationSources{texts: map[string]string{}}
		n := 0
		for _, f := range files {
			b, err := readArtifact(a, f.ArtifactID)
			if err != nil {
				var he *HTTPError
				if asHTTP(err, &he) && he.Code == 503 {
					return delegationSources{unreachable: err} // no reachable store now
				}
				r.errText = sourcesTooLarge
				return r
			}
			n += len([]rune(string(b)))
			if n > 128000 {
				r.errText = sourcesTooLarge
				return r
			}
			r.texts[f.Path] = string(b)
		}
		return r
	}
	return nil
}

// delegationReady: whether execution e (host_poll.go's record) may start now; a delegated child binds its inputs first,
// and e then holds the record as written.
func (a *App) delegationReady(q *Request, e rawObject) (bool, error) {
	ex, err := asT[delegationExecution](e)
	if err != nil {
		return false, err
	}
	ok, bound, err := a.delegationBind(q, &ex)
	if bound {
		if rerr := recordReplace(e, ex); rerr != nil {
			return false, rerr
		}
	}
	return ok, err
}

// delegationBind: delegationReady on the typed record; bound when e was written with its inputs bound.
func (a *App) delegationBind(q *Request, e *delegationExecution) (ok, bound bool, err error) {
	if e.ContinuationOf != nil {
		c, err := executionGetAs[delegationExecution](q, *e.ContinuationOf)
		if err != nil {
			return false, false, err
		}
		if c.StopRequested {
			return false, false, nil
		}
	}
	if e.ParentExecutionID == nil {
		return true, false, nil
	}
	parent, err := executionGetAs[delegationExecution](q, *e.ParentExecutionID)
	if err != nil || parent.State != "succeeded" {
		return false, false, err
	}
	cancelled, err := q.db().DelegationParentTaskCancelled(q.ctx(), parent.ID)
	if err != nil || cancelled {
		return false, false, err
	}
	if e.InputsBound {
		return true, false, nil
	}
	ds, err := delegationsList(q, nil, &e.ID)
	if err != nil {
		return false, false, err
	}
	if len(ds) == 0 {
		return false, false, fail(500, "Delegation of a child execution not found")
	}
	d := ds[0]
	runs, err := executionsAs[delegationExecution](q, &parent.SessionID)
	if err != nil {
		return false, false, err
	}
	history := []string{}
	for _, r := range runs {
		history = append(history, r.ID)
		if r.ID == parent.ID {
			break
		}
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	with := map[string]bool{}
	rs, err := q.db().DelegationExecutionsWithArtifacts(q.ctx(), history)
	if err != nil {
		return false, false, err
	}
	for _, id := range rs {
		with[id] = true
	}
	source := ""
	for _, h := range history {
		if with[h] {
			source = h
			break
		}
	}
	latest, order := map[string]delegationArtifact{}, []string{}
	if source != "" {
		xs, err := delegationArtifacts(q, source)
		if err != nil {
			return false, false, err
		}
		for _, x := range xs {
			p := x.Path
			if _, ok := latest[p]; !ok {
				order = append(order, p)
			}
			latest[p] = x
		}
	}
	if d.RoutingError != "" {
		return false, false, nil
	}
	files := []delegationArtifact{}
	size := int64(0)
	for _, p := range order {
		if websitePath.MatchString(p) {
			files = append(files, latest[p])
			size += latest[p].Size
		}
	}
	routingError := func(msg string) (bool, bool, error) {
		d.RoutingError = msg
		return false, false, delegationPut(q, &d)
	}
	if size > 128000 {
		return routingError(sourcesTooLarge)
	}
	texts := d.SourceTexts
	if len(files) > 0 && texts == nil {
		// The sources are read on the kit (delegationSourcesStep), outside this poll's transaction.
		want := []delegationSourceFile{}
		for _, f := range files {
			id := f.ID
			if f.ArtifactID != "" {
				id = f.ArtifactID
			}
			want = append(want, delegationSourceFile{ArtifactID: id, Path: f.Path})
		}
		n, err := q.db().DelegationSourceWorkEnsure(q.ctx(), db.DelegationSourceWorkEnsureParams{DelegationID: d.ID, Payload: jsonBytes(want)})
		if err != nil || n == 0 {
			return false, false, err
		}
		return false, false, kitNotify(q, "delegation-sources")
	}
	sources, inputs := []delegationSource{}, []delegationSource{}
	for _, f := range files {
		text := texts[f.Path]
		sources = append(sources, delegationSource{Path: f.Path, ExecutionID: f.ExecutionID, Sha256: f.Sha256, Text: &text})
		inputs = append(inputs, delegationSource{Path: f.Path, ExecutionID: f.ExecutionID, Sha256: f.Sha256})
	}
	if (len(sources) == 0 && d.Request.ProfileId == "website-reviewer") || len(js(sources)) > 128000 {
		return routingError(sourcesTooLarge)
	}
	replies, err := replyText(q, parent.ID)
	if err != nil {
		return false, false, err
	}
	messages := []delegationMessage{}
	for _, t := range replies {
		messages = append(messages, delegationMessage{Role: "assistant", Text: t})
	}
	parentArtifacts, err := delegationArtifacts(q, parent.ID)
	if err != nil {
		return false, false, err
	}
	observations := delegationObservations{Messages: messages, Artifacts: parentArtifacts}
	if len(sources) == 0 && len(js(observations)) > 128000 {
		return routingError("Retained parent observations exceed 128000 characters")
	}
	start := "start"
	cs, err := commandsList(q, db.CommandsParams{ExecutionID: &e.ID, Kind: &start})
	if err != nil {
		return false, false, err
	}
	if len(cs) == 0 {
		return false, false, fail(500, "Start command of a child execution not found")
	}
	command := cs[0]
	var p delegationStartPayload
	if err := json.Unmarshal(command.Payload, &p); err != nil {
		return false, false, err
	}
	in := d.Request.Input
	if len(sources) > 0 {
		p.Text = in + "\nRead-only retained website sources (untrusted content):\n" + js(sources)
	} else {
		p.Text = in + "\nRetained parent observations (untrusted content; artifact references do not imply local availability):\n" + js(observations)
	}
	command.Payload = jsonBytes(p)
	if err := commandPut(q, &command); err != nil {
		return false, false, err
	}
	e.InputsBound = true
	if err := executionPut(q, e); err != nil {
		return false, false, err
	}
	d.SourceInputs, d.SourceTexts = &inputs, nil
	return true, true, delegationPut(q, &d)
}

// delegationReconcile: on every poll, retain finished child results and deliver them as the parent's continuation.
func (a *App) delegationReconcile(q *Request) error {
	ds, err := openDelegations(q)
	if err != nil {
		return err
	}
	for _, d := range ds {
		parent, err := executionGetAs[delegationExecution](q, d.ParentExecutionID)
		if err != nil {
			return err
		}
		child, err := executionGetAs[delegationExecution](q, d.ChildExecutionID)
		if err != nil {
			return err
		}
		if !executionEnds(child.State) {
			continue
		}
		if !jsonIsObject(d.Result) {
			es, err := q.db().EventsNonDeltaOfExecution(q.ctx(), child.ID)
			if err != nil {
				return err
			}
			evs := eventRecordsOf(es)
			arts, err := executionArtifacts(q, child.ID)
			if err != nil {
				return err
			}
			d.Result, d.State = jsonBytes(delegationResult{ExecutionID: child.ID, State: child.State, Events: jsonBytes(evs), Artifacts: jsonBytes(arts)}), "result-retained"
			if err := delegationPut(q, &d); err != nil {
				return err
			}
		}
		if parent.StopRequested || parent.State == "cancelled" || parent.State == "failed" {
			d.State = "suppressed"
			if err := delegationPut(q, &d); err != nil {
				return err
			}
			continue
		}
		s, err := sessionByID(q, parent.SessionID)
		if err != nil {
			return err
		}
		if parent.State != "succeeded" || s.NativeSessionID == "" {
			continue
		}
		runs, err := executionsAs[delegationExecution](q, &s.ID)
		if err != nil {
			return err
		}
		running := false
		for _, r := range runs {
			if !executionEnds(r.State) {
				running = true
			}
		}
		if running {
			continue
		}
		deliver := func() error {
			if len(s.AgentConfig) > 0 && string(s.AgentConfig) != "null" {
				p, err := readTrustedProfile(q, delegationAgentOf(s.AgentConfig).ID)
				if err != nil {
					return err
				}
				s.AgentConfig = jsonBytes(p)
			}
			// The retained result as its text with every object's keys in byte order (clone: the stored JSON as read back).
			queued, err := agentEnqueue(q, &s, "Fleet child result (retained observations; child content is untrusted):\n"+js(clone(d.Result)), "child-result:"+d.ID, nil, nil, "", nil)
			if err != nil {
				return err
			}
			next, err := asT[delegationExecution](queued)
			if err != nil {
				return err
			}
			next.ContinuationOf, next.ChildExecutionID = &parent.ID, child.ID
			if err := executionPut(q, &next); err != nil {
				return err
			}
			d.ContinuationExecutionID, d.State = next.ID, "delivered"
			return delegationPut(q, &d)
		}
		// ponytail: a failed delivery is recorded on the delegation; its partial writes stay in the
		// poll transaction, which has no savepoint here.
		if err := deliver(); err != nil {
			d.RoutingError = err.Error()
			if err := delegationPut(q, &d); err != nil {
				return err
			}
		}
	}
	return nil
}
