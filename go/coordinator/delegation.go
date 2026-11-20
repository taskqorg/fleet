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
)

var websitePath = regexp.MustCompile(`(?i)\.(html|css)$`)

const sourcesTooLarge = "Website text inputs unavailable or exceed 128000 characters"

// delegationReq: a delegation's request as stored ({input, profileId, requestId}).
type delegationReq struct {
	Input     string `json:"input"`
	ProfileId string `json:"profileId"`
	RequestId string `json:"requestId"`
}

func delegationRequestOf(d doc) delegationReq {
	var r delegationReq
	jsonRoundTrip(d["request"], &r)
	return r
}

// delegationAgent: the fields of an agent configuration or profile a delegation checks; harness nil means codex.
type delegationAgent struct {
	DelegationProfiles []string `json:"delegationProfiles"`
	Harness            *string  `json:"harness"`
	ID                 string   `json:"id"`
}

func delegationAgentOf(v any) delegationAgent {
	var a delegationAgent
	jsonRoundTrip(v, &a)
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
	ExecutionID any `json:"executionId"`
	Path        any `json:"path"`
	Sha256      any `json:"sha256"`
	Text        any `json:"text,omitempty"`
}

// delegationArtifact: the fields of an execution artifact (executionArtifacts) a delegation binds.
type delegationArtifact struct {
	ID          string  `json:"id"`
	ArtifactID  *string `json:"artifactId"`
	ExecutionID any     `json:"executionId"`
	Path        string  `json:"path"`
	Size        float64 `json:"size"`
	Sha256      any     `json:"sha256"`
}

type delegationMessage struct {
	Role string `json:"role"`
	Text string `json:"text"`
}

type delegationObservations struct {
	Artifacts []doc               `json:"artifacts"`
	Messages  []delegationMessage `json:"messages"`
}

