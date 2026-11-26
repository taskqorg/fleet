// System changes overview (docs/designs/fleet-system-management.md): drift across repository files, active store
// definitions, Coordinator/UI/host release versions, pinned vs observed harness CLIs, and the release-managed components
// per kind with their proposal and host states (pins_view.go).
package main

import (
	"sort"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("system_changes", systemChanges)
}

func systemChanges(q *Request, _ *contract.SystemChangesRequest) (contract.SystemChangesReply, error) {
	usage, err := recentUsage(q, 30, "agent", "pipeline")
	if err != nil {
		return contract.SystemChangesReply{}, err
	}
	return systemChangesOf(q, usage, nil)
}

// systemChangesOf: system_changes with the last 30 days' usage of agents and pipelines already read (recentUsage).
// current, when not nil, is every current definition already read (library_view); nil reads them.
func systemChangesOf(q *Request, usage map[string]map[string]contract.Window, current []definitionRow) (contract.SystemChangesReply, error) {
	out := contract.SystemChangesReply{
		RepositoryChanged: []contract.SystemChangeDefinition{},
		Ahead:             []contract.SystemChangeDefinition{},
		StoreOnly:         []contract.SystemChangeDefinition{},
		Experiments:       []contract.SystemChangeExperiment{},
		HarnessDrift:      []contract.SystemChangeHostHarness{},
		Components:        []contract.SystemChangeKind{},
	}

	defs := current
	if defs == nil {
		var err error
		if defs, err = definitionsOf(q.ctx(), q.db(), definitionFilter{current: true}); err != nil {
			return out, err
		}
	}

	// A Fleet edit on its way to the repository names its publish task when the head is the revision it sent; a
	// store-only definition a live experiment names is that experiment's Lab variant.
	published, err := q.db().DefPublishTasks(q.ctx())
	if err != nil {
		return out, err
	}
	sent := map[string]db.DefPublishTasksRow{}
	for _, p := range published {
		sent[p.Kind+"/"+p.ID] = p
	}
	prs, err := publishTaskPRs(q, published)
	if err != nil {
		return out, err
	}
	experiments := map[string]bool{}
	for _, d := range defs {
		st := d.state()
		if st == contract.DefinitionStateSynced || d.Kind == "project" {
			continue
		}
		var runs int64
		switch d.Kind {
		case "agent":
			runs = usage["agent"][d.ID].Runs
		case "pipeline":
			runs = usage["pipeline"][d.ID].Runs
		}
		item := contract.SystemChangeDefinition{
			Kind:    contract.DefinitionKind(d.Kind),
			Id:      d.ID,
			Name:    definitionName(d),
			Path:    d.Path,
			State:   st,
			Head:    d.Head,
			RepoSha: d.RepoSha,
			Runs30d: runs,
			At:      ptr(d.At.UTC().Format(time.RFC3339)),
		}
		if d.Message != "" {
			item.Summary = ptr(d.Message)
		}
		// A conflict (repository-changed) needs a person even while an earlier publish is open: the repository changed
		// under it.
		if p, ok := sent[d.Kind+"/"+d.ID]; ok && int64(p.Revision) == d.Head && st != contract.DefinitionStateRepositoryChanged {
			item.Publish = &contract.SystemChangePublish{TaskId: p.TaskID, Code: p.Code, State: p.State}
			if pr, ok := prs[p.TaskID]; ok {
				item.Publish.Pr = &contract.SystemChangePublishPR{Number: pr.Number, Url: pr.Url, Stage: pr.Stage, Since: pr.Since}
			}
		}
		if st == contract.DefinitionStateStoreOnly {
			exps, err := q.db().DefExperimentsNaming(q.ctx(), d.ID)
			if err != nil {
				return out, err
			}
			if len(exps) > 0 {
				item.Experiment = &exps[0].ID
				if !experiments[exps[0].ID] {
					experiments[exps[0].ID] = true
					out.Experiments = append(out.Experiments, contract.SystemChangeExperiment{Id: exps[0].ID, Title: exps[0].Title, State: exps[0].State, At: exps[0].CreatedAt.UTC().Format(time.RFC3339)})
				}
			}
		}
		switch st {
		case contract.DefinitionStateRepositoryChanged, contract.DefinitionStateRepositoryDeleted:
			out.RepositoryChanged = append(out.RepositoryChanged, item)
		case contract.DefinitionStateAhead:
			out.Ahead = append(out.Ahead, item)
		case contract.DefinitionStateStoreOnly:
			out.StoreOnly = append(out.StoreOnly, item)
		}
	}

	sort.Slice(out.StoreOnly, func(i, j int) bool {
		if out.StoreOnly[i].Runs30d != out.StoreOnly[j].Runs30d {
			return out.StoreOnly[i].Runs30d > out.StoreOnly[j].Runs30d
		}
		return out.StoreOnly[i].Id < out.StoreOnly[j].Id
	})

	hosts, err := hostsListReply(q, false)
	if err != nil {
		return out, err
	}
	out.Versions = systemVersionsFromHosts(hosts)
	out.Components = systemComponents(hosts.Hosts)
	for _, h := range hosts.Hosts {
		for _, hv := range h.HarnessVersions {
			if hv.Expected == "" {
				continue
			}
			behind := hv.Observed == nil || *hv.Observed != hv.Expected
			out.HarnessDrift = append(out.HarnessDrift, contract.SystemChangeHostHarness{
				HostId:   h.Id,
				Harness:  hv.Harness,
				Expected: hv.Expected,
				Observed: hv.Observed,
				Behind:   behind,
			})
		}
	}
	sort.Slice(out.HarnessDrift, func(i, j int) bool {
		if out.HarnessDrift[i].Behind != out.HarnessDrift[j].Behind {
			return out.HarnessDrift[i].Behind
		}
		if out.HarnessDrift[i].HostId != out.HarnessDrift[j].HostId {
			return out.HarnessDrift[i].HostId < out.HarnessDrift[j].HostId
		}
		return out.HarnessDrift[i].Harness < out.HarnessDrift[j].Harness
	})
	out.AgentEnvironment, err = agentEnvironment(q)
	return out, err
}

// publishTaskPRs: each publish task's latest pull request (publishing_status of the tasks' projects, by task).
func publishTaskPRs(q *Request, published []db.DefPublishTasksRow) (map[string]contract.DeliveryItem, error) {
	allowed, tasks := map[string]bool{}, map[string]bool{}
	for _, p := range published {
		if p.ProjectID != "" {
			allowed[p.ProjectID], tasks[p.TaskID] = true, true
		}
	}
	out := map[string]contract.DeliveryItem{}
	if len(allowed) == 0 {
		return out, nil
	}
	_, items, _, err := publishingItems(q, &contract.PublishingStatusRequest{}, allowed, nil)
	if err != nil {
		return nil, err
	}
	for _, it := range items {
		task := deref(it.TaskId)
		if !tasks[task] {
			continue
		}
		if prev, ok := out[task]; !ok || prev.Number < it.Number {
			out[task] = it
		}
	}
	return out, nil
}
