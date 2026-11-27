package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

// Retain the relevant facts from the existing bounded delivery reads. A
// missing target is stale only after a complete open-list read.
type deliveryEvidence struct {
	Changes          map[int64]deliveryRevision `json:"changes,omitempty"`
	ReadAt           time.Time                  `json:"readAt,omitzero"`
	OpenListComplete bool                       `json:"openListComplete,omitempty"`
	Runs             []ghRun                    `json:"runs,omitempty"`
}

type deliveryRevision struct {
	Branch   string        `json:"branch"`
	Head     string        `json:"head"`
	Closed   bool          `json:"closed,omitempty"`
	Pipeline *glMRPipeline `json:"pipeline,omitempty"`
}

func githubDeliveryFacts(g ghRepo, readAt time.Time) deliveryEvidence {
	e := deliveryEvidence{ReadAt: readAt, Changes: make(map[int64]deliveryRevision, len(g.open)+len(g.closed))}
	for _, p := range g.closed {
		e.Changes[p.Number] = deliveryRevision{Branch: p.Head.Ref, Head: p.Head.SHA, Closed: true}
	}
	for _, p := range g.open {
		e.Changes[p.Number] = deliveryRevision{Branch: p.Head.Ref, Head: p.Head.SHA}
	}
	for _, list := range [][]ghRun{g.runs, g.finished} {
		for _, r := range list {
			if r.Event == "pull_request" && r.ID != 0 {
				e.Runs = append(e.Runs, r)
			}
		}
	}
	return e
}

func gitLabDeliveryFacts(g glRepo, readAt time.Time) deliveryEvidence {
	// The configured mrs_open read returns up to 30 entries; a full page is incomplete.
	e := deliveryEvidence{ReadAt: readAt, OpenListComplete: g.open != nil && len(g.open) < 30,
		Changes: make(map[int64]deliveryRevision, len(g.open)+len(g.merged))}
	for _, m := range g.merged {
		e.Changes[m.IID] = deliveryRevision{Branch: m.SourceBranch, Head: m.SHA, Closed: true}
	}
	for _, m := range g.open {
		e.Changes[m.IID] = deliveryRevision{Branch: m.SourceBranch, Head: m.SHA, Pipeline: m.HeadPipeline}
	}
	return e
}

func (d deliveryBlocker) repositoryKey() string {
	if d.Provider == "gitlab" {
		return "gitlab:" + strings.ToLower(d.Host+"/"+d.Repo)
	}
	if d.Provider == "github" && d.Host == "github.com" {
		return strings.ToLower(d.Repo)
	}
	return ""
}

func (o deliveryEvidence) conflicts(d deliveryBlocker, afterReport bool) bool {
	if revision, ok := o.Changes[d.Number]; ok {
		if revision.Closed || revision.Branch != "" && revision.Branch != d.Branch ||
			revision.Head != "" && d.Head != "" && revision.Head != d.Head {
			return true
		}
		if p := revision.Pipeline; p != nil && d.Run > 0 && p.SHA == d.Head &&
			(p.ID > d.Run || p.ID == d.Run && p.Status == "success" && d.isCI()) {
			return true
		}
	} else if afterReport && d.Number > 0 && o.OpenListComplete {
		return true
	}
	if d.Number == 0 && d.Head != "" {
		for _, revision := range o.Changes {
			if !revision.Closed && revision.Branch == d.Branch && revision.Head != "" && revision.Head != d.Head {
				return true
			}
		}
	}
	if d.Run == 0 {
		return false
	}
	var original ghRun
	for _, r := range o.Runs {
		if r.ID != d.Run {
			continue
		}
		if r.Branch != "" && r.Branch != d.Branch || r.HeadSHA != "" && r.HeadSHA != d.Head ||
			r.Attempt > d.Attempt || r.Attempt == d.Attempt && r.Conclusion == "success" && d.isCI() {
			return true
		}
		original = r
	}
	if original.Workflow != 0 {
		for _, r := range o.Runs {
			if r.Workflow == original.Workflow && r.HeadSHA == d.Head && r.Branch == d.Branch &&
				r.ID > original.ID && r.CreatedAt >= original.CreatedAt {
				return true
			}
		}
	}
	return false
}

func (d deliveryBlocker) isCI() bool {
	return d.Operation == "ci_logs" || d.Operation == "rerun_failed"
}

func observeDeliveryProblems(q *Request, s deliveryState) error {
	rows, err := q.db().OpenDeliveryProblems(q.ctx(), db.OpenDeliveryProblemsParams{})
	if err != nil {
		return err
	}
	for _, p := range rows {
		var data publishingBlockerData
		if err := json.Unmarshal(p.Data, &data); err != nil {
			return err
		}
		repo := s.repos[data.Delivery.repositoryKey()]
		if repo != nil && slices.Contains(repo.projects, deref(p.ProjectID)) &&
			!repo.ci.ReadAt.Before(p.SeenAt) && repo.ci.conflicts(data.Delivery, true) {
			if err := closeDeliveryBlocker(q, p, "cleared", "The delivery head or CI run changed"); err != nil {
				return err
			}
		}
	}
	return nil
}

