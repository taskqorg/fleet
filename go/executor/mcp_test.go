package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Risk: a provider charge is reported as a plain error (or the reverse), so an agent repeats a paid call
// (fleet-connections.md §5). Fake gateway; one case per outcome rule.
func TestConnectionCallOutcomes(t *testing.T) {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(401)
			return
		}
		w.Header().Set("x-fleet-call-id", "c1")
		switch r.URL.Path {
		case "/v1/call/web/ok":
			w.Write([]byte(`{"usage":{"credits":1},"r":1}`))
		case "/v1/call/web/garbled":
			w.Write([]byte(`not json`))
		case "/v1/call/web/provider":
			w.Header().Set("x-fleet-error", "provider-502")
			w.WriteHeader(502)
			w.Write([]byte(`{}`))
		case "/v1/call/web/timeout":
			w.Header().Set("x-fleet-error", "provider-timeout")
			w.WriteHeader(504)
		}
	}))
	defer gw.Close()
	t.Setenv("FLEET_GATEWAY_URL", gw.URL)
	t.Setenv("FLEET_RUN_TOKEN", "tok")
	for tool, want := range map[string][2]any{"ok": {"ok", nil}, "garbled": {"unknown", "invalid-response"}, "provider": {"error", "http-502"}, "timeout": {"unknown", "provider-timeout"}} {
		r := connectionCall("web", tool, obj{})
		if r["outcome"] != want[0] || r["error_code"] != want[1] || r["callId"] != "c1" {
			t.Fatal(tool, r)
		}
	}
	gw.Close()
	if r := connectionCall("web", "ok", obj{}); r["outcome"] != "error" || r["error_code"] != "unreachable" {
		t.Fatal("refused connection", r)
	}
}
