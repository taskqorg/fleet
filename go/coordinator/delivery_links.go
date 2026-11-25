package main

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	"fleet/contract"
)

// A pull request links to a Fleet task through its branch. A session with a branch works on <prefix>/<session id>
// (executor sessionBranch), and its pull request keeps that head branch whether the worker opened it with gh or through
// project-forge, so the link needs no step from the worker and no stored row: it is read from the session each time.
// Branches of other tools (claude/<name>) name no session and link to nothing.

var sessionBranchID = regexp.MustCompile(`/([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})$`)

// branchSession: the session id a session branch names, or "".
func branchSession(branch string) string {
	if m := sessionBranchID.FindStringSubmatch(branch); m != nil {
		return m[1]
	}
	return ""
}

// branchTasks: the task of each named session that has one, by session id.
func branchTasks(q *Request, branches []string) (map[string]string, error) {
	ids := []string{}
	for _, b := range branches {
		if id := branchSession(b); id != "" && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	out := map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	rs, err := typedRows[struct {
		ID     string `db:"id"`
		TaskID string `db:"task_id"`
	}](q, `SELECT id, task_id FROM session WHERE id = ANY($1::text[]) AND task_id IS NOT NULL`, []any{ids})
	for _, r := range rs {
		out[r.ID] = r.TaskID
	}
	return out, err
}

// linkDeliveryItems names the session and task of each item from a session branch.
func linkDeliveryItems(q *Request, items []contract.DeliveryItem) error {
	branches := make([]string, 0, len(items))
	for _, it := range items {
		branches = append(branches, deref(it.Branch))
	}
	tasks, err := branchTasks(q, branches)
	if err != nil {
		return err
	}
	for i, it := range items {
		if s := branchSession(deref(it.Branch)); s != "" {
			if t, ok := tasks[s]; ok {
				items[i].SessionId, items[i].TaskId = ptr(s), ptr(t)
			}
		}
	}
	return nil
}

// taskDeliveryStages: for each of the given tasks with a pull request delivery still holds, the stage of its first one
// (the lowest number; a repository's own numbers, so the first repository by name on a tie across repositories). The
// stage is publishing_status's: a host's publishing record of the pull request wins over the GitHub read.
func taskDeliveryStages(q *Request, taskIDs []string) (map[string]contract.DeliveryStage, error) {
	type pr struct {
		repo   string
		number int64
		branch string
		stage  contract.DeliveryStage
	}
	byKey := map[string]pr{}
	deliveryMu.Lock()
	for _, r := range deliveryLast.repos {
		for _, it := range r.items {
			if b := deref(it.Branch); branchSession(b) != "" {
				byKey[strings.ToLower(it.Repo)+"#"+strconv.FormatInt(it.Number, 10)] = pr{it.Repo, it.Number, b, it.Stage}
			}
		}
	}
	deliveryMu.Unlock()
	pubMu.Lock()
	for _, rec := range pubRecords {
		key := strings.ToLower(rec.Repo) + "#" + strconv.FormatInt(rec.Number, 10)
		b := deref(rec.Branch)
		if b == "" {
			b = byKey[key].branch
		}
		stage := contract.DeliveryStage(rec.Stage)
		if !stage.Valid() {
			stage = contract.DeliveryStagePrOpen
		}
		if branchSession(b) != "" {
			byKey[key] = pr{rec.Repo, rec.Number, b, stage}
		}
	}
	pubMu.Unlock()
	out := map[string]contract.DeliveryStage{}
	if len(byKey) == 0 || len(taskIDs) == 0 {
		return out, nil
	}
	prs := make([]pr, 0, len(byKey))
	branches := make([]string, 0, len(byKey))
	for _, p := range byKey {
		prs = append(prs, p)
		branches = append(branches, p.branch)
	}
	slices.SortFunc(prs, func(a, b pr) int {
		if a.number != b.number {
			return int(a.number - b.number)
		}
		return strings.Compare(a.repo, b.repo)
	})
	tasks, err := branchTasks(q, branches)
	if err != nil {
		return nil, err
	}
	for _, p := range prs {
		t, ok := tasks[branchSession(p.branch)]
		if _, seen := out[t]; ok && !seen && slices.Contains(taskIDs, t) {
			out[t] = p.stage
		}
	}
	return out, nil
}
