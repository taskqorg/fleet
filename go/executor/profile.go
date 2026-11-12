package main

// Agent profiles (agentConfig): validation, evidence, Codex flags, MCP servers.

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"

	"fleet/contract"
)

func sha(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func profileHash(v any) string { return sha([]byte(enc(v))) }

func acp(h string) bool { return h == "antigravity" || h == "acp" }

// profileValidate accepts only the profile shapes Fleet can apply to each harness (contract.ParseProfile, the validator
// the coordinator uses too).
func profileValidate(v any) error {
	_, err := contract.ParseProfile([]byte(enc(v)))
	return err
}

// validateAntigravitySettings checks a start command's Antigravity settings with the profile rules.
func validateAntigravitySettings(settings any) error {
	if settings == nil {
		return nil
	}
	_, err := contract.ParseAntigravitySettings([]byte(enc(settings)))
	return err
}

func exe() string { p, _ := os.Executable(); return p }

// profileServers maps named grants to Fleet-owned launchers; definitions never provide commands or environment.
func profileServers(v any, root, harness, account string) (M, error) {
	servers := M{}
	for _, grant := range jArr(v, "mcpServers") {
		name := jStr(grant, "id")
		if name == "design-rules" {
			servers[name] = M{"command": exe(), "args": []any{"mcp", "design-rules", enc(jAt(grant, "data"))}, "tools": []any{"get_design_rules"}}
			continue
		}
		binding, err := namedMCPBinding(os.Getenv("FLEET_NATIVE_ACCOUNTS"), account, name, root)
		if err != nil {
			return nil, err
		}
		binding["tools"] = jAt(grant, "data", "tools")
		servers[name] = binding
	}
	return servers, nil
}

// withSkills appends skill text to Codex developer instructions (skill discovery is disabled in Fleet's Codex runs).
func withSkills(v any, skills any) M {
	out := jObj(jClone(v))
	if out == nil {
		out = M{}
	}
	text := jStr(v, "instructions")
	for _, s := range jArr(skills) {
		if jIsStr(s, "id") && jIsStr(s, "text") {
			text += "\n\n# Skill: " + jStr(s, "id") + "\n\n" + jStr(s, "text")
		}
	}
	out["instructions"] = text
	return out
}

// profileApply: JSON string quoting is also valid TOML basic-string quoting for these values.
func profileApply(c *exec.Cmd, v any) {
	c.Args = append(c.Args, "-c", "developer_instructions="+enc(jAt(v, "instructions")))

}

func profilePrepare(v any, account string, skills any) (M, M, error) {
	capabilityEvidence := M{}
	if err := profileValidate(v); err != nil {
		return nil, nil, err
	}
	selected, err := profileServers(v, "", "", account)
	if err != nil {
		return nil, nil, err
	}
	for name, value := range selected {
		if name == "design-rules" {
			continue
		}
		binding := jObj(value)
		capabilityEvidence[name] = M{"command": binding["command"], "dependencyPaths": binding["dependencies"], "selectedTools": binding["tools"], "transport": "stdio", "ownedDependencies": binding["ownedDependencies"], "bindingSha256": profileHash(binding), "nativeToolCalls": "unqualified until observed", "childHomePolicy": "fresh execution-owned MCP home; harness credential home unchanged"}
	}

	o := jObj(v)
	if h, ok := o["harness"].(string); ok && h != "codex" {
		return M{"capabilityReadiness": capabilityEvidence, "requested": v, "profileSha256": profileHash(v), "applied": M{"harness": o["harness"], "model": o["model"], "instructionRole": o["instructionRole"],
			"instructions": o["instructions"], "tools": o["tools"], "mcpServers": o["mcpServers"], "delegationProfiles": o["delegationProfiles"], "settings": o["settings"],
			"nativeReadiness": "unknown", "nativePermissions": "dontAsk for Claude; native for Antigravity; no sandbox parity"}}, selected, nil
	}
	applied := withSkills(v, skills)
	resolvedSkills := []any{}
	for _, skill := range jArr(skills) {
		resolvedSkills = append(resolvedSkills, M{"id": jAt(skill, "id"), "sha256": sha([]byte(jStr(skill, "text")))})
	}
	bin, err := os.ReadFile(exe())
	if err != nil {
		return nil, nil, err
	}
	return M{"capabilityReadiness": capabilityEvidence, "instructionSources": M{"profile": "explicit developer_instructions", "personalConfig": "not loaded", "personalHome": "not forwarded", "projectInstructions": "discovery disabled", "skills": "explicit resolved snapshots; native discovery disabled", "memories": "disabled", "hooks": "disabled", "nativeObservation": "pending rollout provenance check"},
		"requested": v, "resolvedSkills": resolvedSkills, "profileSha256": profileHash(v), "instructionsSha256": sha([]byte(jStr(applied, "instructions"))), "grantsSha256": profileHash(o["mcpServers"]),
		"applied": M{"model": o["model"], "effort": o["effort"], "instructions": applied["instructions"], "mcpServers": o["mcpServers"],
			"mapping":             "developerInstructions on open; acknowledged developer-role thread/inject_items before resumed turn; native model-context application unqualified",
			"availability":        "unknown until native call/result",
			"resumeCompatibility": "requested overrides supplied per invocation; actual changed settings unqualified; prior conversation context persists",
			"mcpServerSha256":     sha(bin)}}, selected, nil
}

// Apply the same executor-resolved text used by the native protocol and preparation receipt.
func applyResolvedInstructions(cmd *exec.Cmd, harness, text string) {
	if harness == "codex" {
		profileApply(cmd, M{"instructions": text})
	}
	if harness == "claude" {
		for i := 1; i+1 < len(cmd.Args); i++ {
			if cmd.Args[i] == "--system-prompt" {
				cmd.Args[i+1] = text
			}
		}
	}
}
