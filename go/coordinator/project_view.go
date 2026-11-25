package main

// Project view (project_view, docs/design-system/pages/projects.md, docs/designs/fleet-data-layer.md): one project's
// Overview in one read. Each block is the head of a facet: what waits for the person (open questions and Lab
// decisions), tasks in flight, the pipelines the project uses by job, the latest results and conversations, and
// delivery. Every block reuses the facet's own read with a small limit, so the counts match the facets.

import (
	"encoding/json"
	"slices"
	"sort"
	"time"

	"fleet/contract"
)

func init() {
	op("project_view", projectView)
}

const (
	projectViewQuestions     = 3
	projectViewTasks         = 5
	projectViewResults       = 3
	projectViewConversations = 3
	projectViewLabDays       = 90
)

func projectView(q *Request, r *contract.ProjectViewRequest) (contract.ProjectViewReply, error) {
	out := contract.ProjectViewReply{Waiting: []contract.QuestionRow{}, Decisions: []contract.ProjectDecision{}, InWork: []contract.TaskRow{},
		Pipelines: []contract.ProjectPipelineGroup{}, Results: []contract.ArtifactRow{}, Conversations: []contract.SessionRow{}, Delivery: []contract.DeliveryItem{}}
	id := r.ProjectId
	if id == "" || id == "-" {
		return out, fail(400, "projectId: a project")
	}
	pc, err := projectContext(q, id)
	if err != nil {
		return out, err
	}

	qs, err := questionsList(q, "", "", "open", "", []string{id}, nil, nil)
	if err != nil {
		return out, err
	}
	sort.SliceStable(qs, func(i, j int) bool { return textMs(qs[i].AskedAt) < textMs(qs[j].AskedAt) })
	out.WaitingTotal = int64(len(qs))
	out.Waiting = append(out.Waiting, qs[:min(len(qs), projectViewQuestions)]...)

	board, err := labLeaderboard(q, &contract.LabLeaderboardRequest{ProjectId: &id, Days: ptr(int64(projectViewLabDays))})
	if err != nil {
		return out, err
	}
	exps, err := labExperimentsList(q, nil)
	if err != nil {
		return out, err
	}
	mine := []contract.LabExperimentRecord{}
	for _, x := range exps.Experiments {
		if x.ProjectId == id {
			mine = append(mine, x)
		}
	}
	for _, x := range mine {
		if x.State != "done" || x.Decision != nil && *x.Decision != "" && *x.Decision != "pending" {
			continue
		}
		d := contract.ProjectDecision{ExperimentId: x.Id, Goal: deref(x.Goal), At: x.CreatedAt, Choices: []contract.LabLeaderboardEntry{}}
		for _, e := range board.Entries {
			if e.LatestExperimentId == x.Id {
				d.Choices = append(d.Choices, e)
			}
		}
		if len(d.Choices) == 0 { // a later experiment re-ran every variant: there is nothing of this one to choose from
			continue
		}
		sort.SliceStable(d.Choices, func(i, j int) bool { return betterEntry(d.Choices[i], d.Choices[j]) })
		out.Decisions = append(out.Decisions, d)
	}

	tr := &contract.TaskPageRequest{Filter: &contract.ListFilter{ProjectId: []string{id}, State: projectsRunningTasks()}, Limit: ptr(int64(projectViewTasks))}
	tasks, err := taskPage(q, tr, jsonBytes(tr.Filter)) // taskPage reads the filter from its text
	if err != nil {
		return out, err
	}
	out.InWork = append(out.InWork, tasks.Tasks...)
	for _, st := range tr.Filter.State { // counts are per state over the filter without its states
		out.InWorkTotal += tasks.Counts[st]
	}

	if out.Pipelines, err = projectPipelines(q, id, board.Entries, mine); err != nil {
		return out, err
	}

	arts, err := opArtifactList(q, &contract.ArtifactListRequest{ProjectId: &id, Limit: ptr(int64(projectViewResults))})
	if err != nil {
		return out, err
	}
	out.Results = append(out.Results, arts.Artifacts...)
	for _, n := range arts.Counts {
		out.ResultsTotal += n
	}

	none := "none"
	sr := &contract.SessionsListRequest{Filter: &contract.ListFilter{ProjectId: []string{id}}, Group: &none, Limit: ptr(int64(projectViewConversations))}
	sessions, err := sessionsPageOf(q, sr, jsonBytes(sr.Filter))
	if err != nil {
		return out, err
	}
	out.Conversations, out.ConversationsTotal = append(out.Conversations, sessions.Sessions...), sessions.Total
	// Talk to PM opens the project's PM conversation: the newest session on its pm role, among its latest 200.
	pm := agentView[struct {
		Settings struct {
			Roles map[string]string `json:"roles"`
		} `json:"settings"`
	}](pc).Settings.Roles["pm"]
	if pm == "" {
		pm = "pm"
	}
	pr := &contract.SessionsListRequest{Filter: &contract.ListFilter{ProjectId: []string{id}}, Group: &none, Limit: ptr(int64(200))}
	if all, err := sessionsPageOf(q, pr, jsonBytes(pr.Filter)); err == nil {
		for i, x := range all.Sessions {
			if deref(x.ProfileId) == pm {
				out.Pm = &all.Sessions[i]
				break
			}
		}
	}

	allowed := map[string]bool{id: true}
	_, items, _, err := publishingItems(q, &contract.PublishingStatusRequest{}, allowed, nil)
	if err != nil {
		return out, err
	}
	for _, it := range items {
		if slices.Contains(projectsInFlight, it.Stage) {
			out.Delivery = append(out.Delivery, it)
		}
		if it.Merged != nil && (out.LastMergedAt == nil || *it.Merged > *out.LastMergedAt) {
			out.LastMergedAt = ptr(*it.Merged)
		}
	}
	out.Merged = homeMerged(allowed, time.Time{})
	return out, nil
}

