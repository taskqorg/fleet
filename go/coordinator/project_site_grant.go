package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"fleet/contract"
)

// Owner-installed, independent of the editable project settings. Changing either the approval
// or the credential bytes changes the evidence generation. The seed stays in the private store.
type siteAccessGrant struct {
	Target       contract.AgentSiteTarget `json:"target"`
	Profiles     map[string]string        `json:"profiles"`
	Seed         string                   `json:"generation"`
	ExpiresAt    *int64                   `json:"expiresAt,omitempty"`
	SiteReadOnly bool                     `json:"siteReadOnly,omitempty"`
	AccessFile   string                   `json:"accessFile,omitempty"`
	Headers      map[string]string        `json:"headers,omitempty"`
}

type siteGrantStatus struct {
	generation string
	expiresAt  *int64
	production *contract.BrowserProductionAccess
	reason     string
}

func readSiteAccessGrant(root, projectID string, target contract.AgentSiteTarget, roles rawObject, now int64) siteGrantStatus {
	var out siteGrantStatus
	out.reason = "grant_missing"
	if root == "" || !idPattern.MatchString(projectID) || !idPattern.MatchString(target.Id) {
		return out
	}
	dir := filepath.Join(root, "project-browser")
	if !filepath.IsAbs(dir) {
		out.reason = "grant_invalid"
		return out
	}
	info, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		return out
	}
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		out.reason = "grant_invalid"
		return out
	}
	raw, err := readPrivateBrowserFile(filepath.Join(dir, projectID+"."+target.Id+".grant.json"))
	if errors.Is(err, os.ErrNotExist) {
		return out
	}
	out.reason = "grant_invalid"
	var grant siteAccessGrant
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err != nil || d.Decode(&grant) != nil || !json.Valid(raw) || !validBrowserCredential(grant.Seed) || len(grant.Seed) < 32 {
		return out
	}
	if !siteGrantMatches(grant, target, roles) {
		out.reason = "grant_does_not_match"
		return out
	}
	out.expiresAt = grant.ExpiresAt
	if out.expiresAt != nil && *out.expiresAt <= now {
		out.reason = "credential_expired"
		return out
	}
	var credential []byte
	if target.AuthKind != contract.AgentSiteAuthKindNone {
		out.reason = "credential_unavailable"
		if !filepath.IsLocal(grant.AccessFile) || strings.ContainsAny(grant.AccessFile, `/\`) || grant.AccessFile == "." {
			return out
		}
		if credential, err = readPrivateBrowserFile(filepath.Join(dir, grant.AccessFile)); err != nil {
			return out
		}
	} else if grant.AccessFile != "" {
		return out
	}
	binding := contract.BrowserBindingProduction{Origin: target.Origin, Headers: grant.Headers,
		ReadinessProbe: contract.BrowserProductionProbe{Path: target.Probe.Path, Method: string(target.Probe.Method),
			Status: &target.Probe.ExpectedStatus, IdentityField: target.Probe.IdentityField, ExpectedIdentity: target.ExpectedIdentity}}
	production, err := binding.ResolveSite(string(credential))
	if err != nil || !siteProviderMatches(target.AuthKind, production) || len(jsonBytes(production)) >= contract.BrowserAccessBytes {
		return out
	}
	// Hash only in trusted code. The private random generation prevents guessing a low-entropy header
	// from the digest even if a host observation is exported.
	target.Roles = slices.Clone(target.Roles)
	slices.Sort(target.Roles)
	out.generation = sha256Text(string(raw) + "\x00" + string(credential) + "\x00" + canonical(target))
	out.production, out.reason = production, ""
	return out
}

func sameSiteTarget(a, b contract.AgentSiteTarget) bool {
	a.Roles, b.Roles = nil, nil
	return reflect.DeepEqual(a, b)
}

func siteGrantMatches(grant siteAccessGrant, target contract.AgentSiteTarget, roles rawObject) bool {
	if !sameSiteTarget(grant.Target, target) ||
		(target.Scope == contract.AgentSiteAccessScopeVerifyReadOnly && !grant.SiteReadOnly) {
		return false
	}
	for _, role := range target.Roles {
		if !slices.Contains(grant.Target.Roles, role) || grant.Profiles[role] == "" || grant.Profiles[role] != rawText(roles[role]) {
			return false
		}
	}
	return true
}

func siteProviderMatches(kind contract.AgentSiteAuthKind, production *contract.BrowserProductionAccess) bool {
	switch kind {
	case contract.AgentSiteAuthKindNone:
		return len(production.Headers) == 0
	case contract.AgentSiteAuthKindVerificationHeader:
		return len(production.Headers) == 1 && !strings.HasPrefix(strings.ToLower(production.Headers[0].Name), "cf-access-")
	case contract.AgentSiteAuthKindCloudflareAccess:
		return len(production.Headers) == 2 &&
			slices.ContainsFunc(production.Headers, func(h contract.BrowserSecretHeader) bool {
				return strings.EqualFold(h.Name, "CF-Access-Client-Id")
			}) &&
			slices.ContainsFunc(production.Headers, func(h contract.BrowserSecretHeader) bool {
				return strings.EqualFold(h.Name, "CF-Access-Client-Secret")
			})
	}
	return false
}

type siteAccessProject struct {
	Config       *contract.AgentSiteAccess     `json:"agentSiteAccess"`
	Roles        rawObject                     `json:"roles"`
	Environments []contract.ProjectEnvironment `json:"environments"`
	Execution    *contract.ProjectExecution    `json:"execution"`
}

func (p siteAccessProject) hostEligible(host string) bool {
	return p.Execution != nil && slices.Contains(p.Execution.Tools, "browser") &&
		slices.ContainsFunc(p.Environments, func(env contract.ProjectEnvironment) bool {
			return deref(env.HostId) == host
		})
}

func (p siteAccessProject) profileEligible(profile string) bool {
	return p.Execution != nil && (len(p.Execution.Profiles) == 0 || slices.Contains(p.Execution.Profiles, profile))
}

func (p siteAccessProject) target(id string) *contract.AgentSiteTarget {
	if p.Config != nil && p.Config.Mode == contract.AgentSiteAccessModeWeb {
		for i := range p.Config.Targets {
			if p.Config.Targets[i].Id == id {
				return &p.Config.Targets[i]
			}
		}
	}
	return nil
}

func managedSiteHolder(q *Request, email string) bool {
	holder := q.peopleSnapshot().person(email)
	return holder != nil && holder.Owner
}

func siteTargetForRun(q *Request, label string, project siteAccessProject, e agentExecution) (*contract.AgentSiteTarget, string, string) {
	if e.ProjectID != label || e.SessionID == "" || e.terminal() || isTrue(e.StopRequested) ||
		isTrue(e.TaskStopRequested) || deref(e.HostID) == "" {
		return nil, "", "run_not_active"
	}
	s, err := sessionByID(q, e.SessionID)
	if err != nil || s.ProjectID != label || s.DirectorAccessRevokedAt != nil {
		return nil, "", "run_not_eligible"
	}
	people := q.peopleSnapshot()
	dir := people.person(firstString(e.DirectedBy, s.DirectedBy))
	requester := people.person(firstString(e.TakenFrom, e.DirectedBy, s.DirectedBy))
	if dir == nil || requester == nil || isViewer(requester) || !mayAccessProjectID(requester, label) {
		return nil, s.Role, "director_not_eligible"
	}
	profile, ok := profileOf(e.AgentConfig)
	if !ok || !project.profileEligible(profile.ID) {
		return nil, s.Role, "profile_not_eligible"
	}
	if _, err := readTrustedProfile(q, profile.ID); err != nil {
		return nil, s.Role, "profile_not_eligible"
	}
	access := "read"
	if e.Dispatch != nil && deref(e.Dispatch.Access) == "write" && s.Access == "write" {
		access = "write"
	}
	target, reason := selectSiteTarget(project.Config, project.Roles, s.Role, profile.ID, access, s.SiteTargetID)
	if target == nil {
		return nil, s.Role, reason
	}
	snap := agentView[projectSnapshot](e.ProjectSnapshot)
	frozen := agentView[siteAccessProject](snap.Settings)
	old := frozen.target(target.Id)
	if old == nil || !sameSiteTarget(*old, *target) || !slices.Contains(old.Roles, s.Role) ||
		rawText(frozen.Roles[s.Role]) != profile.ID || !frozen.profileEligible(profile.ID) {
		return nil, s.Role, "run_binding_changed"
	}
	if !project.hostEligible(deref(e.HostID)) || !frozen.hostEligible(deref(e.HostID)) {
		return nil, s.Role, "host_not_eligible"
	}
	return target, s.Role, ""
}

// Managed requests are made by the trusted executor. Empty project lists the host's check targets.
// A project request with no execution checks one target. Execution requests resolve the calling run,
// and a request that already holds a generation revalidates it without returning credential values.
func managedSiteAccess(q *Request, r *contract.BrowserAccessRequest) (contract.BrowserAccessReply, error) {
	var out contract.BrowserAccessReply
	err := q.app.store.Read(q.ctx(), q, func(q *Request) error {
		if r.ProjectId == "" && r.TargetId == nil && r.ExecutionId == nil && r.Generation == nil {
			out.Managed = ptr(true)
			if !managedSiteHolder(q, q.host.Holder) {
				return nil
			}
			heads, err := headTexts(q, "project")
			if err != nil {
				return err
			}
			for _, h := range heads {
				p := agentView[siteAccessProject](projectHeadSettings(h.id, h.text))
				if p.Config != nil && p.Config.Mode == contract.AgentSiteAccessModeWeb && p.hostEligible(q.hostID()) {
					for _, t := range p.Config.Targets {
						out.Targets = append(out.Targets, contract.BrowserTargetRef{ProjectId: h.id, TargetId: t.Id})
					}
				}
			}
			return nil
		}
		settings, err := projectSettingsByID(q, r.ProjectId)
		if err != nil {
			return err
		}
		p := agentView[siteAccessProject](settings)
		out.Managed = ptr(p.Config != nil || legacySiteRetired(secretDir(), r.ProjectId))
		if !isTrue(out.Managed) || p.Config == nil || p.Config.Mode != contract.AgentSiteAccessModeWeb ||
			!managedSiteHolder(q, q.host.Holder) || !p.hostEligible(q.hostID()) {
			return nil
		}
		var target *contract.AgentSiteTarget
		if r.ExecutionId == nil && r.Generation == nil {
			target = p.target(deref(r.TargetId))
		} else if r.ExecutionId != nil {
			e, err := getExecution(q, *r.ExecutionId)
			if err != nil || deref(e.HostID) != q.hostID() {
				return nil
			}
			target, _, _ = siteTargetForRun(q, r.ProjectId, p, e)
			if target != nil && r.TargetId != nil && *r.TargetId != target.Id {
				target = nil
			}
		}
		if target == nil {
			return nil
		}
		at := nowMs()
		grant := readSiteAccessGrant(secretDir(), r.ProjectId, *target, p.Roles, at)
		if grant.reason != "" {
			return nil
		}
		if r.Generation != nil && *r.Generation != grant.generation {
			return nil
		}
		out.Generation, out.TargetId, out.IssuedAt = &grant.generation, &target.Id, &at
		if r.Generation == nil {
			out.Production = grant.production
		}
		if len(jsonBytes(out)) > contract.BrowserAccessBytes {
			return fail(503, "Site browser grant exceeds the private transport size")
		}
		return nil
	})
	return out, err
}

// A managed cutover retires the legacy path independently of later edits to the project setting.
// The owner keeps this marker through revocation. Treat filesystem errors as a retirement.
func legacySiteRetired(root, projectID string) bool {
	if !filepath.IsAbs(root) || !idPattern.MatchString(projectID) {
		return false
	}
	_, err := os.Lstat(filepath.Join(root, "project-browser", projectID+".managed"))
	return !errors.Is(err, os.ErrNotExist)
}

// An old executor can otherwise use its legacy local production file without contacting Fleet.
// Remove only the optional browser tool on its managed-project handoffs. Copy the command
// and leave the coordinator's prepared command cache unchanged.
func fenceLegacySiteCommand(q *Request, cmd contract.Command, projectID string) (*contract.Command, error) {
	var settings rawObject
	switch {
	case cmd.Start != nil && cmd.Start.Payload != nil:
		settings = cmd.Start.Payload.Project
		projectID = firstString(deref(cmd.Start.Payload.ProjectId), projectID)
	case cmd.Readiness != nil && cmd.Readiness.Payload != nil:
		settings = cmd.Readiness.Payload.Project
	default:
		return &cmd, nil
	}
	execution := recOf(settings["execution"])
	tools := jsonStrings(execution["tools"])
	if projectID == "" || !slices.Contains(tools, "browser") {
		return &cmd, nil
	}
	if !jsonGiven(settings["agentSiteAccess"]) && !legacySiteRetired(secretDir(), projectID) {
		current, err := projectSettingsByID(q, projectID)
		if err != nil {
			return nil, err
		}
		if !jsonGiven(current["agentSiteAccess"]) {
			return &cmd, nil
		}
	}
	withoutBrowser := make([]string, 0, len(tools)-1)
	for _, tool := range tools {
		if tool != "browser" {
			withoutBrowser = append(withoutBrowser, tool)
		}
	}
	execution["tools"] = jsonBytes(withoutBrowser)
	settings = maps.Clone(settings)
	settings["execution"] = jsonBytes(execution)
	cmd.Raw = nil
	if cmd.Start != nil {
		start, payload := *cmd.Start, *cmd.Start.Payload
		payload.Project = settings
		start.Payload, cmd.Start = &payload, &start
	} else {
		readiness, payload := *cmd.Readiness, *cmd.Readiness.Payload
		payload.Project = settings
		readiness.Payload, cmd.Readiness = &payload, &readiness
	}
	return &cmd, nil
}
