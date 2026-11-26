package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
)

// Load the actual coordinator catalogue: a copied fixture would miss a wrong
// auth header, version, tool route or replay flag in the shipped snapshot.
func newAnthropicFixture(t *testing.T, url string) *fixture {
	t.Helper()
	source, err := os.ReadFile("../coordinator/providers.go")
	if err != nil {
		t.Fatal(err)
	}
	_, literal, ok := strings.Cut(string(source), "const providersJSON = `")
	if !ok {
		t.Fatal("provider catalogue literal missing")
	}
	literal, _, _ = strings.Cut(literal, "`")
	var catalogue map[string]any
	if err := json.Unmarshal([]byte(literal), &catalogue); err != nil {
		t.Fatal(err)
	}
	if catalogue["anthropic"] == nil {
		t.Fatal("Anthropic catalogue missing")
	}
	f := newFixture(t, url, "invented-anthropic-primary", "invented-anthropic-secondary")
	f.halt()
	s := testSnapshot(url, []keyState{{"invented-anthropic-primary", "active"}, {"invented-anthropic-secondary", "active"}}, []string{testToken}, "all")
	p := catalogue["anthropic"].(map[string]any)
	p["baseUrl"] = url
	s["providers"] = map[string]any{"anthropic": p}
	// An arbitrary connection name must work; provider identity selects behavior.
	c := s["connections"].(map[string]any)["tavily"].(map[string]any)
	c["provider"], c["tools"] = "anthropic", []string{"create_message", "list_models"}
	f.write(s)
	f.start()
	return f
}

const nativeMessage = `{"model":"invented-model","max_tokens":32,"messages":[{"role":"user","content":"invented prompt"}],"stream":false}`

// Requirement: native Anthropic requests/replies and actual token attribution (fleet-connections.md).
// Risk: wrong auth/version, lost native content, secret leakage or invented billing/usage.
func TestAnthropicNative(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("x-api-key") != "invented-anthropic-primary" || r.Header.Get("Authorization") != "" || r.Header.Get("anthropic-version") != "2026-10-21" || r.Header.Get("Content-Type") != "application/json" {
			t.Error("incorrect native headers")
		}
		data, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/models" {
			if r.Method != "GET" || len(data) != 0 || r.URL.Query().Get("after_id") != "model/&?invented" || r.URL.Query().Get("limit") != "2" {
				t.Error("incorrect model list request")
			}
			io.WriteString(w, `{"data":[{"type":"model","id":"invented-model"}],"has_more":true,"last_id":"invented-model"}`)
		} else {
			if r.Method != "POST" || r.URL.Path != "/v1/messages" {
				t.Error("incorrect Messages route")
			}
			var got, want any
			json.Unmarshal(data, &got)
			json.Unmarshal([]byte(nativeMessage), &want)
			expect(t, got, want, "native request")
			io.WriteString(w, `{"id":"invented-message","type":"message","model":"invented-model","content":[{"type":"text","text":"invented-anthropic-primary"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":7,"cache_creation_input_tokens":3,"cache_read_input_tokens":4,"cache_creation":{"ephemeral_5m_input_tokens":3},"output_tokens_details":{"thinking_tokens":2}}}`)
		}
	}))
	defer server.Close()
	f := newAnthropicFixture(t, server.URL)
	status, _, body := f.raw("POST", "/v1/call/tavily/create_message", "Bearer "+testToken, nativeMessage)
	expect(t, status, 200, "native status")
	expect(t, field(body, "content"), []any{map[string]any{"type": "text", "text": "[key]"}}, "redacted native content")
	expect(t, body["stop_reason"], "end_turn", "native stop reason")
	status, _, body = f.raw("POST", "/v1/call/tavily/list_models", "Bearer "+testToken, `{"limit":2,"after_id":"model/&?invented"}`)
	expect(t, []any{status, body["has_more"]}, []any{200, true}, "native model page")
	lines, text := f.spool()
	expect(t, len(lines), 2, "call count")
	expect(t, field(lines[0], "usage"), map[string]any{"unit": "tokens", "amount": 24}, "actual tokens without double counting")
	expect(t, field(lines[0], "tokens"), map[string]any{"input": 10, "output": 7, "cacheRead": 4, "cacheWrite": 3}, "token split")
	expect(t, lines[0]["model"], "invented-model", "answering model")
	expect(t, lines[0]["executionId"], "exec-1", "attribution")
	expect(t, lines[0]["keyFingerprint"], fp("invented-anthropic-primary"), "key identity")
	if lines[1]["usage"] != nil || strings.Contains(text, "invented-anthropic-") {
		t.Fatal("invented usage or secret in spool")
	}
	expect(t, calls.Load(), int64(2), "provider calls")
}

