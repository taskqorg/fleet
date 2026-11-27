package main

import (
	"cmp"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"fleet/contract"
)

func siteCheck(state contract.ProjectSetupCheckState, reason, action string, at *int64) contract.ProjectSetupCheck {
	return setupCheck(contract.ProjectSetupCheckKindAgentSiteAccess, state, reason, action, at)
}

func summarizeProjectSetup(checks []contract.ProjectSetupCheck) contract.ProjectSetupState {
	state := contract.ProjectSetupStateNotApplicable
	for _, check := range checks {
		switch check.State {
		case contract.ProjectSetupCheckStateMissing, contract.ProjectSetupCheckStateBlocked:
			return contract.ProjectSetupStateAttention
		case contract.ProjectSetupCheckStateChecking:
			state = contract.ProjectSetupStateChecking
		case contract.ProjectSetupCheckStateUnknown:
			if state != contract.ProjectSetupStateChecking {
				state = contract.ProjectSetupStateUnknown
			}
		case contract.ProjectSetupCheckStateReady:
			if state == contract.ProjectSetupStateNotApplicable {
				state = contract.ProjectSetupStateReady
			}
		}
	}
	return state
}

type projectSetupHost struct {
	id, holder   string
	browser      []contract.BrowserTargetObservation
	active       []string
	capabilities []string
	connections  []string
	seen         int64
}

func projectSetupHosts(q *Request, now int64) ([]projectSetupHost, error) {
	return unscoped(q, func() ([]projectSetupHost, error) {
		rows, err := q.db().HostObservations(q.ctx())
		if err != nil {
			return nil, err
		}
		out := make([]projectSetupHost, 0, len(rows))
		for _, row := range rows {
			seen := int64(q.app.effectiveHostSeen(row.ID, float64(timeMs(row.LastSeenAt))))
			if seen == 0 || now-seen >= hostStaleMs {
				continue
			}
			var p hostPollObservationPayload
			if err := json.Unmarshal(row.Payload, &p); err != nil {
				return nil, err
			}
			out = append(out, projectSetupHost{id: row.ID, holder: deref(row.Holder), browser: p.BrowserTargets,
				active: valueOr(p.ActiveExecutions), capabilities: valueOr(p.Capabilities), connections: valueOr(p.GitConnections), seen: seen})
		}
		return out, nil
	})
}

func sanitizeBrowserTargets(observations []contract.BrowserTargetObservation, active []string, now int64) []contract.BrowserTargetObservation {
	var out []contract.BrowserTargetObservation
	for _, v := range observations {
		digest, err := hex.DecodeString(v.Generation)
		if !idPattern.MatchString(v.ProjectId) || !idPattern.MatchString(v.TargetId) || err != nil || len(digest) != 32 ||
			v.StartedAt <= 0 || v.CheckedAt < v.StartedAt || v.CheckedAt > now ||
			(now-v.StartedAt >= contract.SiteAccessEvidenceAge.Milliseconds() && (v.ExecutionId == nil || !slices.Contains(active, *v.ExecutionId))) {
			continue
		}
		switch v.ReasonCode {
		case "verified", "runtime_unavailable", "auth_rejected", "identity_mismatch", "unexpected_status", "probe_failed":
			out = append(out, v)
		}
	}
	return out
}

// A host can have more than one poll in flight. Keep the newest check per target and run
// even when an earlier poll's request reaches the coordinator late.
func mergeBrowserTargets(prior, current []contract.BrowserTargetObservation, active []string, now int64) []contract.BrowserTargetObservation {
	type key struct{ project, target, execution string }
	byTarget := map[key]contract.BrowserTargetObservation{}
	for _, group := range [][]contract.BrowserTargetObservation{sanitizeBrowserTargets(prior, active, now), current} {
		for _, report := range group {
			k := key{report.ProjectId, report.TargetId, deref(report.ExecutionId)}
			if previous, ok := byTarget[k]; !ok || newerSiteProbe(report, previous) {
				byTarget[k] = report
			}
		}
	}
	out := slices.Collect(maps.Values(byTarget))
	slices.SortFunc(out, func(a, b contract.BrowserTargetObservation) int {
		return cmp.Or(cmp.Compare(a.ProjectId, b.ProjectId), cmp.Compare(a.TargetId, b.TargetId),
			cmp.Compare(deref(a.ExecutionId), deref(b.ExecutionId)))
	})
	return out
}

func newerSiteProbe(a, b contract.BrowserTargetObservation) bool {
	return a.StartedAt > b.StartedAt || (a.StartedAt == b.StartedAt && a.CheckedAt > b.CheckedAt)
}

