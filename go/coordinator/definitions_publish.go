package main

import (
	"fmt"
	"sort"
	"strings"

	"fleet/contract"
	"fleet/coordinator/db"
)

// definition_publish (fleet-definition-editors.md, One home per kind): a Fleet edit of an agent, a pipeline or the
// catalogue goes to the repository by a pull request. One Fleet task in the project whose repository is Fleet's own
// carries the files; the queue starts its worker (taskAutoStart), the worker opens the pull request under the project
// rules, and the task's pull request is the definition's progress (publishing_status {taskId}). When a release ships
// the same content, the definition is synced and follows the repository again. A save publishes by itself
// (definitionSave, definitionAddSkill, Lab promote); a definition made in Fleet is published by this operation, since
// only its author knows whether it is a product definition or Lab scratch.
func init() {
	op("definition_publish", func(q *Request, r *contract.DefinitionPublishRequest) (contract.DefinitionPublishReply, error) {
		if len(r.Keys) == 0 {
			return contract.DefinitionPublishReply{Items: []contract.DefinitionBulkResult{}}, fail(400, "keys must not be empty")
		}
		return publishDefinitions(q, r.Keys, r.Preview)
	})
}

// publishDefinitions: one task for the keys that are a Fleet edit not yet sent (ahead or store-only, not a live Lab
// variant, head revision not published before); the rest are skipped with the reason.
func publishDefinitions(q *Request, keys []contract.DefinitionKey, preview bool) (contract.DefinitionPublishReply, error) {
	out := contract.DefinitionPublishReply{Items: []contract.DefinitionBulkResult{}}
	sent, err := publishedRevisions(q)
	if err != nil {
		return out, err
	}
	var heads []*definitionRow
	skip := func(k contract.DefinitionKey, reason string) {
		out.Items = append(out.Items, contract.DefinitionBulkResult{Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusSkipped, Reason: &reason})
	}
	for _, k := range keys {
		if err := definitionKind(k.Kind); err != nil {
			return out, err
		}
		if k.Kind == contract.DefinitionKindProject {
			skip(k, "a project lives in Fleet")
			continue
		}
		h, err := headOf(q, string(k.Kind), k.Id, !preview)
		if err != nil {
			return out, err
		}
		if h == nil {
			out.Items = append(out.Items, contract.DefinitionBulkResult{Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusError, Reason: ptr("Unknown definition")})
			continue
		}
		// A repository-changed head is a conflict: the person keeps theirs (a save with repoSha, then it is ahead) or
		// takes the repository's; publishing it would undo the repository's change.
		if st := h.state(); st != contract.DefinitionStateAhead && st != contract.DefinitionStateStoreOnly {
			skip(k, "state is "+string(st))
			continue
		}
		if sent[string(k.Kind)+"/"+k.Id] >= h.Head {
			skip(k, fmt.Sprintf("r%d is already on its way", h.Head))
			continue
		}
		if exps, err := q.db().DefExperimentsNaming(q.ctx(), k.Id); err != nil {
			return out, err
		} else if len(exps) > 0 && h.state() == contract.DefinitionStateStoreOnly {
			skip(k, "a Lab variant of experiment "+exps[0])
			continue
		}
		heads = append(heads, h)
		out.Items = append(out.Items, contract.DefinitionBulkResult{Kind: k.Kind, Id: k.Id, Status: contract.BulkOpStatusWouldApply, Revision: &h.Head})
	}
	if preview || len(heads) == 0 {
		return out, nil
	}
	project := definitionsProject(q)
	if project == "" {
		return out, fail(409, "No project has Fleet's repository ("+githubPublicRepo+") and a worker role")
	}
	title, outcome := publishBrief(heads)
	task, err := createTask(q, newTask{ProjectID: &project, Title: title, ExpectedOutcome: &outcome, CreatedBy: actor(q, "operator"), DirectedBy: director(q)})
	if err != nil {
		return out, err
	}
	for _, h := range heads {
		if err := q.db().DefPublishInsert(q.ctx(), db.DefPublishInsertParams{Kind: h.Kind, ID: h.ID, Revision: int32(h.Head), TaskID: task}); err != nil {
			return out, err
		}
	}
	for i := range out.Items {
		if out.Items[i].Status == contract.BulkOpStatusWouldApply {
			out.Items[i].Status = contract.BulkOpStatusApplied
		}
	}
	out.TaskId = &task
	return out, nil
}

