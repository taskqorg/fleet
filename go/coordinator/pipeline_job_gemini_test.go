package main

import (
	"encoding/json"
	"testing"
)

// Requirement: a job step polling a Gemini interaction (video, Deep Research) completes on status completed with the
// whole reply as output, fails on failed/cancelled/incomplete, and keeps polling otherwise.
// Risk: a finished clip never completes the step, or a failed interaction polls forever.
func TestGeminiInteractionPoll(t *testing.T) {
	cases := []struct {
		reply, state, problem string
	}{
		{`{"object":"interaction","id":"v1_a","status":"in_progress"}`, "running", ""},
		{`{"object":"interaction","id":"v1_a","status":"requires_action"}`, "running", ""},
		{`{"object":"interaction","id":"v1_a","status":"completed","steps":[],"usage":{"total_tokens":1200}}`, "succeeded", ""},
		{`{"object":"interaction","id":"v1_a","status":"cancelled"}`, "failed", `"interaction-cancelled"`},
		{`{"object":"interaction","id":"v1_a","status":"failed","error":{"code":400,"message":"blocked"}}`, "failed", `{"code":400,"message":"blocked"}`},
	}
	for _, c := range cases {
		got := geminiInteractionPoll(json.RawMessage(c.reply))
		if got == nil || got.State != c.state || string(got.Error) != c.problem {
			t.Fatalf("%s: got %+v", c.reply, got)
		}
		if c.state == "succeeded" && (string(got.Output) != c.reply || string(got.Usage) != `{"unit":"tokens","amount":1200}`) {
			t.Fatalf("output or usage: %s %s", got.Output, got.Usage)
		}
	}
	for _, other := range []string{`{"status":"SUCCEEDED"}`, `[]`, `{"object":"interaction"}`} {
		if geminiInteractionPoll(json.RawMessage(other)) != nil {
			t.Fatalf("%s is not an interaction", other)
		}
	}
}
