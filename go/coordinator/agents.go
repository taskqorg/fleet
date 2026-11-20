// The Agents screen's reads (fleet-definition-editors.md): the effective profile with its layers, harness and model
// combinations seen in executions, and the models each harness offers.
package main

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// agentContent: the head of an agent definition, or the caller's draft.
func agentContent(q *Request, id string, draft bool) (rawObject, string, error) {
	var p rawObject
	if draft {
		d, err := draftOf(q, "agent", id)
		if err != nil || d == nil {
			if err == nil {
				err = fail(404, "No draft of "+id)
			}
			return nil, "", err
		}
		if err := json.Unmarshal([]byte(d.Content), &p); err != nil {
			return nil, "", err
		}
		return p, "draft on r" + strconv.FormatInt(d.Base, 10), nil
	}
	h, err := headOf(q, "agent", id, false)
	if err != nil || h == nil {
		if err == nil {
			err = fail(404, "Unknown agent "+id)
		}
		return nil, "", err
	}
	// The profile a session start would get: the same reader and validator.
	trusted, err := readTrustedProfile(q, id)
	if err != nil {
		return nil, "", err
	}
	if err := json.Unmarshal(jsonBytes(trusted), &p); err != nil {
		return nil, "", err
	}
	return p, "r" + strconv.FormatInt(h.Head, 10), nil
}

// agentProjectView: the project fields agentEffective reads. A role names a profile id, or an object with its profile;
// a context file entry of the settings is its path.
type agentProjectView struct {
	Revision int64 `json:"revision"`
	Settings struct {
		Roles        map[string]json.RawMessage `json:"roles"`
		ContextFiles []json.RawMessage          `json:"contextFiles"`
	} `json:"settings"`
}

// agentEffective: what a session of this agent in the project receives, in layers. Fleet sends the profile as saved
// (the definition layer decides every setting); the project's roles pick it, and its context files reach the agent as
// project rules.
func agentEffective(q *Request, r *contract.AgentEffectiveRequest) (contract.AgentEffectiveReply, error) {
	var out contract.AgentEffectiveReply
	if projectSettingsOf(q, r.ProjectId) == nil || (q.person != nil && !mayAccessProjectID(q.person, r.ProjectId)) {
		return out, fail(404, "Unknown project")
	}
	p, rev, err := agentContent(q, r.Id, r.Draft != nil && *r.Draft)
	if err != nil {
		return out, err
	}
	project, err := projectContext(q, r.ProjectId)
	if err != nil {
		return out, err
	}
	var pv agentProjectView
	jsonRoundTrip(project, &pv)
	projectRevision := "r" + strconv.FormatInt(pv.Revision, 10)
	roles := []string{}
	for role, v := range pv.Settings.Roles {
		var name string
		var obj struct {
			Profile string `json:"profile"`
		}
		if json.Unmarshal(v, &name) != nil {
			json.Unmarshal(v, &obj)
		}
		if name == r.Id || obj.Profile == r.Id {
			roles = append(roles, "role:"+role)
		}
	}
	sort.Strings(roles)
	fields := slices.Sorted(maps.Keys(p))
	ctx := []string{}
	for _, f := range pv.Settings.ContextFiles {
		var path string
		json.Unmarshal(f, &path)
		ctx = append(ctx, path)
	}
	out.Layers = []contract.EffectiveLayer{
		{Name: contract.EffectiveLayerNameRole, Source: definitionPath("project", r.ProjectId), Revision: &projectRevision, Fields: roles},
		{Name: contract.EffectiveLayerNameDefinition, Source: definitionPath("agent", r.Id), Revision: &rev, Fields: fields},
		{Name: contract.EffectiveLayerNameProject, Source: definitionPath("project", r.ProjectId), Revision: &projectRevision, Fields: ctx},
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
	hd, err := headOf(q, "agent", r.Id, false)
	if err != nil || hd == nil {
		return out, err
	}
	runs, err := q.db().AgentsCompatibilityRuns(q.ctx(), db.AgentsCompatibilityRunsParams{Defs: jsonBytes([]map[string]string{{"kind": "agent", "id": r.Id}}), ID: r.Id})
	if err != nil {
		return out, err
	}
	headHarness, headModel, headEffort := firstString(head.text("harness"), "codex"), head.text("model"), head.text("effort")
	seenHead := false
	for _, x := range runs {
		c := contract.CompatibilityRow{Harness: x.Harness, HarnessVersion: x.HarnessVersion, Model: x.Model}
		json.Unmarshal(x.Effort, &c.Effort)
		json.Unmarshal(x.Revision, &c.Revision)
		id := x.ID
		c.Evidence = &contract.RunSummary{ExecutionId: &id, State: x.State, At: x.At}
		// Judged only on a run at the head revision; failed only for a harness or start problem (never incompatible
		// without a run that says so).
		atHead := c.Revision != nil && *c.Revision == hd.Head
		switch {
		case atHead && x.State == "succeeded":
			c.State = contract.CompatibilityStateChecked
		case atHead && x.State == "failed" && x.Problem != nil && harnessProblem(*x.Problem):
			c.State, c.Error = contract.CompatibilityStateFailed, x.Problem
		default:
			c.State = contract.CompatibilityStateUnchecked
		}
		if c.Harness == headHarness && c.Model == headModel && deref(c.Effort) == headEffort {
			seenHead = true
		}
		out.Rows = append(out.Rows, c)
	}
	if !seenHead {
		var e *string
		if headEffort != "" {
			e = &headEffort
		}
		h := hd.Head
		out.Rows = append(out.Rows, contract.CompatibilityRow{Harness: headHarness, Model: headModel, Effort: e, Revision: &h, State: contract.CompatibilityStateUnchecked})
	}
	return out, nil
}

// harnessProblem: an execution problem that comes from the harness or its start, not from the work (a task's tests).
func harnessProblem(p string) bool {
	for _, x := range []string{"harness", "start", "infra:", "preparation", "protocol", "auth", "spawn"} {
		if strings.Contains(p, x) {
			return true
		}
	}
	return false
}

// harnessModels: the shipped list config/harness-models.json (a release default, not a definition).
// ponytail: executors do not report availableModels yet; the reported source comes with that event field.
func harnessModels(q *Request, r *contract.HarnessModelsRequest) (contract.HarnessModelsReply, error) {
	out := contract.HarnessModelsReply{Models: []contract.HarnessModel{}}
	b, err := os.ReadFile(filepath.Join(configDir(), "harness-models.json"))
	if err != nil {
		return out, fail(500, "No config/harness-models.json in this release")
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
	op("agent_effective", agentEffective)
	op("agent_compatibility", agentCompatibility)
	op("harness_models", harnessModels)
}
