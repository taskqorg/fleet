package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"fleet/contract"

	"github.com/jackc/pgx/v5/pgconn"
)

// Requirement: one command whose handoff fails (an authority refusal, a missing execution) is stopped with its reason
// and never handed over, while the other commands of the same poll are delivered.
// Risk: one poisoned row fails every poll of the host, which then gets no work and no update target (2026-11-17).
func TestPollIsolatesPoisonedCommands(t *testing.T) {
	env := openTestCoordinator(t, false)
	env.registerHost(t, "iso-host")
	ids := map[string]string{}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		for _, id := range []string{"good", "refused"} {
			run := planFixture(t, id)
			if err := putRun(q, run); err != nil {
				return err
			}
			if err := selectPipelineStep(q, run, 0); err != nil {
				return err
			}
			nativeID, err := startScript(q, run, stepRow(run, 0), "fixture-not-executed", nil, "iso-host", []any{})
			if err != nil {
				return err
			}
			step := mapOf(anys(run["steps"])[0])
			step["state"], step["executionId"] = "running", nativeID
			commands, err := all(q, "command", doc{"executionId": nativeID, "kind": "script"})
			if err != nil || len(commands) != 1 {
				return fmt.Errorf("script command: %v %d", err, len(commands))
			}
			dispatch := mapOf(pipelinePlanEntry(run, 0)["dispatch"])
			dispatch["phase"], dispatch["nativeExecutionId"], dispatch["commandId"], dispatch["hostId"] = "prepared", nativeID, commands[0]["id"], "iso-host"
			if err := putRun(q, run); err != nil {
				return err
			}
			ids[id] = astr(commands[0]["id"])
		}
		// The refused run's initiating actor is no longer a person: its authority stamp is a 403.
		if err := exec(q, `UPDATE execution SET data=jsonb_set(data,'{authority,actorEmail}','"gone@example.com"') WHERE id='refused'`); err != nil {
			return err
		}
		// A command of an execution that no longer exists.
		orphan := doc{"id": newID(), "executionId": "no-such-execution", "hostId": "iso-host", "kind": "input", "payload": doc{}, "status": "pending"}
		ids["orphan"] = astr(orphan["id"])
		return put(q, "command", orphan)
	}); err != nil {
		t.Fatal(err)
	}
	out, err := env.app.pollReadyCommands(env.ctx, "iso-host", nil)
	if err != nil {
		t.Fatal("one poisoned command failed the poll:", err)
	}
	got := map[string]bool{}
	for _, c := range out {
		got[astr(c["id"])] = true
	}
	if !got[ids["good"]] || got[ids["refused"]] || got[ids["orphan"]] || len(out) != 1 {
		t.Fatalf("delivered %v, want only the good script %s", out, ids["good"])
	}
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		for id, want := range map[string]string{"refused": "refused: Pipeline initiating actor no longer has project access", "orphan": "handoff failed: Not found"} {
			c, err := get(q, ids[id], "command")
			if err != nil {
				return err
			}
			if c["status"] != "stopped" || c["stopDetail"] != want {
				return fmt.Errorf("%s command is %v (%v), want stopped with %q", id, c["status"], c["stopDetail"], want)
			}
			if mapOf(c["payload"])["runToken"] != nil {
				return fmt.Errorf("%s command carries a run token", id)
			}
		}
		run, err := getRun(q, "refused")
		if err != nil {
			return err
		}
		native, err := get(q, mapOf(pipelinePlanEntry(run, 0)["dispatch"])["nativeExecutionId"], "execution")
		if err != nil {
			return err
		}
		if native["state"] != "cancelled" || native["stopDetail"] != "refused: Pipeline initiating actor no longer has project access" {
			return fmt.Errorf("refused script's execution is %v (%v), want cancelled with the reason", native["state"], native["stopDetail"])
		}
		if phase := mapOf(pipelinePlanEntry(run, 0)["dispatch"])["phase"]; phase != "prepared" {
			return fmt.Errorf("refused run's dispatch is %v, want still prepared (never handed off)", phase)
		}
		run, err = getRun(q, "good")
		if err != nil {
			return err
		}
		if phase := mapOf(pipelinePlanEntry(run, 0)["dispatch"])["phase"]; phase != "handed_off" {
			return fmt.Errorf("good run's dispatch is %v, want handed_off", phase)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestPollCommandRefusal(t *testing.T) {
	for _, c := range []struct {
		err     error
		reason  string
		outcome pollCommandOutcome
	}{
		{fail(403, "Pipeline connection or tool is no longer granted"), "refused: Pipeline connection or tool is no longer granted", pollStopsCommand},
		{fail(404, "Not found"), "handoff failed: Not found", pollStopsCommand},
		{fail(409, "Pipeline pinned profile changed before dispatch"), "handoff failed: Pipeline pinned profile changed before dispatch", pollStopsCommand},
		{fail(503, "store unreachable"), "", pollRetriesCommand},
		{fail(429, "busy"), "", pollRetriesCommand},
		{&pgconn.PgError{Code: "40001"}, "", pollFails},
		{&pgconn.PgError{Code: "40P01"}, "", pollFails},
		{&pgconn.PgError{Code: "55P03"}, "", pollRetriesCommand},
		{&pgconn.PgError{Code: "22P02"}, "handoff failed: database error 22P02", pollStopsCommand},
		{&pgconn.PgError{Code: "42501"}, "refused: not in scope", pollStopsCommand},
		{fmt.Errorf("wrapped: %w", context.Canceled), "", pollFails},
	} {
		if reason, outcome := pollCommandRefusal(c.err); reason != c.reason || outcome != c.outcome {
			t.Errorf("%v: %q %v, want %q %v", c.err, reason, outcome, c.reason, c.outcome)
		}
	}
}

func TestPollFailureOf(t *testing.T) {
	f, ok := pollFailureOf(&HTTPError{Code: 409, Msg: "Unsupported", Extra: doc{"reason": "pipeline_format_unsupported"}})
	if !ok || f.status != 409 || f.reason != "pipeline_format_unsupported" || !internalPollError(f) {
		t.Fatalf("409 %+v %v", f, ok)
	}
	if f, _ := pollFailureOf(&HTTPError{Code: 409, Msg: "x", Extra: doc{"reason": "Free Text <b>"}}); f.reason != "http_409" {
		t.Fatalf("free-text reason became %q", f.reason)
	}
	if f, ok := pollFailureOf(fail(400, "Invalid body")); !ok || f.reason != "http_400" || internalPollError(f) {
		t.Fatalf("400 %+v %v: recorded, but a client error, not for Bugsink", f, ok)
	}
	if _, ok := pollFailureOf(&HTTPError{Code: 410, Msg: "host-removed"}); ok {
		t.Fatal("a removed host recorded a poll failure")
	}
	if f, _ := pollFailureOf(fmt.Errorf("%s", strings.Repeat("é", 400))); f.status != 500 || f.reason != "internal" || len([]rune(f.message)) != hostPollMessageRunes || internalPollError(f) {
		t.Fatalf("500 %+v", f)
	}
}

// Requirement: a failing host poll is recorded by the coordinator (status, reason code, first-failure time), shown in
// hosts_list and system_versions, opens one needs-you problem after two minutes, and is cleared with the problem
// withdrawn by the next successful poll.
// Risk: hosts look healthy (lastSeenAt moves) while every poll fails, and nothing surfaces it.
func TestHostPollFailureRecordAndProblem(t *testing.T) {
	env := openTestCoordinator(t, false)
	secret := env.registerHost(t, "sick-host")
	hdr := map[string]string{"Authorization": "Bearer " + secret}
	good := contract.PollRequest{HostId: "sick-host", Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}}
	if code, body := env.callJSON(t, "POST", "/api/hosts/poll", good, hdr); code != 200 {
		t.Fatalf("poll %d %v", code, body)
	}
	pollError := func(op string) doc {
		t.Helper()
		code, body := env.callJSON(t, "POST", "/api/ops/"+op, doc{}, env.ownerHeaders())
		if code != 200 {
			t.Fatalf("%s %d %v", op, code, body)
		}
		for _, h := range anys(body["hosts"]) {
			if mapOf(h)["id"] == "sick-host" {
				return mapOf(mapOf(h)["pollError"])
			}
		}
		t.Fatalf("%s has no sick-host: %v", op, body)
		return nil
	}
	question := func() doc {
		t.Helper()
		var q doc
		if err := runTx(env.ctx, env.app, nil, false, "", func(rq *Request) error {
			var err error
			q, err = first(rq, "SELECT id,state,text,cause FROM question WHERE data->>'cause'=$1 ORDER BY seq DESC LIMIT 1", hostPollCause("sick-host"))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return q
	}
	if pollError("hosts_list") != nil {
		t.Fatal("a healthy host shows a poll error")
	}

	// A poll whose body the coordinator cannot read answers 400 and is recorded on the server.
	if code, _ := env.callJSON(t, "POST", "/api/hosts/poll", doc{"platform": 5}, hdr); code != 400 {
		t.Fatalf("bad poll answered %d", code)
	}
	pe := pollError("hosts_list")
	if pe == nil || num(pe["status"]) != 400 || pe["reason"] != "http_400" || num(pe["since"]) <= 0 {
		t.Fatalf("recorded %v", pe)
	}
	since := num(pe["since"])
	// An internal failure keeps the first-failure time and takes the newer reason.
	env.app.notePollFailure(env.ctx, "sick-host", testOwnerEmail, &HTTPError{Code: 409, Msg: "Unsupported pipeline run format", Extra: doc{"reason": "pipeline_format_unsupported"}})
	for _, op := range []string{"hosts_list", "system_versions"} {
		pe := pollError(op)
		if pe == nil || num(pe["status"]) != 409 || pe["reason"] != "pipeline_format_unsupported" || num(pe["since"]) != since || pe["message"] != "Unsupported pipeline run format" {
			t.Fatalf("%s pollError %v", op, pe)
		}
	}
	if q := question(); q != nil {
		t.Fatal("problem opened before two minutes:", q)
	}
	// An observation flush (here a poll's own part, before its commands) keeps the record.
	env.app.clearHostCache("sick-host")
	body, _ := json.Marshal(good)
	if _, err := env.app.pollOnce(&Request{app: env.app, r: httptest.NewRequestWithContext(env.ctx, "POST", "/api/hosts/poll", nil), host: doc{"hostId": "sick-host", "holder": testOwnerEmail}, body: body}); err != nil {
		t.Fatal(err)
	}
	if pe := pollError("hosts_list"); pe == nil || num(pe["since"]) != since {
		t.Fatalf("a flush dropped the record: %v", pe)
	}
	// Two minutes later the next failure opens one problem.
	if err := runTx(env.ctx, env.app, nil, true, "host:sick-host", func(q *Request) error {
		return exec(q, "UPDATE document SET data=jsonb_set(data,'{pollError,since}',to_jsonb($2::float8)) WHERE kind='host-observation' AND id=$1", "sick-host", since-3*60*1000)
	}); err != nil {
		t.Fatal(err)
	}
	env.app.notePollFailure(env.ctx, "sick-host", testOwnerEmail, &HTTPError{Code: 409, Msg: "Unsupported pipeline run format", Extra: doc{"reason": "pipeline_format_unsupported"}})
	q := question()
	if q == nil || q["state"] != "open" || !strings.Contains(astr(q["text"]), "sick-host") || !strings.Contains(astr(q["text"]), "pipeline_format_unsupported") || strings.Contains(astr(q["text"]), "Unsupported pipeline run format") {
		t.Fatalf("problem %v", q)
	}
	env.app.notePollFailure(env.ctx, "sick-host", testOwnerEmail, &HTTPError{Code: 409, Msg: "again", Extra: doc{"reason": "pipeline_format_unsupported"}})
	var open int
	if err := runTx(env.ctx, env.app, nil, false, "", func(rq *Request) error {
		return rq.tx.QueryRow(rq.ctx(), "SELECT count(*) FROM question WHERE data->>'cause'=$1 AND state='open'", hostPollCause("sick-host")).Scan(&open)
	}); err != nil || open != 1 {
		t.Fatalf("open problems %d %v, want one", open, err)
	}

	// The next successful poll clears the record and withdraws the problem.
	if code, body := env.callJSON(t, "POST", "/api/hosts/poll", good, hdr); code != 200 {
		t.Fatalf("poll %d %v", code, body)
	}
	for _, op := range []string{"hosts_list", "system_versions"} {
		if pe := pollError(op); pe != nil {
			t.Fatalf("%s keeps %v after a good poll", op, pe)
		}
	}
	if q := question(); q == nil || q["state"] != "withdrawn" {
		t.Fatalf("problem after recovery %v", q)
	}
}
