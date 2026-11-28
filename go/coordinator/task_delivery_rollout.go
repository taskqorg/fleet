package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"fleet/coordinator/db"
)

type taskDeliveryHostProof struct {
	Target          string `json:"target"`
	Version         string `json:"version"`
	RequiredVersion string `json:"requiredVersion,omitempty"`
}

type taskDeliveryRollout struct {
	release            releaseInfo
	coordinator        string
	coordinatorHealthy bool
	site               string
	liveVersions       []string
	hosts              []taskDeliveryHostProof
	hasHostObservation bool
	unknown            bool
}

// Delivery display counts live host versions. Durable task proof requires a healthy coordinator and every observed
// executor eligible for auto-update, including one with a stale observation, which leaves proof unconfirmed.
func observeFleetTaskRollout(q *Request, ui string) (taskDeliveryRollout, error) {
	healthy, rel := healthyRelease()
	out := taskDeliveryRollout{coordinator: version, coordinatorHealthy: deref(healthy) == version, release: rel, site: ui}
	records, err := hostRecords(q)
	if err != nil {
		return out, err
	}
	observations, err := deliveryHostsSeen(q)
	if err != nil {
		return out, err
	}
	configured := map[string]hostRecord{}
	for _, host := range records {
		configured[host.ID] = host
	}
	now := time.Now()
	for _, observed := range observations {
		seen := q.app.effectiveHostSeen(observed.ID, observed.LastSeenMs)
		out.observeExecutor(configured[observed.ID], observed, now.Sub(time.UnixMilli(int64(seen))), now)
	}
	return out, nil
}

func (r *taskDeliveryRollout) observeExecutor(host hostRecord, observed db.DeliveryHostsSeenRow, age time.Duration, now time.Time) {
	if age < time.Hour && observed.Version != "" {
		r.liveVersions = append(r.liveVersions, observed.Version)
	}
	if host.ID == "" || host.removed() || observed.LastSeenMs == 0 {
		return
	}
	r.hasHostObservation = true
	if auto := host.autoUpdate(); auto != "on" {
		until, err := time.Parse(time.RFC3339, auto)
		if err != nil || until.After(now) {
			return // off or held; an expired hold is eligible for auto-update again
		}
	}
	if age > time.Hour || !releaseVersionRe.MatchString(observed.Version) {
		r.unknown = true
		return
	}
	r.hosts = append(r.hosts, taskDeliveryHostProof{Target: observed.ID, Version: observed.Version})
}

// executorRolloutFloor uses the healthy running release's manifest to prove equivalence within its unchanged interval.
func executorRolloutFloor(required, running string, healthy bool, rel releaseInfo) string {
	changedIn := rel.Executor.ChangedIn
	if healthy && rel.Version == running && releaseVersionRe.MatchString(running) &&
		!olderVersion(running, required) && olderVersion(changedIn, required) && releaseVersionRe.MatchString(changedIn) {
		return changedIn
	}
	return required
}

func (r taskDeliveryRollout) displayedInstallation(tag string) bool {
	if uiOnlyRelease(tag) {
		return len(r.installation(tag)) != 0
	}
	return installed(executorRolloutFloor(releaseVersionOf(tag), r.coordinator, r.coordinatorHealthy, r.release), r.liveVersions)
}

func (r taskDeliveryRollout) installation(tag string) []taskDeliveryHostProof {
	required := releaseVersionOf(tag)
	if required == "" {
		return nil
	}
	if uiOnlyRelease(tag) {
		if r.site == "" || versionLess(r.site, required) {
			return nil
		}
		return []taskDeliveryHostProof{{Target: "site", Version: r.site}}
	}
	if r.unknown || !r.hasHostObservation || !r.coordinatorHealthy ||
		!releaseVersionRe.MatchString(required) || olderVersion(r.coordinator, required) {
		return nil
	}
	executorRequired := executorRolloutFloor(required, r.coordinator, r.coordinatorHealthy, r.release)
	proof := []taskDeliveryHostProof{{Target: "coordinator", Version: r.coordinator}}
	for _, host := range r.hosts {
		if olderVersion(host.Version, executorRequired) {
			return nil
		}
		if executorRequired != required {
			host.RequiredVersion = executorRequired
		}
		proof = append(proof, host)
	}
	return proof
}

func confirmedRolloutRollback(previous deliveryMergedPR, observed []taskDeliveryHostProof) bool {
	required := releaseVersionOf(previous.Release)
	if required == "" {
		return false
	}
	ui := uiOnlyRelease(previous.Release)
	for _, host := range observed {
		if host.Version == "" || !ui && !releaseVersionRe.MatchString(host.Version) {
			continue
		}
		for _, old := range previous.InstalledOn {
			if old.Target == host.Target && versionLess(host.Version, firstString(old.RequiredVersion, required)) {
				return true
			}
		}
	}
	return false
}

// Qualify only current task candidates, outside PostgreSQL write locks. Timestamp ordering provides a candidate
// release. GitHub's compare response or exact commit equality supplies the ancestry evidence used for acceptance.
func qualifyFleetTaskDeliveries(ctx context.Context, a *App, projects []string, prs []deliveryMergedPR, rollout taskDeliveryRollout) error {
	var matches []taskDeliveryMatch
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		matches, err = findTaskDeliveryMatches(q, prs)
		return err
	}); err != nil {
		return err
	}
	candidates := map[int64]bool{}
	for _, match := range matches {
		if slices.Contains(projects, match.projectID) {
			candidates[match.pr.Number] = true
		}
	}
	var failures error
	for i := range prs {
		pr := &prs[i]
		if candidates[pr.Number] {
			pr.ObservedOn = slices.Clone(rollout.hosts)
			if rollout.coordinatorHealthy {
				pr.ObservedOn = append(pr.ObservedOn, taskDeliveryHostProof{Target: "coordinator", Version: rollout.coordinator})
			}
			if rollout.site != "" {
				pr.ObservedOn = append(pr.ObservedOn, taskDeliveryHostProof{Target: "site", Version: rollout.site})
			}
		}
		if !pr.Delivered {
			continue
		}
		pr.Delivered = false
		if !candidates[pr.Number] {
			continue
		}
		installation := rollout.installation(pr.Release)
		if len(installation) == 0 || !validCommitSHA(pr.ReleaseTarget) || !validCommitSHA(pr.MergeSHA) {
			continue
		}
		covered, err := fleetReleaseContains(ctx, a, projects[0], pr.MergeSHA, pr.ReleaseTarget)
		if err != nil {
			failures = errors.Join(failures, fmt.Errorf("PR %d release ancestry: %w", pr.Number, err))
			continue
		}
		if covered {
			pr.Delivered, pr.ReleaseCovered, pr.InstalledOn = true, true, installation
		}
	}
	return failures
}

func fleetReleaseContains(ctx context.Context, a *App, project, merge, target string) (bool, error) {
	if merge == target {
		return true, nil
	}
	owner, repo, _ := strings.Cut(githubPublicRepo, "/")
	raw, _, err := githubRead(ctx, a, project, "compare_commits", map[string]string{"owner": owner, "repo": repo, "base": merge, "head": target})
	if err != nil {
		return false, err
	}
	var comparison struct {
		Status    string `json:"status"`
		Behind    int64  `json:"behind_by"`
		MergeBase struct {
			SHA string `json:"sha"`
		} `json:"merge_base_commit"`
	}
	if err := json.Unmarshal(raw, &comparison); err != nil {
		return false, err
	}
	return comparison.Status == "ahead" && comparison.Behind == 0 && comparison.MergeBase.SHA == merge, nil
}
