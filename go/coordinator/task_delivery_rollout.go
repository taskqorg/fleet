package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

type taskDeliveryHostProof struct {
	Target  string `json:"target"`
	Version string `json:"version"`
}

type taskDeliveryRollout struct {
	site    string
	hosts   []taskDeliveryHostProof
	unknown bool
}

// The native rollout must reach the coordinator and every observed host eligible for auto-update. The UI route alone
// proves the independent UI release. A stale or versionless required host leaves the native rollout unconfirmed.
func observeFleetTaskRollout(q *Request, ui string) (taskDeliveryRollout, error) {
	out := taskDeliveryRollout{site: ui, hosts: []taskDeliveryHostProof{{Target: "coordinator", Version: version}}}
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
	for _, observed := range observations {
		host, exists := configured[observed.ID]
		if !exists || host.removed() || host.autoUpdate() != "on" || observed.LastSeenMs == 0 {
			continue
		}
		seen := q.app.effectiveHostSeen(observed.ID, observed.LastSeenMs)
		if time.Since(time.UnixMilli(int64(seen))) > time.Hour || observed.Version == "" {
			out.unknown = true
			continue
		}
		out.hosts = append(out.hosts, taskDeliveryHostProof{Target: observed.ID, Version: observed.Version})
	}
	return out, nil
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
	if r.unknown || len(r.hosts) == 0 {
		return nil
	}
	for _, host := range r.hosts {
		if host.Version == "" || versionLess(host.Version, required) {
			return nil
		}
	}
	return r.hosts
}

func confirmedRolloutRollback(previous deliveryMergedPR, observed []taskDeliveryHostProof) bool {
	required := releaseVersionOf(previous.Release)
	if required == "" {
		return false
	}
	for _, host := range observed {
		if host.Version != "" && versionLess(host.Version, required) &&
			slices.ContainsFunc(previous.InstalledOn, func(old taskDeliveryHostProof) bool { return old.Target == host.Target }) {
			return true
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
