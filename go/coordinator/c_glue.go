// Area C seams to areas A and B: the person gate on area C operations, long operations (their own transactions), the
// question answer hooks, pipeline_run and direct_search (projects.ts runPipeline), and the agent step start.
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// longOperations: operations that open their own transactions (q.tx nil). runOp (b_ops.go) runs them without one.
var longOperations = map[string]handler{}

// answerHooks: checked before a question's answer is recorded and run after, in the same transaction (tasks.ts onAnswer).
// question is the row (snake_case columns); in after, its answer column holds the answer.
type answerHook struct {
	check func(q *Request, question doc, answer string) error
	after func(q *Request, question doc) error
}

var answerHooks = []answerHook{{labAnswerCheck, labAnswerAfter}}

func runAnswerChecks(q *Request, question doc, answer string) error {
	for _, h := range answerHooks {
		if err := h.check(q, question, answer); err != nil {
			return err
		}
	}
	return nil
}

func runAnswerAfter(q *Request, id string) error {
	row, err := first(q, "SELECT * FROM question WHERE id=$1", id)
	if err != nil || row == nil {
		return err
	}
	for _, h := range answerHooks {
		if err := h.after(q, row); err != nil {
			return err
		}
	}
	return nil
}

// cop: an area C operation behind the same person gate as every other operation.
func cop(name string, read bool, h handler) {
	operation(name, read, func(q *Request) (any, error) {
		if err := q.app.guardOp(q, name, q.body); err != nil {
			return nil, err
		}
		return h(q)
	})
}

// clong: a long operation: POST /api/ops/<name> without a surrounding transaction, listed among the tools, and run by
// runOp without one.
func clong(name string, h handler) {
	gated := func(q *Request) (any, error) {
		if err := q.app.guardOp(q, name, q.body); err != nil {
			return nil, err
		}
		return h(q)
	}
	longRoute("POST /api/ops/"+name, gated)
	longOperations[name] = gated
	operation(name, false, func(q *Request) (any, error) {
		return nil, fail(500, "long operation "+name+" ran inside a transaction")
	})
}

// cWrite runs fn in one write transaction in the person's scope (long operations and routes).
func cWrite(q *Request, lock string, fn func(q *Request) error) error {
	return q.app.store.Tx(q.ctx(), scopeOf(q.person), true, lock, func(tx pgx.Tx) error {
		q.tx = tx
		defer func() { q.tx = nil }()
		return fn(q)
	})
}

// runPipeline: the selected method's v2 pipeline with the query as the example ({query, prompt}); waits for the run.
func runPipeline(q *Request, invocation string) (any, error) {
	b, err := cBody(q)
	if err != nil {
		return nil, err
	}
	if invocation == "direct" {
		b["invocation"] = "direct"
	}
	rid, _ := b["requestId"].(string)
	query, isStr := b["query"].(string)
	_, methodOK := b["methodId"].(string)
	if !cKeysOnly(b, "projectId", "requestId", "query", "methodId", "invocation") || rid == "" || len(rid) > 200 || !isStr || strings.TrimSpace(query) == "" || len(query) > 2000 || b["methodId"] != nil && !methodOK {
		return nil, fail(400, "Expected projectId, requestId, query and optional methodId")
	}
	inv := astr(b["invocation"])
	if inv == "" {
		inv = "pipeline"
	}
	var run doc
	old := false
	err = cWrite(q, "", func(q *Request) error {
		signature := sha256hex([]byte(cCanon(doc{"projectId": b["projectId"], "query": query, "methodId": b["methodId"], "invocation": inv})))
		prev, err := runsWhere(q, "data->>'projectId'=$1 AND data->>'requestId'=$2", astr(b["projectId"]), rid)
		if err != nil {
			return err
		}
		if len(prev) > 0 {
			if prev[0]["signature"] != signature {
				return fail(409, "Request identity conflict")
			}
			run, old = prev[0], true
			return nil
		}
		if inv != "direct" && inv != "pipeline" {
			return fail(400, "Invalid invocation")
		}
		p, err := projectContext(astr(b["projectId"]))
		if err != nil {
			return err
		}
		c, err := catalogue(q, astr(b["projectId"]))
		if err != nil {
			return err
		}
		want := astr(b["methodId"])
		source := c["recommendationSource"]
		if want == "" {
			want = astr(c["recommendedMethod"])
		} else {
			source = "explicit"
		}
		var method doc
		for _, m := range cList(c["methods"]) {
			if mapOf(m)["id"] == want {
				method = mapOf(m)
			}
		}
		if method == nil {
			return fail(400, "Selected method unavailable for project")
		}
		run, err = pipelineStart(q, doc{"file": method["pipeline"], "projectId": b["projectId"], "requestId": rid, "example": doc{"query": query, "prompt": query},
			"requestedBy": requester(q), "extra": doc{"signature": signature, "invocation": inv, "selectedMethod": method["id"], "selectionSource": source, "context": p}})
		return err
	})
	if err != nil || old {
		return run, err
	}
	pipelineAdvance(q.app, astr(run["id"]))
	return pipelineWait(q.ctx(), q.app, astr(run["id"]))
}