// betterEntry: whether a ranks above b on a project's sample: a higher mean score, then more trials that succeeded,
// then fewer failures, then a shorter median time.
func betterEntry(a, b contract.LabLeaderboardEntry) bool {
	if (a.ScoreMean == nil) != (b.ScoreMean == nil) {
		return a.ScoreMean != nil
	}
	if a.ScoreMean != nil && *a.ScoreMean != *b.ScoreMean {
		return *a.ScoreMean > *b.ScoreMean
	}
	if a.SucceededTrials != b.SucceededTrials {
		return a.SucceededTrials > b.SucceededTrials
	}
	if a.FailedTrials != b.FailedTrials {
		return a.FailedTrials < b.FailedTrials
	}
	if a.WallMsMedian == nil || b.WallMsMedian == nil {
		return a.WallMsMedian != nil
	}
	return *a.WallMsMedian < *b.WallMsMedian
}

// projectPipelines: the pipelines a project uses, by job. A job is a catalogue capability: its method in use for the
// project (the project's recommendation, else the shared default), the methods scoped to the project, and the
// methods whose pipelines the project's experiments ran. A pipeline the experiments ran that no method names forms a
// job of its use case. Shared methods the project neither uses nor tried stay out: they are Library's.
func projectPipelines(q *Request, id string, entries []contract.LabLeaderboardEntry, exps []contract.LabExperimentRecord) ([]contract.ProjectPipelineGroup, error) {
	c, err := catalogueBase(q, id)
	if err != nil {
		return nil, err
	}
	var cat struct {
		RecommendedMethod    *string `json:"recommendedMethod"`
		RecommendationSource *string `json:"recommendationSource"`
		Methods              []struct {
			ID, Name, Capability, Pipeline, Scope, Status, Description string
		} `json:"methods"`
	}
	json.Unmarshal(jsonBytes(c), &cat)

	type pipeStat struct {
		best   *contract.LabLeaderboardEntry
		trials int64
		lastAt string
		useCas string
	}
	stats := map[string]*pipeStat{}
	for i, e := range entries {
		s := stats[e.PipelineId]
		if s == nil {
			s = &pipeStat{}
			stats[e.PipelineId] = s
		}
		s.trials += e.Trials
		if e.LatestAt > s.lastAt {
			s.lastAt = e.LatestAt
		}
		if s.best == nil || betterEntry(e, *s.best) {
			s.best = &entries[i]
		}
		s.useCas = e.UseCaseId
	}
	groups := map[string]*contract.ProjectPipelineGroup{}
	order := []string{}
	group := func(capability string) *contract.ProjectPipelineGroup {
		g := groups[capability]
		if g == nil {
			g = &contract.ProjectPipelineGroup{Capability: capability, Pipelines: []contract.ProjectPipelineRow{}}
			groups[capability] = g
			order = append(order, capability)
		}
		return g
	}
	inUse := deref(cat.RecommendedMethod)
	// A method Lab promoted names its pipeline but no capability: it does the job of the methods naming the same pipeline.
	capOf := map[string]string{}
	for _, m := range cat.Methods {
		if m.Capability != "" && capOf[trimJSON(m.Pipeline)] == "" {
			capOf[trimJSON(m.Pipeline)] = m.Capability
		}
	}
	listed := map[string]bool{}
	for _, m := range cat.Methods {
		pipe := trimJSON(m.Pipeline)
		s := stats[pipe]
		if m.ID != inUse && m.Scope != id && s == nil {
			continue
		}
		capability := m.Capability
		if capability == "" {
			capability = capOf[pipe]
		}
		if capability == "" && s != nil {
			capability = s.useCas
		}
		g := group(capability)
		if m.ID == inUse {
			g.InUse, g.InUseSource = ptr(m.ID), cat.RecommendationSource
		}
		row := contract.ProjectPipelineRow{PipelineId: pipe, MethodId: ptr(m.ID), Name: textPtr(m.Name), Status: textPtr(m.Status), Description: textPtr(m.Description)}
		if s != nil {
			row.Best, row.Trials, row.LastAt = s.best, s.trials, textPtr(s.lastAt)
			if g.UseCaseId == nil && s.useCas != "" {
				g.UseCaseId = ptr(s.useCas)
			}
		}
		g.Pipelines = append(g.Pipelines, row)
		listed[pipe] = true
	}
	unlisted := []string{}
	for pipe := range stats {
		if !listed[pipe] {
			unlisted = append(unlisted, pipe)
		}
	}
	sort.Strings(unlisted)
	for _, pipe := range unlisted {
		s := stats[pipe]
		// A pipeline no method names joins the job whose comparison (use case) it ran in, else forms one.
		var g *contract.ProjectPipelineGroup
		for _, k := range order {
			if u := groups[k].UseCaseId; u != nil && *u == s.useCas {
				g = groups[k]
			}
		}
		if g == nil {
			g = group(s.useCas)
			g.UseCaseId = ptr(s.useCas)
		}
		g.Pipelines = append(g.Pipelines, contract.ProjectPipelineRow{PipelineId: pipe, Best: s.best, Trials: s.trials, LastAt: textPtr(s.lastAt)})
	}
	out := make([]contract.ProjectPipelineGroup, 0, len(order))
	for _, k := range order {
		g := groups[k]
		pipes := map[string]bool{}
		for _, p := range g.Pipelines {
			pipes[p.PipelineId] = true
		}
		for _, x := range exps {
			if experimentRan(x, pipes) {
				g.Experiments++
			}
		}
		sort.SliceStable(g.Pipelines, func(i, j int) bool {
			a, b := g.Pipelines[i], g.Pipelines[j]
			if ai, bi := a.MethodId != nil && g.InUse != nil && *a.MethodId == *g.InUse, b.MethodId != nil && g.InUse != nil && *b.MethodId == *g.InUse; ai != bi {
				return ai
			}
			if (a.Best == nil) != (b.Best == nil) {
				return a.Best != nil
			}
			if a.Best != nil && b.Best != nil && betterEntry(*a.Best, *b.Best) != betterEntry(*b.Best, *a.Best) {
				return betterEntry(*a.Best, *b.Best)
			}
			return a.PipelineId < b.PipelineId
		})
		out = append(out, *g)
	}
	// Jobs with a decision to make or a method in use come first; the rest keep the catalogue's order.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Experiments > 0 && out[j].Experiments == 0 })
	return out, nil
}

