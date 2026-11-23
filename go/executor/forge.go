package main

import (
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fleet/contract"
)

func forgeRepo(remote string) string {
	u, err := url.Parse(remote)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return ""
	}
	parts := strings.Split(strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git"), "/")
	if len(parts) != 2 {
		return ""
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || !onlyBytes(p, func(b byte) bool { return alnum(b) || b == '-' || b == '_' || b == '.' }) {
			return ""
		}
	}
	return strings.Join(parts, "/")
}

// forgeContext is the project forge server's context, built by the executor at launch (argv of `mcp project-forge`).
type forgeContext struct {
	Repositories   string `json:"repositories"`
	CredentialHome string `json:"credentialHome"`
	Repo           string `json:"repo"`
	Base           string `json:"base"`
	Branch         string `json:"branch"`
	Prefix         string `json:"prefix"`
	AutoMerge      bool   `json:"autoMerge"`
	MergeMethod    string `json:"mergeMethod"`
	Workdir        string `json:"workdir"`
	Home           string `json:"home"`
	Store          string `json:"store"`
	HostId         string `json:"hostId,omitempty"`
	CommandId      string `json:"commandId,omitempty"`
	ExecutionId    string `json:"executionId,omitempty"`
	ProjectId      string `json:"projectId,omitempty"`
	Journal        string `json:"journal,omitempty"`
	ReadOnly       bool   `json:"readOnly,omitempty"`
}

// forgeCommand reads only the explicitly connected host credential store; native HOME stays isolated.
func forgeCommand(ctx forgeContext, name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Dir = ctx.Workdir
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + ctx.Home, "GH_CONFIG_DIR=" + ctx.Store, "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	credential := exec.Command("gh", "auth", "token", "--hostname", "github.com")
	credential.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + ctx.CredentialHome, "GH_CONFIG_DIR=" + ctx.Store, "GH_PROMPT_DISABLED=1"}
	if token, err := boundedOutput(credential, probeWait); err == nil {
		c.Env = append(c.Env, "GH_TOKEN="+strings.TrimSpace(string(token)))
	}
	return c
}

