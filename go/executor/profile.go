package main

// Agent profiles (agentConfig): validation, evidence, Codex flags, MCP servers.

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

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
	var s contract.AntigravitySettings
	d := json.NewDecoder(strings.NewReader(enc(settings)))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil {
		return errors.New("unmapped Antigravity setting")
	}
	return s.Check()
}

func exe() string { p, _ := os.Executable(); return p }

// profileServers: the design-rules server of a profile, as an MCP server spec.
func profileServers(v any) M {
	servers := M{}
	if s := jArr(v, "mcpServers"); len(s) > 0 {
		servers["design-rules"] = M{"command": exe(), "args": []any{"mcp", "design-rules", enc(jAt(s[0], "data"))}}
	}
	return servers
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
	if s := jArr(v, "mcpServers"); len(s) > 0 {
		args := []any{"mcp", "design-rules", enc(jAt(s[0], "data"))}
		c.Args = append(c.Args, "-c", "mcp_servers.design-rules.command="+enc(exe()), "-c", "mcp_servers.design-rules.args="+enc(args), "-c", "mcp_servers.design-rules.required=true")
	}
}

func profilePrepare(v any) (M, error) {
	if err := profileValidate(v); err != nil {
		return nil, err
	}
	o := jObj(v)
	if h, ok := o["harness"].(string); ok && h != "codex" {
		return M{"requested": v, "profileSha256": profileHash(v), "applied": M{"harness": o["harness"], "model": o["model"], "instructionRole": o["instructionRole"],
			"instructions": o["instructions"], "tools": o["tools"], "mcpServers": o["mcpServers"], "delegationProfiles": o["delegationProfiles"], "settings": o["settings"],
			"nativeReadiness": "unknown", "nativePermissions": "dontAsk for Claude; native for Antigravity; no sandbox parity"}}, nil
	}
	bin, err := os.ReadFile(exe())
	if err != nil {
		return nil, err
	}
	return M{"instructionSources": M{"profile": "explicit developer_instructions", "personalConfig": "not loaded", "personalHome": "not forwarded", "projectInstructions": "discovery disabled", "skills": "disabled", "memories": "disabled", "hooks": "disabled", "nativeObservation": "pending rollout provenance check"},
		"requested": v, "profileSha256": profileHash(v), "instructionsSha256": sha([]byte(jStr(v, "instructions"))), "grantsSha256": profileHash(o["mcpServers"]),
		"applied": M{"model": o["model"], "effort": o["effort"], "instructions": o["instructions"], "mcpServers": o["mcpServers"],
			"mapping":             "developerInstructions on open; acknowledged developer-role thread/inject_items before resumed turn; native model-context application unqualified",
			"availability":        "unknown until native call/result",
			"resumeCompatibility": "requested overrides supplied per invocation; actual changed settings unqualified; prior conversation context persists",
			"mcpServerSha256":     sha(bin)}}, nil
}
