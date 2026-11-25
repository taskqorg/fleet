package main

import (
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: project_view (pages/projects.md, Overview) answers one project's glance in one read: its open
// questions (oldest first, 3 shown), tasks in flight, latest conversations and the pipelines it uses by job, with the
// method in use. Risk: another project's work leaks in, a shared method the project never used shows as its pipeline,
// or the counts drift from the facets' own lists.
func TestProjectView(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	now := time.Now().UTC()
	for _, s := range []struct{ id, project string }{{"pjv-s1", "specq"}, {"pjv-s2", "specq"}, {"pjv-o1", "desiign"}} {
		exec(`INSERT INTO session (id, project_id, created_at) VALUES ($1, $2, $3)`, s.id, s.project, now)
	}
	exec(`INSERT INTO task (id, code, project_id, title, state) VALUES ('pjv-t1', 'T-SPC-901', 'specq', 'one', 'in_progress'),
		('pjv-t2', 'T-SPC-902', 'specq', 'two', 'completed'), ('pjv-t3', 'T-DZN-901', 'desiign', 'other', 'in_progress')`)
	for i, p := range []string{"specq", "specq", "specq", "specq", "desiign"} {
		exec(`INSERT INTO question (id, kind, project_id, text, asked_at) VALUES ($1, 'operator_decision', $2, 'Pick?', $3)`, "pjv-q"+itoa(i), p, now.Add(time.Duration(i)*time.Minute))
	}

	var v contract.ProjectViewReply
	e.opInto(t, "project_view", map[string]any{"projectId": "specq"}, e.ownerHeaders(), &v)
	if v.WaitingTotal != 4 || len(v.Waiting) != 3 || v.Waiting[0].Id != "pjv-q0" {
		t.Fatalf("waiting: the 3 oldest of the project's 4: %d %+v", v.WaitingTotal, v.Waiting)
	}
	if v.InWorkTotal != 1 || len(v.InWork) != 1 || v.InWork[0].Id != "pjv-t1" {
		t.Fatalf("in work: only the project's task in flight: %d %+v", v.InWorkTotal, v.InWork)
	}
	if v.ConversationsTotal != 2 || len(v.Conversations) != 2 {
		t.Fatalf("conversations: the project's two: %d %+v", v.ConversationsTotal, v.Conversations)
	}
	var web *contract.ProjectPipelineGroup
	for i, g := range v.Pipelines {
		if g.Capability == "web-research" {
			web = &v.Pipelines[i]
		}
		for _, p := range g.Pipelines {
			if p.MethodId != nil && *p.MethodId == "meshy-prop" {
				t.Fatalf("a shared method the project never used is not its pipeline: %+v", g)
			}
		}
	}
	if web == nil || deref(web.InUse) != "search-basic" || deref(web.InUseSource) != "shared" || len(web.Pipelines) != 1 || web.Pipelines[0].PipelineId != "search-basic" {
		t.Fatalf("web research: the shared default in use: %+v", v.Pipelines)
	}

	if code, _ := e.callJSON(t, "POST", "/api/ops/project_view", map[string]any{"projectId": "no-such"}, e.ownerHeaders()); code != 404 {
		t.Fatalf("unknown project: %d", code)
	}
}

// Requirement: a project's variants rank by mean score, then trials that succeeded, then fewer failures, then time.
func TestBetterEntry(t *testing.T) {
	one, ninety := 1.0, 0.99
	five, four := int64(300000), int64(240000)
	codex := contract.LabLeaderboardEntry{VariantLabel: "codex", ScoreMean: &ninety, SucceededTrials: 6, WallMsMedian: &five}
	gemini := contract.LabLeaderboardEntry{VariantLabel: "gemini", ScoreMean: &one, SucceededTrials: 3, FailedTrials: 3, WallMsMedian: &four}
	openai := contract.LabLeaderboardEntry{VariantLabel: "openai", ScoreMean: &ninety, SucceededTrials: 6, WallMsMedian: &four}
	cancelled := contract.LabLeaderboardEntry{VariantLabel: "claude"}
	if !betterEntry(gemini, codex) || !betterEntry(openai, codex) || betterEntry(codex, openai) || !betterEntry(codex, cancelled) || betterEntry(cancelled, codex) {
		t.Fatal("rank order")
	}
}

// Requirement: project_pipeline_view (the pipeline page) says what the pipeline does, gives its outline and whether
// its method is the project's in use. Risk: the page shows another job's method or a pipeline the project cannot see.
func TestProjectPipelineView(t *testing.T) {
	e := openTestCoordinator(t, false)
	var v contract.ProjectPipelineViewReply
	e.opInto(t, "project_pipeline_view", map[string]any{"projectId": "specq", "pipelineId": "search-basic"}, e.ownerHeaders(), &v)
	if !v.InUse || deref(v.Method) != "search-basic" || deref(v.Capability) != "web-research" || v.Description == nil || len(v.Outline) == 0 || v.Outline[0].N == nil {
		t.Fatalf("search-basic for specq: %+v", v)
	}
	var other contract.ProjectPipelineViewReply
	e.opInto(t, "project_pipeline_view", map[string]any{"projectId": "specq", "pipelineId": "doc-recognize-openai"}, e.ownerHeaders(), &other)
	if other.InUse || other.Method != nil || deref(other.Description) == "" {
		t.Fatalf("a DAM method is not specq's; the definition still says what it does: %+v", other)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/project_pipeline_view", map[string]any{"projectId": "specq", "pipelineId": "no-such"}, e.ownerHeaders()); code != 404 {
		t.Fatalf("unknown pipeline: %d", code)
	}
}
