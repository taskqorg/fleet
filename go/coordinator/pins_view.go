package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/harnessrelease"
)

// The component rows of Changes (docs/designs/fleet-host-updates.md, "What the owner sees"; system_changes components):
// per kind of release-managed component, the pinned version, the open proposal with its stage and Lab trials, and each
// host's state. The proposals and the pin watch's flagged versions are read in the delivery pass (pinsViewPass, at most
// once a minute) and kept here; system_changes joins them with this release's pins and the hosts it reads anyway.
const pinsViewEvery = time.Minute

// pinsViewKind: what the delivery pass last saw of one kind's proposal.
type pinsViewKind struct {
	proposal *contract.SystemPinsProposal
	proposed map[string]string   // component name to the proposal's version (linux-amd64, else darwin-arm64)
	failures map[string][]string // component name to its failed trials
}

var (
	pinsViewMu      sync.Mutex
	pinsViewAt      time.Time
	pinsViewWatchAt time.Time
	pinsViewKinds   = map[string]pinsViewKind{}
	pinsViewFailing = map[string]pinsRegression{}    // host + " " + CLI: the pinned version fails clearly more there
	pinsViewFiles   = map[string]map[string]string{} // kind + " " + head sha: the proposal's versions (a head never changes)
)

// pinsTargetVersions: one version per component of a pin file's targets: linux-amd64's, else darwin-arm64's.
func pinsTargetVersions(versions map[string]map[string]string) map[string]string {
	out := map[string]string{}
	for _, t := range []string{"darwin-arm64", "linux-amd64"} {
		for name, v := range versions[t] {
			out[name] = v
		}
	}
	return out
}

// pinsViewPass: from the delivery pass with the public repository's open and closed pull requests.
func pinsViewPass(ctx context.Context, a *App, open, closed []ghPull) {
	pinsViewMu.Lock()
	if time.Since(pinsViewAt) < pinsViewEvery {
		pinsViewMu.Unlock()
		return
	}
	pinsViewAt = time.Now()
	watch := time.Since(pinsViewWatchAt) >= 10*time.Minute // the watch's own period: its runs read is the heavy one
	if watch {
		pinsViewWatchAt = pinsViewAt
	}
	pinsViewMu.Unlock()
	kinds := map[string]pinsViewKind{}
	for _, k := range harnessrelease.Kinds {
		v, err := pinsViewOf(ctx, a, k, open, closed)
		if err != nil {
			// A failed read keeps what the last pass saw, so a passing GitHub error does not hide the proposal.
			log.Printf("pins view %s: %v", k.Name, err)
			pinsViewMu.Lock()
			v = pinsViewKinds[k.Name]
			pinsViewMu.Unlock()
		}
		kinds[k.Name] = v
	}
	var failing map[string]pinsRegression
	if watch {
		var err error
		if failing, err = pinsViewRegressions(ctx, a); err != nil {
			log.Printf("pins view watch: %v", err)
			failing = nil
		}
	}
	pinsViewMu.Lock()
	defer pinsViewMu.Unlock()
	pinsViewKinds = kinds
	if failing != nil {
		pinsViewFailing = failing
	}
}

