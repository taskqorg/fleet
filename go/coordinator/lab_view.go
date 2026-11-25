package main

// Lab view (lab_view, docs/design-system/pages/laboratory.md, docs/designs/fleet-data-layer.md): the Lab screen in one
// read: the experiments, the use-case registry, the open rating questions and the last 30 days' leaderboard the idle
// screen and the tree's best scores show. An opened use case or suite reads its own lab_leaderboard.

import "fleet/contract"

func init() {
	op("lab_view", labView)
}

// labHistoryShown: the history entries Lab's empty pane shows.
const labHistoryShown = 8

func labView(q *Request, _ *contract.LabViewRequest) (contract.LabViewReply, error) {
	var out contract.LabViewReply
	exps, err := labExperimentsList(q, nil)
	if err != nil {
		return out, err
	}
	out.Experiments = exps.Experiments
	reg, err := labRegistry()
	if err != nil {
		return out, err
	}
	out.UseCases = reg.UseCases
	ratings, err := labRatings(q, nil, ptr("open"))
	if err != nil {
		return out, err
	}
	out.Ratings = ratings.Ratings
	if out.Leaderboard, err = labLeaderboard(q, &contract.LabLeaderboardRequest{Days: ptr(int64(30))}); err != nil {
		return out, err
	}
	// The empty detail pane's history (docs/designs/fleet-history.md): Lab's newest entries, the full list on History.
	lab := contract.HistorySection("lab")
	h, err := historyView(q, &contract.HistoryViewRequest{Section: &lab, Limit: ptr(int64(labHistoryShown))})
	out.History = h.Entries
	return out, err
}
