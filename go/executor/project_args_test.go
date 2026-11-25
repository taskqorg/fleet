package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: a project tool whose schema takes an object (access_grant_ask's scope) accepts it, and every required
// argument is checked by its declared kind. Risk (2026-11-26, DAM pilot): the check took every non-integer argument
// for a string, so access_grant_ask could never be called.
func TestArgumentsFitObjects(t *testing.T) {
	scope := M{"database": "revilio_db", "tables": []any{"project", "upload"}}
	ask := M{"requestId": "r1", "server": "pds9", "kind": "mysql", "level": "read", "scope": scope, "reason": "inventory"}
	if err := argumentsFit("access_grant_ask", ask); err != nil {
		t.Fatalf("object scope refused: %v", err)
	}
	for name, change := range map[string]func(M){
		"scope as a string": func(m M) { m["scope"] = "revilio_db" },
		"scope missing":     func(m M) { delete(m, "scope") },
		"reason blank":      func(m M) { m["reason"] = " " },
		"unknown field":     func(m M) { m["projectX"] = "x" },
	} {
		m := M{}
		for k, v := range ask {
			m[k] = v
		}
		change(m)
		if err := argumentsFit("access_grant_ask", m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := argumentsFit("connections_key_request", M{"requestId": "r", "connection": "tavily"}); err != nil {
		t.Errorf("string tool refused: %v", err)
	}
	if err := argumentsFit("connections_key_request", M{"requestId": "r", "connection": M{}}); err == nil {
		t.Error("an object for a string argument was accepted")
	}
}

// Requirement: a project tool's inputSchema (mcp-tools.json, what the agent sees and argumentsFit checks) and the
// coordinator operation's request type (contracts/ops-*.schema.json, what decodes the call) agree: every tool argument
// is a request property of the same JSON kind, every request property but the injected projectId is a tool argument,
// and every required request property but projectId is required by the tool. Risk: the two drift and a documented
// call shape fails only at run time (DAM pilot, scope; pipeline_run inputs).
func TestProjectToolsMatchOperations(t *testing.T) {
	defs := map[string]map[string]any{}
	files, _ := filepath.Glob("../../contracts/ops*.schema.json")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Defs map[string]map[string]any `json:"$defs"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		for k, v := range doc.Defs {
			defs[k] = v
		}
	}
	kind := func(p map[string]any) string {
		for range 4 {
			ref, _ := p["$ref"].(string)
			if ref == "" {
				break
			}
			p = defs[ref[strings.LastIndex(ref, "/")+1:]]
		}
		k, _ := p["type"].(string)
		return k
	}
	checked := 0
	for _, op := range contract.ProjectOperationValues {
		name := string(op)
		schema, ok := toolSchemas[name]
		if !ok {
			t.Errorf("project operation %s has no tool in mcp-tools.json", name)
			continue
		}
		var pascal strings.Builder
		for _, part := range strings.Split(name, "_") {
			pascal.WriteString(strings.ToUpper(part[:1]) + part[1:])
		}
		req, ok := defs[pascal.String()+"Request"]
		if !ok {
			continue // an operation without a typed request (account_list has its own native path)
		}
		checked++
		props, _ := req["properties"].(map[string]any)
		for arg, p := range schema.Properties {
			cp, ok := props[arg].(map[string]any)
			if !ok {
				t.Errorf("%s: tool argument %s is not a request property", name, arg)
				continue
			}
			want, got := kind(cp), p.Type
			if got == "" {
				got = "string"
			}
			if want != "" && want != got {
				t.Errorf("%s: %s is %s in the tool, %s in the operation", name, arg, got, want)
			}
		}
		// The converse: every request property but the injected projectId is a tool argument, so an operation that
		// gains one (pipeline_run inputs, DAM 2026-11-26) cannot ship with an agent tool that refuses it.
		for arg := range props {
			if _, ok := schema.Properties[arg]; !ok && arg != "projectId" {
				t.Errorf("%s: the operation takes %s, the tool does not", name, arg)
			}
		}
		required, _ := req["required"].([]any)
		for _, r := range required {
			if r == "projectId" {
				continue
			}
			if !slices.Contains(schema.Required, r.(string)) {
				t.Errorf("%s: the operation requires %s, the tool does not", name, r)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no project operation was compared")
	}
}
