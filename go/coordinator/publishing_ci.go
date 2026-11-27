package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

const (
	ciRecoveryQueue    = "publishing-ci"
	ciCommandLease     = 2 * time.Minute // Replaces commands that have not reported. A spent claim is never granted again.
	ciReadbackInterval = 30 * time.Second
	ciReadbackWindow   = 30 * time.Minute // After the observation window, give the worker the retained uncertainty.
)

type ciRecoveryPayload struct {
	Target      contract.PublishingCommandPayload `json:"target"`
	Reason      string                            `json:"reason"`
	Session     string                            `json:"session"`
	Task        string                            `json:"task"`
	Director    string                            `json:"director"`
	Observation *contract.PublishingCIObservation `json:"observation,omitempty"`
}

// The request identity survives a worker continuation in its original session. The
// initiating execution and actor are kept for provenance, outside the identity comparison.
type ciRecoveryRequest struct {
	Request  contract.PublishingRerunFailedRequest `json:"request"`
	Session  string                                `json:"session"`
	Director string                                `json:"director"`
	Actor    string                                `json:"actor"`
	Source   string                                `json:"source"`
}

func init() {
	op("publishing_rerun_failed", publishingRerunFailed)
	op("publishing_ci_recovery", publishingCIRecovery)
	hostRoute(contract.RoutePublishingCiClaim, publishingCIClaim)
	hostRoute(contract.RoutePublishingCiObservation, publishingCIReport)
	kitRegister(&kitQueue{
		Name: ciRecoveryQueue, Period: 5 * time.Second, Limit: 8, Due: ciRecoveryDue, Step: ciRecoveryStep,
		Retry: kitRetry{Table: "publishing_ci_recovery", Key: "id", Guard: "publishing_ci_recovery.attempts=$5::int",
			Class: func(error) string { return "ci_recovery_unavailable" }},
	})
}

func publishingCIPolicy(s publishingProjectSettings, repoID string, ref contract.RepoRef) *contract.PublishingCIRerunPolicy {
	if s.Execution == nil || !s.Execution.Forge || s.Integration == nil || s.Integration.Mode != "pr" || !contract.CIRerunProvider(ref) {
		return nil
	}
	p := s.Integration.CiRerun
	if !contract.ValidCIRerunPolicy(p) || p.RepoId != repoID {
		return nil
	}
	return p
}

func ciHostInEnvironment(host, repoID string, envs []projectEnvironment) bool {
	return len(envs) == 0 || slices.ContainsFunc(envs, func(e projectEnvironment) bool {
		return e.HostID == host && (e.Repo == nil || *e.Repo == repoID)
	})
}

func publishingCIHost(h publishingHostView, ref contract.RepoRef, repoID string, envs []projectEnvironment) bool {
	return ciHostInEnvironment(h.id, repoID, envs) && slices.Contains(h.caps, string(contract.CapabilityPublishingCiRerunV1)) &&
		hostCapsHaveRepo(h.caps, ref) && publishingHostForgeRank(h, ref) == 0
}

