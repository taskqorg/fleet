package main

import (
	"testing"

	"fleet/contract"
)

// Requirement: system_versions reports the coordinator, the served UI and each host's executor version in one read.
// Risk: the navigation's version mark shows a host as current while it runs an older release, or misses a host.
func TestSystemVersions(t *testing.T) {
	env := openTestCoordinator(t, false)
	old := version
	version = "1.261115.5"
	t.Cleanup(func() { version = old })
	for id, v := range map[string]string{"h-old": "1.261114.2", "h-new": "1.261115.5"} {
		secret := env.registerHost(t, id)
		v := v
		code, body := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{HostId: id, Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}, Version: &v},
			map[string]string{"Authorization": "Bearer " + secret})
		if code != 200 {
			t.Fatalf("poll %s: %d %v", id, code, body)
		}
	}
	code, body := env.callJSON(t, "POST", "/api/ops/system_versions", doc{}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("system_versions %d %v", code, body)
	}
	if c := body["coordinator"].(map[string]any); c["version"] != "1.261115.5" || c["started"] == "" {
		t.Fatalf("coordinator %v", c)
	}
	if u := body["ui"].(map[string]any); u["kind"] != "bundled" || u["version"] != "1.261115.5" {
		t.Fatalf("ui %v", u)
	}
	behind := map[string]any{}
	for _, h := range body["hosts"].([]any) {
		h := h.(map[string]any)
		behind[h["id"].(string)] = h["behind"]
	}
	if behind["h-old"] != nil || behind["h-new"] != nil {
		t.Fatalf("behind %v", behind)
	}
}

// Requirement: installed and offered releases are distinct observations; risk: unknown becomes current or behind.
func TestVersionBehindTarget(t *testing.T) {
	target := "1.261115.5"
	for _, tc := range []struct {
		installed string
		target    *string
		want      any
	}{
		{"1.261114.2", &target, true}, {target, &target, false}, {"dev", &target, nil}, {target, nil, nil},
	} {
		if got := versionBehindTarget(tc.installed, tc.target); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.installed, got, tc.want)
		}
	}
}
