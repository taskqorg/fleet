package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// A declared design task keeps a writer queued even before its first offer.
type designBuildWait struct{ reason string }

func (w *designBuildWait) Error() string { return w.reason }

func designImpactOf(requirements json.RawMessage) string {
	var r struct {
		VisualImpact string `json:"visualImpact"`
	}
	_ = json.Unmarshal(requirements, &r)
	return r.VisualImpact
}

func needsDesignSelection(t db.Task) bool {
	impact := designImpactOf(t.CompletionRequirements)
	return deref(t.ProjectID) == "fleet" && (impact == "visual" || impact == "unknown")
}

func declareDesignImpact(q *Request, id, impact string) error {
	if err := q.db().TaskLifecycleLock(q.ctx(), id); err != nil {
		return err
	}
	task, err := taskLoad(q, id)
	if err != nil {
		return err
	}
	if deref(task.ProjectID) != "fleet" {
		return fail(400, "Design impact is a Fleet task declaration")
	}
	if !jsonIsObject(task.CompletionRequirements) {
		return fail(409, "Task completion requirements must be an object to declare design impact")
	}
	if designImpactOf(task.CompletionRequirements) == impact {
		return nil
	}
	switch task.State {
	case db.TaskStateCompleted, db.TaskStateCancelled, db.TaskStateFailed:
		return fail(409, "A terminal task cannot change its design declaration")
	}
	if needsDesignSelection(task) && impact == "nonvisual" {
		return fail(409, "An enrolled design task cannot be declared nonvisual")
	}
	started, err := q.db().TaskHasPriorDesignWriter(q.ctx(), &id)
	if err != nil {
		return err
	}
	if started {
		return fail(409, "Declare design impact before the task's first implementation writer")
	}
	if err := q.db().TaskDeclareDesignImpact(q.ctx(), db.TaskDeclareDesignImpactParams{TaskID: id, Impact: impact}); err != nil {
		return err
	}
	return wakeTask(q, id)
}

func activeDesignOffer(q *Request, task string) (db.Question, error) {
	if task == "" {
		return db.Question{}, nil
	}
	// Fence both intake enrollment and the first offer against dispatch.
	if err := q.db().TaskLifecycleLock(q.ctx(), task); err != nil {
		return db.Question{}, err
	}
	t, err := taskLoad(q, task)
	if err != nil || !needsDesignSelection(t) {
		return db.Question{}, err
	}
	row, err := q.db().LatestDesignOffer(q.ctx(), &task)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Question{}, &designBuildWait{"Waiting for the first sealed design offer"}
	}
	return row, err
}

func selectedBuildSource(q *Request, row db.Question, repo string) (*contract.DesignReviewOption, error) {
	s, err := designRecord(row)
	if err != nil {
		return nil, err
	}
	if s == nil || row.State != db.QuestionStateAnswered || row.AnsweredBy == nil || deref(row.AnsweredBy) != deref(row.AddressedTo) || deref(row.Answer) == designRework {
		return nil, &designBuildWait{"Waiting for the Fleet owner's design selection"}
	}
	if repo != s.Request.Repository {
		return nil, &designBuildWait{"The build repository differs from the selected design"}
	}
	digest, err := designDigest(q, &s.Request, deref(row.ProjectID), deref(row.TaskID), deref(row.ExecutionID), true, false)
	if err != nil {
		return nil, err
	}
	if digest != s.Digest {
		return nil, &designBuildWait{"Selected design evidence has changed"}
	}
	for _, o := range s.Request.Options {
		if o.Key == deref(row.Answer) {
			return &o, nil
		}
	}
	return nil, &designBuildWait{"No retained source matches the selected option"}
}

func designWriter(row db.Question) (string, error) {
	var d struct {
		Execution string `json:"designBuildExecution"`
	}
	err := json.Unmarshal(row.Data, &d)
	return d.Execution, err
}

// claimDesignBuild runs in the transaction that hands a start to a host. A retry of the same start reuses its claim.
func claimDesignBuild(q *Request, task, id, repo string) ([]contract.InputArtifact, error) {
	row, err := activeDesignOffer(q, task)
	if err != nil || row.ID == "" {
		return nil, err
	}
	selected, err := selectedBuildSource(q, row, repo)
	if err != nil {
		return nil, err
	}
	writer, err := designWriter(row)
	if err != nil {
		return nil, err
	}
	if writer != "" && writer != id {
		previous, err := getExecution(q, writer)
		if err != nil {
			return nil, err
		}
		if !previous.terminal() {
			return nil, &designBuildWait{"Another execution is implementing this design"}
		}
	}
	if writer != id {
		if err := q.db().AssignDesignWriter(q.ctx(), db.AssignDesignWriterParams{QuestionID: row.ID, ExecutionID: id}); err != nil {
			return nil, err
		}
	}
	prefix := "design-review/" + row.ID + "/"
	inputs := []contract.InputArtifact{{ArtifactId: selected.SourceArtifactId, Path: prefix + "selected-source"}}
	for i, c := range selected.Cases {
		inputs = append(inputs, contract.InputArtifact{ArtifactId: c.RecipeArtifactId, Path: prefix + fmt.Sprintf("case-%d-recipe.json", i+1)})
	}
	return inputs, nil
}