// ciSource checks both the execution's branch binding and the live grant. Calling it again at the
// claim prevents a queued command from spending the round after a pause, stop or policy change.
func ciSource(q *Request, projectID, executionID string) (ciRecoveryPayload, error) {
	var result ciRecoveryPayload
	e, err := getExecution(q, executionID)
	if err != nil {
		return result, err
	}
	if e.ProjectID != projectID || e.SessionID == "" || e.State != "running" || e.unconfirmed() ||
		isTrue(e.StopRequested) || isTrue(e.TaskStopRequested) || deref(e.HostID) == "" {
		return result, fail(409, "CI recovery requires an active source execution")
	}
	s, err := sessionByID(q, e.SessionID)
	if err != nil {
		return result, err
	}
	if s.ProjectID != projectID || s.Access == "read" || (s.Branch != nil && !*s.Branch) || s.ArchivedAt != nil ||
		s.DirectorAccessRevokedAt != nil || s.BotID != "" {
		return result, fail(403, "CI recovery requires a granted worker branch")
	}
	taskID := firstString(e.DeliveryID, e.TaskID, s.DeliveryID, s.TaskID)
	if taskID == "" {
		return result, fail(403, "CI recovery requires a task")
	}
	task, err := taskLoad(q, taskID)
	if err != nil {
		return result, err
	}
	if deref(task.ProjectID) != projectID {
		return result, fail(403, "Task does not belong to the source project")
	}
	state, err := q.db().TaskStateForShare(q.ctx(), taskID)
	if err != nil {
		return result, err
	}
	if state != db.TaskStateInProgress && state != db.TaskStateWaiting {
		return result, fail(409, "Task is paused, cancelled or inactive")
	}
	revoked, err := q.db().CIRecoverySourceRevoked(q.ctx(), &e.ID)
	if err != nil {
		return result, err
	}
	if revoked {
		return result, fail(403, "Source execution access revoked")
	}
	director := firstString(e.TakenFrom, e.DirectedBy, s.DirectedBy)
	person := q.peopleSnapshot().person(director)
	if person == nil || !mayAccessProjectID(person, projectID) {
		return result, fail(403, "Project outside actor grants")
	}
	settings, prefix, err := loadPublishingSettings(q, projectID)
	if err != nil {
		return result, err
	}
	frozen, frozenPrefix, err := forgeProjectSettings(q, e, projectID)
	if err != nil {
		return result, err
	}
	ref, ok := forgeSelectedRepoRef(settings.runSettings, *e.HostID)
	frozenRef, pinned := forgeSelectedRepoRef(frozen, *e.HostID)
	if !ok || !pinned || frozenRef != ref || prefix != frozenPrefix ||
		frozen.Execution == nil || !frozen.Execution.Forge ||
		prefix == "gh-readonly-queue" || strings.HasPrefix(prefix, "gh-readonly-queue/") {
		return result, fail(403, "Execution repository or branch grant changed")
	}
	profile, _ := profileOf(e.AgentConfig)
	for _, repo := range settings.Repos {
		configured, ok := contract.ParseRepoRemote(repo.Remote, repo.Forge, repo.Host)
		if !ok || configured != ref {
			continue
		}
		policy := publishingCIPolicy(settings, repo.ID, ref)
		if policy == nil || !slices.Contains(policy.Profiles, profile.ID) ||
			(len(settings.Execution.Profiles) > 0 && !slices.Contains(settings.Execution.Profiles, profile.ID)) {
			continue
		}
		return ciRecoveryPayload{Session: s.ID, Task: taskID, Director: director, Target: contract.PublishingCommandPayload{
			Action: "rerun_failed", ProjectId: projectID, RepoId: &repo.ID, Repo: ref.Slug, Provider: ref.Provider, Host: ref.Host,
			Prefix: &prefix, Branch: ptr(prefix + "/" + s.ID), BaseBranch: firstString(repo.DefaultBranch, "main"),
			SourceExecutionId: &e.ID, CiRecovery: &contract.PublishingCICommand{Policy: *policy},
		}}, nil
	}
	return result, fail(403, "Project CI rerun policy veto")
}

func ciRecoveryView(row db.PublishingCiRecovery, payload ciRecoveryPayload) contract.PublishingRerunFailedReply {
	state := row.State
	if state == "handoff_pending" {
		state = "handoff"
	}
	return contract.PublishingRerunFailedReply{
		Id: row.ID, ProjectId: row.ProjectID, Repo: row.Repo, Number: row.Number, HeadSha: row.HeadSha,
		RunId: row.RunNumber, Attempt: row.OriginalAttempt, State: state, SourceExecutionId: row.SourceExecutionID,
		RequestedAt: row.CreatedAt.UTC().Format(time.RFC3339), UpdatedAt: row.UpdatedAt.UTC().Format(time.RFC3339),
		Evidence: payload.Observation, Handoff: row.Handoff, DispatchAcknowledged: row.Dispatched,
	}
}

func publishingCIRecovery(q *Request, r *contract.PublishingCiRecoveryRequest) (contract.PublishingCiRecoveryReply, error) {
	out := contract.PublishingCiRecoveryReply{Recoveries: []contract.PublishingRerunFailedReply{}}
	if !mayAccessProjectID(q.person, r.ProjectId) || projectSettingsOf(q, r.ProjectId) == nil {
		return out, fail(404, "Unknown project")
	}
	if r.ExpectedHeadSha != nil && !contract.ValidPublishingSHA(*r.ExpectedHeadSha) {
		return out, fail(400, "Expected full lowercase head SHA")
	}
	rows, err := q.db().CIRecoveryList(q.ctx(), db.CIRecoveryListParams{ProjectID: r.ProjectId, HeadSha: r.ExpectedHeadSha})
	for _, row := range rows {
		var payload ciRecoveryPayload
		if err := json.Unmarshal(row.Payload, &payload); err != nil {
			return out, err
		}
		out.Recoveries = append(out.Recoveries, ciRecoveryView(row, payload))
	}
	return out, err
}

