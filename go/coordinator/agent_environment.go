// Agent environment (docs/design-system/pages/changes.md, Agent environment): what every future agent gets, read with
// system_changes. Connection grants and key pools come from the grants drift check (grants.go); the sandbox is what
// the executor gives each profile's harness (go/executor harness_codex.go, executor_launch.go codeAuthorityFor).
package main

import (
	"encoding/json"
	"slices"
	"sort"

	"fleet/contract"
)

func agentEnvironment(q *Request) (contract.AgentEnvironment, error) {
	grants, err := grantsDrift(q, true)
	if err != nil {
		return contract.AgentEnvironment{}, err
	}
	profiles, err := listProfiles(q)
	if err != nil {
		return contract.AgentEnvironment{}, err
	}
	groups := map[[3]string]*contract.SandboxGroup{}
	for _, p := range profiles {
		h, mode, network := profileSandbox(jsonBytes(p))
		k := [3]string{h, mode, map[bool]string{true: "on"}[network]}
		if groups[k] == nil {
			groups[k] = &contract.SandboxGroup{Harness: h, Mode: mode, Network: network, Profiles: []string{}}
		}
		groups[k].Profiles = append(groups[k].Profiles, p.text("id"))
	}
	out := contract.AgentEnvironment{Grants: grants, Sandbox: []contract.SandboxGroup{}, Profiles: int64(len(profiles))}
	for _, g := range groups {
		sort.Strings(g.Profiles)
		out.Sandbox = append(out.Sandbox, *g)
	}
	sort.Slice(out.Sandbox, func(i, j int) bool {
		a, b := out.Sandbox[i], out.Sandbox[j]
		if a.Harness != b.Harness {
			return a.Harness < b.Harness
		}
		return len(a.Profiles) > len(b.Profiles)
	})
	return out, nil
}

// profileSandbox: a profile's harness, what its writable runs may write, and whether they reach the network, as the
// executor applies them: Codex writes its work area, its Go cache and the git roots (network only with gitNetwork),
// or runs without a sandbox when settings.sandbox is false; Claude's Bash runs unsandboxed, and a Claude profile
// without Bash has only its file tools; Antigravity and acp take their settings' sandbox and mode.
func profileSandbox(raw json.RawMessage) (harness, mode string, network bool) {
	var p struct {
		Harness    string   `json:"harness"`
		GitNetwork bool     `json:"gitNetwork"`
		Tools      []string `json:"tools"`
		Settings   struct {
			Sandbox *bool  `json:"sandbox"`
			Mode    string `json:"mode"`
		} `json:"settings"`
	}
	_ = json.Unmarshal(raw, &p)
	harness = harnessOfProfile(raw)
	switch harness {
	case "codex":
		if p.Settings.Sandbox != nil && !*p.Settings.Sandbox {
			return harness, "no sandbox on writable runs", true
		}
		return harness, "writes the work area, its Go cache and git roots", p.GitNetwork
	case "claude":
		if !slices.Contains(p.Tools, "Bash") {
			return harness, "file tools only, no Bash", false
		}
		return harness, "Bash without a sandbox", true
	default:
		mode = "sandbox"
		if p.Settings.Sandbox != nil && !*p.Settings.Sandbox {
			mode = "no sandbox"
		}
		if p.Settings.Mode != "" {
			mode += ", " + p.Settings.Mode
		}
		return harness, mode, p.GitNetwork
	}
}
