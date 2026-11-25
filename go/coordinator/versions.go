package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"fleet/contract"
)

// system_versions: the installed version of every Fleet part in one read, for the navigation's version mark and its
// overview (docs/design-system/controls/nav-rail.md, Version) and for agents.
func init() {
	op("system_versions", func(q *Request, _ *contract.SystemVersionsRequest) (contract.SystemVersionsReply, error) {
		return systemVersions(q)
	})
}

func systemVersions(q *Request) (contract.SystemVersionsReply, error) {
	list, err := hostsListReply(q, false)
	if err != nil {
		return contract.SystemVersionsReply{}, err
	}
	return systemVersionsFromHosts(list), nil
}

// systemVersionsFromHosts: the reply for a hosts list read in the same request. pollProblem says whether a failing host's
// polls have failed long enough to open its poll problem (host_poll_health.go): that confirmed failure tones the version
// mark; a single failed poll that the next good poll clears does not.
func systemVersionsFromHosts(list contract.HostsListReply) contract.SystemVersionsReply {
	target := healthyTarget()
	hosts := []contract.HostVersion{}
	for _, h := range list.Hosts {
		row := contract.HostVersion{Id: h.Id, Version: h.Version, AutoUpdate: h.AutoUpdate, Update: h.Update, LastSeenAt: h.LastSeenAt, PollError: h.PollError}
		if b, ok := versionBehindTarget(valueOr(h.Version), target).(bool); ok {
			// A host whose executor this release does not change is not behind (release pacing).
			b = b && (h.Pacing == nil || h.Pacing.State != contract.UpdatePacingStateCurrent)
			row.Behind = &b
		}
		// The same rule that opens the host's poll problem (notePollFailure), read from the record itself: the problem
		// is owner-addressed with no project, so a project-scoped reader could not see the question row.
		if h.PollError != nil && float64(list.ServerNow)-h.PollError.Since >= hostPollProblemAfterMs {
			row.PollProblem = ptr(true)
		}
		hosts = append(hosts, row)
	}
	coord := contract.CoordinatorVersion{Version: version, Started: coordinatorStarted.Format(time.RFC3339), Offered: target}
	if p := ownRelease().Previous; p != "" {
		coord.Previous = &p
	}
	if c := buildCommit(); c != "" {
		coord.Commit = &c
	}
	return contract.SystemVersionsReply{Coordinator: coord, Ui: uiRelease(), Hosts: hosts, ServerNow: list.ServerNow, StaleAfterMs: list.StaleAfterMs,
		Gateway: gatewayVersion()}
}

// Missing or non-release values do not establish an update comparison.
func versionBehindTarget(installed string, offered *string) any {
	if offered == nil || !releaseVersionRe.MatchString(installed) || !releaseVersionRe.MatchString(*offered) {
		return nil
	}
	return olderVersion(installed, *offered)
}

func buildCommit() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			if s.Key == "vcs.revision" {
				return s.Value
			}
		}
	}
	return ""
}

// uiReleaseManifest: release.json of the independently installed UI release that appRoutes serves, when it is one: kind
// ui and built for this backend version. ok false means the site serves the bundled app. The one rule for appRoutes,
// uiServed and system_versions.
type uiManifest struct{ Kind, Version, BackendVersion, Commit string }

func selectedUIRelease() (dir string, m uiManifest, ok bool) {
	root := os.Getenv("FLEET_UI_ROOT")
	if root == "" {
		return "", m, false
	}
	cur, err := filepath.EvalSymlinks(filepath.Join(root, "current"))
	if err != nil {
		return "", m, false
	}
	b, err := os.ReadFile(filepath.Join(cur, "release.json"))
	if err != nil || json.Unmarshal(b, &m) != nil || m.Kind != "ui" || m.BackendVersion != version {
		return "", m, false
	}
	return cur, m, true
}

// uiRelease: the UI the site serves, as GET /ui-release.json reports it.
func uiRelease() contract.UiVersion {
	if _, m, ok := selectedUIRelease(); ok {
		out := contract.UiVersion{Kind: contract.UiKindUi, Version: m.Version, BackendVersion: m.BackendVersion}
		if m.Version == "" {
			out.Version = version
		}
		if m.Commit != "" {
			out.Commit = &m.Commit
		}
		return out
	}
	return contract.UiVersion{Kind: contract.UiKindBundled, Version: version, BackendVersion: version}
}

// The gateway's state is probed outside any request: a read returns the last probe at once (never holding its
// transaction on the network) and starts a new probe (coalesced, background.go) when that one is older than a minute;
// pending until the first probe. A failed probe is repeated at once, so unreachable means the probe and its re-probe
// both failed: one dropped request is not reported as an outage.
var gatewayProbe struct {
	sync.Mutex
	at   time.Time
	last *contract.GatewayVersion
}

var gatewayProbeNow = coalesced("gateway probe", func(ctx context.Context, _ struct{}) error {
	d := probeGateway(ctx)
	if d.State == contract.GatewayStateUnreachable {
		d = probeGateway(ctx)
	}
	d.ObservedAt = ptr(float64(time.Now().UnixMilli()))
	gatewayProbe.Lock()
	gatewayProbe.last, gatewayProbe.at = &d, time.Now()
	gatewayProbe.Unlock()
	return nil
})

func gatewayVersion() *contract.GatewayVersion {
	if gatewayURL() == "" {
		return nil
	}
	gatewayProbe.Lock()
	defer gatewayProbe.Unlock()
	if time.Since(gatewayProbe.at) > time.Minute {
		gatewayProbe.at = time.Now()
		gatewayProbeNow(struct{}{})
	}
	if gatewayProbe.last == nil {
		return &contract.GatewayVersion{State: contract.GatewayStatePending}
	}
	last := *gatewayProbe.last
	return &last
}

// probeGateway: GET /healthz of fleet-gateway (go/gateway), with the Access headers gatewayCall uses for https.
func probeGateway(ctx context.Context) contract.GatewayVersion {
	unreachable := contract.GatewayVersion{State: contract.GatewayStateUnreachable}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", gatewayURL()+"/healthz", nil)
	if err != nil {
		return unreachable
	}
	if strings.HasPrefix(gatewayURL(), "https://") {
		for k, v := range cfAccessHeaders() {
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return unreachable
	}
	defer res.Body.Close()
	var h struct{ Status, Version string }
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&h) != nil || (h.Status != "ok" && h.Status != "draining") {
		return unreachable
	}
	out := contract.GatewayVersion{State: contract.GatewayState(h.Status)}
	if h.Version != "" {
		out.Version = &h.Version
	}
	return out
}
