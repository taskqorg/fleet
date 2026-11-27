package main

import (
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"fleet/contract"
)

// Site access authors intent. The private grant independently approves the target and its role bindings.
func parseAgentSiteAccess(raw json.RawMessage, roles rawObject) (*contract.AgentSiteAccess, error) {
	bad := func(field string) error { return fail(400, "Invalid agentSiteAccess "+field) }
	if jsonKind(raw) != '{' || hasNullSiteFields(raw) {
		return nil, bad("setting")
	}
	config, err := decodeValid[contract.AgentSiteAccess](raw, true)
	if err != nil {
		return nil, err
	}
	if config.Mode == contract.AgentSiteAccessModeNotAWebApp {
		if len(config.Targets) != 0 || config.DefaultTargetId != nil {
			return nil, bad("non-web targets")
		}
		return config, nil
	}

	ids := map[string]bool{}
	type destination struct {
		origin string
		scope  contract.AgentSiteAccessScope
	}
	seen := map[destination]bool{}
	for _, t := range config.Targets {
		if !idPattern.MatchString(t.Id) || ids[t.Id] {
			return nil, bad("target id")
		}
		ids[t.Id] = true
		dst := destination{t.Origin, t.Scope}
		if !contract.ValidHTTPSBrowserOrigin(t.Origin) || seen[dst] {
			return nil, bad("target origin or scope")
		}
		seen[dst] = true
		if len(t.Roles) == 0 {
			return nil, bad("target roles")
		}
		distinct := map[string]bool{}
		for _, role := range t.Roles {
			if !idPattern.MatchString(role) || distinct[role] || !idPattern.MatchString(rawText(roles[role])) {
				return nil, bad("target roles")
			}
			distinct[role] = true
		}
		if !validSiteProbe(t) {
			return nil, bad("probe or expected identity")
		}
	}
	if config.DefaultTargetId != nil && !ids[*config.DefaultTargetId] {
		return nil, bad("default target")
	}
	return config, nil
}

func hasNullSiteFields(raw json.RawMessage) bool {
	switch jsonKind(raw) {
	case '{':
		for _, val := range recOf(raw) {
			if hasNullSiteFields(val) {
				return true
			}
		}
	case '[':
		return slices.ContainsFunc(jsonList(raw), hasNullSiteFields)
	case 'n':
		return true
	}
	return false
}

func validSiteProbe(t contract.AgentSiteTarget) bool {
	p := t.Probe
	if !strings.HasPrefix(p.Path, "/") || strings.HasPrefix(p.Path, "//") ||
		strings.ContainsAny(p.Path, "\\?#") || strings.ContainsFunc(p.Path, unicode.IsSpace) {
		return false
	}
	u, err := url.ParseRequestURI(p.Path)
	if err != nil || u.Host != "" || strings.HasPrefix(u.Path, "//") ||
		strings.ContainsAny(u.Path, "\\") || strings.ContainsFunc(u.Path, unicode.IsControl) {
		return false
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	if p.ExpectedStatus < 200 || p.ExpectedStatus > 599 {
		return false
	}
	if t.AuthKind == contract.AgentSiteAuthKindNone {
		return t.ExpectedIdentity == nil && p.IdentityField == nil
	}
	return p.Method != contract.AgentSiteProbeMethodHEAD &&
		strings.TrimSpace(deref(t.ExpectedIdentity)) != "" && strings.TrimSpace(deref(p.IdentityField)) != "" &&
		!strings.ContainsFunc(deref(t.ExpectedIdentity)+deref(p.IdentityField), unicode.IsControl)
}

// selectSiteTarget shares selection rules between the mediator and the current-run projection.
// Read-only execution access cannot select an operator principal. A declared default applies only when eligible.
func selectSiteTarget(config *contract.AgentSiteAccess, roles rawObject, role, profile, access, targetID string) (*contract.AgentSiteTarget, string) {
	if config == nil {
		return nil, "applicability_unknown"
	}
	if config.Mode == contract.AgentSiteAccessModeNotAWebApp {
		return nil, "not_a_web_app"
	}
	if len(config.Targets) == 0 {
		return nil, "target_missing"
	}
	if role == "" || profile == "" || rawText(roles[role]) != profile {
		return nil, "role_not_eligible"
	}
	eligible := make([]*contract.AgentSiteTarget, 0, len(config.Targets))
	for i := range config.Targets {
		target := &config.Targets[i]
		if slices.Contains(target.Roles, role) &&
			(target.Scope == contract.AgentSiteAccessScopeVerifyReadOnly || access == "write") {
			eligible = append(eligible, target)
		}
	}
	for _, t := range eligible {
		if t.Id == firstString(targetID, deref(config.DefaultTargetId)) {
			return t, ""
		}
	}
	if len(eligible) == 0 || targetID != "" {
		return nil, "role_or_scope_not_eligible"
	}
	if len(eligible) != 1 {
		return nil, "target_selection_required"
	}
	return eligible[0], ""
}