func newestSiteObservation(host projectSetupHost, pid, target, generation, executionID string, now int64) *contract.BrowserTargetObservation {
	var latest *contract.BrowserTargetObservation
	for i := range host.browser {
		check := &host.browser[i]
		if check.ProjectId != pid || check.TargetId != target || check.Generation != generation ||
			check.StartedAt <= 0 || check.CheckedAt < check.StartedAt || check.CheckedAt > now ||
			(executionID == "" && now-check.StartedAt >= contract.SiteAccessEvidenceAge.Milliseconds()) ||
			(executionID != "" && deref(check.ExecutionId) != executionID) {
			continue
		}
		// A late result of an older check cannot replace a newer check on that host.
		if latest == nil || newerSiteProbe(*check, *latest) {
			latest = check
		}
	}
	return latest
}

func projectSiteTarget(pid string, target contract.AgentSiteTarget, grant siteGrantStatus, hosts []projectSetupHost, now int64) contract.ProjectSiteAccessTarget {
	out := contract.ProjectSiteAccessTarget{TargetId: target.Id, Origin: target.Origin, Scope: target.Scope, Roles: target.Roles,
		AuthKind: target.AuthKind, ExpectedIdentity: target.ExpectedIdentity, ExpiresAt: grant.expiresAt}
	set := func(state contract.ProjectSetupCheckState, reason, action string) contract.ProjectSiteAccessTarget {
		out.State, out.ReasonCode, out.NextAction = state, reason, optStr(action)
		return out
	}
	switch grant.reason {
	case "grant_missing":
		return set(contract.ProjectSetupCheckStateMissing, grant.reason, "configure")
	case "credential_expired", "credential_unavailable":
		return set(contract.ProjectSetupCheckStateBlocked, grant.reason, "rotate_credential")
	case "":
	default:
		return set(contract.ProjectSetupCheckStateBlocked, grant.reason, "configure")
	}

	var ready, failed *contract.BrowserTargetObservation
	for _, host := range hosts {
		check := newestSiteObservation(host, pid, target.Id, grant.generation, "", now)
		if check == nil {
			continue
		}
		if check.ReasonCode == "verified" {
			if ready == nil || check.CheckedAt > ready.CheckedAt {
				ready = check
			}
		} else if failed == nil || check.CheckedAt > failed.CheckedAt {
			failed = check
		}
	}
	if ready != nil {
		out.ObservedIdentity, out.CheckedAt = target.ExpectedIdentity, &ready.CheckedAt
		return set(contract.ProjectSetupCheckStateReady, "verified", "use_project_browser")
	}
	if failed != nil {
		out.CheckedAt = &failed.CheckedAt
		action := "recheck"
		if failed.ReasonCode == "auth_rejected" {
			action = "rotate_credential"
		}
		return set(contract.ProjectSetupCheckStateBlocked, failed.ReasonCode, action)
	}
	return set(contract.ProjectSetupCheckStateUnknown, "target_unverified", "recheck")
}

func summarizeSiteTargets(targets []contract.ProjectSiteAccessTarget) contract.ProjectSetupCheck {
	best := siteCheck(contract.ProjectSetupCheckStateReady, "verified", "use_project_browser", nil)
	for _, target := range targets {
		// Confirmed gaps take precedence over unverified targets.
		if target.State == contract.ProjectSetupCheckStateBlocked || target.State == contract.ProjectSetupCheckStateMissing {
			return siteCheck(target.State, target.ReasonCode, deref(target.NextAction), target.CheckedAt)
		}
		if target.State != contract.ProjectSetupCheckStateReady {
			best = siteCheck(target.State, target.ReasonCode, deref(target.NextAction), target.CheckedAt)
		} else if best.State == contract.ProjectSetupCheckStateReady && (best.ObservedAt == nil ||
			target.CheckedAt != nil && *target.CheckedAt < *best.ObservedAt) {
			best.ObservedAt = target.CheckedAt
		}
	}
	return best
}

