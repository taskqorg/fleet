package main

// Agent profiles (agentConfig): validation, evidence, Codex flags, MCP servers.

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
)

func sha(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

func profileHash(v any) string { return sha([]byte(enc(v))) }

func acp(h string) bool { return h == "antigravity" || h == "acp" }

func exactKeys(v any, keys ...string) bool {
	o, ok := v.(M)
	if !ok || len(o) != len(keys) {
		return false
	}
	for _, k := range keys {
		if _, ok := o[k]; !ok {
			return false
		}
	}
	return true
}

func badText(v any, max int) bool { s, ok := v.(string); return !ok || len(s) > max }
func badArg(v any) bool           { s, ok := v.(string); return !ok || s == "" || s[0] == '-' }
func badID(v any) bool            { s, ok := v.(string); return !ok || s == "" || len(s) > 80 }

// profileValidate accepts only the profile shapes Fleet can apply to each harness.
func profileValidate(v any) error {
	o := jObj(v)
	if h, ok := o["harness"].(string); ok && h != "codex" {
		role := "user"
		if h == "claude" {
			role = "system"
		}
		if badID(o["id"]) || !(h == "claude" || acp(h)) || o["instructionRole"] != role ||
			((h == "claude" || acp(h)) && o["effort"] != nil) || badArg(o["model"]) || badText(o["instructions"], 16384) {
			return errors.New("unsupported native profile role/settings/grants")
		}
		known := []string{"id", "harness", "model", "effort", "instructionRole", "instructions", "mcpServers", "tools", "delegationProfiles", "settings", "fleetTools", "collaborationTools", "skills", "connections", "fleetOperations"}
		if o == nil {
			return errors.New("unknown native profile setting")
		}
		for k := range o {
			if !contains(known, k) {
				return errors.New("unknown native profile setting")
			}
		}
		if h == "claude" {
			tools, ok := o["tools"].([]any)
			if !ok {
				return errors.New("unsupported Claude tool grant")
			}
			for _, t := range tools {
				if !contains([]any{"Read", "Edit", "Write", "Bash", "Glob", "Grep"}, t) {
					return errors.New("unsupported Claude tool grant")
				}
			}
		}
		if _, has := o["tools"]; acp(h) && has {
			return errors.New("Antigravity tool allowlist unsupported")
		}
		grants := jObj(jClone(o))
		for _, k := range []string{"harness", "instructionRole", "tools", "settings"} {
			delete(grants, k)
		}
		grants["model"], grants["effort"] = "gpt-6.1-sol", "low"
		if err := profileValidate(grants); err != nil {
			return err
		}
		if _, has := o["settings"]; h == "claude" && has {
			return errors.New("unmapped Claude settings")
		}
		if acp(h) {
			return validateAntigravitySettings(o["settings"])
		}
		return nil
	}
	if r, has := o["instructionRole"]; has && r != "developer" {
		return errors.New("unsupported Codex instruction role/tools")
	}
	if _, has := o["tools"]; has {
		return errors.New("unsupported Codex instruction role/tools")
	}
	base := jObj(jClone(o))
	delete(base, "harness")
	delete(base, "instructionRole")
	for _, k := range []string{"fleetTools", "collaborationTools", "fleetOperations"} {
		if x, has := base[k]; has {
			delete(base, k)
			if _, ok := x.(bool); !ok {
				return errors.New("invalid tool setting")
			}
		}
	}
	for _, l := range []struct {
		key string
		max int
		err string
	}{{"delegationProfiles", 8, "invalid delegation grant"}, {"skills", 32, "invalid profile skills"}, {"connections", 32, "invalid profile connections"}} {
		list, has := base[l.key]
		if !has {
			continue
		}
		delete(base, l.key)
		ids, ok := list.([]any)
		if !ok || len(ids) > l.max {
			return errors.New(l.err)
		}
		for i, id := range ids {
			s, ok := id.(string)
			if !ok || badID(s) || !onlyBytes(s, func(b byte) bool { return b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' }) || contains(ids[:i], id) {
				return errors.New(l.err)
			}
		}
	}
	if !exactKeys(base, "id", "model", "effort", "instructions", "mcpServers") || badID(o["id"]) || badArg(o["model"]) || badArg(o["effort"]) || badText(o["instructions"], 16384) {
		return errors.New("unsupported agent profile")
	}
	servers, ok := o["mcpServers"].([]any)
	if !ok {
		return errors.New("mcpServers must be an array")
	}
	if len(servers) > 1 {
		return errors.New("at most one design-rules grant")
	}
	for _, s := range servers {
		if !exactKeys(s, "id", "data") || jStr(s, "id") != "design-rules" || jObj(s, "data") == nil || len(enc(jAt(s, "data"))) > 16384 {
			return errors.New("unsupported MCP grant")
		}
	}
	return nil
}

// validateAntigravitySettings: nil means absent.
func validateAntigravitySettings(settings any) error {
	if settings == nil {
		return nil
	}
	o, ok := settings.(M)
	if !ok {
		return errors.New("invalid Antigravity settings")
	}
	for k, v := range o {
		_, isBool := v.(bool)
		switch {
		case k == "sandbox" && isBool:
		case k == "includeDefaultCustomizations" && v == false:
		case k == "mode" && (v == "plan" || v == "accept-edits"):
		default:
			return errors.New("unmapped Antigravity setting")
		}
	}
	return nil
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
