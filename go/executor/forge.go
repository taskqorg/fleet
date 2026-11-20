package main

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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
	Repositories   string    `json:"repositories"`
	CredentialHome string    `json:"credentialHome"`
	Repo           string    `json:"repo"`
	Base           string    `json:"base"`
	Branch         string    `json:"branch"`
	Prefix         string    `json:"prefix"`
	AutoMerge      looseBool `json:"autoMerge"`
	MergeMethod    string    `json:"mergeMethod"`
	Workdir        string    `json:"workdir"`
	Home           string    `json:"home"`
	Store          string    `json:"store"`
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

var forgeTools = []any{M{"name": "publish_draft", "description": "Push this execution branch to its granted project repository and open or reuse a draft pull request. Never merges.", "inputSchema": M{"type": "object", "additionalProperties": false, "required": []any{"title", "body"}, "properties": M{"title": M{"type": "string"}, "body": M{"type": "string"}}}}, M{"name": "delivery", "description": "Read this branch PR checks/head/status, mark it ready, or enable project-authorized auto-merge through required checks. Independent review remains required.", "inputSchema": M{"type": "object", "additionalProperties": false, "required": []any{"action"}, "properties": M{"action": M{"type": "string", "enum": []any{"status", "ready", "auto_merge"}}}}}}

// forgeArgs are the arguments of publish_draft ({title, body}) and delivery ({action}).
type forgeArgs struct {
	Title  *string `json:"title"`
	Body   *string `json:"body"`
	Action string  `json:"action"`
}

// forgePR is what a publication answers of a GitHub pull request.
type forgePR struct {
	HTMLURL json.RawMessage `json:"html_url"`
	Number  json.RawMessage `json:"number"`
}

func forgePRReply(raw json.RawMessage, existing bool) M {
	var pr forgePR
	json.Unmarshal(raw, &pr)
	return mcpText(M{"url": decRaw(pr.HTMLURL), "number": decRaw(pr.Number), "existing": existing}, &no)
}

// forgeCall answers one tools/call of the project forge server.
func forgeCall(name string, arguments, context any) (any, error) {
	var keys map[string]json.RawMessage
	var a forgeArgs
	in := []byte(enc(arguments))
	json.Unmarshal(in, &keys)
	typed := json.Unmarshal(in, &a) == nil
	if !typed || (name == "publish_draft" && (len(keys) != 2 || a.Title == nil || a.Body == nil || strings.TrimSpace(*a.Title) == "")) || (name == "delivery" && (len(keys) != 1 || (a.Action != "status" && a.Action != "ready" && a.Action != "auto_merge"))) || (name != "publish_draft" && name != "delivery") {
		return nil, errors.New("invalid publication request")
	}
	var ctx forgeContext
	retype(context, &ctx)
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
	if _, err := dec(string(b)); err != nil {
		return nil, errors.New("invalid forge response")
	}
	var open looseList
	json.Unmarshal(b, &open)
	for _, pr := range open.Items {
		return forgePRReply(pr, true), nil
	}
	c = forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "--method", "POST", "repos/"+repo+"/pulls", "-f", "title="+*a.Title, "-f", "body="+*a.Body, "-f", "head="+branch, "-f", "base="+ctx.Base, "-F", "draft=true")
	b, err = boundedOutput(c, probeWait)
	if err != nil {
		return nil, errors.New("project-pr-create-unconfirmed; inspect existing pull request before retry")
	}
	if _, err := dec(string(b)); err != nil {
		return nil, errors.New("invalid forge response")
	}
	return forgePRReply(b, false), nil
}

func forgeGranted(repo string) bool {
	for _, allowed := range strings.Split(os.Getenv("FLEET_FORGE_REPOSITORIES"), ",") {
		if allowed == repo && repo != "" {
			return true
		}
	}
	return false
}

func forgeReadiness(project any) string {
	var origin struct {
		Remote string `json:"remote"`
	}
	retype(gitRepo(project, nil), &origin)
	repo := forgeRepo(origin.Remote)
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
	var v struct {
		Permissions struct {
			Push bool `json:"push"`
		} `json:"permissions"`
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(b, &v) != nil || !v.Permissions.Push || v.FullName != repo {
		return "forge-repository-push-unready"
	}
	return ""
}
