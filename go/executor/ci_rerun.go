package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
)

type ciGitHub struct {
	base, token string
	ctx         context.Context
	do          func(*http.Request) (*http.Response, error)
}

type ciGitHubPull struct {
	Number int64       `json:"number"`
	State  string      `json:"state"`
	Head   ciGitHubRef `json:"head"`
	Base   ciGitHubRef `json:"base"`
}

type ciGitHubRef struct {
	SHA  string       `json:"sha"`
	Ref  string       `json:"ref"`
	Repo ciGitHubRepo `json:"repo"`
}

type ciGitHubRepo struct {
	ID       int64  `json:"id"`
	FullName string `json:"full_name"`
}

type ciGitHubRun struct {
	ID             int64          `json:"id"`
	WorkflowID     int64          `json:"workflow_id"`
	Number         int64          `json:"run_number"`
	Attempt        int64          `json:"run_attempt"`
	Event          string         `json:"event"`
	Branch         string         `json:"head_branch"`
	SHA            string         `json:"head_sha"`
	Path           string         `json:"path"`
	Status         string         `json:"status"`
	Conclusion     string         `json:"conclusion"`
	Repository     ciGitHubRepo   `json:"repository"`
	HeadRepository ciGitHubRepo   `json:"head_repository"`
	Pulls          []ciGitHubPull `json:"pull_requests"`
}

type ciResponse struct {
	status  int
	more    bool
	retryAt *int64
}

func (g ciGitHub) request(method, path string, dest any) ciResponse {
	req, err := http.NewRequestWithContext(g.ctx, method, g.base+path, nil)
	if err != nil {
		return ciResponse{}
	}
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.do(req)
	if err != nil {
		return ciResponse{}
	}
	defer resp.Body.Close()
	out := ciResponse{status: resp.StatusCode, more: strings.Contains(resp.Header.Get("Link"), `rel="next"`)}
	if resp.StatusCode == 429 || resp.StatusCode == 403 {
		now := time.Now()
		out.retryAt = ciRetryAt(resp.Header, now)
		if out.retryAt != nil {
			out.status = 429
		} else if resp.StatusCode == 429 {
			at := now.Add(time.Minute).UnixMilli()
			out.retryAt = &at
		}
	}
	if dest != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(dest); err != nil {
			out.status = 0
		}
	}
	return out
}

func ciRetryAt(headers http.Header, now time.Time) *int64 {
	after := headers.Get("Retry-After")
	if seconds, err := strconv.ParseInt(after, 10, 32); err == nil && seconds >= 0 {
		at := now.Add(time.Duration(seconds) * time.Second).UnixMilli()
		return &at
	}
	if t, err := http.ParseTime(after); err == nil {
		at := t.UnixMilli()
		return &at
	}
	if headers.Get("X-RateLimit-Remaining") == "0" {
		if seconds, err := strconv.ParseInt(headers.Get("X-RateLimit-Reset"), 10, 64); err == nil && seconds > now.Unix() {
			at := time.Unix(seconds, 0).UnixMilli()
			return &at
		}
	}
	return nil
}

func ciRefused(reason string) contract.PublishingCIObservation {
	return contract.PublishingCIObservation{State: contract.PublishingCIStateRefused, Reason: reason}
}

func ciReadFailure(response ciResponse, observe bool) contract.PublishingCIObservation {
	if response.status == 429 {
		state := contract.PublishingCIStateRateLimited
		if observe {
			state = contract.PublishingCIStateObserving
		}
		return contract.PublishingCIObservation{State: state, Reason: "provider_read_rate_limited", RetryAt: response.retryAt}
	}
	if response.status == http.StatusUnprocessableEntity {
		if observe {
			return contract.PublishingCIObservation{State: contract.PublishingCIStateObserving, Reason: "provider_jobs_unverified"}
		}
		return ciRefused("provider_jobs_unverified")
	}
	if observe {
		return contract.PublishingCIObservation{State: contract.PublishingCIStateObserving, Reason: "provider_read_unavailable"}
	}
	if response.status == 0 || response.status >= 500 {
		return contract.PublishingCIObservation{State: contract.PublishingCIStateRateLimited, Reason: "provider_read_unavailable"}
	}
	return ciRefused("provider_read_denied_or_missing")
}

