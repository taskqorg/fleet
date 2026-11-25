package main

import (
	"strings"
	"testing"
)

func TestPinsProblems(t *testing.T) {
	body := "<!-- fleet-pins-qualification:failed -->\nUpdate.\n\n<!-- fleet-pins-qualification-summary -->\n- cli/codex 0.172.0 on linux-amd64: version: 0.171.0\n- cli/claude 2.1.332 on darwin-arm64: result event (sonnet): timeout\n<!-- /fleet-pins-qualification-summary -->\n\nRun: x"
	open := []ghPull{
		{Body: body, URL: "https://github.com/taskqorg/fleet/pull/1", Head: ghPullHead{Ref: "feature", SHA: "a"}},
		{Body: body, URL: "https://github.com/taskqorg/fleet/pull/2", Head: ghPullHead{Ref: "automation/cli-pins/x", SHA: "c"}},
		{Body: body, URL: "https://github.com/taskqorg/fleet/pull/669", Head: ghPullHead{Ref: "automation/cli-pins", SHA: "b"}},
		{Body: strings.Replace(body, "qualification:failed", "qualification:offline-passed", 1), Head: ghPullHead{Ref: "automation/tool-pins", SHA: "d"}},
	}
	ps := pinsProblems(open)
	if len(ps) != 1 || ps[0].Cause != "pins:cli:b" || !strings.Contains(ps[0].Text, "pull/669") ||
		!strings.Contains(ps[0].Text, "cli/codex 0.172.0 on linux-amd64: version: 0.171.0; cli/claude 2.1.332") || strings.Contains(ps[0].Text, "Run:") {
		t.Fatalf("problems: %+v", ps)
	}
}