// pinsViewOf: one kind's open proposal, or the one merged in the last day.
func pinsViewOf(ctx context.Context, a *App, k harnessrelease.Kind, open, closed []ghPull) (pinsViewKind, error) {
	var pr *ghPull
	landed := false
	for i := range open {
		if pinsKind(open[i].Head.Ref) == k.Name && pinsWritable(open[i]) == nil {
			pr = &open[i]
			break
		}
	}
	if pr == nil {
		for i := range closed {
			c := closed[i]
			if pinsKind(c.Head.Ref) != k.Name || pinsWritable(c) != nil || c.MergedAt == nil {
				continue
			}
			if t, err := time.Parse(time.RFC3339, *c.MergedAt); err == nil && time.Since(t) < 24*time.Hour && (pr == nil || *c.MergedAt > *pr.MergedAt) {
				pr, landed = &closed[i], true
			}
		}
	}
	if pr == nil {
		return pinsViewKind{}, nil
	}
	proposed, err := pinsProposedVersions(ctx, a, k, pr.Head.SHA)
	if err != nil {
		return pinsViewKind{}, err
	}
	// What the proposal changes is read against its base, so a landed proposal still names its versions once this
	// release pins them.
	pinned, err := pinsProposedVersions(ctx, a, k, pr.Base.SHA)
	// No pin file at the base (a kind's first proposal): compare with this release's pins. Any other failure keeps the
	// last pass's view (pinsViewPass).
	if err != nil && strings.Contains(err.Error(), "404") {
		_, current, perr := k.Pinned(releaseDir())
		if perr != nil {
			return pinsViewKind{}, perr
		}
		pinned = pinsTargetVersions(current)
	} else if err != nil {
		return pinsViewKind{}, err
	}
	changed := map[string]string{}
	for name, v := range proposed {
		if pinned[name] != v {
			changed[name] = v
		}
	}
	p := &contract.SystemPinsProposal{Number: pr.Number, Url: pr.URL, At: pr.CreatedAt, Failures: []string{}}
	out := pinsViewKind{proposal: p, proposed: changed, failures: map[string][]string{}}
	if landed {
		p.Stage, p.At = contract.PinsStageLanded, *pr.MergedAt
		return out, nil
	}
	statuses, err := pinsStatuses(ctx, a, pr.Head.SHA)
	if err != nil {
		return pinsViewKind{}, err
	}
	if strings.Contains(pr.Body, pinsFailed) {
		p.Failures = append(p.Failures, pinsBodyFailures(pr.Body)...)
	}
	if k.Name == harnessrelease.CLIs.Name {
		if err := a.store.Read(ctx, nil, func(q *Request) error {
			owner := q.peopleSnapshot().person(ownerEmail(q))
			if owner == nil {
				return nil
			}
			q.person = owner // the experiment is the owner's (pinsQualifyHead)
			st, ok, err := pinsExperimentProgress(q, "pins:"+k.Name+":"+pr.Head.SHA)
			if err != nil || !ok {
				return err
			}
			failures := st.Failures
			if !st.Done { // a succeeded trial whose check is not scored yet is pending, not failed
				failures = slices.DeleteFunc(slices.Clone(failures), func(f string) bool { return slices.Contains(st.Unscored, f) })
			}
			p.Trials = &contract.SystemPinsTrials{Passed: int64(st.Ok), Failed: int64(len(failures)), Total: int64(st.Trials)}
			p.Failures = append(p.Failures, failures...)
			return nil
		}); err != nil {
			return pinsViewKind{}, err
		}
	}
	for _, f := range p.Failures {
		for name := range changed {
			if pinsFailureNames(f, name) {
				out.failures[name] = append(out.failures[name], f)
			}
		}
	}
	bad := func(s string) bool { return s == "failure" || s == "error" }
	reverting := false
	for name, v := range changed {
		if pinned[name] != "" && componentOlder(v, pinned[name]) {
			reverting = true
		}
	}
	switch {
	case strings.Contains(pr.Body, pinsFailed) || bad(statuses["qualify-offline"]) || bad(statuses["qualify-hosts"]):
		p.Stage = contract.PinsStageFailed
	case reverting:
		p.Stage = contract.PinsStageReverting
	case pr.AutoMerge != nil || statuses["qualify-hosts"] == "success":
		p.Stage = contract.PinsStageMerging
		// Qualified, yet GitHub may hold it: a ruleset's required approval is not "merging".
		if why := pinsBlocked(ctx, a, pr.Number, statuses); why != "" {
			p.Stage, p.Blocked = contract.PinsStageBlocked, &why
		}
	case statuses["qualify-offline"] == "success":
		p.Stage = contract.PinsStageHosts
	default:
		p.Stage = contract.PinsStageOffline
	}
	return out, nil
}

// componentOlder: whether component version a (0.168.0) is below b (0.169.0); false when either has no x.y.z.
func componentOlder(a, b string) bool {
	pa, pb := strings.Split(pinsSemver.FindString(a), "."), strings.Split(pinsSemver.FindString(b), ".")
	if len(pa) != 3 || len(pb) != 3 {
		return false
	}
	for i := range pa {
		x, _ := strconv.Atoi(pa[i])
		y, _ := strconv.Atoi(pb[i])
		if x != y {
			return x < y
		}
	}
	return false
}

