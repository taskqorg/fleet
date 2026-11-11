// The Agents screen's reads (fleet-definition-editors.md): the effective profile with its layers, harness and model
// combinations seen in executions, and the models each harness offers.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"fleet/contract"
)

// agentContent: the head of an agent definition, or the caller's draft.
func agentContent(q *Request, id string, draft bool) (doc, string, error) {
	if draft {
		d, err := draftOf(q, "agent", id)
		if err != nil || d == nil {
			if err == nil {
				err = fail(404, "No draft of "+id)
			}
			return nil, "", err
		}
		var p doc
		return p, "draft on r" + jsString(d.Base), json.Unmarshal([]byte(d.Content), &p)
	}
	h, err := headOf(q, "agent", id, false)
	if err != nil || h == nil {
		if err == nil {
			err = fail(404, "Unknown agent "+id)
		}
		return nil, "", err
	}
	var p doc
	return p, "r" + jsString(h.Head), json.Unmarshal(h.Content, &p)
}

func jsString(n int64) string { b, _ := json.Marshal(n); return string(b) }

// agentEffective: what a session of this agent in the project receives, in layers. Fleet sends the profile as saved
// (the definition layer decides every setting); the project's roles pick it, and its context files reach the agent as
// project rules.
func agentEffective(q *Request, r *contract.AgentEffectiveRequest) (contract.AgentEffectiveReply, error) {
	var out contract.AgentEffectiveReply
	if projectSettingsOf(r.ProjectId) == nil || (q.person != nil && !mayAccessProjectID(q.person, r.ProjectId)) {
		return out, fail(404, "Unknown project")
	}
	p, rev, err := agentContent(q, r.Id, r.Draft != nil && *r.Draft)
	if err != nil {
		return out, err
	}
	s := projectSettingsOf(r.ProjectId)
	roles := []string{}
	for role, v := range mapOf(s["roles"]) {
		if astr(v) == r.Id || astr(mapOf(v)["profile"]) == r.Id {
			roles = append(roles, "role:"+role)
		}
	}
	sort.Strings(roles)
	fields := sortedKeysOf(p)
	ctx := []string{}
	for _, f := range anys(s["contextFiles"]) {
		ctx = append(ctx, astr(f))
	}
	out.Layers = []contract.EffectiveLayer{
		{Name: contract.EffectiveLayerNameRole, Source: "config/projects/" + r.ProjectId + "/project.json", Fields: roles},
		{Name: contract.EffectiveLayerNameDefinition, Source: definitionPath("agent", r.Id), Revision: &rev, Fields: fields},
		{Name: contract.EffectiveLayerNameProject, Source: "config/projects/" + r.ProjectId, Fields: ctx},
	}
	out.Result, err = asT[contract.AgentDefinition](p)
	return out, err
}

// agentCompatibility: the (harness, version, model, effort) combinations of this agent's executions in 30 days, plus the
// head's own; checked when its latest run ended without a harness or start error, failed with one, else unchecked.
func agentCompatibility(q *Request, r *contract.AgentCompatibilityRequest) (contract.AgentCompatibilityReply, error) {
	out := contract.AgentCompatibilityReply{Rows: []contract.CompatibilityRow{}}
	head, _, err := agentContent(q, r.Id, false)
	if err != nil {
		return out, err
	}
	rows, err := q.tx.Query(q.ctx(), `SELECT DISTINCT ON (coalesce(e.harness,''),coalesce(e.harness_version,''),coalesce(e.model,''),coalesce(e.data->>'effort',''))
	   coalesce(e.harness,'codex'),e.harness_version,coalesce(e.model,''),e.data->>'effort',e.state::text,e.problem,e.id,coalesce(e.ended_at,e.started_at,e.updated_at)::text,(d->>'revision')::int
	 FROM execution e CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d
	 WHERE e.definitions @> $1 AND d->>'id'=$2 AND coalesce(e.started_at,e.updated_at) >= now() - interval '30 days'
	 ORDER BY coalesce(e.harness,''),coalesce(e.harness_version,''),coalesce(e.model,''),coalesce(e.data->>'effort',''),e.seq DESC`,
		jsonBytes([]map[string]string{{"kind": "agent", "id": r.Id}}), r.Id)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	seenHead := false
	for rows.Next() {
		var c contract.CompatibilityRow
		var state, id, at string
		var problem *string
		var rev *int64
		if err := rows.Scan(&c.Harness, &c.HarnessVersion, &c.Model, &c.Effort, &state, &problem, &id, &at, &rev); err != nil {
			return out, err
		}
		c.Revision, c.Evidence = rev, &contract.RunSummary{ExecutionId: &id, State: state, At: at}
		switch {
		case state == "succeeded":
			c.State = contract.CompatibilityStateChecked
		case state == "failed" && problem != nil:
			c.State, c.Error = contract.CompatibilityStateFailed, problem
		default:
			c.State = contract.CompatibilityStateUnchecked // never shown as incompatible without a run that says so
		}
		if c.Harness == firstString(head["harness"], "codex") && c.Model == astr(head["model"]) {
			seenHead = true
		}
		out.Rows = append(out.Rows, c)
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if !seenHead {
		e := strPtr(head["effort"])
		out.Rows = append(out.Rows, contract.CompatibilityRow{Harness: firstString(head["harness"], "codex"), Model: astr(head["model"]), Effort: e, State: contract.CompatibilityStateUnchecked})
	}
	return out, nil
}

// harnessModels: the shipped list config/harness-models.json (a release default, not a definition).
// ponytail: executors do not report availableModels yet; the reported source comes with that event field.
func harnessModels(q *Request, r *contract.HarnessModelsRequest) (contract.HarnessModelsReply, error) {
	out := contract.HarnessModelsReply{Models: []contract.HarnessModel{}}
	b, err := os.ReadFile(filepath.Join(configDir(), "harness-models.json"))
	if err != nil {
		return out, nil
	}
	var all map[string][]contract.HarnessModel
	if err := json.Unmarshal(b, &all); err != nil {
		return out, fail(500, "Invalid harness-models.json")
	}
	for _, m := range all[r.Harness] {
		m.Source = contract.ModelSourceShipped
		out.Models = append(out.Models, m)
	}
	return out, nil
}

func init() {
	gatedObjOp("agent_effective", true, typedOp(agentEffective))
	gatedObjOp("agent_compatibility", true, typedOp(agentCompatibility))
	gatedObjOp("harness_models", true, typedOp(harnessModels))
}
