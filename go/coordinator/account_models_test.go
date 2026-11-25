package main

import (
	"testing"
)

func TestModelRefusal(t *testing.T) {
	probed := accountBinding{Models: []string{"opus", "claude-sonnet-5-5"}}
	for _, c := range []struct {
		b     accountBinding
		model string
		want  bool
	}{
		{accountBinding{}, "claude-opus-5-5", false},                  // not probed: never skips
		{probed, "", false},                                           // no model asked
		{probed, "claude-opus-5-5", false},                            // alias "opus"
		{probed, "claude-sonnet-5-5", false},                          // exact
		{probed, "claude-haiku-5-5", true},                            // not offered
		{accountBinding{Models: []string{}}, "claude-opus-5-5", true}, // probed, offers nothing
		{accountBinding{RejectedModels: map[string]int64{"gpt-5.5": 1}}, "gpt-5.5", true},
	} {
		if got := c.b.modelRefusal(c.model) != ""; got != c.want {
			t.Errorf("%+v %q: refused %v, want %v", c.b, c.model, got, c.want)
		}
	}
}

func TestRejectedModelText(t *testing.T) {
	for p, want := range map[string]bool{
		"turn.failed: model_not_found: The model `gpt-9` does not exist": true,
		"harness exited 1: Error: model claude-opus-9 not found":         true,
		"turn.failed: unknown model: foo":                                true,
		"turn.failed: the model is not available right now (overloaded)": false,
		"harness exited 1: rate limit reached":                           false,
		"preparation failed: no usable key":                              false,
	} {
		if got := rejectedModelText.MatchString(p); got != want {
			t.Errorf("%q: %v, want %v", p, got, want)
		}
	}
}
