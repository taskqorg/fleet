package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// T-FLT-142. Use real operation and host routes, RLS, and the work-kit step.
// A lost POST receipt, another host or a duplicate request must never gain a second dispatch claim.
func TestCIRerunDurableCoordinator(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	proj, source, sid := "ci-pilot", newID(), newID()
	const host = "ci-host"
	hostHeaders := map[string]string{"Authorization": "Bearer " + e.registerHost(t, host)}
	policy := contract.PublishingCIRerunPolicy{RepoId: "main", Workflow: ".github/workflows/ci.yml",
		WorkflowBlobSha: strings.Repeat("a", 40), RequiredJobs: []string{"go"}, Profiles: []string{"coding-worker"}}
	settings := doc{"id": proj, "name": "CI pilot", "contextFiles": []string{"rules.md"}, "execution": doc{"forge": true, "profiles": []string{"coding-worker"}},
		"repos":       []doc{{"id": "main", "remote": "https://github.com/example/repo"}},
		"integration": doc{"mode": "pr", "autoMerge": false}}
	var project projectWriteResult
	projectContent := doc{"settings": settings, "contextFiles": []doc{{"path": "rules.md", "text": "CI pilot fixture."}}}
	e.opInto(t, "project_create", doc{"requestId": proj, "id": proj, "content": projectContent}, h, &project)
	now := time.Now()
	var task contract.TaskDetail
	e.opInto(t, "task_create", doc{"title": "CI recovery fixture", "projectId": proj}, h, &task)
	write := func(fn func(*Request) error) {
		t.Helper()
		if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, fn); err != nil {
			t.Fatal(err)
		}
	}
	write(func(q *Request) error {
		capabilities := []string{contract.PublishingHostCapability("github", "github.com", "example/repo"),
			string(contract.CapabilityPublishingCiRerunV1)}
		logins := []contract.ForgeLogin{{Provider: "github", Host: "github.com", Ok: true}}
		if err := q.db().HostObservationPut(q.ctx(), db.HostObservationPutParams{ID: host, Holder: ptr(testOwnerEmail),
			LastSeenAt: &now, Payload: jsonBytes(hostPollObservationPayload{Capabilities: &capabilities, ForgeLogins: &logins})}); err != nil {
			return err
		}
		if err := sessionPut(q, &agentSession{ID: sid, ProjectID: proj, TaskID: task.Id, DirectedBy: testOwnerEmail,
			HostID: host, AgentConfig: jsonBytes(agentProfile{ID: "coding-worker", Harness: "codex"})}); err != nil {
			return err
		}
		if err := executionWrite(q, &agentExecution{ID: source, SessionID: sid, HostID: ptr(host), ProjectID: proj,
			TaskID: task.Id, DirectedBy: testOwnerEmail, State: "running", CreatedAt: now.UnixMilli(),
			AgentConfig: jsonBytes(agentProfile{ID: "coding-worker", Harness: "codex"})}); err != nil {
			return err
		}
		state, err := q.db().TaskStateForShare(q.ctx(), task.Id)
		if err == nil && state == db.TaskStateQueued {
			_, err = taskTransition(q, task.Id, "first_execution", "")
		}
		return err
	})
	request := func(id, hex string) contract.PublishingRerunFailedRequest {
		return contract.PublishingRerunFailedRequest{ProjectId: &proj, SourceExecutionId: &source, RequestId: id, Number: 13,
			ExpectedHeadSha: strings.Repeat(hex, 40), RunId: 55, Attempt: 1, Reason: "Fixture transient failure"}
	}
	submit := func(r contract.PublishingRerunFailedRequest) contract.PublishingRerunFailedReply {
		t.Helper()
		var out contract.PublishingRerunFailedReply
		e.opInto(t, "publishing_rerun_failed", r, h, &out)
		return out
	}
	reject := func(r contract.PublishingRerunFailedRequest, code int) {
		t.Helper()
		if status, got := e.callJSON(t, "POST", "/api/ops/publishing_rerun_failed", r, h); status != code {
			t.Fatalf("submit %q = %d %v, want %d", r.RequestId, status, got, code)
		}
	}
	reject(request("no-opt-in", "a"), 403)
	settings["integration"] = doc{"mode": "pr", "autoMerge": false, "ciRerun": policy}
	var updated projectWriteResult
	e.opInto(t, "project_update", doc{"requestId": proj + "-grant", "projectId": proj, "baseRevision": project.Revision,
		"content": projectContent}, h, &updated)

	var wg sync.WaitGroup
	concurrent := make([]contract.PublishingRerunFailedReply, 2)
	for i, id := range []string{"first", "duplicate"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			concurrent[i] = submit(request(id, "a"))
		}()
	}
	wg.Wait()
	if concurrent[0].Id == "" || concurrent[0].Id != concurrent[1].Id || concurrent[0].State != "queued" {
		t.Fatalf("duplicate callers did not share a round: %+v", concurrent)
	}
	r := request("first", "a")
	r.RunId++
	reject(r, 409)
	if retry := submit(request("first", "a")); retry.Id != concurrent[0].Id {
		t.Fatalf("request replay created a round: %+v", retry)
	}
	readRow := func(key string) db.PublishingCiRecovery {
		t.Helper()
		var row db.PublishingCiRecovery
		write(func(q *Request) error {
			var err error
			row, err = q.db().CIRecoveryGet(q.ctx(), key)
			return err
		})
		return row
	}
	advance := func(key string, at time.Time) {
		t.Helper()
		write(func(q *Request) error { return ciRecoveryStep(q, at, &kitClaim{Key: key}) })
	}
	loadCommand := func(row db.PublishingCiRecovery) (contract.PublishingCIClaim, contract.PublishingCommandPayload) {
		t.Helper()
		var c agentCommand
		write(func(q *Request) error {
			var err error
			c, err = commandGet(q, deref(row.CommandID))
			return err
		})
		var p contract.PublishingCommandPayload
		if err := json.Unmarshal(c.Payload, &p); err != nil {
			t.Fatal(err)
		}
		claim := contract.PublishingCIClaim{HostId: c.HostID, CommandId: c.ID, ExecutionId: c.ExecutionID, RecoveryId: row.ID,
			OriginalJobs: []contract.PublishingCIFailedJob{{Id: 101, Name: "go", HeadSha: row.HeadSha}}}
		return claim, p
	}
	callClaim := func(b contract.PublishingCIClaim, want int) {
		t.Helper()
		if code, got := e.callJSON(t, "POST", contract.RoutePublishingCiClaim.Path, b, hostHeaders); code != want {
			t.Fatalf("claim = %d %v, want %d", code, got, want)
		}
	}
	report := func(b contract.PublishingCIClaim, claimed bool, out contract.PublishingCIObservation) {
		t.Helper()
		req := contract.PublishingCIReport{HostId: b.HostId, CommandId: b.CommandId, ExecutionId: b.ExecutionId,
			RecoveryId: b.RecoveryId, Claimed: claimed, Observation: out}
		if code, got := e.callJSON(t, "POST", contract.RoutePublishingCiObservation.Path, req, hostHeaders); code != 200 {
			t.Fatalf("observation = %d %v", code, got)
		}
	}

	first := concurrent[0]
	advance(first.Id, time.Now())
	row := readRow(first.Id)
	claim, cmd := loadCommand(row)
	if cmd.CiRecovery.ObserveOnly {
		t.Fatal("first reservation is observation-only")
	}
	callClaim(claim, 200)
	callClaim(claim, 409)
	// An unclaimed duplicate 429 receipt cannot reopen the spent claim.
	report(claim, false, contract.PublishingCIObservation{State: "rate_limited", Reason: "provider_read_rate_limited"})
	if row := readRow(first.Id); row.State != "dispatch_unknown" {
		t.Fatalf("lost receipt = %s", row.State)
	}

	// Simulate host loss after claim and POST. The next command only observes, and the original cannot dispatch again.
	advance(first.Id, row.CommandUntil.Add(time.Millisecond))
	obs, cmd := loadCommand(readRow(first.Id))
	if obs.CommandId == claim.CommandId || !cmd.CiRecovery.ObserveOnly || cmd.CiRecovery.Dispatched {
		t.Fatalf("lost receipt did not schedule unattributed readback: %+v", cmd.CiRecovery)
	}
	callClaim(obs, 409)
	callClaim(claim, 409)
	report(obs, false, contract.PublishingCIObservation{State: "handoff", Reason: "replacement_workflow_failed_attribution_unknown",
		Attempt: ptr(int64(2)), FailedJobs: []contract.PublishingCIFailedJob{{Id: 102, Name: "go", HeadSha: first.HeadSha}}})
	advance(first.Id, time.Now())
	report(obs, false, contract.PublishingCIObservation{State: "passed", Reason: "late duplicate"})
	var records contract.PublishingCiRecoveryReply
	e.opInto(t, "publishing_ci_recovery", contract.PublishingCiRecoveryRequest{ProjectId: proj, ExpectedHeadSha: &first.HeadSha}, h, &records)
	if len(records.Recoveries) != 1 || records.Recoveries[0].State != "handoff" || deref(records.Recoveries[0].Handoff) != "submitted" ||
		len(records.Recoveries[0].Evidence.OriginalJobs) != 1 || len(records.Recoveries[0].Evidence.FailedJobs) != 1 {
		t.Fatalf("read-only handoff evidence: %+v", records)
	}
	write(func(q *Request) error {
		messages, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: &sid, RequestID: ptr("ci-recovery:" + first.Id)})
		if err == nil && (len(messages) != 1 || !strings.Contains(messages[0].Text, `"id":102`)) {
			t.Fatalf("worker handoff: %+v", messages)
		}
		return err
	})

	rate := submit(request("throttle", "b"))
	advance(rate.Id, time.Now())
	b1, _ := loadCommand(readRow(rate.Id))
	callClaim(b1, 200)
	retryAt := time.Now().Add(3 * time.Minute).UnixMilli()
	report(b1, true, contract.PublishingCIObservation{State: "rate_limited", Reason: "provider_post_rate_limited", RetryAt: &retryAt})
	advance(rate.Id, time.Now())
	throttled := readRow(rate.Id)
	if throttled.CommandID != nil || throttled.State != "rate_limited" || throttled.NextAttemptAt.UnixMilli() != retryAt {
		t.Fatalf("429 did not respect the provider deadline: %+v", throttled)
	}
	advance(rate.Id, throttled.NextAttemptAt.Add(time.Millisecond))
	b2, cmd := loadCommand(readRow(rate.Id))
	if cmd.CiRecovery.ObserveOnly {
		t.Fatal("a definite rejected POST did not allow a fresh claim")
	}
	callClaim(b1, 409)
	callClaim(b2, 200)

	wrongRun := submit(request("queue-run", "f"))
	advance(wrongRun.Id, time.Now())
	wrong, _ := loadCommand(readRow(wrongRun.Id))
	report(wrong, false, contract.PublishingCIObservation{State: "refused", Reason: "merge_queue_requires_clean_reentry"})
	correct := request("head-run", "f")
	correct.RunId++
	if retry := submit(correct); retry.Id == wrongRun.Id || retry.State != "queued" {
		t.Fatalf("pre-dispatch refusal consumed the head allowance: %+v", retry)
	}
	if prior := submit(request("queue-run", "f")); prior.Id != wrongRun.Id || prior.State != "refused" {
		t.Fatalf("a released request should replay its own refusal: %+v", prior)
	}

	fenced := submit(request("policy-veto", "1"))
	advance(fenced.Id, time.Now())
	vetoed, _ := loadCommand(readRow(fenced.Id))
	settings["integration"] = doc{"mode": "pr", "ciRerun": nil}
	e.opInto(t, "project_update", doc{"requestId": proj + "-disable", "projectId": proj, "baseRevision": updated.Revision,
		"content": projectContent}, h, &updated)
	callClaim(vetoed, 403)
	settings["integration"] = doc{"mode": "pr", "ciRerun": policy}
	e.opInto(t, "project_update", doc{"requestId": proj + "-restore", "projectId": proj, "baseRevision": updated.Revision,
		"content": projectContent}, h, &updated)

	for _, veto := range []struct {
		op, head string
		code     int
	}{{"pause", "c", 409}, {"revoke", "d", 403}, {"cancel", "e", 409}} {
		reserved := submit(request(veto.op, veto.head))
		advance(reserved.Id, time.Now())
		b, _ := loadCommand(readRow(reserved.Id))
		token := "invented-ci-test-token"
		write(func(q *Request) error {
			if veto.op == "revoke" {
				return runTokenPut(q, runToken{ID: token, ExecutionID: &source, ProjectID: &proj, RevokedAt: jsonBytes(nowMs())})
			}
			_, err := taskTransition(q, task.Id, veto.op, "")
			return err
		})
		callClaim(b, veto.code)
		if veto.op == "cancel" {
			break
		}
		write(func(q *Request) error {
			if veto.op == "revoke" {
				return runTokenPut(q, runToken{ID: token, ExecutionID: &source, ProjectID: &proj})
			}
			_, err := taskTransition(q, task.Id, "resume", "")
			return err
		})
	}
	resumed := newID()
	write(func(q *Request) error {
		return executionWrite(q, &agentExecution{ID: resumed, SessionID: sid, HostID: ptr(host), ProjectID: proj,
			TaskID: task.Id, DirectedBy: testOwnerEmail, State: "succeeded", CreatedAt: now.UnixMilli()})
	})
	replay := request("first", "a")
	replay.SourceExecutionId = &resumed
	if prior := submit(replay); prior.Id != first.Id || prior.State != "handoff" {
		t.Fatalf("continuation did not recover its prior request: %+v", prior)
	}

}
