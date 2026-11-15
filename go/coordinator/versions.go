package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"runtime/debug"
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

// uiRelease: the UI the site serves, as GET /ui-release.json reports it (appRoutes picks the same one).
func uiRelease() doc {
	if root := os.Getenv("FLEET_UI_ROOT"); root != "" {
		var m struct{ Kind, Version, BackendVersion, Commit string }
		if cur, err := filepath.EvalSymlinks(filepath.Join(root, "current")); err == nil {
			if b, err := os.ReadFile(filepath.Join(cur, "release.json")); err == nil && json.Unmarshal(b, &m) == nil && m.Kind == "ui" && m.BackendVersion == version && m.Version != "" {
				out := doc{"kind": "ui", "version": m.Version, "backendVersion": m.BackendVersion}
				if m.Commit != "" {
					out["commit"] = m.Commit
				}
				return out
			}
		}
	}
	return doc{"kind": "bundled", "version": version, "backendVersion": version}
}

// gatewayVersion: the gateway's GET /healthz version (go/gateway), nil when no gateway is configured.
func gatewayVersion(ctx context.Context) doc {
	base := gatewayURL()
	if base == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", base+"/healthz", nil)
	if err != nil {
		return doc{"state": "unreachable"}
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return doc{"state": "unreachable"}
	}
	defer res.Body.Close()
	var h struct{ Version string }
	if res.StatusCode != 200 || json.NewDecoder(res.Body).Decode(&h) != nil {
		return doc{"state": "unreachable"}
	}
	out := doc{"state": "ok"}
	if h.Version != "" {
		out["version"] = h.Version
	}
	return out
}
