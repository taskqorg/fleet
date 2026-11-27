package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"fleet/contract"
)

// forgeRepo preserves the strict GitHub HTTPS remote parser used by legacy callers and boundary tests.
func forgeRepo(remote string) string {
	ref, ok := contract.ParseRepoRemote(remote, "github", "github.com")
	if !ok {
		return ""
	}
	return ref.Slug
}

// forgeContext is the project-publishing / project-forge server context built by the executor at launch.
type forgeContext struct {
	Repositories     string `json:"repositories"`
	CredentialHome   string `json:"credentialHome"`
	Provider         string `json:"provider,omitempty"`
	Host             string `json:"host,omitempty"`
	Repo             string `json:"repo"`
	Remote           string `json:"remote,omitempty"`
	Connection       string `json:"connection,omitempty"`
	ConnectionsDir   string `json:"connectionsDir,omitempty"`
	Base             string `json:"base"`
	Branch           string `json:"branch"`
	SessionBranch    string `json:"sessionBranch,omitempty"`
	Prefix           string `json:"prefix"`
	Number           int64  `json:"number,omitempty"`
	AutoMerge        bool   `json:"autoMerge"`
	MergeMethod      string `json:"mergeMethod"`
	Workdir          string `json:"workdir"`
	Home             string `json:"home"`
	Store            string `json:"store"`
	GlabStore        string `json:"glabStore,omitempty"`
	HostId           string `json:"hostId,omitempty"`
	CommandId        string `json:"commandId,omitempty"`
	ExecutionId      string `json:"executionId,omitempty"`
	ProjectId        string `json:"projectId,omitempty"`
	RepoId           string `json:"repoId,omitempty"`
	Journal          string `json:"journal,omitempty"`
	ReadOnly         bool   `json:"readOnly,omitempty"`
	SkipWorktreeHead bool   `json:"skipWorktreeHead,omitempty"`
	ReportPublishing bool   `json:"reportPublishing,omitempty"`
}

func resolveGlabStore(store string) string {
	if store == "" || !filepath.IsAbs(store) {
		return ""
	}
	if isFile(filepath.Join(store, "glab", "config.yml")) {
		return filepath.Join(store, "glab")
	}
	if isFile(filepath.Join(store, "config.yml")) {
		return store
	}
	return ""
}

func glabConfiguredHosts(store string) []string {
	dir := resolveGlabStore(store)
	if dir == "" {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(dir, "config.yml"))
	if err != nil {
		return nil
	}
	var hosts []string
	inHosts := false
	hostsIndent := -1
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if !inHosts {
			if trimmed == "hosts:" {
				inHosts = true
				hostsIndent = indent
			}
			continue
		}
		if indent <= hostsIndent {
			break
		}
		if strings.HasSuffix(trimmed, ":") && !strings.Contains(trimmed, " ") {
			h := strings.ToLower(strings.TrimSuffix(trimmed, ":"))
			if h != "" && !strings.ContainsAny(h, "/@\\") {
				hosts = append(hosts, h)
			}
		}
	}
	return hosts
}

func (ctx forgeContext) resolvedRef() (contract.RepoRef, bool) {
	provider := strings.ToLower(strings.TrimSpace(ctx.Provider))
	host := strings.ToLower(strings.TrimSpace(ctx.Host))
	if provider == "" {
		provider = "github"
	}
	if host == "" {
		if provider == "github" {
			host = "github.com"
		} else if provider == "gitlab" {
			host = "gitlab.com"
		}
	}
	if ctx.Remote != "" {
		return contract.ParseRepoRemote(ctx.Remote, provider, host)
	}
	if provider == "github" {
		if host == "github.com" {
			if ctx.Repo == "" || forgeRepo("https://github.com/"+ctx.Repo) != ctx.Repo {
				return contract.RepoRef{}, false
			}
			return contract.RepoRef{Provider: "github", Host: "github.com", Slug: ctx.Repo}, true
		}
		return contract.ParseRepoRemote("https://"+host+"/"+ctx.Repo+".git", "github", host)
	}
	return contract.ParseRepoRemote("https://"+host+"/"+ctx.Repo+".git", "gitlab", host)
}

func (ctx forgeContext) hasCredentialStore(provider string) bool {
	if !filepath.IsAbs(ctx.Store) {
		return false
	}
	if provider == "gitlab" {
		return ctx.GlabStore != "" && isFile(filepath.Join(ctx.GlabStore, "config.yml")) || resolveGlabStore(ctx.Store) != ""
	}
	return isFile(filepath.Join(ctx.Store, "hosts.yml"))
}

// forgeCommand reads only the explicitly connected host credential store; native HOME stays isolated.
func forgeCommand(ctx forgeContext, name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Dir = ctx.Workdir
	if c.Dir == "" {
		c.Dir = os.TempDir()
	}
	provider := strings.ToLower(strings.TrimSpace(ctx.Provider))
	if provider == "" {
		provider = "github"
	}
	host := strings.ToLower(strings.TrimSpace(ctx.Host))
	if provider == "gitlab" {
		if host == "" {
			host = "gitlab.com"
		}
		glabDir := ctx.GlabStore
		if glabDir == "" {
			glabDir = resolveGlabStore(ctx.Store)
		}
		c.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + ctx.Home,
			"GLAB_CONFIG_DIR=" + glabDir,
			"GITLAB_HOST=https://" + host,
			"GLAB_NO_PROMPT=1",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
		}
		if token := forgeCredential(ctx, provider, host); token != "" {
			c.Env = append(c.Env, "GITLAB_TOKEN="+token)
		}
		return c
	}

	if host == "" {
		host = "github.com"
	}
	c.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + ctx.Home,
		"GH_CONFIG_DIR=" + ctx.Store,
		"GH_PROMPT_DISABLED=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	if host != "github.com" {
		c.Env = append(c.Env, "GH_HOST="+host)
	}
	if token := forgeCredential(ctx, provider, host); token != "" {
		c.Env = append(c.Env, "GH_TOKEN="+token)
	}
	return c
}

func forgeCredential(ctx forgeContext, provider, host string) string {
	var credential *exec.Cmd
	if provider == "gitlab" {
		glabDir := ctx.GlabStore
		if glabDir == "" {
			glabDir = resolveGlabStore(ctx.Store)
		}
		// --host, not -h: glab 1.112 reads -h as --help and prints the usage on stdout with exit 0, which became a token
		// that the GitLab API refused (2026-11-26: no host reported GitLab publishing, a DAM turn waited unplaced).
		credential = exec.Command("glab", "config", "get", "token", "--host", host)
		credential.Env = []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + ctx.CredentialHome,
			"GLAB_CONFIG_DIR=" + glabDir,
			"GITLAB_HOST=https://" + host,
			"GLAB_NO_PROMPT=1",
		}
	} else {
		credential = exec.Command("gh", "auth", "token", "--hostname", host)
		credential.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + ctx.CredentialHome, "GH_CONFIG_DIR=" + ctx.Store, "GH_PROMPT_DISABLED=1"}
	}
	token, err := boundedOutput(credential, probeWait)
	if err != nil {
		return ""
	}
	t := strings.TrimSpace(string(token))
	if strings.ContainsAny(t, " \t\r\n") {
		return "" // not a token (a usage text or a warning): the CLI falls back to its own config
	}
	return t
}

var ciLogsTool = M{
	"name":        "ci_logs",
	"description": "Read failed-job metadata, credential-redacted error windows, and the tail for a CI job on the granted execution branch's live head. The reply reports scan completeness and omitted evidence.",
	"inputSchema": M{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"expectedHeadSha", "jobId"},
		"properties": M{
			"expectedHeadSha": M{"type": "string", "pattern": "^[0-9a-f]{40}$"},
			"jobId":           M{"type": "integer", "minimum": 1},
		},
	},
	"annotations": M{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true},
}

var forgeTools = []any{
	ciLogsTool,
	M{
		"name":        "publish_draft",
		"description": "Push this execution branch to its granted project repository and open or reuse a draft pull request or merge request. Accepts an explicit remote-head lease for rebased fleet/* branches. Never merges.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"title", "body"},
			"properties": M{
				"title":            M{"type": "string"},
				"body":             M{"type": "string"},
				"force_with_lease": M{"type": "string", "pattern": "^[0-9a-f]{40}$", "description": "Expected current remote branch commit (40 lowercase hex characters). Omit for a regular push."},
			},
		},
	},
	M{
		"name":        "delivery",
		"description": "Read this branch PR/MR checks/head/status, post exact-SHA code-review status with verified Fleet review evidence (head_sha, artifact_id, description), mark it ready, or enable project-authorized auto-merge through required checks. Independent review remains required.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"action"},
			"properties": M{
				"action":      M{"type": "string", "enum": []any{"status", "code_review", "ready", "auto_merge"}},
				"head_sha":    M{"type": "string", "description": "Full 40-character lowercase commit SHA for action=code_review."},
				"artifact_id": M{"type": "string", "description": "Stored Fleet review artifact ID referencing head_sha for action=code_review."},
				"description": M{"type": "string", "description": "Single-line review summary (<=140 chars) for action=code_review."},
			},
		},
	},
}

var ciRerunTool = M{
	"name":        "rerun_failed",
	"description": "Request one policy-granted retry for a verified transient PR-head CI failure. Keep the source execution active through dispatch and use ci_recovery to follow the durable record.",
	"inputSchema": M{"type": "object", "additionalProperties": false,
		"required": []string{"requestId", "number", "expectedHeadSha", "runId", "attempt", "reason"},
		"properties": M{"requestId": M{"type": "string"}, "number": M{"type": "integer"}, "expectedHeadSha": M{"type": "string"},
			"runId": M{"type": "integer"}, "attempt": M{"type": "integer"}, "reason": M{"type": "string"}}},
	"annotations": M{"readOnlyHint": false, "destructiveHint": false, "idempotentHint": true},
}

