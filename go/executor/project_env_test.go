package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestProjectEnvPod(t *testing.T) {
	a, b := projectEnvPod("s1"), projectEnvPod("s2")
	if a == b || !strings.HasPrefix(a, "fleet-env-") || len(a) != len("fleet-env-")+16 {
		t.Fatalf("pods %q %q", a, b)
	}
}

func TestEnvArgsSorted(t *testing.T) {
	got := strings.Join(envArgs(map[string]string{"B": "2", "A": "1"}), " ")
	if got != "-e A=1 -e B=2" {
		t.Fatalf("got %q", got)
	}
}

func TestProjectEnvCallNeedsCommand(t *testing.T) {
	if _, err := projectEnvCall("env_exec", M{}, projectEnvConfig{}); err == nil {
		t.Fatal("empty command accepted")
	}
	if _, err := projectEnvCall("other", M{}, projectEnvConfig{}); err == nil {
		t.Fatal("unknown tool accepted")
	}
}

func TestProjectEnvToolTimeoutWired(t *testing.T) {
	cmd := exec.Command("codex")
	harness{name: "codex"}.WireMCP(cmd, map[string]any{}, map[string]any{"project-env": map[string]any{"command": "x", "args": []any{}, "tools": []any{"env_exec"}, "toolTimeoutSec": 5400}}, nil)
	if !strings.Contains(strings.Join(cmd.Args, " "), "mcp_servers.project-env.tool_timeout_sec=5400") {
		t.Fatalf("args %v", cmd.Args)
	}
}
