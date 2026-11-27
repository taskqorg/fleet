// Publishing blockers are Problems. Reports carry classified evidence, never
// provider output or credentials. A duty bot can ask for a scoped owner decision.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

const publishingBlockerSource = "publishing-blocker"

type deliveryBlocker struct {
	Provider    string `json:"provider,omitempty"`
	Host        string `json:"host,omitempty"`
	Repo        string `json:"repo,omitempty"`
	Branch      string `json:"branch"`
	Number      int64  `json:"number,omitempty"`
	Head        string `json:"headSha,omitempty"`
	Run         int64  `json:"runId,omitempty"`
	Attempt     int64  `json:"attempt,omitempty"`
	Job         int64  `json:"jobId,omitempty"`
	Task        string `json:"taskId"`
	Session     string `json:"sessionId"`
	Execution   string `json:"executionId"`
	Mediator    string `json:"mediatorHostId,omitempty"`
	Operation   string `json:"operation"`
	Reason      string `json:"reason"`
	Recovery    string `json:"recoveryId,omitempty"`
	Observation string `json:"observation,omitempty"`
}

type publishingBlockerData struct {
	Delivery deliveryBlocker `json:"delivery"`
}

func init() {
	op("publishing_report_blocker", publishingReportBlocker)
}

func publishingReportBlocker(q *Request, r *contract.PublishingReportBlockerRequest) (contract.ProblemGetReply, error) {
	var out contract.ProblemGetReply
	project, execution := deref(r.ProjectId), deref(r.SourceExecutionId)
	if execution == "" || q.agent != "execution:"+execution {
		return out, fail(403, "A blocker report must come from its source execution")
	}
	if project == "" || !mayAccessProjectID(q.person, project) {
		return out, errNotFound
	}
	e, err := getExecution(q, execution)
	if err != nil {
		return out, err
	}
	if e.ProjectID != project || e.SessionID == "" || e.State != "running" || e.unconfirmed() ||
		isTrue(e.StopRequested) || isTrue(e.TaskStopRequested) {
		return out, fail(409, "Delivery blocker requires an active source execution")
	}
	s, err := sessionByID(q, e.SessionID)
	if err != nil {
		return out, err
	}
	tid := firstString(e.DeliveryID, e.TaskID, s.DeliveryID, s.TaskID)
	if tid == "" || s.ProjectID != project || s.Access == "read" || s.Branch != nil && !*s.Branch ||
		s.ArchivedAt != nil || s.DirectorAccessRevokedAt != nil {
		return out, fail(403, "Delivery blocker requires a task and a bound worktree")
	}
	task, err := taskLoad(q, tid)
	if err != nil {
		return out, err
	}
	if deref(task.ProjectID) != project || !deliveryTaskActive(task.State) {
		return out, fail(409, "Delivery blocker requires an active task in the project")
	}
	settings, prefix, err := forgeProjectSettings(q, e, project)
	if err != nil {
		return out, err
	}
	ref, bound := forgeSelectedRepoRef(settings, deref(e.HostID))
	d := deliveryBlocker{
		Provider: ref.Provider, Host: ref.Host, Repo: ref.Slug, Branch: prefix + "/" + s.ID,
		Number: valueOr(r.Number), Head: deref(r.ExpectedHeadSha), Run: valueOr(r.RunId),
		Attempt: valueOr(r.Attempt), Job: valueOr(r.JobId), Task: tid, Session: s.ID, Execution: e.ID,
		Mediator: deref(e.HostID), Operation: string(r.Operation), Reason: string(r.Reason),
	}
	if (d.Head != "" && !contract.ValidPublishingSHA(d.Head)) ||
		(d.Number > 0 && (!bound || d.Head == "")) ||
		(d.Run > 0 && (d.Number == 0 || (ref.Provider == "github" && d.Attempt == 0))) ||
		(d.Attempt > 0 && (d.Run == 0 || ref.Provider != "github")) ||
		(d.Job > 0 && (!bound || d.Head == "")) {
		return out, fail(400, "Expected exact head for delivery evidence and an observed attempt for a GitHub run")
	}
	if err := validateReportedDelivery(q, project, d, time.Time{}); err != nil {
		return out, err
	}
	p, err := unscoped(q, func() (db.Problem, error) {
		return raiseDeliveryBlocker(q, project, deref(task.Code), d)
	})
	if err != nil {
		return out, err
	}
	return contract.ProblemGetReply{Problem: problemOut(q, p)}, nil
}