var ciRecoveryTool = M{
	"name":        "ci_recovery",
	"description": "Read this project's durable PR-head rerun ledger and worker handoff evidence. Does not dispatch CI.",
	"inputSchema": M{"type": "object", "additionalProperties": false, "properties": M{"expectedHeadSha": M{"type": "string"}}},
	"annotations": M{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true},
}

var publishingTools = []any{
	ciLogsTool, ciRerunTool, ciRecoveryTool,
	M{
		"name":        "capabilities",
		"description": "Read the bound repository publishing capabilities: provider (github or gitlab), host, repo, branch, baseBranch, mergeMethod, autoMerge, readOnly, and supported actions.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"properties":           M{},
		},
		"annotations": M{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true},
	},
	M{
		"name":        "publish_draft",
		"description": "Push this execution branch to its granted project repository and open or reuse a draft pull request (GitHub) or draft merge request (GitLab). Enforces optional expectedHeadSha fencing.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"title", "body"},
			"properties": M{
				"requestId":         M{"type": "string"},
				"title":             M{"type": "string"},
				"body":              M{"type": "string"},
				"expectedHeadSha":   M{"type": "string"},
				"sourceExecutionId": M{"type": "string"},
			},
		},
	},
	M{
		"name":        "status",
		"description": "Read live PR/MR status, head SHA, checks/pipeline rollup, mergeability, auto-merge state and merge queue entry for this execution branch.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"properties": M{
				"expectedHeadSha": M{"type": "string"},
			},
		},
		"annotations": M{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true},
	},
	M{
		"name":        "record_code_review",
		"description": "Verify stored Fleet review evidence referencing exact expectedHeadSha, record audit provenance, and post and read back the code-review commit status on GitHub or GitLab.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"expectedHeadSha", "artifactId", "description"},
			"properties": M{
				"requestId":         M{"type": "string"},
				"expectedHeadSha":   M{"type": "string"},
				"artifactId":        M{"type": "string"},
				"description":       M{"type": "string"},
				"sourceExecutionId": M{"type": "string"},
			},
		},
	},
	M{
		"name":        "mark_ready",
		"description": "Mark the draft PR/MR ready for review idempotently, fenced by optional expectedHeadSha.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"properties": M{
				"requestId":         M{"type": "string"},
				"expectedHeadSha":   M{"type": "string"},
				"sourceExecutionId": M{"type": "string"},
			},
		},
	},
	M{
		"name":        "request_merge",
		"description": "Request project-authorized auto-merge / merge-when-pipeline-succeeds through required checks without admin bypass, fenced by optional expectedHeadSha.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"properties": M{
				"requestId":         M{"type": "string"},
				"expectedHeadSha":   M{"type": "string"},
				"mergeMethod":       M{"type": "string", "enum": []any{"squash", "merge", "rebase"}},
				"sourceExecutionId": M{"type": "string"},
			},
		},
	},
	M{
		"name":        "cancel_merge_request",
		"description": "Cancel an active auto-merge request, merge-when-pipeline-succeeds flag, or merge queue entry idempotently, fenced by optional expectedHeadSha.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"properties": M{
				"requestId":         M{"type": "string"},
				"expectedHeadSha":   M{"type": "string"},
				"sourceExecutionId": M{"type": "string"},
			},
		},
	},
}

// forgeArgs are the arguments of legacy project-forge calls.
type forgeArgs struct {
	Title          *string `json:"title"`
	Body           *string `json:"body"`
	Action         string  `json:"action"`
	HeadSha        *string `json:"head_sha"`
	ArtifactId     *string `json:"artifact_id"`
	Description    *string `json:"description"`
	ForceWithLease *string `json:"force_with_lease"`
}

// publishingArgs are the unified arguments of project-publishing MCP tools.
type publishingArgs struct {
	RequestId         *string `json:"requestId"`
	Title             *string `json:"title"`
	Body              *string `json:"body"`
	ExpectedHeadSha   *string `json:"expectedHeadSha"`
	ArtifactId        *string `json:"artifactId"`
	Description       *string `json:"description"`
	MergeMethod       *string `json:"mergeMethod"`
	SourceExecutionId *string `json:"sourceExecutionId"`
	leaseSHA          string
}

// pubView normalizes a GitHub PR or GitLab MR for shared validation and replies.
type pubView struct {
	Number           int64
	URL              string
	Title            string
	IsDraft          bool
	State            string // "OPEN", "MERGED", "CLOSED"
	BaseRefName      string
	HeadRefOid       string
	HeadRefName      string
	Kind             string // "pull_request" or "merge_request"
	AutoMergeEnabled bool
	Mergeable        string
	MergeStateStatus string
	ChecksState      string
	ReviewState      string
	Raw              any
}

type forgePR struct {
	HTMLURL jsonValue `json:"html_url"`
	Number  jsonValue `json:"number"`
}

type forgePRView struct {
	Number           int64  `json:"number"`
	URL              string `json:"url"`
	Title            string `json:"title"`
	IsDraft          bool   `json:"isDraft"`
	State            string `json:"state"`
	BaseRefName      string `json:"baseRefName"`
	HeadRefOid       string `json:"headRefOid"`
	HeadRefName      string `json:"headRefName"`
	Mergeable        string `json:"mergeable"`
	MergeStateStatus string `json:"mergeStateStatus"`
	AutoMergeRequest any    `json:"autoMergeRequest"`
}

type gitlabMRView struct {
	IID                       int64  `json:"iid"`
	WebURL                    string `json:"web_url"`
	Title                     string `json:"title"`
	Draft                     bool   `json:"draft"`
	WorkInProgress            bool   `json:"work_in_progress"`
	State                     string `json:"state"`
	TargetBranch              string `json:"target_branch"`
	SourceBranch              string `json:"source_branch"`
	SHA                       string `json:"sha"`
	MergeStatus               string `json:"merge_status"`
	DetailedMergeStatus       string `json:"detailed_merge_status"`
	MergeWhenPipelineSucceeds bool   `json:"merge_when_pipeline_succeeds"`
	HeadPipeline              *struct {
		ID     int64  `json:"id"`
		Status string `json:"status"`
		WebURL string `json:"web_url"`
		SHA    string `json:"sha"`
	} `json:"head_pipeline"`
}

type forgeBranchRule struct {
	Type string `json:"type"`
}

type forgeGraphQLQueueReply struct {
	Data struct {
		Repository struct {
			PullRequest struct {
				ID               string `json:"id"`
				AutoMergeRequest *struct {
					EnabledAt string `json:"enabledAt"`
				} `json:"autoMergeRequest"`
				MergeQueueEntry *struct {
					ID       string `json:"id"`
					State    string `json:"state"`
					Position *int64 `json:"position"`
				} `json:"mergeQueueEntry"`
			} `json:"pullRequest"`
		} `json:"repository"`
	} `json:"data"`
}

type forgeCommitStatusView struct {
	SHA      string `json:"sha"`
	State    string `json:"state"`
	Statuses []struct {
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description"`
		TargetURL   string `json:"target_url"`
		UpdatedAt   string `json:"updated_at"`
		Creator     struct {
			Login string `json:"login"`
		} `json:"creator"`
	} `json:"statuses"`
}

type gitlabCommitStatus struct {
	Name        string `json:"name"`
	Context     string `json:"context"`
	Status      string `json:"status"`
	Description string `json:"description"`
	TargetURL   string `json:"target_url"`
	UpdatedAt   string `json:"updated_at"`
	Author      struct {
		Username string `json:"username"`
	} `json:"author"`
}

func forgeRun(cmd *exec.Cmd) ([]byte, string, error) {
	var se bytes.Buffer
	cmd.Stderr = &se
	b, err := boundedOutput(cmd, probeWait)
	return b, se.String(), err
}

func forgeSanitizeErr(prefix, stderr string, err error) error {
	line, fallback := "", ""
	for _, raw := range strings.Split(stderr, "\n") {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		lower := strings.ToLower(t)
		if strings.Contains(lower, "ghp_") || strings.Contains(lower, "gho_") || strings.Contains(lower, "ghs_") ||
			strings.Contains(lower, "ghr_") || strings.Contains(lower, "ghu_") || strings.Contains(lower, "github_pat_") ||
			strings.Contains(lower, "glpat-") || strings.Contains(lower, "gloas-") || strings.Contains(lower, "glcbt-") ||
			strings.Contains(lower, "glrt-") || strings.Contains(lower, "gldt-") ||
			strings.Contains(lower, "private-token") || strings.Contains(lower, "job-token") ||
			strings.Contains(lower, "bearer ") {
			continue
		}
		if fallback == "" {
			fallback = t
		}
		if strings.HasPrefix(lower, "deprecation warning:") {
			continue
		}
		line = t
		break
	}
	if line == "" {
		line = fallback
	}
	if line == "" && err != nil {
		line = strings.TrimSpace(err.Error())
	}
	if len(line) > 200 {
		line = line[:200]
	}
	if line == "" {
		return errors.New(prefix + "; inspect status before retry")
	}
	return errors.New(prefix + ": " + line)
}

