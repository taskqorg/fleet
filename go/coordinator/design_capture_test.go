package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fleet/contract"
)

// The simulator uses actual command, artifact and receipt state with no guest.
func TestDesignCaptureDispatchAuthorizationReplayAndReturn(t *testing.T) {
	e := openTestCoordinator(t, false)
	sql := func(query string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var project projectWriteResult
	e.opInto(t, "project_create", doc{"requestId": "design-capture-fixture", "id": "fleet", "content": doc{
		"settings":     doc{"id": "fleet", "name": "Fleet", "contextFiles": []string{"rules.md"}, "roles": doc{"designer": "codex-designer"}},
		"contextFiles": []doc{{"path": "rules.md", "text": "Invented renderer fixture."}},
	}}, e.ownerHeaders(), &project)
	var task contract.TaskDetail
	e.opInto(t, "task_create", doc{"title": "Synthetic capture", "projectId": "fleet"}, e.ownerHeaders(), &task)
	const designerHost = "capture-designer"
	designerSecret := e.registerHost(t, designerHost)
	rendererSecret := e.registerHost(t, contract.DesignCaptureHost)
	var originExecutions []string
	var reportOriginReady func()
	turn := func(role string) (string, string) {
		sid, eid := newID(), newID()
		sql("INSERT INTO session (id,project_id,task_id,directed_by,payload) VALUES ($1,'fleet',$2,$3,$4)", sid, task.Id, testOwnerEmail, jsonBytes(doc{"role": role}))
		sql("INSERT INTO execution (id,kind,state,host_id,project_id,session_id,task_id,harness,started_at,data,observation,config_ref,profile_id,directed_by) VALUES ($1,'agent_run','running',$2,'fleet',$3,$4,'codex',now(),$5,'confirmed','{}','codex-designer',$6)", eid, designerHost, sid, task.Id, jsonBytes(doc{"agentConfig": doc{"id": "codex-designer"}}), testOwnerEmail)
		if role == "designer" {
			originExecutions = append(originExecutions, eid)
			if reportOriginReady != nil {
				reportOriginReady()
			}
		}
		return sid, eid
	}
	_, eid := turn("designer")
	_, foreign := turn("worker")
	asset := func(owner, name, text string) string {
		id := newID()
		sql("INSERT INTO artifact (id,project_id,scope,content_hash,size,media_type,name,execution_id,state,role) VALUES ($1,'fleet','prod',$2,$3,'application/octet-stream',$4,$5,'stored','original')", id, sha256hex([]byte(text)), len(text), name, strPtr(owner))
		return id
	}
	base := asset("", "base.bundle", "invented bundle")
	overlay := asset(eid, "overlay.json", "invented overlay")
	fixture := asset(eid, "fixture.json", "invented fixture")
	foreignOverlay := asset(foreign, "overlay.json", "foreign overlay")
	runtime := contract.DesignCaptureRuntime{Node: "test", Chromium: "test", Playwright: "test", ImageSHA256: strings.Repeat("1", 64), FontSHA256: strings.Repeat("2", 64), LockSHA256: strings.Repeat("3", 64)}
	producer := contract.DesignCaptureProducer{Commit: strings.Repeat("4", 40), Version: "invented", Target: "linux-amd64"}
	cfg := designCaptureInstallation{Host: contract.DesignCaptureHost, BaseArtifact: base, Producer: producer, Runtime: runtime, LaunchSHA256: strings.Repeat("5", 64)}
	t.Setenv("FLEET_DESIGN_CAPTURE_INSTALLATION", string(jsonBytes(cfg)))
	key := filepath.Join(t.TempDir(), "test-signer")
	seed := make([]byte, ed25519.SeedSize)
	if err := os.WriteFile(key, []byte(hex.EncodeToString(seed)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_DESIGN_CAPTURE_SIGNING_KEY_FILE", key)

	call := func(host, execution, action, request string, overlayID, fixtureID *string) (contract.DesignCaptureReply, error) {
		var result contract.DesignCaptureReply
		q := &Request{app: e.app, person: e.app.peopleSnapshot().person(testOwnerEmail), agent: "execution:" + execution}
		if host != "" {
			q.host = &authHost{HostID: host, Holder: testOwnerEmail}
		}
		err := e.app.store.Write(e.ctx, q, []string{GLOBAL}, func(q *Request) error {
			var err error
			result, err = designCapture(q, &contract.DesignCaptureRequest{Action: contract.DesignCaptureAction(action), RequestId: request, OverlayArtifactId: overlayID, FixtureArtifactId: fixtureID})
			return err
		})
		return result, err
	}
	reportOrigin := func(platform contract.Platform, caps []string) {
		t.Helper()
		code, reply := e.callJSON(t, "POST", contract.RoutePoll.Path, contract.PollRequest{
			HostId: designerHost, Platform: platform, Version: ptr("signed-test"), Capabilities: caps,
		}, map[string]string{"Authorization": "Bearer " + designerSecret})
		if code != 200 {
			t.Fatalf("test origin poll failed: %d %+v", code, reply)
		}
	}
	reportOriginReady = func() {
		var caps []string
		for _, execution := range originExecutions {
			caps = append(caps, contract.DesignCaptureStageCapability(execution))
		}
		reportOrigin(contract.PlatformLinux, caps)
	}
	t.Run("unsupported origin cannot reserve", func(t *testing.T) {
		for _, tc := range []struct {
			platform contract.Platform
			caps     []string
		}{
			{contract.PlatformDarwin, []string{contract.DesignCaptureStageCapability(eid)}},
			{contract.PlatformLinux, []string{"design-capture-stage-linux-v1"}},
			{contract.PlatformLinux, []string{contract.DesignCaptureStageCapability(foreign)}},
		} {
			reportOrigin(tc.platform, tc.caps)
			_, err := call(designerHost, eid, "start", "first", &overlay, &fixture)
			if err == nil || !strings.Contains(err.Error(), "Linux capture staging capability") {
				t.Fatalf("unsupported origin was admitted: %s %v", tc.platform, err)
			}
			var count int
			if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT
				(SELECT count(*) FROM execution WHERE id=$1) +
				(SELECT count(*) FROM command WHERE id=$1)`, designCaptureJobID(eid)).Scan(&count); err != nil || count != 0 {
				t.Fatalf("unsupported origin left a job or command: %d %v", count, err)
			}
		}
	})
	t.Run("a different host cannot attest for an incapable designer", func(t *testing.T) {
		reportOrigin(contract.PlatformLinux, []string{"script"})
		caps := []string{contract.DesignCaptureStageCapability(eid)}
		code, reply := e.callJSON(t, "POST", contract.RoutePoll.Path, contract.PollRequest{
			HostId: contract.DesignCaptureHost, Platform: contract.PlatformLinux, Version: ptr("signed-test"), Capabilities: caps,
		}, map[string]string{"Authorization": "Bearer " + rendererSecret})
		if code != 200 {
			t.Fatalf("renderer test poll: %d %+v", code, reply)
		}
		_, err := call(designerHost, eid, "start", "first", &overlay, &fixture)
		if err == nil || !strings.Contains(err.Error(), "Linux capture staging capability") {
			t.Fatalf("renderer readiness substituted for the incapable origin: %v", err)
		}
		var jobCount int
		if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT count(*) FROM command WHERE id=$1", designCaptureJobID(eid)).Scan(&jobCount); err != nil || jobCount != 0 {
			t.Fatalf("incapable origin left a command: %d %v", jobCount, err)
		}
	})

	reportOriginReady()
	for _, bad := range []struct{ host, turn, input string }{
		{"", eid, overlay}, {"wrong-host", eid, overlay}, {designerHost, foreign, overlay}, {designerHost, eid, foreignOverlay},
	} {
		if _, err := call(bad.host, bad.turn, "start", "first", &bad.input, &fixture); err == nil {
			t.Fatalf("accepted unauthorized capture: %+v", bad)
		}
	}
	startCommand := newID()
	sql("INSERT INTO command (id,execution_id,host_id,project_id,kind,status,payload,handed_out) VALUES ($1,$2,$3,'fleet','start','started','{}',true)",
		startCommand, eid, designerHost)
	httpCode, rpc := e.callJSON(t, "POST", contract.RouteAgentTools.Path, contract.AgentToolsRequest{
		HostId: designerHost, CommandId: startCommand, ExecutionId: &eid,
		Request: jsonBytes(doc{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": doc{
			"name": "design_capture", "arguments": doc{"action": "status", "requestId": "first"},
		}}),
	}, map[string]string{"Authorization": "Bearer " + designerSecret})
	if httpCode != 200 || !strings.Contains(string(jsonBytes(rpc)), "configured") {
		t.Fatalf("authenticated designer MCP status did not reach the fixed operation: %d %+v", httpCode, rpc)
	}

	start, err := call(designerHost, eid, "start", "first", &overlay, &fixture)
	if err != nil || start.State != "preparing" {
		t.Fatalf("start = %+v, %v", start, err)
	}
	replay, err := call(designerHost, eid, "start", "first", &overlay, &fixture)
	if err != nil || replay.JobId != start.JobId {
		t.Fatalf("replay = %+v, %v", replay, err)
	}
	if _, err := call(designerHost, eid, "start", "changed", &overlay, &fixture); err == nil {
		t.Fatal("request replay changed identity")
	}
	if _, err := call(designerHost, eid, "start", "first", &fixture, &fixture); err == nil {
		t.Fatal("request replay changed inputs")
	}

	without, err := e.app.pollReadyCommands(e.ctx, contract.DesignCaptureHost, []string{"script"})
	if err != nil || len(without) != 0 {
		t.Fatalf("unready broker received work: %v %v", len(without), err)
	}
	caps := []string{"script", contract.DesignCaptureCapability}
	t.Run("withdrawn destination capability keeps the same unclaimed job", func(t *testing.T) {
		reportOrigin(contract.PlatformLinux, []string{contract.DesignCaptureStageCapability(foreign)})
		commands, err := e.app.pollReadyCommands(e.ctx, contract.DesignCaptureHost, caps)
		if err != nil || len(commands) != 0 {
			t.Fatalf("unavailable destination obtained a permit: %d %v", len(commands), err)
		}
		var handed bool
		if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT handed_out FROM command WHERE id=$1", start.JobId).Scan(&handed); err != nil || handed {
			t.Fatalf("unavailable destination claimed the durable job: %v %v", handed, err)
		}
		reportOriginReady()
	})
	t.Run("a stale distinct origin report cannot authorize dispatch", func(t *testing.T) {
		sql("UPDATE host_observation SET last_seen_at=now()-interval '2 minutes' WHERE id=$1", designerHost)
		commands, err := e.app.pollReadyCommands(e.ctx, contract.DesignCaptureHost, caps)
		if err != nil || len(commands) != 0 {
			t.Fatalf("stale origin obtained a permit: %d %v", len(commands), err)
		}
		var handed bool
		if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT handed_out FROM command WHERE id=$1", start.JobId).Scan(&handed); err != nil || handed {
			t.Fatalf("stale origin claimed the durable job: %v %v", handed, err)
		}
		// A changed poll is persisted immediately even when the heartbeat
		// normally coalesces repeated identical reports.
		reportOrigin(contract.PlatformLinux, []string{"script"})
		reportOriginReady()
	})
	first, err := e.app.pollReadyCommands(e.ctx, contract.DesignCaptureHost, caps)
	if err != nil || len(first) != 1 || first[0].Script == nil {
		t.Fatalf("fixed host poll: %v %v", len(first), err)
	}
	second, err := e.app.pollReadyCommands(e.ctx, contract.DesignCaptureHost, caps)
	if err != nil || len(second) != 1 || string(jsonBytes(second[0])) != string(jsonBytes(first[0])) {
		t.Fatalf("poll replay changed command: %v", err)
	}
	var p contract.DesignCaptureCommand
	var permit contract.DesignCapturePermit
	raw := first[0].Script.Payload.Rest
	if json.Unmarshal(raw["designCapture"], &p) != nil || json.Unmarshal(raw["designCapturePermit"], &permit) != nil {
		t.Fatal("missing typed dispatch")
	}
	signature, _ := hex.DecodeString(permit.Signature)
	if !ed25519.Verify(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), p.Request.PermitMessage(permit.ExpiresAt, p.InstallationDigest()), signature) {
		t.Fatal("invalid per-job permit")
	}
	if p.OriginHost != designerHost || deref(first[0].Script.HostId) != contract.DesignCaptureHost || p.Request.Execution != eid || p.Request.Job != start.JobId || p.Request.BaseSHA256 != sha256hex([]byte("invented bundle")) {
		t.Fatalf("invalid job identity: %+v", p.Request)
	}

	receipt := func(status contract.ReceiptStatus, artifacts []string) error {
		q := &Request{app: e.app, host: &authHost{HostID: contract.DesignCaptureHost, Holder: testOwnerEmail}}
		return e.app.store.Write(e.ctx, q, []string{GLOBAL, DISPATCH}, func(q *Request) error {
			_, _, err := e.app.statusReceiptIn(q, &contract.Receipt{HostId: contract.DesignCaptureHost, ExecutionId: start.JobId, CommandId: start.JobId, Status: status, Artifacts: artifacts})
			return err
		})
	}
	if err := receipt(contract.ReceiptStatusStarted, nil); err != nil {
		t.Fatal(err)
	}
	asset(foreign, "receipt.json", "foreign receipt")
	asset(foreign, "capture.tar", "foreign archive")
	if pending, err := call(designerHost, eid, "stage", "first", nil, nil); err != nil || pending.Result != nil {
		t.Fatalf("foreign job artifacts were accepted: %+v %v", pending, err)
	}
	rid := asset(start.JobId, "receipt.json", "invented measured receipt")
	aid := asset(start.JobId, "capture.tar", "invented archive")
	if err := receipt(contract.ReceiptStatusCompleted, []string{"receipt.json", "capture.tar"}); err != nil {
		t.Fatal(err)
	}
	staged, err := call(designerHost, eid, "stage", "first", nil, nil)
	if err != nil || string(staged.Result["receiptArtifact"]) != string(jsonBytes(rid)) || string(staged.Result["captureArtifact"]) != string(jsonBytes(aid)) {
		t.Fatalf("stage grant: %+v %v", staged, err)
	}

	// Use a real presigned store URL in the read ordering checks.
	store := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("X-Amz-Signature") == "" || filepath.Base(r.URL.Path) != sha256hex([]byte("invented archive")) {
			http.Error(w, "missing exact grant", 403)
			return
		}
		_, _ = io.WriteString(w, "invented archive")
	}))
	t.Cleanup(store.Close)
	savedSecrets := storeSecretsDir
	storeSecretsDir = t.TempDir()
	t.Cleanup(func() { storeSecretsDir = savedSecrets })
	if err := os.WriteFile(filepath.Join(storeSecretsDir, "capture-store.key"), []byte(`{"accessKeyId":"invented","secretAccessKey":"invented"}`), 0600); err != nil {
		t.Fatal(err)
	}
	sql("INSERT INTO store (id,host_id,bucket,endpoint,state) VALUES ('capture-store',$1,'fleet',$2,'reachable')", designerHost, store.URL)
	sql("INSERT INTO artifact_copy (artifact_id,store_id,project_id,state) VALUES ($1,'capture-store','fleet','verified')", aid)
	transportView := func(command *string) (int, contract.ArtifactView) {
		code, reply := e.callJSON(t, "POST", contract.RouteArtifactsGet.Path, contract.HostArtifactGetRequest{
			HostId: designerHost, ExecutionId: &eid, CommandId: command, ArtifactId: aid,
		}, map[string]string{"Authorization": "Bearer " + designerSecret})
		var view contract.ArtifactView
		_ = json.Unmarshal(jsonBytes(reply), &view)
		return code, view
	}
	transport := func(command *string) int {
		code, _ := transportView(command)
		return code
	}
	if code := transport(nil); code != 403 {
		t.Fatalf("generic artifact GET bypassed the capture mediator: %d", code)
	}
	if code, _ := e.callJSON(t, "GET", "/api/results/"+aid, nil, e.ownerHeaders()); code != 403 {
		t.Fatalf("project result view exposed private capture transport: %d", code)
	}
	if code, view := transportView(&startCommand); code != 200 || view.Url == nil {
		t.Fatalf("mediator rejected a current verified execution: %d %+v", code, view)
	}
	t.Run("child stop and membership revoke order the read grant", func(t *testing.T) {
		member := "capture-order-member@example.invalid"
		sql("INSERT INTO person(email,name,roles) VALUES ($1,'Synthetic grant member',ARRAY['operator'])", member)
		people := map[string]*Person{member: {Email: member, Name: "Synthetic grant member", Roles: []string{"operator"}}}
		for _, person := range e.app.peopleSnapshot().list() {
			people[person.Email] = person
		}
		e.app.publishPeople(people)
		// Keep the member's project access entirely in the initial definition row.
		sql("UPDATE execution SET directed_by=$2,data=jsonb_set(data,'{directedBy}',to_jsonb($2::text)) WHERE id=$1", eid, member)
		defer sql("UPDATE execution SET directed_by=$2,data=jsonb_set(data,'{directedBy}',to_jsonb($2::text)) WHERE id=$1", eid, testOwnerEmail)
		revokeMembership := func(q *Request) error {
			row, err := q.db().DefProjectAccess(q.ctx(), "fleet")
			if err != nil {
				return err
			}
			_, err = projectInitialRevokeOf(q, "fleet", row.AccessVersion)
			return err
		}
		stopChild := func(q *Request) error {
			_, err := requestExecutionStop(q, start.JobId, false)
			return err
		}
		for _, tc := range []struct {
			name   string
			revoke func(*Request) error
		}{{"child stop", stopChild}, {"membership revoke", revokeMembership}} {
			t.Run(tc.name, func(t *testing.T) {
				reset := func() {
					sql("UPDATE definition SET initial_member=$1,initial_member_revoked_at=NULL WHERE kind='project' AND id='fleet'", member)
					sql("UPDATE execution SET data=data-'stopRequested' WHERE id=$1", start.JobId)
				}
				reset()
				// Revoke-first: prove the real host GET blocks on this transaction.
				started, revokeCommitted, release := holdCaptureTestTx(t, e, tc.revoke)
				select {
				case <-started:
				case err := <-revokeCommitted:
					t.Fatalf("revoke did not acquire its lock: %v", err)
				}
				revokePID := release.pid
				get := make(chan int, 1)
				go func() { get <- transport(&startCommand) }()
				waitCaptureTestBlocker(t, e, revokePID)
				release.commit()
				if err := <-revokeCommitted; err != nil {
					t.Fatal(err)
				}
				if code := <-get; code != 403 {
					t.Fatalf("revocation committed first, GET = %d", code)
				}
				reset()

				// Grant-first: hold the real authority and presign locks until
				// the competing revocation waits, then consume the granted URL.
				var signed string
				grant := func(q *Request) error {
					a := &Artifact{ID: aid, ExecutionID: &start.JobId, State: "stored"}
					if _, err := captureTransportAuthority(q, designerHost, eid, startCommand, a); err != nil {
						return err
					}
					view, err := artifactView(context.WithValue(withTx(q), captureReadKey{}, aid), q.app, aid, designerHost)
					signed = deref(view.Url)
					return err
				}
				started, grantCommitted, release := holdCaptureTestTx(t, e, grant)
				select {
				case <-started:
				case err := <-grantCommitted:
					t.Fatalf("grant did not acquire its locks: %v", err)
				}
				stopped := make(chan error, 1)
				go func() { stopped <- e.app.store.Write(e.ctx, nil, nil, tc.revoke) }()
				waitCaptureTestBlocker(t, e, release.pid)
				release.commit()
				if err := <-grantCommitted; err != nil {
					t.Fatal(err)
				}
				if err := <-stopped; err != nil {
					t.Fatal(err)
				}
				resp, err := store.Client().Get(signed)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				raw, err := io.ReadAll(resp.Body)
				if err != nil || resp.StatusCode != 200 || string(raw) != "invented archive" {
					t.Fatalf("grant committed first, read = %d %q %v", resp.StatusCode, raw, err)
				}
				reset()
			})
		}
	})

	if _, err := call(designerHost, eid, "cancel", "first", nil, nil); err != nil {
		t.Fatal(err)
	}
	if code := transport(&startCommand); code != 403 {
		t.Fatalf("cancellation before mediated download did not fence release: %d", code)
	}
	if revoked, err := call(designerHost, eid, "stage", "first", nil, nil); err != nil || revoked.Result != nil || revoked.State != "cancelled" {
		t.Fatalf("completed job revoke released outputs: %+v %v", revoked, err)
	}
	_, next := turn("designer")
	nextOverlay := asset(next, "overlay.json", "next overlay")
	nextFixture := asset(next, "fixture.json", "next fixture")
	stopping, err := call(designerHost, next, "start", "cancel-test", &nextOverlay, &nextFixture)
	if err != nil {
		t.Fatal(err)
	}
	_, err = call(designerHost, next, "cancel", "cancel-test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	final, err := call(designerHost, next, "stage", "cancel-test", nil, nil)
	if err != nil || final.JobId != stopping.JobId || final.Result != nil || final.State != "cancelled" {
		t.Fatalf("cancel released outputs: %+v %v", final, err)
	}

	_, parent := turn("designer")
	parentOverlay := asset(parent, "overlay.json", "parent overlay")
	parentFixture := asset(parent, "fixture.json", "parent fixture")
	child, err := call(designerHost, parent, "start", "parent-stop", &parentOverlay, &parentFixture)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.app.store.Write(e.ctx, &Request{app: e.app}, []string{GLOBAL}, func(q *Request) error {
		if _, err := requestExecutionStop(q, parent, false); err != nil {
			return err
		}
		job, err := getExecution(q, child.JobId)
		if err != nil {
			return err
		}
		if !isTrue(job.StopRequested) {
			t.Fatal("designer stop did not fence its capture")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := call(designerHost, parent, "stage", "parent-stop", nil, nil); err == nil {
		t.Fatal("stopped parent staged a capture")
	}

	for _, kind := range []string{"paused task", "role revoked", "origin director revoked", "origin host changed", "expired while broker unavailable"} {
		t.Run(kind, func(t *testing.T) {
			sid, execution := turn("designer")
			overlay := asset(execution, "overlay.json", "bound overlay")
			fixture := asset(execution, "fixture.json", "bound fixture")
			job, err := call(designerHost, execution, "start", "handoff-check", &overlay, &fixture)
			if err != nil {
				t.Fatal(err)
			}
			hostCaps := caps
			switch kind {
			case "paused task":
				sql("UPDATE task SET state='paused' WHERE id=$1", task.Id)
				defer sql("UPDATE task SET state='queued' WHERE id=$1", task.Id)
			case "role revoked":
				sql("UPDATE session SET payload=jsonb_set(payload,'{role}','\"worker\"') WHERE id=$1", sid)
			case "origin director revoked":
				sql("UPDATE execution SET directed_by=$2,data=jsonb_set(data,'{directedBy}',to_jsonb($2::text)) WHERE id=$1",
					execution, "former-director@example.invalid")
			case "origin host changed":
				sql("UPDATE execution SET host_id=$2 WHERE id=$1", execution, contract.DesignCaptureHost)
				code, _ := e.callJSON(t, "POST", contract.RoutePoll.Path, contract.PollRequest{
					HostId: contract.DesignCaptureHost, Platform: contract.PlatformLinux, Version: ptr("signed-test"),
					Capabilities: []string{contract.DesignCaptureStageCapability(execution)},
				}, map[string]string{"Authorization": "Bearer " + rendererSecret})
				if code != 200 {
					t.Fatalf("replacement host poll returned %d", code)
				}
			case "expired while broker unavailable":
				sql("UPDATE command SET payload=jsonb_set(payload,'{payload,permitExpiresAt}','0') WHERE id=$1", job.JobId)
				hostCaps = []string{"script"}
			}
			commands, err := e.app.pollReadyCommands(e.ctx, contract.DesignCaptureHost, hostCaps)
			if err != nil {
				t.Fatal(err)
			}
			for _, command := range commands {
				if command.Script != nil {
					t.Fatal("stale capture was handed out")
				}
			}
			var state string
			if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT state::text FROM execution WHERE id=$1", job.JobId).Scan(&state); err != nil || state != "cancelled" {
				t.Fatalf("stale capture did not settle: %q %v", state, err)
			}
		})
	}

	// Start through native MCP as a non-owner whose only project authority
	// initially comes from the live membership row. Deliberately keep the
	// in-memory snapshot unchanged while committing each database update.
	const member = "capture-member@example.invalid"
	sql("INSERT INTO person(email,name,roles) VALUES ($1,'Synthetic member',ARRAY['operator'])", member)
	people := map[string]*Person{member: {Email: member, Name: "Synthetic member", Roles: []string{"operator"}}}
	for _, person := range e.app.peopleSnapshot().list() {
		people[person.Email] = person
	}
	e.app.publishPeople(people)
	sql("UPDATE definition SET initial_member=$1,initial_member_revoked_at=NULL WHERE kind='project' AND id='fleet'", member)

	startMember := func(label string) string {
		t.Helper()
		_, execution := turn("designer")
		sql("UPDATE execution SET directed_by=$2,data=jsonb_set(data,'{directedBy}',to_jsonb($2::text)) WHERE id=$1", execution, member)
		command := newID()
		sql("INSERT INTO command (id,execution_id,host_id,project_id,kind,status,payload,handed_out) VALUES ($1,$2,$3,'fleet','start','started','{}',true)",
			command, execution, designerHost)
		overlay := asset(execution, "overlay.json", "synthetic member overlay")
		fixture := asset(execution, "fixture.json", "synthetic member fixture")
		code, body := e.callJSON(t, "POST", contract.RouteAgentTools.Path, contract.AgentToolsRequest{
			HostId: designerHost, CommandId: command, ExecutionId: &execution,
			Request: jsonBytes(doc{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": doc{
				"name": "design_capture", "arguments": doc{"action": "start", "requestId": label, "overlayArtifactId": overlay, "fixtureArtifactId": fixture},
			}}),
		}, map[string]string{"Authorization": "Bearer " + designerSecret})
		job := designCaptureJobID(execution)
		if code != 200 || !strings.Contains(string(jsonBytes(body)), job) {
			t.Fatalf("native MCP member start failed: %d %+v", code, body)
		}
		return job
	}
	pollHas := func(job string) bool {
		t.Helper()
		commands, err := e.app.pollReadyCommands(e.ctx, contract.DesignCaptureHost, caps)
		if err != nil {
			t.Fatal(err)
		}
		for _, command := range commands {
			if command.Script != nil && command.Script.ExecutionId == job {
				return true
			}
		}
		return false
	}
	if allowed := startMember("initial-membership"); !pollHas(allowed) {
		t.Fatal("initial project member passed native MCP but was denied at handoff")
	}
	revokedMembership := startMember("membership-revocation")
	sql("UPDATE definition SET initial_member_revoked_at=now(),access_version=access_version+1 WHERE kind='project' AND id='fleet'")
	if pollHas(revokedMembership) {
		t.Fatal("committed initial membership revocation did not fence handoff")
	}
	// Give the member an explicit project grant in both sources, then remove
	// only the live row. A stale published projection must never allow it.
	people[member].Grants = []personGrant{{Kind: "project", ID: "fleet"}}
	e.app.publishPeople(people)
	sql("INSERT INTO person_grant (email,object_kind,object_id) VALUES ($1,'project','fleet')", member)
	revokedGrant := startMember("direct-revocation")
	sql("DELETE FROM person_grant WHERE email=$1 AND object_kind='project' AND object_id='fleet'", member)
	if pollHas(revokedGrant) {
		t.Fatal("stale people projection overrode a committed direct project revocation")
	}

}

// Hold the real write transaction until pg_blocking_pids observes the ordering.
// Cleanup releases the barrier even when an assertion fails.
type captureLockBarrier struct {
	pid    int
	commit func()
}

func holdCaptureTestTx(t *testing.T, e *testCoordEnv, operation func(*Request) error) (<-chan struct{}, <-chan error, *captureLockBarrier) {
	t.Helper()
	acquired := make(chan struct{})
	release := make(chan struct{})
	result := make(chan error, 1)
	barrier := &captureLockBarrier{commit: sync.OnceFunc(func() { close(release) })}
	t.Cleanup(barrier.commit)
	go func() {
		ctx, cancel := context.WithTimeout(e.ctx, 8*time.Second)
		defer cancel()
		result <- e.app.store.writeOnce(ctx, nil, nil, func(q *Request) error {
			if err := q.tx.QueryRow(q.ctx(), "SELECT pg_backend_pid()").Scan(&barrier.pid); err != nil {
				return err
			}
			if err := operation(q); err != nil {
				return err
			}
			close(acquired)
			select {
			case <-release:
				return nil
			case <-q.ctx().Done():
				return q.ctx().Err()
			}
		})
	}()
	return acquired, result, barrier
}

func waitCaptureTestBlocker(t *testing.T, e *testCoordEnv, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(e.ctx, 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		var blocked bool
		err := e.ownerStore.pool.QueryRow(ctx, `SELECT EXISTS(SELECT FROM pg_stat_activity
			WHERE $1 = ANY(pg_blocking_pids(pid)))`, pid).Scan(&blocked)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("competing transaction did not wait for capture authority")
		}
	}
}
