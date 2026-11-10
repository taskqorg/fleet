package main

// Checkout sync: on base-updated, fetch and fast-forward this host's clean checkouts. Dirty or diverged checkouts stay
// untouched and are reported with a reason; no raw stderr leaves the host.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// syncFileProtocol lets tests fetch from a local origin.
var syncFileProtocol = false

func syncGit(dir string, access []string, args ...string) (string, bool) {
	a := []string{}
	if syncFileProtocol {
		a = append(a, "-c", "protocol.file.allow=always")
	}
	c := hardenedGit(append(append(a, "-C", dir), args...)...)
	if h, ok := os.LookupEnv("HOME"); ok {
		c.Env = append(c.Env, "HOME="+h)
	}
	gitApply(c, access)
	o, err := c.Output()
	return strings.TrimSpace(string(o)), err == nil
}

// syncAllowed: the same gate as worktree: a canonical path under a host project root, not overlapping executor dirs; the
// toplevel must be the checkout itself and the git dirs under the same root.
func syncAllowed(dir string, roots, forbidden []string) string {
	p, err := canon(dir)
	if err != nil || overlaps(p, forbidden) {
		return ""
	}
	root := ""
	for _, r := range roots {
		if under(p, r) {
			root = r
			break
		}
	}
	if root == "" {
		return ""
	}
	for _, x := range []struct {
		arg   string
		exact bool
	}{{"--show-toplevel", true}, {"--git-common-dir", false}, {"--absolute-git-dir", false}} {
		c := hardenedGit("-C", p, "rev-parse", "--path-format=absolute", x.arg)
		c.Env = append(c.Env, "GIT_CEILING_DIRECTORIES="+root)
		o, err := c.Output()
		if err != nil {
			return ""
		}
		q, err := canon(strings.TrimSpace(string(o)))
		if err != nil || overlaps(q, forbidden) || !under(q, root) || x.exact && q != p {
			return ""
		}
	}
	return p
}

func syncOne(dir, sha string, access []string) error {
	if !exists(filepath.Join(dir, ".git")) {
		return errors.New("checkout-missing")
	}
	s, ok := syncGit(dir, access, "status", "--porcelain")
	if !ok {
		return errors.New("status-failed")
	}
	if s != "" {
		return errors.New("checkout-dirty")
	}
	if _, ok := syncGit(dir, access, "fetch", "--quiet", "origin"); !ok {
		return errors.New("fetch-failed")
	}
	if _, ok := syncGit(dir, access, "merge", "--ff-only", "--quiet", sha); !ok {
		return errors.New("not-fast-forward")
	}
	return nil
}

func checkoutSync(p any, host string, roots, forbidden []string) (M, bool) {
	sha := jStr(p, "sha")
	valid := len(sha) >= 7 && isHex(sha)
	results := []any{}
	ok := true
	for _, e := range jArr(p, "project", "environments") {
		if jStr(e, "hostId") != host || !(jAt(e, "repo") == nil || jAt(p, "repo") == nil || jEq(jAt(e, "repo"), jAt(p, "repo"))) || !jIsStr(e, "checkoutPath") {
			continue
		}
		dir := jStr(e, "checkoutPath")
		err := errors.New("invalid-sha")
		if valid {
			access, aerr := gitEnv(jAt(p, "project"), gitRepo(jAt(p, "project"), jAt(e, "repo")), connectionsDir())
			if d := syncAllowed(dir, roots, forbidden); d == "" {
				err = errors.New("checkout-denied")
			} else if aerr != nil {
				err = aerr
			} else {
				err = syncOne(d, sha, access)
			}
		}
		if err == nil {
			results = append(results, M{"checkoutPath": dir, "status": "synced", "sha": sha})
		} else {
			ok = false
			results = append(results, M{"checkoutPath": dir, "status": "failed", "reason": err.Error()})
		}
	}
	return M{"hostId": host, "sha": sha, "checkouts": results}, ok
}
