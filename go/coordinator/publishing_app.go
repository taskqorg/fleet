package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/projectpublisher"
)

func init() { hostRoute(contract.RoutePublishingApp, publishingAppRoute) }

func publishingAppRoute(q *Request, b *contract.PublishingAppRequest) (contract.PublishingAppReply, error) {
	return publishApp(q, b, projectpublisher.Authorize)
}

func publishApp(q *Request, b *contract.PublishingAppRequest, authorize func(context.Context, projectpublisher.Operation) (projectpublisher.Reply, error)) (contract.PublishingAppReply, error) {
	var reply contract.PublishingAppReply
	if b.HostId != q.hostID() || b.HostId != projectpublisher.Executor || b.AgentToolKey == "" {
		return reply, fail(403, "Repository publisher host rejected")
	}
	var g appPublishingGrant
	// Design admission locks the task even when it is nonvisual. Release those locks before reaching the publisher.
	err := q.app.store.Write(q.ctx(), q, nil, func(rq *Request) error {
		var err error
		g, err = appPublishingScope(rq, b)
		return err
	})
	if err != nil {
		return reply, err
	}
	var evidence contract.ForgeReviewReply
	if g.op.Action == "record_code_review" {
		evidence, err = forgeReviewRoute(q, &g.review)
		if err != nil {
			return reply, err
		}
		if !evidence.Verified {
			return reply, fail(409, "Repository App code-review requires verified evidence")
		}
		g.op.TargetURL = deref(evidence.TargetUrl)
	}
	ctx, cancel := context.WithTimeout(q.ctx(), 30*time.Second)
	defer cancel()
	out, err := authorize(ctx, g.op)
	if err != nil {
		reply.Error = ptr(err.Error())
		return reply, nil
	}
	if out.Ticket != "" {
		reply.Ticket = &out.Ticket
		return reply, nil
	}
	m, err := recordRaw(out.Result)
	if err != nil || m == nil {
		return reply, fail(502, "Repository publisher returned an invalid result")
	}
	if g.source != "" {
		m["sourceExecutionId"] = jsonBytes(g.source)
	}
	if g.request != "" {
		m["requestId"] = jsonBytes(g.request)
	}
	if g.op.Action == "record_code_review" {
		m["artifactId"], m["artifactSha256"], m["fleetActor"] = jsonBytes(evidence.ArtifactId), jsonBytes(evidence.ArtifactSha256), jsonBytes(evidence.Actor)
		m["githubActor"], m["independentReview"] = jsonBytes(projectpublisher.Actor), jsonBytes(evidence.Independent)
		m["reviewExecutionId"], m["onBehalfOf"] = jsonBytes(evidence.ReviewExecutionId), jsonBytes(evidence.OnBehalfOf)
	}
	reply.Result = jsonBytes(m)
	return reply, nil
}

type appPublishingGrant struct {
	op              projectpublisher.Operation
	source, request string
	review          contract.ForgeReviewRequest
}

