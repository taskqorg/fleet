package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"fleet/contract"
)

// requirement: T-FLT-51 exposes the same repository-scoped ci_logs tool on both MCP servers.
// risk: a caller overrides the repository or selects a job without valid identity and head fences.
func TestCILogsMCPBoundary(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for name, call := range map[string]func(string, any, any) (any, error){
		"forge": forgeCall, "publishing": publishingCall,
	} {
		t.Run(name, func(t *testing.T) {
			for _, args := range []M{
				{"jobId": 1},
				{"jobId": 0, "expectedHeadSha": sha},
				{"jobId": 1.5, "expectedHeadSha": sha},
				{"jobId": "1", "expectedHeadSha": sha},
				{"jobId": 1, "expectedHeadSha": strings.ToUpper(sha)},
				{"jobId": 1, "expectedHeadSha": sha, "repo": "other/project"},
			} {
				if _, err := call("ci_logs", args, M{}); err == nil || !strings.HasPrefix(err.Error(), "ci_logs requires") {
					t.Fatalf("invalid arguments %v: %v", args, err)
				}
			}
			ctx := M{"repo": "invented/project", "repositories": "other/project", "branch": "fleet/fixture", "sessionBranch": "fleet/fixture", "prefix": "fleet", "readOnly": true}
			if _, err := call("ci_logs", M{"jobId": 1, "expectedHeadSha": sha}, ctx); err == nil || err.Error() != "publication authority unavailable" {
				t.Fatalf("ungranted repository: %v", err)
			}
			ctx["repositories"] = "invented/project"
			ctx["sessionBranch"] = "fleet/another-session"
			if _, err := call("ci_logs", M{"jobId": 1, "expectedHeadSha": sha}, ctx); err == nil || err.Error() != "ci_logs requires a granted execution branch" {
				t.Fatalf("another session's branch: %v", err)
			}
		})
	}
}

