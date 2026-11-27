package contract

import (
	"net"
	"net/url"
	"slices"
	"strings"
)

// RepoRef identifies a parsed Git repository binding across GitHub and GitLab (including self-hosted instances).
type RepoRef struct {
	Provider string // "github" or "gitlab"
	Host     string // lowercase host (e.g. "github.com", "gitlab.com", "gitlab.ufobe.com")
	Slug     string // repository path without leading/trailing slash or .git (e.g. "taskqorg/fleet", "gradus/cyprusbz")
}

// PublishingHostCapability returns the capability string for a publishing grant on a host.
func PublishingHostCapability(provider, host, slug string) string {
	return "publishing:" + provider + ":" + strings.ToLower(host) + ":" + slug
}

func isHostChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '.' || b == ':'
}

func isSlugChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '-' || b == '_' || b == '.'
}

func allBytes(s string, ok func(byte) bool) bool {
	for i := 0; i < len(s); i++ {
		if !ok(s[i]) {
			return false
		}
	}
	return true
}

func validForgeHost(h string) (string, bool) {
	h = strings.ToLower(strings.TrimSpace(h))
	if h == "" || strings.Contains(h, "..") || strings.ContainsAny(h, "/@?#\\ ") || !allBytes(h, isHostChar) {
		return "", false
	}
	host := h
	if strings.Contains(h, ":") {
		parsedHost, port, err := net.SplitHostPort(h)
		if err != nil || parsedHost == "" || port == "" {
			return "", false
		}
		host = parsedHost
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") || strings.HasPrefix(host, "-") {
		return "", false
	}
	return h, true
}

func validRepoSlug(slug, provider string) bool {
	if slug == "" || strings.HasPrefix(slug, "/") || strings.HasSuffix(slug, "/") {
		return false
	}
	parts := strings.Split(slug, "/")
	if provider == "github" && len(parts) != 2 {
		return false
	}
	if len(parts) < 2 {
		return false
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || !allBytes(p, isSlugChar) {
			return false
		}
	}
	return true
}

// ParseRepoRemote resolves provider ("github"|"gitlab"), host, and repository slug from a project repo remote and hints.
func ParseRepoRemote(remote, forgeHint, hostHint string) (RepoRef, bool) {
	remote = strings.TrimSpace(remote)
	forgeHint = strings.ToLower(strings.TrimSpace(forgeHint))
	if forgeHint != "" && forgeHint != "github" && forgeHint != "gitlab" {
		return RepoRef{}, false
	}
	var cleanHostHint string
	if strings.TrimSpace(hostHint) != "" {
		var ok bool
		cleanHostHint, ok = validForgeHost(hostHint)
		if !ok {
			return RepoRef{}, false
		}
	}
	if remote == "" {
		return RepoRef{}, false
	}
	var rawHost, rawPath string
	switch {
	case strings.HasPrefix(remote, "https://"), strings.HasPrefix(remote, "ssh://"):
		u, err := url.Parse(remote)
		if err != nil || u.RawQuery != "" || u.Fragment != "" {
			return RepoRef{}, false
		}
		if u.Scheme == "https" && u.User != nil {
			return RepoRef{}, false
		}
		if u.Scheme == "ssh" && u.User != nil && u.User.Username() != "git" {
			return RepoRef{}, false
		}
		rawHost = u.Host
		rawPath = u.Path
	case strings.HasPrefix(remote, "git@"):
		rest := strings.TrimPrefix(remote, "git@")
		hostPart, pathPart, found := strings.Cut(rest, ":")
		if !found || strings.ContainsAny(rest, "?#\\ ") {
			return RepoRef{}, false
		}
		rawHost = hostPart
		rawPath = pathPart
	default:
		return RepoRef{}, false
	}
	host, ok := validForgeHost(rawHost)
	if !ok {
		return RepoRef{}, false
	}
	if cleanHostHint != "" {
		if host != cleanHostHint {
			return RepoRef{}, false
		}
	}
	provider := forgeHint
	if provider == "" {
		switch {
		case host == "github.com":
			provider = "github"
		case host == "gitlab.com" || strings.HasPrefix(host, "gitlab."):
			provider = "gitlab"
		default:
			return RepoRef{}, false
		}
	}
	slug := strings.TrimSuffix(strings.Trim(rawPath, "/"), ".git")
	if !validRepoSlug(slug, provider) {
		return RepoRef{}, false
	}
	return RepoRef{Provider: provider, Host: host, Slug: slug}, true
}