var skillID = regexp.MustCompile(`^[a-z0-9-]{1,80}$`)

// Artifact hooks over area D (d_artifacts.go). The caller's transaction rides in the context, so D writes the rows in it.
func cPID(v any) *string {
	if s, ok := v.(string); ok {
		return &s
	}
	return nil
}

func init() {
	cArtifactSave = func(q *Request, projectID any, scope, executionID, name string, b []byte, media string) (string, error) {
		id, _, err := saveObject(cCtx(q), q.app, cPID(projectID), scope, executionID, name, b, media)
		return id, err
	}
	cArtifactWriteObject = func(q *Request, projectID any, scope string, b []byte, media string) (string, string, error) {
		s, hash, err := writeObject(cCtx(q), q.app, cPID(projectID), scope, b, media)
		if err != nil {
			return "", "", err
		}
		return hash, s.ID, nil
	}
	cArtifactRead = artifactBytes
}

func init() {
	for _, inv := range []string{"pipeline", "direct"} {
		inv := inv
		name := map[string]string{"pipeline": "pipeline_run", "direct": "direct_search"}[inv]
		h := func(q *Request) (any, error) { return runPipeline(q, inv) }
		clong(name, h)
		projectOps[name] = registeredOp{false, h}
	}
	blongRoute("POST /api/pipeline-runs", func(q *Request) (any, error) { return runPipeline(q, "pipeline") })

	cRecommend = func(q *Request, projectID, methodID string) (any, error) {
		sub := *q
		sub.body = cJSON(doc{"projectId": projectID, "methodId": methodID})
		return projectOps["recommendation_set"].h(&sub)
	}
	cProjectKnown = func(q *Request, id string) bool { _, err := catalogue(q, id); return err == nil }

	// Agent step (app.ts startAgent): the step's trusted profile with the variant overrides, its skills, a session of the
	// run, and an ordinary queued execution.
	cStartAgentStep = func(q *Request, run, step doc, stepExecutionID, prompt string, inputs []any, overrides doc) (string, error) {
		pid := astr(step["profile"])
		if overrides["profile"] != nil {
			pid = astr(overrides["profile"])
		}
		profile, err := readTrustedProfile(pid)
		if err != nil {
			return "", err
		}
		if v, ok := overrides["model"]; ok {
			profile["model"] = v
		}
		if v, ok := overrides["skills"]; ok {
			profile["skills"] = v
		}
		web := "connections"
		if v, ok := overrides["web_route"]; ok {
			if v != "connections" && v != "builtin" {
				return "", fail(400, "web_route must be connections or builtin")
			}
			web = astr(v)
		}
		skills := []any{}
		for _, x := range cList(profile["skills"]) {
			id, _ := x.(string)
			if !skillID.MatchString(id) {
				return "", fail(400, "Invalid skill")
			}
			text, err := os.ReadFile(filepath.Join(cConfigDir(), "skills", id, "SKILL.md"))
			if err != nil {
				return "", fail(400, "Unknown skill "+id)
			}
			skills = append(skills, doc{"id": id, "text": string(text)})
		}
		s := doc{"id": uuid(), "createdAt": now(), "agentConfig": profile, "pipelineRunId": run["id"]}
		if overrides["account"] != nil {
			s["account"] = overrides["account"]
		}
		if run["projectId"] != nil {
			s["projectId"] = run["projectId"]
		}
		if err := putDoc(q, "session", s); err != nil {
			return "", err
		}
		execution := doc{"pipelineStepId": stepExecutionID, "pipelineRunId": run["id"]}
		if run["experimentId"] != nil {
			execution["experimentId"] = run["experimentId"]
		}
		host := ""
		if overrides["host"] != nil {
			host = fmt.Sprint(overrides["host"])
		}
		e, err := enqueue(q, s, prompt, "pipeline:"+stepExecutionID, doc{"inputs": inputs, "skills": skills, "webRoute": web}, execution, host)
		if err != nil {
			return "", err
		}
		return astr(e["id"]), nil
	}
}

// cCtx: the request context carrying its transaction, so area D's helpers (dTx) run in it instead of taking the lock again.
func cCtx(q *Request) context.Context {
	if q.tx == nil {
		return q.ctx()
	}
	return context.WithValue(q.ctx(), txKey{}, q.tx)
}
