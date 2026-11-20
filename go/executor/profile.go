package main

// Agent profiles (agentConfig): validation, evidence, Codex flags, MCP servers.

import (
	"crypto/sha256"
	"encoding/json"
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

// profileGrants: the profile's MCP grants (a profile that does not fit names none; profileValidate refuses it first).
func profileGrants(v any) []contract.ProfileMcpGrant {
	p, _ := contractOf[struct {
		McpServers []contract.ProfileMcpGrant `json:"mcpServers"`
	}](v)
	return p.McpServers
}

// jsonOf: a decoded value as the journal reads JSON (numbers kept as written), so enc writes it as before.
func jsonOf(v any) any {
	b, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return decRaw(b)
}

// profileServers maps named grants to Fleet-owned launchers; definitions never provide commands or environment.
func profileServers(v any, root, harness, account string) (M, error) {
	servers := M{}
	for _, grant := range profileGrants(v) {
		name := grant.Id
		if name == "design-rules" {
			discovery := M{"tools": designRulesTools}
			servers[name] = M{"command": exe(), "args": []any{"mcp", "design-rules", enc(jsonOf(grant.Data))}, "tools": mcpToolNames(discovery)}
			continue
		}
		binding, err := namedMCPBinding(os.Getenv("FLEET_NATIVE_ACCOUNTS"), account, name, root)
		if err != nil {
			return nil, err
		}
		binding["tools"] = decRaw(grant.Data["tools"])
		servers[name] = binding
	}
	return servers, nil
}

// skillItem: one resolved skill snapshot of a start payload; an item that is not an object reads as empty.
type skillItem struct {
	Id   jsonValue `json:"id"`
	Text jsonValue `json:"text"`
}

func skillItems(skills any) []skillItem { return typed[[]skillItem](skills) }

// skillText appends skill text to Codex developer instructions (skill discovery is disabled in Fleet's Codex runs); a
// skill without a string id and text is skipped.
func skillText(instructions string, skills any) string {
	for _, s := range skillItems(skills) {
		id, idOK := valStr(s.Id)
		text, textOK := valStr(s.Text)
		if idOK && textOK {
			instructions += "\n\n# Skill: " + id + "\n\n" + text
		}
	}
	return instructions
}

// launchFields: the strings a harness adapter reads from its launch map (the start payload, or the agent profile in it);
// a field that is missing or not a string reads as "".
type launchFields struct {
	Model, Instructions, CodeMode, Text string
}

func launchOf(v any) launchFields {
	raw := typed[struct {
		Model        string `json:"model"`
		Instructions string `json:"instructions"`
		CodeMode     string `json:"codeMode"`
		Text         string `json:"text"`
	}](v)
	return launchFields{Model: raw.Model, Instructions: raw.Instructions, CodeMode: raw.CodeMode, Text: raw.Text}
}

// profileInstructions: the profile's instructions ("" when not a string).
func profileInstructions(v any) string {
	return typed[struct {
		Instructions string `json:"instructions"`
	}](v).Instructions
}

// profileApply: JSON string quoting is also valid TOML basic-string quoting for these values.
func profileApply(c *exec.Cmd, instructions string) {
	c.Args = append(c.Args, "-c", "developer_instructions="+enc(instructions))

}

func profilePrepare(v any, account string, skills any) (M, M, error) {
	capabilityEvidence := M{}
	profile, err := contract.ParseProfile([]byte(enc(v)))
	if err != nil {
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
		binding, _ := value.(M) // built by namedMCPBinding
		capabilityEvidence[name] = M{"command": binding["command"], "dependencyPaths": binding["dependencies"], "selectedTools": binding["tools"], "transport": "stdio", "ownedDependencies": binding["ownedDependencies"], "bindingSha256": profileHash(binding), "nativeToolCalls": "unqualified until observed", "childHomePolicy": "fresh execution-owned MCP home; harness credential home unchanged"}
	}

	// The evidence echoes the requested fields as sent, so their hashes stay those of the received profile.
	fields := typed[map[string]jsonValue](v)
	o := func(k string) any { return fields[k].V }
	if profile.Harness != nil && *profile.Harness != contract.ProfileHarnessCodex {
		return M{"capabilityReadiness": capabilityEvidence, "requested": v, "profileSha256": profileHash(v), "applied": M{"harness": o("harness"), "model": o("model"), "instructionRole": o("instructionRole"),
			"instructions": o("instructions"), "tools": o("tools"), "mcpServers": o("mcpServers"), "delegationProfiles": o("delegationProfiles"), "settings": o("settings"),
			"nativeReadiness": "unknown", "nativePermissions": "dontAsk for Claude; native for Antigravity; no sandbox parity"}}, selected, nil
	}
	applied := skillText(profile.Instructions, skills)
	resolvedSkills := []any{}
	for _, skill := range skillItems(skills) {
		text, _ := valStr(skill.Text)
		resolvedSkills = append(resolvedSkills, M{"id": skill.Id.V, "sha256": sha([]byte(text))})
	}
	bin, err := os.ReadFile(exe())
	if err != nil {
		return nil, nil, err
	}
	return M{"capabilityReadiness": capabilityEvidence, "instructionSources": M{"profile": "explicit developer_instructions", "personalConfig": "not loaded", "personalHome": "not forwarded", "projectInstructions": "discovery disabled", "skills": "explicit resolved snapshots; native discovery disabled", "memories": "disabled", "hooks": "disabled", "nativeObservation": "pending rollout provenance check"},
		"requested": v, "resolvedSkills": resolvedSkills, "profileSha256": profileHash(v), "instructionsSha256": sha([]byte(applied)), "grantsSha256": profileHash(o("mcpServers")),
		"applied": M{"model": o("model"), "effort": o("effort"), "instructions": applied, "mcpServers": o("mcpServers"),
			"mapping":             "developerInstructions on open; acknowledged developer-role thread/inject_items before resumed turn; native model-context application unqualified",
			"availability":        "unknown until native call/result",
			"resumeCompatibility": "requested overrides supplied per invocation; actual changed settings unqualified; prior conversation context persists",
			"mcpServerSha256":     sha(bin)}}, selected, nil
}

// Apply the same executor-resolved text used by the native protocol and preparation receipt.
func applyResolvedInstructions(cmd *exec.Cmd, harness, text string) {
	if harness == "codex" {
		profileApply(cmd, text)
	}
	if harness == "claude" {
		for i := 1; i+1 < len(cmd.Args); i++ {
			if cmd.Args[i] == "--system-prompt" {
				cmd.Args[i+1] = text
			}
		}
	}
}
