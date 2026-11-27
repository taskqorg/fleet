package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
)

const (
	ciLogLimit      = 16 * 1024
	ciLogScanLimit  = 8 * 1024 * 1024
	ciMetadataLimit = 256 * 1024
)

type ciGet func(target string, authenticated bool) (*http.Response, error)

type ciJob struct {
	ID            int64                             `json:"id"`
	Name          string                            `json:"name"`
	RunID         int64                             `json:"run_id"`
	RunURL        string                            `json:"run_url"`
	RunAttempt    int64                             `json:"run_attempt"`
	WorkflowName  string                            `json:"workflow_name"`
	CheckRunURL   string                            `json:"check_run_url"`
	HeadSHA       string                            `json:"head_sha"`
	HeadBranch    string                            `json:"head_branch"`
	Status        string                            `json:"status"`
	Conclusion    string                            `json:"conclusion"`
	Steps         []contract.PublishingCIFailedStep `json:"steps"`
	Ref           string                            `json:"ref"`
	Tag           bool                              `json:"tag"`
	Stage         string                            `json:"stage"`
	FailureReason string                            `json:"failure_reason"`
	Commit        struct {
		ID string `json:"id"`
	} `json:"commit"`
	Pipeline struct {
		ID  int64  `json:"id"`
		SHA string `json:"sha"`
	} `json:"pipeline"`
}

func ciLogsCall(arguments any, ctx forgeContext) (any, error) {
	keys, _ := jClone(arguments).(M)
	args := typed[contract.PublishingCILogsRequest](keys)
	if len(keys) != 2 || args.JobId <= 0 || !validForgeHex40(args.ExpectedHeadSha) {
		return nil, errors.New("ci_logs requires a positive jobId and a full 40-character lowercase expectedHeadSha")
	}
	if ctx.SkipWorktreeHead || ctx.Branch == "" || ctx.Branch != ctx.SessionBranch || ctx.Branch == "main" || ctx.Branch == strings.TrimPrefix(ctx.Base, "refs/heads/") {
		return nil, errors.New("ci_logs requires a granted execution branch")
	}
	ref, _, err := validatePublishingContext(ctx)
	if err != nil {
		return nil, err
	}
	token := forgeCredential(ctx, ref.Provider, ref.Host)
	if token == "" {
		return nil, errors.New("forge-credential-unavailable")
	}
	reply, err := readCILogs(ref, ctx.Branch, args, token, httpClient(probeWait).Do)
	if err != nil {
		return nil, err
	}
	return mcpText(reply, &no), nil
}

