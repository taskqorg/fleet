package main

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fmt"
	"slices"
	"strings"
	"time"

	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// observeTaskDeliveries records authoritative correlations in the same transaction as the delivery snapshot.
func observeTaskDeliveries(q *Request, repos map[string]*deliveryRepo) error {
	var candidates []deliveryMergedPR
	projects := map[string][]string{}
	for _, repo := range repos {
		if repo == nil || repo.problem != "" {
			continue
		}
		for _, pr := range repo.delivered {
			candidates = append(candidates, pr)
			projects[pr.URL] = repo.projects
		}
		for _, it := range repo.items {
			switch it.Stage {
			case contract.DeliveryStagePrOpen, contract.DeliveryStagePrCi, contract.DeliveryStagePrFailed,
				contract.DeliveryStageMqWait, contract.DeliveryStageMqCi, contract.DeliveryStageMqFailed:
				candidates = append(candidates, deliveryMergedPR{Repo: it.Repo, Number: it.Number, URL: it.Url,
					Branch: deref(it.Branch), HeadSHA: deref(it.HeadSha), Stage: string(it.Stage)})
				projects[it.Url] = repo.projects
			}
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	matches, err := findTaskDeliveryMatches(q, candidates)
	if err != nil {
		return err
	}
	slices.SortFunc(matches, func(a, b taskDeliveryMatch) int { return strings.Compare(a.taskID, b.taskID) })
	for _, match := range matches {
		if !slices.Contains(projects[match.pr.URL], match.projectID) {
			continue
		}
		t, err := taskLoad(q, match.taskID)
		if errors.Is(err, errNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if err := retainTaskDelivery(q, t, match.pr); err != nil {
			return err
		}
	}
	return nil
}

func retainTaskDelivery(q *Request, t db.Task, pr deliveryMergedPR) error {
	if err := q.db().TaskLifecycleLock(q.ctx(), t.ID); err != nil {
		return err
	}
	if strings.EqualFold(pr.Repo, githubPublicRepo) && (!pr.ReleaseCovered ||
		!validCommitSHA(pr.ReleaseTarget) || len(pr.InstalledOn) == 0) {
		pr.Delivered = false
	}
	if !pr.Delivered && pr.Stage != "ambiguous" {
		previous, err := q.db().TaskDeliveryProof(q.ctx(), t.ID)
		if err == nil {
			old, err := retainedDelivery(previous)
			if err != nil {
				return err
			}
			if old.Delivered && pr.Stage == "closed" && old.URL != pr.URL {
				return nil // A discarded branch cannot supersede confirmed delivery.
			}
			if old.Delivered && old.URL == pr.URL && old.HeadSHA == pr.HeadSHA && old.MergeSHA == pr.MergeSHA {
				if !confirmedRolloutRollback(old, pr.ObservedOn) {
					pr = old
				} else {
					pr.RollbackConfirmed = true
				}
			}
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	requirement, _ := taskLiveCheck(t)
	key := deliveryCheckKey(t.ID, requirement, pr)
	n, err := q.db().TaskDeliveryProofPut(q.ctx(), db.TaskDeliveryProofPutParams{
		TaskID: t.ID, ProjectID: t.ProjectID, Proof: jsonBytes(deliverySnapPR{deliveryMergedPR: pr, SnapDelivered: pr.Delivered}),
		VerificationKey: key,
	})
	if err != nil || n == 0 {
		return err
	}
	return wakeTask(q, t.ID)
}

func validCommitSHA(s string) bool {
	_, err := hex.DecodeString(s)
	return len(s) == 40 && err == nil
}

func taskAcceptancePolicy(t db.Task) (taskCompletionReq, error) {
	var req taskCompletionReq
	if len(t.CompletionRequirements) > 0 {
		if err := json.Unmarshal(t.CompletionRequirements, &req); err != nil {
			return req, fail(409, "Task completion policy is invalid and needs correction.")
		}
	}
	return req, nil
}

func taskRequiresHuman(t db.Task) bool {
	req, err := taskAcceptancePolicy(t)
	return err != nil || isTrue(req.RequireHumanAcceptance) || isTrue(req.ManualAcceptance) ||
		strings.EqualFold(strings.TrimSpace(req.Acceptance), "human") || strings.EqualFold(strings.TrimSpace(req.Acceptance), "manual")
}

func acceptanceRequiresHuman(q *Request, t db.Task) (bool, error) {
	if taskRequiresHuman(t) {
		return true, nil
	}
	requirement, err := taskLiveCheck(t)
	if err != nil || requirement == "" {
		return false, err
	}
	_, err = q.db().TaskDeliveryProof(q.ctx(), t.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return false, err
}

func taskLiveCheck(t db.Task) (string, error) {
	var policy struct {
		LiveCheck any `json:"liveCheck"`
	}
	if len(t.CompletionRequirements) > 0 && json.Unmarshal(t.CompletionRequirements, &policy) != nil {
		return "", fail(409, "The task's live check requirement needs correction.")
	}
	switch v := policy.LiveCheck.(type) {
	case string:
		if check := strings.TrimSpace(v); check != "" {
			return check, nil
		}
	case bool:
		if v {
			return firstString(landingCheckLine(deref(t.ExpectedOutcome)), "Verify the delivered result on its live target."), nil
		}
	case nil:
	default:
		return "", fail(409, "The task's live check requirement needs correction.")
	}
	return landingCheckLine(deref(t.ExpectedOutcome)), nil
}

func landingCheckLine(outcome string) string {
	for _, line := range strings.Split(outcome, "\n") {
		if landingWaitPattern.MatchString(line) && strings.Contains(strings.ToLower(line), "live check") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func deliveryCheckKey(taskID, requirement string, pr deliveryMergedPR) string {
	return frameHash([]string{taskID, requirement, pr.URL, pr.HeadSHA, pr.MergeSHA, pr.Stage, pr.Release})
}

func retainedDelivery(row db.TaskDeliveryResult) (deliveryMergedPR, error) {
	var snapshot deliverySnapPR
	err := json.Unmarshal(row.Proof, &snapshot)
	pr := snapshot.deliveryMergedPR
	pr.Delivered = snapshot.SnapDelivered
	return pr, err
}

func taskDeliveryVerified(q *Request, t db.Task, requirement string, pr deliveryMergedPR) (bool, error) {
	if !pr.Delivered {
		return false, nil
	}
	if requirement == "" {
		return true, nil
	}
	at, err := time.Parse(time.RFC3339, pr.DeliveredAt)
	if err != nil {
		return false, nil
	}
	return q.db().TaskDeliveryVerified(q.ctx(), db.TaskDeliveryVerifiedParams{
		TaskID: &t.ID, VerificationKey: deliveryCheckKey(t.ID, requirement, pr), DeliveredAt: pgtype.Timestamptz{Time: at, Valid: true},
	})
}

func eligibleTaskResult(q *Request, taskID string, earlier, pending, requireReport bool, delivery *deliveryMergedPR) (taskAcceptanceResult, error) {
	t, err := taskLoad(q, taskID)
	if err != nil {
		return taskAcceptanceResult{}, err
	}
	if !openTask[string(t.State)] || t.State == db.TaskStatePaused {
		return taskAcceptanceResult{}, fail(409, "Task does not allow acceptance in its current state")
	}
	if reason, err := waitReason(q, taskID); err != nil || reason != "" {
		if err == nil {
			err = fail(409, "Task has an unresolved "+reason)
		}
		return taskAcceptanceResult{}, err
	}
	requirement, err := taskLiveCheck(t)
	if err != nil {
		return taskAcceptanceResult{}, err
	}
	if requirement != "" || delivery != nil {
		proof, err := q.db().TaskDeliveryProof(q.ctx(), taskID)
		if errors.Is(err, pgx.ErrNoRows) {
			if delivery == nil && !requireReport && landingCheckLine(deref(t.ExpectedOutcome)) == "" {
				return acceptanceResult(q, taskID, earlier, pending, false)
			}
			if landingCheckLine(deref(t.ExpectedOutcome)) != "" {
				err = fail(409, "Task is waiting for the required verified delivery")
			} else {
				err = fail(409, "This live check requires an explicit acceptance offer")
			}
		}
		if err != nil {
			return taskAcceptanceResult{}, err
		}
		pr, err := retainedDelivery(proof)
		if err != nil {
			return taskAcceptanceResult{}, err
		}
		if !pr.Delivered || !deliveryRepositoryAllowed(deliveryPolicyForProject(q, deref(t.ProjectID)), pr) ||
			delivery != nil && !sameDeliveryRef(&pr, delivery) {
			return taskAcceptanceResult{}, fail(409, "The selected delivery no longer matches the verified result")
		}
		verified, err := taskDeliveryVerified(q, t, requirement, pr)
		if err != nil {
			return taskAcceptanceResult{}, err
		}
		if !verified {
			return taskAcceptanceResult{}, fail(409, "Task is waiting for the required live check")
		}
	}
	if delivery != nil && !deliverySuppliesResult(t, *delivery) {
		requireReport = true
		delivery = nil
	}
	return acceptanceResult(q, taskID, earlier, pending, requireReport && delivery == nil)
}

func deliverySuppliesResult(t db.Task, pr deliveryMergedPR) bool {
	// Independent deliverables must remain with their own acceptance evidence. The current typed policy covers
	// PR identity, verification, human approval and stored artifacts. Unknown requirements require the worker report.
	var requirements rawObject
	if len(t.CompletionRequirements) > 0 && json.Unmarshal(t.CompletionRequirements, &requirements) != nil {
		return false
	}
	for key := range requirements {
		switch key {
		case "requireHumanAcceptance", "manualAcceptance", "autoAccept", "acceptance", "prNumber",
			"pullRequest", "prUrl", "branch", "liveCheck", "artifacts":
		default:
			return false
		}
	}
	dest := strings.TrimSpace(deref(t.ResultDestination))
	if dest == "" || dest == pr.URL {
		return true
	}
	return slices.ContainsFunc(explicitPRRefs(dest), func(ref deliveryPRRef) bool {
		return strings.EqualFold(ref.repo, pr.Repo) && ref.number == pr.Number
	})
}

func decideDeliveredTask(q *Request, now time.Time, t db.Task, e *agentExecution, proof db.TaskDeliveryResult) (string, time.Time, error) {
	pr, err := retainedDelivery(proof)
	if err != nil {
		return "", time.Time{}, err
	}
	if !pr.Delivered {
		switch pr.Stage {
		case "ambiguous", "closed", string(contract.DeliveryStagePrFailed), string(contract.DeliveryStageMqFailed):
			return "blocked delivery decision", time.Time{}, deliveredTaskDecision(q, t, e,
				"The task has an ambiguous or blocked delivery. Inspect the linked changes before continuing.")
		}
		return "", now.Add(time.Minute), nil
	}
	requirement, err := taskLiveCheck(t)
	if err != nil {
		return "completion policy decision", time.Time{}, deliveredTaskDecision(q, t, e, err.Error())
	}
	verified, err := taskDeliveryVerified(q, t, requirement, pr)
	if err != nil {
		return "", time.Time{}, err
	}
	if !verified {
		key := deliveryCheckKey(t.ID, requirement, pr)
		if proof.VerificationKey != key {
			return "live check policy changed", time.Time{}, deliveredTaskDecision(q, t, e, "The required live check changed after the delivery was recorded.")
		}
		if _, err := q.db().TaskDeliveryVerificationGet(q.ctx(), key); err == nil {
			return "live check requires recovery", time.Time{}, deliveredTaskDecision(q, t, e,
				"The delivery verification turn ended without an affirmative live check for the delivered target.")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return "", time.Time{}, err
		}
		input := fmt.Sprintf("Fleet delivery verification for task %s. Delivery %s landed at stage %s, release %s (head %s, merge %s). Perform only the required check: %s. Record an affirmative result with worker_done outcome succeeded, liveCheck %q and a summary of what was observed. Report failure if the check fails.",
			firstString(deref(t.Code), t.Title), pr.URL, pr.Stage, pr.Release, pr.HeadSHA, pr.MergeSHA, requirement, key)
		continued, err := admitTaskContinuation(q, t, e, "verify-delivery:"+key, input)
		if err != nil {
			var conflict *HTTPError
			if errors.As(err, &conflict) && conflict.Code < 500 {
				return "live check requires recovery", time.Time{}, deliveredTaskDecision(q, t, e, conflict.Msg)
			}
			return "", time.Time{}, err
		}
		if continued == nil {
			return "", now.Add(5 * time.Second), nil
		}
		err = q.db().TaskDeliveryVerificationAdmit(q.ctx(), db.TaskDeliveryVerificationAdmitParams{
			VerificationKey: key, TaskID: t.ID, ProjectID: t.ProjectID, ExecutionID: continued.ID, Deadline: now.Add(recoveryWindow),
		})
		return "", now.Add(5 * time.Second), err
	}
	accepted, err := reconcileTaskDelivery(q, t.ID, pr)
	var conflict *HTTPError
	if errors.As(err, &conflict) && conflict.Code == 409 {
		return "delivery requires recovery", time.Time{}, deliveredTaskDecision(q, t, e, conflict.Msg)
	}
	if err != nil {
		return "", time.Time{}, err
	}
	if accepted {
		return "delivered result settled", time.Time{}, nil
	}
	return "delivery rejected", time.Time{}, deliveredTaskDecision(q, t, e, "The delivered result was previously rejected.")
}

func deliveredTaskDecision(q *Request, t db.Task, e *agentExecution, reason string) error {
	if e == nil {
		return askTaskRecovery(q, t, "", "", reason, "delivery")
	}
	return askTaskRecovery(q, t, e.ID, e.SessionID, reason, "delivery")
}

func reconcileTaskDelivery(q *Request, taskID string, pr deliveryMergedPR) (bool, error) {
	t, err := taskLoad(q, taskID)
	if err != nil {
		return false, err
	}
	if t.AcceptedAt != nil || t.State == db.TaskStateCompleted {
		return true, nil
	}
	pending, err := q.db().TaskHasPendingWork(q.ctx(), &taskID)
	if err != nil {
		return false, err
	}
	selected, err := eligibleTaskResult(q, taskID, false, pending, false, &pr)
	if err != nil {
		return false, err
	}
	rejected, err := deliveryRejectedEarlier(q, taskID, selected.id, pr)
	if err != nil || rejected {
		return false, err
	}
	if _, err := settleTask(q, taskID, ""); err != nil {
		return false, err
	}
	offerText := fmt.Sprintf("PR #%d (%s) merged and delivered. Accept the result of: %s", pr.Number, pr.Repo, t.Title)
	if pr.Release != "" {
		offerText = fmt.Sprintf("PR #%d (%s) merged and delivered (%s). Accept the result of: %s", pr.Number, pr.Repo, pr.Release, t.Title)
	}
	qid, err := requestAcceptanceWithDelivery(q, acceptTask{ID: taskID, Title: t.Title, ProjectID: t.ProjectID}, nil, &offerText, deliveryActor, &pr)
	if err != nil {
		return false, err
	}
	req, policyErr := taskAcceptancePolicy(t)
	autoAccept := policyErr == nil && deliveryPolicyForProject(q, deref(t.ProjectID)).autoAccept && !taskRequiresHuman(t) &&
		(req.AutoAccept == nil || *req.AutoAccept)
	if !autoAccept {
		if t.State == db.TaskStateQueued {
			current, err := taskLoad(q, taskID)
			if err != nil {
				return false, err
			}
			if current.State == db.TaskStateQueued {
				_, err = taskTransition(q, taskID, "blocked", awaitingResult)
				return err == nil, err
			}
		}
		return true, nil
	}
	if _, err := answerQuestion(q, qid, "accepted", deliveryActor, nil); err != nil {
		return false, err
	}
	return true, audit(q, taskID, "delivery_reconciled", nil, pr, auditExtra{ProjectID: t.ProjectID, Reason: "auto_accepted"})
}