func deliveryTaskActive(state db.TaskState) bool {
	return state == db.TaskStateInProgress || state == db.TaskStateWaiting
}

func (d deliveryBlocker) changeLabel() string {
	if d.Number <= 0 {
		return d.Repo
	}
	if d.Provider == "gitlab" {
		return fmt.Sprintf("%s MR !%d", d.Repo, d.Number)
	}
	return fmt.Sprintf("%s PR #%d", d.Repo, d.Number)
}

func (d deliveryBlocker) changeURL() string {
	if d.Number <= 0 {
		return ""
	}
	path := "pull"
	if d.Provider == "gitlab" {
		path = "-/merge_requests"
	}
	return fmt.Sprintf("https://%s/%s/%s/%d", d.Host, d.Repo, path, d.Number)
}

func (d deliveryBlocker) cause(project string) string {
	return "problem:publishing-blocker:" + sha256Text(fmt.Sprintf("%s|%s|%s|%s|%d|%s|%d|%d|%s|%s",
		project, d.Host, strings.ToLower(d.Repo), d.Branch, d.Number, d.Head, d.Run, d.Attempt, d.Operation, d.Reason))
}

func raiseDeliveryBlocker(q *Request, project, taskCode string, d deliveryBlocker) (db.Problem, error) {
	label := strings.ReplaceAll(d.Operation, "_", " ") + ": " + strings.ReplaceAll(d.Reason, "_", " ")
	if change := d.changeLabel(); change != "" {
		label += " on " + change
	}
	if taskCode != "" {
		label += " (" + taskCode + ")"
	}
	id, err := problemRaiseID(q, publishingBlockerSource, Problem{
		Cause: d.cause(project), Text: label, ProjectID: project, RefreshText: true,
		Data: publishingBlockerData{Delivery: d},
	})
	if err != nil {
		return db.Problem{}, err
	}
	return q.db().ProblemGet(q.ctx(), id)
}

// A report can precede the next provider snapshot. Reject a known different
// branch at intake, but let the duty bot see a blocker on a just-pushed head.
func validateReportedDelivery(q *Request, project string, d deliveryBlocker, reportedAt time.Time) error {
	snap, err := savedDeliverySnapshot(q)
	if err != nil {
		return err
	}
	if repo, ok := snap.Repos[d.repositoryKey()]; ok &&
		slices.Contains(repo.Projects, project) {
		if change, ok := repo.CI.Changes[d.Number]; ok && change.Branch != "" && change.Branch != d.Branch {
			return fail(409, "The reported delivery belongs to another branch")
		}
		if !reportedAt.IsZero() && repo.CI.conflicts(d, !repo.CI.ReadAt.Before(reportedAt)) {
			return fail(409, "The reported delivery head or run is stale")
		}
	}
	return nil
}

// A scoped answer is useful evidence for the bot. It cannot alter grants or run
// provider operations. The existing publishing operations still fence every write.
type deliveryAuthorityQuestion struct {
	ProblemID string                             `json:"problemId"`
	Problem   string                             `json:"problem"`
	Decision  contract.DeliveryAuthorityDecision `json:"deliveryAuthority"`
	Policy    string                             `json:"deliveryPolicy"`
	Target    deliveryBlocker                    `json:"deliveryTarget"`
}

func deliveryAuthorityOf(raw []byte) (deliveryAuthorityQuestion, bool) {
	var d deliveryAuthorityQuestion
	err := json.Unmarshal(raw, &d)
	return d, err == nil && d.Policy != "" && d.ProblemID != ""
}

