package main

import (
	"encoding/json"
	"fleet/contract"
	"slices"
	"strings"
)

// Session presets (docs/designs/fleet-conversations.md, "Start a conversation"): what a new conversation does. A preset
// names a project role and a system profile to fall back on, the access (read or write), whether the session gets its own
// branch, and a starter instruction added to every turn. A project lists its own in settings.presets (its definition, so
// people and agents edit them with project_update); without them the system presets below apply.
type preset struct {
	ID, Name, Description, Role, Profile, Access, Starter string
	Branch                                                bool
}

// systemPresets: the defaults Fleet ships. Each names a project role and the system profile used when the project does
// not map that role (a project without roles, such as cs), so a conversation can always start.
var systemPresets = []preset{
	{ID: "discuss", Name: "Discuss", Description: "Talk an idea or a question through. The code does not change; tasks and pipelines can be started.", Role: "pm", Profile: "pm", Access: "read"},
	{ID: "design", Name: "Design", Description: "Design or change an interface on its own branch, from the design system.", Role: "worker", Profile: "coding-worker", Access: "write", Branch: true,
		Starter: "Design work. Before you change an interface, read the project's design rules where the project has them: first the principles card at the top of docs/design-system/owner-rules.md (node scripts/ui-context.ts <page> prints it with the page's context), then the rules it links and the design book (docs/design-system/README.md); follow the card without being told. Where the project has packages/ui, build screens only from it and run pnpm run check:design."},
	{ID: "fix", Name: "Fix / build", Description: "Fix a bug or build a feature on its own branch.", Role: "worker", Profile: "coding-worker", Access: "write", Branch: true},
	{ID: "review", Name: "Review", Description: "Review code or a change and report the findings. The code does not change.", Role: "reviewer", Profile: "claude-reviewer", Access: "read"},
	{ID: "investigate", Name: "Investigate a problem", Description: "Find the cause of a problem in errors, logs and code. The code does not change; tasks can be written.", Role: "pm", Profile: "pm", Access: "read",
		Starter: "Investigate the problem the person describes. Read the project's errors with the bugsink connection tools where the project grants them (the Bugsink projects its settings name in errors.bugsink), the execution logs and telemetry through Fleet operations, and the code. Report the cause with its evidence and a proposed fix; do not change the code (you may write a task for the fix)."},
}

var presetKeys = []string{"id", "name", "description", "role", "profile", "access", "branch", "starter"}

// presetSettings: the project settings a preset reads (a typed view of the open settings object).
type presetSettings struct {
	Presets  []json.RawMessage `json:"presets"`
	Roles    map[string]string `json:"roles"`
	Worktree struct {
		BaseRef         string `json:"baseRef"`
		StartFromOrigin *bool  `json:"startFromOrigin"`
		BranchPrefix    string `json:"branchPrefix"`
	} `json:"worktree"`
	Repos []struct {
		DefaultBranch string `json:"defaultBranch"`
	} `json:"repos"`
}

func presetSettingsOf(settings rawObject) presetSettings {
	var s presetSettings
	jsonRoundTrip(settings, &s)
	return s
}