func publishingRerunFailed(q *Request, r *contract.PublishingRerunFailedRequest) (contract.PublishingRerunFailedReply, error) {
	var none contract.PublishingRerunFailedReply
	projectID, executionID := deref(r.ProjectId), deref(r.SourceExecutionId)
	if projectID == "" || executionID == "" || !contract.ValidPublishingSHA(r.ExpectedHeadSha) ||
		strings.TrimSpace(r.RequestId) == "" || len(r.RequestId) > 200 ||
		strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 500 || r.Number <= 0 || r.RunId <= 0 || r.Attempt <= 0 {
		return none, fail(400, "Expected source execution, PR, exact head SHA, run ID, observed attempt, requestId and reason")
	}
	if !mayAccessProjectID(q.person, projectID) {
		return none, fail(404, "Unknown project")
	}
	if q.agent != "" && q.agent != "execution:"+executionID {
		return none, fail(403, "CI recovery source must be the calling execution")
	}
	execution, err := getExecution(q, executionID)
	if err != nil {
		return none, err
	}
	if execution.ProjectID != projectID || execution.SessionID == "" {
		return none, fail(403, "CI recovery requires a project session")
	}
	requestKey := sha256Text(projectID + "\x00" + r.RequestId)
	request := ciRecoveryRequest{Request: *r, Session: execution.SessionID, Director: director(q),
		Actor: actor(q, ""), Source: executionID}
	request.Request.SourceExecutionId = nil
	prior, err := q.db().CIRequestGet(q.ctx(), requestKey)
	if err == nil {
		var original ciRecoveryRequest
		if err := json.Unmarshal(prior.Payload, &original); err != nil {
			return none, err
		}
		if original.Session != request.Session || original.Director != request.Director ||
			string(jsonBytes(original.Request)) != string(jsonBytes(request.Request)) {
			return none, fail(409, "CI recovery request identity conflict")
		}
		return ciRecoveryRead(q, prior.RecoveryID)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return none, err
	}
	payload, err := ciSource(q, projectID, executionID)
	if err != nil {
		return none, err
	}
	if q.person == nil || (q.person.Email != payload.Director && (q.agent != "" || !fullAccess(q.person))) {
		return none, fail(403, "CI recovery source belongs to another director")
	}
	// A provider PR is a single budget even when the repository is bound to more than one Fleet project.
	roundKey := sha256Text(fmt.Sprintf("%s/%s#%d@%s", payload.Target.Host, strings.ToLower(payload.Target.Repo), r.Number, r.ExpectedHeadSha))
	key := newID()
	payload.Reason = r.Reason
	payload.Target.RequestId, payload.Target.Number, payload.Target.ExpectedHeadSha = r.RequestId, &r.Number, &r.ExpectedHeadSha
	payload.Target.CiRecovery.Id, payload.Target.CiRecovery.RunId, payload.Target.CiRecovery.Attempt = key, r.RunId, r.Attempt
	inserted, err := q.db().CIRecoveryInsert(q.ctx(), db.CIRecoveryInsertParams{
		ID: key, ProjectID: projectID, RoundKey: roundKey, Repo: payload.Target.Repo, Number: r.Number, HeadSha: r.ExpectedHeadSha,
		RunNumber: r.RunId, OriginalAttempt: r.Attempt, SourceExecutionID: executionID, Payload: jsonBytes(payload),
	})
	if err != nil {
		return none, err
	}
	if inserted == 0 {
		key, err = q.db().CIRecoveryActive(q.ctx(), roundKey)
		if errors.Is(err, pgx.ErrNoRows) {
			return none, fail(409, "A recovery round already exists for this PR head")
		}
		if err != nil {
			return none, err
		}
	}
	reply, err := ciRecoveryRead(q, key)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && reply.ProjectId != projectID) {
		return none, fail(409, "A recovery round already exists for this PR head")
	}
	if err != nil {
		return none, err
	}
	if err := q.db().CIRequestInsert(q.ctx(), db.CIRequestInsertParams{ID: requestKey, ProjectID: projectID, RecoveryID: key, Payload: jsonBytes(request)}); err != nil {
		return none, err
	}
	if inserted != 0 {
		if err := audit(q, "publishing-ci:"+key, "reserved", nil, payload, auditExtra{ProjectID: &projectID}); err != nil {
			return none, err
		}
		if err := kitNotify(q, ciRecoveryQueue); err != nil {
			return none, err
		}
	}
	return reply, nil
}