var forgeTools = []any{
	M{
		"name":        "publish_draft",
		"description": "Push this execution branch to its granted project repository and open or reuse a draft pull request. Never merges.",
		"inputSchema": M{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"title", "body"},
			"properties": M{
				"title": M{"type": "string"},
				"body":  M{"type": "string"},
			},
		},
	},
	M{
		"name":        "delivery",
		"description": "Read this branch PR checks/head/status, post exact-SHA code-review status with verified Fleet review evidence (head_sha, artifact_id, description), mark it ready, or enable project-authorized auto-merge through required checks. Independent review remains required.",
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

// forgeArgs are the arguments of publish_draft ({title, body}) and delivery ({action, ...}).
type forgeArgs struct {
	Title       *string `json:"title"`
	Body        *string `json:"body"`
	Action      string  `json:"action"`
	HeadSha     *string `json:"head_sha"`
	ArtifactId  *string `json:"artifact_id"`
	Description *string `json:"description"`
}

// forgePR is what a publication answers of a GitHub pull request.
type forgePR struct {
	HTMLURL jsonValue `json:"html_url"`
	Number  jsonValue `json:"number"`
}

type forgePRView struct {
	Number      int64  `json:"number"`
	URL         string `json:"url"`
	IsDraft     bool   `json:"isDraft"`
	State       string `json:"state"`
	HeadRefOid  string `json:"headRefOid"`
	HeadRefName string `json:"headRefName"`
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

func forgePRReply(raw any, existing bool) M {
	pr := typed[forgePR](raw)
	return mcpText(M{"url": pr.HTMLURL.V, "number": pr.Number.V, "existing": existing}, &no)
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
	rev, err := boundedOutput(forgeCommand(ctx, "git", "rev-parse", "HEAD"), probeWait)
	if err != nil || strings.TrimSpace(string(rev)) != headSha {
		return errors.New("local branch head does not match reviewed head_sha")
	}
	return nil
}

func forgeLivePRForHead(ctx forgeContext, headSha string) (forgePRView, error) {
	b, err := boundedOutput(forgeCommand(ctx, "gh", "pr", "view", ctx.Branch, "--repo", ctx.Repo, "--json", "number,url,isDraft,state,headRefOid,headRefName"), probeWait)
	if err != nil {
		return forgePRView{}, errors.New("project-code-review-pr-unconfirmed; inspect status before retry")
	}
	pr := typedRaw[forgePRView](b)
	if pr.State != "OPEN" || pr.HeadRefName != ctx.Branch || pr.Number <= 0 {
		return forgePRView{}, errors.New("pull request is not open for this execution branch")
	}
	if pr.HeadRefOid != headSha {
		return forgePRView{}, errors.New("pull request head changed; review the current head before posting code-review")
	}
	return pr, nil
}

func forgeCodeReview(ctx forgeContext, a forgeArgs) (any, error) {
	headSha, artifactID, desc := *a.HeadSha, *a.ArtifactId, *a.Description
	if !validForgeHex40(headSha) {
		return nil, errors.New("invalid head_sha: expected full 40-character lowercase commit sha")
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
	userBytes, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "user"), probeWait)
	if err != nil {
		return nil, errors.New("forge-actor-auth-unready")
	}
	ghUser := typedRaw[struct {
		Login string `json:"login"`
	}](userBytes)
	if strings.TrimSpace(ghUser.Login) == "" {
		return nil, errors.New("forge-actor-auth-unready")
	}
	repoBytes, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "repos/"+ctx.Repo), probeWait)
	if err != nil {
		return nil, errors.New("forge-repository-auth-unready")
	}
	repoInfo := typedRaw[struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
		FullName string `json:"full_name"`
	}](repoBytes)
	if !repoInfo.Permissions.Push || repoInfo.FullName != ctx.Repo {
		return nil, errors.New("forge-repository-push-unready")
	}
	if _, err := forgeLivePRForHead(ctx, headSha); err != nil {
		return nil, err
	}

	db, err := openJournal(ctx.Journal)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	key := ctx.CommandId + ":forge-review:" + newID()
	req := contract.ForgeReviewRequest{
		HostId:       ctx.HostId,
		CommandId:    ctx.CommandId,
		ExecutionId:  ctx.ExecutionId,
		AgentToolKey: key,
		Repo:         ctx.Repo,
		Branch:       ctx.Branch,
		HeadSha:      headSha,
		ArtifactId:   artifactID,
		Description:  desc,
		GithubActor:  ghUser.Login,
	}
	if err := journalOnce(db, key, enc(req), contract.RouteForgeReview.Path); err != nil {
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

	// Re-verify branch head and open PR head immediately before status write to fence concurrent head movement.
	if err := forgeVerifyBranchHead(ctx, headSha); err != nil {
		return nil, err
	}
	pr, err := forgeLivePRForHead(ctx, headSha)
	if err != nil {
		return nil, err
	}

	postArgs := []string{"api", "--hostname", "github.com", "--method", "POST", "repos/" + ctx.Repo + "/statuses/" + headSha,
		"-f", "state=success", "-f", "context=code-review", "-f", "description=" + desc}
	targetURL := ""
	if reply.TargetUrl != nil && *reply.TargetUrl != "" {
		targetURL = *reply.TargetUrl
		postArgs = append(postArgs, "-f", "target_url="+targetURL)
	}
	if _, err := boundedOutput(forgeCommand(ctx, "gh", postArgs...), probeWait); err != nil {
		return nil, errors.New("project-code-review-post-failed; inspect commit status before retry")
	}

	readbackBytes, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "repos/"+ctx.Repo+"/commits/"+headSha+"/status"), probeWait)
	if err != nil {
		return nil, errors.New("project-code-review-readback-unconfirmed; inspect commit status before retry")
	}
	statusView := typedRaw[forgeCommitStatusView](readbackBytes)
	if statusView.SHA != headSha {
		return nil, errors.New("project-code-review-readback-unconfirmed; commit sha mismatch")
	}
	matched := false
	statusCreator := ""
	updatedAt := ""
	for _, st := range statusView.Statuses {
		if st.Context == "code-review" && st.State == "success" && st.Description == desc && (targetURL == "" || st.TargetURL == targetURL) {
			matched = true
			statusCreator = st.Creator.Login
			updatedAt = st.UpdatedAt
			break
		}
	}
	if !matched {
		return nil, errors.New("project-code-review-readback-unconfirmed; code-review status absent on reviewed head")
	}

	out := M{
		"action":            "code_review",
		"repo":              ctx.Repo,
		"branch":            ctx.Branch,
		"prNumber":          pr.Number,
		"prUrl":             pr.URL,
		"headSha":           headSha,
		"context":           "code-review",
		"state":             "success",
		"description":       desc,
		"artifactId":        reply.ArtifactId,
		"artifactSha256":    reply.ArtifactSha256,
		"githubActor":       ghUser.Login,
		"fleetActor":        reply.Actor,
		"independentReview": reply.Independent,
		"readbackVerified":  true,
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
	return mcpText(out, &no), nil
}