// pinsFailureNames: whether a failure line names a component (a trial label is "<host> <cli> <model>: ...").
func pinsFailureNames(failure, name string) bool {
	head, _, _ := strings.Cut(failure, ":")
	for _, w := range strings.Fields(head) {
		if w == name || strings.HasSuffix(w, "/"+name) {
			return true
		}
	}
	return false
}

// pinsProposedVersions: the versions a proposal head pins, read once per head.
func pinsProposedVersions(ctx context.Context, a *App, k harnessrelease.Kind, sha string) (map[string]string, error) {
	key := k.Name + " " + sha
	pinsViewMu.Lock()
	v, ok := pinsViewFiles[key]
	pinsViewMu.Unlock()
	if ok {
		return v, nil
	}
	b, _, err := githubRead(ctx, a, ciProject, "file_at", map[string]string{"owner": ciOwner, "repo": ciRepo, "path": "config/" + k.File, "ref": sha})
	if err != nil {
		return nil, err
	}
	if b, err = pinsFileOf(b); err != nil {
		return nil, err
	}
	_, versions, err := k.PinnedIn(b)
	if err != nil {
		return nil, fmt.Errorf("%s at %s: %w", k.File, sha, err)
	}
	v = pinsTargetVersions(versions)
	pinsViewMu.Lock()
	if len(pinsViewFiles) > 64 {
		clear(pinsViewFiles)
	}
	pinsViewFiles[key] = v
	pinsViewMu.Unlock()
	return v, nil
}

// pinsViewRegressions: the pinned CLI versions the watch's rule flags now, per host and CLI (pins_watch.go).
func pinsViewRegressions(ctx context.Context, a *App) (map[string]pinsRegression, error) {
	assets, err := harnessrelease.Read(releaseDir(), "linux-amd64")
	out := map[string]pinsRegression{}
	if err != nil || len(assets) == 0 {
		return out, err
	}
	pinned := map[string]string{}
	for name, asset := range assets {
		pinned[name] = asset.Version
	}
	var runs []pinsWatchRun
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		return unscopedNoVal(q, func() error {
			rows, err := q.db().PinsWatchRuns(q.ctx(), harnessrelease.CLIs.Names)
			for _, r := range rows {
				runs = append(runs, pinsWatchRun{Host: r.HostID, Harness: r.Harness, Version: r.HarnessVersion, Failed: r.Failed, Total: r.Total, FirstAt: r.FirstAt.Time})
			}
			return err
		})
	}); err != nil {
		return out, err
	}
	for _, r := range pinsRegressions(runs, pinned, time.Now()) {
		out[r.Host+" "+r.Harness] = r
	}
	return out, nil
}

// systemComponents: the component rows of system_changes, from this release's pins, the hosts and the last pass.
func systemComponents(hosts []contract.HostRow) []contract.SystemChangeKind {
	pinsViewMu.Lock()
	kinds, failing := pinsViewKinds, pinsViewFailing
	pinsViewMu.Unlock()
	live := []contract.HostRow{}
	for _, h := range hosts {
		if h.RemovedAt == nil {
			live = append(live, h)
		}
	}
	sort.Slice(live, func(i, j int) bool { return live[i].Id < live[j].Id })
	out := []contract.SystemChangeKind{}
	for _, k := range harnessrelease.Kinds {
		enabled, versions, err := k.Pinned(releaseDir())
		if err != nil { // an unreadable pin file is not "pins off": its cells stay unknown
			log.Printf("system_changes %s pins: %v", k.Name, err)
			enabled, versions = true, nil
		}
		view := kinds[k.Name]
		row := contract.SystemChangeKind{Kind: contract.ComponentKind(k.Name), Enabled: enabled, Proposal: view.proposal, Components: []contract.SystemChangeComponent{}}
		pinned := pinsTargetVersions(versions)
		for _, name := range k.Names {
			c := contract.SystemChangeComponent{Name: name, Failures: view.failures[name], Hosts: []contract.SystemChangeComponentHost{}}
			if c.Failures == nil {
				c.Failures = []string{}
			}
			if v := pinned[name]; v != "" {
				c.Pinned = &v
			}
			if v, ok := view.proposed[name]; ok {
				c.Proposed = &v
			}
			for _, h := range live {
				target := pinsTargetOf(deref(h.Platform))
				c.Hosts = append(c.Hosts, componentHost(k, name, enabled, versions[target][name], h, failing[h.Id+" "+name]))
			}
			row.Components = append(row.Components, c)
		}
		out = append(out, row)
	}
	return out
}

