package main

// delegation. A running agent asks for one child execution through a child receipt; the child
// starts when its parent completed, with the parent's retained observations bound into its start command; its result goes
// back to the parent session as a continuation.

import (
	"context"
	"net/http"
	"regexp"
	"sync"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

var (
	fetchingSources sync.Map
	websitePath     = regexp.MustCompile(`(?i)\.(html|css)$`)
)

const sourcesTooLarge = "Website text inputs unavailable or exceed 128000 characters"

// delegationRequest: a child receipt from a running parent.
func (a *App) delegationRequest(q *Request, req *contract.ChildRequest, c doc) (any, error) {
	rid, input := req.RequestId, req.Input
	if len([]rune(rid)) > 200 || len([]rune(input)) > 32000 {
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
		if mapOf(d["request"])["requestId"] == rid {
			if canonical(d["request"]) != canonical(r) {
				return nil, fail(409, "Delegation identity conflict")
			}
			return d, nil
		}
	}
	if c["kind"] != "start" || c["status"] != "started" || parent["stopRequested"] == true || parent["parentExecutionId"] != nil || parent["continuationOf"] != nil {
		return nil, fail(403, "Delegation unavailable")
	}
	pcfg := mapOf(parent["agentConfig"])
	if !inList(strs(pcfg["delegationProfiles"]), req.ProfileId) {
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
	if astr(nonNil(profile["harness"], "codex")) != astr(nonNil(pcfg["harness"], "codex")) {
		return nil, fail(403, "Cross-harness child needs an explicit account binding")
	}
	session := doc{"id": newID(), "createdAt": nowMs(), "agentConfig": profile, "parentExecutionId": parent["id"]}
	if ps["account"] != nil {
		session["account"] = ps["account"]
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

// fetchSources reads website sources outside the poll transaction; a later poll binds them.
func (a *App) fetchSources(id string, files []doc) {
	if _, busy := fetchingSources.LoadOrStore(id, true); busy {
		return
	}
	go func() {
		defer fetchingSources.Delete(id)
		texts, errText := doc{}, ""
		n := 0
		for _, f := range files {
			b, err := readArtifact(a, astr(nonNil(f["artifactId"], f["id"])))
			if err != nil {
				var he *HTTPError
				if !asHTTP(err, &he) || he.Code != 503 {
					errText = sourcesTooLarge
				} else {
					return // no reachable store now; the next poll asks again
				}
				break
			}
			n += len([]rune(string(b)))
			if n > 128000 {
				errText = sourcesTooLarge
				break
			}
			texts[astr(f["path"])] = string(b)
		}
		ctx := context.Background()
		a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
			q := &Request{app: a, tx: tx, r: (&http.Request{}).WithContext(ctx)}
			d, err := get(q, id, "delegation")
			if err != nil {
				return err
			}
			if errText != "" {
				d["routingError"] = errText
			} else {
				d["sourceTexts"] = texts
			}
			return put(q, "delegation", d)
		})
	}()
}

// delegationReady: whether a start command of this execution may go out now (parent completed, inputs bound).
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
	rs, err := queryRows(q, "SELECT DISTINCT execution_id AS id FROM artifact WHERE state='stored' AND execution_id=ANY($1) UNION SELECT DISTINCT data->>'executionId' FROM document WHERE kind='artifact' AND data->>'executionId'=ANY($1)", history)
	if err != nil {
		return false, err
	}
	for _, r := range rs {
		with[astr(r["id"])] = true
	}
	source := ""
	for _, h := range history {
		if with[h] {
			source = h
			break
		}
	}
	latest, order := map[string]doc{}, []string{}
	if source != "" {
		as, err := executionArtifacts(q, source)
		if err != nil {
			return false, err
		}
		for _, x := range as {
			p := astr(x["path"])
			if _, ok := latest[p]; !ok {
				order = append(order, p)
			}
			latest[p] = x
		}
	}
	if d["routingError"] != nil {
		return false, nil
	}
	files := []doc{}
	size := 0.0
	for _, p := range order {
		if websitePath.MatchString(p) {
			files = append(files, latest[p])
			size += num(latest[p]["size"])
		}
	}
	routingError := func(msg string) (bool, error) {
		d["routingError"] = msg
		return false, put(q, "delegation", d)
	}
	if size > 128000 {
		return routingError(sourcesTooLarge)
	}
	texts := mapOf(d["sourceTexts"])
	if len(files) > 0 && texts == nil {
		a.fetchSources(astr(d["id"]), files)
		return false, nil
	}
	sources, inputs := []doc{}, []any{}
	for _, f := range files {
		sources = append(sources, doc{"path": f["path"], "executionId": f["executionId"], "sha256": f["sha256"], "text": nonNil(texts[astr(f["path"])], "")})
		inputs = append(inputs, doc{"path": f["path"], "executionId": f["executionId"], "sha256": f["sha256"]})
	}
	if (len(sources) == 0 && mapOf(d["request"])["profileId"] == "website-reviewer") || len(js(sources)) > 128000 {
		return routingError(sourcesTooLarge)
	}
	replies, err := replyText(q, astr(parent["id"]))
	if err != nil {
		return false, err
	}
	messages := []doc{}
	for _, t := range replies {
		messages = append(messages, doc{"role": "assistant", "text": t})
	}
	parentArtifacts, err := executionArtifacts(q, astr(parent["id"]))
	if err != nil {
		return false, err
	}
	observations := doc{"messages": messages, "artifacts": parentArtifacts}
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
	in := astr(mapOf(d["request"])["input"])
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
			if cfg := mapOf(s["agentConfig"]); cfg != nil {
				p, err := readTrustedProfile(q, astr(cfg["id"]))
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
