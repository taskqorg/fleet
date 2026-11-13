package main

import (
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

// forgeCommand reads only the explicitly connected host credential store; native HOME stays isolated.
func forgeCommand(ctx any, name string, args ...string) *exec.Cmd {
	c := exec.Command(name, args...)
	c.Dir = jStr(ctx, "workdir")
	c.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + jStr(ctx, "home"), "GH_CONFIG_DIR=" + jStr(ctx, "store"), "GH_PROMPT_DISABLED=1", "GIT_TERMINAL_PROMPT=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	credential := exec.Command("gh", "auth", "token", "--hostname", "github.com")
	credential.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + jStr(ctx, "credentialHome"), "GH_CONFIG_DIR=" + jStr(ctx, "store"), "GH_PROMPT_DISABLED=1"}
	if token, err := boundedOutput(credential, probeWait); err == nil {
		c.Env = append(c.Env, "GH_TOKEN="+strings.TrimSpace(string(token)))
	}
	return c
}

func forge(req any, ctx any) (any, error) {
	switch jStr(req, "method") {
	case "initialize":
		return M{"protocolVersion": "2024-11-05", "capabilities": M{"tools": M{}}, "serverInfo": M{"name": "fleet-project-forge", "version": "1"}}, nil
	case "ping":
		return M{}, nil
	case "tools/list":
		return M{"tools": []any{M{"name": "publish_draft", "description": "Push this execution branch to its granted project repository and open or reuse a draft pull request. Never merges.", "inputSchema": M{"type": "object", "additionalProperties": false, "required": []any{"title", "body"}, "properties": M{"title": M{"type": "string"}, "body": M{"type": "string"}}}}, M{"name": "delivery", "description": "Read this branch PR checks/head/status, mark it ready, or enable project-authorized auto-merge through required checks. Independent review remains required.", "inputSchema": M{"type": "object", "additionalProperties": false, "required": []any{"action"}, "properties": M{"action": M{"type": "string", "enum": []any{"status", "ready", "auto_merge"}}}}}}}, nil
	case "tools/call":
		p := jAt(req, "params")
		a := jObj(jAt(p, "arguments"))
		if (jStr(p, "name") == "publish_draft" && (len(a) != 2 || !jIsStr(a, "title") || !jIsStr(a, "body") || strings.TrimSpace(jStr(a, "title")) == "")) || (jStr(p, "name") == "delivery" && (len(a) != 1 || (jStr(a, "action") != "status" && jStr(a, "action") != "ready" && jStr(a, "action") != "auto_merge"))) || (jStr(p, "name") != "publish_draft" && jStr(p, "name") != "delivery") {
			return nil, errors.New("invalid publication request")
		}
		repo, branch := jStr(ctx, "repo"), jStr(ctx, "branch")
		if !strings.Contains(","+jStr(ctx, "repositories")+",", ","+repo+",") || repo == "" || forgeRepo("https://github.com/"+repo) != repo || jStr(ctx, "prefix") == "" || !strings.HasPrefix(branch, jStr(ctx, "prefix")+"/") || strings.ContainsAny(branch, " :~^?*[\\") {
			return nil, errors.New("publication authority unavailable")
		}
		if !filepath.IsAbs(jStr(ctx, "store")) || !isFile(filepath.Join(jStr(ctx, "store"), "hosts.yml")) {
			return nil, errors.New("forge-credential-store-missing")
		}
		if _, err := boundedOutput(forgeCommand(ctx, "git", "check-ref-format", "--branch", branch), probeWait); err != nil {
			return nil, errors.New("invalid execution branch")
		}
		current, err := boundedOutput(forgeCommand(ctx, "git", "symbolic-ref", "--short", "HEAD"), probeWait)
		if err != nil || strings.TrimSpace(string(current)) != branch {
			return nil, errors.New("execution branch changed")
		}
		if jStr(p, "name") == "delivery" {
			args := []string{"pr", "view", branch, "--repo", repo, "--json", "number,url,isDraft,state,headRefOid,statusCheckRollup,mergeStateStatus,autoMergeRequest"}
			switch jStr(a, "action") {
			case "ready":
				args = []string{"pr", "ready", branch, "--repo", repo}
			case "auto_merge":
				if jAt(ctx, "autoMerge") != true {
					return nil, errors.New("project auto-merge not granted")
				}
				method := jStr(ctx, "mergeMethod")
				if method != "squash" && method != "merge" && method != "rebase" {
					return nil, errors.New("project merge method unavailable")
				}
				args = []string{"pr", "merge", branch, "--repo", repo, "--auto", "--" + method}
			}
			b, err := boundedOutput(forgeCommand(ctx, "gh", args...), probeWait)
			if err != nil {
				return nil, errors.New("project-delivery-unconfirmed; inspect status before retry")
			}
			if jStr(a, "action") == "status" {
				v, err := dec(string(b))
				if err != nil {
					return nil, errors.New("invalid forge response")
				}
				return mcpText(v, &no), nil
			}
			return mcpText(M{"action": jStr(a, "action"), "requested": true, "note": "Read status to establish the resulting state; required checks and independent review still apply."}, &no), nil
		}
		if rewrites, err := boundedOutput(forgeCommand(ctx, "git", "config", "--get-regexp", `^(url\.|http\.|https\.|core\.gitproxy$)`), probeWait); err == nil || len(rewrites) > 0 {
			return nil, errors.New("repository transport overrides are not permitted for publication")
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
		existing, err := dec(string(b))
		if err != nil {
			return nil, errors.New("invalid forge response")
		}
		for _, pr := range jArr(existing) {
			return mcpText(M{"url": jAt(pr, "html_url"), "number": jAt(pr, "number"), "existing": true}, &no), nil
		}
		c = forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "--method", "POST", "repos/"+repo+"/pulls", "-f", "title="+jStr(a, "title"), "-f", "body="+jStr(a, "body"), "-f", "head="+branch, "-f", "base="+jStr(ctx, "base"), "-F", "draft=true")
		b, err = boundedOutput(c, probeWait)
		if err != nil {
			return nil, errors.New("project-pr-create-unconfirmed; inspect existing pull request before retry")
		}
		pr, err := dec(string(b))
		if err != nil {
			return nil, errors.New("invalid forge response")
		}
		return mcpText(M{"url": jAt(pr, "html_url"), "number": jAt(pr, "number"), "existing": false}, &no), nil
	}
	return nil, errors.New("unsupported forge request")
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
	repo := forgeRepo(jStr(gitRepo(project, nil), "remote"))
	if !forgeGranted(repo) {
		return "forge-repository-not-granted"
	}
	store := os.Getenv("FLEET_FORGE_CONFIG_DIR")
	if !filepath.IsAbs(store) || !isFile(filepath.Join(store, "hosts.yml")) {
		return "forge-credential-store-missing"
	}
	ctx := M{"store": store, "home": os.TempDir(), "credentialHome": os.Getenv("HOME"), "workdir": os.TempDir()}
	b, err := boundedOutput(forgeCommand(ctx, "gh", "api", "--hostname", "github.com", "repos/"+repo), probeWait)
	if err != nil {
		return "forge-repository-auth-unready"
	}
	v, err := dec(string(b))
	if err != nil || jAt(v, "permissions", "push") != true || jStr(v, "full_name") != repo {
		return "forge-repository-push-unready"
	}
	return ""
}