// requirement: T-FLT-51 reads logs only for a failed job in the granted repository at the live head.
// risk: a foreign, stale, successful or unverified job causes a raw log download.
func TestCILogsVerifyJobAndHeadBeforeDownload(t *testing.T) {
	sha := strings.Repeat("a", 40)
	stale := strings.Repeat("b", 40)
	const ghBase = "https://api.github.com/repos/invented/project"
	const glBase = "https://gitlab.example.test/api/v4/projects/invented%2Fgroup%2Fproject"
	gh := contract.RepoRef{Provider: "github", Host: "github.com", Slug: "invented/project"}
	gl := contract.RepoRef{Provider: "gitlab", Host: "gitlab.example.test", Slug: "invented/group/project"}
	ghJob := fmt.Sprintf(`{"id":7,"run_id":9,"run_url":%q,"head_sha":%q,"status":"completed","conclusion":"failure"}`, ghBase+"/actions/runs/9", sha)
	glJob := fmt.Sprintf(`{"id":7,"commit":{"id":%q},"status":"failed","ref":"fleet/fixture"}`, sha)
	ghHead := fmt.Sprintf(`{"name":"fleet/fixture","commit":{"sha":%q}}`, sha)
	glHead := fmt.Sprintf(`{"name":"fleet/fixture","commit":{"id":%q}}`, sha)
	for _, tc := range []struct {
		name            string
		ref             contract.RepoRef
		jobURL, job     string
		branchURL, head string
		wantRequests    int
	}{
		{"GitHub", gh, ghBase + "/actions/jobs/7", ghJob, ghBase + "/branches/fleet%2Ffixture", ghHead, 5},
		{"GitLab", gl, glBase + "/jobs/7", glJob, glBase + "/repository/branches/fleet%2Ffixture", glHead, 3},
		{"foreign GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, "invented/project", "other/project", 1), "", "", 1},
		{"wrong GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, `"id":7`, `"id":8`, 1), "", "", 1},
		{"passing GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, "failure", "success", 1), "", "", 1},
		{"running GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, "completed", "in_progress", 1), "", "", 1},
		{"stale GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, sha, stale, 1), "", "", 1},
		{"stale GitLab job", gl, glBase + "/jobs/7", strings.Replace(glJob, sha, stale, 1), "", "", 1},
		{"same SHA on another GitLab branch", gl, glBase + "/jobs/7", strings.Replace(glJob, "fleet/fixture", "fleet/other", 1), "", "", 1},
		{"same SHA on another GitHub branch", gh, ghBase + "/actions/jobs/7", strings.TrimSuffix(ghJob, "}") + `,"head_branch":"fleet/other"}`, "", "", 1},
		{"GitHub fork run", gh, ghBase + "/actions/jobs/7", ghJob, "", "", 2},
		{"passing GitLab job", gl, glBase + "/jobs/7", strings.Replace(glJob, "failed", "success", 1), "", "", 1},
		{"moved GitHub branch", gh, ghBase + "/actions/jobs/7", ghJob, ghBase + "/branches/fleet%2Ffixture", strings.Replace(ghHead, sha, stale, 1), 3},
		{"moved GitLab branch", gl, glBase + "/jobs/7", glJob, glBase + "/repository/branches/fleet%2Ffixture", strings.Replace(glHead, sha, stale, 1), 2},
		{"oversized metadata", gh, ghBase + "/actions/jobs/7", ghJob + strings.Repeat(" ", ciMetadataLimit), "", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "invented-credential-exact-value"
			const signedDownload = "https://pipelines.actions.githubusercontent.com/download?signed=invented"
			calls := 0
			redirect := false
			do := func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != http.MethodGet {
					t.Fatalf("unexpected method: %s", req.Method)
				}
				reply := func(status int, body string) *http.Response {
					return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}
				}
				if req.URL.String() == signedDownload {
					if !redirect || len(req.Header) != 0 {
						t.Fatalf("signed request carried API headers or bypassed the redirect")
					}
					return reply(http.StatusOK, "failure: "+token), nil
				}
				if tc.ref.Provider == "github" && req.Header.Get("Authorization") != "Bearer "+token ||
					tc.ref.Provider == "gitlab" && req.Header.Get("Private-Token") != token {
					t.Fatal("missing provider authentication")
				}
				switch req.URL.String() {
				case tc.jobURL:
					return reply(http.StatusOK, tc.job), nil
				case ghBase + "/actions/runs/9":
					repo := tc.ref.Slug
					if tc.name == "GitHub fork run" {
						repo = "fork/project"
					}
					return reply(http.StatusOK, fmt.Sprintf(`{"id":9,"head_sha":%q,"head_branch":"fleet/fixture","head_repository":{"full_name":%q}}`, sha, repo)), nil
				case tc.branchURL:
					return reply(http.StatusOK, tc.head), nil
				case tc.jobURL + "/logs":
					redirect = true
					resp := reply(http.StatusFound, "")
					resp.Header = http.Header{"Location": []string{signedDownload}}
					return resp, nil
				case tc.jobURL + "/trace":
					return reply(http.StatusOK, "failure: "+token), nil
				default:
					t.Fatalf("unexpected request: %s", req.URL)
					return nil, nil
				}
			}
			got, err := readCILogs(tc.ref, "fleet/fixture", contract.PublishingCILogsRequest{ExpectedHeadSha: sha, JobId: 7}, token, do)
			if calls != tc.wantRequests {
				t.Fatalf("made %d requests, want %d", calls, tc.wantRequests)
			}
			if tc.branchURL == "" || tc.wantRequests == 2 || tc.ref.Provider == "github" && tc.wantRequests == 3 {
				if err == nil || got.Excerpt != "" {
					t.Fatal("unverified job returned an excerpt")
				}
			} else if err != nil || got.Excerpt != "failure: [REDACTED]" || got.Truncated {
				t.Fatalf("unexpected excerpt: %+v, %v", got, err)
			}
		})
	}
}

// requirement: T-FLT-140 selects actual failures after setup and retains the final cleanup output.
// risk: a noisy log or wrapper errors hide the assertion that explains the failed job.
func TestCILogsLateFailureAndGitLabSection(t *testing.T) {
	// Invented replay of the PR #1310 pattern. Cleanup alone would exceed the entire tool budget.
	trace := strings.Repeat("setup and dependency output\n", 1000) +
		"\x1b[0Ksection_start:1730000000:step_script\r\x1b[0KBuild and test\n" +
		"=== RUN   TestResume\n--- FAIL: TestResume (0.01s)\n    resume_test.go:42: expected retained result\nFAIL\tfleet/example\n" +
		"\x1b[0Ksection_end:1730000002:step_script\r\x1b[0K\n" +
		strings.Repeat("noisy cleanup\n", 1600) + "cleanup finished\n"
	got := readCIFixture(t, "gitlab", http.StatusOK, strings.NewReader(trace), "")
	if got.ScanStatus != "complete" || !got.Truncated || got.ScannedBytes != int64(len(trace)) ||
		got.PipelineId == nil || *got.PipelineId != 24 || str0(got.Stage) != "test" ||
		str0(got.FailureReason) != "script_failure" || got.JobId != 7 {
		t.Fatalf("incorrect trace metadata: %s", enc(got))
	}
	if len(got.Windows) != 1 || str0(got.Windows[0].Section) != "step_script" ||
		!strings.Contains(ciWindowText(got, &got.Windows[0]), "expected retained result") ||
		!strings.HasSuffix(ciWindowText(got, got.Tail), "cleanup finished\n") {
		t.Fatal("late error, failed trace section or tail was lost")
	}
}

// requirement: T-FLT-140 keeps failed steps and same-job Checks annotations when job logs are missing or inconclusive.
// risk: an expired trace yields no diagnosis, a rerun's metadata is attributed to the wrong job, or an unrelated check is read.
func TestCILogsGitHubAnnotationFallback(t *testing.T) {
	const annotation = `[{"path":"go/resume_test.go","start_line":42,"end_line":43,"annotation_level":"failure","message":"Expected resume after restart. Authorization: Bearer invented-annotation-token"},{"annotation_level":"warning","message":"do not show this warning"}]`
	for _, tc := range []struct {
		name, trace, scanStatus string
		status                  int
	}{
		{"expired", "provider error echoes " + ciFixtureToken, "missing", http.StatusGone},
		{"wrapper only", "##[error]Process completed with exit code 1.\n", "complete", http.StatusOK},
		{"denied", "unreadable", "denied", http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readCIFixture(t, "github", tc.status, strings.NewReader(tc.trace), annotation)
			if string(got.ScanStatus) != tc.scanStatus || got.Attempt == nil || *got.Attempt != 2 ||
				got.RunId == nil || *got.RunId != 9 || str0(got.JobName) != "Go" ||
				len(got.FailedSteps) != 1 || got.FailedSteps[0].Number != 6 || got.FailedSteps[0].Name != "Run tests" ||
				got.AnnotationStatus == nil || *got.AnnotationStatus != "complete" {
				t.Fatalf("missing GitHub job, attempt or fallback metadata: %s", enc(got))
			}
			window := got.Windows[len(got.Windows)-1]
			if window.Source != "annotation" || str0(window.Path) != "go/resume_test.go" || window.StartLine == nil ||
				*window.StartLine != 42 || ciWindowText(got, &window) != "Expected resume after restart. [REDACTED]" ||
				strings.Contains(got.Excerpt, "do not show") || (tc.status != http.StatusOK && got.Tail != nil) {
				t.Fatal("incorrect annotation fallback")
			}
		})
	}
	var requested bool
	base := "https://api.github.com/repos/invented/project"
	windows, status := readCIAnnotations(func(_ string, _ bool) (*http.Response, error) {
		requested = true
		return nil, errors.New("unexpected annotation request")
	}, base, "https://api.github.com/repos/other/project/check-runs/16", ciFixtureToken)
	if requested || len(windows) != 0 || status != "unavailable" {
		t.Fatal("requested annotations from a different repository")
	}
}

// requirement: T-FLT-140 reports missing traces and scan cuts explicitly, with bounded sanitized evidence.
// risk: workers mistake a transport failure for a complete tail or a cut leaks a credential.
func TestCILogsPartialTraceAndRedaction(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		got := readCIFixture(t, "gitlab", http.StatusNotFound, strings.NewReader(ciFixtureToken), "")
		if got.ScanStatus != "missing" || got.Tail != nil || got.Excerpt != "" || !got.Truncated || str0(got.Stage) != "test" {
			t.Fatal("missing trace lost the stage or claimed log coverage")
		}
	})
	t.Run("small reads and window cuts", func(t *testing.T) {
		const tokenCut = 11
		secret := ciFixtureToken[:tokenCut] + "\x1b[31m\n\t" + ciFixtureToken[tokenCut:]
		trace := "panic: boundary failure\n" + strings.Repeat("x", 760) + secret + "\n" +
			strings.Repeat("x", 12000) + " error[E0425]: marker inside oversized line\n" +
			strings.Repeat("é", 2039) + ciFixtureToken + " final\n"
		got := readCIFixture(t, "gitlab", http.StatusOK, ciShortReader{strings.NewReader(trace)}, "")
		if !got.Truncated || got.ScanStatus != "complete" || len(got.Windows) == 0 || !utf8.ValidString(got.Excerpt) ||
			!strings.Contains(got.Excerpt, "marker inside oversized line") || !strings.HasSuffix(ciWindowText(got, got.Tail), "[REDACTED] final\n") ||
			strings.Contains(got.Excerpt, ciFixtureToken[:tokenCut]) || strings.Contains(got.Excerpt, ciFixtureToken[tokenCut:]) {
			t.Fatal("oversized line, chunk redaction, UTF-8 or window cut failed")
		}
	})
	t.Run("interrupted after credential prefix and partial control", func(t *testing.T) {
		got := readCIFixture(t, "gitlab", http.StatusOK, ciInterruptedReader{"last observed line\npanic: " + ciFixtureToken[:12] + "\x1b[3"}, "")
		if got.ScanStatus != "interrupted" || !got.Truncated || got.Tail == nil ||
			strings.Contains(got.Excerpt, ciFixtureToken[:12]) || !strings.Contains(got.Excerpt, "panic: [REDACTED]") {
			t.Fatalf("interrupted scan retained unsafe text or claimed EOF: %s", enc(got))
		}
	})
	t.Run("scan cap and cut credential", func(t *testing.T) {
		prefix := " final observed part: " + ciFixtureToken[:8]
		raw := strings.Repeat("x", ciLogScanLimit-len(prefix)) + prefix + ciFixtureToken[8:] + " unread bytes"
		log := strings.NewReader(raw)
		got := readCIFixture(t, "gitlab", http.StatusOK, log, "")
		if got.ScanStatus != "scan_limit" || !got.Truncated || got.ScannedBytes != ciLogScanLimit ||
			log.Len() != len(raw)-(ciLogScanLimit+1) || strings.Contains(got.Excerpt, ciFixtureToken[:8]) ||
			!strings.HasSuffix(ciWindowText(got, got.Tail), "[REDACTED]") {
			t.Fatal("read cap, cut redaction or available tail was incorrect")
		}
	})
	t.Run("encoded payload", func(t *testing.T) {
		rows := strings.Repeat("panic: "+strings.Repeat("\\\"", 300)+"\nline\nline\nline\n", 40)
		got := readCIFixture(t, "gitlab", http.StatusOK, strings.NewReader(rows+"tail end\n"), "")
		if len(enc(got)) > ciLogLimit || !got.Truncated || got.OmittedWindows == 0 ||
			!strings.HasSuffix(ciWindowText(got, got.Tail), "tail end\n") || len(got.Windows) < 2 {
			t.Fatalf("encoded reply budget lost tail or windows: bytes=%d windows=%d", len(enc(got)), len(got.Windows))
		}
	})
}

