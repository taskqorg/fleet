package main

import (
	"strings"
	"testing"
)

func TestHarnessPinsProblem(t *testing.T) {
	body := "<!-- fleet-harness-qualification:failed -->\nUpdate.\n\n<!-- fleet-harness-qualification-summary -->\n- codex 0.172.0 on linux-amd64: version: 0.171.0\n- claude 2.1.332 on darwin-arm64: result event (sonnet): timeout\n<!-- /fleet-harness-qualification-summary -->\n\nRun: x"
	open := []ghPull{
		{Body: body, URL: "https://github.com/taskqorg/fleet/pull/1", Head: ghPullHead{Ref: "feature", SHA: "a"}},
		{Body: body, URL: "https://github.com/taskqorg/fleet/pull/669", Head: ghPullHead{Ref: harnessPinsBranch, SHA: "b"}},
	}
	p := harnessPinsProblem(open)
	if p == nil || p.Cause != "harness-pins:b" || !strings.Contains(p.Text, "pull/669") ||
		!strings.Contains(p.Text, "codex 0.172.0 on linux-amd64: version: 0.171.0; claude 2.1.332") || strings.Contains(p.Text, "Run:") {
		t.Fatalf("failed proposal: %+v", p)
	}
	open[1].Body = strings.Replace(body, "qualification:failed", "qualification:passed", 1)
	if p := harnessPinsProblem(open); p != nil {
		t.Fatalf("qualified proposal asks: %+v", p)
	}
}
