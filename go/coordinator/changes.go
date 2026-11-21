// System changes overview (docs/designs/fleet-system-management.md): drift across repository files, active store
// definitions, Coordinator/UI/host release versions, and pinned vs observed harness CLIs.
package main

import (
	"sort"

	"fleet/contract"
)

func init() {
	op("system_changes", systemChanges)
}

func systemChanges(q *Request, _ *contract.SystemChangesRequest) (contract.SystemChangesReply, error) {
	out := contract.SystemChangesReply{
		RepositoryChanged: []contract.SystemChangeDefinition{},
		Ahead:             []contract.SystemChangeDefinition{},
		StoreOnly:         []contract.SystemChangeDefinition{},
		HarnessDrift:      []contract.SystemChangeHostHarness{},
	}

	defs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{current: true})
	if err != nil {
		return out, err
	}
	agentWin, _, err := usageWindows(q, "agent", "", 30)
	if err != nil {
		return out, err
	}
	pipeWin, _, err := usageWindows(q, "pipeline", "", 30)
	if err != nil {
		return out, err
	}

	for _, d := range defs {
		st := d.state()
		if st == contract.DefinitionStateSynced || d.Kind == "project" {
			continue
		}
		var runs int64
		switch d.Kind {
		case "agent":
			runs = agentWin[d.ID].Runs
		case "pipeline":
			runs = pipeWin[d.ID].Runs
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
	return out, nil
}
