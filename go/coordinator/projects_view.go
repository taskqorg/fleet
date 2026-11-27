package main

// Projects view (projects_view, docs/design-system/pages/projects.md, docs/designs/fleet-data-layer.md): the Projects
// screen's catalogue facts and cross-project summary in one read. The server counts per project (sessions working,
// tasks in flight, open questions) and sends only the summary's first questions and the pull requests in flight.

import (
	"slices"
	"sort"

	"fleet/contract"
	"fleet/coordinator/db"
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

	// Open questions to people (Needs you's rule, home_view): a question addressed to a bot is that bot's; per project
	// for the catalogue, and the focus's first ones for the summary.
	qs, err := questionRows(q, db.TaskQuestionsParams{State: ptr("open"), PeopleOnly: true}, nil, nil)
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

	catalogue, lines, err := projectWork(q, focus)
	if err != nil {
		return out, err
	}
	for p, n := range questions {
		c := catalogue[p]
		c.Questions = n
		catalogue[p] = c
	}
	out.Catalogue = projectCounts(catalogue)
	out.Lines = projectCounts(lines)
	// Projects with no active work still have setup state.
	heads, err := headTexts(q, "project")
	if err != nil {
		return out, err
	}
	out.Setup = map[string]contract.ProjectSetupState{}
	now := nowMs()
	hosts, _ := projectSetupHosts(q, now)
	for _, head := range heads {
		if !mayAccessProjectID(q.person, head.id) {
			continue
		}
		setup, _ := projectSetupFor(q, head.id, projectHeadSettings(head.id, head.text), hosts, now)
		out.Setup[head.id] = setup.State
	}

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

// projectWork counts catalogue and focused work together, applying the lists' filters before grouping.
func projectWork(q *Request, f rowFilter) (map[string]contract.ProjectWorkCounts, map[string]contract.ProjectWorkCounts, error) {
	catalogue := map[string]contract.ProjectWorkCounts{}
	lines := map[string]contract.ProjectWorkCounts{}
	type workCount struct {
		ProjectID *string `db:"project_id"`
		All       int64   `db:"n"`
		Focused   int64   `db:"focused"`
	}
	sql, args := sqlProjectWorkingCounts(f)
	working, err := typedRows[workCount](q, sql, args)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range working {
		p := deref(r.ProjectID)
		c, l := catalogue[p], lines[p]
		c.Working += r.All
		l.Working += r.Focused
		catalogue[p], lines[p] = c, l
	}
	sql, args = sqlProjectTaskCounts(f)
	tasks, err := typedRows[workCount](q, sql, args)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range tasks {
		p := deref(r.ProjectID)
		c, l := catalogue[p], lines[p]
		c.InFlight += r.All
		l.InFlight += r.Focused
		catalogue[p], lines[p] = c, l
	}
	return catalogue, lines, nil
}

// projectCounts: the counts of each project with any, ordered by project id (work outside projects last).
func projectCounts(by map[string]contract.ProjectWorkCounts) []contract.ProjectWorkCounts {
	ids := make([]string, 0, len(by))
	for p, c := range by {
		if c.Questions > 0 || c.Working > 0 || c.InFlight > 0 {
			ids = append(ids, p)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		return (ids[i] == "") != (ids[j] == "") && ids[j] == "" || (ids[i] == "") == (ids[j] == "") && ids[i] < ids[j]
	})
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