// The publishing mediator already journals status and publication receipts.
// Consume confirmed observations without issuing any extra provider calls.
func observePublishingBlockers(q *Request, b *contract.PublishingHostRecord) error {
	if deref(b.ProjectId) == "" || b.Number <= 0 || b.Branch == "" || b.Host == "" || b.Repo == "" ||
		deref(b.Error) != "" {
		return nil
	}
	var evidence struct {
		ObservedAt time.Time `json:"observedAt"`
	}
	if json.Unmarshal(b.Result, &evidence) != nil || evidence.ObservedAt.IsZero() {
		return nil // Older host receipts can arrive late. Use the delivery pass for those.
	}
	ps, err := q.db().OpenDeliveryProblems(q.ctx(), db.OpenDeliveryProblemsParams{Host: &b.Host, Repo: &b.Repo, ProjectID: b.ProjectId})
	if err != nil {
		return err
	}
	for _, p := range ps {
		if evidence.ObservedAt.Before(p.SeenAt) {
			continue
		}
		var data publishingBlockerData
		if err := json.Unmarshal(p.Data, &data); err != nil {
			return err
		}
		if reason := publishingBlockerResolution(data.Delivery, b); reason != "" {
			if err := closeDeliveryBlocker(q, p, "cleared", reason); err != nil {
				return err
			}
		}
	}
	return nil
}

func publishingBlockerResolution(d deliveryBlocker, b *contract.PublishingHostRecord) string {
	if d.Provider != b.Provider || d.Branch != b.Branch || d.Number != 0 && d.Number != b.Number {
		return ""
	}
	state := strings.ToLower(b.State)
	if state == "closed" || state == "merged" || state == "locked" {
		return "The delivery closed"
	}
	if d.Head != "" && d.Head != b.HeadSha {
		if contract.ValidPublishingSHA(b.HeadSha) {
			return "The delivery head changed"
		}
		return ""
	}
	// A successful repeat of the same publishing operation is direct evidence
	// that its operational blocker cleared.
	if !d.isCI() && d.Operation == deref(b.Action) && (d.Head == "" || d.Head == b.HeadSha) {
		return "Publishing operation succeeded"
	}
	if d.Provider != "gitlab" || d.Run == 0 || deref(b.Action) != "status" || b.HeadSha != d.Head {
		return ""
	}
	var result struct {
		HeadPipeline *struct {
			ID     int64  `json:"id"`
			SHA    string `json:"sha"`
			Status string `json:"status"`
		} `json:"headPipeline"`
	}
	if json.Unmarshal(b.Result, &result) == nil && result.HeadPipeline != nil {
		run := result.HeadPipeline
		if run.ID > 0 && run.SHA == d.Head && (run.ID > d.Run || (run.ID == d.Run && d.isCI() && run.Status == "success")) {
			return "The delivery pipeline was replaced or passed"
		}
	}
	return ""
}

func savedDeliverySnapshot(q *Request) (deliverySnapshot, error) {
	var snap deliverySnapshot
	raw, err := q.db().DeliverySnapshotGet(q.ctx())
	if errors.Is(err, pgx.ErrNoRows) {
		return snap, nil
	}
	if err != nil {
		return snap, err
	}
	return snap, json.Unmarshal(raw, &snap)
}

func observeCIRecoveryBlockers(q *Request, row db.PublishingCiRecovery, p ciRecoveryPayload, o contract.PublishingCIObservation) error {
	staleRun := o.Reason == "newer_run_exists" || o.Reason == "run_is_not_current_failed_attempt"
	if o.Attempt == nil && o.State != contract.PublishingCIStatePassed && o.Reason != "stale_head" && !staleRun {
		return nil
	}
	ps, err := q.db().OpenDeliveryProblems(q.ctx(), db.OpenDeliveryProblemsParams{Host: &p.Target.Host, Repo: &row.Repo, ProjectID: &row.ProjectID})
	if err != nil {
		return err
	}
	for _, problem := range ps {
		var data publishingBlockerData
		if err := json.Unmarshal(problem.Data, &data); err != nil {
			return err
		}
		d := data.Delivery
		if d.Branch != deref(p.Target.Branch) || d.Number != row.Number || d.Head != row.HeadSha {
			continue
		}
		if o.Reason == "stale_head" || d.Run == row.RunNumber &&
			(staleRun || valueOr(o.Attempt) > d.Attempt || valueOr(o.RunId) > 0 && valueOr(o.RunId) != row.RunNumber) ||
			o.State == contract.PublishingCIStatePassed && d.isCI() && d.Run == row.RunNumber {
			if err := closeDeliveryBlocker(q, problem, "cleared", "The CI recovery run changed or passed"); err != nil {
				return err
			}
		}
	}
	return nil
}
