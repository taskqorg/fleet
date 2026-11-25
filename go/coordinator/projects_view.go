package main

// Projects view (projects_view, docs/design-system/pages/projects.md, docs/designs/fleet-data-layer.md): the Projects
// screen's catalogue facts and cross-project summary in one read. The server counts per project (sessions working,
// tasks in flight, open questions) and sends only the summary's first questions and the pull requests in flight.

import (
	"slices"
	"sort"

	"fleet/contract"
)

func init() {
	op("projects_view", projectsView)
}

// projectsViewWaiting: the open questions the summary shows as cards.
const projectsViewWaiting = 3

// projectsInFlight: the delivery stages of a pull request in flight (open, in CI, failed, in the merge queue, releasing,
// installing).
var projectsInFlight = []contract.DeliveryStage{contract.DeliveryStagePrOpen, contract.DeliveryStagePrCi, contract.DeliveryStagePrFailed,
	contract.DeliveryStageMqWait, contract.DeliveryStageMqCi, contract.DeliveryStageMqFailed, contract.DeliveryStageReleasing, contract.DeliveryStageInstalling}

// projectsRunningTasks: the task states of the Running system view (contracts/vocabulary.json), the tasks in flight.
func projectsRunningTasks() []string {
	for _, v := range contract.Vocabulary.SystemViews["tasks"] {
		if v.Name == "Running" {
			return v.State
		}
	}
	return []string{"in_progress", "waiting"}
}

func projectsView(q *Request, r *contract.ProjectsViewRequest) (contract.ProjectsViewReply, error) {
	out := contract.ProjectsViewReply{Catalogue: []contract.ProjectWorkCounts{}, Waiting: []contract.QuestionRow{}, Lines: []contract.ProjectWorkCounts{}, Delivery: []contract.DeliveryItem{}}
	if err := excludeCheck(r.ExcludeProjectIds); err != nil {
		return out, err
	}
	if slices.Contains(r.ProjectIds, "") {
		return out, fail(400, "projectIds: an empty id; '-' is work outside projects")
	}
	focus := rowFilter{}
	if len(r.ProjectIds) > 0 {
		focus.HasProjects = true
		focus.Projects, focus.ProjectsNull = projectSplit(r.ProjectIds)
	}
	if len(r.ExcludeProjectIds) > 0 {
		focus.HasExclude, focus.Exclude = true, r.ExcludeProjectIds
	}

	// Open questions: per project for the catalogue, and the focus's first ones for the summary.
	qs, err := questionsList(q, "", "", "open", "", nil, nil, nil)
	if err != nil {
		return out, err
	}
	questions := map[string]int64{}
	inFocus := projectFocus(r.ProjectIds, r.ExcludeProjectIds)
	sort.SliceStable(qs, func(i, j int) bool { return textMs(qs[i].AskedAt) < textMs(qs[j].AskedAt) })
	for _, x := range qs {
		if x.ProjectId != nil {
			questions[*x.ProjectId]++
		}
		if inFocus(x.ProjectId) {
			out.WaitingTotal++
			if len(out.Waiting) < projectsViewWaiting {
				out.Waiting = append(out.Waiting, x)
			}
		}
	}

	catalogue, err := projectWork(q, rowFilter{})
	if err != nil {
		return out, err
	}
	for p, n := range questions {
		c := catalogue[p]
		c.Questions = n
		catalogue[p] = c
	}
	out.Catalogue = projectCounts(catalogue)
	lines, err := projectWork(q, focus)
	if err != nil {
		return out, err
	}
	out.Lines = projectCounts(lines)

	// Pull requests in flight in the focus: publishing_status's items, without the projects' capabilities.
	_, allowed, err := publishingProjectFilter(q, nil, r.ProjectIds, r.ExcludeProjectIds)
	if err != nil {
		return out, err
	}
	_, items, _, err := publishingItems(q, &contract.PublishingStatusRequest{}, allowed, nil)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		if slices.Contains(projectsInFlight, it.Stage) {
			out.Delivery = append(out.Delivery, it)
		}
	}
	return out, nil
}

// projectWork: per project ("" for work outside projects) of the filter, the sessions working (preparing or running)
// and the tasks in flight, from the lists' own group counts.
func projectWork(q *Request, f rowFilter) (map[string]contract.ProjectWorkCounts, error) {
	out := map[string]contract.ProjectWorkCounts{}
	key := func(g string) string {
		if g == "~" {
			return ""
		}
		return g
	}
	sf := f
	sf.States = []string{"preparing", "running"}
	o, err := sessionOrderOf(ptr("project"), nil)
	if err != nil {
		return nil, err
	}
	_, sg, err := sessionSummary(q, sf, o)
	if err != nil {
		return nil, err
	}
	for _, g := range sg {
		c := out[key(g.Key)]
		c.Working += g.Count
		out[key(g.Key)] = c
	}
	tf := f
	tf.States = projectsRunningTasks()
	_, tg, err := taskSummary(q, tf, ptr("project"), false)
	if err != nil {
		return nil, err
	}
	for _, g := range tg {
		c := out[key(g.Key)]
		c.InFlight += g.Count
		out[key(g.Key)] = c
	}
	return out, nil
}

// projectCounts: the counts of each project with any, ordered by project id (work outside projects last).
func projectCounts(by map[string]contract.ProjectWorkCounts) []contract.ProjectWorkCounts {
	ids := make([]string, 0, len(by))
	for p, c := range by {
		if c.Questions > 0 || c.Working > 0 || c.InFlight > 0 {
			ids = append(ids, p)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return (ids[i] == "") != (ids[j] == "") && ids[j] == "" || (ids[i] == "") == (ids[j] == "") && ids[i] < ids[j] })
	out := make([]contract.ProjectWorkCounts, 0, len(ids))
	for _, p := range ids {
		c := by[p]
		if p != "" {
			c.ProjectId = ptr(p)
		}
		out = append(out, c)
	}
	return out
}

// projectFocus: whether a project (nil: work outside projects, '-') is in projectIds and not in excludeProjectIds.
func projectFocus(projectIds, excludeProjectIds []string) func(*string) bool {
	return func(p *string) bool {
		id := "-"
		if p != nil && *p != "" {
			id = *p
		}
		if len(projectIds) > 0 && !slices.Contains(projectIds, id) {
			return false
		}
		return !slices.Contains(excludeProjectIds, id)
	}
}