func ciRecoveryRead(q *Request, key string) (contract.PublishingRerunFailedReply, error) {
	row, err := q.db().CIRecoveryGet(q.ctx(), key)
	if err != nil {
		return contract.PublishingRerunFailedReply{}, err
	}
	var payload ciRecoveryPayload
	err = json.Unmarshal(row.Payload, &payload)
	return ciRecoveryView(row, payload), err
}

func ciRecoveryDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rows, err := q.db().CIRecoveryDue(q.ctx(), db.CIRecoveryDueParams{DueAt: now, Lim: int32(limit)})
	claims := make([]*kitClaim, 0, len(rows))
	for _, row := range rows {
		claims = append(claims, &kitClaim{Key: row.ID, Attempts: int(row.Attempts), Guard: []any{row.Attempts}})
	}
	return claims, err
}

func ciRecoveryStep(q *Request, now time.Time, claim *kitClaim) error {
	// Serialize policy, task cancellation, host receipts and journal transitions with other Fleet writers.
	if err := kitLock(q, GLOBAL); err != nil {
		return err
	}
	row, err := q.db().CIRecoveryGet(q.ctx(), claim.Key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && row.NextAttemptAt.After(now) {
		return nil
	}
	if err != nil {
		return err
	}
	var payload ciRecoveryPayload
	if err := json.Unmarshal(row.Payload, &payload); err != nil {
		return err
	}
	if row.State == "handoff_pending" {
		return ciRecoveryHandoff(q, row, payload)
	}
	observe := false
	switch row.State {
	case "queued", "rate_limited":
	case "dispatch_unknown", "dispatched", "observing":
		observe = true
	default:
		return nil
	}
	if row.CommandUntil != nil && row.CommandUntil.After(now) {
		return kitSchedule(q, ciRecoveryQueue, row.ID, *row.CommandUntil)
	}
	if now.Sub(row.CreatedAt) >= ciReadbackWindow {
		if observe {
			return ciRecoveryNote(q, row, payload, contract.PublishingCIStateHandoff, "provider_outcome_unconfirmed")
		}
		return ciRecoveryNote(q, row, payload, contract.PublishingCIStateRefused, "dispatch_window_elapsed")
	}
	if !observe {
		if err := ciCheckSource(q, row, payload); err != nil {
			code, _, _ := errorAnswer(err)
			if code == 403 || code == 404 || code == 409 {
				return ciRecoveryNote(q, row, payload, contract.PublishingCIStateRefused, "source_or_policy_changed")
			}
			return err
		}
	}
	h, err := ciChooseHost(q, row.ProjectID, payload.Target)
	if err != nil {
		return err
	}
	if h == "" {
		return kitSchedule(q, ciRecoveryQueue, row.ID, now.Add(ciReadbackInterval))
	}
	p := payload.Target
	in := *p.CiRecovery
	in.ObserveOnly, in.Dispatched = observe, row.Dispatched
	if observe && payload.Observation != nil && payload.Observation.RunId != nil {
		in.RunId = *payload.Observation.RunId
	}
	p.CiRecovery = &in
	cid, err := queuePublishingCommand(q, h, p)
	if err != nil {
		return err
	}
	row.CommandID, row.CommandUntil = &cid, ptr(now.Add(ciCommandLease))
	if err := ciRecoverySave(q, row, payload); err != nil {
		return err
	}
	if err := kitSchedule(q, ciRecoveryQueue, row.ID, *row.CommandUntil); err != nil {
		return err
	}
	afterCommit(q, func() { q.app.wakeHost(h) })
	return nil
}

func ciChooseHost(q *Request, projectID string, target contract.PublishingCommandPayload) (string, error) {
	hosts, err := publishingOnlineHosts(q)
	if err != nil {
		return "", err
	}
	settings, _, err := loadPublishingSettings(q, projectID)
	if err != nil {
		return "", err
	}
	ref := contract.RepoRef{Provider: target.Provider, Host: target.Host, Slug: target.Repo}
	if !slices.ContainsFunc(settings.Repos, func(r projectRepo) bool {
		current, ok := contract.ParseRepoRemote(r.Remote, r.Forge, r.Host)
		return ok && r.ID == deref(target.RepoId) && current == ref
	}) {
		return "", nil
	}
	for _, h := range hosts {
		if publishingCIHost(h, ref, deref(target.RepoId), settings.Environments) &&
			hostHolderAllowsProject(q, h.holder, projectID) {
			return h.id, nil
		}
	}
	return "", nil
}

func ciCheckSource(q *Request, row db.PublishingCiRecovery, p ciRecoveryPayload) error {
	fresh, err := ciSource(q, row.ProjectID, row.SourceExecutionID)
	if err != nil {
		return err
	}
	if fresh.Task != p.Task || fresh.Session != p.Session || fresh.Director != p.Director ||
		fresh.Target.Provider != p.Target.Provider || fresh.Target.Host != p.Target.Host ||
		fresh.Target.Repo != p.Target.Repo || fresh.Target.BaseBranch != p.Target.BaseBranch ||
		deref(fresh.Target.Branch) != deref(p.Target.Branch) ||
		string(jsonBytes(fresh.Target.CiRecovery.Policy)) != string(jsonBytes(p.Target.CiRecovery.Policy)) {
		return fail(403, "CI rerun binding or policy changed")
	}
	return nil
}

func ciRecoverySave(q *Request, row db.PublishingCiRecovery, p ciRecoveryPayload) error {
	return q.db().CIRecoveryMove(q.ctx(), db.CIRecoveryMoveParams{ID: row.ID, State: row.State, Released: row.Released, Dispatched: row.Dispatched,
		CommandID: row.CommandID, CommandUntil: row.CommandUntil, Handoff: row.Handoff, Payload: jsonBytes(p)})
}

func ciRecoveryNote(q *Request, row db.PublishingCiRecovery, p ciRecoveryPayload, state contract.PublishingCIState, reason string) error {
	observation := contract.PublishingCIObservation{State: state, Reason: reason}
	return ciRecordObservation(q, row, p, observation, false)
}

func ciCommandRecord(q *Request, host, execution, command, key string, claiming bool) (db.PublishingCiRecovery, ciRecoveryPayload, contract.PublishingCommandPayload, error) {
	var p ciRecoveryPayload
	var target contract.PublishingCommandPayload
	var row db.PublishingCiRecovery
	if host != q.hostID() || execution == "" || command == "" || key == "" {
		return row, p, target, fail(403, "CI recovery host rejected")
	}
	c, err := commandOf(q, host, execution, command, "CI recovery command rejected")
	if err != nil {
		return row, p, target, err
	}
	if claiming && !slices.Contains(commandOpen, db.CommandStatus(c.Status)) {
		return row, p, target, fail(409, "CI recovery command is no longer active")
	}
	if c.Kind != string(contract.CommandKindPublishing) || json.Unmarshal(c.Payload, &target) != nil ||
		target.CiRecovery == nil || target.CiRecovery.Id != key || target.ProjectId != c.ProjectID {
		return row, p, target, fail(403, "CI recovery command rejected")
	}
	row, err = q.db().CIRecoveryGet(q.ctx(), key)
	if err != nil {
		return row, p, target, err
	}
	if row.ProjectID != c.ProjectID || !hostHolderAllowsProject(q, q.host.Holder, row.ProjectID) {
		return row, p, target, fail(403, "CI recovery host project rejected")
	}
	err = json.Unmarshal(row.Payload, &p)
	return row, p, target, err
}

func publishingCIClaim(q *Request, b *contract.PublishingCIClaim) (contract.OkReply, error) {
	var no contract.OkReply
	row, p, target, err := ciCommandRecord(q, b.HostId, b.ExecutionId, b.CommandId, b.RecoveryId, true)
	if err != nil {
		return no, err
	}
	if target.CiRecovery.ObserveOnly || deref(row.CommandID) != b.CommandId || row.CommandUntil == nil ||
		!row.CommandUntil.After(time.Now()) || (row.State != "queued" && row.State != "rate_limited") ||
		len(b.OriginalJobs) == 0 || row.OriginalAttempt != 1 {
		return no, fail(409, "CI dispatch permission unavailable or already spent")
	}
	if err := ciCheckSource(q, row, p); err != nil {
		return no, err
	}
	settings, _, err := loadPublishingSettings(q, row.ProjectID)
	if err != nil {
		return no, err
	}
	if !ciHostInEnvironment(b.HostId, deref(target.RepoId), settings.Environments) {
		return no, fail(403, "Host repository grant changed")
	}
	required := false
	for _, job := range b.OriginalJobs {
		if job.Id <= 0 || job.HeadSha != row.HeadSha || strings.TrimSpace(job.Name) == "" {
			return no, fail(409, "Original failed job does not match the recovery head")
		}
		required = required || slices.Contains(p.Target.CiRecovery.Policy.RequiredJobs, job.Name)
	}
	if !required {
		return no, fail(409, "No required failed job in CI recovery evidence")
	}
	row.State = "dispatch_unknown"
	p.Observation = &contract.PublishingCIObservation{State: contract.PublishingCIStateDispatchUnknown,
		Reason: "dispatch_claim_committed", Attempt: &row.OriginalAttempt, OriginalJobs: b.OriginalJobs}
	if err := ciRecoverySave(q, row, p); err != nil {
		return no, err
	}
	if err := audit(q, "publishing-ci:"+row.ID, "dispatch_claim", nil, b, auditExtra{ProjectID: &row.ProjectID, Holder: q.host.Holder}); err != nil {
		return no, err
	}
	return contract.OkReply{Ok: true}, nil
}

func publishingCIReport(q *Request, b *contract.PublishingCIReport) (contract.OkReply, error) {
	var no contract.OkReply
	row, p, target, err := ciCommandRecord(q, b.HostId, b.ExecutionId, b.CommandId, b.RecoveryId, false)
	if err != nil {
		return no, err
	}
	// An acknowledgement may travel twice, or after a superseding observation. Neither may rewrite the journal.
	if deref(row.CommandID) != b.CommandId {
		return contract.OkReply{Ok: true}, nil
	}
	if !target.CiRecovery.ObserveOnly && (row.State == "dispatch_unknown") != b.Claimed {
		return contract.OkReply{Ok: true}, nil
	}
	if err := ciRecordObservation(q, row, p, b.Observation, target.CiRecovery.ObserveOnly); err != nil {
		return no, err
	}
	return contract.OkReply{Ok: true}, nil
}

func ciRecordObservation(q *Request, row db.PublishingCiRecovery, p ciRecoveryPayload, observation contract.PublishingCIObservation, observeOnly bool) error {
	if observeOnly && observation.State == contract.PublishingCIStateRefused {
		observation.State = contract.PublishingCIStateObserving
	}
	if p.Observation != nil {
		if observation.Attempt == nil {
			observation.Attempt = p.Observation.Attempt
		}
		if len(observation.OriginalJobs) == 0 {
			observation.OriginalJobs = p.Observation.OriginalJobs
		}
		if observation.RunId == nil {
			observation.RunId = p.Observation.RunId
		}
	}
	if (observation.State == contract.PublishingCIStateDispatched || observation.State == contract.PublishingCIStateDispatchUnknown) &&
		(observeOnly || row.State != "dispatch_unknown") {
		return fail(409, "Missing CI dispatch claim")
	}
	previous := p.Observation
	if observation.State == contract.PublishingCIStateRefused && (row.State == "queued" || row.State == "rate_limited") {
		row.Released = true // A pre-claim refusal cannot consume the head's rerun allowance.
	}
	p.Observation = &observation
	row.CommandID, row.CommandUntil = nil, nil
	row.State = string(observation.State)
	if observation.State == contract.PublishingCIStateDispatched {
		row.Dispatched = true
	}
	if observation.State == contract.PublishingCIStateHandoff {
		row.State = "handoff_pending"
	}
	now := time.Now()
	at := now
	switch observation.State {
	case contract.PublishingCIStateObserving, contract.PublishingCIStateDispatchUnknown, contract.PublishingCIStateDispatched, contract.PublishingCIStateRateLimited:
		if observeOnly && observation.State == contract.PublishingCIStateRateLimited {
			row.State = "observing" // A read throttle can never release a spent claim.
		}
		at = now.Add(ciReadbackInterval)
		if observation.RetryAt != nil {
			if retry := time.UnixMilli(*observation.RetryAt); retry.After(at) {
				at = retry
			}
		}
		if deadline := row.CreatedAt.Add(ciReadbackWindow); deadline.After(now) && at.After(deadline) {
			at = deadline
		}
	}
	if err := ciRecoverySave(q, row, p); err != nil {
		return err
	}
	if err := observeCIRecoveryBlockers(q, row, p, observation); err != nil {
		return err
	}
	if reason := ciRecoveryBlockerReason(observation); reason != "" {
		task, err := taskLoad(q, p.Task)
		if err != nil && !errors.Is(err, errNotFound) {
			return err
		}
		if err == nil && deliveryTaskActive(task.State) {
			mediator := ""
			if q.host != nil {
				mediator = q.hostID()
			}
			d := deliveryBlocker{Provider: p.Target.Provider, Host: p.Target.Host, Repo: row.Repo, Branch: deref(p.Target.Branch),
				Number: row.Number, Head: row.HeadSha, Run: row.RunNumber, Attempt: max(row.OriginalAttempt, valueOr(observation.Attempt)),
				Task: p.Task, Session: p.Session, Execution: row.SourceExecutionID, Operation: "rerun_failed",
				Reason: reason, Recovery: row.ID, Observation: observation.Reason, Mediator: mediator}
			if observation.RunId != nil {
				d.Run = *observation.RunId
			}
			if _, err := raiseDeliveryBlocker(q, row.ProjectID, deref(task.Code), d); err != nil {
				return err
			}
		}
	}
	if string(jsonBytes(previous)) != string(jsonBytes(observation)) {
		if err := audit(q, "publishing-ci:"+row.ID, "observation", previous, observation, auditExtra{ProjectID: &row.ProjectID}); err != nil {
			return err
		}
	}
	if err := kitSchedule(q, ciRecoveryQueue, row.ID, at); err != nil {
		return err
	}
	return kitNotify(q, ciRecoveryQueue)
}

// Only terminal operational outcomes signal duty. Second red still goes to the worker.
func ciRecoveryBlockerReason(o contract.PublishingCIObservation) string {
	if o.State != contract.PublishingCIStateRefused && o.State != contract.PublishingCIStateHandoff {
		return ""
	}
	switch o.Reason {
	case "host_ci_grant_unavailable", "provider_dispatch_denied", "provider_read_denied_or_missing":
		return "access_unavailable"
	case "provider_jobs_unverified":
		return "diagnostics_unavailable"
	case "dispatch_window_elapsed":
		return "provider_unavailable"
	case "provider_outcome_unconfirmed", "replacement_required_jobs_unconfirmed":
		return "outcome_unconfirmed"
	default:
		return ""
	}
}

func ciRecoveryHandoff(q *Request, row db.PublishingCiRecovery, p ciRecoveryPayload) error {
	row.State = "handoff"
	row.Handoff = ptr("held")
	task, err := taskLoad(q, p.Task)
	if err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	session, err := sessionMaybe(q, p.Session)
	if err != nil {
		return err
	}
	person := q.peopleSnapshot().person(p.Director)
	if session != nil && session.ProjectID == row.ProjectID && session.ArchivedAt == nil &&
		session.DirectorAccessRevokedAt == nil && person != nil && mayAccessProjectID(person, row.ProjectID) &&
		firstString(session.DeliveryID, session.TaskID) == p.Task &&
		(task.State == db.TaskStateInProgress || task.State == db.TaskStateWaiting) {
		if _, err := agentSubmit(q, &contract.MessageSendRequest{SessionId: &p.Session, RequestId: ptr("ci-recovery:" + row.ID),
			Text: ptr(fmt.Sprintf("CI recovery needs your attention for %s#%d at %s (run %d). Inspect ci_recovery for the retained evidence, then investigate the current failure.\n\n%s",
				row.Repo, row.Number, row.HeadSha, row.RunNumber, jsonBytes(p.Observation)))}, "wake"); err != nil {
			return err
		}
		row.Handoff = ptr("submitted")
		wakeDispatchAfter(q)
	}
	if err := ciRecoverySave(q, row, p); err != nil {
		return err
	}
	return audit(q, "publishing-ci:"+row.ID, "handoff", nil, row.Handoff, auditExtra{ProjectID: &row.ProjectID})
}
