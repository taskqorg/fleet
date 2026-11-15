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
	operation("system_versions", true, typedOp(func(q *Request, _ *contract.SystemVersionsRequest) (contract.SystemVersionsReply, error) {
		out, err := systemVersions(q)
		return replyAs[contract.SystemVersionsReply](out, err)
	}))
}

func systemVersions(q *Request) (any, error) {
	hl, err := hostsList(q, false)
	if err != nil {
		return nil, err
	}
	list := hl.(doc)
	hosts := []doc{}
	for _, h := range list["hosts"].([]doc) {
		v, _ := h["version"].(string)
		hosts = append(hosts, doc{"id": h["id"], "version": h["version"], "autoUpdate": h["autoUpdate"], "update": h["update"], "lastSeenAt": h["lastSeenAt"],
			"behind": v != "" && olderVersion(v, version)})
	}
	coord := doc{"version": version, "started": coordinatorStarted.Format(time.RFC3339)}
	if p := ownRelease().Previous; p != "" {
		coord["previous"] = p
	}
	if t := healthyTarget(); t != nil {
		coord["offered"] = *t
	}
	if c := buildCommit(); c != "" {
		coord["commit"] = c
	}
	out := doc{"coordinator": coord, "ui": uiRelease(), "hosts": hosts, "serverNow": list["serverNow"], "staleAfterMs": list["staleAfterMs"]}
	if gw := gatewayVersion(q.ctx()); gw != nil {
		out["gateway"] = gw
	}
	return out, nil
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
func uiRelease() doc {
	if _, m, ok := selectedUIRelease(); ok {
		out := doc{"kind": "ui", "version": m.Version, "backendVersion": m.BackendVersion}
		if m.Version == "" {
			out["version"] = version
		}
		if m.Commit != "" {
			out["commit"] = m.Commit
		}
		return out
	}
	return doc{"kind": "bundled", "version": version, "backendVersion": version}
}

// The gateway's state is probed outside any request: a read returns the last probe at once (never holding its
// transaction on the network) and starts a new probe when that one is older than a minute.
var gatewayProbe struct {
	sync.Mutex
	at      time.Time
	last    doc
	running bool
}

func gatewayVersion(_ context.Context) doc {
	if gatewayURL() == "" {
		return nil
	}
	gatewayProbe.Lock()
	defer gatewayProbe.Unlock()
	if !gatewayProbe.running && time.Since(gatewayProbe.at) > time.Minute {
		gatewayProbe.running = true
		go func() {
			d := probeGateway()
			gatewayProbe.Lock()
			gatewayProbe.last, gatewayProbe.at, gatewayProbe.running = d, time.Now(), false
			gatewayProbe.Unlock()
		}()
	}
	return gatewayProbe.last
}

// probeGateway: GET /healthz of fleet-gateway (go/gateway), with the Access headers gatewayCall uses for https.
func probeGateway() doc {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", gatewayURL()+"/healthz", nil)
	if err != nil {
		return doc{"state": "unreachable"}
	}
	if strings.HasPrefix(gatewayURL(), "https://") {
		for k, v := range cfAccessHeaders() {
			req.Header.Set(k, v)
		}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return doc{"state": "unreachable"}
	}
	defer res.Body.Close()
	var h struct{ Status, Version string }
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&h) != nil || (h.Status != "ok" && h.Status != "draining") {
		return doc{"state": "unreachable"}
	}
	out := doc{"state": h.Status}
	if h.Version != "" {
		out["version"] = h.Version
	}
	return out
}