func attachDesignSources(q *Request, task string, e *dispatchExecution, host string, p *contract.StartPayload) error {
	if task == "" {
		return nil
	}
	var snap projectSnapshot
	var settings runSettings
	_ = lenientDecode(e.ProjectSnapshot, &snap)
	_ = lenientDecode(snap.Settings, &settings)
	ref, _ := forgeSelectedRepoRef(settings, host)
	inputs, err := claimDesignBuild(q, task, e.ID, ref.Host+"/"+ref.Slug)
	if err != nil || len(inputs) == 0 {
		return err
	}
	for _, old := range p.Inputs {
		if strings.HasPrefix(old.Path, "design-review/") || old.Path == "design-review" {
			return fail(409, "Input path collides with the selected design")
		}
	}
	p.Inputs = append(p.Inputs, inputs...)
	return nil
}

// A replacement or withdrawal invalidates a writer's former selection before a new publishing command is issued.
func checkDesignPublication(q *Request, task, id, repo string) error {
	row, err := activeDesignOffer(q, task)
	if err == nil && row.ID == "" {
		return nil
	}
	if err == nil {
		err = activeDesignTask(q, task, deref(row.ProjectID))
	}
	if err == nil {
		_, err = selectedBuildSource(q, row, repo)
	}
	var pending *designBuildWait
	if errors.As(err, &pending) {
		return fail(409, pending.reason)
	}
	if err != nil {
		return err
	}
	writer, err := designWriter(row)
	if err != nil {
		return err
	}
	if id == "" || id != writer {
		return fail(409, "This execution has no build admission for the active design")
	}
	return nil
}

// restrictFleetDesignStart narrows the preview's payload without changing shared project grants.
func restrictFleetDesignStart(p *contract.StartPayload, cfg rawObject, host string) error {
	if p.Project == nil || cfg == nil || deref(p.Harness) != "codex" {
		return fail(403, "Fleet design requires a sandboxed Codex project checkout")
	}
	var worktree contract.ProjectWorktree
	_ = json.Unmarshal(p.Project["worktree"], &worktree)
	p.Project["worktree"] = jsonBytes(contract.ProjectWorktree{
		BaseRef: worktree.BaseRef, StartFromOrigin: worktree.StartFromOrigin, EnvMode: ptr("worktree"),
	})
	var execution contract.ProjectExecution
	_ = json.Unmarshal(p.Project["execution"], &execution)
	tools := []string{}
	for _, tool := range execution.Tools {
		switch tool {
		case "node", "pnpm", "browser":
			tools = append(tools, tool)
		}
	}
	p.Project["execution"] = jsonBytes(contract.ProjectExecution{Forge: ptr(false), Tools: tools, Profiles: []string{designProfileID}})
	var repos []contract.ProjectRepo
	var environments []contract.ProjectEnvironment
	if json.Unmarshal(p.Project["repos"], &repos) != nil || json.Unmarshal(p.Project["environments"], &environments) != nil {
		return fail(409, "Fleet design requires a configured Fleet repository and checkout")
	}
	var previewRepo *contract.ProjectRepo
	for _, repo := range repos {
		ref, ok := contract.ParseRepoRemote(deref(repo.Remote), deref(repo.Forge), deref(repo.Host))
		if ok && ref.Host+"/"+ref.Slug == fleetDesignRepository {
			previewRepo = &repo
			break
		}
	}
	if previewRepo == nil {
		return fail(403, "Fleet design requires the configured Fleet repository")
	}
	previewRepo.Connection = nil
	checkouts := []contract.ProjectEnvironment{}
	for _, env := range environments {
		if deref(env.HostId) == host && deref(env.CheckoutPath) != "" && (env.Repo == nil || deref(env.Repo) == deref(previewRepo.Id)) {
			checkouts = append(checkouts, contract.ProjectEnvironment{HostId: env.HostId, CheckoutPath: env.CheckoutPath, Repo: previewRepo.Id})
		}
	}
	if len(checkouts) != 1 {
		return fail(409, "Fleet design requires one configured Fleet checkout on this host")
	}
	// The executor stages pinned context from local Git objects before starting Codex.
	// Keep those named bindings after the primary checkout, without Git connections.
	previewRepos := []contract.ProjectRepo{*previewRepo}
	if raw := p.Project["contextSources"]; jsonGiven(raw) {
		sources, err := contract.ParseContextSources(raw)
		if err != nil {
			return fail(409, "Fleet design requires valid pinned context sources")
		}
		for _, source := range sources {
			if source.Repo == deref(previewRepo.Id) {
				continue
			}
			for _, repo := range repos {
				if deref(repo.Id) == source.Repo {
					repo.Connection = nil
					previewRepos = append(previewRepos, repo)
				}
			}
			for _, env := range environments {
				if deref(env.HostId) == host && deref(env.Repo) == source.Repo && deref(env.CheckoutPath) != "" {
					checkouts = append(checkouts, contract.ProjectEnvironment{HostId: env.HostId, CheckoutPath: env.CheckoutPath, Repo: env.Repo})
				}
			}
		}
	}
	p.Project["repos"], p.Project["environments"] = jsonBytes(previewRepos), jsonBytes(checkouts)
	p.WorkingDirectory = nil
	p.Setup, p.Branch, p.GitNetwork = ptr("skip"), ptr(false), ptr(false)
	cfg["settings"] = jsonBytes(struct {
		Sandbox bool `json:"sandbox"`
	}{true})
	cfg["collaborationTools"] = jsonBytes(false)
	cfg["mcpServers"] = jsonBytes([]contract.ProfileMcpGrant{})
	cfg["fleetTools"] = jsonBytes(true)
	delete(cfg, "delegationProfiles")
	return nil
}