// Requirement: bounded native operations, nonstreaming only (fleet-connections.md).
// Risk: true/invalid streaming or URL/header/body overrides reach a paid provider.
func TestAnthropicInputBoundary(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); io.WriteString(w, `{}`) }))
	defer server.Close()
	f := newAnthropicFixture(t, server.URL)
	for _, body := range []string{
		strings.Replace(nativeMessage, `"stream":false`, `"stream":true`, 1),
		strings.Replace(nativeMessage, `"stream":false`, `"stream":null`, 1),
		strings.Replace(nativeMessage, `"stream":false`, `"stream":"false"`, 1),
		strings.Replace(nativeMessage, `"stream":false`, `"url":"https://invented.invalid"`, 1),
		strings.Replace(nativeMessage, `"stream":false`, `"headers":{"x-api-key":"caller"}`, 1),
		strings.Replace(nativeMessage, `"max_tokens":32`, `"max_tokens":0`, 1),
		strings.Replace(nativeMessage, `"model":"invented-model"`, `"model":null`, 1),
		`{"model":"invented-model","max_tokens":1,"messages":[]}`,
	} {
		status, _, _ := f.raw("POST", "/v1/call/tavily/create_message", "Bearer "+testToken, body)
		expect(t, status, 400, "invalid native input")
	}
	for _, body := range []string{`{"limit":0}`, `{"limit":1001}`, `{"limit":1.5}`, `{"after_id":null}`, `{"url":"https://invented.invalid"}`} {
		status, _, _ := f.raw("POST", "/v1/call/tavily/list_models", "Bearer "+testToken, body)
		expect(t, status, 400, "invalid list input")
	}
	expect(t, calls.Load(), int64(0), "refusals stay local")
}

// Requirement: missing measurements and failed model reads stay unavailable.
// Risk: malformed or missing usage becomes measured zero, or failed reads look like empty availability.
func TestAnthropicUnknownUsageAndReads(t *testing.T) {
	var reply string
	var status int = 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status); io.WriteString(w, reply) }))
	defer server.Close()
	f := newAnthropicFixture(t, server.URL)
	for _, usage := range []string{`{}`, `{"input_tokens":3}`, `{"input_tokens":null,"output_tokens":2}`, `{"input_tokens":-1,"output_tokens":2}`, `{"input_tokens":1.5,"output_tokens":2}`, `{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":"bad"}`, `{"input_tokens":9223372036854775807,"output_tokens":1}`} {
		reply = `{"usage":` + usage + `}`
		f.raw("POST", "/v1/call/tavily/create_message", "Bearer "+testToken, nativeMessage)
		lines, _ := f.spool()
		if lines[len(lines)-1]["usage"] != nil {
			t.Fatal("malformed usage measured")
		}
	}
	for _, code := range []int{403, 429, 503} {
		status = code
		reply = `{"type":"error","error":{"type":"invented"}}`
		got, h, body := f.raw("POST", "/v1/call/tavily/list_models", "Bearer "+testToken, `{}`)
		expect(t, got, code, "failed read status")
		expect(t, h.Get("X-Fleet-Outcome"), "error", "failed read outcome")
		if body["data"] != nil {
			t.Fatal("invented model availability")
		}
	}
}
