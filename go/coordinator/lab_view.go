package main

// Lab view (lab_view, docs/design-system/pages/laboratory.md, docs/designs/fleet-data-layer.md): the Lab screen in one
// read: the experiments, the use-case registry, the open rating questions and the last 30 days' leaderboard the idle
// screen and the tree's best scores show. An opened use case or suite reads its own lab_leaderboard.

import (
	"slices"
	"strings"

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
	board := &contract.LabLeaderboardRequest{Days: ptr(int64(30)), ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds}
	if out.Leaderboard, err = labLeaderboard(q, board); err != nil {
		return out, err
	}
	if out.UseCaseProjects, err = labViewUseCaseProjects(q, out.Experiments, out.Leaderboard.Entries); err != nil {
		return out, err
	}
	if out.ProjectNames, err = labViewProjectNames(q, out.Experiments); err != nil {
		return out, err
	}
	// The empty detail pane's history (docs/designs/fleet-history.md): Lab's newest entries, the full list on History.
	lab := contract.HistorySection("lab")
	h, err := historyView(q, &contract.HistoryViewRequest{Section: &lab, Limit: ptr(int64(labHistoryShown)), ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds})
	out.History = h.Entries
	return out, err
}

// labViewUseCaseProjects: the use cases each experiment ran (its examples' use cases, and the leaderboard groups whose
// latest experiment it is), reduced to one project per use case by labUseCaseProjects.
func labViewUseCaseProjects(q *Request, exps []contract.LabExperimentRecord, entries []contract.LabLeaderboardEntry) (map[string]string, error) {
	ids := []string{}
	for _, x := range exps {
		for _, ex := range x.Examples {
			if a := deref(ex.ArtifactId); a != "" && !slices.Contains(ids, a) {
				ids = append(ids, a)
			}
		}
	}
	ucOf, err := labExampleUseCases(q, ids)
	if err != nil {
		return nil, err
	}
	ran := map[string][]string{} // experiment id -> use cases
	for _, x := range exps {
		for _, ex := range x.Examples {
			if uc := ucOf[deref(ex.ArtifactId)]; uc != "" && !slices.Contains(ran[x.Id], uc) {
				ran[x.Id] = append(ran[x.Id], uc)
			}
		}
	}
	for _, e := range entries {
		if !slices.Contains(ran[e.LatestExperimentId], e.UseCaseId) {
			ran[e.LatestExperimentId] = append(ran[e.LatestExperimentId], e.UseCaseId)
		}
	}
	return labUseCaseProjects(exps, ran), nil
}

// labUseCaseProjects: a use case's project is the project of its experiments (docs/design-system/pages/laboratory.md,
// "global Lab is the same pages, grouped by project"). When its experiments are in several projects: the project the
// use case is named after (dam:document-recognition in dam), else the one with most experiments, else the newest.
func labUseCaseProjects(exps []contract.LabExperimentRecord, ran map[string][]string) map[string]string {
	type tally struct {
		n      int
		newest string
	}
	by := map[string]map[string]*tally{}
	for _, x := range exps {
		if x.ProjectId == "" {
			continue
		}
		for _, uc := range ran[x.Id] {
			if by[uc] == nil {
				by[uc] = map[string]*tally{}
			}
			t := by[uc][x.ProjectId]
			if t == nil {
				t = &tally{}
				by[uc][x.ProjectId] = t
			}
			t.n++
			t.newest = max(t.newest, x.CreatedAt)
		}
	}
	out := map[string]string{}
	for uc, ps := range by {
		if prefix, _, ok := strings.Cut(uc, ":"); ok && ps[prefix] != nil {
			out[uc] = prefix
			continue
		}
		best := ""
		for p, t := range ps {
			b := ps[best]
			if b == nil || t.n > b.n || t.n == b.n && (t.newest > b.newest || t.newest == b.newest && p < best) {
				best = p
			}
		}
		out[uc] = best
	}
	return out
}

// labViewProjectNames: the names of the experiments' projects (a project without a name reads as its id).
func labViewProjectNames(q *Request, exps []contract.LabExperimentRecord) (map[string]string, error) {
	out := map[string]string{}
	for _, x := range exps {
		if x.ProjectId != "" {
			out[x.ProjectId] = x.ProjectId
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	rows, err := definitionRows(q, "d.kind='project' AND d.deleted_at IS NULL", 0)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		if _, ok := out[r.ID]; ok {
			out[r.ID] = firstString(projectName(r.Content), r.ID)
		}
	}
	return out, nil
}
