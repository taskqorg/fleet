package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"fleet/contract"
	"fleet/harnessrelease"
)

// Host qualification of an agent CLI proposal (docs/designs/fleet-host-updates.md, step 4). When the proposal's head
// passed offline qualification, the coordinator runs the Lab experiment pins:cli:<sha>: for every host with a ready
// account of a changed CLI, one trial per configured model on the candidate CLI (pipeline pins-qualify, example
// pins-qualify-cli: one shell call, one Fleet MCP call, a structured result file the example's check reads). The
// trials are the compatibility facts. When every trial ends, pinsPublish posts qualify-hosts and, on a pass,
// code-review and auto-merge; a failure becomes the proposal's owner question.
const (
	pinsQualifyPipeline = "pins-qualify"
	pinsQualifyExample  = "pins-qualify-cli"
)

// pinsCandidate: the proposal's pins of one target, read from its head.
type pinsCandidate map[string]map[string]harnessrelease.Asset

// pinsTargetOf: the pin target a host platform runs (WSL runs linux).
func pinsTargetOf(platform string) string {
	switch platform {
	case "linux", "win32":
		return "linux-amd64"
	case "darwin":
		return "darwin-arm64"
	}
	return ""
}

// pinsVariant: one trial of the experiment, before it is a Lab variant.
type pinsVariant struct {
	Host, Account, Harness, Model string
	Asset                         harnessrelease.Asset
}

func (v pinsVariant) label() string {
	return strings.Join([]string{v.Host, v.Harness, v.Model}, " ")
}

// pinsHostVariants: the trials for a proposal: every changed CLI, on every host where a ready account binds it, once per
// configured model. current is this release's pins per target; models the configured models per harness.
func pinsHostVariants(accounts []accountRec, current, candidate pinsCandidate, models map[string][]string) []pinsVariant {
	out := []pinsVariant{}
	seen := map[string]bool{}
	for _, a := range accounts {
		for _, b := range a.bindings() {
			if b.HostID == nil || deref(b.Readiness) != "ready" {
				continue
			}
			target := pinsTargetOf(deref(b.Platform))
			next, ok := candidate[target][a.Harness]
			if target == "" || !ok || current[target][a.Harness].Digest() == next.Digest() {
				continue
			}
			for _, m := range models[a.Harness] {
				v := pinsVariant{Host: *b.HostID, Account: a.ID, Harness: a.Harness, Model: m, Asset: next}
				if !seen[v.label()] {
					seen[v.label()] = true
					out = append(out, v)
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].label() < out[j].label() })
	return out
}

// pinsLiveVariants keeps the trials on hosts seen in the last hour, and is not ready while one of them runs an executor
// older than this coordinator's executor: an older executor ignores the candidate and would run its release's CLI, so
// the trial would qualify the current pin, not the proposed one. coordinator is the release the executor last changed
// in (release pacing: hosts skip releases that leave it unchanged), else the coordinator's own version.
func pinsLiveVariants(variants []pinsVariant, hosts map[string]hostVersion, coordinator string) ([]pinsVariant, bool) {
	out := []pinsVariant{}
	for _, v := range variants {
		h, live := hosts[v.Host]
		if !live {
			continue
		}
		if olderVersion(h.version, coordinator) {
			return nil, false
		}
		out = append(out, v)
	}
	return out, true
}

// pinsConfiguredModels: the model ids of config/harness-models.json per harness.
func pinsConfiguredModels() (map[string][]string, error) {
	b, err := os.ReadFile(filepath.Join(configDir(), "harness-models.json"))
	if err != nil {
		return nil, err
	}
	var all map[string][]contract.HarnessModel
	if err := json.Unmarshal(b, &all); err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for h, ms := range all {
		for _, m := range ms {
			out[h] = append(out[h], m.Id)
		}
	}
	return out, nil
}

// pinsQualifyState: what host qualification of one head says now.
type pinsQualifyState struct {
	Done     bool
	Passed   bool
	Failures []string
	Trials   int
}

// pinsTrialOutcome: the verdict of the experiment's trials: done when every variant has a terminal trial; passed when each
// succeeded and its script check passed.
func pinsTrialOutcome(variants []string, runs []*labRun, scores []labScore) pinsQualifyState {
	passed := map[string]bool{}
	for _, s := range scores {
		if s.Form == "pass_fail" && rawValue(s.Value) == true {
			for _, id := range s.TrialIDs {
				passed[id] = true
			}
		}
	}
	last := map[string]*labRun{}
	for _, r := range runs {
		if r.Variant != nil {
			last[*r.Variant] = r
		}
	}
	st := pinsQualifyState{Done: true, Passed: true, Trials: len(variants)}
	for _, v := range variants {
		r := last[v]
		switch {
		case r == nil || !executionEnds(r.State):
			st.Done = false
		case r.State != "succeeded":
			st.Passed = false
			st.Failures = append(st.Failures, fmt.Sprintf("%s: run %s", v, r.State))
		case !passed[r.ID]:
			st.Passed = false
			st.Failures = append(st.Failures, v+": shell, Fleet tool or result check failed")
		}
	}
	if !st.Done {
		st.Passed = false
	}
	return st
}

// pinsQualifyHead runs step 4 for one proposal head: starts the experiment when there is none, reads it otherwise.
// Answers nil until the experiment is done. Without a live host that can run a trial, it waits.
func pinsQualifyHead(ctx context.Context, a *App, pr ghPull, candidate pinsCandidate) (*pinsQualifyState, error) {
	group := "pins:cli:" + pr.Head.SHA
	var state *pinsQualifyState
	found := false
	asOwner := func(q *Request) error {
		owner := q.peopleSnapshot().person(ownerEmail(q))
		if owner == nil {
			return fmt.Errorf("pins qualification: no owner")
		}
		q.person = owner // the owner approved the procedure (2026-11-26); the experiment is requested in their name
		return nil
	}
	// Read the experiment of this head; a GLOBAL write only when it must be created.
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		if err := asOwner(q); err != nil {
			return err
		}
		st, ok, err := pinsExperimentState(q, group)
		state, found = st, ok
		return err
	}); err != nil || found {
		return state, err
	}
	err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		if err := asOwner(q); err != nil {
			return err
		}
		if st, ok, err := pinsExperimentState(q, group); err != nil || ok {
			state = st
			return err
		}
		accounts, err := accountRecs(q)
		if err != nil {
			return err
		}
		models, err := pinsConfiguredModels()
		if err != nil {
			return err
		}
		current := pinsCandidate{}
		for _, t := range []string{"linux-amd64", "darwin-arm64"} {
			current[t], _ = harnessrelease.Read(releaseDir(), t)
		}
		seen, err := deliveryHostsSeen(q)
		if err != nil {
			return err
		}
		variants, ready := pinsLiveVariants(pinsHostVariants(accounts, current, candidate, models), deliveryHostVersions(seen, time.Now()), executorRelease())
		if !ready || len(variants) == 0 {
			return nil // wait: a host still runs an executor without candidate runs, or no host can run a trial now
		}
		example, err := pinsQualifyExampleID(q)
		if err != nil {
			return err
		}
		req := pinsExperimentRequest(group, pr, example, variants)
		v, err := expCreate(q, &req, false)
		if err != nil {
			return err
		}
		return expLaunch(q, v.Id)
	})
	return state, err
}