// requirement: T-FLT-140 never follows a cross-origin or second redirect and suppresses provider errors.
// risk: a trace redirect reaches an unauthorized origin or leaks provider credentials through an error.
func TestCILogsUnsafeDownloads(t *testing.T) {
	for _, location := range []string{
		"https://127.0.0.1/log", "https://productionresultssa0.blob.core.windows.net.evil.test/log",
		"https://invented:pass@pipelines.actions.githubusercontent.com/download",
		"http://pipelines.actions.githubusercontent.com/download", "https://pipelines.actions.githubusercontent.com:8080/download",
		"https://arbitrary.blob.core.windows.net/log",
	} {
		t.Run(location, func(t *testing.T) {
			ref := contract.RepoRef{Provider: "github", Host: "github.com"}
			calls := 0
			raw, status := downloadCITrace(func(_ string, auth bool) (*http.Response, error) {
				calls++
				if !auth {
					t.Fatal("followed an unsafe signed download")
				}
				resp := ciTestResponse(http.StatusFound, strings.NewReader(ciFixtureToken))
				resp.Header = http.Header{"Location": {location}}
				return resp, nil
			}, "bound job", ref)
			if calls != 1 || len(raw) != 0 || status != "unsupported_redirect" {
				t.Fatal("unsafe redirect did not fail closed")
			}
		})
	}
	ref := contract.RepoRef{Provider: "github", Host: "github.com", Slug: "invented/project"}
	calls := 0
	raw, status := downloadCITrace(func(_ string, auth bool) (*http.Response, error) {
		calls++
		if auth != (calls == 1) || calls > 2 {
			t.Fatal("followed a second redirect or passed authentication to storage")
		}
		resp := ciTestResponse(http.StatusFound, strings.NewReader(ciFixtureToken))
		resp.Header = http.Header{"Location": {"https://productionresultssa0.blob.core.windows.net/log?signed=invented"}}
		return resp, nil
	}, "bound job", ref)
	if calls != 2 || len(raw) != 0 || status != "unsupported_redirect" {
		t.Fatal("second redirect was not rejected")
	}
	got, err := readCILogs(ref, "fleet/fixture", contract.PublishingCILogsRequest{
		ExpectedHeadSha: strings.Repeat("a", 40), JobId: 7,
	}, ciFixtureToken, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider error echoes " + ciFixtureToken)
	})
	if got.Excerpt != "" || err == nil || strings.Contains(err.Error(), ciFixtureToken) {
		t.Fatal("provider diagnostic escaped the boundary")
	}
}

