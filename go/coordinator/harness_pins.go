package main

import (
	"context"
	"strings"
	"sync"
)

// The managed CLI update (.github/workflows/harness-pins.yml, docs/designs/fleet-self-update.md#managed-agent-clis)
// qualifies its own pull request and merges it by itself. A failed qualification leaves the pull request open with a
// marker and a summary in its body; this turns it into one owner question per proposed head, withdrawn when the pull
// request merges, closes or qualifies. Read from the delivery pass's open pull requests of the public repository.
const (
	harnessPinsBranch  = "automation/harness-pins"
	harnessPinsFailed  = "<!-- fleet-harness-qualification:failed -->"
	harnessPinsSummary = "<!-- fleet-harness-qualification-summary -->"
	harnessPinsEnd     = "<!-- /fleet-harness-qualification-summary -->"
)

var (
	harnessPinsMu   sync.Mutex
	harnessPinsLast *string // the cause last written; nil before the first pass, so a restart reconciles once
)

// harnessPinsProblem: the current problem of the open pull requests, nil when none failed qualification.
func harnessPinsProblem(open []ghPull) *Problem {
	for _, pr := range open {
		if pr.Head.Ref != harnessPinsBranch || !strings.Contains(pr.Body, harnessPinsFailed) {
			continue
		}
		summary := pr.Body
		if i := strings.Index(summary, harnessPinsSummary); i >= 0 {
			summary = summary[i+len(harnessPinsSummary):]
		}
		if i := strings.Index(summary, harnessPinsEnd); i >= 0 {
			summary = summary[:i]
		}
		var failures []string
		for _, line := range strings.Split(summary, "\n") {
			if f := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- ")); f != "" && strings.HasPrefix(strings.TrimSpace(line), "- ") {
				failures = append(failures, f)
			}
		}
		if len(failures) > 3 {
			failures = append(failures[:3], "and more in the pull request")
		}
		text := "Managed agent CLI update failed qualification and stays unmerged (" + pr.URL + "): " + strings.Join(failures, "; ") + "."
		return &Problem{Cause: "harness-pins:" + pr.Head.SHA, Text: text, Options: []any{"ok"}}
	}
	return nil
}

// harnessPinsPass writes the question set when it changed since the last pass.
func harnessPinsPass(ctx context.Context, a *App, open []ghPull) error {
	p := harnessPinsProblem(open)
	cause := ""
	current := []Problem{}
	if p != nil {
		cause, current = p.Cause, []Problem{*p}
	}
	harnessPinsMu.Lock()
	defer harnessPinsMu.Unlock()
	if harnessPinsLast != nil && *harnessPinsLast == cause {
		return nil
	}
	if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return problemsSet(q, "harness-pins", "^harness-pins:", current)
	}); err != nil {
		return err
	}
	harnessPinsLast = &cause
	return nil
}