// parsePresets validates settings.presets: a list of {id, name, description?, role?, profile?, access?, branch?, starter?}.
// access defaults to write, branch to access == write. A preset names a role, a profile or both.
func parsePresets(v any) ([]preset, error) {
	var xs []json.RawMessage
	if b := jsonBytes(v); len(b) == 0 || b[0] != '[' || json.Unmarshal(b, &xs) != nil {
		return nil, fail(400, "Invalid project setting presets")
	}
	out, seen := []preset{}, map[string]bool{}
	for i, x := range xs {
		var o map[string]json.RawMessage
		bad := func(k string) error { return fail(400, "Invalid project setting presets["+itoa(i)+"]"+k) }
		if len(x) == 0 || x[0] != '{' || json.Unmarshal(x, &o) != nil {
			return nil, bad("")
		}
		str := map[string]string{}
		for k, y := range o {
			if !slices.Contains(presetKeys, k) {
				return nil, bad("." + k)
			}
			if k == "branch" {
				continue
			}
			var s string
			if len(y) == 0 || y[0] != '"' || json.Unmarshal(y, &s) != nil {
				return nil, bad("." + k)
			}
			str[k] = s
		}
		p := preset{ID: str["id"], Name: strings.TrimSpace(str["name"]), Description: str["description"], Role: str["role"], Profile: str["profile"], Access: firstString(str["access"], "write"), Starter: str["starter"]}
		if !idPattern.MatchString(p.ID) || seen[p.ID] {
			return nil, bad(".id")
		}
		seen[p.ID] = true
		if p.Name == "" {
			return nil, bad(".name")
		}
		if p.Role == "" && p.Profile == "" {
			return nil, bad(": a role or a profile")
		}
		if p.Access != "read" && p.Access != "write" {
			return nil, bad(".access")
		}
		p.Branch = p.Access == "write"
		if b, given := o["branch"]; given {
			if string(b) != "true" && string(b) != "false" {
				return nil, bad(".branch")
			}
			p.Branch = string(b) == "true"
			if p.Access == "write" && !p.Branch {
				return nil, bad(".branch: write access needs a branch")
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// presetsOf: the project's presets, or the system presets when it defines none (settings nil: outside projects; any
// JSON object value).
func presetsOf(settingsValue any) ([]preset, contract.SessionPresetSource, error) {
	var settings rawObject
	json.Unmarshal(jsonBytes(settingsValue), &settings)
	if v, ok := settings["presets"]; ok && len(presetSettingsOf(settings).Presets) > 0 {
		ps, err := parsePresets(v)
		return ps, contract.SessionPresetSourceProject, err
	}
	return systemPresets, contract.SessionPresetSourceSystem, nil
}

// resolvePreset: the profile a preset starts with in this project. Its role when the project maps it, else its own
// profile; role is "" when it fell back. unavailable says why it cannot start.
func resolvePreset(q *Request, p preset, settings rawObject) (role, profileID string, profile rawObject, unavailable string) {
	if id := presetSettingsOf(settings).Roles[p.Role]; p.Role != "" && id != "" {
		role, profileID = p.Role, id
	} else {
		profileID = p.Profile
	}
	if profileID == "" {
		return "", "", nil, "The project has no " + p.Role + " role"
	}
	if profile = roleProfile(q, profileID); profile == nil {
		return role, profileID, nil, "Profile " + profileID + " is not available"
	}
	return role, profileID, profile, ""
}

// callerReadOnly: the call comes from an agent whose own session is read-only; the sessions it creates are read-only too.
func callerReadOnly(q *Request) bool {
	id, ok := strings.CutPrefix(q.agent, "execution:")
	if !ok {
		return false
	}
	e, _ := executionMaybe(q, id)
	if e == nil {
		return false
	}
	s, _ := sessionMaybe(q, e.SessionID)
	return s != nil && s.Access == "read"
}

// profileInRoles: the project maps some role to this profile; an agent starts only such profiles.
func profileInRoles(settings rawObject, profileID string) bool {
	for _, v := range presetSettingsOf(settings).Roles {
		if v == profileID && profileID != "" {
			return true
		}
	}
	return false
}

func sessionPresets(q *Request, r *contract.SessionPresetsRequest) (contract.SessionPresetsReply, error) {
	out := contract.SessionPresetsReply{Presets: []contract.SessionPreset{}}
	var settings rawObject
	if pid := deref(r.ProjectId); pid != "" {
		if settings = projectSettingsOf(q, pid); settings == nil || (q.person != nil && !mayAccessProjectID(q.person, pid)) {
			return out, fail(404, "Unknown project")
		}
		ps := presetSettingsOf(settings)
		wt := ps.Worktree
		base := "HEAD"
		if wt.BaseRef != "" {
			base = wt.BaseRef
		} else if wt.StartFromOrigin == nil || *wt.StartFromOrigin {
			branch := ""
			if len(ps.Repos) > 0 {
				branch = ps.Repos[0].DefaultBranch
			}
			base = "origin/" + firstString(branch, "main")
		}
		out.BranchPrefix, out.Base = ptr(firstString(wt.BranchPrefix, "fleet")), &base
	}
	ps, source, err := presetsOf(settings)
	if err != nil {
		return out, err
	}
	for _, p := range ps {
		role, profileID, profile, unavailable := resolvePreset(q, p, settings)          // ponytail: one profile read per preset (five); cache by id if presets grow
		if unavailable == "" && q.agent != "" && !profileInRoles(settings, profileID) { // what session_start refuses an agent
			unavailable = "Your project maps no role to profile " + profileID
		}
		if unavailable == "" && settings == nil && !mayAccessProjectID(q.person, "") { // what session_start refuses outside projects
			unavailable = "Starting outside projects needs full access"
		}
		x := contract.SessionPreset{Id: p.ID, Name: p.Name, Description: p.Description, Access: contract.SessionAccess(p.Access), Branch: p.Branch, Source: source,
			Role: optStr(role), ProfileId: optStr(profileID), Starter: optStr(p.Starter), Unavailable: optStr(unavailable)}
		if profile != nil {
			var pr struct {
				Harness string `json:"harness"`
				Model   string `json:"model"`
				Effort  string `json:"effort"`
			}
			jsonRoundTrip(profile, &pr)
			x.Harness, x.Model, x.Effort = ptr(firstString(pr.Harness, "codex")), optStr(pr.Model), optStr(pr.Effort)
		}
		out.Presets = append(out.Presets, x)
	}
	return out, nil
}

func init() {
	op("session_presets", sessionPresets)
}