func (run ciGitHubRun) boundRun(p contract.PublishingCommandPayload) bool {
	// GitHub returns the workflow path alone or followed by @ref. Before
	// claiming, the workflow endpoint must confirm the exact file for this ID.
	workflow, _, _ := strings.Cut(run.Path, "@")
	return run.ID == p.CiRecovery.RunId && run.WorkflowID > 0 && run.Number > 0 && run.Attempt > 0 &&
		run.Event == "pull_request" && run.Branch == str0(p.Branch) && run.SHA == str0(p.ExpectedHeadSha) &&
		workflow == p.CiRecovery.Policy.Workflow && run.Repository.ID > 0 &&
		run.HeadRepository.ID == run.Repository.ID && strings.EqualFold(run.Repository.FullName, p.Repo)
}

func (run ciGitHubRun) matches(p contract.PublishingCommandPayload) bool {
	if !run.boundRun(p) {
		return false
	}
	return slices.ContainsFunc(run.Pulls, func(pr ciGitHubPull) bool {
		return pr.Number == *p.Number && pr.Head.SHA == run.SHA && pr.Head.Ref == run.Branch &&
			pr.Head.Repo.ID == run.Repository.ID && pr.Base.Ref == p.BaseBranch && pr.Base.Repo.ID == run.Repository.ID &&
			contract.ValidPublishingSHA(pr.Base.SHA)
	})
}

func (g ciGitHub) jobs(run ciGitHubRun, attempt int64) ([]ciJob, ciResponse) {
	var page struct {
		Total int     `json:"total_count"`
		Jobs  []ciJob `json:"jobs"`
	}
	path := fmt.Sprintf("/actions/runs/%d/attempts/%d/jobs?per_page=100", run.ID, attempt)
	res := g.request(http.MethodGet, path, &page)
	if res.status != http.StatusOK {
		return nil, res
	}
	if res.more || page.Total != len(page.Jobs) {
		return nil, ciResponse{status: http.StatusUnprocessableEntity}
	}
	for _, job := range page.Jobs {
		if job.ID <= 0 || job.RunID != run.ID || (job.RunAttempt != 0 && job.RunAttempt != attempt) || job.HeadSHA != run.SHA {
			return nil, ciResponse{status: http.StatusUnprocessableEntity}
		}
	}
	return page.Jobs, res
}

func ciJobRed(job ciJob) bool {
	return job.Status == "completed" && (job.Conclusion == "failure" || job.Conclusion == "timed_out" || job.Conclusion == "startup_failure")
}

func (g ciGitHub) failedJobs(jobs []ciJob) []contract.PublishingCIFailedJob {
	var failed []contract.PublishingCIFailedJob
	for _, job := range jobs {
		if ciJobRed(job) {
			failed = append(failed, contract.PublishingCIFailedJob{Id: job.ID, HeadSha: job.HeadSHA, Name: str0(ciMetadataText(job.Name, g.token))})
		}
	}
	return failed
}

