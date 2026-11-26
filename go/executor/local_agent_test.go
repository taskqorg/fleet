package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: connecting Antigravity rewrites only Fleet's servers in its settings file: a server of the person's
// own stays, a Fleet server of a connection the token no longer names goes, the token's connections are added.
func TestLocalAgentAntigravitySettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_config.json")
	os.WriteFile(path, []byte(`{"mcpServers":{"brilliant":{"command":"/x/brilliant"},"old":{"command":"/r/fleet-executor","args":["mcp","connection","old"]}},"other":1}`), 0o600)
	var out bytes.Buffer
	env := map[string]string{"FLEET_GATEWAY_URL": "https://g", "FLEET_TOKEN_FILE": "/t"}
	if err := antigravityServers(path, "/r/fleet-executor", env, []string{"fleet-postgres"}, &out); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		McpServers map[string]struct {
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		} `json:"mcpServers"`
		Other int `json:"other"`
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	pg, ok := doc.McpServers["fleet-postgres"]
	if _, mine := doc.McpServers["brilliant"]; !mine || doc.Other != 1 || !ok || strings.Join(pg.Args, " ") != "mcp connection fleet-postgres" || pg.Env["FLEET_TOKEN_FILE"] != "/t" {
		t.Fatalf("settings: %s", b)
	}
	if _, stale := doc.McpServers["old"]; stale {
		t.Fatalf("a Fleet server the token no longer names stayed: %s", b)
	}
}

// Requirement: the command's check call is a read that needs no arguments.
func TestLocalAgentProbeTool(t *testing.T) {
	spec := dec2(t, `{"name":"youtrack","tools":[{"name":"issue_get","idempotent":true,"inputSchema":{"required":["issue"]}},{"name":"comment_add","idempotent":false,"inputSchema":{}},{"name":"projects","idempotent":true,"inputSchema":{"properties":{}}}]}`)
	if p := probeTool(spec); p == nil || typed[connectionTool](p).Name != "projects" {
		t.Fatalf("probe: %v", p)
	}
	if p := probeTool(dec2(t, `{"name":"tavily","tools":[{"name":"web_search","idempotent":true,"inputSchema":{"required":["query"]}}]}`)); p != nil {
		t.Fatalf("a tool with required arguments was chosen: %v", p)
	}
}

func dec2(t *testing.T, s string) any {
	t.Helper()
	v, err := dec(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
