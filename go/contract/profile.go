package contract

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
)

// ParseProfile decodes an agent profile (contracts/profile.schema.json) and checks the rules the schema subset cannot
// say. It is the one validator: the coordinator runs it on config/agents/*.json, the executor before any spawn, so a
// profile the coordinator dispatches is never refused on the host.
func ParseProfile(raw []byte) (Profile, error) {
	var p Profile
	if err := strict(raw, &p); err != nil {
		return p, fmt.Errorf("invalid profile: %w", err)
	}
	var top map[string]json.RawMessage
	json.Unmarshal(raw, &top)
	for _, k := range []string{"id", "model", "instructions", "mcpServers"} {
		if _, ok := top[k]; !ok {
			return p, fmt.Errorf("invalid profile: %s is missing", k)
		}
	}
	return p, p.check()
}

// ParseAntigravitySettings decodes and checks a start command's Antigravity settings with the profile rules.
func ParseAntigravitySettings(raw []byte) (AntigravitySettings, error) {
	var s AntigravitySettings
	if err := strict(raw, &s); err != nil {
		return s, fmt.Errorf("unmapped Antigravity setting: %w", err)
	}
	return s, s.Check()
}

// strict decodes raw into v and refuses what encoding/json lets through: a key that is not exactly a field name
// (encoding/json matches names case-insensitively, so "mcpservers" would be checked while a reader of the raw map uses
// "mcpServers"), and null on anything but an optional string.
func strict(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	return exact(raw, reflect.TypeOf(v).Elem())
}

func exact(raw json.RawMessage, t reflect.Type) error {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Slice:
		var items []json.RawMessage
		json.Unmarshal(raw, &items)
		for _, item := range items {
			if err := exact(item, t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Struct:
		var fields map[string]json.RawMessage
		json.Unmarshal(raw, &fields)
		for k, value := range fields {
			f, ok := fieldByTag(t, k)
			if !ok {
				return fmt.Errorf("unknown field %q", k)
			}
			if string(value) == "null" {
				if f.Type != reflect.TypeOf((*string)(nil)) {
					return fmt.Errorf("%s must not be null", k)
				}
				continue
			}
			if err := exact(value, f.Type); err != nil {
				return err
			}
		}
	}
	return nil
}

func fieldByTag(t reflect.Type, name string) (reflect.StructField, bool) {
	for i := range t.NumField() {
		if tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); tag == name {
			return t.Field(i), true
		}
	}
	return reflect.StructField{}, false
}

var grantID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

func badArg(s string) bool { return s == "" || s[0] == '-' }

func (p Profile) check() error {
	if badArg(p.Id) || len(p.Id) > 80 || badArg(p.Model) || len(p.Instructions) > 16384 {
		return errors.New("unsupported agent profile: id, model or instructions")
	}
	seenServers := map[string]bool{}
	for _, server := range p.McpServers {
		if seenServers[server.Id] {
			return errors.New("duplicate MCP grant")
		}
		seenServers[server.Id] = true
		if server.Id != "design-rules" {
			if !grantID.MatchString(server.Id) || server.Id == "fleet" || server.Id == "connection" || len(server.Data) != 1 {
				return errors.New("MCP grant needs a unique named connection and explicit tools only")
			}
			var tools []string
			if json.Unmarshal(server.Data["tools"], &tools) != nil || len(tools) == 0 {
				return errors.New("MCP grant requires explicit tool names")
			}
			seen := map[string]bool{}
			for _, name := range tools {
				if name == "" || seen[name] || strings.IndexFunc(name, func(r rune) bool {
					return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-')
				}) >= 0 {
					return errors.New("invalid or duplicate MCP tool name")
				}
				seen[name] = true
			}
			continue
		}

		var data bytes.Buffer
		e := json.NewEncoder(&data)
		e.SetEscapeHTML(false)
		if server.Id != "design-rules" || server.Data == nil || e.Encode(server.Data) != nil || data.Len()-1 > 16384 {
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
			if !grantID.MatchString(id) || slices.Contains(l.ids[:i], id) {
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