// pinsExperimentState: the outcome of the experiment of group (nil while its trials run); ok when it exists.
func pinsExperimentState(q *Request, group string) (*pinsQualifyState, bool, error) {
	exps, err := labExperimentsList(q, nil)
	if err != nil {
		return nil, false, err
	}
	for _, e := range exps.Experiments {
		if deref(e.LabGroup) != group {
			continue
		}
		runs, err := expRuns(q, e.Id)
		if err != nil {
			return nil, true, err
		}
		scores, err := scoresCurrent(q, e.Id)
		if err != nil {
			return nil, true, err
		}
		labels := []string{}
		for _, v := range e.Variants {
			labels = append(labels, deref(v.Label))
		}
		st := pinsTrialOutcome(labels, expTrials(runs), scores)
		if st.Done || e.State == "stopped" || e.State == "failed" {
			st.Done = true
			return &st, true, nil
		}
		return nil, true, nil
	}
	return nil, false, nil
}

// pinsExperimentRequest: the Lab experiment of one proposal head.
func pinsExperimentRequest(group string, pr ghPull, example string, variants []pinsVariant) contract.LabExperimentCreateRequest {
	type overrides struct {
		Host      string `json:"host"`
		Account   string `json:"account"`
		Profile   string `json:"profile"`
		Model     string `json:"model"`
		Candidate struct {
			Name  string               `json:"name"`
			Asset harnessrelease.Asset `json:"asset"`
		} `json:"candidate"`
	}
	type variant struct {
		Label     string    `json:"label"`
		Overrides overrides `json:"overrides"`
	}
	vs := []variant{}
	for _, v := range variants {
		o := overrides{Host: v.Host, Account: v.Account, Profile: "pins-qualify-" + v.Harness, Model: v.Model}
		o.Candidate.Name, o.Candidate.Asset = v.Harness, v.Asset
		vs = append(vs, variant{Label: v.label(), Overrides: o})
	}
	kind := "ab"
	if len(vs) == 1 {
		kind = "single"
	}
	goal := "Qualify the agent CLI proposal " + pr.URL + " on the hosts (head " + pr.Head.SHA[:12] + ")"
	hypothesis := "Every changed CLI runs every configured model on every host with a ready account: one shell call, one Fleet MCP call and the result file."
	project := ciProject
	return contract.LabExperimentCreateRequest{ProjectId: &project, PipelineId: ptr(pinsQualifyPipeline), Kind: &kind,
		Variants: jsonBytes(vs), Examples: jsonBytes([]string{example}),
		Evaluators: json.RawMessage(`[{"kind":"script","ref":"example.checks","form":"pass_fail"}]`),
		Goal:       &goal, Hypothesis: &hypothesis, LabGroup: jsonBytes(group)}
}