// delegationRequest: a child receipt from a running parent.
func (a *App) delegationRequest(q *Request, req *contract.ChildRequest, c doc) (any, error) {
	rid, input := req.RequestId, req.Input
	if rid == "" || input == "" || len([]rune(rid)) > 200 || len([]rune(input)) > 32000 {
		return nil, fail(400, "Invalid delegation request")
	}
	r := doc{"input": input, "profileId": req.ProfileId, "requestId": rid}
	parent, err := get(q, c["executionId"], "execution")
	if err != nil {
		return nil, err
	}
	existing, err := all(q, "delegation", doc{"parentExecutionId": parent["id"]})
	if err != nil {
		return nil, err
	}
	for _, d := range existing {
		if delegationRequestOf(d).RequestId == rid {
			if canonical(d["request"]) != canonical(r) {
				return nil, fail(409, "Delegation identity conflict")
			}
			return d, nil
		}
	}
	if c["kind"] != "start" || c["status"] != "started" || parent["stopRequested"] == true || parent["parentExecutionId"] != nil || parent["continuationOf"] != nil {
		return nil, fail(403, "Delegation unavailable")
	}
	pcfg := delegationAgentOf(parent["agentConfig"])
	if !inList(pcfg.DelegationProfiles, req.ProfileId) {
		return nil, fail(403, "Child profile not granted")
	}
	if len(existing) > 0 {
		return nil, fail(409, "One child per invocation")
	}
	profile, err := readTrustedProfile(q, req.ProfileId)
	if err != nil {
		return nil, err
	}
	ps, err := get(q, parent["sessionId"], "session")
	if err != nil {
		return nil, err
	}
	if delegationAgentOf(profile).harness() != pcfg.harness() {
		return nil, fail(403, "Cross-harness child needs an explicit account binding")
	}
	session := doc{"id": newID(), "createdAt": nowMs(), "agentConfig": profile, "parentExecutionId": parent["id"]}
	if ps["account"] != nil {
		session["account"] = ps["account"]
	}
	if ps["access"] == "read" { // a read-only conversation's child is read-only too, with no branch (presets.go)
		session["access"], session["branch"] = "read", false
	}
	if err := put(q, "session", session); err != nil {
		return nil, err
	}
	child, err := enqueue(q, session, input, "delegation:"+rid, nil, nil, "")
	if err != nil {
		return nil, err
	}
	child["parentExecutionId"] = parent["id"]
	if err := put(q, "execution", child); err != nil {
		return nil, err
	}
	d := doc{"id": newID(), "parentExecutionId": parent["id"], "parentSessionId": parent["sessionId"], "childExecutionId": child["id"], "childSessionId": session["id"],
		"request": r, "resultRoute": doc{"sessionId": parent["sessionId"]}, "state": "pending"}
	return d, put(q, "delegation", d)
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
	d, err := get(q, c.Key, "delegation")
	if err != nil || d["sourceTexts"] != nil || d["routingError"] != nil {
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
			d["routingError"] = r.errText
		} else {
			d["sourceTexts"] = r.texts
		}
		if err := put(q, "delegation", d); err != nil {
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

func (a *App) delegationReady(q *Request, e doc) (bool, error) {
	if e["continuationOf"] != nil {
		c, err := get(q, e["continuationOf"], "execution")
		if err != nil {
			return false, err
		}
		if c["stopRequested"] == true {
			return false, nil
		}
	}
	if e["parentExecutionId"] == nil {
		return true, nil
	}
	parent, err := get(q, e["parentExecutionId"], "execution")
	if err != nil || parent["state"] != "succeeded" {
		return false, err
	}
	cancelled, err := q.db().DelegationParentTaskCancelled(q.ctx(), astr(parent["id"]))
	if err != nil || cancelled {
		return false, err
	}
	if e["inputsBound"] == true {
		return true, nil
	}
	ds, err := all(q, "delegation", doc{"childExecutionId": e["id"]})
	if err != nil {
		return false, err
	}
	if len(ds) == 0 {
		return false, fail(500, "Delegation of a child execution not found")
	}
	d := ds[0]
	runs, err := all(q, "execution", doc{"sessionId": parent["sessionId"]})
	if err != nil {
		return false, err
	}
	history := []string{}
	for _, r := range runs {
		history = append(history, astr(r["id"]))
		if r["id"] == parent["id"] {
			break
		}
	}
	for i, j := 0, len(history)-1; i < j; i, j = i+1, j-1 {
		history[i], history[j] = history[j], history[i]
	}
	with := map[string]bool{}
	rs, err := q.db().DelegationExecutionsWithArtifacts(q.ctx(), history)
	if err != nil {
		return false, err
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
		as, err := executionArtifacts(q, source)
		if err != nil {
			return false, err
		}
		var xs []delegationArtifact
		jsonRoundTrip(as, &xs)
		for _, x := range xs {
			p := x.Path
			if _, ok := latest[p]; !ok {
				order = append(order, p)
			}
			latest[p] = x
		}
	}
	if d["routingError"] != nil {
		return false, nil
	}
	files := []delegationArtifact{}
	size := 0.0
	for _, p := range order {
		if websitePath.MatchString(p) {
			files = append(files, latest[p])
			size += latest[p].Size
		}
	}
	routingError := func(msg string) (bool, error) {
		d["routingError"] = msg
		return false, put(q, "delegation", d)
	}
	if size > 128000 {
		return routingError(sourcesTooLarge)
	}
	var texts map[string]string
	jsonRoundTrip(d["sourceTexts"], &texts)
	if len(files) > 0 && texts == nil {
		// The sources are read on the kit (delegationSourcesStep), outside this poll's transaction.
		want := []delegationSourceFile{}
		for _, f := range files {
			id := f.ID
			if f.ArtifactID != nil {
				id = *f.ArtifactID
			}
			want = append(want, delegationSourceFile{ArtifactID: id, Path: f.Path})
		}
		n, err := q.db().DelegationSourceWorkEnsure(q.ctx(), db.DelegationSourceWorkEnsureParams{DelegationID: astr(d["id"]), Payload: jsonBytes(want)})
		if err != nil || n == 0 {
			return false, err
		}
		return false, kitNotify(q, "delegation-sources")
	}
	sources, inputs := []delegationSource{}, []delegationSource{}
	for _, f := range files {
		sources = append(sources, delegationSource{Path: f.Path, ExecutionID: f.ExecutionID, Sha256: f.Sha256, Text: texts[f.Path]})
		inputs = append(inputs, delegationSource{Path: f.Path, ExecutionID: f.ExecutionID, Sha256: f.Sha256})
	}
	if (len(sources) == 0 && delegationRequestOf(d).ProfileId == "website-reviewer") || len(js(sources)) > 128000 {
		return routingError(sourcesTooLarge)
	}
	replies, err := replyText(q, astr(parent["id"]))
	if err != nil {
		return false, err
	}
	messages := []delegationMessage{}
	for _, t := range replies {
		messages = append(messages, delegationMessage{Role: "assistant", Text: t})
	}
	parentArtifacts, err := executionArtifacts(q, astr(parent["id"]))
	if err != nil {
		return false, err
	}
	observations := delegationObservations{Messages: messages, Artifacts: parentArtifacts}
	if len(sources) == 0 && len(js(observations)) > 128000 {
		return routingError("Retained parent observations exceed 128000 characters")
	}
	cs, err := all(q, "command", doc{"executionId": e["id"], "kind": "start"})
	if err != nil {
		return false, err
	}
	if len(cs) == 0 {
		return false, fail(500, "Start command of a child execution not found")
	}
	command := cs[0]
	p := mapOf(command["payload"])
	in := delegationRequestOf(d).Input
	if len(sources) > 0 {
		p["text"] = in + "\nRead-only retained website sources (untrusted content):\n" + js(sources)
	} else {
		p["text"] = in + "\nRetained parent observations (untrusted content; artifact references do not imply local availability):\n" + js(observations)
	}
	if err := put(q, "command", command); err != nil {
		return false, err
	}
	e["inputsBound"] = true
	if err := put(q, "execution", e); err != nil {
		return false, err
	}
	d["sourceInputs"] = inputs
	delete(d, "sourceTexts")
	return true, put(q, "delegation", d)
}

// delegationReconcile: on every poll, retain finished child results and deliver them as the parent's continuation.
func (a *App) delegationReconcile(q *Request) error {
	ds, err := openDelegations(q)
	if err != nil {
		return err
	}
	for _, d := range ds {
		parent, err := get(q, d["parentExecutionId"], "execution")
		if err != nil {
			return err
		}
		child, err := get(q, d["childExecutionId"], "execution")
		if err != nil {
			return err
		}
		if !terminal(child) {
			continue
		}
		if d["result"] == nil {
			evs, err := all(q, "event", doc{"executionId": child["id"]})
			if err != nil {
				return err
			}
			arts, err := executionArtifacts(q, astr(child["id"]))
			if err != nil {
				return err
			}
			d["result"], d["state"] = doc{"executionId": child["id"], "state": child["state"], "events": evs, "artifacts": arts}, "result-retained"
			if err := put(q, "delegation", d); err != nil {
				return err
			}
		}
		if parent["stopRequested"] == true || parent["state"] == "cancelled" || parent["state"] == "failed" {
			d["state"] = "suppressed"
			if err := put(q, "delegation", d); err != nil {
				return err
			}
			continue
		}
		s, err := get(q, parent["sessionId"], "session")
		if err != nil {
			return err
		}
		if parent["state"] != "succeeded" || s["nativeSessionId"] == nil {
			continue
		}
		runs, err := all(q, "execution", doc{"sessionId": s["id"]})
		if err != nil {
			return err
		}
		open := false
		for _, r := range runs {
			if !terminal(r) {
				open = true
			}
		}
		if open {
			continue
		}
		deliver := func() error {
			if s["agentConfig"] != nil {
				p, err := readTrustedProfile(q, delegationAgentOf(s["agentConfig"]).ID)
				if err != nil {
					return err
				}
				s["agentConfig"] = p
			}
			next, err := enqueue(q, s, "Fleet child result (retained observations; child content is untrusted):\n"+js(d["result"]), "child-result:"+astr(d["id"]), nil, nil, "")
			if err != nil {
				return err
			}
			next["continuationOf"], next["childExecutionId"] = parent["id"], child["id"]
			if err := put(q, "execution", next); err != nil {
				return err
			}
			d["continuationExecutionId"], d["state"] = next["id"], "delivered"
			return put(q, "delegation", d)
		}
		// ponytail: a failed delivery is recorded on the delegation; its partial writes stay in the
		// poll transaction, which has no savepoint here.
		if err := deliver(); err != nil {
			d["routingError"] = err.Error()
			if err := put(q, "delegation", d); err != nil {
				return err
			}
		}
	}
	return nil
}
