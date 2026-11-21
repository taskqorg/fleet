package main

import (
	"bytes"
	"encoding/json"
	"fleet/contract"
	"path"
	"strings"
)

// projectBundle validates only owned context, never reads a caller-selected host path. v is the open bundle
// ({settings, contextFiles}, any JSON value of it, a rawObject as openObject read it); projectSettings (projects.go)
// checks the settings. The bundle comes back as openObject reads it.
func projectBundle(id string, bundle any) (rawObject, error) {
	v, read := bundle.(rawObject)
	if !read {
		var err error
		if v, err = openObject(jsonBytes(bundle)); err != nil {
			return nil, fail(400, "Expected settings and contextFiles")
		}
	}
	if !keysOnly(v, "settings", "contextFiles") {
		return nil, fail(400, "Expected settings and contextFiles")
	}
	var b struct {
		Settings     json.RawMessage `json:"settings"`
		ContextFiles json.RawMessage `json:"contextFiles"`
	}
	json.Unmarshal(jsonBytes(v), &b)
	var s rawObject
	var st struct {
		ID    any `json:"id"`
		Name  any `json:"name"`
		Repos []struct {
			ID any `json:"id"`
		} `json:"repos"`
		ContextFiles json.RawMessage `json:"contextFiles"`
	}
	if _, ok := jsonObject(b.Settings); ok {
		json.Unmarshal(b.Settings, &s)
		json.Unmarshal(b.Settings, &st)
	}
	name, _ := st.Name.(string)
	if !idPattern.MatchString(id) || st.ID != id || strings.TrimSpace(name) == "" {
		return nil, fail(400, "Invalid project identity")
	}
	if _, _, err := projectSettings(s); err != nil {
		return nil, err
	}
	if value, present := s["contextSources"]; present {
		sources, err := contract.ContextSources(rawAny(value))
		if err != nil {
			return nil, fail(400, "Invalid contextSources")
		}
		for _, source := range sources {
			found := 0
			for _, r := range st.Repos {
				if r.ID == source.Repo {
					found++
				}
			}
			if found != 1 {
				return nil, fail(400, "Unknown contextSources repo")
			}
		}
	}
	names, ok := jsonArray(st.ContextFiles)
	if !ok {
		return nil, fail(400, "Expected contextFiles paths")
	}
	files, ok := jsonArray(b.ContextFiles)
	if !ok {
		return nil, fail(400, "Expected owned context files")
	}
	owned := map[string]string{}
	for _, f := range files {
		m, isObject := jsonObject(f)
		var e struct {
			Path any `json:"path"`
			Text any `json:"text"`
		}
		json.Unmarshal(f, &e)
		n, _ := e.Path.(string)
		text, ok := e.Text.(string)
		onlyKeys := true
		for k := range m {
			onlyKeys = onlyKeys && (k == "path" || k == "text")
		}
		if !isObject || !onlyKeys || !ok || n == "" || path.IsAbs(n) || path.Clean(n) != n || n == "." || n == ".." || strings.HasPrefix(n, "../") || strings.Contains(n, "\\") {
			return nil, fail(400, "Invalid owned context path")
		}
		if _, exists := owned[n]; exists {
			return nil, fail(400, "Duplicate context path")
		}
		owned[n] = text
	}
	seen := map[string]bool{}
	for _, raw := range names {
		var n string
		if t := bytes.TrimSpace(raw); len(t) == 0 || t[0] != '"' || json.Unmarshal(raw, &n) != nil || seen[n] {
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