// The coordinator derives authority from the dispatched command and current project grant. The executor
// supplies a publication's content and expected head, never its repo or branch.
func appPublishingScope(q *Request, b *contract.PublishingAppRequest) (appPublishingGrant, error) {
	var g appPublishingGrant
	c, err := commandOf(q, b.HostId, b.ExecutionId, b.CommandId, "Publisher command rejected")
	if err != nil {
		return g, err
	}
	hostJob := c.Kind == string(contract.CommandKindPublishing)
	if c.Status != "accepted" && c.Status != "started" &&
		!(hostJob && c.Status == "pending" && isTrue(c.HandedOut)) {
		return g, fail(409, "Publisher command is inactive")
	}
	op := &g.op
	op.Action, op.Base, op.Head = b.Action, "main", deref(b.ExpectedHeadSha)
	op.Title, op.Body, op.Description, op.Lease = deref(b.Title), deref(b.Body), deref(b.Description), deref(b.Lease)
	artifactID, projectID, reviewCommand := deref(b.ArtifactId), c.ProjectID, ""
	g.request = deref(b.RequestId)
	var e agentExecution
	var person *Person
	var start contract.StartPayload
	var payload contract.PublishingCommandPayload
	switch contract.CommandKind(c.Kind) {
	case contract.CommandKindStart, contract.CommandKindInput:
		e, err = getExecution(q, c.ExecutionID)
		if err != nil {
			return g, err
		}
		if json.Unmarshal(c.Payload, &start) != nil || c.SessionID == "" || e.SessionID != c.SessionID ||
			(start.Branch != nil && !*start.Branch) {
			return g, fail(403, "Publisher start requires a writable managed branch")
		}
		if op.Action != "status" {
			var flight contract.ProjectExecution
			if isTrue(start.ReadOnly) || json.Unmarshal(start.Project["execution"], &flight) != nil || !isTrue(flight.Forge) {
				return g, fail(403, "Execution start does not grant publishing")
			}
		}
		projectID = firstString(e.dispatchProject(), e.ProjectID, deref(start.ProjectId))
		if (c.ProjectID != "" && c.ProjectID != projectID) || (start.ProjectId != nil && *start.ProjectId != projectID) {
			return g, fail(403, "Publisher start project binding changed")
		}
		g.source, reviewCommand = e.ID, c.ID
	case contract.CommandKindPublishing:
		if json.Unmarshal(c.Payload, &payload) != nil || payload.Action != b.Action || payload.ProjectId != projectID ||
			payload.BaseBranch != op.Base || payload.Host != projectpublisher.Host || payload.Provider != "github" ||
			payload.Repo != projectpublisher.Repo || op.Lease != "" {
			return g, fail(403, "Publishing command does not match the repository operation")
		}
		if payload.ExpectedHeadSha != nil && *payload.ExpectedHeadSha != op.Head {
			return g, fail(403, "Publishing command head changed")
		}
		job, err := getExecution(q, c.ExecutionID)
		if err != nil {
			return g, err
		}
		person = q.peopleSnapshot().person(job.DirectedBy)
		if !mayAccessProjectID(person, projectID) {
			return g, fail(403, "Operator no longer has publishing project access")
		}
		g.source, g.request = deref(payload.SourceExecutionId), payload.RequestId
		op.Branch, op.Title, op.Body, op.Description = deref(payload.Branch), deref(payload.Title), deref(payload.Body), deref(payload.Description)
		if payload.Number != nil {
			op.Number = *payload.Number
		}
		artifactID = deref(payload.ArtifactId)
	default:
		return g, fail(403, "Publisher command kind rejected")
	}
	settings, prefix, err := loadPublishingSettings(q, projectID)
	if err != nil {
		return g, err
	}
	ref, ok := forgeSelectedRepoRef(settings.runSettings, b.HostId)
	if projectID != "fleet" || settings.Execution == nil || !settings.Execution.Forge || !ok || !projectpublisher.Required(ref) {
		return g, fail(403, "Project does not grant the repository publisher")
	}
	if !slices.Contains([]string{"status", "publish_draft", "record_code_review", "mark_ready", "request_merge", "cancel_merge_request"}, op.Action) ||
		(op.Head != "" && !forgeSHAPattern.MatchString(op.Head)) || op.Number < 0 {
		return g, fail(400, "Invalid publishing action or head")
	}
	op.MergeMethod = "squash"
	if settings.Integration != nil && settings.Integration.MergeMethod != "" {
		op.MergeMethod = settings.Integration.MergeMethod
	}
	if (b.MergeMethod != nil && *b.MergeMethod != op.MergeMethod) || (hostJob && payload.MergeMethod != op.MergeMethod) {
		return g, fail(403, "Merge method differs from project integration policy")
	}
	if op.Action == "request_merge" && (settings.Integration == nil || settings.Integration.Mode != "pr" ||
		!settings.Integration.AutoMerge || (hostJob && !payload.AutoMerge)) {
		return g, fail(403, "Project auto-merge not granted")
	}
	row, err := q.db().HostObservationOf(q.ctx(), b.HostId)
	if err != nil {
		return g, err
	}
	var obs hostPollObservationPayload
	if json.Unmarshal(row.Payload, &obs) != nil || obs.Capabilities == nil ||
		!contract.HasCapability(*obs.Capabilities, projectpublisher.Capability) {
		return g, fail(409, "Host has no verified repository App capability")
	}
	// The coordinator already authorized this operator's veto. An open PR can outlive its session or task.
	if hostJob && op.Action == "cancel_merge_request" {
		if op.Branch != "" && !validForgeBranch(op.Branch) {
			return g, fail(400, "Publishing branch invalid")
		}
		return g, nil
	}
	if !hostJob {
		op.Branch = prefix + "/" + contract.WorkspaceSession(c.SessionID, deref(start.WorkspaceKey))
	} else if g.source != "" {
		e, err = getExecution(q, g.source)
	} else if op.Action != "publish_draft" && op.Action != "record_code_review" {
		e, err = appBranchExecution(q, prefix, op.Branch)
		g.source = e.ID
	}
	if err != nil {
		return g, err
	}
	if e.ID == "" || e.SessionID == "" || firstString(e.dispatchProject(), e.ProjectID) != projectID ||
		!validForgeBranch(op.Branch) || (hostJob && op.Branch != publicationExecutionBranch(prefix, e)) {
		return g, fail(403, "Publishing source does not match the managed branch")
	}
	session, err := sessionByID(q, e.SessionID)
	if err != nil {
		return g, err
	}
	if session.ProjectID != projectID || (session.Branch != nil && !*session.Branch) ||
		session.ArchivedAt != nil || session.DirectorAccessRevokedAt != nil ||
		(op.Action != "status" && session.Access == "read") {
		return g, fail(403, "Publishing requires a writable session branch")
	}
	if (!hostJob || op.Action == "publish_draft" || op.Action == "record_code_review") && deref(e.HostID) != b.HostId {
		return g, fail(403, "Publishing source is not on PDS18")
	}
	if !hostJob || op.Action == "record_code_review" {
		if e.State != "running" || e.unconfirmed() || isTrue(e.StopRequested) || isTrue(e.TaskStopRequested) {
			return g, fail(409, "Publishing requires an active source execution")
		}
		actor, err := forgeResolveActor(q, e, projectID, q.host.Holder)
		if err != nil {
			return g, err
		}
		if !hostJob {
			person = actor
		}
	}
	if op.Action != "status" {
		dispatch, err := executionGetAs[dispatchExecution](q, e.ID)
		if err != nil {
			return g, err
		}
		if !publishes(&dispatch) {
			return g, fail(403, "Execution is not authorized to publish")
		}
	}
	profile, _ := profileOf(e.AgentConfig)
	if fleetDesigner(projectID, profile.ID) || (len(settings.Execution.Profiles) != 0 && !slices.Contains(settings.Execution.Profiles, profile.ID)) {
		return g, fail(403, "Execution profile is not granted publishing access")
	}
	frozen, pinnedPrefix, err := forgeProjectSettings(q, e, projectID)
	if err != nil {
		return g, err
	}
	pinnedRef, ok := forgeSelectedRepoRef(frozen, deref(e.HostID))
	if !ok || !projectpublisher.Required(pinnedRef) || pinnedPrefix != prefix || frozen.Execution == nil || !frozen.Execution.Forge ||
		(len(frozen.Execution.Profiles) != 0 && !slices.Contains(frozen.Execution.Profiles, profile.ID)) {
		return g, fail(403, "Repository publisher is not granted by the execution snapshot")
	}
	admission := *q
	admission.person = person
	if err := checkPublishingDesign(&admission, projectID, e, op.Action, ref); err != nil {
		return g, err
	}
	if op.Action == "record_code_review" {
		if reviewCommand == "" {
			cs, err := q.db().CommandsOfExecution(q.ctx(), &g.source)
			if err != nil {
				return g, err
			}
			for _, cmd := range cs {
				if (cmd.Kind == "start" || cmd.Kind == "input") && (cmd.Status == "accepted" || cmd.Status == "started") {
					reviewCommand = cmd.ID
				}
			}
		}
		if reviewCommand == "" {
			return g, fail(409, "Review source command is unavailable")
		}
		g.review = contract.ForgeReviewRequest{HostId: b.HostId, ExecutionId: g.source, CommandId: reviewCommand, AgentToolKey: b.AgentToolKey,
			Repo: ref.Slug, Provider: &ref.Provider, Host: &ref.Host, Branch: op.Branch, HeadSha: op.Head, ArtifactId: artifactID,
			Description: op.Description, GithubActor: projectpublisher.Actor, RequestId: optStr(g.request), SourceExecutionId: &g.source}
	}
	return g, nil
}

func publicationExecutionBranch(prefix string, e agentExecution) string {
	var key string
	if e.Dispatch != nil {
		key = deref(e.Dispatch.WorkspaceKey)
	}
	return prefix + "/" + contract.WorkspaceSession(e.SessionID, key)
}

func appBranchExecution(q *Request, prefix, branch string) (agentExecution, error) {
	var none agentExecution
	sid, ok := strings.CutPrefix(branch, prefix+"/")
	if !ok || sid == "" || strings.Contains(sid, "/") {
		return none, fail(403, "Repository App publication requires a managed branch")
	}
	if i := strings.LastIndexByte(sid, '-'); i >= 0 && len(sid)-i == 65 {
		sid = sid[:i]
	}
	es, err := executionsAs[agentExecution](q, &sid)
	if err != nil {
		return none, err
	}
	for i := len(es) - 1; i >= 0; i-- {
		if deref(es[i].HostID) != "" && publicationExecutionBranch(prefix, es[i]) == branch {
			return es[i], nil
		}
	}
	return none, nil
}
