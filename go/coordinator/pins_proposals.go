package main

import (
	"context"
	"strings"
	"sync"
)

// Component pin proposals (.github/workflows/pins-kind.yml, docs/designs/fleet-host-updates.md): the pull requests
// automation/<kind>-pins. A failed qualification leaves the proposal open with a marker and a summary in its body;
// this turns it into one owner question per kind and proposed head (pins:<kind>:<sha>), withdrawn when the proposal
// merges, closes or qualifies. Read from the delivery pass's open pull requests of the public repository.
const (
	pinsBranchPrefix = "automation/"
	pinsBranchSuffix = "-pins"
	pinsFailed       = "<!-- fleet-pins-qualification:failed -->"
	pinsSummary      = "<!-- fleet-pins-qualification-summary -->"
	pinsSummaryEnd   = "<!-- /fleet-pins-qualification-summary -->"
)

var (
	pinsMu   sync.Mutex
	pinsLast *string // the cause last written; nil before the first pass, so a restart reconciles once
)

// pinsKind: the kind a proposal branch names ("cli" for automation/cli-pins), "" for any other branch.
func pinsKind(ref string) string {
	kind, ok := strings.CutPrefix(ref, pinsBranchPrefix)
	if kind, ok2 := strings.CutSuffix(kind, pinsBranchSuffix); ok && ok2 && kind != "" && !strings.Contains(kind, "/") {
		return kind
	}
	return ""
}

// pinsProblems: the current problems of the open proposals, one per failed kind.
func pinsProblems(open []ghPull) []Problem {
	out := []Problem{}
	for _, pr := range open {
		kind := pinsKind(pr.Head.Ref)
		if kind == "" || !strings.EqualFold(pr.Head.Repo.FullName, githubPublicRepo) || !strings.Contains(pr.Body, pinsFailed) {
			continue
		}
		summary := pr.Body
		if i := strings.Index(summary, pinsSummary); i >= 0 {
			summary = summary[i+len(pinsSummary):]
		}
		if i := strings.Index(summary, pinsSummaryEnd); i >= 0 {
			summary = summary[:i]
		}
		var failures []string
		for _, line := range strings.Split(summary, "\n") {
			if f, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok && strings.TrimSpace(f) != "" {
				failures = append(failures, strings.TrimSpace(f))
			}
		}
		if len(failures) > 3 {
			failures = append(failures[:3], "and more in the pull request")
		}
		text := "Managed " + kind + " pin proposal failed qualification and stays unmerged (" + pr.URL + "): " + strings.Join(failures, "; ") + "."
		out = append(out, Problem{Cause: "pins:" + kind + ":" + pr.Head.SHA, Text: text, Options: []any{"ok"}})
	}
	return out
}

// pinsPass writes the question set when it changed since the last pass.
func pinsPass(ctx context.Context, a *App, open []ghPull) error {
	current := pinsProblems(open)
	causes := []string{}
	for _, p := range current {
		causes = append(causes, p.Cause)
	}
	key := strings.Join(causes, " ")
	pinsMu.Lock()
	defer pinsMu.Unlock()
	if pinsLast != nil && *pinsLast == key {
		return nil
	}
	if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return problemsSet(q, "pins", "^pins:", current)
	}); err != nil {
		return err
	}
	pinsLast = &key
	return nil
}