func readCILogs(ref contract.RepoRef, branch string, args contract.PublishingCILogsRequest, token string, do func(*http.Request) (*http.Response, error)) (contract.PublishingCILogsReply, error) {
	var reply contract.PublishingCILogsReply
	base := "https://" + ref.Host + "/api/v3/repos/" + ref.Slug
	jobPath, branchPath, suffix := "/actions/jobs/", "/branches/", "/logs"
	gitlab := ref.Provider == "gitlab"
	if gitlab {
		base = "https://" + ref.Host + "/api/v4/projects/" + url.PathEscape(ref.Slug)
		jobPath, branchPath, suffix = "/jobs/", "/repository/branches/", "/trace"
	} else if ref.Host == "github.com" {
		base = "https://api.github.com/repos/" + ref.Slug
	} else if strings.HasSuffix(ref.Host, ".ghe.com") {
		base = "https://api." + ref.Host + "/repos/" + ref.Slug
	}
	get := func(target string, authenticated bool) (*http.Response, error) {
		req, err := http.NewRequest(http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if authenticated {
			if gitlab {
				req.Header.Set("Private-Token", token)
			} else {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Accept", "application/vnd.github+json")
			}
		}
		return do(req)
	}

	jobID := strconv.FormatInt(args.JobId, 10)
	jobURL := base + jobPath + jobID
	var job ciJob
	if _, ok := ciMetadata(get, jobURL, &job); !ok || job.ID != args.JobId {
		return reply, errors.New("CI job identity could not be verified")
	}
	runID := strconv.FormatInt(job.RunID, 10)
	if gitlab {
		// GitLab scopes the job and trace endpoints to the project.
		if job.Commit.ID != args.ExpectedHeadSha || (job.Pipeline.SHA != "" && job.Pipeline.SHA != args.ExpectedHeadSha) ||
			job.Ref != branch || job.Tag || job.Status != "failed" {
			return reply, errors.New("CI job must have failed on the granted branch and expectedHeadSha")
		}
	} else {
		runURL := base + "/actions/runs/" + runID
		if job.RunID <= 0 || !strings.EqualFold(job.RunURL, runURL) {
			return reply, errors.New("CI job does not belong to the granted repository")
		}
		if job.HeadSHA != args.ExpectedHeadSha || (job.HeadBranch != "" && job.HeadBranch != branch) ||
			job.Status != "completed" || job.Conclusion != "failure" {
			return reply, errors.New("CI job must have failed on the granted branch and expectedHeadSha")
		}
		var run struct {
			ID             int64  `json:"id"`
			HeadSHA        string `json:"head_sha"`
			HeadBranch     string `json:"head_branch"`
			HeadRepository *struct {
				FullName string `json:"full_name"`
			} `json:"head_repository"`
		}
		if _, ok := ciMetadata(get, runURL, &run); !ok || run.ID != job.RunID ||
			run.HeadSHA != args.ExpectedHeadSha || run.HeadBranch != branch || run.HeadRepository == nil ||
			!strings.EqualFold(run.HeadRepository.FullName, ref.Slug) {
			return reply, errors.New("CI workflow source could not be verified for the granted repository, branch and head")
		}
	}

	var head struct {
		Name   string `json:"name"`
		Commit struct {
			ID  string `json:"id"`
			SHA string `json:"sha"`
		} `json:"commit"`
	}
	_, headOK := ciMetadata(get, base+branchPath+url.PathEscape(branch), &head)
	headSHA := head.Commit.SHA
	if gitlab {
		headSHA = head.Commit.ID
	}
	if !headOK || head.Name != branch || headSHA != args.ExpectedHeadSha {
		return reply, errors.New("execution branch head could not be verified; inspect status before reading CI logs")
	}

	repoURL := "https://" + ref.Host + "/" + ref.Slug
	reply = contract.PublishingCILogsReply{
		Provider: contract.PublishingProvider(ref.Provider), Host: ref.Host, Repo: ref.Slug, Branch: branch,
		HeadSha: headSHA, JobId: job.ID, ObservedAt: time.Now().UTC().Format(time.RFC3339),
		JobName: ciMetadataText(job.Name, token),
		Windows: []contract.PublishingCIWindow{},
	}
	if gitlab {
		reply.JobUrl = repoURL + "/-/jobs/" + jobID
		reply.PipelineId = ciPositive(job.Pipeline.ID)
		reply.Stage = ciMetadataText(job.Stage, token)
		reply.FailureReason = ciMetadataText(job.FailureReason, token)
	} else {
		reply.JobUrl = repoURL + "/actions/runs/" + runID + "/job/" + jobID
		reply.RunId = &job.RunID
		reply.Attempt = ciPositive(job.RunAttempt)
		reply.WorkflowName = ciMetadataText(job.WorkflowName, token)
		var omitted int64
		for _, step := range job.Steps {
			if step.Conclusion != "failure" {
				continue
			}
			if len(reply.FailedSteps) == 12 {
				omitted++
				continue
			}
			step.Name = ciClipPrefix(sanitizeCIText(step.Name, token, false), 160)
			reply.FailedSteps = append(reply.FailedSteps, step)
		}
		reply.OmittedFailedSteps = ciPositive(omitted)
	}

	raw, status := downloadCITrace(get, jobURL+suffix, ref)
	reply.ScanStatus, reply.ScannedBytes = status, int64(len(raw))
	complete := status == contract.PublishingCIScanStatusComplete
	text := sanitizeCIText(string(raw), token, !complete)
	windows, omitted, tail := selectCITrace(text, gitlab)
	if !gitlab && (!complete || !hasCIPrimaryWindow(windows)) {
		annotations, status := readCIAnnotations(get, base, job.CheckRunURL, token)
		reply.AnnotationStatus = &status
		windows = append(windows, annotations...)
	}
	if !complete {
		tail.Partial = &yes
	}
	fillCIExcerpt(&reply, windows, omitted, tail, len(text) <= ciTailLimit)
	// Never return a credential even if a pathological value matches a fixed field or sanitized output.
	if encoded := enc(reply); len(encoded) > ciLogLimit || (token != "" && strings.Contains(encoded, token)) {
		return contract.PublishingCILogsReply{}, errors.New("CI diagnostics could not be returned safely")
	}
	return reply, nil
}

// ciMetadata decodes exactly one bounded response from a caller-constructed URL.
func ciMetadata(get ciGet, target string, dest any) (more bool, ok bool) {
	resp, err := get(target, true)
	if err != nil {
		return false, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ciMetadataLimit+1))
	return strings.Contains(resp.Header.Get("Link"), `rel="next"`),
		err == nil && len(body) <= ciMetadataLimit && json.Unmarshal(body, dest) == nil
}