// ParseForgeRepoEntry parses one comma-separated entry from FLEET_FORGE_REPOSITORIES.
// Entries may be "owner/repo" (GitHub) or "<host>/<slug>" (GitHub or GitLab, including nested subgroups).
func ParseForgeRepoEntry(entry string) (host, slug string, ok bool) {
	entry = strings.TrimSpace(entry)
	entry = strings.TrimSuffix(strings.Trim(entry, "/"), ".git")
	if entry == "" || strings.Contains(entry, "..") || strings.ContainsAny(entry, " \t\r\n?#@:\\") {
		return "", "", false
	}
	parts := strings.Split(entry, "/")
	if len(parts) == 2 && !strings.Contains(parts[0], ".") {
		if !validRepoSlug(entry, "github") {
			return "", "", false
		}
		return "", entry, true
	}
	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		h, hok := validForgeHost(parts[0])
		s := strings.Join(parts[1:], "/")
		// "<host>/<group>/*" grants every project under the group, today's and future ones.
		if g, wild := strings.CutSuffix(s, "/*"); wild && hok && !strings.Contains(g, "*") && validRepoSlug(g+"/x", "gitlab") {
			return h, s, true
		}
		if !hok || !validRepoSlug(s, "gitlab") {
			return "", "", false
		}
		return h, s, true
	}
	return "", "", false
}

// SlugGroupGrants: the group grants ("<group>/*", for each enclosing group and subgroup) that cover a GitLab slug.
func SlugGroupGrants(slug string) []string {
	parts := strings.Split(slug, "/")
	out := make([]string, 0, len(parts))
	for i := 1; i < len(parts); i++ {
		out = append(out, strings.Join(parts[:i], "/")+"/*")
	}
	return out
}

// ForgeRepoGranted reports whether repositoriesEnv grants publishing to ref.
func ForgeRepoGranted(repositoriesEnv string, ref RepoRef) bool {
	if ref.Host == "" || ref.Slug == "" {
		return false
	}
	for _, raw := range strings.Split(repositoriesEnv, ",") {
		h, s, ok := ParseForgeRepoEntry(raw)
		if !ok {
			continue
		}
		if h != "" && strings.EqualFold(h, ref.Host) && (s == ref.Slug || slices.Contains(SlugGroupGrants(ref.Slug), s)) {
			return true
		}
		if h == "" && s == ref.Slug {
			return true
		}
	}
	return false
}

// ValidCIRerunPolicy checks the shape of the owner grant before either side can use it.
func ValidCIRerunPolicy(p *PublishingCIRerunPolicy) bool {
	if p == nil || p.RepoId == "" || len(p.RequiredJobs) == 0 || len(p.Profiles) == 0 ||
		!ValidPublishingSHA(p.WorkflowBlobSha) {
		return false
	}
	file, ok := strings.CutPrefix(p.Workflow, ".github/workflows/")
	if !ok || file == "" || strings.Contains(file, "/") || !allBytes(file, isSlugChar) ||
		(!strings.HasSuffix(file, ".yml") && !strings.HasSuffix(file, ".yaml")) {
		return false
	}
	for _, values := range [][]string{p.Profiles, p.RequiredJobs} {
		for _, v := range values {
			if strings.TrimSpace(v) == "" {
				return false
			}
		}
	}
	return true
}

func ValidPublishingSHA(s string) bool {
	return len(s) == 40 && allBytes(s, func(b byte) bool {
		return b >= '0' && b <= '9' || b >= 'a' && b <= 'f'
	})
}

// CIRerunProvider marks the provider for which the PR and attempt fences are implemented.
func CIRerunProvider(ref RepoRef) bool {
	return ref.Provider == "github" && ref.Host == "github.com"
}
