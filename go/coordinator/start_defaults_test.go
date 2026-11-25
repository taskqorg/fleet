package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Requirement: a start without profile instructions gets only rules that hold for any start.
// Risk: one-off rules (no subagents, a static-website layout) reach every project's agents.
func TestDefaultInstructionsAreGeneric(t *testing.T) {
	for _, bad := range []string{"subagent", "website", "output/index.html"} {
		if strings.Contains(defaultInstructions, bad) {
			t.Fatalf("defaultInstructions carries the one-off rule %q: %s", bad, defaultInstructions)
		}
	}
}

// Requirement: a profile without a model takes its harness's default model (contracts/vocabulary.json harness rows);
// an empty or unknown harness keeps codex's.
// Risk: a Claude profile records a GPT model on its executions and continuation key.
func TestDefaultModelPerHarness(t *testing.T) {
	for cfg, want := range map[string]string{
		`{"id":"p","harness":"claude"}`:                "sonnet",
		`{"id":"p","harness":"antigravity"}`:           "gemini-3.8-flash-low",
		`{"id":"p","harness":"codex"}`:                 "gpt-6.1-sol",
		`{"id":"p"}`:                                   "gpt-6.1-sol",
		`{"id":"p","harness":"acp"}`:                   "gpt-6.1-sol",
		`{"id":"p","harness":"claude","model":"opus"}`: "opus",
	} {
		if got := sideOf("k", json.RawMessage(cfg))[1]; got != want {
			t.Errorf("sideOf(%s) model = %q, want %q", cfg, got, want)
		}
	}
}