// recover verifies a failed workflow bound to an open same-repository PR. Only the holder of the
// coordinator's one-shot claim can send the POST; readback and replay never need that claim.
func (g ciGitHub) recover(p contract.PublishingCommandPayload, claim func([]contract.PublishingCIFailedJob) bool) (contract.PublishingCIObservation, bool) {
	in := p.CiRecovery
	head, branch := str0(p.ExpectedHeadSha), str0(p.Branch)
	if in == nil || p.Number == nil || *p.Number <= 0 || !contract.ValidPublishingSHA(head) ||
		!contract.ValidCIRerunPolicy(&in.Policy) || in.Policy.RepoId != str0(p.RepoId) ||
		str0(p.Prefix) == "" || !strings.HasPrefix(branch, *p.Prefix+"/") ||
		branch == p.BaseBranch || branch == "main" || strings.HasPrefix(branch, "gh-readonly-queue/") {
		return ciRefused("invalid_ci_target"), false
	}
	runPath := fmt.Sprintf("/actions/runs/%d", in.RunId)
	if in.ObserveOnly {
		var run ciGitHubRun
		res := g.request(http.MethodGet, runPath, &run)
		if res.status != http.StatusOK {
			return ciReadFailure(res, true), false
		}
		// The coordinator journals the PR association before entering readback.
		// GitHub may omit that association after the PR moves or closes.
		if !run.boundRun(p) {
			return contract.PublishingCIObservation{State: contract.PublishingCIStateObserving, Reason: "unverified_workflow_run"}, false
		}
		return g.readback(run, in), false
	}
	var pull ciGitHubPull
	res := g.request(http.MethodGet, fmt.Sprintf("/pulls/%d", *p.Number), &pull)
	if res.status != http.StatusOK {
		return ciReadFailure(res, in.ObserveOnly), false
	}
	if pull.State != "open" || pull.Number != *p.Number || pull.Head.Ref != branch || pull.Base.Ref != p.BaseBranch ||
		pull.Base.Repo.ID <= 0 || pull.Head.Repo.ID != pull.Base.Repo.ID || !strings.EqualFold(pull.Base.Repo.FullName, p.Repo) {
		return ciRefused("unbound_pull_request"), false
	}
	if pull.Head.SHA != head {
		return ciRefused("stale_head"), false
	}
	var run ciGitHubRun
	res = g.request(http.MethodGet, runPath, &run)
	if res.status != http.StatusOK {
		return ciReadFailure(res, in.ObserveOnly), false
	}
	if run.Event == "merge_group" || strings.HasPrefix(run.Branch, "gh-readonly-queue/") {
		return ciRefused("merge_queue_requires_clean_reentry"), false
	}
	if !run.matches(p) || run.Repository.ID != pull.Base.Repo.ID {
		return ciRefused("unverified_workflow_run"), false
	}
	var workflow struct {
		ID   int64  `json:"id"`
		Path string `json:"path"`
	}
	res = g.request(http.MethodGet, fmt.Sprintf("/actions/workflows/%d", run.WorkflowID), &workflow)
	if res.status != http.StatusOK {
		return ciReadFailure(res, false), false
	}
	if workflow.ID != run.WorkflowID || workflow.Path != in.Policy.Workflow {
		return ciRefused("unverified_workflow_run"), false
	}
	if run.Attempt > 1 {
		return g.readback(run, in), false
	}
	if run.Attempt != in.Attempt || run.Status != "completed" || run.Conclusion != "failure" {
		return ciRefused("run_is_not_current_failed_attempt"), false
	}
	// The event may use GitHub's PR merge ref. Require the same owner-approved workflow blob
	// at both sides of the provider's run association so the file itself cannot be changed by that merge.
	bases := []string{head}
	for _, pr := range run.Pulls {
		if pr.Number == pull.Number {
			bases = append(bases, pr.Base.SHA)
		}
	}
	for _, sha := range bases {
		if !contract.ValidPublishingSHA(sha) {
			return ciRefused("unverified_workflow_run"), false
		}
		var file struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		}
		res = g.request(http.MethodGet, "/contents/"+in.Policy.Workflow+"?ref="+sha, &file)
		if res.status != http.StatusOK {
			return ciReadFailure(res, false), false
		}
		if file.Type != "file" || file.SHA != in.Policy.WorkflowBlobSha {
			return ciRefused("workflow_policy_mismatch"), false
		}
	}
	jobs, res := g.jobs(run, run.Attempt)
	if res.status != http.StatusOK {
		return ciReadFailure(res, false), false
	}
	original := g.failedJobs(jobs)
	if !slices.ContainsFunc(original, func(j contract.PublishingCIFailedJob) bool {
		return slices.Contains(in.Policy.RequiredJobs, j.Name)
	}) {
		return ciRefused("no_failed_required_job"), false
	}
	var history struct {
		Total int           `json:"total_count"`
		Runs  []ciGitHubRun `json:"workflow_runs"`
	}
	historyPath := fmt.Sprintf("/actions/workflows/%d/runs?event=pull_request&head_sha=%s&per_page=100", run.WorkflowID, head)
	res = g.request(http.MethodGet, historyPath, &history)
	if res.status != http.StatusOK {
		return ciReadFailure(res, false), false
	}
	if res.more || history.Total != len(history.Runs) || !slices.ContainsFunc(history.Runs, func(r ciGitHubRun) bool { return r.ID == run.ID }) {
		return ciRefused("incomplete_workflow_history"), false
	}
	var external ciGitHubRun
	newer := false
	for _, other := range history.Runs {
		if other.ID == run.ID || other.SHA != head || other.Branch != branch {
			continue
		}
		otherInput := *in
		otherInput.RunId = other.ID
		otherTarget := p
		otherTarget.CiRecovery = &otherInput
		if !other.matches(otherTarget) || other.Repository.ID != run.Repository.ID || other.WorkflowID != run.WorkflowID {
			return ciRefused("unverified_workflow_history"), false
		}
		if other.Attempt > 1 && other.Number > external.Number {
			external = other
		}
		newer = newer || other.Number >= run.Number
	}
	if external.ID != 0 {
		out := g.readback(external, in)
		out.RunId = &external.ID
		return out, false
	}
	if newer {
		return ciRefused("newer_run_exists"), false
	}
	var live struct {
		Name   string `json:"name"`
		Commit struct {
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	res = g.request(http.MethodGet, "/branches/"+url.PathEscape(branch), &live)
	if res.status != http.StatusOK {
		return ciReadFailure(res, false), false
	}
	if live.Name != branch || live.Commit.SHA != head {
		return ciRefused("stale_head"), false
	}
	var latest ciGitHubRun
	res = g.request(http.MethodGet, runPath, &latest)
	if res.status != http.StatusOK {
		return ciReadFailure(res, false), false
	}
	if !latest.matches(p) || latest.Repository.ID != pull.Base.Repo.ID || latest.WorkflowID != run.WorkflowID {
		return ciRefused("unverified_workflow_run"), false
	}
	if latest.Attempt > 1 {
		return g.readback(latest, in), false
	}
	if latest.Status != "completed" || latest.Conclusion != "failure" {
		return ciRefused("run_is_not_current_failed_attempt"), false
	}
	var current ciGitHubPull
	res = g.request(http.MethodGet, fmt.Sprintf("/pulls/%d", *p.Number), &current)
	if res.status != http.StatusOK {
		return ciReadFailure(res, false), false
	}
	if current.State != "open" || current.Number != pull.Number || current.Head != pull.Head ||
		current.Base.Ref != pull.Base.Ref || current.Base.Repo != pull.Base.Repo {
		return ciRefused("stale_head"), false
	}
	if !claim(original) {
		// No acknowledgement means no POST and no contradictory observation. The ledger owns recovery.
		return contract.PublishingCIObservation{}, false
	}
	res = g.request(http.MethodPost, runPath+"/rerun-failed-jobs", nil)
	out := contract.PublishingCIObservation{State: contract.PublishingCIStateDispatchUnknown, Reason: "provider_dispatch_unconfirmed", Attempt: &run.Attempt}
	switch res.status {
	case http.StatusCreated:
		out.State, out.Reason = contract.PublishingCIStateDispatched, "provider_acknowledged"
	case http.StatusTooManyRequests:
		out.State, out.Reason, out.RetryAt = contract.PublishingCIStateRateLimited, "provider_post_rate_limited", res.retryAt
	case http.StatusForbidden, http.StatusUnauthorized, http.StatusNotFound:
		out.State, out.Reason = contract.PublishingCIStateHandoff, "provider_dispatch_denied"
	}
	return out, true
}

func (g ciGitHub) readback(run ciGitHubRun, in *contract.PublishingCICommand) contract.PublishingCIObservation {
	out := contract.PublishingCIObservation{State: contract.PublishingCIStateObserving, Reason: "replacement_attempt_pending", Attempt: &run.Attempt, RunId: &run.ID}
	if run.Attempt == 1 || run.Attempt < in.Attempt {
		return out
	}
	original, res := g.jobs(run, 1)
	if res.status != http.StatusOK {
		return ciReadFailure(res, true)
	}
	replacement, res := g.jobs(run, run.Attempt)
	if res.status != http.StatusOK {
		return ciReadFailure(res, true)
	}

	// The attempt endpoint lists only jobs in that attempt. Required jobs that passed
	// on attempt one can legitimately be absent from a failed-jobs rerun.
	remaining := map[string]int{}
	for _, job := range original {
		if job.Conclusion != "success" && slices.Contains(in.Policy.RequiredJobs, job.Name) {
			remaining[job.Name]++
		}
	}
	hadRequired := len(remaining) != 0
	var failed []ciJob
	pending := false
	for _, job := range replacement {
		if !slices.Contains(in.Policy.RequiredJobs, job.Name) {
			continue
		}
		switch {
		case ciJobRed(job):
			failed = append(failed, job)
		case job.Status == "completed" && job.Conclusion == "success":
			if remaining[job.Name] > 1 {
				remaining[job.Name]--
			} else {
				delete(remaining, job.Name)
			}
		default:
			pending = true
		}
	}
	switch {
	case len(failed) > 0:
		out.State, out.Reason = contract.PublishingCIStateHandoff, "replacement_required_jobs_failed"
		out.FailedJobs = g.failedJobs(failed)
	case hadRequired && len(remaining) == 0 && !pending:
		out.State, out.Reason = contract.PublishingCIStatePassed, "replacement_required_jobs_passed"
	case run.Status == "completed":
		out.State, out.Reason = contract.PublishingCIStateHandoff, "replacement_required_jobs_unconfirmed"
		if run.Conclusion == "cancelled" {
			out.Reason = "replacement_attempt_cancelled"
		}
	default:
		return out
	}
	out.OriginalJobs = g.failedJobs(original)
	if !in.Dispatched || run.Attempt != 2 {
		out.Reason += "_attribution_unknown"
	}
	return out
}

func (e *executor) runCIRerunCommand(cmd contract.Command, execution, cid string) error {
	p := *cmd.Publishing.Payload
	ctx := forgeContext{Provider: p.Provider, Host: p.Host, Repo: p.Repo,
		Store: os.Getenv("FLEET_FORGE_CONFIG_DIR"), CredentialHome: os.Getenv("HOME")}
	ref, ok := ctx.resolvedRef()
	out := ciRefused("host_ci_grant_unavailable")
	if p.CiRecovery.ObserveOnly {
		out = contract.PublishingCIObservation{State: contract.PublishingCIStateObserving, Reason: "host_ci_grant_unavailable"}
	}
	claimed := false
	if ok && contract.CIRerunProvider(ref) && forgeRepoGrantedRef(ref) && ctx.hasCredentialStore(ref.Provider) {
		if token := forgeCredential(ctx, ref.Provider, ref.Host); token != "" {
			runCtx, cancel := context.WithTimeout(e.stopCtx, 50*time.Second)
			defer cancel()
			g := ciGitHub{base: "https://api.github.com/repos/" + ref.Slug, ctx: runCtx, token: token, do: httpClient(probeWait).Do}
			out, claimed = g.recover(p, func(jobs []contract.PublishingCIFailedJob) bool {
				b := contract.PublishingCIClaim{HostId: e.host, CommandId: cid, ExecutionId: execution,
					RecoveryId: p.CiRecovery.Id, OriginalJobs: jobs}
				a := sendWithClient(runCtx, outboxClient, e.url, e.token, contract.RoutePublishingCiClaim.Path, contract.RoutePublishingCiClaim.Path, b)
				r := typedRaw[contract.OkReply](a.value)
				return a.kind == answerOK && r.Ok
			})
		}
	}
	return e.inTx(func() error {
		if out.State != "" {
			b := contract.PublishingCIReport{HostId: e.host, CommandId: cid, ExecutionId: execution,
				RecoveryId: p.CiRecovery.Id, Claimed: claimed, Observation: out}
			if err := queueTo(e.ex, contract.RoutePublishingCiObservation, b); err != nil {
				return err
			}
		}
		return e.finish(cmd, "completed", "")
	})
}

func publishingCIProxy(name string, args json.RawMessage, ctx forgeContext) (any, error) {
	if ctx.Journal == "" || ctx.CommandId == "" || ctx.ExecutionId == "" {
		return nil, errors.New("CI recovery requires a bound Fleet execution")
	}
	if name == "rerun_failed" {
		if ctx.ReadOnly || ctx.Branch == "" || ctx.Branch != ctx.SessionBranch {
			return nil, errors.New("CI recovery requires a granted execution branch")
		}
		if exactKeys(args, "requestId", "number", "expectedHeadSha", "runId", "attempt", "reason") != nil {
			return nil, errors.New("invalid CI recovery arguments")
		}
		r, err := contractOf[contract.PublishingRerunFailedRequest](decRaw(args))
		if err != nil || r.ProjectId != nil || r.SourceExecutionId != nil {
			return nil, errors.New("invalid CI recovery arguments")
		}
	}
	db, err := openJournal(ctx.Journal)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	operation := "publishing_" + name
	delegation := &delegationContext{HostId: &ctx.HostId, CommandId: &ctx.CommandId,
		ExecutionId: jsonValue{Set: true, V: ctx.ExecutionId}, Operations: []fleetOperation{{Name: operation}}}
	return delegationCall(operation, decRaw(args), delegation, db)
}
