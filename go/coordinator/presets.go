package main

import (
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
		Starter: "Design work. Before you change an interface, read the project's design rules: docs/design-system/owner-rules.md and the design book (docs/design-system/README.md) where the project has them. Where the project has packages/ui, build screens only from it and run pnpm run check:design."},
	{ID: "fix", Name: "Fix / build", Description: "Fix a bug or build a feature on its own branch.", Role: "worker", Profile: "coding-worker", Access: "write", Branch: true},
	{ID: "review", Name: "Review", Description: "Review code or a change and report the findings. The code does not change.", Role: "reviewer", Profile: "claude-reviewer", Access: "read"},
	{ID: "investigate", Name: "Investigate a problem", Description: "Find the cause of a problem in errors, logs and code. The code does not change; tasks can be written.", Role: "pm", Profile: "pm", Access: "read",
		Starter: "Investigate the problem the person describes. Read the project's errors with the bugsink connection tools where the project grants them (the Bugsink projects its settings name in errors.bugsink), the execution logs and telemetry through Fleet operations, and the code. Report the cause with its evidence and a proposed fix; do not change the code (you may write a task for the fix)."},
}

var presetKeys = []string{"id", "name", "description", "role", "profile", "access", "branch", "starter"}

// parsePresets validates settings.presets: a list of {id, name, description?, role?, profile?, access?, branch?, starter?}.
// access defaults to write, branch to access == write. A preset names a role, a profile or both.
func parsePresets(v any) ([]preset, error) {
	xs, ok := v.([]any)
	if !ok {
		return nil, fail(400, "Invalid project setting presets")
	}
	out, seen := []preset{}, map[string]bool{}
	for i, x := range xs {
		o, ok := x.(map[string]any)
		bad := func(k string) error { return fail(400, "Invalid project setting presets["+itoa(i)+"]"+k) }
		if !ok {
			return nil, bad("")
		}
		for k, y := range o {
			if !slices.Contains(presetKeys, k) {
				return nil, bad("." + k)
			}
			if _, s := y.(string); k != "branch" && !s {
				return nil, bad("." + k)
			}
		}
		p := preset{ID: astr(o["id"]), Name: strings.TrimSpace(astr(o["name"])), Description: astr(o["description"]), Role: astr(o["role"]), Profile: astr(o["profile"]), Access: firstString(o["access"], "write"), Starter: astr(o["starter"])}
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
			if p.Branch, ok = b.(bool); !ok {
				return nil, bad(".branch")
			}
			if p.Access == "write" && !p.Branch {
				return nil, bad(".branch: write access needs a branch")
			}
		}
		out = append(out, p)
	}
	return out, nil
}

// presetsOf: the project's presets, or the system presets when it defines none (settings nil: outside projects).
func presetsOf(settings doc) ([]preset, contract.SessionPresetSource, error) {
	if v, ok := settings["presets"]; ok && len(anys(v)) > 0 {
		ps, err := parsePresets(v)
		return ps, contract.SessionPresetSourceProject, err
	}
	return systemPresets, contract.SessionPresetSourceSystem, nil
}

// resolvePreset: the profile a preset starts with in this project. Its role when the project maps it, else its own
// profile; role is "" when it fell back. unavailable says why it cannot start.
func resolvePreset(q *Request, p preset, settings doc) (role, profileID string, profile doc, unavailable string) {
	if id := astr(mapOf(settings["roles"])[p.Role]); p.Role != "" && id != "" {
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
	e, _ := maybe(q, id, "execution")
	if e == nil {
		return false
	}
	s, _ := maybe(q, astr(e["sessionId"]), "session")
	return s != nil && s["access"] == "read"
}

// profileInRoles: the project maps some role to this profile; an agent starts only such profiles.
func profileInRoles(settings doc, profileID string) bool {
	for _, v := range mapOf(settings["roles"]) {
		if v == profileID && profileID != "" {
			return true
		}
	}
	return false
}

func sessionPresets(q *Request, r *contract.SessionPresetsRequest) (contract.SessionPresetsReply, error) {
	out := contract.SessionPresetsReply{Presets: []contract.SessionPreset{}}
	var settings doc
	if pid := deref(r.ProjectId); pid != "" {
		if settings = projectSettingsOf(q, pid); settings == nil || (q.person != nil && !mayAccessProjectID(q.person, pid)) {
			return out, fail(404, "Unknown project")
		}
		wt := mapOf(settings["worktree"])
		base := "HEAD"
		if b := astr(wt["baseRef"]); b != "" {
			base = b
		} else if wt["startFromOrigin"] != false {
			base = "origin/" + firstString(mapOf(firstOf(anys(settings["repos"])))["defaultBranch"], "main")
		}
		out.BranchPrefix, out.Base = ptr(firstString(wt["branchPrefix"], "fleet")), &base
	}
	ps, source, err := presetsOf(settings)
	if err != nil {
		return out, err
	}
	for _, p := range ps {
		role, profileID, profile, unavailable := resolvePreset(q, p, settings) // ponytail: one profile read per preset (five); cache by id if presets grow
		if unavailable == "" && q.agent != "" && !profileInRoles(settings, profileID) { // what session_start refuses an agent
			unavailable = "Your project maps no role to profile " + profileID
		}
		if unavailable == "" && settings == nil && !mayAccessProjectID(q.person, "") { // what session_start refuses outside projects
			unavailable = "Starting outside projects needs full access"
		}
		x := contract.SessionPreset{Id: p.ID, Name: p.Name, Description: p.Description, Access: contract.SessionAccess(p.Access), Branch: p.Branch, Source: source,
			Role: optStr(role), ProfileId: optStr(profileID), Starter: optStr(p.Starter), Unavailable: optStr(unavailable)}
		if profile != nil {
			x.Harness, x.Model, x.Effort = ptr(firstString(profile["harness"], "codex")), optStr(astr(profile["model"])), optStr(astr(profile["effort"]))
		}
		out.Presets = append(out.Presets, x)
	}
	return out, nil
}

func firstOf(xs []any) any {
	if len(xs) == 0 {
		return nil
	}
	return xs[0]
}

func init() {
	agentTypedOp("session_presets", true, sessionPresets)
	agentDescriptions["session_presets"] = "Presets for starting a session in a project (projectId; a person may omit it for outside projects, an agent reads its own project): id, name, what it does, the role or profile it resolves to (a system profile when the project does not map the role; an agent starts only profiles its project maps), access read|write, branch, starter instruction; unavailable says why one cannot start. Start with session_start preset=<id>"
}