// forgeCall answers one tools/call of the project forge server.
func forgeCall(name string, arguments, context any) (any, error) {
	// A member of another type reads as absent; the key counts then refuse the request, as a strict decode did.
	keys, _ := jClone(arguments).(M)
	a := typed[forgeArgs](keys)
	validDelivery := (a.Action == "status" || a.Action == "ready" || a.Action == "auto_merge") && len(keys) == 1 ||
		a.Action == "code_review" && len(keys) == 4 && a.HeadSha != nil && a.ArtifactId != nil && a.Description != nil
	if (name == "publish_draft" && (len(keys) != 2 || a.Title == nil || a.Body == nil || strings.TrimSpace(*a.Title) == "")) || (name == "delivery" && !validDelivery) || (name != "publish_draft" && name != "delivery") {
		return nil, errors.New("invalid publication request")
	}
	ctx := typed[forgeContext](context)
	if ctx.ReadOnly && (name == "publish_draft" || (name == "delivery" && a.Action != "status")) {
		return nil, errors.New("project-forge-read-only")
	}
	repo, branch := ctx.Repo, ctx.Branch
	if !strings.Contains(","+ctx.Repositories+",", ","+repo+",") || repo == "" || forgeRepo("https://github.com/"+repo) != repo || ctx.Prefix == "" || !strings.HasPrefix(branch, ctx.Prefix+"/") || strings.ContainsAny(branch, " :~^?*[\\") {
		return nil, errors.New("publication authority unavailable")
	}
	if !filepath.IsAbs(ctx.Store) || !isFile(filepath.Join(ctx.Store, "hosts.yml")) {
		return nil, errors.New("forge-credential-store-missing")
	}
	if _, err := boundedOutput(forgeCommand(ctx, "git", "check-ref-format", "--branch", branch), probeWait); err != nil {
		return nil, errors.New("invalid execution branch")
	}
	current, err := boundedOutput(forgeCommand(ctx, "git", "symbolic-ref", "--short", "HEAD"), probeWait)
	if err != nil || strings.TrimSpace(string(current)) != branch {
		return nil, errors.New("execution branch changed")
	}
	if name == "delivery" {
		if a.Action == "code_review" {
			return forgeCodeReview(ctx, a)
		}
		args := []string{"pr", "view", branch, "--repo", repo, "--json", "number,url,isDraft,state,headRefOid,statusCheckRollup,mergeStateStatus,autoMergeRequest"}
		switch a.Action {
		case "ready":
			args = []string{"pr", "ready", branch, "--repo", repo}
		case "auto_merge":
			if !ctx.AutoMerge {
				return nil, errors.New("project auto-merge not granted")
			}
			method := ctx.MergeMethod
			if method != "squash" && method != "merge" && method != "rebase" {
				return nil, errors.New("project merge method unavailable")
			}
			args = []string{"pr", "merge", branch, "--repo", repo, "--auto", "--" + method}
		}
		b, err := boundedOutput(forgeCommand(ctx, "gh", args...), probeWait)
		if err != nil {
			return nil, errors.New("project-delivery-unconfirmed; inspect status before retry")
		}
		if a.Action == "status" {
			v, err := dec(string(b))
			if err != nil {
				return nil, errors.New("invalid forge response")
			}
			return mcpText(v, &no), nil
		}
		return mcpText(M{"action": a.Action, "requested": true, "note": "Read status to establish the resulting state; required checks and independent review still apply."}, &no), nil
	}
	rewrites, err := boundedOutput(forgeCommand(ctx, "git", "config", "--get-regexp", `^(url\.|http\.|https\.|core\.gitproxy$)`), probeWait)
	var absent *exec.ExitError
	if err == nil || len(rewrites) > 0 || !errors.As(err, &absent) || absent.ExitCode() != 1 {
		return nil, errors.New("repository transport overrides absent check failed")
	}
	// Only the frozen repository URL and branch reach Git. No hook or ambient credential helper runs.
	c := forgeCommand(ctx, "git", "-c", "core.hooksPath=/dev/null", "-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential", "push", "https://github.com/"+repo+".git", "HEAD:refs/heads/"+branch)
	if _, err := boundedOutput(c, probeWait); err != nil {
		return nil, errors.New("project-push-failed; inspect remote branch before retry")
	}
	c = forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "repos/"+repo+"/pulls?state=open&head="+url.QueryEscape(strings.Split(repo, "/")[0]+":"+branch))
	b, err := boundedOutput(c, probeWait)
	if err != nil {
		return nil, errors.New("project-pr-read-failed; inspect before retry")
	}
	listed, err := dec(string(b))
	if err != nil {
		return nil, errors.New("invalid forge response")
	}
	for _, pr := range typed[[]jsonValue](listed) {
		return forgePRReply(pr, true), nil
	}
	c = forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "--method", "POST", "repos/"+repo+"/pulls", "-f", "title="+*a.Title, "-f", "body="+*a.Body, "-f", "head="+branch, "-f", "base="+ctx.Base, "-F", "draft=true")
	b, err = boundedOutput(c, probeWait)
	if err != nil {
		return nil, errors.New("project-pr-create-unconfirmed; inspect existing pull request before retry")
	}
	created, err := dec(string(b))
	if err != nil {
		return nil, errors.New("invalid forge response")
	}
	return forgePRReply(created, false), nil
}

func forgeGranted(repo string) bool {
	for _, allowed := range strings.Split(os.Getenv("FLEET_FORGE_REPOSITORIES"), ",") {
		if allowed == repo && repo != "" {
			return true
		}
	}
	return false
}

// forgeReadiness: remote is the project's first repository's (primaryRemote).
func forgeReadiness(remote string) string {
	repo := forgeRepo(remote)
	if !forgeGranted(repo) {
		return "forge-repository-not-granted"
	}
	store := os.Getenv("FLEET_FORGE_CONFIG_DIR")
	if !filepath.IsAbs(store) || !isFile(filepath.Join(store, "hosts.yml")) {
		return "forge-credential-store-missing"
	}
	ctx := forgeContext{Store: store, Home: os.TempDir(), CredentialHome: os.Getenv("HOME"), Workdir: os.TempDir()}
	b, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "repos/"+repo), probeWait)
	if err != nil {
		return "forge-repository-auth-unready"
	}
	v := typedRaw[struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
		FullName string `json:"full_name"`
	}](b)
	if !v.Permissions.Push || v.FullName != repo {
		return "forge-repository-push-unready"
	}
	return ""
}