// experimentRan: whether an experiment's baseline or a variant ran one of the pipelines.
func experimentRan(x contract.LabExperimentRecord, pipes map[string]bool) bool {
	if pipes[x.PipelineId] {
		return true
	}
	for _, v := range x.Variants {
		if v.PipelineId != nil && pipes[*v.PipelineId] {
			return true
		}
	}
	return false
}

// trimJSON: a catalogue method's pipeline file name as its pipeline id (search-basic.json is search-basic).
func trimJSON(file string) string {
	if len(file) > 5 && file[len(file)-5:] == ".json" {
		return file[:len(file)-5]
	}
	return file
}

func textPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func init() {
	op("project_pipeline_view", projectPipelineView)
}

// projectPipelineView: the pipeline page (project_pipeline_view): what the pipeline does and its outline, its method
// and whether the project uses it, the comparison on the project's sample for its job, and the project's experiments
// that ran it.
func projectPipelineView(q *Request, r *contract.ProjectPipelineViewRequest) (contract.ProjectPipelineViewReply, error) {
	out := contract.ProjectPipelineViewReply{PipelineId: r.PipelineId, Outline: []contract.PipelineOutlineLine{}, Ranking: []contract.LabLeaderboardEntry{}, Experiments: []contract.LabExperimentRecord{}}
	id := r.ProjectId
	if id == "" || id == "-" || r.PipelineId == "" {
		return out, fail(400, "projectId and pipelineId")
	}
	if _, err := projectContext(q, id); err != nil {
		return out, err
	}
	xs, err := definitionsOf(q.ctx(), q.db(), definitionFilter{kind: "pipeline", id: r.PipelineId, live: true})
	if err != nil {
		return out, err
	}
	if len(xs) == 0 {
		return out, fail(404, "Unknown pipeline")
	}
	var d pipelineDef
	if err := json.Unmarshal(xs[0].Content, &d); err != nil {
		return out, fail(500, "Invalid pipeline")
	}
	out.Revision, out.Outline = xs[0].Revision, pipelineOutline(&d)
	out.Description, out.Reads, out.Returns = textPtr(d.Description), textPtr(d.Input), textPtr(d.Output)

	board, err := labLeaderboard(q, &contract.LabLeaderboardRequest{ProjectId: &id, Days: ptr(int64(projectViewLabDays))})
	if err != nil {
		return out, err
	}
	exps, err := labExperimentsList(q, nil)
	if err != nil {
		return out, err
	}
	mine := []contract.LabExperimentRecord{}
	for _, x := range exps.Experiments {
		if x.ProjectId == id {
			mine = append(mine, x)
		}
	}
	groups, err := projectPipelines(q, id, board.Entries, mine)
	if err != nil {
		return out, err
	}
	for _, g := range groups {
		for _, p := range g.Pipelines {
			if p.PipelineId != r.PipelineId {
				continue
			}
			out.Method, out.Capability, out.UseCaseId = p.MethodId, &g.Capability, g.UseCaseId
			out.InUse = p.MethodId != nil && g.InUse != nil && *p.MethodId == *g.InUse
			if out.Description == nil {
				out.Description = p.Description
			}
		}
	}
	for _, e := range board.Entries {
		if out.UseCaseId != nil && e.UseCaseId == *out.UseCaseId || out.UseCaseId == nil && e.PipelineId == r.PipelineId {
			out.Ranking = append(out.Ranking, e)
		}
	}
	sort.SliceStable(out.Ranking, func(i, j int) bool { return betterEntry(out.Ranking[i], out.Ranking[j]) })
	for _, x := range mine {
		if experimentRan(x, map[string]bool{r.PipelineId: true}) {
			out.Experiments = append(out.Experiments, x)
		}
	}
	sort.SliceStable(out.Experiments, func(i, j int) bool { return out.Experiments[i].CreatedAt > out.Experiments[j].CreatedAt })
	return out, nil
}
