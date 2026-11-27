package main

import (
	"fleet/contract"
)

// Release pacing (docs/designs/fleet-host-updates.md section 7): which release a poll offers its host. A host installs
// a release only when the release changes its host part (executor, CLI and tool pins). Because executors update
// zero-downtime via detached run-workers and poller unix.Exec, any changed release is offered immediately with no idle
// gate or time-window deferral.

// hostPace is one poll's decision: the target to offer (nil: none) and what hosts_list shows (nil: nothing paced).
type hostPace struct {
	target *string
	view   *contract.HostUpdatePacing
}

// paceUpdate decides whether target should be offered to a host running *running.
// When the executor part has not changed since *running (changedIn skip), no target is offered so hosts do not restart
// unnecessarily on pure UI/coordinator/docs commits.
func paceUpdate(target *string, rel releaseInfo, running *string) hostPace {
	if target == nil || running == nil || !olderVersion(*running, *target) || rel.Executor.ChangedIn == "" {
		return hostPace{target: target} // nothing to install, or a release built before pacing: offer as before
	}
	v := *running
	view := func(state contract.UpdatePacingState, class contract.UpdatePacingClass, reason string) *contract.HostUpdatePacing {
		return &contract.HostUpdatePacing{Release: *target, State: state, Class: class, Reason: reason}
	}
	if !olderVersion(v, rel.Executor.ChangedIn) {
		return hostPace{view: view(contract.UpdatePacingStateCurrent, contract.UpdatePacingClassNormal,
			"executor unchanged since "+rel.Executor.ChangedIn)}
	}
	if u := rel.Executor.UrgentIn; u != "" && olderVersion(v, u) {
		return hostPace{target: target, view: view(contract.UpdatePacingStateOffered, contract.UpdatePacingClassImmediate,
			"immediate change in "+u)}
	}
	return hostPace{target: target, view: view(contract.UpdatePacingStateOffered, contract.UpdatePacingClassNormal,
		"zero-downtime update offered")}
}

// versionBehind: a host N-2 or older is flagged only when the executor changed since its version: a host that skipped
// web and coordinator releases runs the current host part.
func versionBehind(v string, rel releaseInfo) bool {
	if !olderVersion(v, rel.Previous) {
		return false
	}
	return rel.Executor.ChangedIn == "" || olderVersion(v, rel.Executor.ChangedIn)
}

// executorRelease: the oldest release whose executor equals this coordinator's: its executor.changedIn, else its own
// version (a release built before pacing).
func executorRelease() string {
	if c := ownRelease().Executor.ChangedIn; c != "" {
		return c
	}
	return version
}
