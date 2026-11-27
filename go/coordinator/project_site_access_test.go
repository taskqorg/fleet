package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

func siteTestTarget() doc {
	return doc{
		"id": "production", "origin": "https://site.example", "roles": []string{"worker"},
		"scope": "verify_read_only", "authKind": "verification_header", "expectedIdentity": "test-verifier",
		"probe": doc{"path": "/api/verify", "method": "GET", "expectedStatus": 200, "identityField": "principal"},
	}
}

func siteTestBundle(site any) doc {
	settings := doc{"id": "site-test", "name": "Site test", "roles": doc{"worker": "coding-worker"},
		"contextFiles": []string{}, "environments": []doc{{"hostId": "site-host", "checkoutPath": "/invented/site"},
			{"hostId": "site-other", "checkoutPath": "/invented/site"}}, "execution": doc{"tools": []string{"browser"}}}
	if site != nil {
		settings["agentSiteAccess"] = site
	}
	return doc{"settings": settings, "contextFiles": []doc{}}
}

func TestAgentSiteAccessValidation(t *testing.T) {
	valid := func() doc { return doc{"mode": "web", "targets": []doc{siteTestTarget()}} }
	for _, tc := range []struct {
		name   string
		change func(site, target, probe doc)
	}{
		{"unknown mode", func(s, _, _ doc) { s["mode"] = "maybe" }},
		{"non-web with target", func(s, _, _ doc) { s["mode"] = "not_a_web_app" }},
		{"unknown default", func(s, _, _ doc) { s["defaultTargetId"] = "other" }},
		{"secret on target", func(_, t, _ doc) { t["headers"] = doc{"key": "invented-secret"} }},
		{"access file on probe", func(_, _, p doc) { p["accessFile"] = "/invented/private" }},
		{"unknown role", func(_, t, _ doc) { t["roles"] = []string{"admin"} }},
		{"duplicate role", func(_, t, _ doc) { t["roles"] = []string{"worker", "worker"} }},
		{"scope", func(_, t, _ doc) { t["scope"] = "write" }},
		{"auth kind", func(_, t, _ doc) { t["authKind"] = "cookie" }},
		{"http", func(_, t, _ doc) { t["origin"] = "http://site.example" }},
		{"userinfo", func(_, t, _ doc) { t["origin"] = "https://example@site.example" }},
		{"origin with slash", func(_, t, _ doc) { t["origin"] = "https://site.example/" }},
		{"default port", func(_, t, _ doc) { t["origin"] = "https://site.example:443" }},
		{"host rewrite", func(_, t, _ doc) { t["origin"] = "https://127.1" }},
		{"cross-origin probe", func(_, _, p doc) { p["path"] = "//other.example/check" }},
		{"path rewrite", func(_, _, p doc) { p["path"] = "/a/%2e%2e/verify" }},
		{"unsafe method", func(_, _, p doc) { p["method"] = "PATCH" }},
		{"missing status", func(_, _, p doc) { delete(p, "expectedStatus") }},
		{"invalid status", func(_, _, p doc) { p["expectedStatus"] = 999 }},
		{"null target list", func(s, _, _ doc) { s["targets"] = nil }},
		{"missing identity", func(_, t, _ doc) { delete(t, "expectedIdentity") }},
		{"missing identity field", func(_, _, p doc) { delete(p, "identityField") }},
		{"HEAD identity", func(_, _, p doc) { p["method"] = "HEAD" }},
		{"duplicate id", func(s, t, _ doc) { s["targets"] = []doc{t, siteTestTarget()} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site := valid()
			target := site["targets"].([]doc)[0]
			tc.change(site, target, target["probe"].(doc))
			if _, err := projectBundle("site-test", siteTestBundle(site)); err == nil {
				t.Fatal("invalid setting accepted")
			}
		})
	}

	public := siteTestTarget()
	public["origin"], public["authKind"] = "https://site.example:8443", "none"
	delete(public, "expectedIdentity")
	public["probe"] = doc{"path": "/health", "method": "HEAD", "expectedStatus": 204}
	for _, site := range []any{nil, valid(), doc{"mode": "web", "targets": []doc{}},
		doc{"mode": "not_a_web_app"}, doc{"mode": "web", "targets": []doc{public}}} {
		bundle, err := projectBundle("site-test", siteTestBundle(site))
		if err != nil {
			t.Fatal(err)
		}
		normalized, unknown, err := projectSettings(recOf(bundle["settings"]))
		if err != nil || len(unknown) != 0 || (site == nil) != (normalized["agentSiteAccess"] == nil) {
			t.Fatalf("optional setting was not normalized: %v, %v", unknown, err)
		}
	}
}

