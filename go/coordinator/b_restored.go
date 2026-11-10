// Routes and operations of the TypeScript coordinator (app.ts) that the Go rewrite had not carried over:
// execution_differences with GET /api/executions/{id}/differences, GET /api/reconciliation and GET /test-sites.
package main

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const differenceTextLimit = 131072

// executionDifferences compares the stored files of two executions of one session (app.ts execution_differences).
func executionDifferences(q *Request, id, previousID string) (any, error) {
	if len(previousID) > 300 {
		previousID = previousID[:300]
	}
	current, err := get(q, id, "execution")
	if err != nil {
		return nil, err
	}
	previous, err := get(q, previousID, "execution")
	if err != nil {
		return nil, err
	}
	if str(current, "sessionId") != str(previous, "sessionId") {
		return nil, fail(403, "Different session")
	}
	before, err := hookArtifactsOf(q, str(previous, "id"))
	if err != nil {
		return nil, err
	}
	after, err := hookArtifactsOf(q, str(current, "id"))
	if err != nil {
		return nil, err
	}
	byPath := func(xs []doc) map[string]doc {
		m := map[string]doc{}
		for _, x := range xs {
			m[str(x, "path")] = x
		}
		return m
	}
	left, right := byPath(before), byPath(after)
	var paths []string
	for p := range left {
		paths = append(paths, p)
	}
	for p := range right {
		if _, ok := left[p]; !ok {
			paths = append(paths, p)
		}
	}
	sort.Strings(paths)
	lines := func(x doc) ([]string, error) {
		if x == nil {
			return []string{}, nil
		}
		b, err := artifactBytes(q.ctx(), q.app, str(x, "artifactId"), differenceTextLimit)
		if err != nil {
			return nil, err
		}
		return strings.Split(string(b), "\n"), nil
	}
	small := func(x doc) bool { n, _ := cNum(x["size"]); return x == nil || n <= differenceTextLimit }
	ref := func(x doc) any {
		if x == nil {
			return nil
		}
		return doc{"id": x["id"], "sha256": x["sha256"]}
	}
	textual := regexp.MustCompile(`(?i)\.(html|css)$`)
	files := []doc{}
	for _, p := range paths {
		l, r := left[p], right[p]
		var change any
		if textual.MatchString(p) && small(l) && small(r) {
			b, err := lines(l)
			if err != nil {
				return nil, err
			}
			a, err := lines(r)
			if err != nil {
				return nil, err
			}
			start, end := 0, 0
			for start < len(b) && start < len(a) && b[start] == a[start] {
				start++
			}
			for end < len(b)-start && end < len(a)-start && b[len(b)-1-end] == a[len(a)-1-end] {
				end++
			}
			change = doc{"firstLine": start + 1, "removed": b[start : len(b)-end], "added": a[start : len(a)-end]}
		}
		state := "modified"
		switch {
		case l == nil:
			state = "added"
		case r == nil:
			state = "removed"
		case str(l, "sha256") == str(r, "sha256"):
			state = "unchanged"
		}
		files = append(files, doc{"path": p, "textChange": change, "state": state, "previous": ref(l), "current": ref(r)})
	}
	return doc{"sessionId": current["sessionId"], "previousExecutionId": previous["id"], "executionId": current["id"], "files": files}, nil
}

var testSiteFile = regexp.MustCompile(`^[a-zA-Z0-9_-]+\.(html|css)$`)

func init() {
	bop("execution_differences", true, func(q *Request) (any, error) {
		b := body(q)
		return executionDifferences(q, str(b, "executionId"), str(b, "previous"))
	})
	broute("GET /api/executions/{id}/differences", func(q *Request) (any, error) {
		return executionDifferences(q, q.r.PathValue("id"), q.r.URL.Query().Get("previous"))
	})
	broute("GET /api/reconciliation", func(q *Request) (any, error) {
		r, err := first(q, "SELECT data FROM document WHERE kind='reconciliation' ORDER BY seq DESC LIMIT 1")
		if err != nil {
			return nil, err
		}
		if r == nil {
			return doc{"reconciliation": nil}, nil
		}
		return doc{"reconciliation": r["data"]}, nil
	})
	// The example sites an agent builds (docs/website-example.md), served sandboxed from FLEET_TEST_SITE_ROOT.
	broute("GET /test-sites/{project}/{file}", func(q *Request) (any, error) {
		root, project, name := os.Getenv("FLEET_TEST_SITE_ROOT"), q.r.PathValue("project"), q.r.PathValue("file")
		if root == "" || (project != "hello-world" && project != "hello-world-alt") || !testSiteFile.MatchString(name) {
			return nil, fail(404, "Unknown test site")
		}
		base, err := filepath.EvalSymlinks(filepath.Join(root, project))
		if err != nil {
			return nil, fail(404, "Unknown test site")
		}
		path, err := filepath.EvalSymlinks(filepath.Join(base, "current", name))
		if err != nil {
			return nil, fail(404, "Unknown test site")
		}
		if !strings.HasPrefix(path, base+string(filepath.Separator)) {
			return nil, fail(403, "Test-site path rejected")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fail(404, "Unknown test site")
		}
		return raw(func(w http.ResponseWriter) error {
			w.Header().Set("Content-Security-Policy", "sandbox allow-same-origin; default-src 'none'; style-src 'self' 'unsafe-inline'; img-src data:; frame-ancestors 'self'")
			media := "text/html"
			if strings.HasSuffix(name, ".css") {
				media = "text/css"
			}
			w.Header().Set("Content-Type", media)
			_, err := w.Write(b)
			return err
		}), nil
	})
}
