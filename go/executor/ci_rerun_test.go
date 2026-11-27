package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

// T-FLT-142. The provider fixture exercises the read/claim/POST boundary against real HTTP
// paths, including eventual provider readback and ambiguous dispatches.
func TestCIRerunGitHub(t *testing.T) {
	head, base, blob := strings.Repeat("a", 40), strings.Repeat("b", 40), strings.Repeat("c", 40)
	branch, repo := "fleet/session", "example/repo"
	policy := contract.PublishingCIRerunPolicy{RepoId: "main", Workflow: ".github/workflows/ci.yml", WorkflowBlobSha: blob,
		RequiredJobs: []string{"go", "app"}, Profiles: []string{"worker"}}
	boundRepo := ciGitHubRepo{ID: 1, FullName: repo}
	pull := ciGitHubPull{Number: 17, State: "open", Head: ciGitHubRef{head, branch, boundRepo}, Base: ciGitHubRef{base, "main", boundRepo}}
	failedRun := ciGitHubRun{ID: 41, WorkflowID: 3, Number: 82, Attempt: 1, Event: "pull_request", Branch: branch, SHA: head,
		Path: policy.Workflow, Status: "completed", Conclusion: "failure", Repository: boundRepo, HeadRepository: boundRepo, Pulls: []ciGitHubPull{pull}}
	for _, tc := range []struct {
		name                               string
		state                              contract.PublishingCIState
		reason                             string
		change                             func(*ciRerunFixture)
		observe, acknowledged, refuseClaim bool
		wantPosts, wantClaims              int
	}{
		{name: "acknowledged once", state: "dispatched", reason: "provider_acknowledged", wantPosts: 1, wantClaims: 1},
		{name: "GitHub workflow path with ref suffix", state: "dispatched", reason: "provider_acknowledged", wantPosts: 1, wantClaims: 1,
			change: func(f *ciRerunFixture) { f.run.Path += "@refs/pull/17/merge"; f.history = []ciGitHubRun{f.run} }},
		{name: "stale head", state: "refused", reason: "stale_head", change: func(f *ciRerunFixture) { f.pull.Head.SHA = base }},
		{name: "head moves in preflight", state: "refused", reason: "stale_head", change: func(f *ciRerunFixture) { f.moveHead = true }},
		{name: "merge queue", state: "refused", reason: "merge_queue_requires_clean_reentry", change: func(f *ciRerunFixture) { f.run.Event = "merge_group" }},
		{name: "fork", state: "refused", reason: "unbound_pull_request", change: func(f *ciRerunFixture) { f.pull.Head.Repo.ID++ }},
		{name: "another workflow", state: "refused", reason: "unverified_workflow_run", change: func(f *ciRerunFixture) { f.run.Path = ".github/workflows/release.yml" }},
		{name: "workflow filename with an at sign cannot impersonate the grant", state: "refused", reason: "unverified_workflow_run",
			change: func(f *ciRerunFixture) {
				f.workflowPath += "@impostor.yml"
				f.run.Path = f.workflowPath + "@refs/pull/17/merge"
			}},
		{name: "workflow changed at tested revision", state: "refused", reason: "workflow_policy_mismatch", change: func(f *ciRerunFixture) { f.blob = base }},
		{name: "provider rejected POST with 429", state: "rate_limited", reason: "provider_post_rate_limited", wantPosts: 1, wantClaims: 1, change: func(f *ciRerunFixture) { f.postStatus = 429 }},
		{name: "provider unknown after POST", state: "dispatch_unknown", reason: "provider_dispatch_unconfirmed", wantPosts: 1, wantClaims: 1, change: func(f *ciRerunFixture) { f.postStatus = 502 }},
		{name: "rate limited on read", state: "rate_limited", reason: "provider_read_rate_limited", change: func(f *ciRerunFixture) { f.getStatus = 429 }},
		{name: "dispatch claim not acknowledged", refuseClaim: true, wantClaims: 1},
		{name: "lost host observes pending attempt", observe: true, state: "observing", reason: "replacement_attempt_pending"},
		{name: "lost host observes green attempt", observe: true, state: "passed", reason: "replacement_required_jobs_passed_attribution_unknown",
			change: func(f *ciRerunFixture) {
				f.run.Attempt, f.run.Conclusion, f.replacementConclusion = 2, "success", "success"
			}},
		{name: "merged PR still reconciles", observe: true, acknowledged: true, state: "passed", reason: "replacement_required_jobs_passed",
			change: func(f *ciRerunFixture) {
				f.pull.State, f.run.Attempt, f.replacementConclusion = "closed", 2, "success"
				f.run.Pulls = nil
			}},
		{name: "moved head still reconciles original second red", observe: true, acknowledged: true, state: "handoff", reason: "replacement_required_jobs_failed",
			change: func(f *ciRerunFixture) { f.pull.Head.SHA, f.run.Attempt = base, 2 }},
		{name: "non-required failure leaves required recovery green", observe: true, acknowledged: true, state: "passed", reason: "replacement_required_jobs_passed",
			change: func(f *ciRerunFixture) { f.run.Attempt, f.replacementConclusion = 2, "success"; f.reportFails = true }},
		{name: "missing read credential stays unknown", observe: true, state: "observing", reason: "provider_read_unavailable",
			change: func(f *ciRerunFixture) { f.getStatus = 403 }},
		{name: "cancelled replacement", observe: true, acknowledged: true, state: "handoff", reason: "replacement_attempt_cancelled",
			change: func(f *ciRerunFixture) {
				f.run.Attempt, f.run.Conclusion, f.replacementConclusion = 2, "cancelled", "cancelled"
			}},
		{name: "second red handoff", observe: true, acknowledged: true, state: "handoff", reason: "replacement_required_jobs_failed",
			change: func(f *ciRerunFixture) { f.run.Attempt = 2 }},
		{name: "external retry remains observation-only when jobs throttle", state: "observing", reason: "provider_read_rate_limited",
			change: func(f *ciRerunFixture) { f.run.Attempt, f.jobsStatus = 2, 429 }},
		{name: "cancelled original required job is still unresolved", observe: true, acknowledged: true, state: "handoff", reason: "replacement_required_jobs_unconfirmed",
			change: func(f *ciRerunFixture) {
				f.run.Attempt, f.replacementConclusion, f.originalAppCancelled = 2, "success", true
			}},
		{name: "required retry times out", observe: true, acknowledged: true, state: "handoff", reason: "replacement_required_jobs_failed",
			change: func(f *ciRerunFixture) { f.run.Attempt, f.replacementConclusion = 2, "timed_out" }},
		{name: "external retry already running", state: "observing", reason: "replacement_attempt_pending",
			change: func(f *ciRerunFixture) { f.run.Attempt, f.run.Status = 2, "in_progress" }},
		{name: "external retry races preflight", state: "observing", reason: "replacement_attempt_pending",
			change: func(f *ciRerunFixture) { f.raceRetry = true }},
		{name: "another external run used the round", state: "observing", reason: "replacement_attempt_pending",
			change: func(f *ciRerunFixture) {
				other := f.run
				other.ID, other.Attempt, other.Status = 40, 2, "in_progress"
				f.history = []ciGitHubRun{f.run, other}
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := ciRerunFixture{t: t, pull: pull, run: failedRun, blob: blob, workflowPath: policy.Workflow, postStatus: 201, history: []ciGitHubRun{failedRun}}
			if tc.change != nil {
				tc.change(&f)
			}
			s := httptest.NewServer(http.HandlerFunc(f.serve))
			defer s.Close()
			p := contract.PublishingCommandPayload{Number: &pull.Number, ExpectedHeadSha: &head, Branch: &branch, Prefix: testString("fleet"),
				BaseBranch: "main", Repo: repo, RepoId: &policy.RepoId,
				CiRecovery: &contract.PublishingCICommand{Policy: policy, RunId: failedRun.ID, Attempt: 1,
					ObserveOnly: tc.observe, Dispatched: tc.acknowledged}}
			g := ciGitHub{base: s.URL, token: "invented-fixture-token", ctx: context.Background(), do: s.Client().Do}
			claims := 0
			out, claimed := g.recover(p, func(jobs []contract.PublishingCIFailedJob) bool {
				claims++
				if len(jobs) != 1 || jobs[0].Id != 101 || jobs[0].Name != "go" || jobs[0].HeadSha != head {
					t.Errorf("original evidence = %#v", jobs)
				}
				return !tc.refuseClaim
			})
			if out.State != tc.state || out.Reason != tc.reason || claimed != (tc.wantPosts != 0) ||
				claims != tc.wantClaims || f.posts != tc.wantPosts {
				t.Fatalf("observation=%+v claims=%d posts=%d", out, claims, f.posts)
			}
			if (tc.state == "rate_limited" || tc.reason == "provider_read_rate_limited") && (out.RetryAt == nil || *out.RetryAt <= time.Now().UnixMilli()) {
				t.Fatalf("retry timestamp missing: %+v", out)
			}
			if tc.observe && f.pulls != 0 {
				t.Fatal("observation depends on current PR state")
			}
			if tc.reason == "replacement_required_jobs_failed" && (len(out.FailedJobs) != 1 || out.FailedJobs[0].Id != 102 || out.Attempt == nil || *out.Attempt != 2) {
				t.Fatalf("replacement evidence missing: %+v", out)
			}
		})
	}
}

