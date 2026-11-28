package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
	designerSecret := e.registerHost(t, "capture-designer")
	e.registerHost(t, contract.DesignCaptureHost)
	const designerHost = "capture-designer"
	turn := func(role string) (string, string) {
		sid, eid := newID(), newID()
		sql("INSERT INTO session (id,project_id,task_id,directed_by,payload) VALUES ($1,'fleet',$2,$3,$4)", sid, task.Id, testOwnerEmail, jsonBytes(doc{"role": role}))
		sql("INSERT INTO execution (id,kind,state,host_id,project_id,session_id,task_id,harness,started_at,data,observation,config_ref,profile_id,directed_by) VALUES ($1,'agent_run','running',$2,'fleet',$3,$4,'codex',now(),$5,'confirmed','{}','codex-designer',$6)", eid, designerHost, sid, task.Id, jsonBytes(doc{"agentConfig": doc{"id": "codex-designer"}}), testOwnerEmail)
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
	cfg := designCaptureInstallation{Host: contract.DesignCaptureHost, BaseArtifact: base, Producer: producer, Runtime: runtime}
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
	if !ed25519.Verify(ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey), p.Request.PermitMessage(permit.ExpiresAt), signature) {
		t.Fatal("invalid per-job permit")
	}
	if p.Request.Execution != eid || p.Request.Job != start.JobId || p.Request.BaseSHA256 != sha256hex([]byte("invented bundle")) {
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

	if _, err := call(designerHost, eid, "cancel", "first", nil, nil); err != nil {
		t.Fatal(err)
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

	for _, kind := range []string{"paused task", "role revoked", "origin director revoked", "expired while broker unavailable"} {
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

}
