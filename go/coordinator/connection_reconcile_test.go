package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: docs/designs/fleet-connections.md §2 Reconcile: failed top-level usage reconciliation reads unknown, never as a match.
// Risk: transport, non-2xx, or malformed /v1/usage replies silently retain stale reconcile results without marking unknown.
func TestConnReconcileTopLevelFailures(t *testing.T) {
	t.Setenv("FLEET_SECRET_DIR", t.TempDir())
	t.Setenv("FLEET_GATEWAY_DIR", t.TempDir())
	env := openTestCoordinator(t, false)

	mustOp := func(name string, req any) map[string]any {
		t.Helper()
		code, body := env.callJSON(t, "POST", "/api/ops/"+name, req, env.ownerHeaders())
		if code != 200 {
			t.Fatalf("%s: %d %v", name, code, body)
		}
		return body
	}
	mustOp("connections_create", doc{"name": "tavily", "provider": "tavily"})
	mustOp("connections_create", doc{"name": "anthropic", "provider": "anthropic"})
	keySecret := "tvly-reconcile-secret-value"
	keyReply := mustOp("connections_key_add", doc{"connection": "tavily", "value": keySecret})
	fp := keyReply["fingerprint"].(string)
	retiredReply := mustOp("connections_key_add", doc{"connection": "tavily", "value": "tvly-retired-secret-value"})
	retiredFP := retiredReply["fingerprint"].(string)
	mustOp("connections_key_state", doc{"keyId": retiredFP, "state": "retired"})
	mustOp("connections_key_add", doc{"connection": "anthropic", "value": "anth-no-usage-secret-value"})

	var status int
	var payload string
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/usage" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+usageToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	}))
	t.Cleanup(gw.Close)
	t.Setenv("FLEET_GATEWAY_URL", gw.URL)

	readKey := func(conn, targetFP string) map[string]any {
		t.Helper()
		listed := mustOp("connections_list", doc{})
		raw, _ := json.Marshal(listed)
		if strings.Contains(string(raw), keySecret) || strings.Contains(string(raw), usageToken) {
			t.Fatalf("connections_list leaked a secret")
		}
		for _, c := range listed["connections"].([]any) {
			cm := c.(map[string]any)
			if cm["name"] != conn {
				continue
			}
			for _, k := range cm["keys"].([]any) {
				km := k.(map[string]any)
				if km["fingerprint"] == targetFP {
					return km
				}
			}
		}
		t.Fatalf("key %s/%s not found", conn, targetFP)
		return nil
	}

	t1 := nowMs() - 2000
	t2 := nowMs() - 1000
	status = http.StatusOK
	payload = js(doc{"at": t1, "keys": []doc{{"key": fp, "usage": 10}}})
	connReconcile(env.ctx, env.app)
	payload = js(doc{"at": t2, "keys": []doc{{"key": fp, "usage": 15}}})
	connReconcile(env.ctx, env.app)

	rc := readKey("tavily", fp)["reconcile"].(map[string]any)
	if rc["unknown"] != nil || rc["providerDelta"] != float64(5) || rc["recorded"] != float64(0) || rc["difference"] != float64(5) || rc["at"] != float64(t2) {
		t.Fatalf("unexpected prior reconcile: %v", rc)
	}

	checkFailure := func(label, wantSubstr string) {
		t.Helper()
		connReconcile(env.ctx, env.app)
		got := readKey("tavily", fp)["reconcile"].(map[string]any)
		unk, _ := got["unknown"].(string)
		if unk == "" || !strings.Contains(unk, wantSubstr) {
			t.Fatalf("%s: unknown = %q, want substring %q", label, unk, wantSubstr)
		}
		if strings.Contains(unk, usageToken) || strings.Contains(unk, keySecret) {
			t.Fatalf("%s: unknown leaked a secret: %q", label, unk)
		}
		at, _ := got["at"].(float64)
		if at <= float64(t2) {
			t.Fatalf("%s: reconcile.at = %v, want > %d", label, at, t2)
		}
		if got["providerDelta"] != float64(5) || got["recorded"] != float64(0) || got["difference"] != float64(5) {
			t.Fatalf("%s: prior measurements not preserved: %v", label, got)
		}
		if r := readKey("tavily", retiredFP)["reconcile"]; r != nil {
			t.Fatalf("%s: retired key got reconcile %v", label, r)
		}
		for _, c := range mustOp("connections_list", doc{})["connections"].([]any) {
			cm := c.(map[string]any)
			if cm["name"] == "anthropic" {
				ak := cm["keys"].([]any)[0].(map[string]any)
				if ak["reconcile"] != nil {
					t.Fatalf("%s: provider without usage got reconcile %v", label, ak["reconcile"])
				}
			}
		}
	}

	// Non-2xx reply.
	status = http.StatusBadGateway
	payload = `{"error":"bad gateway"}`
	checkFailure("non-2xx", "502")

	// Malformed top-level JSON (invalid syntax and missing keys field).
	status = http.StatusOK
	payload = `{broken`
	checkFailure("invalid-json", "invalid JSON")
	payload = `{"at":123}`
	checkFailure("missing-keys", "invalid JSON")

	// Transport failure.
	gw.Close()
	checkFailure("transport", "usage request:")

	// Subsequent 200 clears unknown and preserves baseline 10.
	t5 := nowMs() + 1000
	gw2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(js(doc{"at": t5, "keys": []doc{{"key": fp, "usage": 18}}})))
	}))
	defer gw2.Close()
	t.Setenv("FLEET_GATEWAY_URL", gw2.URL)
	connReconcile(env.ctx, env.app)
	recovered := readKey("tavily", fp)["reconcile"].(map[string]any)
	if recovered["unknown"] != nil || recovered["at"] != float64(t5) || recovered["providerDelta"] != float64(8) || recovered["difference"] != float64(8) {
		t.Fatalf("recovery reconcile = %v", recovered)
	}
}