func deliveryPolicy(q *Request, project, ownerBot string, d deliveryBlocker) (string, error) {
	s, prefix, err := loadPublishingSettings(q, project)
	if err != nil {
		return "", err
	}
	var connection *apiConnection
	var name string
	var ready bool
	owner, _, _ := strings.Cut(d.Repo, "/")
	switch d.Provider {
	case "github":
		name, connection, ready, err = githubConnectionOf(q, owner)
	case "gitlab":
		var configured string
		for _, repo := range s.Repos {
			if ref, ok := contract.ParseRepoRemote(repo.Remote, repo.Forge, repo.Host); ok && ref.Slug == d.Repo && ref.Host == d.Host {
				configured = repo.Connection
				break
			}
		}
		name, connection, ready, err = gitlabConnectionOf(q, d.Host, owner, configured)
	}
	if err != nil {
		return "", err
	}
	source, err := sessionMaybe(q, d.Session)
	if err != nil {
		return "", err
	}
	bot, err := readBot(q, ownerBot)
	if err != nil {
		return "", err
	}
	version := func(email string) string {
		if p := q.peopleSnapshot().person(email); p != nil {
			return grantsVersion(p)
		}
		return ""
	}
	sourceGrant := ""
	if source != nil {
		sourceGrant = version(source.DirectedBy)
	}
	grant, _ := connGrantOf(connection)
	var tools []string
	if connection != nil {
		tools = connection.Tools
	}
	hostGrant, err := deliveryMediatorGrant(q, d, project)
	if err != nil {
		return "", err
	}
	return sha256Text(string(jsonBytes(s.Repos)) + string(jsonBytes(s.Environments)) +
		string(jsonBytes(s.Execution)) + string(jsonBytes(s.Integration)) + prefix +
		string(jsonBytes([]any{name, ready, grant.Access, grant.Holder, tools, sourceGrant, version(bot.Director), hostGrant}))), nil
}

func deliveryMediatorGrant(q *Request, d deliveryBlocker, project string) (string, error) {
	if d.Mediator == "" {
		return "", nil
	}
	h, err := q.db().HostObservationOf(q.ctx(), d.Mediator)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var payload hostPollObservationPayload
	if err := json.Unmarshal(h.Payload, &payload); err != nil {
		return "", err
	}
	ref := contract.RepoRef{Provider: d.Provider, Host: d.Host, Slug: d.Repo}
	return fmt.Sprintf("%t/%t/%t", hostHolderAllowsProject(q, deref(h.Holder), project),
		hostCapsHaveRepo(valueOr(payload.Capabilities), ref),
		slices.Contains(valueOr(payload.Capabilities), string(contract.CapabilityPublishingCiRerunV1))), nil
}