func TestAgentSiteAccessRoleAndScope(t *testing.T) {
	roles := rawObject{"worker": jsonBytes("coding-worker"), "pm": jsonBytes("pm")}
	reader := siteTestTarget()
	operator := siteTestTarget()
	operator["id"], operator["scope"] = "operations", "operator"
	config, err := parseAgentSiteAccess(jsonBytes(doc{"mode": "web", "targets": []doc{reader, operator}}), roles)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		access, preferred, requested, role, profile string
		wantTarget, wantReason                      string
	}{
		{"read", "", "", "worker", "coding-worker", "production", ""},
		{"read", "operations", "", "worker", "coding-worker", "production", ""},
		{"read", "", "operations", "worker", "coding-worker", "", "role_or_scope_not_eligible"},
		{"write", "", "", "worker", "coding-worker", "", "target_selection_required"},
		{"write", "operations", "", "worker", "coding-worker", "operations", ""},
		{"write", "operations", "production", "worker", "coding-worker", "production", ""},
		{"write", "operations", "", "worker", "pm", "", "role_not_eligible"},
		{"write", "operations", "", "pm", "pm", "", "role_or_scope_not_eligible"},
	} {
		config.DefaultTargetId = optStr(tc.preferred)
		target, reason := selectSiteTarget(config, roles, tc.role, tc.profile, tc.access, tc.requested)
		gotTarget := ""
		if target != nil {
			gotTarget = target.Id
		}
		if gotTarget != tc.wantTarget || reason != tc.wantReason {
			t.Errorf("%+v: target %q, reason %q", tc, gotTarget, reason)
		}
	}
	grant := siteAccessGrant{Target: config.Targets[0], Profiles: map[string]string{"worker": "coding-worker"}}
	if siteGrantMatches(grant, config.Targets[0], roles) {
		t.Fatal("read-only scope accepted without an approved site-enforced policy")
	}
	grant.SiteReadOnly = true
	expansion := config.Targets[0]
	expansion.Roles = []string{"worker", "pm"}
	if !siteGrantMatches(grant, config.Targets[0], roles) || siteGrantMatches(grant, expansion, roles) {
		t.Fatal("grant failed to distinguish original scope from role expansion")
	}
}