func validForgeHex40(s string) bool {
	return len(s) == 40 && onlyBytes(s, func(b byte) bool { return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') })
}

func validForgeArtifact(s string) bool {
	return s != "" && len(s) <= 200 && !strings.Contains(s, "..") && onlyBytes(s, func(b byte) bool { return alnum(b) || b == '-' || b == '_' || b == '.' })
}

func validForgeStatusDescription(s string) bool {
	if strings.TrimSpace(s) == "" || len(s) > 140 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] == 0x7f {
			return false
		}
	}
	return true
}

func forgeVerifyBranchHead(ctx forgeContext, headSha string) error {
	current, err := boundedOutput(forgeCommand(ctx, "git", "symbolic-ref", "--short", "HEAD"), probeWait)
	if err != nil || strings.TrimSpace(string(current)) != ctx.Branch {
		return errors.New("execution branch changed")
	}
	if headSha != "" {
		rev, err := boundedOutput(forgeCommand(ctx, "git", "rev-parse", "HEAD"), probeWait)
		if err != nil || strings.TrimSpace(string(rev)) != headSha {
			return errors.New("local branch head does not match reviewed head_sha")
		}
	}
	return nil
}

func checkCleanTransportConfig(ctx forgeContext) error {
	// Git can resolve a literal URL as a configured remote name.
	rewrites, err := boundedOutput(forgeCommand(ctx, "git", "config", "--get-regexp", `^(url\.|http\.|https\.|core\.gitproxy$|remote\.(https://|ssh://|git@))`), probeWait)
	var absent *exec.ExitError
	if err == nil || len(rewrites) > 0 || !errors.As(err, &absent) || absent.ExitCode() != 1 {
		return errors.New("repository transport overrides absent check failed")
	}
	return nil
}

func forgePushArgs(branch, remote, leaseSHA string) []string {
	ref := "refs/heads/" + branch
	args := []string{"-c", "push.pushOption=", "push", "--no-follow-tags", "--recurse-submodules=no"}
	source := "HEAD"
	if leaseSHA != "" {
		args = append(args, "--force-with-lease="+ref+":"+leaseSHA)
		source = ref
	}
	return append(args, "--", remote, source+":"+ref)
}

type publishingAdapter interface {
	kind() string
	verifyActorAndPush(ctx forgeContext) (string, error)
	pushBranch(ctx forgeContext, ref contract.RepoRef, leaseSHA string) error
	findOpen(ctx forgeContext) (pubView, bool, error)
	createDraft(ctx forgeContext, title, body string) (pubView, error)
	liveStatus(ctx forgeContext) (pubView, M, error)
	postCodeReview(ctx forgeContext, sha, desc, targetURL string) error
	readbackCodeReview(ctx forgeContext, sha, desc, targetURL string) (string, string, bool, error)
	markReady(ctx forgeContext, pr pubView) error
	requestMerge(ctx forgeContext, pr pubView, method string) error
	cancelMergeRequest(ctx forgeContext, pr pubView) (bool, bool, error)
}

func adapterFor(ref contract.RepoRef) publishingAdapter {
	if ref.Provider == "gitlab" {
		return gitlabAdapter{host: ref.Host}
	}
	return githubAdapter{host: ref.Host}
}

// ---------- GitHub PR Adapter ----------

type githubAdapter struct{ host string }

func (a githubAdapter) kind() string { return "pull_request" }

func (a githubAdapter) ghHost() string {
	if a.host == "" {
		return "github.com"
	}
	return a.host
}

func (a githubAdapter) repoArg(ctx forgeContext) string {
	if a.ghHost() == "github.com" {
		return ctx.Repo
	}
	return a.ghHost() + "/" + ctx.Repo
}

func (a githubAdapter) verifyActorAndPush(ctx forgeContext) (string, error) {
	userBytes, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "user"), probeWait)
	if err != nil {
		return "", errors.New("forge-actor-auth-unready")
	}
	ghUser := typedRaw[struct {
		Login string `json:"login"`
	}](userBytes)
	if strings.TrimSpace(ghUser.Login) == "" {
		return "", errors.New("forge-actor-auth-unready")
	}
	repoBytes, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "repos/"+ctx.Repo), probeWait)
	if err != nil {
		return "", errors.New("forge-repository-auth-unready")
	}
	repoInfo := typedRaw[struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
		FullName string `json:"full_name"`
	}](repoBytes)
	if !repoInfo.Permissions.Push || !strings.EqualFold(repoInfo.FullName, ctx.Repo) {
		return "", errors.New("forge-repository-push-unready")
	}
	return ghUser.Login, nil
}

func (a githubAdapter) pushBranch(ctx forgeContext, ref contract.RepoRef, leaseSHA string) error {
	if err := checkCleanTransportConfig(ctx); err != nil {
		return err
	}
	args := forgePushArgs(ctx.Branch, "https://"+a.ghHost()+"/"+ref.Slug+".git", leaseSHA)
	c := forgeCommand(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential"}, args...)...)
	if _, err := boundedOutput(c, probeWait); err != nil {
		return errors.New("project-push-failed; inspect remote branch before retry")
	}
	return nil
}

func (a githubAdapter) findOpen(ctx forgeContext) (pubView, bool, error) {
	owner := strings.Split(ctx.Repo, "/")[0]
	c := forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "repos/"+ctx.Repo+"/pulls?state=open&head="+url.QueryEscape(owner+":"+ctx.Branch))
	b, err := boundedOutput(c, probeWait)
	if err != nil {
		return pubView{}, false, errors.New("project-pr-read-failed; inspect before retry")
	}
	listed, err := dec(string(b))
	if err != nil {
		return pubView{}, false, errors.New("invalid forge response")
	}
	for _, raw := range typed[[]jsonValue](listed) {
		pr := typed[struct {
			HTMLURL string `json:"html_url"`
			Number  int64  `json:"number"`
			Title   string `json:"title"`
			Draft   bool   `json:"draft"`
			State   string `json:"state"`
			Head    struct {
				SHA string `json:"sha"`
				Ref string `json:"ref"`
			} `json:"head"`
			Base struct {
				Ref string `json:"ref"`
			} `json:"base"`
		}](raw)
		return pubView{
			Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title, IsDraft: pr.Draft,
			State: strings.ToUpper(pr.State), BaseRefName: pr.Base.Ref, HeadRefOid: pr.Head.SHA, HeadRefName: pr.Head.Ref,
			Kind: "pull_request", Raw: raw,
		}, true, nil
	}
	return pubView{}, false, nil
}

func (a githubAdapter) createDraft(ctx forgeContext, title, body string) (pubView, error) {
	c := forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "--method", "POST", "repos/"+ctx.Repo+"/pulls", "-f", "title="+title, "-f", "body="+body, "-f", "head="+ctx.Branch, "-f", "base="+ctx.Base, "-F", "draft=true")
	b, err := boundedOutput(c, probeWait)
	if err != nil {
		return pubView{}, errors.New("project-pr-create-unconfirmed; inspect existing pull request before retry")
	}
	created, err := dec(string(b))
	if err != nil {
		return pubView{}, errors.New("invalid forge response")
	}
	pr := typed[struct {
		HTMLURL string `json:"html_url"`
		Number  int64  `json:"number"`
		Title   string `json:"title"`
		Draft   bool   `json:"draft"`
		Head    struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}](created)
	return pubView{
		Number: pr.Number, URL: pr.HTMLURL, Title: pr.Title, IsDraft: pr.Draft || true,
		State: "OPEN", BaseRefName: ctx.Base, HeadRefOid: pr.Head.SHA, HeadRefName: ctx.Branch,
		Kind: "pull_request", Raw: created,
	}, nil
}

func (a githubAdapter) viewSelector(ctx forgeContext) string {
	if ctx.Branch != "" {
		return ctx.Branch
	}
	return strconv.FormatInt(ctx.Number, 10)
}

func (a githubAdapter) liveStatus(ctx forgeContext) (pubView, M, error) {
	b, stderr, err := forgeRun(forgeCommand(ctx, "gh", "pr", "view", a.viewSelector(ctx), "--repo", a.repoArg(ctx), "--json", "number,url,isDraft,state,baseRefName,headRefOid,headRefName,mergeable,statusCheckRollup,mergeStateStatus,autoMergeRequest"))
	if err != nil {
		return pubView{}, nil, forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
	}
	v, err := dec(string(b))
	if err != nil {
		return pubView{}, nil, errors.New("invalid forge response")
	}
	pr := typedRaw[forgePRView](b)
	view := pubView{
		Number: pr.Number, URL: pr.URL, Title: pr.Title, IsDraft: pr.IsDraft, State: pr.State,
		BaseRefName: pr.BaseRefName, HeadRefOid: pr.HeadRefOid, HeadRefName: pr.HeadRefName,
		Kind: "pull_request", AutoMergeEnabled: pr.AutoMergeRequest != nil,
		Mergeable: pr.Mergeable, MergeStateStatus: pr.MergeStateStatus,
	}
	m, _ := v.(M)
	if m == nil {
		m = M{}
	}
	if pr.State == "OPEN" && pr.Number > 0 && !pr.IsDraft {
		if mq := a.mergeQueueEntry(ctx, pr.Number); mq != nil {
			m["mergeQueueEntry"] = mq
			view.AutoMergeEnabled = true
		}
	}
	m["provider"] = "github"
	m["host"] = a.ghHost()
	m["repo"] = ctx.Repo
	m["kind"] = "pull_request"
	return view, m, nil
}

