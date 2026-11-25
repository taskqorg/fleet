package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/harnessrelease"
)

// The 24-hour watch of a landed CLI pin (docs/designs/fleet-host-updates.md, steps 9 and 10). For each host, the agent
// runs on the pinned version of a CLI are compared with that host's runs on other versions over the week before. A
// version that first ran on a host less than 24 hours ago and fails clearly more there marks its merged proposal's head
// with the failed status pins-watch ("revert <cli> <version>: ..."). The daily pins workflow turns that into a revert
// proposal, which also skips the version, and qualifies it like any other. The owner gets one note per reverted version.
const (
	pinsWatchWindow   = 24 * time.Hour
	pinsWatchMinRuns  = 5    // runs of the new version on a host before it is judged
	pinsWatchMinFails = 3    // and failures among them
	pinsWatchMargin   = 0.25 // its failure share above the host's other versions
)

var (
	pinsWatchMu   sync.Mutex
	pinsWatchLast time.Time
)

// pinsWatchRun: one host's ended runs of one CLI version.
type pinsWatchRun struct {
	Host, Harness, Version string
	Failed, Total          int64
	FirstAt                time.Time
}

// pinsRegression: a landed version that fails on a host.
type pinsRegression struct {
	Harness, Version, Host string
	Failed, Total          int64
	Baseline               float64
}

func (r pinsRegression) describe() string {
	return fmt.Sprintf("revert %s %s: %d of %d runs failed on %s (other versions %.0f%%)", r.Harness, r.Version, r.Failed, r.Total, r.Host, r.Baseline*100)
}

// pinsRegressions: the pinned versions (per CLI) whose runs fail clearly more than the host's other versions, judged
// only within the watch window after the version first ran on that host.
func pinsRegressions(runs []pinsWatchRun, pinned map[string]string, now time.Time) []pinsRegression {
	type key struct{ host, harness string }
	newer, base := map[key]pinsWatchRun{}, map[key][2]int64{}
	for _, r := range runs {
		v := pinned[r.Harness]
		k := key{r.Host, r.Harness}
		if v != "" && strings.Contains(r.Version, v) {
			newer[k] = r
		} else {
			b := base[k]
			base[k] = [2]int64{b[0] + r.Failed, b[1] + r.Total}
		}
	}
	out := []pinsRegression{}
	for k, n := range newer {
		if now.Sub(n.FirstAt) > pinsWatchWindow || n.Total < pinsWatchMinRuns || n.Failed < pinsWatchMinFails {
			continue
		}
		b := base[k]
		rate := 0.0
		if b[1] > 0 {
			rate = float64(b[0]) / float64(b[1])
		}
		if float64(n.Failed)/float64(n.Total) >= rate+pinsWatchMargin {
			out = append(out, pinsRegression{Harness: k.harness, Version: pinned[k.harness], Host: k.host, Failed: n.Failed, Total: n.Total, Baseline: rate})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].describe() < out[j].describe() })
	return out
}

// pinsWatchPass: at most every 10 minutes, from the delivery pass with the public repository's closed pull requests.
func pinsWatchPass(ctx context.Context, a *App, closed []ghPull) error {
	pinsWatchMu.Lock()
	if time.Since(pinsWatchLast) < 10*time.Minute {
		pinsWatchMu.Unlock()
		return nil
	}
	pinsWatchLast = time.Now()
	pinsWatchMu.Unlock()
	assets, err := harnessrelease.Read(releaseDir(), "linux-amd64")
	if err != nil || len(assets) == 0 {
		return err
	}
	pinned := map[string]string{}
	for name, a := range assets {
		pinned[name] = a.Version
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
		return err
	}
	problems := []Problem{}
	done := map[string]bool{}
	for _, r := range pinsRegressions(runs, pinned, time.Now()) {
		if done[r.Harness] {
			continue
		}
		done[r.Harness] = true
		pr, ok := pinsLandedProposal(ctx, a, closed, r.Harness, r.Version)
		if !ok {
			continue
		}
		if st, err := pinsStatuses(ctx, a, pr.Head.SHA); err == nil && st["pins-watch"] == "" {
			if err := pinsWatchPublish(ctx, a, pr, r.describe()); err != nil {
				return err
			}
		}
		problems = append(problems, Problem{Cause: "pins:cli:revert:" + r.Harness + "@" + r.Version, Options: []any{"ok"},
			Text: "Managed CLI " + r.Harness + " " + r.Version + " is being reverted: " + r.describe() + ". The next pins run proposes the previous version and skips this one (" + pr.URL + ")."})
	}
	return a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return problemsSet(q, "pins", "^pins:[a-z]+:revert:", problems)
	})
}

// pinsLandedProposal: the merged CLI proposal whose head pins version of harness.
func pinsLandedProposal(ctx context.Context, a *App, closed []ghPull, harness, version string) (ghPull, bool) {
	for _, pr := range closed {
		if pinsKind(pr.Head.Ref) != "cli" || pr.MergedAt == nil || pinsWritable(pr) != nil {
			continue
		}
		c, err := pinsCandidateAt(ctx, a, pr.Head.SHA)
		if err == nil && c["linux-amd64"][harness].Version == version {
			return pr, true
		}
	}
	return ghPull{}, false
}

// pinsWatchPublish: the failed status pins-watch on a landed proposal's head, which the pins workflow reads.
func pinsWatchPublish(ctx context.Context, a *App, pr ghPull, description string) error {
	if err := pinsWritable(pr); err != nil {
		return err
	}
	line := pinsGatewayCall(ctx, a, "status_create", map[string]string{"owner": ciOwner, "repo": ciRepo, "sha": pr.Head.SHA,
		"state": "failure", "context": "pins-watch", "description": description, "target_url": "https://fleet.ufobe.com/live"})
	if line.Outcome != contract.CallOutcomeOk {
		return fmt.Errorf("%s status_create: %s %d", pinsWriteConn, deref(line.ErrorCode), line.Status)
	}
	return nil
}
