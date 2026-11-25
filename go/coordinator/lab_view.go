package main

// Lab view (lab_view, docs/design-system/pages/laboratory.md, docs/designs/fleet-data-layer.md): the Lab screen in one
// read: the experiments, the use-case registry, the open rating questions and the last 30 days' leaderboard the idle
// screen and the tree's best scores show. An opened use case or suite reads its own lab_leaderboard.

import (
	"slices"

	"fleet/contract"
)

func init() {
	op("lab_view", labView)
}

// labHistoryShown: the history entries Lab's empty pane shows.
const labHistoryShown = 8

func labView(q *Request, r *contract.LabViewRequest) (contract.LabViewReply, error) {
	out := contract.LabViewReply{Experiments: []contract.LabExperimentRecord{}, Ratings: []contract.LabRating{}}
	if err := excludeCheck(r.ExcludeProjectIds); err != nil {
		return out, err
	}
	if slices.Contains(r.ProjectIds, "") {
		return out, fail(400, "projectIds: an empty id; '-' is work outside projects")
	}
	exps, err := labExperimentsList(q, nil)
	if err != nil {
		return out, err
	}
	// The header focus, as every cross-project screen applies it: experiments of the focus, without ignored projects.
	inFocus := func(p string) bool {
		if p == "" {
			p = "-"
		}
		return (len(r.ProjectIds) == 0 || slices.Contains(r.ProjectIds, p)) && !slices.Contains(r.ExcludeProjectIds, p)
	}
	kept := map[string]bool{}
	for _, x := range exps.Experiments {
		if inFocus(x.ProjectId) {
			out.Experiments = append(out.Experiments, x)
			kept[x.Id] = true
		}
	}
	reg, err := labRegistry()
	if err != nil {
		return out, err
	}
	out.UseCases = reg.UseCases
	ratings, err := labRatings(q, nil, ptr("open"))
	if err != nil {
		return out, err
	}
	for _, x := range ratings.Ratings {
		if x.ExperimentId == nil || kept[*x.ExperimentId] {
			out.Ratings = append(out.Ratings, x)
		}
	}
	// One project in focus: its leaderboard; otherwise every project's (the board has no project list filter).
	board := &contract.LabLeaderboardRequest{Days: ptr(int64(30))}
	if len(r.ProjectIds) == 1 && r.ProjectIds[0] != "-" {
		board.ProjectId = &r.ProjectIds[0]
	}
	if out.Leaderboard, err = labLeaderboard(q, board); err != nil {
		return out, err
	}
	// The empty detail pane's history (docs/designs/fleet-history.md): Lab's newest entries, the full list on History.
	lab := contract.HistorySection("lab")
	h, err := historyView(q, &contract.HistoryViewRequest{Section: &lab, Limit: ptr(int64(labHistoryShown)), ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds})
	out.History = h.Entries
	return out, err
}
