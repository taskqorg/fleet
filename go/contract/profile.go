package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ParseProfile decodes an agent profile (contracts/profile.schema.json) and checks the rules the schema subset cannot
// say. It is the one validator: the coordinator runs it on config/agents/*.json, the executor before any spawn, so a
// profile the coordinator dispatches is never refused on the host.
func ParseProfile(raw []byte) (Profile, error) {
	var p Profile
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid profile: %w", err)
	}
	return p, p.check()
}

func badArg(s string) bool { return s == "" || s[0] == '-' }

func (p Profile) check() error {
	if p.Id == "" || len(p.Id) > 80 || badArg(p.Model) || len(p.Instructions) > 16384 {
		return errors.New("unsupported agent profile: id, model or instructions")
	}
	if len(p.McpServers) > 1 {
		return errors.New("at most one design-rules grant")
	}
	for _, s := range p.McpServers {
		if data, _ := json.Marshal(s.Data); s.Id != "design-rules" || s.Data == nil || len(data) > 16384 {
			return errors.New("unsupported MCP grant")
		}
	}
	for _, l := range []struct {
		ids  []string
		max  int
		what string
	}{{p.DelegationProfiles, 8, "delegation grant"}, {p.Skills, 32, "profile skills"}, {p.Connections, 32, "profile connections"}} {
		if len(l.ids) > l.max {
			return errors.New("invalid " + l.what)
		}
		for i, id := range l.ids {
			if id == "" || len(id) > 80 || strings.Trim(id, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" || slices.Contains(l.ids[:i], id) {
				return errors.New("invalid " + l.what)
			}
		}
	}
	role := ""
	if p.InstructionRole != nil {
		role = *p.InstructionRole
	}
	harness := ProfileHarnessCodex
	if p.Harness != nil {
		harness = *p.Harness
	}
	switch harness {
	case ProfileHarnessCodex:
		if p.Effort == nil || badArg(*p.Effort) || role != "" && role != "developer" || p.Tools != nil || p.Settings != nil {
			return errors.New("unsupported Codex profile: effort, instruction role, tools or settings")
		}
	case ProfileHarnessClaude:
		if p.Effort != nil || role != "system" || p.Tools == nil || p.Settings != nil {
			return errors.New("unsupported Claude profile: effort, instruction role, tools or settings")
		}
		for _, t := range p.Tools {
			if !slices.Contains([]ClaudeTool{ClaudeToolRead, ClaudeToolEdit, ClaudeToolWrite, ClaudeToolBash, ClaudeToolGlob, ClaudeToolGrep}, t) {
				return errors.New("unsupported Claude tool grant")
			}
		}
	case ProfileHarnessAntigravity, ProfileHarnessAcp:
		if p.Effort != nil || role != "user" || p.Tools != nil {
			return errors.New("unsupported Antigravity profile: effort, instruction role or tools")
		}
		return p.Settings.Check()
	default:
		return errors.New("unknown harness")
	}
	return nil
}

// Check accepts only the Antigravity settings Fleet maps to native flags; nil is no settings.
func (s *AntigravitySettings) Check() error {
	if s == nil {
		return nil
	}
	if s.IncludeDefaultCustomizations != nil && *s.IncludeDefaultCustomizations || s.Mode != nil && *s.Mode != AntigravityModePlan && *s.Mode != AntigravityModeAcceptEdits {
		return errors.New("unmapped Antigravity setting")
	}
	return nil
}
