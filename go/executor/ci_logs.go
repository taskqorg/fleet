package main

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"fleet/contract"
)

const (
	ciLogLimit      = 16 * 1024
	ciMetadataLimit = 256 * 1024
)

type ciLogsArgs struct {
	ExpectedHeadSha string `json:"expectedHeadSha"`
	JobID           int64  `json:"jobId"`
}

type ciJob struct {
	ID         int64  `json:"id"`
	RunID      int64  `json:"run_id"`
	RunURL     string `json:"run_url"`
	HeadSHA    string `json:"head_sha"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Commit     struct {
		ID string `json:"id"`
	} `json:"commit"`
}

type ciLogExcerpt struct {
	Excerpt   string `json:"excerpt"`
	Truncated bool   `json:"truncated"`
}

var errCILogsUnavailable = errors.New("CI job log unavailable")

func ciLogsCall(arguments any, ctx forgeContext) (any, error) {
	keys, _ := jClone(arguments).(M)
	args := typed[ciLogsArgs](keys)
	if len(keys) != 2 || args.JobID <= 0 || !validForgeHex40(args.ExpectedHeadSha) {
		return nil, errors.New("ci_logs requires a positive jobId and a full 40-character lowercase expectedHeadSha")
	}
	ref, _, err := validatePublishingContext(ctx)
	if err != nil {
		return nil, err
	}
	if ctx.Branch == "" {
		return nil, errors.New("ci_logs requires a granted execution branch")
	}
	token := forgeCredential(ctx, ref.Provider, ref.Host)
	if token == "" {
		return nil, errors.New("forge-credential-unavailable")
	}
	excerpt, err := readCILogs(ref, ctx.Branch, args, token, httpClient(probeWait).Do)
	if err != nil {
		return nil, err
	}
	return mcpText(excerpt, &no), nil
}

func readCILogs(ref contract.RepoRef, branch string, args ciLogsArgs, token string, do func(*http.Request) (*http.Response, error)) (ciLogExcerpt, error) {
	base := "https://" + ref.Host + "/api/v3/repos/" + ref.Slug
	jobPath, branchPath, suffix := "/actions/jobs/", "/branches/", "/logs"
	if ref.Provider == "gitlab" {
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
			return nil, errCILogsUnavailable
		}
		if authenticated {
			if ref.Provider == "gitlab" {
				req.Header.Set("Private-Token", token)
			} else {
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("Accept", "application/vnd.github+json")
			}
		}
		resp, err := do(req)
		if err != nil {
			return nil, errCILogsUnavailable
		}
		return resp, nil
	}
	metadata := func(target string) ([]byte, error) {
		resp, err := get(target, true)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, errors.New("CI job metadata unavailable")
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, ciMetadataLimit+1))
		if err != nil || len(b) > ciMetadataLimit {
			return nil, errors.New("CI job metadata unavailable")
		}
		return b, nil
	}

	jobURL := base + jobPath + strconv.FormatInt(args.JobID, 10)
	raw, err := metadata(jobURL)
	if err != nil {
		return ciLogExcerpt{}, err
	}
	job := typedRaw[ciJob](raw)
	if job.ID != args.JobID {
		return ciLogExcerpt{}, errors.New("CI job identity could not be verified")
	}
	if ref.Provider == "gitlab" {
		// Both GitLab job endpoints are project-scoped and metadata redirects are refused.
		if job.Commit.ID != args.ExpectedHeadSha || job.Status != "failed" {
			return ciLogExcerpt{}, errors.New("CI job must have failed on expectedHeadSha")
		}
	} else {
		runURL := base + "/actions/runs/" + strconv.FormatInt(job.RunID, 10)
		if job.RunID <= 0 || !strings.EqualFold(job.RunURL, runURL) {
			return ciLogExcerpt{}, errors.New("CI job does not belong to the granted repository")
		}
		if job.HeadSHA != args.ExpectedHeadSha || job.Status != "completed" || job.Conclusion != "failure" {
			return ciLogExcerpt{}, errors.New("CI job must have failed on expectedHeadSha")
		}
	}

	raw, err = metadata(base + branchPath + url.PathEscape(branch))
	if err != nil {
		return ciLogExcerpt{}, err
	}
	head := typedRaw[struct {
		Name   string `json:"name"`
		Commit struct {
			ID  string `json:"id"`
			SHA string `json:"sha"`
		} `json:"commit"`
	}](raw)
	headSHA := head.Commit.SHA
	if ref.Provider == "gitlab" {
		headSHA = head.Commit.ID
	}
	if head.Name != branch || headSHA != args.ExpectedHeadSha {
		return ciLogExcerpt{}, errors.New("execution branch head changed; inspect status before reading CI logs")
	}

	resp, err := get(jobURL+suffix, true)
	if err != nil {
		return ciLogExcerpt{}, err
	}
	if ref.Provider == "github" && resp.StatusCode == http.StatusFound {
		// The authenticated API grants one signed download. The fresh GET carries no API credential.
		location, err := url.Parse(resp.Header.Get("Location"))
		resp.Body.Close()
		if err != nil || location.Scheme != "https" || location.Hostname() == "" || location.User != nil {
			return ciLogExcerpt{}, errCILogsUnavailable
		}
		resp, err = get(location.String(), false)
		if err != nil {
			return ciLogExcerpt{}, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ciLogExcerpt{}, errCILogsUnavailable
	}
	raw, err = io.ReadAll(io.LimitReader(resp.Body, ciLogLimit+1))
	if err != nil {
		return ciLogExcerpt{}, errCILogsUnavailable
	}
	return redactCIExcerpt(raw, token), nil
}

func redactCIExcerpt(raw []byte, token string) ciLogExcerpt {
	truncated := len(raw) > ciLogLimit
	if truncated {
		raw = raw[:ciLogLimit]
	}
	parts := strings.Split(strings.ToValidUTF8(string(raw), ""), token)
	if truncated {
		// Only the text after the last full match can contain a split credential.
		tail := parts[len(parts)-1]
		for n := min(len(tail), len(token)-1); n > 0; n-- {
			if strings.HasSuffix(tail, token[:n]) {
				parts[len(parts)-1] = tail[:len(tail)-n] + "[REDACTED]"
				break
			}
		}
	}
	excerpt := strings.Join(parts, "[REDACTED]")
	if strings.Contains(excerpt, token) {
		// A credential can itself match text created by the replacement.
		return ciLogExcerpt{Truncated: true}
	}
	if len(excerpt) > ciLogLimit {
		end := ciLogLimit
		for !utf8.RuneStart(excerpt[end]) {
			end--
		}
		excerpt, truncated = excerpt[:end], true
	}
	return ciLogExcerpt{Excerpt: excerpt, Truncated: truncated}
}