func deliveryAuthorityCurrent(q *Request, p db.Problem, data deliveryAuthorityQuestion) (bool, error) {
	if p.State != "open" || p.OwnerBotID == nil {
		return false, nil
	}
	policy, err := deliveryPolicy(q, deref(p.ProjectID), deref(p.OwnerBotID), data.Target)
	if errors.Is(err, errNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if policy != data.Policy {
		return false, nil
	}
	t, err := taskLoad(q, data.Target.Task)
	if errors.Is(err, errNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return deref(t.ProjectID) == deref(p.ProjectID) && deliveryTaskActive(t.State), nil
}

func reconcileDeliveryAuthority(q *Request) error {
	ps, err := q.db().OpenDeliveryEscalations(q.ctx())
	if err != nil {
		return err
	}
	for _, p := range ps {
		qu, err := q.db().TaskQuestion(q.ctx(), *p.QuestionID)
		if err != nil {
			return err
		}
		data, ok := deliveryAuthorityOf(qu.Data)
		if !ok {
			continue
		}
		current, err := deliveryAuthorityCurrent(q, p, data)
		if err != nil {
			return err
		}
		if !current {
			if err := withdrawDeliveryAuthority(q, p, data, "Delivery authority or task changed", true); err != nil {
				return err
			}
		}
	}
	return nil
}

func withdrawDeliveryAuthority(q *Request, p db.Problem, data deliveryAuthorityQuestion, reason string, keepOpen bool) error {
	if p.QuestionID == nil {
		return nil
	}
	if _, err := questionTransition(q, *p.QuestionID, questionEvent{To: "withdrawn",
		Withdrawal: jsonBytes(struct {
			Reason string `json:"reason"`
			At     int64  `json:"at"`
		}{reason, nowMs()})}); err != nil {
		return err
	}
	if keepOpen {
		if err := problemAnswerAfter(q, *p.QuestionID, jsonBytes(data)); err != nil {
			return err
		}
	}
	if _, err := settleTask(q, data.Target.Task, ""); err != nil && !errors.Is(err, errNotFound) {
		return err
	}
	return nil
}

func closeDeliveryBlocker(q *Request, p db.Problem, closeReason, reason string) error {
	if p.QuestionID != nil {
		qu, err := q.db().TaskQuestion(q.ctx(), *p.QuestionID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if data, ok := deliveryAuthorityOf(qu.Data); ok {
			if err := withdrawDeliveryAuthority(q, p, data, reason, false); err != nil {
				return err
			}
		}
	}
	return problemsCloseIDs(q, []string{p.ID}, closeReason)
}

func askDeliveryAuthority(q *Request, p db.Problem, r *contract.ProblemEscalateRequest, session, bot string) (string, error) {
	var d publishingBlockerData
	if err := json.Unmarshal(p.Data, &d); err != nil {
		return "", err
	}
	target, decision := d.Delivery, r.DeliveryAuthority
	if decision == nil || !deliveryAuthorityKind(r.Kind, decision.Authority) || len(r.Options) != 0 {
		return "", fail(400, "A delivery problem requires structured owner authority. Keep diagnostics and routine retries with Problems duty")
	}
	if !contract.ValidPublishingSHA(target.Head) && (target.Number > 0 || decision.Authority != "project_grant") {
		return "", fail(409, "An authority decision needs an exact delivery head")
	}
	title := strings.TrimSpace(r.Text)
	authority, choices, consequences, err := deliveryAuthorityCopy(title, *decision)
	if err != nil {
		return "", err
	}
	project := deref(p.ProjectID)
	if err := validateReportedDelivery(q, project, target, p.SeenAt); err != nil {
		return "", err
	}
	rows, err := q.db().OpenDeliveryProblems(q.ctx(), db.OpenDeliveryProblemsParams{Host: optStr(target.Host), Repo: optStr(target.Repo), ProjectID: &project})
	if err != nil {
		return "", err
	}
	for _, other := range rows {
		if other.QuestionID == nil {
			continue
		}
		var otherData publishingBlockerData
		if err := json.Unmarshal(other.Data, &otherData); err != nil {
			return "", err
		}
		t := otherData.Delivery
		if t.Branch != target.Branch || t.Head != target.Head || t.Number != target.Number {
			continue
		}
		open, err := q.db().ProblemQuestionOpen(q.ctx(), *other.QuestionID)
		if err != nil {
			return "", err
		}
		if open {
			return "", fail(409, "This delivery already has an open authority decision")
		}
	}
	policy, err := deliveryPolicy(q, project, bot, target)
	if err != nil {
		return "", err
	}
	code := problemCode(p.Seq)
	data := deliveryAuthorityQuestion{ProblemID: p.ID, Problem: code, Decision: *decision, Policy: policy, Target: target}
	task, err := taskLoad(q, target.Task)
	if err != nil {
		return "", err
	}
	if deref(task.ProjectID) != project || !deliveryTaskActive(task.State) {
		return "", fail(409, "The delivery task is no longer active")
	}
	botName, err := q.db().ProblemOwnerName(q.ctx(), bot)
	if err != nil {
		return "", err
	}
	text := fmt.Sprintf("%s\n\n%s · %s · %s → Fleet owner\n%s\nCheck: %s\n%s\nAttempts: %s\nStill unknown: %s\nWaiting: %s\n\n%s\nRecommended: %s",
		title, deref(task.Code), code, botName, p.Text, decision.Check, decision.Diagnosis, decision.Attempts, decision.Uncertainty,
		decision.Waiting, consequences, decision.Recommendation)
	text += "\nAuthority: " + authority
	if link := target.changeURL(); link != "" {
		text += "\n" + link
	}
	by := actor(q, "fleet")
	return askQuestionAs(q, questionAsk{
		Kind: "operator_decision", Text: text, Options: jsonBytes(deliveryAuthorityOptions{Choices: choices, DeliveryAuthority: true}),
		AskedBy: &by, ProjectID: project, TaskID: target.Task, SessionID: optStr(session), Data: jsonBytes(data),
		DirectToOwner: true,
	})
}

// The first choice is primary on every surface. Keep the recommendation and each
// consequence in the question body as Telegram and web use that same record.
type deliveryAuthorityOptions struct {
	Choices           []string `json:"choices"`
	DeliveryAuthority bool     `json:"deliveryAuthority"`
}

func deliveryAuthorityCopy(title string, d contract.DeliveryAuthorityDecision) (authority string, labels []string, consequences string, err error) {
	switch d.Authority {
	case "project_grant":
		authority = "Change a project grant"
	case "project_policy":
		authority = "Change a project delivery policy"
	case "external_authority":
		authority = "Act with authority held outside Fleet"
	case "product_decision":
		authority = "Choose the delivery outcome"
	default:
		return "", nil, "", fail(400, "Choose the owner authority needed")
	}
	for _, field := range []string{title, d.Check, d.Diagnosis, d.Attempts, d.Uncertainty, d.Waiting} {
		if !deliveryDecisionLine(field, 240) {
			return "", nil, "", fail(400, "Supply one concise line for each part of the authority decision, using entity codes")
		}
	}
	if len(d.Choices) < 2 || len(d.Choices) > 4 {
		return "", nil, "", fail(400, "Supply two to four choices and their consequences")
	}
	var lines []string
	for _, c := range d.Choices {
		if !deliveryDecisionLine(c.Label, 30) || !deliveryDecisionLine(c.Consequence, 200) || slices.Contains(labels, c.Label) {
			return "", nil, "", fail(400, "Supply distinct choice labels and one consequence per choice, using entity codes")
		}
		labels = append(labels, c.Label)
		lines = append(lines, c.Label+" — "+c.Consequence)
	}
	recommended := slices.Index(labels, d.Recommendation)
	if recommended < 0 {
		return "", nil, "", fail(400, "Recommend one of the listed choices")
	}
	if recommended > 0 {
		label, line := labels[recommended], lines[recommended]
		copy(labels[1:], labels[:recommended])
		copy(lines[1:], lines[:recommended])
		labels[0], lines[0] = label, line
	}
	return authority, labels, strings.Join(lines, "\n"), nil
}

func deliveryAuthorityKind(kind contract.ProblemEscalationKind, scope contract.DeliveryAuthorityKind) bool {
	switch scope {
	case "project_grant", "project_policy":
		return kind == "authority"
	case "external_authority":
		return kind == "authority" || kind == "only-you"
	case "product_decision":
		return kind == "product"
	default:
		return false
	}
}

var deliveryMachineID = regexp.MustCompile(`(?i)\b(?:[0-9a-f]{40}|[0-9a-f]{64}|[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12})\b`)

func deliveryDecisionLine(s string, size int) bool {
	return s == strings.TrimSpace(s) && s != "" && len([]rune(s)) <= size &&
		!strings.ContainsAny(s, "\r\n\t") && !deliveryMachineID.MatchString(s)
}

func checkDeliveryAuthorityAnswer(q *Request, id string, d deliveryAuthorityQuestion) error {
	p, err := q.db().ProblemGet(q.ctx(), d.ProblemID)
	if err != nil {
		return err
	}
	current, err := deliveryAuthorityCurrent(q, p, d)
	if err != nil {
		return err
	}
	if !current || deref(p.QuestionID) != id {
		return fail(409, "This delivery authority decision is stale")
	}
	return validateReportedDelivery(q, deref(p.ProjectID), d.Target, p.SeenAt)
}

func continueDeliveryAuthority(q *Request, asked db.Question, data deliveryAuthorityQuestion, answer string) error {
	sid := deref(asked.SessionID)
	if sid == "" {
		return fail(409, "The duty conversation is unavailable")
	}
	if err := acquireTxLock(q, "session:"+sid); err != nil {
		return err
	}
	s, err := sessionMaybe(q, sid)
	if err != nil {
		return err
	}
	if s == nil {
		return fail(409, "The duty conversation is unavailable")
	}
	p, err := q.db().ProblemGet(q.ctx(), data.ProblemID)
	if err != nil {
		return err
	}
	if s.BotID == "" || s.BotID != deref(p.OwnerBotID) || s.ArchivedAt != nil || s.DirectorAccessRevokedAt != nil {
		return fail(409, "The duty conversation can no longer continue this decision")
	}
	b, err := readBot(q, s.BotID)
	if err != nil {
		return err
	}
	if b.Paused {
		return fail(409, "The duty bot is paused")
	}
	person := q.peopleSnapshot().person(s.DirectedBy)
	if person == nil || !mayAccessProjectID(person, deref(p.ProjectID)) {
		return fail(409, "The duty bot lost access to this delivery project")
	}
	// Give the bot the exact recorded target privately. Any follow-up must read
	// current provider state and use the original, fenced publishing operation.
	body := fmt.Sprintf("Fleet owner decision for %s:\n%s\n\nAnswer: %s\n\nTarget evidence: %s\nRecheck the exact delivery and current grants before acting. This reply changes no grant and authorizes no action on a different head or run.",
		data.Problem, asked.Text, answer, jsonBytes(data.Target))
	if _, err := agentSubmit(q, &contract.MessageSendRequest{SessionId: &sid, RequestId: ptr("reply:" + asked.ID), Text: &body}, ""); err != nil {
		return err
	}
	wakeDispatchAfter(q)
	return q.db().ProblemsWoken(q.ctx(), []string{p.ID})
}