func (a githubAdapter) mergeQueueInfo(ctx forgeContext, number int64) (forgeGraphQLQueueReply, error) {
	var r forgeGraphQLQueueReply
	parts := strings.Split(ctx.Repo, "/")
	if len(parts) != 2 || number <= 0 {
		return r, errors.New("invalid pull request number")
	}
	q := `query($owner:String!,$name:String!,$n:Int!){repository(owner:$owner,name:$name){pullRequest(number:$n){id autoMergeRequest{enabledAt} mergeQueueEntry{id state position}}}}`
	b, stderr, err := forgeRun(forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "graphql", "-f", "query="+q, "-f", "owner="+parts[0], "-f", "name="+parts[1], "-F", "n="+strconv.FormatInt(number, 10)))
	if err != nil {
		return r, forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
	}
	return typedRaw[forgeGraphQLQueueReply](b), nil
}

func (a githubAdapter) mergeQueueEntry(ctx forgeContext, number int64) any {
	r, err := a.mergeQueueInfo(ctx, number)
	if err != nil || r.Data.Repository.PullRequest.MergeQueueEntry == nil {
		return nil
	}
	mq := r.Data.Repository.PullRequest.MergeQueueEntry
	out := M{"state": mq.State}
	if mq.Position != nil {
		out["position"] = *mq.Position
	}
	return out
}

func (a githubAdapter) baseHasMergeQueue(ctx forgeContext, base string) bool {
	if base == "" {
		return false
	}
	b, _, err := forgeRun(forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "repos/"+ctx.Repo+"/rules/branches/"+url.PathEscape(base)))
	if err != nil {
		return false
	}
	for _, r := range typedRaw[[]forgeBranchRule](b) {
		if r.Type == "merge_queue" {
			return true
		}
	}
	return false
}

func (a githubAdapter) postCodeReview(ctx forgeContext, sha, desc, targetURL string) error {
	postArgs := []string{"api", "--hostname", a.ghHost(), "--method", "POST", "repos/" + ctx.Repo + "/statuses/" + sha,
		"-f", "state=success", "-f", "context=code-review", "-f", "description=" + desc}
	if targetURL != "" {
		postArgs = append(postArgs, "-f", "target_url="+targetURL)
	}
	if _, err := boundedOutput(forgeCommand(ctx, "gh", postArgs...), probeWait); err != nil {
		return errors.New("project-code-review-post-failed; inspect commit status before retry")
	}
	return nil
}

func (a githubAdapter) readbackCodeReview(ctx forgeContext, sha, desc, targetURL string) (string, string, bool, error) {
	readbackBytes, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "repos/"+ctx.Repo+"/commits/"+sha+"/status"), probeWait)
	if err != nil {
		return "", "", false, errors.New("project-code-review-readback-unconfirmed; inspect commit status before retry")
	}
	statusView := typedRaw[forgeCommitStatusView](readbackBytes)
	if statusView.SHA != sha {
		return "", "", false, errors.New("project-code-review-readback-unconfirmed; commit sha mismatch")
	}
	for _, st := range statusView.Statuses {
		if st.Context == "code-review" && st.State == "success" && st.Description == desc && (targetURL == "" || st.TargetURL == targetURL) {
			return st.Creator.Login, st.UpdatedAt, true, nil
		}
	}
	return "", "", false, nil
}

func (a githubAdapter) markReady(ctx forgeContext, pr pubView) error {
	sel := ctx.Branch
	if sel == "" {
		sel = strconv.FormatInt(pr.Number, 10)
	}
	if _, stderr, err := forgeRun(forgeCommand(ctx, "gh", "pr", "ready", sel, "--repo", a.repoArg(ctx))); err != nil {
		return forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
	}
	return nil
}

func (a githubAdapter) requestMerge(ctx forgeContext, pr pubView, method string) error {
	base := pr.BaseRefName
	if base == "" {
		base = ctx.Base
	}
	sel := ctx.Branch
	if sel == "" {
		sel = strconv.FormatInt(pr.Number, 10)
	}
	args := []string{"pr", "merge", sel, "--repo", a.repoArg(ctx), "--auto"}
	if !a.baseHasMergeQueue(ctx, base) {
		args = append(args, "--"+method)
	}
	if _, stderr, err := forgeRun(forgeCommand(ctx, "gh", args...)); err != nil {
		return forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
	}
	return nil
}

func (a githubAdapter) cancelMergeRequest(ctx forgeContext, pr pubView) (bool, bool, error) {
	info, err := a.mergeQueueInfo(ctx, pr.Number)
	if err != nil {
		return false, false, err
	}
	prNode := info.Data.Repository.PullRequest
	if prNode.AutoMergeRequest == nil && prNode.MergeQueueEntry == nil {
		return false, true, nil
	}
	if prNode.MergeQueueEntry != nil && prNode.MergeQueueEntry.ID != "" {
		m := `mutation($id:ID!){dequeuePullRequest(input:{id:$id}){mergeQueueEntry{id}}}`
		if _, stderr, err := forgeRun(forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "graphql", "-f", "query="+m, "-f", "id="+prNode.MergeQueueEntry.ID)); err != nil {
			return false, false, forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
		}
	}
	if prNode.AutoMergeRequest != nil && prNode.ID != "" {
		m := `mutation($id:ID!){disablePullRequestAutoMerge(input:{pullRequestId:$id}){pullRequest{id}}}`
		if _, stderr, err := forgeRun(forgeCommand(ctx, "gh", "api", "--hostname", a.ghHost(), "graphql", "-f", "query="+m, "-f", "id="+prNode.ID)); err != nil {
			return false, false, forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
		}
	}
	return true, false, nil
}

// ---------- GitLab MR Adapter (gitlab.com and self-hosted GitLab) ----------

type gitlabAdapter struct{ host string }

func (a gitlabAdapter) kind() string { return "merge_request" }

func (a gitlabAdapter) glHost() string {
	if a.host == "" {
		return "gitlab.com"
	}
	return a.host
}

func (a gitlabAdapter) projPath(ctx forgeContext) string {
	return url.PathEscape(ctx.Repo)
}