func TestAgentSiteAccessReadiness(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	const (
		projectID = "site-test"
		targetID  = "production"
		hostID    = "site-host"
		otherID   = "site-other"
	)
	headers := func(host string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + e.registerHost(t, host)}
	}
	host, other, outside := headers(hostID), headers(otherID), headers("site-outside")
	root := t.TempDir()
	t.Setenv("FLEET_SECRET_DIR", root)
	dir := filepath.Join(root, "project-browser")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	write := func(name string, value []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), value, 0600); err != nil {
			t.Fatal(err)
		}
	}
	target := siteTestTarget()
	bundle := siteTestBundle(doc{"mode": "web", "targets": []doc{target}})
	target["accessFile"] = "/invented/private"
	create := doc{"id": projectID, "requestId": "site-create", "content": bundle}
	if status, _ := e.callJSON(t, "POST", "/api/ops/project_create", create, owner); status != http.StatusBadRequest {
		t.Fatalf("write accepted private material: %d", status)
	}
	delete(target, "accessFile")
	var version projectWriteResult
	e.opInto(t, "project_create", create, owner, &version)

	status := func(want contract.ProjectSetupCheckState, reason string) contract.ProjectReadinessReply {
		t.Helper()
		var out contract.ProjectReadinessReply
		e.opInto(t, "project_readiness", doc{"projectId": projectID, "mode": "status"}, owner, &out)
		if len(out.Hosts) != 0 || out.Setup == nil || out.SiteAccess == nil || len(out.SiteAccess.Targets) != 1 {
			t.Fatalf("invalid status: %+v", out)
		}
		got := out.SiteAccess.Targets[0]
		if got.State != want || got.ReasonCode != reason {
			t.Fatalf("target state %q/%q, want %q/%q", got.State, got.ReasonCode, want, reason)
		}
		return out
	}
	status(contract.ProjectSetupCheckStateMissing, "grant_missing")

	assertProblem := func(want int) {
		t.Helper()
		if err := e.app.reconcileProjectSiteProblems(e.ctx); err != nil {
			t.Fatal(err)
		}
		err := e.app.store.Read(e.ctx, nil, func(q *Request) error {
			open, err := q.db().ProblemsOpenOfSource(q.ctx(), projectSiteProblemSource)
			if len(open) != want {
				t.Errorf("open site access issues = %d, want %d", len(open), want)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	assertProblem(1)
	assertProblem(1)

	private := doc{"target": target, "profiles": doc{"worker": "coding-worker"},
		"generation": "invented-test-generation-for-site-access", "siteReadOnly": true,
		"accessFile": "site.env", "headers": doc{"X-Site-Test-Token": "SITE_TOKEN"}}
	write("site-test.production.grant.json", jsonBytes(private))
	write("site.env", []byte("SITE_TOKEN=invented-site-secret\n"))
	status(contract.ProjectSetupCheckStateUnknown, "target_unverified")
	assertProblem(1)

	request := contract.BrowserAccessRequest{Managed: ptr(true), ProjectId: projectID, TargetId: ptr(targetID)}
	readGrant := func(headers map[string]string, request contract.BrowserAccessRequest) contract.BrowserAccessReply {
		t.Helper()
		code, response := e.callJSON(t, "POST", contract.RouteBrowserAccess.Path, request, headers)
		if code != http.StatusOK {
			t.Fatalf("browser access: HTTP %d", code)
		}
		return agentView[contract.BrowserAccessReply](response)
	}
	if denied := readGrant(outside, request); denied.Production != nil || denied.Generation != nil {
		t.Fatal("ungranted host received managed authority")
	}
	if legacy := readGrant(host, contract.BrowserAccessRequest{ProjectId: projectID}); legacy.Production != nil || legacy.ClientSecret != "" {
		t.Fatal("managed project fell back to legacy authority")
	}
	grant := readGrant(host, request)
	if grant.Production == nil || grant.Generation == nil || grant.IssuedAt == nil {
		t.Fatal("managed host did not receive the approved grant")
	}
	probe := contract.BrowserTargetObservation{ProjectId: projectID, TargetId: targetID, Generation: *grant.Generation,
		StartedAt: *grant.IssuedAt, CheckedAt: *grant.IssuedAt, ReasonCode: "verified"}
	poll := func(name string, headers map[string]string, active []string, checks ...contract.BrowserTargetObservation) {
		t.Helper()
		code, _ := e.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
			HostId: name, Platform: contract.PlatformLinux, BrowserTargets: checks, ActiveExecutions: active,
			Capabilities: []string{string(contract.CapabilityAgentSiteAccessV1)}}, headers)
		if code != http.StatusOK {
			t.Fatalf("poll: HTTP %d", code)
		}
	}
	poll("site-outside", outside, nil, probe)
	status(contract.ProjectSetupCheckStateUnknown, "target_unverified")
	poll(hostID, host, nil, probe)
	ready := status(contract.ProjectSetupCheckStateReady, "verified")
	assertProblem(0)
	if ready.Setup.State != contract.ProjectSetupStateReady || deref(ready.SiteAccess.Targets[0].ObservedIdentity) != "test-verifier" {
		t.Fatalf("verified site did not flow to setup: %+v", ready.Setup)
	}

	update := func(requestID string) {
		t.Helper()
		var next projectWriteResult
		e.opInto(t, "project_update", doc{"projectId": projectID, "requestId": requestID, "baseRevision": version.Revision, "content": bundle}, owner, &next)
		version = next
	}
	bundle["settings"].(doc)["description"] = "Edited without changing the grant."
	update("site-description")
	status(contract.ProjectSetupCheckStateReady, "verified")
	var context contract.ProjectContextReply
	e.opInto(t, "project_context", doc{"projectId": projectID}, owner, &context)
	if context.AgentSiteAccess == nil || context.Settings.AgentSiteAccess == nil || context.Content.Settings.AgentSiteAccess == nil ||
		context.Revision != version.Revision || context.Setup == nil || context.Setup.State != ready.Setup.State {
		t.Fatal("project_context omitted authored intent, readback, or setup")
	}
	enabled := resourceReadsEnabled
	resourceReadsEnabled = true
	t.Cleanup(func() { resourceReadsEnabled = enabled })
	var detail contract.ProjectDetailReply
	e.opInto(t, "project_detail", doc{"projectId": projectID}, owner, &detail)
	var view contract.ProjectViewReply
	e.opInto(t, "project_view", doc{"projectId": projectID}, owner, &view)
	var overview contract.ProjectsViewReply
	e.opInto(t, "projects_view", doc{}, owner, &overview)
	for _, x := range []*contract.ProjectSetup{detail.Setup, view.Setup, context.Setup} {
		if x == nil || x.State != ready.Setup.State {
			t.Fatal("readers disagree on setup state")
		}
	}
	if detail.Settings.AgentSiteAccess == nil || overview.Setup[projectID] != ready.Setup.State {
		t.Fatal("configuration or catalogue summary missing")
	}
	for _, v := range []any{context, detail, view, overview, ready} {
		if strings.Contains(string(jsonBytes(v)), "invented-site-secret") || strings.Contains(string(jsonBytes(v)), "site.env") ||
			strings.Contains(string(jsonBytes(v)), *grant.Generation) {
			t.Fatal("private grant material reached a public projection")
		}
	}

	runID, sessionID := newID(), newID()
	err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		if err := sessionPut(q, &agentSession{ID: sessionID, ProjectID: projectID, Role: "worker", DirectedBy: testOwnerEmail, Access: "read"}); err != nil {
			return err
		}
		return executionWrite(q, &agentExecution{ID: runID, SessionID: sessionID, ProjectID: projectID, DirectedBy: testOwnerEmail,
			HostID: ptr(otherID), State: "running", CreatedAt: nowMs(), Dispatch: &agentDispatch{Access: ptr("read")},
			AgentConfig:     jsonBytes(agentProfile{ID: "coding-worker", Harness: "codex"}),
			ProjectSnapshot: jsonBytes(projectSnapshot{Settings: jsonBytes(context.Settings)})})
	})
	if err != nil {
		t.Fatal(err)
	}
	caller := func(want contract.ProjectSiteAccessAvailability) {
		t.Helper()
		err := e.app.store.Read(e.ctx, &Request{app: e.app, agent: "execution:" + runID}, func(q *Request) error {
			_, access := readProjectSetup(q, projectID, recOf(context.Settings))
			if access.ForCaller == nil || access.ForCaller.Availability != want {
				t.Errorf("caller readiness = %+v, want %q", access.ForCaller, want)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	poll(otherID, other, []string{runID})
	caller(contract.ProjectSiteAccessAvailabilityUnknown)
	runRequest := request
	runRequest.ExecutionId, runRequest.TargetId = &runID, nil
	if invalidRun := readGrant(host, runRequest); invalidRun.Generation != nil {
		t.Fatal("another host received a run's grant")
	}
	runGrant := readGrant(other, runRequest)
	if runGrant.Generation == nil || runGrant.IssuedAt == nil || runGrant.Production == nil {
		t.Fatal("selected run received no grant")
	}
	probe.ExecutionId, probe.StartedAt, probe.CheckedAt = &runID, *runGrant.IssuedAt, *runGrant.IssuedAt
	poll(otherID, other, []string{runID}, probe)
	caller(contract.ProjectSiteAccessAvailabilityAvailable)
	runRequest.TargetId, runRequest.Generation = ptr(targetID), runGrant.Generation
	if fence := readGrant(other, runRequest); fence.Generation == nil || fence.Production != nil {
		t.Fatal("generation validation returned no confirmation or included a credential")
	}

	write("site.env", []byte("SITE_TOKEN=invented-site-rotated\n"))
	if fence := readGrant(other, runRequest); fence.Generation != nil {
		t.Fatal("rotation kept the previous run lease")
	}
	status(contract.ProjectSetupCheckStateUnknown, "target_unverified")
	caller(contract.ProjectSiteAccessAvailabilityUnknown)
	oldProof := probe
	fresh := readGrant(host, request)
	probe.ExecutionId, probe.Generation, probe.StartedAt, probe.CheckedAt = nil, *fresh.Generation, *fresh.IssuedAt, *fresh.IssuedAt
	poll(hostID, host, nil, oldProof, probe)
	status(contract.ProjectSetupCheckStateReady, "verified")

	private["expiresAt"] = time.Now().Add(-time.Minute).UnixMilli()
	write("site-test.production.grant.json", jsonBytes(private))
	status(contract.ProjectSetupCheckStateBlocked, "credential_expired")
	assertProblem(1)
	bundle["settings"].(doc)["agentSiteAccess"] = doc{"mode": "not_a_web_app"}
	update("site-non-web")
	var noSite contract.ProjectReadinessReply
	e.opInto(t, "project_readiness", doc{"projectId": projectID, "mode": "status"}, owner, &noSite)
	if noSite.Setup == nil || noSite.Setup.State != contract.ProjectSetupStateNotApplicable || len(noSite.SiteAccess.Targets) != 0 {
		t.Fatalf("non-web project generated a site finding: %+v", noSite)
	}
	assertProblem(0)
	var queued contract.ProjectReadinessReply
	e.opInto(t, "project_readiness", doc{"projectId": projectID}, owner, &queued)
	if len(queued.Hosts) != 2 || queued.Hosts[0].ExecutionId == nil || queued.Hosts[1].ExecutionId == nil {
		t.Fatal("default readiness no longer queues one check per host")
	}
}

func TestAgentSiteAccessIgnoresLateEvidence(t *testing.T) {
	now := time.Now().UnixMilli()
	target := agentView[contract.AgentSiteTarget](siteTestTarget())
	grant := siteGrantStatus{generation: strings.Repeat("a", 64)}
	base := contract.BrowserTargetObservation{ProjectId: "site-test", TargetId: target.Id, Generation: grant.generation,
		StartedAt: now - 3000, CheckedAt: now - 1000, ReasonCode: "verified"}
	newer := base
	newer.StartedAt, newer.CheckedAt, newer.ReasonCode = now-2000, now-1500, "identity_mismatch"
	host := projectSetupHost{id: "x", browser: []contract.BrowserTargetObservation{newer, base}}
	got := projectSiteTarget("site-test", target, grant, []projectSetupHost{host}, now)
	if got.State != contract.ProjectSetupCheckStateBlocked || got.ObservedIdentity != nil || got.ReasonCode != "identity_mismatch" {
		t.Fatalf("older success masked newer failure: %+v", got)
	}
	got = projectSiteTarget("site-test", target, grant, []projectSetupHost{host}, now+contract.SiteAccessEvidenceAge.Milliseconds())
	if got.State != contract.ProjectSetupCheckStateUnknown {
		t.Fatalf("expired evidence was treated as current: %+v", got)
	}
}
