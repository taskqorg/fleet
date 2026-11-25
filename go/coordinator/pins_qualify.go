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
	"fleet/coordinator/db"
	"fleet/harnessrelease"
)

// Host qualification of an agent CLI proposal (docs/designs/fleet-host-updates.md, step 4). When the proposal's head
// passed offline qualification, the coordinator runs the Lab experiment pins:cli:<sha>: for every host with a ready
// account of a changed CLI, one trial per configured model on the candidate CLI (pipeline pins-qualify, example
// pins-qualify-cli: one shell call, one Fleet MCP call, a structured result file the example's check reads). The
// trials are the compatibility facts. When every trial ends, pinsPublish posts qualify-hosts and, on a pass,
// code-review and auto-merge; a failure becomes the proposal's owner question.
const (
	pinsSkippedMark     = "; skipped: " // in the experiment's goal; the published result repeats it
	pinsQualifyPipeline = "pins-qualify"
	pinsQualifyExample  = "pins-qualify-cli"
	pinsQualifyVersion  = 2 // the example's version in config/lab/examples; a changed example raises it
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
	Target                        string // the pin target the host runs (pinsTargetOf)
	Asset                         harnessrelease.Asset
}

// coverage: what a trial proves for every host of its target.
func (v pinsVariant) coverage() string { return v.Target + " " + v.Harness + " " + v.Model }

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
				v := pinsVariant{Host: *b.HostID, Account: a.ID, Harness: a.Harness, Model: m, Target: target, Asset: next}
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

// pinsHost: a host as host qualification sees it: its running executor, whether it polled in the last hour, and
// whether its update is stuck (held, refused or failed) rather than on its way.
type pinsHost struct {
	version     string
	live, stuck bool
}

// pinsHostsOf: every host's executor version from its last observation, whatever its update state: a host in the middle
// of an update is still a host that must run the trials.
func pinsHostsOf(rows []db.DeliveryHostsSeenRow, now time.Time) map[string]pinsHost {
	out := map[string]pinsHost{}
	for _, o := range rows {
		u, _ := decodeUpdate(json.RawMessage(o.Update))
		stuck := u.State == contract.UpdateStateHeld || u.State == contract.UpdateStateRefused || u.State == contract.UpdateStateFailed
		out[o.ID] = pinsHost{version: o.Version, live: o.Version != "" && now.UnixMilli()-int64(o.LastSeenMs) <= 3600_000, stuck: stuck}
	}
	return out
}

// pinsLiveVariants keeps the trials on hosts that polled in the last hour. A host whose executor is older than since
// ignores the candidate and would run its release's CLI, so its trial would qualify the current pin: while its update is
// on its way the qualification waits for it. An offline host, or one whose update is held, refused or failed, is
// skipped by name, but only when another host of the same target runs the same CLI and model: the qualification counts
// per platform (docs/designs/fleet-host-updates.md, step 4), and no target with a ready account goes unqualified. A
// combination only a skipped host could run makes it wait.
func pinsLiveVariants(variants []pinsVariant, hosts map[string]pinsHost, since string) (out []pinsVariant, skipped []string, ready bool) {
	out = []pinsVariant{}
	named := map[string]bool{}
	var left []pinsVariant
	for _, v := range variants {
		h := hosts[v.Host]
		why := ""
		switch {
		case !h.live:
			why = v.Host + " (offline)"
		case olderVersion(h.version, since) && !h.stuck:
			return nil, nil, false
		case olderVersion(h.version, since):
			why = v.Host + " (executor " + h.version + " cannot run a candidate; its update is held or refused)"
		default:
			out = append(out, v)
			continue
		}
		left = append(left, v)
		if !named[v.Host] {
			named[v.Host] = true
			skipped = append(skipped, why)
		}
	}
	covered := map[string]bool{}
	for _, v := range out {
		covered[v.coverage()] = true
	}
	for _, v := range left {
		if !covered[v.coverage()] {
			return nil, nil, false
		}
	}
	return out, skipped, true
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
	Ok       int      // trials that succeeded and passed their check
	Unscored []string // of Failures: trials that succeeded with no pass_fail score yet (pending while the experiment runs)
	Skipped  string   // hosts the experiment left out and why (pinsLiveVariants), from its goal
}