// componentHost: one host's cell of a component row.
func componentHost(k harnessrelease.Kind, name string, enabled bool, pinned string, h contract.HostRow, flagged pinsRegression) contract.SystemChangeComponentHost {
	cell := contract.SystemChangeComponentHost{HostId: h.Id, State: contract.ComponentHostStateUnknown}
	// The version the host reports: its CLI report, its host tools report (also while the kind's pins are off). With the
	// kind's pins on, a tool the host reports without a pin is its own PATH tool (its release has no tool pins yet), not
	// the managed one: own, unless it is installing or refused the release that brings the pin, or the pin fails there.
	unmanaged := false
	if k.Name == harnessrelease.Tools.Name {
		for _, tv := range h.ToolVersions {
			if tv.Tool == name && tv.Observed != nil {
				cell.Observed = tv.Observed
				unmanaged = tv.Expected == nil
			}
		}
	}
	if k.Name == harnessrelease.CLIs.Name {
		for _, hv := range h.HarnessVersions {
			if hv.Harness == name && hv.Observed != nil {
				o := pinsSemver.FindString(*hv.Observed)
				if o == "" {
					o = *hv.Observed
				}
				cell.Observed = &o
			}
		}
	}
	if !enabled {
		cell.State = contract.ComponentHostStateOwn
		return cell
	}
	if pinned == "" { // nothing pinned for this host's target: no drift to show
		if unmanaged {
			cell.State = contract.ComponentHostStateOwn
		}
		return cell
	}
	cell.Pinned = &pinned
	upd, hasUpd := decodeUpdate(rawAny(h.Update))
	switch {
	case !unmanaged && flagged.Total > 0 && flagged.Version == pinned:
		cell.State, cell.Failed, cell.Total = contract.ComponentHostStateFailing, &flagged.Failed, &flagged.Total
	case !unmanaged && cell.Observed != nil && *cell.Observed == pinned:
		cell.State = contract.ComponentHostStatePinned
	case hasUpd && upd.State == contract.UpdateStatePending && upd.Version == ownRelease().Version:
		cell.State = contract.ComponentHostStateInstalling
	case hasUpd && upd.Version == ownRelease().Version && (upd.State == contract.UpdateStateRefused || upd.State == contract.UpdateStateFailed || upd.State == contract.UpdateStateError):
		cell.State = contract.ComponentHostStateRefused
		if upd.Error != nil {
			cell.Reason = upd.Error
		}
	case unmanaged:
		cell.State = contract.ComponentHostStateOwn
	case cell.Observed != nil:
		cell.State = contract.ComponentHostStateBehind
	}
	return cell
}

// pinsBlocked: why GitHub holds a qualified proposal, "" when it does not or the read fails. GitHub answers
// mergeable_state blocked while a required check or review is missing; with every required check and the proposal's
// statuses green, what is left is an approving review a ruleset requires (an unattributed bot commit).
func pinsBlocked(ctx context.Context, a *App, number int64, statuses map[string]string) string {
	b, _, err := githubRead(ctx, a, ciProject, "pull_get", map[string]string{"owner": ciOwner, "repo": ciRepo, "number": fmt.Sprint(number)})
	if err != nil {
		return ""
	}
	var pr struct {
		MergeableState string `json:"mergeable_state"`
	}
	if json.Unmarshal(b, &pr) != nil || pr.MergeableState != "blocked" {
		return ""
	}
	return pinsBlockedReason(statuses)
}

// pinsBlockedReason: the words for a blocked proposal by what its own statuses show.
func pinsBlockedReason(statuses map[string]string) string {
	for _, c := range []string{"qualify-offline", "qualify-hosts", "code-review"} {
		if statuses[c] != "success" {
			return "waits for " + c
		}
	}
	return "needs owner approval (ruleset)"
}