func (a gitlabAdapter) verifyActorAndPush(ctx forgeContext) (string, error) {
	userBytes, err := boundedOutput(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "user"), probeWait)
	if err != nil {
		return "", errors.New("forge-actor-auth-unready")
	}
	glUser := typedRaw[struct {
		Username string `json:"username"`
	}](userBytes)
	if strings.TrimSpace(glUser.Username) == "" {
		return "", errors.New("forge-actor-auth-unready")
	}
	projBytes, err := boundedOutput(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "projects/"+a.projPath(ctx)), probeWait)
	if err != nil {
		return "", errors.New("forge-repository-auth-unready")
	}
	projInfo := typedRaw[struct {
		PathWithNamespace string `json:"path_with_namespace"`
		Permissions       struct {
			ProjectAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"project_access"`
			GroupAccess *struct {
				AccessLevel int `json:"access_level"`
			} `json:"group_access"`
		} `json:"permissions"`
	}](projBytes)
	access := 0
	if projInfo.Permissions.ProjectAccess != nil && projInfo.Permissions.ProjectAccess.AccessLevel > access {
		access = projInfo.Permissions.ProjectAccess.AccessLevel
	}
	if projInfo.Permissions.GroupAccess != nil && projInfo.Permissions.GroupAccess.AccessLevel > access {
		access = projInfo.Permissions.GroupAccess.AccessLevel
	}
	if access < 30 || !strings.EqualFold(projInfo.PathWithNamespace, ctx.Repo) {
		return "", errors.New("forge-repository-push-unready")
	}
	return glUser.Username, nil
}

func (a gitlabAdapter) pushBranch(ctx forgeContext, ref contract.RepoRef, leaseSHA string) error {
	if err := checkCleanTransportConfig(ctx); err != nil {
		return err
	}
	// If the repo binds an SSH remote and its Git connection key exists, push using the connection's SSH key.
	if (strings.HasPrefix(ctx.Remote, "git@") || strings.HasPrefix(ctx.Remote, "ssh://")) && ctx.Connection != "" && ctx.ConnectionsDir != "" {
		proj := contract.ProjectContext{}
		repo := contract.ProjectRepo{Remote: &ctx.Remote, Connection: &ctx.Connection}
		if vars, err := gitEnv(proj, &repo, ctx.ConnectionsDir); err == nil {
			c := forgeCommand(ctx, "git", forgePushArgs(ctx.Branch, ctx.Remote, leaseSHA)...)
			gitApply(c, vars)
			if _, err := boundedOutput(c, probeWait); err != nil {
				return errors.New("project-push-failed; inspect remote branch before retry")
			}
			return nil
		}
	}
	args := forgePushArgs(ctx.Branch, "https://"+a.glHost()+"/"+ref.Slug+".git", leaseSHA)
	c := forgeCommand(ctx, "git", append([]string{"-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "credential.helper=!glab auth git-credential"}, args...)...)
	if _, err := boundedOutput(c, probeWait); err != nil {
		return errors.New("project-push-failed; inspect remote branch before retry")
	}
	return nil
}

func gitlabMRState(state string) string {
	switch strings.ToLower(state) {
	case "opened":
		return "OPEN"
	case "merged":
		return "MERGED"
	case "closed":
		return "CLOSED"
	default:
		return strings.ToUpper(state)
	}
}

func gitlabToPubView(mr gitlabMRView, raw any) pubView {
	checks := ""
	if mr.HeadPipeline != nil {
		checks = mr.HeadPipeline.Status
	}
	return pubView{
		Number:           mr.IID,
		URL:              mr.WebURL,
		Title:            mr.Title,
		IsDraft:          mr.Draft || mr.WorkInProgress,
		State:            gitlabMRState(mr.State),
		BaseRefName:      mr.TargetBranch,
		HeadRefOid:       mr.SHA,
		HeadRefName:      mr.SourceBranch,
		Kind:             "merge_request",
		AutoMergeEnabled: mr.MergeWhenPipelineSucceeds,
		Mergeable:        mr.MergeStatus,
		MergeStateStatus: mr.DetailedMergeStatus,
		ChecksState:      checks,
		Raw:              raw,
	}
}

func (a gitlabAdapter) findOpen(ctx forgeContext) (pubView, bool, error) {
	c := forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "projects/"+a.projPath(ctx)+"/merge_requests?state=opened&source_branch="+url.QueryEscape(ctx.Branch))
	b, err := boundedOutput(c, probeWait)
	if err != nil {
		return pubView{}, false, errors.New("project-pr-read-failed; inspect before retry")
	}
	mrs := typedRaw[[]gitlabMRView](b)
	rawList, _ := dec(string(b))
	rawArr, _ := rawList.([]any)
	for i, mr := range mrs {
		var raw any
		if i < len(rawArr) {
			raw = rawArr[i]
		}
		return gitlabToPubView(mr, raw), true, nil
	}
	return pubView{}, false, nil
}

func (a gitlabAdapter) createDraft(ctx forgeContext, title, body string) (pubView, error) {
	draftTitle := strings.TrimSpace(title)
	if !strings.HasPrefix(strings.ToLower(draftTitle), "draft:") && !strings.HasPrefix(strings.ToLower(draftTitle), "wip:") {
		draftTitle = "Draft: " + draftTitle
	}
	c := forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "--method", "POST", "projects/"+a.projPath(ctx)+"/merge_requests",
		"-f", "source_branch="+ctx.Branch,
		"-f", "target_branch="+ctx.Base,
		"-f", "title="+draftTitle,
		"-f", "description="+body)
	b, err := boundedOutput(c, probeWait)
	if err != nil {
		return pubView{}, errors.New("project-pr-create-unconfirmed; inspect existing merge request before retry")
	}
	mr := typedRaw[gitlabMRView](b)
	raw, _ := dec(string(b))
	v := gitlabToPubView(mr, raw)
	v.IsDraft = true
	return v, nil
}

func (a gitlabAdapter) liveStatus(ctx forgeContext) (pubView, M, error) {
	var b []byte
	var stderr string
	var err error
	if ctx.Number > 0 {
		b, stderr, err = forgeRun(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "projects/"+a.projPath(ctx)+"/merge_requests/"+strconv.FormatInt(ctx.Number, 10)))
		if err != nil {
			return pubView{}, nil, forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
		}
	} else {
		b, stderr, err = forgeRun(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "projects/"+a.projPath(ctx)+"/merge_requests?state=opened&source_branch="+url.QueryEscape(ctx.Branch)))
		if err != nil {
			return pubView{}, nil, forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
		}
		mrs := typedRaw[[]gitlabMRView](b)
		if len(mrs) == 0 {
			return pubView{}, nil, errors.New("merge request is not open for this execution branch")
		}
		b = []byte(enc(mrs[0]))
	}
	mr := typedRaw[gitlabMRView](b)
	view := gitlabToPubView(mr, nil)
	out := M{
		"provider":         "gitlab",
		"host":             a.glHost(),
		"repo":             ctx.Repo,
		"kind":             "merge_request",
		"number":           view.Number,
		"url":              view.URL,
		"title":            view.Title,
		"isDraft":          view.IsDraft,
		"state":            view.State,
		"baseRefName":      view.BaseRefName,
		"headRefOid":       view.HeadRefOid,
		"headRefName":      view.HeadRefName,
		"mergeable":        view.Mergeable,
		"mergeStateStatus": view.MergeStateStatus,
	}
	if view.AutoMergeEnabled {
		out["autoMergeRequest"] = M{"enabled": true}
	}
	if mr.HeadPipeline != nil {
		out["headPipeline"] = M{"id": mr.HeadPipeline.ID, "status": mr.HeadPipeline.Status, "url": mr.HeadPipeline.WebURL, "sha": mr.HeadPipeline.SHA}
	}
	if view.HeadRefOid != "" {
		if sb, _, serr := forgeRun(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "projects/"+a.projPath(ctx)+"/repository/commits/"+view.HeadRefOid+"/statuses")); serr == nil {
			if parsed, perr := dec(string(sb)); perr == nil {
				out["statusCheckRollup"] = parsed
			}
		}
	}
	return view, out, nil
}

func (a gitlabAdapter) postCodeReview(ctx forgeContext, sha, desc, targetURL string) error {
	postArgs := []string{"api", "--hostname", a.glHost(), "--method", "POST", "projects/" + a.projPath(ctx) + "/statuses/" + sha,
		"-f", "state=success", "-f", "name=code-review", "-f", "context=code-review", "-f", "description=" + desc}
	if targetURL != "" {
		postArgs = append(postArgs, "-f", "target_url="+targetURL)
	}
	if _, err := boundedOutput(forgeCommand(ctx, "glab", postArgs...), probeWait); err != nil {
		return errors.New("project-code-review-post-failed; inspect commit status before retry")
	}
	return nil
}

func (a gitlabAdapter) readbackCodeReview(ctx forgeContext, sha, desc, targetURL string) (string, string, bool, error) {
	readbackBytes, err := boundedOutput(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "projects/"+a.projPath(ctx)+"/repository/commits/"+sha+"/statuses"), probeWait)
	if err != nil {
		return "", "", false, errors.New("project-code-review-readback-unconfirmed; inspect commit status before retry")
	}
	statuses := typedRaw[[]gitlabCommitStatus](readbackBytes)
	for _, st := range statuses {
		ctxName := st.Name
		if ctxName == "" {
			ctxName = st.Context
		}
		if ctxName == "code-review" && st.Status == "success" && st.Description == desc && (targetURL == "" || st.TargetURL == targetURL) {
			return st.Author.Username, st.UpdatedAt, true, nil
		}
	}
	return "", "", false, nil
}

func stripDraftPrefix(title string) string {
	t := strings.TrimSpace(title)
	for {
		lower := strings.ToLower(t)
		switch {
		case strings.HasPrefix(lower, "draft:"):
			t = strings.TrimSpace(t[len("draft:"):])
		case strings.HasPrefix(lower, "wip:"):
			t = strings.TrimSpace(t[len("wip:"):])
		case strings.HasPrefix(lower, "[draft]"):
			t = strings.TrimSpace(t[len("[draft]"):])
		case strings.HasPrefix(lower, "[wip]"):
			t = strings.TrimSpace(t[len("[wip]"):])
		default:
			return t
		}
	}
}

func (a gitlabAdapter) markReady(ctx forgeContext, pr pubView) error {
	readyTitle := stripDraftPrefix(pr.Title)
	if readyTitle == "" {
		readyTitle = "Ready for review"
	}
	if _, stderr, err := forgeRun(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "--method", "PUT", "projects/"+a.projPath(ctx)+"/merge_requests/"+strconv.FormatInt(pr.Number, 10), "-f", "title="+readyTitle)); err != nil {
		return forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
	}
	return nil
}

func gitlabPipelineActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "created", "waiting_for_resource", "preparing", "pending", "running", "waiting_for_callback", "scheduled":
		return true
	default:
		return false
	}
}

func (a gitlabAdapter) requestMerge(ctx forgeContext, pr pubView, method string) error {
	squash := "false"
	if method == "squash" {
		squash = "true"
	}
	args := []string{
		"api", "--hostname", a.glHost(), "--method", "PUT",
		"projects/" + a.projPath(ctx) + "/merge_requests/" + strconv.FormatInt(pr.Number, 10) + "/merge",
		"-F", "squash=" + squash,
	}
	if gitlabPipelineActive(pr.ChecksState) {
		args = append(args, "-F", "merge_when_pipeline_succeeds=true")
	}
	if pr.HeadRefOid != "" {
		args = append(args, "-f", "sha="+pr.HeadRefOid)
	}
	if _, stderr, err := forgeRun(forgeCommand(ctx, "glab", args...)); err != nil {
		return forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
	}
	return nil
}

func (a gitlabAdapter) cancelMergeRequest(ctx forgeContext, pr pubView) (bool, bool, error) {
	if !pr.AutoMergeEnabled {
		return false, true, nil
	}
	if _, stderr, err := forgeRun(forgeCommand(ctx, "glab", "api", "--hostname", a.glHost(), "--method", "POST", "projects/"+a.projPath(ctx)+"/merge_requests/"+strconv.FormatInt(pr.Number, 10)+"/cancel_merge_when_pipeline_succeeds")); err != nil {
		return false, false, forgeSanitizeErr("project-delivery-unconfirmed", stderr, err)
	}
	return true, false, nil
}

// ---------- Shared Publishing Logic ----------

func publishingLiveOpen(ctx forgeContext, adapter publishingAdapter, expectedHeadSha string) (pubView, error) {
	pr, _, err := adapter.liveStatus(ctx)
	if err != nil {
		return pubView{}, err
	}
	if pr.State != "OPEN" || pr.Number <= 0 {
		return pubView{}, errors.New("pull/merge request is not open for this execution branch")
	}
	if ctx.Branch != "" && pr.HeadRefName != "" && pr.HeadRefName != ctx.Branch {
		return pubView{}, errors.New("pull/merge request is not open for this execution branch")
	}
	if expectedHeadSha != "" && pr.HeadRefOid != expectedHeadSha {
		return pubView{}, errors.New("pull request head changed; review the current head before continuing")
	}
	return pr, nil
}

func validatePublishingContext(ctx forgeContext) (contract.RepoRef, publishingAdapter, error) {
	ref, ok := ctx.resolvedRef()
	if !ok || !contract.ForgeRepoGranted(ctx.Repositories, ref) {
		return contract.RepoRef{}, nil, errors.New("publication authority unavailable")
	}
	if !ctx.SkipWorktreeHead {
		if ctx.Prefix == "" || !strings.HasPrefix(ctx.Branch, ctx.Prefix+"/") || strings.ContainsAny(ctx.Branch, " :~^?*[\\") {
			return contract.RepoRef{}, nil, errors.New("publication authority unavailable")
		}
	} else if ctx.Branch == "" && ctx.Number <= 0 {
		return contract.RepoRef{}, nil, errors.New("publication target unavailable")
	}
	if !ctx.hasCredentialStore(ref.Provider) {
		return contract.RepoRef{}, nil, errors.New("forge-credential-store-missing")
	}
	if !ctx.SkipWorktreeHead {
		if _, err := boundedOutput(forgeCommand(ctx, "git", "check-ref-format", "--branch", ctx.Branch), probeWait); err != nil {
			return contract.RepoRef{}, nil, errors.New("invalid execution branch")
		}
		if err := forgeVerifyBranchHead(ctx, ""); err != nil {
			return contract.RepoRef{}, nil, err
		}
	}
	return ref, adapterFor(ref), nil
}

func verifyCodeReviewEvidence(ctx forgeContext, ref contract.RepoRef, adapter publishingAdapter, headSha, artifactID, desc, reqID, sourceExecID string) (M, error) {
	if !validForgeHex40(headSha) {
		return nil, errors.New("invalid head_sha / expectedHeadSha: expected full 40-character lowercase commit sha")
	}
	if !validForgeArtifact(artifactID) {
		return nil, errors.New("invalid artifact_id")
	}
	if !validForgeStatusDescription(desc) {
		return nil, errors.New("invalid description: expected non-empty single-line summary <= 140 bytes")
	}
	if ctx.HostId == "" || ctx.CommandId == "" || ctx.ExecutionId == "" || !filepath.IsAbs(ctx.Journal) || !isFile(ctx.Journal) {
		return nil, errors.New("fleet review verification context unavailable")
	}
	if err := forgeVerifyBranchHead(ctx, headSha); err != nil {
		return nil, err
	}
	providerActor, err := adapter.verifyActorAndPush(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := publishingLiveOpen(ctx, adapter, headSha); err != nil {
		return nil, err
	}

	db, err := openJournal(ctx.Journal)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	key := ctx.CommandId + ":forge-review:" + newID()
	req := contract.ForgeReviewRequest{
		HostId:        ctx.HostId,
		CommandId:     ctx.CommandId,
		ExecutionId:   ctx.ExecutionId,
		AgentToolKey:  key,
		Repo:          ref.Slug,
		Branch:        ctx.Branch,
		HeadSha:       headSha,
		ArtifactId:    artifactID,
		Description:   desc,
		GithubActor:   providerActor,
		Provider:      &ref.Provider,
		Host:          &ref.Host,
		ProviderActor: &providerActor,
	}
	if reqID != "" {
		req.RequestId = &reqID
	}
	if sourceExecID != "" {
		req.SourceExecutionId = &sourceExecID
	}
	if err := journalOnce(db, key, enc(req), contract.RouteForgeReview.Path, ""); err != nil {
		return nil, err
	}
	r, err := wait(db, "delegation_requests", key, 20, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, errors.New("project-code-review-unconfirmed; inspect status before retry")
	}
	if r.Keys.Error.Set {
		problem := typed[struct {
			Message string `json:"message"`
			Error   string `json:"error"`
		}](r.Value)
		msg := strings.TrimSpace(problem.Message)
		if msg == "" {
			msg = strings.TrimSpace(problem.Error)
		}
		if msg == "" {
			msg = "review evidence rejected"
		}
		return nil, errors.New(msg)
	}
	reply := typedRaw[contract.ForgeReviewReply]([]byte(enc(r.Value)))
	if !reply.Verified || reply.ArtifactId != artifactID {
		return nil, errors.New("review evidence verification failed")
	}

	// Re-verify branch head and open PR/MR head immediately before status write to fence concurrent head movement.
	if err := forgeVerifyBranchHead(ctx, headSha); err != nil {
		return nil, err
	}
	pr, err := publishingLiveOpen(ctx, adapter, headSha)
	if err != nil {
		return nil, err
	}
	targetURL := ""
	if reply.TargetUrl != nil && *reply.TargetUrl != "" {
		targetURL = *reply.TargetUrl
	}
	if err := adapter.postCodeReview(ctx, headSha, desc, targetURL); err != nil {
		return nil, err
	}
	statusCreator, updatedAt, matched, err := adapter.readbackCodeReview(ctx, headSha, desc, targetURL)
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, errors.New("project-code-review-readback-unconfirmed; code-review status absent on reviewed head")
	}

	out := M{
		"action":            "code_review",
		"provider":          ref.Provider,
		"host":              ref.Host,
		"repo":              ref.Slug,
		"branch":            ctx.Branch,
		"kind":              adapter.kind(),
		"prNumber":          pr.Number,
		"number":            pr.Number,
		"prUrl":             pr.URL,
		"url":               pr.URL,
		"headSha":           headSha,
		"context":           "code-review",
		"state":             "success",
		"description":       desc,
		"artifactId":        reply.ArtifactId,
		"artifactSha256":    reply.ArtifactSha256,
		"providerActor":     providerActor,
		"githubActor":       providerActor,
		"fleetActor":        reply.Actor,
		"independentReview": reply.Independent,
		"readbackVerified":  true,
	}
	if reqID != "" {
		out["requestId"] = reqID
	}
	if sourceExecID != "" {
		out["sourceExecutionId"] = sourceExecID
	}
	if reply.OnBehalfOf != nil && *reply.OnBehalfOf != "" {
		out["onBehalfOf"] = *reply.OnBehalfOf
	}
	if reply.ReviewExecutionId != nil && *reply.ReviewExecutionId != "" {
		out["reviewExecutionId"] = *reply.ReviewExecutionId
	}
	if statusCreator != "" {
		out["statusCreator"] = statusCreator
	}
	if updatedAt != "" {
		out["updatedAt"] = updatedAt
	}
	if targetURL != "" {
		out["targetUrl"] = targetURL
	}
	return out, nil
}

func recordPublishingToJournal(ctx forgeContext, ref contract.RepoRef, adapter publishingAdapter, action string, pr pubView, reqID string, rawResult M) {
	if !ctx.ReportPublishing || ctx.HostId == "" || ctx.Journal == "" || pr.Number <= 0 {
		return
	}
	db, err := openJournal(ctx.Journal)
	if err != nil {
		return
	}
	defer db.Close()
	rec := contract.PublishingHostRecord{
		HostId:            ctx.HostId,
		CommandId:         optStr(ctx.CommandId),
		ExecutionId:       optStr(ctx.ExecutionId),
		RequestId:         optStr(reqID),
		Action:            optStr(action),
		ProjectId:         optStr(ctx.ProjectId),
		RepoId:            optStr(ctx.RepoId),
		Provider:          ref.Provider,
		Host:              ref.Host,
		Repo:              ref.Slug,
		Kind:              adapter.kind(),
		Number:            pr.Number,
		Title:             pr.Title,
		Url:               pr.URL,
		Branch:            firstStr(pr.HeadRefName, ctx.Branch),
		BaseBranch:        optStr(firstStr(pr.BaseRefName, ctx.Base)),
		HeadSha:           pr.HeadRefOid,
		IsDraft:           pr.IsDraft,
		State:             pr.State,
		AutoMergeEnabled:  &pr.AutoMergeEnabled,
		MergeMethod:       optStr(ctx.MergeMethod),
		SourceExecutionId: optStr(ctx.ExecutionId),
		Result:            json.RawMessage(enc(rawResult)),
	}
	key := "pub:" + ctx.CommandId + ":" + newID()
	_ = journalOnce(db, key, enc(rec), contract.RoutePublishingRecord.Path, "")
}

func firstStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func publishingCapabilitiesReply(ctx forgeContext, ref contract.RepoRef, adapter publishingAdapter) M {
	actions := []string{"capabilities", "status", "ci_logs"}
	if !ctx.ReadOnly {
		actions = append(actions, "publish_draft", "record_code_review", "mark_ready")
		if ctx.AutoMerge {
			actions = append(actions, "request_merge", "cancel_merge_request")
		} else {
			actions = append(actions, "cancel_merge_request")
		}
	}
	return M{
		"provider":    ref.Provider,
		"host":        ref.Host,
		"repo":        ref.Slug,
		"kind":        adapter.kind(),
		"branch":      ctx.Branch,
		"baseBranch":  ctx.Base,
		"mergeMethod": ctx.MergeMethod,
		"autoMerge":   ctx.AutoMerge,
		"readOnly":    ctx.ReadOnly,
		"actions":     actions,
	}
}

func checkExpectedHeadAndSource(ctx forgeContext, expectedHeadSha, sourceExecID *string) (string, string, error) {
	sha := ""
	if expectedHeadSha != nil && *expectedHeadSha != "" {
		sha = strings.TrimSpace(*expectedHeadSha)
		if !validForgeHex40(sha) {
			return "", "", errors.New("invalid head_sha / expectedHeadSha: expected full 40-character lowercase commit sha")
		}
	}
	src := ctx.ExecutionId
	if sourceExecID != nil && *sourceExecID != "" {
		if ctx.ExecutionId != "" && *sourceExecID != ctx.ExecutionId {
			return "", "", errors.New("sourceExecutionId does not match current execution")
		}
		src = *sourceExecID
	}
	return sha, src, nil
}

func executePublishingAction(ctx forgeContext, action string, args publishingArgs) (M, error) {
	ref, adapter, err := validatePublishingContext(ctx)
	if err != nil {
		return nil, err
	}
	if action == "capabilities" {
		return publishingCapabilitiesReply(ctx, ref, adapter), nil
	}
	if ctx.ReadOnly && action != "status" {
		return nil, errors.New("project-forge-read-only")
	}
	reqID := ""
	if args.RequestId != nil {
		reqID = strings.TrimSpace(*args.RequestId)
	}
	expectedSha, srcExec, err := checkExpectedHeadAndSource(ctx, args.ExpectedHeadSha, args.SourceExecutionId)
	if err != nil {
		return nil, err
	}

	switch action {
	case "status":
		view, rawMap, err := adapter.liveStatus(ctx)
		if err != nil {
			return nil, err
		}
		if expectedSha != "" {
			rawMap["expectedHeadSha"] = expectedSha
			rawMap["headMatchesExpected"] = view.HeadRefOid == expectedSha
		}
		recordPublishingToJournal(ctx, ref, adapter, action, view, reqID, rawMap)
		return rawMap, nil

	case "publish_draft":
		if args.Title == nil || args.Body == nil || strings.TrimSpace(*args.Title) == "" {
			return nil, errors.New("invalid publication request")
		}
		if ctx.SkipWorktreeHead || ctx.Branch == "main" || ctx.Branch == strings.TrimPrefix(ctx.Base, "refs/heads/") {
			return nil, errors.New("publication authority unavailable")
		}
		if args.leaseSHA != "" && (!strings.HasPrefix(ctx.Branch, "fleet/") || ctx.Branch != ctx.SessionBranch) {
			return nil, errors.New("force_with_lease requires the granted fleet/* branch")
		}
		if expectedSha != "" {
			if err := forgeVerifyBranchHead(ctx, expectedSha); err != nil {
				return nil, err
			}
		}
		if err := adapter.pushBranch(ctx, ref, args.leaseSHA); err != nil {
			return nil, err
		}
		if existing, ok, err := adapter.findOpen(ctx); err != nil {
			return nil, err
		} else if ok {
			out := M{
				"provider": ref.Provider, "host": ref.Host, "repo": ref.Slug, "branch": ctx.Branch, "baseBranch": ctx.Base,
				"kind": adapter.kind(), "number": existing.Number, "url": existing.URL, "headSha": existing.HeadRefOid,
				"isDraft": existing.IsDraft, "existing": true,
			}
			if reqID != "" {
				out["requestId"] = reqID
			}
			if srcExec != "" {
				out["sourceExecutionId"] = srcExec
			}
			recordPublishingToJournal(ctx, ref, adapter, action, existing, reqID, out)
			return out, nil
		}
		created, err := adapter.createDraft(ctx, *args.Title, *args.Body)
		if err != nil {
			return nil, err
		}
		out := M{
			"provider": ref.Provider, "host": ref.Host, "repo": ref.Slug, "branch": ctx.Branch, "baseBranch": ctx.Base,
			"kind": adapter.kind(), "number": created.Number, "url": created.URL, "headSha": created.HeadRefOid,
			"isDraft": true, "existing": false,
		}
		if reqID != "" {
			out["requestId"] = reqID
		}
		if srcExec != "" {
			out["sourceExecutionId"] = srcExec
		}
		recordPublishingToJournal(ctx, ref, adapter, action, created, reqID, out)
		return out, nil

	case "record_code_review":
		if args.ExpectedHeadSha == nil || args.ArtifactId == nil || args.Description == nil {
			return nil, errors.New("invalid publication request")
		}
		return verifyCodeReviewEvidence(ctx, ref, adapter, *args.ExpectedHeadSha, *args.ArtifactId, *args.Description, reqID, srcExec)

	case "mark_ready":
		pr, err := publishingLiveOpen(ctx, adapter, expectedSha)
		if err != nil {
			return nil, err
		}
		if !pr.IsDraft {
			out := M{
				"action": "mark_ready", "provider": ref.Provider, "host": ref.Host, "repo": ref.Slug,
				"branch": firstStr(pr.HeadRefName, ctx.Branch), "kind": adapter.kind(),
				"requested": true, "alreadyReady": true, "isDraft": false,
				"prNumber": pr.Number, "number": pr.Number, "prUrl": pr.URL, "url": pr.URL,
				"headSha": pr.HeadRefOid, "note": "Pull/merge request is already ready for review.",
			}
			if reqID != "" {
				out["requestId"] = reqID
			}
			recordPublishingToJournal(ctx, ref, adapter, action, pr, reqID, out)
			return out, nil
		}
		if err := adapter.markReady(ctx, pr); err != nil {
			return nil, err
		}
		pr.IsDraft = false
		out := M{
			"action": "mark_ready", "provider": ref.Provider, "host": ref.Host, "repo": ref.Slug,
			"branch": firstStr(pr.HeadRefName, ctx.Branch), "kind": adapter.kind(),
			"requested": true, "alreadyReady": false, "isDraft": false,
			"prNumber": pr.Number, "number": pr.Number, "prUrl": pr.URL, "url": pr.URL,
			"headSha": pr.HeadRefOid, "note": "Read status to establish the resulting state; required checks and independent review still apply.",
		}
		if reqID != "" {
			out["requestId"] = reqID
		}
		recordPublishingToJournal(ctx, ref, adapter, action, pr, reqID, out)
		return out, nil

	case "request_merge":
		if !ctx.AutoMerge {
			return nil, errors.New("project auto-merge not granted")
		}
		method := ctx.MergeMethod
		if method != "squash" && method != "merge" && method != "rebase" {
			return nil, errors.New("project merge method unavailable")
		}
		if args.MergeMethod != nil && *args.MergeMethod != "" {
			if *args.MergeMethod != method {
				return nil, errors.New("merge method differs from project integration policy")
			}
		}
		pr, err := publishingLiveOpen(ctx, adapter, expectedSha)
		if err != nil {
			return nil, err
		}
		if pr.IsDraft {
			return nil, errors.New("pull/merge request is still a draft; mark ready before enabling auto-merge")
		}
		if err := adapter.requestMerge(ctx, pr, method); err != nil {
			return nil, err
		}
		pr.AutoMergeEnabled = true
		out := M{
			"action": "request_merge", "provider": ref.Provider, "host": ref.Host, "repo": ref.Slug,
			"branch": firstStr(pr.HeadRefName, ctx.Branch), "kind": adapter.kind(),
			"requested": true, "autoMergeEnabled": true, "mergeMethod": method,
			"prNumber": pr.Number, "number": pr.Number, "prUrl": pr.URL, "url": pr.URL,
			"headSha": pr.HeadRefOid, "note": "Read status to establish the resulting state; required checks and independent review still apply.",
		}
		if reqID != "" {
			out["requestId"] = reqID
		}
		recordPublishingToJournal(ctx, ref, adapter, action, pr, reqID, out)
		return out, nil

	case "cancel_merge_request":
		pr, err := publishingLiveOpen(ctx, adapter, expectedSha)
		if err != nil {
			return nil, err
		}
		cancelled, alreadyInactive, err := adapter.cancelMergeRequest(ctx, pr)
		if err != nil {
			return nil, err
		}
		pr.AutoMergeEnabled = false
		out := M{
			"action": "cancel_merge_request", "provider": ref.Provider, "host": ref.Host, "repo": ref.Slug,
			"branch": firstStr(pr.HeadRefName, ctx.Branch), "kind": adapter.kind(),
			"cancelled": cancelled, "alreadyInactive": alreadyInactive,
			"prNumber": pr.Number, "number": pr.Number, "prUrl": pr.URL, "url": pr.URL,
			"headSha": pr.HeadRefOid,
		}
		if reqID != "" {
			out["requestId"] = reqID
		}
		recordPublishingToJournal(ctx, ref, adapter, action, pr, reqID, out)
		return out, nil
	}
	return nil, errors.New("invalid publication request")
}

// publishingCall answers one tools/call of the unified project-publishing MCP server.
func publishingCall(name string, arguments, context any) (any, error) {
	if name == "ci_logs" {
		return ciLogsCall(arguments, typed[forgeContext](context))
	}
	keys, ok := jClone(arguments).(M)
	if !ok {
		keys = M{}
	}
	allowed := map[string]map[string]bool{
		"capabilities":         {},
		"publish_draft":        {"requestId": true, "title": true, "body": true, "expectedHeadSha": true, "sourceExecutionId": true},
		"status":               {"expectedHeadSha": true},
		"record_code_review":   {"requestId": true, "expectedHeadSha": true, "artifactId": true, "description": true, "sourceExecutionId": true},
		"mark_ready":           {"requestId": true, "expectedHeadSha": true, "sourceExecutionId": true},
		"request_merge":        {"requestId": true, "expectedHeadSha": true, "mergeMethod": true, "sourceExecutionId": true},
		"cancel_merge_request": {"requestId": true, "expectedHeadSha": true, "sourceExecutionId": true},
	}
	schemaKeys, known := allowed[name]
	if !known {
		return nil, errors.New("invalid publication request")
	}
	for k := range keys {
		if !schemaKeys[k] {
			return nil, errors.New("invalid publication request")
		}
	}
	a := typed[publishingArgs](keys)
	ctx := typed[forgeContext](context)
	out, err := executePublishingAction(ctx, name, a)
	if err != nil {
		return nil, err
	}
	return mcpText(out, &no), nil
}

// forgeCall answers one tools/call of the legacy project-forge MCP server (additive compatibility alias).
func forgeCall(name string, arguments, context any) (any, error) {
	if name == "ci_logs" {
		return ciLogsCall(arguments, typed[forgeContext](context))
	}
	keys, _ := jClone(arguments).(M)
	a := typed[forgeArgs](keys)
	validDelivery := (a.Action == "status" || a.Action == "ready" || a.Action == "auto_merge") && len(keys) == 1 ||
		a.Action == "code_review" && len(keys) == 4 && a.HeadSha != nil && a.ArtifactId != nil && a.Description != nil
	leaseSHA := ""
	publishKeys := 2
	if _, hasLease := keys["force_with_lease"]; name == "publish_draft" && hasLease {
		leaseSHA = str0(a.ForceWithLease)
		if !validForgeHex40(leaseSHA) {
			return nil, errors.New("invalid force_with_lease: expected full 40-character lowercase remote commit sha")
		}
		publishKeys++
	}
	if (name == "publish_draft" && (len(keys) != publishKeys || a.Title == nil || a.Body == nil || strings.TrimSpace(*a.Title) == "")) || (name == "delivery" && !validDelivery) || (name != "publish_draft" && name != "delivery") {
		return nil, errors.New("invalid publication request")
	}
	ctx := typed[forgeContext](context)
	if name == "publish_draft" {
		out, err := executePublishingAction(ctx, "publish_draft", publishingArgs{Title: a.Title, Body: a.Body, leaseSHA: leaseSHA})
		if err != nil {
			return nil, err
		}
		return mcpText(M{"url": out["url"], "number": out["number"], "existing": out["existing"]}, &no), nil
	}
	mapped := a.Action
	switch a.Action {
	case "ready":
		mapped = "mark_ready"
	case "auto_merge":
		mapped = "request_merge"
	case "code_review":
		mapped = "record_code_review"
	}
	out, err := executePublishingAction(ctx, mapped, publishingArgs{
		ExpectedHeadSha: a.HeadSha,
		ArtifactId:      a.ArtifactId,
		Description:     a.Description,
	})
	if err != nil {
		return nil, err
	}
	if a.Action == "ready" || a.Action == "auto_merge" {
		out["action"] = a.Action
	}
	return mcpText(out, &no), nil
}

func forgeGranted(repo string) bool {
	if repo == "" {
		return false
	}
	ref, ok := contract.ParseRepoRemote("https://github.com/"+repo+".git", "github", "github.com")
	if !ok {
		return false
	}
	return contract.ForgeRepoGranted(os.Getenv("FLEET_FORGE_REPOSITORIES"), ref)
}

func forgeRepoGrantedRef(ref contract.RepoRef) bool {
	return contract.ForgeRepoGranted(os.Getenv("FLEET_FORGE_REPOSITORIES"), ref)
}

// forgeReadiness checks readiness for a GitHub or GitLab repository binding.
func forgeReadiness(remote string) string {
	return publishingReadiness(remote, "", "")
}

func publishingReadiness(remote, forgeHint, hostHint string) string {
	ref, ok := contract.ParseRepoRemote(remote, forgeHint, hostHint)
	if !ok || !forgeRepoGrantedRef(ref) {
		return "forge-repository-not-granted"
	}
	store := os.Getenv("FLEET_FORGE_CONFIG_DIR")
	ctx := forgeContext{
		Provider:       ref.Provider,
		Host:           ref.Host,
		Repo:           ref.Slug,
		Remote:         remote,
		Store:          store,
		GlabStore:      resolveGlabStore(store),
		Home:           os.TempDir(),
		CredentialHome: os.Getenv("HOME"),
		Workdir:        os.TempDir(),
	}
	if !ctx.hasCredentialStore(ref.Provider) {
		return "forge-credential-store-missing"
	}
	if _, err := adapterFor(ref).verifyActorAndPush(ctx); err != nil {
		return err.Error()
	}
	return ""
}

func (e *executor) runPublishingCommand(cmd contract.Command, execution, cid string) error {
	if cmd.Publishing == nil || cmd.Publishing.Payload == nil {
		return e.finish(cmd, "failed", "")
	}
	p := cmd.Publishing.Payload
	if p.Action == "rerun_failed" && p.CiRecovery != nil {
		return e.runCIRerunCommand(cmd, execution, cid)
	}
	store := os.Getenv("FLEET_FORGE_CONFIG_DIR")
	var num int64
	if p.Number != nil {
		num = *p.Number
	}
	skipHead := p.Action != "publish_draft" && p.Action != "record_code_review"
	workdir := os.TempDir()
	if p.SourceExecutionId != nil && *p.SourceExecutionId != "" {
		var mcpDir, sess, body string
		_ = e.db.QueryRow("SELECT mcp_dir FROM launches WHERE execution=? LIMIT 1", *p.SourceExecutionId).Scan(&mcpDir)
		if mcpDir != "" {
			if wt := filepath.Join(filepath.Dir(mcpDir), "worktree"); exists(filepath.Join(wt, ".git")) {
				workdir = wt
			}
		}
		if workdir == os.TempDir() {
			_ = e.db.QueryRow("SELECT session,body FROM commands WHERE execution=? ORDER BY rowid DESC LIMIT 1", *p.SourceExecutionId).Scan(&sess, &body)
			if sess != "" {
				var rawCmd contract.Command
				_ = json.Unmarshal([]byte(body), &rawCmd)
				if wt := filepath.Join(e.commandDir(sess, workspaceKey(rawCmd)), "worktree"); exists(filepath.Join(wt, ".git")) {
					workdir = wt
				}
			}
		}
	}
	ctx := forgeContext{
		Repositories:     os.Getenv("FLEET_FORGE_REPOSITORIES"),
		CredentialHome:   os.Getenv("HOME"),
		Provider:         p.Provider,
		Host:             p.Host,
		Repo:             p.Repo,
		Remote:           str0(p.Remote),
		Base:             p.BaseBranch,
		Branch:           str0(p.Branch),
		Prefix:           str0(p.Prefix),
		Number:           num,
		AutoMerge:        p.AutoMerge,
		MergeMethod:      p.MergeMethod,
		Workdir:          workdir,
		Home:             firstStr(os.Getenv("HOME"), os.TempDir()),
		Store:            store,
		GlabStore:        resolveGlabStore(store),
		HostId:           e.host,
		CommandId:        cid,
		ExecutionId:      execution,
		ProjectId:        p.ProjectId,
		RepoId:           str0(p.RepoId),
		Journal:          filepath.Join(e.root, "journal.sqlite"),
		SkipWorktreeHead: skipHead,
	}
	var mergeMethod *string
	if p.MergeMethod != "" {
		mergeMethod = &p.MergeMethod
	}
	var res M
	var err error
	if !skipHead && !exists(filepath.Join(workdir, ".git")) {
		err = errors.New("source execution worktree unavailable on host")
	} else {
		res, err = executePublishingAction(ctx, p.Action, publishingArgs{
			RequestId:         &p.RequestId,
			Title:             p.Title,
			Body:              p.Body,
			ExpectedHeadSha:   p.ExpectedHeadSha,
			ArtifactId:        p.ArtifactId,
			Description:       p.Description,
			MergeMethod:       mergeMethod,
			SourceExecutionId: p.SourceExecutionId,
		})
	}
	rec := contract.PublishingHostRecord{
		HostId:            e.host,
		CommandId:         &cid,
		ExecutionId:       &execution,
		RequestId:         &p.RequestId,
		Action:            &p.Action,
		ProjectId:         &p.ProjectId,
		RepoId:            p.RepoId,
		Provider:          p.Provider,
		Host:              p.Host,
		Repo:              p.Repo,
		Kind:              map[string]string{"github": "pull_request", "gitlab": "merge_request"}[p.Provider],
		Number:            num,
		Branch:            str0(p.Branch),
		BaseBranch:        &p.BaseBranch,
		SourceExecutionId: p.SourceExecutionId,
	}
	if err != nil {
		msg := err.Error()
		rec.Error = &msg
		rec.State = "FAILED"
	} else {
		if n, ok := res["number"].(int64); ok && n > 0 {
			rec.Number = n
		} else if nf, ok := res["number"].(float64); ok && nf > 0 {
			rec.Number = int64(nf)
		}
		if u, ok := res["url"].(string); ok {
			rec.Url = u
		}
		if t, ok := res["title"].(string); ok {
			rec.Title = t
		}
		if b, ok := res["branch"].(string); ok && b != "" {
			rec.Branch = b
		}
		if h, ok := res["headSha"].(string); ok {
			rec.HeadSha = h
		}
		if d, ok := res["isDraft"].(bool); ok {
			rec.IsDraft = d
		}
		if am, ok := res["autoMergeEnabled"].(bool); ok {
			rec.AutoMergeEnabled = &am
		}
		if st, ok := res["state"].(string); ok && st != "" {
			rec.State = st
		} else {
			rec.State = "OPEN"
		}
		rec.Result = json.RawMessage(enc(res))
	}
	if qerr := queueTo(e.ex, contract.RoutePublishingRecord, rec); qerr != nil {
		return qerr
	}
	return e.finish(cmd, okStatus(err == nil), "")
}
