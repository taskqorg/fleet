package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
)

// Release pacing (docs/designs/fleet-host-updates.md section 7): which release a poll offers its host. A host installs
// a release only when the release changes its host part (executor, CLI and tool pins), at once for an immediate change,
// else when it is idle or inside its daily window. Fixed rules only; nothing here guesses.

// defaultUpdateWindow is the daily window in each host's local time; FLEET_UPDATE_WINDOW overrides it for all hosts.
const defaultUpdateWindow = "03:00-05:00"

// updateWindow: [start, end) in minutes after local midnight; end before start wraps past midnight.
func updateWindow() (start, end int, text string) {
	text = os.Getenv("FLEET_UPDATE_WINDOW")
	if s, e, ok := parseUpdateWindow(text); ok {
		return s, e, text
	}
	s, e, _ := parseUpdateWindow(defaultUpdateWindow)
	return s, e, defaultUpdateWindow
}

func parseUpdateWindow(w string) (start, end int, ok bool) {
	a, b, found := strings.Cut(w, "-")
	if !found {
		return 0, 0, false
	}
	clock := func(s string) (int, bool) {
		h, m, found := strings.Cut(strings.TrimSpace(s), ":")
		hh, err1 := strconv.Atoi(h)
		mm, err2 := strconv.Atoi(m)
		if !found || err1 != nil || err2 != nil || hh < 0 || hh > 23 || mm < 0 || mm > 59 {
			return 0, false
		}
		return hh*60 + mm, true
	}
	start, ok1 := clock(a)
	end, ok2 := clock(b)
	return start, end, ok1 && ok2 && start != end
}

// inUpdateWindow: whether now, at the host's UTC offset (nil: UTC), is inside the window.
func inUpdateWindow(now time.Time, offsetMinutes *int64, start, end int) bool {
	local := now.UTC()
	if offsetMinutes != nil {
		local = local.Add(time.Duration(*offsetMinutes) * time.Minute)
	}
	m := local.Hour()*60 + local.Minute()
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// hostPace is one poll's decision: the target to offer (nil: none) and what hosts_list shows (nil: nothing paced).
type hostPace struct {
	target *string
	view   *contract.HostUpdatePacing
}

// paceUpdate applies the steps of section 7 in order. target and rel are the healthy release the coordinator would
// offer; running is the host's version; drained says the host reports a drain episode; idle that it lists no active
// execution.
func paceUpdate(target *string, rel releaseInfo, running *string, drained, idle bool, offset *int64, now time.Time) hostPace {
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
	normal := func(reason string) hostPace {
		return hostPace{target: target, view: view(contract.UpdatePacingStateOffered, contract.UpdatePacingClassNormal, reason)}
	}
	start, end, text := updateWindow()
	switch {
	case drained:
		return normal("drain in progress")
	case idle:
		return normal("host idle")
	case inUpdateWindow(now, offset, start, end):
		return normal("daily window " + text + " local")
	}
	return hostPace{view: view(contract.UpdatePacingStateDeferred, contract.UpdatePacingClassNormal,
		fmt.Sprintf("waits for idle or %s local", text))}
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