func testString(s string) *string { return &s }

type ciRerunFixture struct {
	workflowPath          string
	jobsStatus            int
	originalAppCancelled  bool
	t                     *testing.T
	pull                  ciGitHubPull
	run                   ciGitHubRun
	history               []ciGitHubRun
	blob                  string
	getStatus, postStatus int
	posts, pulls, reads   int
	moveHead, raceRetry   bool
	replacementConclusion string
	reportFails           bool
}

func (f *ciRerunFixture) serve(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer invented-fixture-token" {
		f.t.Error("provider request lost its scoped credential")
	}
	if r.Method == http.MethodPost {
		f.posts++
		if r.URL.Path != "/actions/runs/41/rerun-failed-jobs" || r.ContentLength != 0 || r.Header.Get("Idempotency-Key") != "" {
			f.t.Errorf("unexpected provider mutation %s", r.URL.Path)
		}
		if f.postStatus == 429 {
			w.Header().Set("Retry-After", "90")
		}
		w.WriteHeader(f.postStatus)
		return
	}
	if f.getStatus != 0 {
		if f.getStatus == 429 {
			w.Header().Set("Retry-After", "90")
		}
		w.WriteHeader(f.getStatus)
		return
	}
	var data any
	switch r.URL.Path {
	case "/pulls/17":
		f.pulls++
		pull := f.pull
		if f.moveHead && f.pulls > 1 {
			pull.Head.SHA = pull.Base.SHA
		}
		data = pull
	case "/actions/runs/41":
		f.reads++
		run := f.run
		if f.raceRetry && f.reads > 1 {
			run.Attempt, run.Status = 2, "in_progress"
		}
		data = run
	case "/contents/.github/workflows/ci.yml":
		data = M{"sha": f.blob, "type": "file"}
	case "/actions/runs/41/attempts/1/jobs", "/actions/runs/41/attempts/2/jobs", "/actions/runs/40/attempts/1/jobs", "/actions/runs/40/attempts/2/jobs":
		if f.jobsStatus != 0 {
			w.WriteHeader(f.jobsStatus)
			return
		}
		attempt, runID := int64(1), f.run.ID
		if strings.Contains(r.URL.Path, "/40/") {
			runID = 40
		}
		conclusion, status := "failure", "completed"
		if strings.Contains(r.URL.Path, "/2/") {
			attempt = 2
			if f.replacementConclusion != "" {
				conclusion = f.replacementConclusion
			}
			if f.run.Status == "in_progress" || f.raceRetry || runID == 40 {
				conclusion, status = "", "in_progress"
			}
		}
		jobs := []ciJob{{ID: 100 + attempt, Name: "go", RunID: runID, RunAttempt: attempt, HeadSHA: f.run.SHA, Conclusion: conclusion, Status: status}}
		if f.originalAppCancelled && attempt == 1 {
			jobs = append(jobs, ciJob{ID: 201, Name: "app", RunID: runID, RunAttempt: attempt, HeadSHA: f.run.SHA, Conclusion: "cancelled", Status: "completed"})
		}
		if f.reportFails && attempt == 2 {
			jobs = append(jobs, ciJob{ID: 300, Name: "reports", RunID: runID, RunAttempt: attempt, HeadSHA: f.run.SHA, Conclusion: "failure", Status: "completed"})
		}
		data = M{"total_count": len(jobs), "jobs": jobs}
	case "/branches/fleet/session":
		data = M{"name": f.pull.Head.Ref, "commit": M{"sha": f.pull.Head.SHA}}
	case "/actions/workflows/3":
		data = M{"id": f.run.WorkflowID, "path": f.workflowPath}
	case "/actions/workflows/3/runs":
		if q := r.URL.Query(); q.Get("head_sha") != f.run.SHA || q.Get("event") != "pull_request" {
			f.t.Error("history read was not scoped to the PR head")
		}
		data = M{"total_count": len(f.history), "workflow_runs": f.history}
	default:
		f.t.Errorf("unexpected provider read %s", r.URL.Path)
		w.WriteHeader(404)
		return
	}
	if err := json.NewEncoder(w).Encode(data); err != nil {
		f.t.Error(err)
	}
}