func ciPositive(n int64) *int64 {
	if n <= 0 {
		return nil
	}
	return &n
}

func readCIAnnotations(get ciGet, base, checkURL, token string) ([]ciWindow, contract.PublishingCIAnnotationStatus) {
	prefix := base + "/check-runs/"
	if len(checkURL) <= len(prefix) || !strings.EqualFold(checkURL[:len(prefix)], prefix) {
		return nil, contract.PublishingCIAnnotationStatusUnavailable
	}
	id, err := strconv.ParseInt(checkURL[len(prefix):], 10, 64)
	if err != nil || id <= 0 {
		return nil, contract.PublishingCIAnnotationStatusUnavailable
	}
	var entries []struct {
		Path  string `json:"path"`
		Start int64  `json:"start_line"`
		End   int64  `json:"end_line"`
		Level string `json:"annotation_level"`
		Text  string `json:"message"`
	}
	more, ok := ciMetadata(get, prefix+strconv.FormatInt(id, 10)+"/annotations?per_page=30", &entries)
	if !ok {
		return nil, contract.PublishingCIAnnotationStatusUnavailable
	}
	status := contract.PublishingCIAnnotationStatusComplete
	if more {
		status = contract.PublishingCIAnnotationStatusPartial
	}
	var windows []ciWindow
	for _, entry := range entries {
		if entry.Level != "failure" {
			continue
		}
		text := sanitizeCIText(entry.Text, token, false)
		if strings.TrimSpace(text) == "" {
			continue
		}
		window := ciWindow{
			PublishingCIWindow: contract.PublishingCIWindow{
				Source: contract.PublishingCIWindowSourceAnnotation, StartLine: ciPositive(entry.Start),
				EndLine: ciPositive(entry.End), Path: ciMetadataText(entry.Path, token),
			},
			text: ciClipPrefix(text, ciWindowLimit), priority: 2,
		}
		if len(window.text) != len(text) {
			window.Partial = &yes
		}
		windows = append(windows, window)
	}
	return windows, status
}

func downloadCITrace(get ciGet, target string, ref contract.RepoRef) ([]byte, contract.PublishingCIScanStatus) {
	resp, err := get(target, true)
	if err != nil {
		return nil, contract.PublishingCIScanStatusUnavailable
	}
	if ref.Provider == "github" && resp.StatusCode == http.StatusFound {
		// One signed storage hop, with no API headers. The shared HTTP client refuses subsequent redirects.
		location, err := url.Parse(resp.Header.Get("Location"))
		resp.Body.Close()
		if err != nil || !ciSignedDownloadAllowed(location, ref.Host) {
			return nil, contract.PublishingCIScanStatusUnsupportedRedirect
		}
		resp, err = get(location.String(), false)
		if err != nil {
			return nil, contract.PublishingCIScanStatusUnavailable
		}
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		raw, err := io.ReadAll(io.LimitReader(resp.Body, ciLogScanLimit+1))
		switch {
		case len(raw) > ciLogScanLimit:
			return raw[:ciLogScanLimit], contract.PublishingCIScanStatusScanLimit
		case err != nil:
			return raw, contract.PublishingCIScanStatusInterrupted
		case len(raw) == 0:
			return nil, contract.PublishingCIScanStatusEmpty
		default:
			return raw, contract.PublishingCIScanStatusComplete
		}
	case http.StatusNotFound, http.StatusGone:
		return nil, contract.PublishingCIScanStatusMissing
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, contract.PublishingCIScanStatusDenied
	case http.StatusTooManyRequests:
		return nil, contract.PublishingCIScanStatusRateLimited
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return nil, contract.PublishingCIScanStatusUnsupportedRedirect
	}
	return nil, contract.PublishingCIScanStatusUnavailable
}

func ciSignedDownloadAllowed(location *url.URL, forgeHost string) bool {
	if location.Scheme != "https" || location.User != nil || location.Fragment != "" ||
		(location.Port() != "" && location.Port() != "443") {
		return false
	}
	host := strings.ToLower(location.Hostname())
	if host == forgeHost {
		return true // Enterprise Server may deliver the download from its own origin.
	}
	return (forgeHost == "github.com" || strings.HasSuffix(forgeHost, ".ghe.com")) &&
		(strings.HasSuffix(host, ".actions.githubusercontent.com") ||
			(strings.HasPrefix(host, "productionresultssa") && strings.Count(host, ".") == 4 &&
				strings.HasSuffix(host, ".blob.core.windows.net")))
}
