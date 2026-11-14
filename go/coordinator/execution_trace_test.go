package main

import (
	"context"
	"encoding/json"
	"fleet/contract"
	"fleet/obs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: persisted origin survives detached dispatch/retry and discovery uses execution RLS.
// Risk: polling/restarting changes trace identity, reveals unrelated work, or reuses its parent's span id.
func TestDurableCommandTraceAndDiscovery(t *testing.T) {
	env := openTestCoordinator(t, false)
	host := "h-trace"
	secret := env.registerHost(t, host)
	ctx, parent := obs.Start(context.Background(), "fixture.request", obs.KindServer)
	var dispatched []*obs.Span
	obs.SetSink(func(s *obs.Span) {
		if s.Name == "dispatch.command" {
			dispatched = append(dispatched, s)
		}
	})
	defer obs.SetSink(nil)
	if err := runTx(ctx, env.app, nil, true, "", func(q *Request) error {
		return put(q, "execution", doc{"id": "x-trace", "executionId": "x-trace", "actor": "program-worker", "hostId": host, "state": "preparing"})
	}); err != nil {
		t.Fatal(err)
	}
	// Detached transaction models dispatch after request completion or coordinator restart.
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		return put(q, "command", doc{"id": "c-trace", "executionId": "x-trace", "hostId": host, "kind": "script", "status": "pending", "payload": doc{"command": "true"}})
	}); err != nil {
		t.Fatal(err)
	}
	code, poll := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{HostId: host, Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}}, map[string]string{"Authorization": "Bearer " + secret})
	if code != 200 {
		t.Fatalf("poll %d %v", code, poll)
	}
	commands := poll["commands"].([]any)
	if len(commands) != 1 {
		t.Fatalf("commands %v", commands)
	}
	raw, _ := json.Marshal(commands[0])
	var c contract.Command
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	tp := deref(c.Traceparent)
	trace, span, ok := obs.ParseTraceparent(tp)
	if !ok || trace != parent.TraceID || span == parent.SpanID || c.Run == nil || deref(c.Run.ExecutionId) != "x-trace" {
		t.Fatalf("command %s", raw)
	}
	if len(dispatched) != 1 || dispatched[0].ParentSpanID != parent.SpanID || dispatched[0].SpanID != span {
		t.Fatalf("dispatch %+v", dispatched)
	}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		saved, err := get(q, "c-trace", "command")
		if err != nil {
			return err
		}
		saved["status"] = "accepted"
		if err := put(q, "command", saved); err != nil {
			return err
		}
		again, err := get(q, "c-trace", "command")
		if err != nil {
			return err
		}
		if again["traceparent"] != tp {
			t.Fatal("retry changed parent")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(dispatched) != 1 {
		t.Fatal("retry emitted dispatch twice")
	}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		_, err := insertExecution(q, doc{"id": "typed-trace", "kind": "pipeline_run", "parentExecutionId": "x-trace", "data": doc{"retained": "value"}})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		data, err := one(q, "SELECT data FROM execution WHERE id=$1", "typed-trace")
		if err != nil {
			return err
		}
		if data["traceparent"] != "00-"+parent.TraceID+"-"+parent.SpanID+"-01" || data["retained"] != "value" {
			t.Fatalf("lost typed parent origin %v", data)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	oldStore := spanStore
	defer func() { spanStore = oldStore }()
	requests := 0
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Query().Get("param_t") != trace {
			t.Error("wrong discovered trace")
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte("{\"data\":[]}"))
	}))
	defer ch.Close()
	spanStore = &obs.ClickHouse{URL: ch.URL}
	code, body := env.callJSON(t, "POST", "/api/ops/telemetry_trace", doc{"executionId": "x-trace"}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("discovery %d %v", code, body)
	}
	unpriv := &Request{app: env.app, r: httptest.NewRequest("POST", "/", nil), person: &Person{Email: "outsider@trial.test"}}
	_, denied := telemetryTrace(unpriv, &contract.TelemetryTraceRequest{ExecutionId: ptr("x-trace")})
	if denied == nil || requests != 1 {
		t.Fatal("ungranted execution reached span store")
	}
	code, _ = env.callJSON(t, "POST", "/api/ops/telemetry_trace", doc{"executionId": "missing"}, env.ownerHeaders())
	if code == 200 || requests != 1 {
		t.Fatal("missing execution reached store")
	}
	code, _ = env.callJSON(t, "POST", "/api/ops/telemetry_trace", doc{"traceId": strings.Repeat("0", 32), "executionId": "x-trace"}, env.ownerHeaders())
	if code != 400 {
		t.Fatal("ambiguous lookup accepted")
	}
}