const ciFixtureToken = "invented-mediator-credential-value"

func readCIFixture(t *testing.T, provider string, traceStatus int, trace io.Reader, annotations string) contract.PublishingCILogsReply {
	t.Helper()
	sha := strings.Repeat("a", 40)
	ref := contract.RepoRef{Provider: provider, Host: "gitlab.example.test", Slug: "invented/project"}
	if provider == "github" {
		ref.Host = "github.com"
	}
	const ghBase = "https://api.github.com/repos/invented/project"
	do := func(req *http.Request) (*http.Response, error) {
		path := req.URL.Path
		var body string
		switch {
		case strings.HasSuffix(path, "/jobs/7") && provider == "gitlab":
			body = fmt.Sprintf(`{"id":7,"name":"Tests","ref":"fleet/fixture","stage":"test","failure_reason":"script_failure","pipeline":{"id":24,"sha":%q},"commit":{"id":%q},"status":"failed"}`, sha, sha)
		case strings.HasSuffix(path, "/actions/jobs/7"):
			body = fmt.Sprintf(`{"id":7,"name":"Go","run_id":9,"run_attempt":2,"run_url":%q,"check_run_url":%q,"head_sha":%q,
				"status":"completed","conclusion":"failure","steps":[{"number":1,"name":"Checkout","conclusion":"success"},
				{"number":6,"name":"Run tests","conclusion":"failure"}]}`, ghBase+"/actions/runs/9", ghBase+"/check-runs/16", sha)
		case strings.HasSuffix(path, "/actions/runs/9"):
			body = fmt.Sprintf(`{"id":9,"head_sha":%q,"head_branch":"fleet/fixture","head_repository":{"full_name":"invented/project"}}`, sha)
		case strings.HasSuffix(path, "/branches/fleet/fixture"):
			body = fmt.Sprintf(`{"name":"fleet/fixture","commit":{"id":%q,"sha":%q}}`, sha, sha)
		case strings.HasSuffix(path, "/trace"), strings.HasSuffix(path, "/logs"):
			return ciTestResponse(traceStatus, trace), nil
		case req.URL.String() == ghBase+"/check-runs/16/annotations?per_page=30" && annotations != "":
			body = annotations
		default:
			t.Fatal("unexpected CI request")
		}
		return ciTestResponse(http.StatusOK, strings.NewReader(body)), nil
	}
	got, err := readCILogs(ref, "fleet/fixture", contract.PublishingCILogsRequest{ExpectedHeadSha: sha, JobId: 7}, ciFixtureToken, do)
	if err != nil {
		t.Fatal(err)
	}
	if err := got.Validate(); err != nil {
		t.Fatal(err)
	}
	return got
}

func ciTestResponse(status int, body io.Reader) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(body)}
}

func ciWindowText(reply contract.PublishingCILogsReply, window *contract.PublishingCIWindow) string {
	if window == nil {
		return ""
	}
	return reply.Excerpt[window.StartByte:window.EndByte]
}

type ciShortReader struct{ io.Reader }

func (r ciShortReader) Read(p []byte) (int, error) {
	return r.Reader.Read(p[:min(3, len(p))])
}

type ciInterruptedReader struct{ text string }

func (r ciInterruptedReader) Read(p []byte) (int, error) {
	return copy(p, r.text), io.ErrUnexpectedEOF
}