// pinsTrialOutcome: the verdict of the experiment's trials: done when every variant has a terminal trial; passed when each
// succeeded and its script check passed.
func pinsTrialOutcome(variants []string, runs []*labRun, scores []labScore) pinsQualifyState {
	passed, scored := map[string]bool{}, map[string]bool{}
	for _, s := range scores {
		if s.Form != "pass_fail" {
			continue
		}
		for _, id := range s.TrialIDs {
			scored[id] = true
			if rawValue(s.Value) == true {
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
	st := pinsQualifyState{Done: len(variants) > 0, Passed: len(variants) > 0, Trials: len(variants)} // no trial is never a pass
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
			if !scored[r.ID] {
				st.Unscored = append(st.Unscored, st.Failures[len(st.Failures)-1])
			}
		default:
			st.Ok++
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
		variants, skipped, ready := pinsLiveVariants(pinsHostVariants(accounts, current, candidate, models), pinsHostsOf(seen, time.Now()), executorRelease())
		if !ready || len(variants) == 0 {
			return nil // wait: a host still runs an executor without candidate runs, or no host can run a trial now
		}
		example, err := pinsQualifyExampleID(q)
		if err != nil {
			return err
		}
		req := pinsExperimentRequest(group, pr, example, variants, skipped)
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
	st, ok, err := pinsExperimentProgress(q, group)
	if err != nil || !ok || !st.Done {
		return nil, ok, err
	}
	return &st, true, nil
}

// pinsExperimentProgress: the trials of the experiment of group so far, done when they all ended or the experiment
// stopped or failed; ok when it exists (Changes shows the counts while it runs).
func pinsExperimentProgress(q *Request, group string) (pinsQualifyState, bool, error) {
	exps, err := labExperimentsList(q, nil)
	if err != nil {
		return pinsQualifyState{}, false, err
	}
	for _, e := range exps.Experiments {
		if deref(e.LabGroup) != group {
			continue
		}
		runs, err := expRuns(q, e.Id)
		if err != nil {
			return pinsQualifyState{}, true, err
		}
		scores, err := scoresCurrent(q, e.Id)
		if err != nil {
			return pinsQualifyState{}, true, err
		}
		labels := []string{}
		for _, v := range e.Variants {
			labels = append(labels, deref(v.Label))
		}
		st := pinsTrialOutcome(labels, expTrials(runs), scores)
		if e.State == "stopped" || e.State == "failed" {
			st.Done = true
		}
		if _, skipped, ok := strings.Cut(deref(e.Goal), pinsSkippedMark); ok {
			st.Skipped = skipped
		}
		return st, true, nil
	}
	return pinsQualifyState{}, false, nil
}

// pinsExperimentRequest: the Lab experiment of one proposal head.
func pinsExperimentRequest(group string, pr ghPull, example string, variants []pinsVariant, skipped []string) contract.LabExperimentCreateRequest {
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
	if len(skipped) > 0 {
		goal += pinsSkippedMark + strings.Join(skipped, ", ")
	}
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
		if e.Name == "examples/"+exampleRef(pinsQualifyExample, pinsQualifyVersion)+".json" {
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
		// The review of the diff (its code-review status): the proposal changes only its kind's pin file; every pin in it
		// is from its vendor's origin with a well-formed digest (pinsCandidateAt). Findings fail it before any trial.
		files, err := pinsFiles(ctx, a, pr.Number)
		if err != nil {
			log.Printf("pins qualification %s: files: %v", pr.URL, err)
			continue
		}
		if findings := pinsReview(files, "cli"); len(findings) > 0 {
			failed[pr.Head.SHA] = findings
			if err := pinsPublish(ctx, a, pr, false, "review: "+strings.Join(findings, "; "), pr.URL+"/files"); err != nil {
				failed[pr.Head.SHA] = append(failed[pr.Head.SHA], "could not record the result on the pull request: "+err.Error())
			}
			continue
		}
		st, err := pinsQualifyHead(ctx, a, pr, candidate)
		if err != nil {
			log.Printf("pins qualification %s: %v", pr.URL, err)
		}
		if err != nil || st == nil || st.Trials == 0 {
			continue
		}
		desc := fmt.Sprintf("%d trials passed on the hosts", st.Trials)
		if !st.Passed {
			desc = strings.Join(st.Failures, "; ")
			failed[pr.Head.SHA] = st.Failures
		}
		if st.Skipped != "" {
			desc += "; skipped " + st.Skipped
		}
		if err := pinsPublish(ctx, a, pr, st.Passed, desc, "https://fleet.ufobe.com/lab"); err != nil {
			failed[pr.Head.SHA] = append(failed[pr.Head.SHA], "could not record the result on the pull request: "+err.Error())
		}
	}
	return failed
}

// pinsFile: one changed file of a pull request.
type pinsFile struct {
	Filename string `json:"filename"`
	Status   string `json:"status"`
}

// pinsFiles: the files a pull request changes (keyless or keyed read of the public repository).
func pinsFiles(ctx context.Context, a *App, number int64) ([]pinsFile, error) {
	b, _, err := githubRead(ctx, a, ciProject, "pull_files", map[string]string{"owner": ciOwner, "repo": ciRepo, "number": fmt.Sprint(number)})
	if err != nil {
		return nil, err
	}
	var files []pinsFile
	return files, json.Unmarshal(b, &files)
}

// pinsReview: the findings of a proposal's diff: it may change exactly its kind's pin file, nothing else.
func pinsReview(files []pinsFile, kind string) []string {
	k, ok := harnessrelease.KindNamed(kind)
	if !ok {
		return []string{"unknown kind " + kind}
	}
	want := "config/" + k.File
	findings := []string{}
	for _, f := range files {
		if f.Filename != want || f.Status != "modified" {
			findings = append(findings, fmt.Sprintf("%s %s: a %s proposal changes only %s", f.Status, f.Filename, kind, want))
		}
	}
	if len(files) == 0 {
		findings = append(findings, "no change to "+want)
	}
	return findings
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

// pinsFileOf: the file in a file_at reply. The keyed read answers the raw file; the keyless one the contents object
// with the file in base64.
func pinsFileOf(b []byte) ([]byte, error) {
	var wrapped struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	if json.Unmarshal(b, &wrapped) == nil && wrapped.Encoding == "base64" {
		return base64.StdEncoding.DecodeString(strings.ReplaceAll(wrapped.Content, "\n", ""))
	}
	return b, nil
}

// pinsCandidateOf: the CLI pins in a file_at reply, every target validated as a candidate.
func pinsCandidateOf(b []byte, sha string) (pinsCandidate, error) {
	b, err := pinsFileOf(b)
	if err != nil {
		return nil, err
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