// publishAfterSave: a save of an agent, a pipeline or the catalogue sends its new head to the repository when it is
// ahead. A synced head is the repository's already; a store-only one is published by its author (definition_publish);
// a repository-changed one waits for the person to keep theirs or take the repository's.
func publishAfterSave(q *Request, keys ...contract.DefinitionKey) error {
	send := []contract.DefinitionKey{}
	for _, k := range keys {
		if k.Kind == contract.DefinitionKindProject {
			continue
		}
		h, err := headOf(q, string(k.Kind), k.Id, false)
		if err != nil {
			return err
		}
		if h != nil && h.state() == contract.DefinitionStateAhead {
			send = append(send, k)
		}
	}
	if len(send) == 0 || definitionsProject(q) == "" {
		return nil
	}
	_, err := publishDefinitions(q, send, false)
	return err
}

// publishedRevisions: the latest published revision per kind/id.
func publishedRevisions(q *Request) (map[string]int64, error) {
	rows, err := q.db().DefPublishTasks(q.ctx())
	out := map[string]int64{}
	for _, r := range rows {
		out[r.Kind+"/"+r.ID] = int64(r.Revision)
	}
	return out, err
}

// definitionsProject: the first project (by id) whose repositories include Fleet's own and that maps a worker profile.
func definitionsProject(q *Request) string {
	ids, err := projectIDs(q)
	if err != nil {
		return ""
	}
	sort.Strings(ids)
	for _, id := range ids {
		if !profileForRole(projectSettingsOf(q, id), "worker") {
			continue
		}
		for _, spec := range projectRepoSpecs(q, id) {
			if m := githubRemote.FindStringSubmatch(spec.Remote); m != nil && strings.EqualFold(m[1]+"/"+m[2], githubPublicRepo) {
				return id
			}
		}
	}
	return ""
}

// publishBrief: the task's title and its worker's brief, with every file as Fleet holds it.
func publishBrief(heads []*definitionRow) (string, string) {
	names := []string{}
	for _, h := range heads {
		names = append(names, h.Kind+"/"+h.ID)
	}
	title := "Publish Fleet definitions to the repository: " + strings.Join(names, ", ")
	if len([]rune(title)) > 200 {
		title = fmt.Sprintf("Publish %d Fleet definitions to the repository", len(heads))
	}
	var b strings.Builder
	b.WriteString("These definitions were changed in Fleet and already run there. Their home is the repository (docs/designs/fleet-definition-editors.md, One home per kind). ")
	b.WriteString("Write each file below at its path exactly as given (canonical JSON), on a branch from main, and deliver it as one pull request under the project rules: review, CI, auto-merge. ")
	b.WriteString("Do not change anything else. If a file on main differs from the repository version named below in a way this change does not cover, stop and report the conflict instead of merging it.\n")
	for _, h := range heads {
		base := "none (new file)"
		if h.RepoSha != nil {
			base = "git blob " + *h.RepoSha
		}
		fmt.Fprintf(&b, "\n%s, Fleet revision %d, repository version %s:\n```json %s\n%s```\n", h.Path, h.Head, base, h.Path, string(h.Content))
	}
	return title, b.String()
}

// publishPromoted: a Lab promote sends the catalogue, and every store-only agent or pipeline the promoted method names,
// to the repository in one task.
func publishPromoted(q *Request, method []byte) error {
	if definitionsProject(q) == "" {
		return nil
	}
	names := map[string]bool{}
	stringsIn(method, names)
	keys := []contract.DefinitionKey{{Kind: contract.DefinitionKindCatalogue, Id: "catalogue"}}
	ids := []string{}
	for id := range names {
		if idPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		for _, kind := range []contract.DefinitionKind{contract.DefinitionKindAgent, contract.DefinitionKindPipeline} {
			if h, err := headOf(q, string(kind), id, false); err != nil {
				return err
			} else if h != nil && h.state() == contract.DefinitionStateStoreOnly {
				keys = append(keys, contract.DefinitionKey{Kind: kind, Id: id})
			}
		}
	}
	_, err := publishDefinitions(q, keys, false)
	return err
}
