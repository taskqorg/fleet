package main

import (
	"slices"

	"fleet/contract"
)

func setupCheck(kind contract.ProjectSetupCheckKind, state contract.ProjectSetupCheckState, reason, action string, observedAt *int64) contract.ProjectSetupCheck {
	return contract.ProjectSetupCheck{Kind: kind, State: state, ReasonCode: reason, NextAction: optStr(action), ObservedAt: observedAt}
}

// Repository, connection and publishing setup use the same repository-scoped host capabilities
// as dispatch. They do not stand in for the environment job's ls-remote or checkout checks.
func projectRepositorySetup(q *Request, id string, p runSettings, hosts []projectSetupHost) []contract.ProjectSetupCheck {
	git, conn, ci := contract.ProjectSetupCheckKindGitForge, contract.ProjectSetupCheckKindConnections, contract.ProjectSetupCheckKindCiPublishing
	needsConnection := slices.ContainsFunc(p.Repos, func(repo projectRepo) bool { return repo.Connection != "" })
	needsPublishing := p.Execution != nil && p.Execution.Forge && len(p.Repos) > 0
	result := []contract.ProjectSetupCheck{
		setupCheck(git, contract.ProjectSetupCheckStateNotApplicable, "no_repository", "", nil),
		setupCheck(conn, contract.ProjectSetupCheckStateNotApplicable, "no_required_connection", "", nil),
		setupCheck(ci, contract.ProjectSetupCheckStateNotApplicable, "publishing_not_required", "", nil),
	}
	if len(p.Repos) > 0 {
		result[0] = setupCheck(git, contract.ProjectSetupCheckStateUnknown, "forge_unverified", "recheck", nil)
	}
	if needsConnection {
		result[1] = setupCheck(conn, contract.ProjectSetupCheckStateUnknown, "connection_unverified", "recheck", nil)
	}
	if needsPublishing {
		result[2] = setupCheck(ci, contract.ProjectSetupCheckStateUnknown, "publishing_unverified", "recheck", nil)
	}
	if len(p.Repos) == 0 {
		return result
	}
	var gitAt, connAt, publishAt int64
	for _, repo := range p.Repos {
		ref, ok := contract.ParseRepoRemote(repo.Remote, repo.Forge, repo.Host)
		if !ok {
			result[0] = setupCheck(git, contract.ProjectSetupCheckStateUnknown, "forge_unsupported", "configure", nil)
			return result
		}
		var foundGit, foundConnection, foundPublishing int64
		for _, h := range hosts {
			if !hostHolderAllowsProject(q, h.holder, id) || !hostCapsHaveRepo(h.capabilities, ref) {
				continue
			}
			foundGit = max(foundGit, h.seen)
			applies := slices.ContainsFunc(p.Environments, func(env projectEnvironment) bool {
				return env.HostID == h.id && (env.Repo == nil || *env.Repo == repo.ID)
			})
			if !applies {
				continue
			}
			foundPublishing = max(foundPublishing, h.seen)
			if repo.Connection != "" && slices.Contains(h.connections, repo.Connection) {
				foundConnection = max(foundConnection, h.seen)
			}
		}
		if foundGit == 0 {
			return result
		}
		gitAt = minPositive(gitAt, foundGit)
		if repo.Connection != "" {
			// A missing host/provider observation leaves the required connection unverified.
			connAt = minPositive(connAt, foundConnection)
			if foundConnection == 0 {
				connAt = -1
			}
		}
		publishAt = minPositive(publishAt, foundPublishing)
		if foundPublishing == 0 {
			publishAt = -1
		}
	}
	result[0] = setupCheck(git, contract.ProjectSetupCheckStateReady, "forge_verified", "", &gitAt)
	if needsConnection && connAt > 0 {
		result[1] = setupCheck(conn, contract.ProjectSetupCheckStateReady, "connection_verified", "", &connAt)
	}
	if needsPublishing && publishAt > 0 {
		result[2] = setupCheck(ci, contract.ProjectSetupCheckStateReady, "publishing_verified", "", &publishAt)
	}
	return result
}

func minPositive(a, b int64) int64 {
	if a == 0 {
		return b
	}
	return min(a, b)
}

func projectPMSetup(q *Request, id string, settings rawObject, hosts []projectSetupHost) contract.ProjectSetupCheck {
	kind := contract.ProjectSetupCheckKindPm
	profileID := rawText(recOf(settings["roles"])["pm"])
	if profileID == "" {
		return setupCheck(kind, contract.ProjectSetupCheckStateNotApplicable, "pm_not_configured", "", nil)
	}
	profile, err := readTrustedProfile(q, profileID)
	if err != nil {
		return setupCheck(kind, contract.ProjectSetupCheckStateUnknown, "pm_profile_unverified", "configure", nil)
	}
	harness := firstString(agentView[agentProfile](profile).Harness, "codex")
	accounts, err := unscoped(q, func() ([]accountRec, error) { return accountRecs(q) })
	if err != nil {
		return setupCheck(kind, contract.ProjectSetupCheckStateUnknown, "pm_binding_unverified", "recheck", nil)
	}
	project := agentView[runSettings](settings)
	owner := ownerEmail(q)
	for _, h := range hosts {
		if !hostHolderAllowsProject(q, h.holder, id) ||
			(len(project.Environments) > 0 && !slices.ContainsFunc(project.Environments, func(env projectEnvironment) bool {
				return env.HostID == h.id
			})) {
			continue
		}
		ok, err := harnessAllowed(q, h.id, harness)
		if err != nil || !ok {
			continue
		}
		for _, account := range accounts {
			if account.Harness != harness || account.Intake == "paused" ||
				(deref(account.ProjectID) != "" && *account.ProjectID != id) ||
				(account.Holder != owner && !slices.Contains(takeProjectsOf(account.Take), id)) {
				continue
			}
			binding := account.binding(h.id, nil, false)
			if binding != nil && deref(binding.Readiness) == "ready" && deref(binding.Binding) != "" {
				at, ok := jsonAs[int64](binding.ObservedAt)
				if ok && at > 0 {
					return setupCheck(kind, contract.ProjectSetupCheckStateReady, "pm_binding_verified", "", &at)
				}
			}
		}
	}
	return setupCheck(kind, contract.ProjectSetupCheckStateUnknown, "pm_binding_unverified", "recheck", nil)
}