// pinsQualifyExampleID: the approved example artifact, imported from config/lab/examples once.
func pinsQualifyExampleID(q *Request) (string, error) {
	approved := contract.LabExampleState("approved")
	list, err := labExamplesList(q, &approved)
	if err != nil {
		return "", err
	}
	for _, e := range list.Examples {
		if strings.HasPrefix(e.Name, "examples/"+pinsQualifyExample) {
			return e.ArtifactId, nil
		}
	}
	b, err := os.ReadFile(filepath.Join(configDir(), "lab", "examples", pinsQualifyExample+".json"))
	if err != nil {
		return "", err
	}
	yes, project := true, ciProject
	r, err := labExamplesAdd(q, &contract.LabExamplesAddRequest{ProjectId: &project, Examples: jsonBytes([]json.RawMessage{b}), Approved: &yes})
	if err != nil {
		return "", err
	}
	for _, e := range r.Examples {
		var id string
		if json.Unmarshal(e["artifactId"], &id) == nil && id != "" {
			return id, nil
		}
	}
	return "", fmt.Errorf("pins qualification: example %s not imported", pinsQualifyExample)
}

// pinsHostPass: step 4 for the open CLI proposals whose head passed offline qualification and has no qualify-hosts yet.
// Answers the host failures per proposal head, for the owner question.
func pinsHostPass(ctx context.Context, a *App, open []ghPull) map[string][]string {
	failed := map[string][]string{}
	for _, pr := range open {
		if pinsKind(pr.Head.Ref) != "cli" || pinsWritable(pr) != nil {
			continue
		}
		statuses, err := pinsStatuses(ctx, a, pr.Head.SHA)
		if err != nil || statuses["qualify-offline"] != "success" {
			continue
		}
		if s := statuses["qualify-hosts"]; s == "success" {
			continue
		} else if s == "failure" {
			failed[pr.Head.SHA] = []string{"host qualification failed (see qualify-hosts)"}
			continue
		}
		candidate, err := pinsCandidateAt(ctx, a, pr.Head.SHA)
		if err != nil {
			log.Printf("pins qualification %s: candidate: %v", pr.URL, err)
			continue
		}
		st, err := pinsQualifyHead(ctx, a, pr, candidate)
		if err != nil {
			log.Printf("pins qualification %s: %v", pr.URL, err)
		}
		if err != nil || st == nil {
			continue
		}
		desc := fmt.Sprintf("%d trials passed on the hosts", st.Trials)
		if !st.Passed {
			desc = strings.Join(st.Failures, "; ")
			failed[pr.Head.SHA] = st.Failures
		}
		if err := pinsPublish(ctx, a, pr, st.Passed, desc, "https://fleet.ufobe.com/lab"); err != nil {
			failed[pr.Head.SHA] = append(failed[pr.Head.SHA], "could not record the result on the pull request: "+err.Error())
		}
	}
	return failed
}

// pinsStatuses: the latest state per context of a commit (keyless or keyed read of the public repository).
func pinsStatuses(ctx context.Context, a *App, sha string) (map[string]string, error) {
	b, _, err := githubRead(ctx, a, ciProject, "commit_status", map[string]string{"owner": ciOwner, "repo": ciRepo, "ref": sha})
	if err != nil {
		return nil, err
	}
	var r struct {
		Statuses []struct {
			Context string `json:"context"`
			State   string `json:"state"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, s := range r.Statuses { // newest first
		if _, ok := out[s.Context]; !ok {
			out[s.Context] = s.State
		}
	}
	return out, nil
}

// pinsCandidateAt: the CLI pins at a commit of the public repository, every target validated as a candidate.
func pinsCandidateAt(ctx context.Context, a *App, sha string) (pinsCandidate, error) {
	b, _, err := githubRead(ctx, a, ciProject, "file_at", map[string]string{"owner": ciOwner, "repo": ciRepo, "path": "config/" + harnessrelease.CLIs.File, "ref": sha})
	if err != nil {
		return nil, err
	}
	return pinsCandidateOf(b, sha)
}

// pinsCandidateOf: the CLI pins in a file_at reply, every target validated as a candidate.
func pinsCandidateOf(b []byte, sha string) (pinsCandidate, error) {
	var err error
	// The keyed read answers the raw file; the keyless one the contents object with the file in base64.
	var wrapped struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if json.Unmarshal(b, &wrapped) == nil && wrapped.Encoding == "base64" {
		if b, err = base64.StdEncoding.DecodeString(strings.ReplaceAll(wrapped.Content, "\n", "")); err != nil {
			return nil, err
		}
	}
	var m struct {
		Targets pinsCandidate `json:"targets"`
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if len(m.Targets) == 0 {
		return nil, fmt.Errorf("no CLI pins at %s", sha)
	}
	out := pinsCandidate{}
	for target, assets := range m.Targets {
		out[target] = map[string]harnessrelease.Asset{}
		for name, asset := range assets {
			if !slices.Contains(harnessrelease.CLIs.Names, name) {
				return nil, fmt.Errorf("unknown CLI %s", name)
			}
			c, err := harnessrelease.CLIs.Candidate(name, asset)
			if err != nil {
				return nil, err
			}
			out[target][name] = c
		}
	}
	return out, nil
}
