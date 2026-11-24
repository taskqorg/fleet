package main

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

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
			ctx := M{"repo": "invented/project", "repositories": "other/project", "branch": "fleet/fixture", "prefix": "fleet", "readOnly": true}
			if _, err := call("ci_logs", M{"jobId": 1, "expectedHeadSha": sha}, ctx); err == nil || err.Error() != "publication authority unavailable" {
				t.Fatalf("ungranted repository: %v", err)
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
	glJob := fmt.Sprintf(`{"id":7,"commit":{"id":%q},"status":"failed"}`, sha)
	ghHead := fmt.Sprintf(`{"name":"fleet/fixture","commit":{"sha":%q}}`, sha)
	glHead := fmt.Sprintf(`{"name":"fleet/fixture","commit":{"id":%q}}`, sha)
	for _, tc := range []struct {
		name            string
		ref             contract.RepoRef
		jobURL, job     string
		branchURL, head string
		wantRequests    int
	}{
		{"GitHub", gh, ghBase + "/actions/jobs/7", ghJob, ghBase + "/branches/fleet%2Ffixture", ghHead, 4},
		{"GitLab", gl, glBase + "/jobs/7", glJob, glBase + "/repository/branches/fleet%2Ffixture", glHead, 3},
		{"foreign GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, "invented/project", "other/project", 1), "", "", 1},
		{"wrong GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, `"id":7`, `"id":8`, 1), "", "", 1},
		{"passing GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, "failure", "success", 1), "", "", 1},
		{"running GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, "completed", "in_progress", 1), "", "", 1},
		{"stale GitHub job", gh, ghBase + "/actions/jobs/7", strings.Replace(ghJob, sha, stale, 1), "", "", 1},
		{"stale GitLab job", gl, glBase + "/jobs/7", strings.Replace(glJob, sha, stale, 1), "", "", 1},
		{"passing GitLab job", gl, glBase + "/jobs/7", strings.Replace(glJob, "failed", "success", 1), "", "", 1},
		{"moved GitHub branch", gh, ghBase + "/actions/jobs/7", ghJob, ghBase + "/branches/fleet%2Ffixture", strings.Replace(ghHead, sha, stale, 1), 2},
		{"moved GitLab branch", gl, glBase + "/jobs/7", glJob, glBase + "/repository/branches/fleet%2Ffixture", strings.Replace(glHead, sha, stale, 1), 2},
		{"oversized metadata", gh, ghBase + "/actions/jobs/7", ghJob + strings.Repeat(" ", ciMetadataLimit), "", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const token = "invented-credential-exact-value"
			const signedDownload = "https://logs.example.test/download?signed=invented"
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
			got, err := readCILogs(tc.ref, "fleet/fixture", ciLogsArgs{ExpectedHeadSha: sha, JobID: 7}, token, do)
			if calls != tc.wantRequests {
				t.Fatalf("made %d requests, want %d", calls, tc.wantRequests)
			}
			if tc.wantRequests < 3 {
				if err == nil || got.Excerpt != "" {
					t.Fatal("unverified job returned an excerpt")
				}
			} else if err != nil || got.Excerpt != "failure: [REDACTED]" || got.Truncated {
				t.Fatalf("unexpected excerpt: %+v, %v", got, err)
			}
		})
	}
}

// requirement: T-FLT-51 caps raw reads and returned excerpts and scrubs exact credential values.
// risk: provider bodies/errors exceed the limit or disclose a full or boundary-split credential.
func TestCILogsBoundsAndRedaction(t *testing.T) {
	const token = "invented-secret-value"
	sha := strings.Repeat("a", 40)
	ref := contract.RepoRef{Provider: "gitlab", Host: "gitlab.example.test", Slug: "invented/project"}
	args := ciLogsArgs{ExpectedHeadSha: sha, JobID: 7}
	raw := "prefix " + token + strings.Repeat("x", ciLogLimit-len("prefix ")-len(token)-5) + token + strings.Repeat("x", ciLogLimit)
	log := strings.NewReader(raw)
	do := func(req *http.Request) (*http.Response, error) {
		var body io.Reader
		switch {
		case strings.HasSuffix(req.URL.Path, "/jobs/7"):
			body = strings.NewReader(fmt.Sprintf(`{"id":7,"commit":{"id":%q},"status":"failed"}`, sha))
		case strings.Contains(req.URL.Path, "/branches/"):
			body = strings.NewReader(fmt.Sprintf(`{"name":"fleet/fixture","commit":{"id":%q}}`, sha))
		case strings.HasSuffix(req.URL.Path, "/jobs/7/trace"):
			body = log
		default:
			t.Fatalf("unexpected request: %s", req.URL)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}, nil
	}
	got, err := readCILogs(ref, "fleet/fixture", args, token, do)
	if err != nil || !got.Truncated || len(got.Excerpt) > ciLogLimit ||
		!strings.HasPrefix(got.Excerpt, "prefix [REDACTED]") || !strings.HasSuffix(got.Excerpt, "[REDACTED]") ||
		strings.Contains(got.Excerpt, token[:5]) || log.Len() != len(raw)-(ciLogLimit+1) {
		t.Fatal("excerpt did not enforce the read, redaction or response bounds", err)
	}

	got, err = readCILogs(ref, "fleet/fixture", args, token, func(*http.Request) (*http.Response, error) {
		return nil, errors.New("provider error echoes " + token)
	})
	if got.Excerpt != "" || err == nil || strings.Contains(err.Error(), token) {
		t.Fatal("provider diagnostic escaped the boundary")
	}
}