func projectSiteSetup(q *Request, pid string, project siteAccessProject, hosts []projectSetupHost, now int64) (contract.ProjectSetupCheck, contract.ProjectSiteAccess) {
	out := contract.ProjectSiteAccess{Targets: []contract.ProjectSiteAccessTarget{}}
	agent := strings.TrimPrefix(q.agent, "execution:")
	var caller *contract.ProjectSiteAccessCaller
	if agent != q.agent && agent != "" {
		caller = &contract.ProjectSiteAccessCaller{Availability: contract.ProjectSiteAccessAvailabilityUnknown, ReasonCode: "applicability_unknown"}
		out.ForCaller = caller
	}
	if project.Config == nil {
		return siteCheck(contract.ProjectSetupCheckStateUnknown, "applicability_unknown", "", nil), out
	}
	out.Mode = &project.Config.Mode
	if project.Config.Mode == contract.AgentSiteAccessModeNotAWebApp {
		if caller != nil {
			caller.Availability, caller.ReasonCode = contract.ProjectSiteAccessAvailabilityNotApplicable, "not_a_web_app"
		}
		return siteCheck(contract.ProjectSetupCheckStateNotApplicable, "not_a_web_app", "", nil), out
	}
	if len(project.Config.Targets) == 0 {
		if caller != nil {
			caller.Availability, caller.ReasonCode, caller.NextAction = contract.ProjectSiteAccessAvailabilityUnavailable, "target_missing", ptr("configure")
		}
		return siteCheck(contract.ProjectSetupCheckStateMissing, "target_missing", "configure", nil), out
	}

	eligible := make([]projectSetupHost, 0, len(hosts))
	for _, h := range hosts {
		if project.hostEligible(h.id) && managedSiteHolder(q, h.holder) &&
			contract.HasCapability(h.capabilities, string(contract.CapabilityAgentSiteAccessV1)) {
			eligible = append(eligible, h)
		}
	}
	var run agentExecution
	var runTarget *contract.AgentSiteTarget
	if caller != nil {
		var err error
		caller.ReasonCode = "run_not_active"
		run, err = getExecution(q, agent)
		if err == nil {
			var role string
			runTarget, role, caller.ReasonCode = siteTargetForRun(q, pid, project, run)
			caller.Role = optStr(role)
		}
		caller.Availability = contract.ProjectSiteAccessAvailabilityUnavailable
	}
	for _, target := range project.Config.Targets {
		grant := readSiteAccessGrant(secretDir(), pid, target, project.Roles, now)
		view := projectSiteTarget(pid, target, grant, eligible, now)
		if grant.reason == "" && (project.Execution == nil || !slices.Contains(project.Execution.Tools, "browser") || len(project.Environments) == 0) {
			view.State, view.ReasonCode, view.NextAction = contract.ProjectSetupCheckStateMissing, "browser_environment_missing", ptr("configure")
		}
		out.Targets = append(out.Targets, view)
		if runTarget != nil && target.Id == runTarget.Id {
			projectSiteCaller(caller, pid, view, grant, eligible, run, now)
		}
	}
	return summarizeSiteTargets(out.Targets), out
}

func projectSiteCaller(caller *contract.ProjectSiteAccessCaller, pid string, view contract.ProjectSiteAccessTarget, grant siteGrantStatus,
	hosts []projectSetupHost, run agentExecution, now int64) {
	caller.TargetId = &view.TargetId
	if view.State == contract.ProjectSetupCheckStateMissing || grant.reason != "" {
		caller.ReasonCode, caller.NextAction = view.ReasonCode, view.NextAction
		return
	}
	for _, host := range hosts {
		if host.id != deref(run.HostID) {
			continue
		}
		newest := newestSiteObservation(host, pid, view.TargetId, grant.generation, "", now)
		launch := newestSiteObservation(host, pid, view.TargetId, grant.generation, run.ID, now)
		if newest != nil && newest.ReasonCode != "verified" {
			caller.ReasonCode, caller.NextAction = newest.ReasonCode, ptr("recheck")
			return
		}
		if newest != nil && launch != nil && launch.ReasonCode == "verified" && slices.Contains(host.active, run.ID) {
			caller.Availability = contract.ProjectSiteAccessAvailabilityAvailable
			caller.ReasonCode, caller.NextAction = "verified", ptr("use_project_browser")
			return
		}
		caller.Availability = contract.ProjectSiteAccessAvailabilityUnknown
		caller.ReasonCode, caller.NextAction = "run_browser_unverified", ptr("recheck")
		return
	}
	caller.ReasonCode, caller.NextAction = "host_unverified", ptr("use_eligible_host")
}

func projectSetupFor(q *Request, pid string, settings rawObject, hosts []projectSetupHost, now int64) (contract.ProjectSetup, contract.ProjectSiteAccess) {
	project := agentView[siteAccessProject](settings)
	site, access := projectSiteSetup(q, pid, project, hosts, now)
	checks := projectRepositorySetup(q, pid, agentView[runSettings](settings), hosts)
	checks = append(checks, site, projectPMSetup(q, pid, settings, hosts))
	return contract.ProjectSetup{Checks: checks, State: summarizeProjectSetup(checks)}, access
}

func readProjectSetup(q *Request, pid string, settings rawObject) (contract.ProjectSetup, contract.ProjectSiteAccess) {
	now := nowMs()
	hosts, err := projectSetupHosts(q, now)
	setup, access := projectSetupFor(q, pid, settings, hosts, now)
	if err != nil {
		for i := range setup.Checks {
			if setup.Checks[i].ReasonCode == "target_unverified" {
				setup.Checks[i].ReasonCode = "host_observation_unavailable"
			}
		}
		for i := range access.Targets {
			if access.Targets[i].ReasonCode == "target_unverified" {
				access.Targets[i].ReasonCode = "host_observation_unavailable"
			}
		}
		if access.ForCaller != nil && access.ForCaller.ReasonCode == "host_unverified" {
			access.ForCaller.Availability, access.ForCaller.ReasonCode = contract.ProjectSiteAccessAvailabilityUnknown, "host_observation_unavailable"
		}
	}
	return setup, access
}

func attachProjectSetup(q *Request, id string, context rawObject) {
	setup, access := readProjectSetup(q, id, recOf(context["settings"]))
	context["setup"], context["siteAccess"] = jsonBytes(setup), jsonBytes(access)
}
