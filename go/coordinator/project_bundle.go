package main

import (
	"fleet/contract"
	"path"
	"strings"
)

// projectBundle validates only owned context, never reads a caller-selected host path.
func projectBundle(id string, v doc) (doc, error) {
	if !keysOnly(v, "settings", "contextFiles") {
		return nil, fail(400, "Expected settings and contextFiles")
	}
	s := mapOf(v["settings"])
	if !idPattern.MatchString(id) || s["id"] != id || strings.TrimSpace(astr(s["name"])) == "" {
		return nil, fail(400, "Invalid project identity")
	}
	if _, _, err := projectSettings(s); err != nil {
		return nil, err
	}
	if value, present := s["contextSources"]; present {
		sources, err := contract.ContextSources(value)
		if err != nil {
			return nil, fail(400, "Invalid contextSources")
		}
		for _, source := range sources {
			found := 0
			for _, r := range anys(s["repos"]) {
				if mapOf(r)["id"] == source.Repo {
					found++
				}
			}
			if found != 1 {
				return nil, fail(400, "Unknown contextSources repo")
			}
		}
	}
	names, ok := s["contextFiles"].([]any)
	if !ok {
		return nil, fail(400, "Expected contextFiles paths")
	}
	files, ok := v["contextFiles"].([]any)
	if !ok {
		return nil, fail(400, "Expected owned context files")
	}
	owned := map[string]string{}
	for _, f := range files {
		m := mapOf(f)
		n := astr(m["path"])
		text, ok := m["text"].(string)
		if !keysOnly(m, "path", "text") || !ok || n == "" || path.IsAbs(n) || path.Clean(n) != n || n == "." || n == ".." || strings.HasPrefix(n, "../") || strings.Contains(n, "\\") {
			return nil, fail(400, "Invalid owned context path")
		}
		if _, exists := owned[n]; exists {
			return nil, fail(400, "Duplicate context path")
		}
		owned[n] = text
	}
	seen := map[string]bool{}
	for _, name := range names {
		n, ok := name.(string)
		if !ok || seen[n] {
			return nil, fail(400, "Invalid context reference")
		}
		if _, ok := owned[n]; !ok {
			return nil, fail(400, "Missing owned context")
		}
		seen[n] = true
	}
	if len(seen) != len(owned) {
		return nil, fail(400, "Unreferenced owned context")
	}
	return v, nil
}
