package main

// Library view (library_view, docs/design-system/pages/library.md, docs/designs/fleet-data-layer.md): the Library
// tree in one read. The definition lists carry their 30-day runs without the resource rollup, which resolves
// container lineage over every execution (definition_list's 1.6 s on prod, 2026-11-26) and which the tree does not
// show; an opened definition reads definition_usage.

import (
	"fleet/contract"
)

func init() {
	op("library_view", libraryView)
}

func libraryView(q *Request, r *contract.LibraryViewRequest) (contract.LibraryViewReply, error) {
	out := contract.LibraryViewReply{Agents: []contract.DefinitionListItem{}, Pipelines: []contract.DefinitionListItem{}, Methods: []contract.DefinitionListItem{},
		Skills: []contract.SkillSummary{}, Recommendations: []contract.RecommendationChange{},
		Changes: contract.SystemChangesReply{RepositoryChanged: []contract.SystemChangeDefinition{}, Ahead: []contract.SystemChangeDefinition{}, StoreOnly: []contract.SystemChangeDefinition{},
			HarnessDrift: []contract.SystemChangeHostHarness{}, Versions: contract.SystemVersionsReply{Hosts: []contract.HostVersion{}}}}
	if err := excludeCheck(r.ExcludeProjectIds); err != nil {
		return out, err
	}
	// Bots are every person's; definitions, skills, drift and recommendations are the architect's (their own
	// operations' auth), so another person's view holds the bots only.
	bots, err := botList(q, &contract.BotListRequest{ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds})
	if err != nil {
		return out, err
	}
	out.Bots = bots.Bots
	if !architect(q) {
		return out, nil
	}
	for kind, dst := range map[contract.DefinitionKind]*[]contract.DefinitionListItem{
		contract.DefinitionKindAgent: &out.Agents, contract.DefinitionKindPipeline: &out.Pipelines, contract.DefinitionKindCatalogue: &out.Methods,
	} {
		l, err := definitionListOf(q, &contract.DefinitionListRequest{Kind: kind}, false)
		if err != nil {
			return out, err
		}
		*dst = l.Items
	}
	skills, err := skillsList(q, &contract.SkillsListRequest{})
	if err != nil {
		return out, err
	}
	out.Skills = skills.Skills
	if out.Changes, err = systemChanges(q, &contract.SystemChangesRequest{}); err != nil {
		return out, err
	}
	recs, err := recommendationChanges(q, &contract.RecommendationChangesRequest{})
	if err != nil {
		return out, err
	}
	out.Recommendations = recs.Changes
	return out, nil
}
