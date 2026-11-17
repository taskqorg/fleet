// Gateway behaviour through the real server and a scripted provider (fleet-connections.md §2-3), ported from
// the Rust gateway's integration tests. Risk covered: a token, key or snapshot rule that passes in isolation but fails in the
// running server, and a key value that leaks to the caller or the spool.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const testToken = "invented-run-token"

func fp(k string) string { return sha(k)[:12] }

type seenCall struct{ key, body, optIn string }

type fakeProvider struct {
	url  string
	lock sync.Mutex
	seen []seenCall
}

func (p *fakeProvider) calls() []seenCall {
	p.lock.Lock()
	defer p.lock.Unlock()
	return append([]seenCall(nil), p.seen...)
}

// newProvider answers with script(key); status 0 cuts the connection without an answer.
func newProvider(t *testing.T, script func(key string) int) *fakeProvider {
	p := &fakeProvider{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		body, _ := io.ReadAll(r.Body)
		p.lock.Lock()
		p.seen = append(p.seen, seenCall{key, string(body), r.Header.Get("X-Opt-In")})
		p.lock.Unlock()
		status := script(key)
		if status == 0 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		if status == 429 {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(status)
		if strings.HasPrefix(r.URL.Path, "/usage") {
			io.WriteString(w, `{"key":{"usage":40}}`)
		} else {
			io.WriteString(w, `{"results":[{"title":"invented"}],"usage":{"credits":2}}`)
		}
	}))
	t.Cleanup(server.Close)
	p.url = server.URL
	return p
}

type keyState struct{ key, state string }

func testSnapshot(url string, keys []keyState, tokens []string, intake string) map[string]any {
	var ks []map[string]any
	for _, k := range keys {
		ks = append(ks, map[string]any{"id": fp(k.key), "state": k.state})
	}
	var ts []map[string]any
	for _, tk := range tokens {
		ts = append(ts, map[string]any{"sha256": sha(tk), "executionId": "exec-1", "projectId": "p1", "connections": []string{"tavily"}})
	}
	blocked := []string{}
	if intake == "blocked" {
		intake, blocked = "all", []string{"project:p1"}
	}
	return map[string]any{"schema": 2, "generated_ms": 1,
		"providers": map[string]any{"tavily": map[string]any{"baseUrl": url, "usage": map[string]any{"path": "/usage", "field": "key.usage"}, "tools": map[string]any{
			"web_search": map[string]any{"method": "POST", "path": "/search", "unit": "credits", "idempotent": true, "inject": map[string]any{"include_usage": true}, "headers": map[string]any{"X-Opt-In": "true"}},
			"submit":     map[string]any{"method": "POST", "path": "/submit", "unit": "credits", "idempotent": false},
			"status":     map[string]any{"method": "GET", "path": "/tasks/{jobId}", "unit": "credits", "idempotent": true}}}},
		"connections": map[string]any{"tavily": map[string]any{"provider": "tavily", "tools": []string{"web_search", "submit", "status"}, "intake": intake, "keys": ks, "blocked": blocked}},
		"tokens":      ts, "usageTokenSha256": sha("usage-token")}
}

type fixture struct {
	t        *testing.T
	dir, url string
	revision string // of the last snapshot written
	config   *config
	address  string
	stop     chan struct{}
	done     chan error
}

func newFixture(t *testing.T, url string, keys ...string) *fixture {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "keys/tavily"), 0o700)
	var states []keyState
	for _, k := range keys {
		os.WriteFile(filepath.Join(dir, "keys/tavily", fp(k)), []byte(k+"\n"), 0o600)
		states = append(states, keyState{k, "active"})
	}
	f := &fixture{t: t, dir: dir, url: url, config: &config{Snapshot: filepath.Join(dir, "snapshot.json"), KeysDir: filepath.Join(dir, "keys"),
		StateDir: filepath.Join(dir, "state"), requestTimeout: 1500 * time.Millisecond, drainTimeout: 5 * time.Second, refreshEvery: 10 * time.Millisecond}}
	f.write(testSnapshot(url, states, []string{testToken}, "all"))
	f.start()
	t.Cleanup(func() {
		if f.stop != nil {
			f.halt()
		}
	})
	return f
}

func (f *fixture) write(v any) {
	data, _ := json.Marshal(v)
	f.revision = sha(string(data))[:12]
	os.WriteFile(filepath.Join(f.dir, "snapshot.tmp"), data, 0o600)
	os.Rename(filepath.Join(f.dir, "snapshot.tmp"), filepath.Join(f.dir, "snapshot.json"))
}

func (f *fixture) start() {
	listener, err := listen("127.0.0.1:0")
	if err != nil {
		f.t.Fatal(err)
	}
	f.address, f.stop, f.done = listener.Addr().String(), make(chan struct{}), make(chan error, 1)
	go func() { f.done <- serve(f.config, listener, f.stop) }()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if response, err := http.Get("http://" + f.address + "/healthz"); err == nil {
			response.Body.Close()
			return
		}
	}
	f.t.Fatal("gateway did not answer")
}

func (f *fixture) halt() {
	// A spare client connection that never sent a request holds Shutdown for 5 s (net/http treats it as new, not idle).
	http.DefaultClient.CloseIdleConnections()
	close(f.stop)
	if err := <-f.done; err != nil {
		f.t.Fatal(err)
	}
	f.stop = nil
}

func (f *fixture) raw(method, path, auth, body string) (int, http.Header, map[string]any) {
	request, _ := http.NewRequest(method, "http://"+f.address+path, strings.NewReader(body))
	if auth != "" {
		request.Header.Set("Authorization", auth)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		f.t.Fatal(err)
	}
	defer response.Body.Close()
	var v map[string]any
	json.NewDecoder(response.Body).Decode(&v)
	return response.StatusCode, response.Header, v
}

func (f *fixture) call(toolName, tok string) (int, http.Header, map[string]any) {
	return f.raw("POST", "/v1/call/tavily/"+toolName, "Bearer "+tok, `{"query":"q","api_key":"mine"}`)
}

func (f *fixture) health() map[string]any { _, _, v := f.raw("GET", "/healthz", "", ""); return v }

func (f *fixture) spool() (lines []map[string]any, text string) {
	files, _ := filepath.Glob(filepath.Join(f.dir, "state/spool/*.jsonl"))
	for _, file := range files {
		data, _ := os.ReadFile(file)
		text += string(data)
		// Only complete lines: the gateway creates the day file before it writes the first line, so a read can see an
		// empty file or a line without its newline yet.
		complete := strings.Split(string(data), "\n")
		for _, l := range complete[:len(complete)-1] {
			var v map[string]any
			json.Unmarshal([]byte(l), &v)
			lines = append(lines, v)
		}
	}
	return lines, text
}

func field(v any, path ...string) any {
	for _, p := range path {
		m, _ := v.(map[string]any)
		v = m[p]
	}
	return v
}

func expect(t *testing.T, got, want any, what string) {
	t.Helper()
	g, _ := json.Marshal(got)
	w, _ := json.Marshal(want)
	if string(g) != string(w) {
		t.Fatalf("%s: got %s, want %s", what, g, w)
	}
}

// adopt waits until the gateway reports the condition on /healthz, so a test never races the snapshot poll.
func (f *fixture) adopt(done func(health map[string]any) bool) {
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); {
		if done(f.health()) {
			return
		}
	}
	f.t.Fatal("gateway did not adopt the snapshot")
}

// adoptWritten waits until the last written snapshot is the gateway's current one.
func (f *fixture) adoptWritten() {
	f.adopt(func(h map[string]any) bool { return field(h, "snapshot", "revision") == f.revision })
}

func TestRunTokenCallInjectsKeyAndSpoolsOneLine(t *testing.T) {
	upstream := newProvider(t, func(string) int { return 200 })
	gw := newFixture(t, upstream.url, "invented-key-a")
	status, head, body := gw.call("web_search", testToken)
	expect(t, status, 200, "status")
	expect(t, field(body, "usage", "credits"), 2, "usage")
	if head.Get("X-Fleet-Call-Id") == "" {
		t.Fatal("no call id")
	}
	sent := upstream.calls()[0]
	expect(t, sent.key, "invented-key-a", "key")
	var forwarded map[string]any
	json.Unmarshal([]byte(sent.body), &forwarded)
	expect(t, forwarded["include_usage"], true, "inject")
	expect(t, sent.optIn, "true", "headers")
	if _, ok := forwarded["api_key"]; ok {
		t.Fatal("the caller never selects a key")
	}
	lines, text := gw.spool()
	expect(t, len(lines), 1, "spool lines")
	expect(t, lines[0]["executionId"], "exec-1", "execution")
	expect(t, lines[0]["outcome"], "ok", "outcome")
	expect(t, lines[0]["keyFingerprint"], fp("invented-key-a"), "fingerprint")
	expect(t, lines[0]["usage"], map[string]any{"unit": "credits", "amount": 2}, "usage line")
	if strings.Contains(text, "invented-key-a") {
		t.Fatal("key value in the spool")
	}
}

func TestUnknownTokenRefusedAndFreshTokenWorksAtOnce(t *testing.T) {
	upstream := newProvider(t, func(string) int { return 200 })
	gw := newFixture(t, upstream.url, "invented-key-a")
	if s, _, _ := gw.call("web_search", "junk"); s != 401 {
		t.Fatal(s)
	}
	// Written between two adoption ticks: the reread on an unknown token finds it.
	gw.write(testSnapshot(gw.url, []keyState{{"invented-key-a", "active"}}, []string{testToken, "fresh-token"}, "all"))
	if s, _, _ := gw.call("web_search", "fresh-token"); s != 200 {
		t.Fatal(s)
	}
	lines, _ := gw.spool()
	expect(t, lines[0]["errorCode"], "unauthenticated", "error")
	expect(t, lines[0]["executionId"], nil, "execution")
}

func TestRefusedKeyIsDisabledPersistedAndNextServes(t *testing.T) {
	upstream := newProvider(t, func(k string) int {
		if k == "invented-key-a" {
			return 401
		}
		return 200
	})
	gw := newFixture(t, upstream.url, "invented-key-a", "invented-key-b")
	status, _, body := gw.call("web_search", testToken)
	expect(t, []any{status, body["error"]}, []any{502, "key-disabled-401"}, "refusal")
	lines, _ := gw.spool()
	expect(t, lines[0]["disabled"], map[string]any{"key": fp("invented-key-a"), "status": 401}, "disabled")
	gw.halt()
	gw.start()
	// Persisted across the restart: the call goes straight to key b.
	if s, _, _ := gw.call("web_search", testToken); s != 200 {
		t.Fatal(s)
	}
	calls := upstream.calls()
	expect(t, calls[len(calls)-1].key, "invented-key-b", "key")
	expect(t, field(gw.health(), "keys", "usable"), 1, "usable")
}

func Test403IsRecordedOnKeyAndNeverDisablesIt(t *testing.T) {
	upstream := newProvider(t, func(string) int { return 403 })
	gw := newFixture(t, upstream.url, "invented-key-a")
	for range 2 {
		if s, _, _ := gw.call("web_search", testToken); s != 403 {
			t.Fatal(s)
		}
	}
	expect(t, len(upstream.calls()), 2, "the key stays in use")
	lines, _ := gw.spool()
	expect(t, lines[0]["disabled"], nil, "disabled")
	expect(t, field(lines[0], "keyProblem", "key"), fp("invented-key-a"), "problem key")
	if !strings.Contains(field(lines[0], "keyProblem", "message").(string), "invented") {
		t.Fatal("no provider message")
	}
	expect(t, field(gw.health(), "keys", "usable"), 1, "usable")
}

func TestUsageReportsEachKeyFromProvider(t *testing.T) {
	upstream := newProvider(t, func(k string) int {
		if k == "invented-key-b" {
			return 500
		}
		return 200
	})
	gw := newFixture(t, upstream.url, "invented-key-a", "invented-key-b")
	if s, _, _ := gw.raw("GET", "/v1/usage", "Bearer "+testToken, ""); s != 401 {
		t.Fatal(s)
	}
	_, _, report := gw.raw("GET", "/v1/usage", "Bearer usage-token", "")
	rows := map[string]map[string]any{}
	for _, r := range report["keys"].([]any) {
		rows[r.(map[string]any)["key"].(string)] = r.(map[string]any)
	}
	expect(t, rows[fp("invented-key-a")]["usage"], 40, "usage a")
	b := rows[fp("invented-key-b")]
	if b["usage"] != nil || !strings.Contains(b["unknown"].(string), "500") {
		t.Fatal(b)
	}
}

func TestPathFieldFillsGetAndReachedBudgetRefuses(t *testing.T) {
	upstream := newProvider(t, func(string) int { return 200 })
	gw := newFixture(t, upstream.url, "invented-key-a")
	if s, _, _ := gw.raw("POST", "/v1/call/tavily/status", "Bearer "+testToken, `{"jobId":"task-7"}`); s != 200 {
		t.Fatal(s)
	}
	expect(t, upstream.calls()[0].body, "", "a GET sends no body")
	if s, _, _ := gw.raw("POST", "/v1/call/tavily/status", "Bearer "+testToken, `{"jobId":"../x"}`); s != 400 {
		t.Fatal(s)
	}
	gw.write(testSnapshot(gw.url, []keyState{{"invented-key-a", "active"}}, []string{testToken}, "blocked"))
	gw.adoptWritten()
	status, _, body := gw.call("web_search", testToken)
	expect(t, []any{status, body["error"]}, []any{403, "budget-reached"}, "budget")
}

func TestQuotaPassesThroughAndDrainingKeysTakeNoCalls(t *testing.T) {
	upstream := newProvider(t, func(k string) int {
		if k == "invented-key-b" {
			return 429
		}
		return 200
	})
	gw := newFixture(t, upstream.url, "invented-key-a", "invented-key-b")
	gw.write(testSnapshot(gw.url, []keyState{{"invented-key-a", "draining"}, {"invented-key-b", "active"}}, []string{testToken}, "all"))
	gw.adoptWritten()
	status, head, _ := gw.call("web_search", testToken)
	expect(t, status, 429, "status")
	expect(t, head.Get("Retry-After"), "7", "retry-after")
	expect(t, len(upstream.calls()), 1, "no switch to another key on a quota reply")
}

func TestNoAnswerFailsOverOnlyForIdempotentTool(t *testing.T) {
	upstream := newProvider(t, func(k string) int {
		if k == "invented-key-a" {
			return 0
		}
		return 200
	})
	gw := newFixture(t, upstream.url, "invented-key-a", "invented-key-b")
	status, _, body := gw.call("submit", testToken)
	expect(t, []any{status, body["error"]}, []any{502, "provider-unreachable"}, "submit")
	lines, _ := gw.spool()
	expect(t, lines[0]["outcome"], "unknown", "outcome")
	expect(t, len(upstream.calls()), 1, "a non-idempotent call is never sent again")
	if s, _, _ := gw.call("web_search", testToken); s != 200 {
		t.Fatal(s)
	}
}

func TestInvalidSnapshotKeepsLastValidAndPausedIntakeRefuses(t *testing.T) {
	upstream := newProvider(t, func(string) int { return 200 })
	gw := newFixture(t, upstream.url, "invented-key-a")
	os.WriteFile(filepath.Join(gw.dir, "snapshot.json"), []byte("not json"), 0o600)
	gw.adopt(func(h map[string]any) bool { return field(h, "snapshot", "rejected") == 1.0 })
	if s, _, _ := gw.call("web_search", testToken); s != 200 {
		t.Fatal(s)
	}
	expect(t, field(gw.health(), "snapshot", "rejected"), 1, "rejected")
	gw.halt()
	gw.start() // starts from the retained copy
	if s, _, _ := gw.call("web_search", testToken); s != 200 {
		t.Fatal(s)
	}
	gw.write(testSnapshot(gw.url, []keyState{{"invented-key-a", "active"}}, []string{testToken}, "paused"))
	gw.adoptWritten()
	_, _, body := gw.call("web_search", testToken)
	expect(t, body["error"], "intake-paused", "intake")
	_, _, body = gw.raw("POST", "/v1/call/other/web_search", "Bearer "+testToken, "{}")
	expect(t, body["error"], "not-granted", "grant")
	if _, err := os.Stat(filepath.Join(gw.dir, "state/snapshot.good.json")); err != nil {
		t.Fatal(err)
	}
}

// Requirement: the gateway binds only to loopback (Caddy forwards /v1/call/* alone). Risk: a config listen on 0.0.0.0 exposes /v1/usage.
func TestListenRefusesNonLoopback(t *testing.T) {
	if l, err := listen("0.0.0.0:0"); err == nil {
		l.Close()
		t.Fatal("bound a non-loopback address")
	}
}

// Requirement: a provider call is sent at most as the tool's replay rule allows (fleet-connections.md §2).
// Risk: Go's client follows a 307/308 with the same method and body, so a redirected POST is charged a second time.
func TestProviderRedirectIsNeverFollowed(t *testing.T) {
	second := newProvider(t, func(string) int { return 200 })
	first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, second.url+r.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(first.Close)
	gw := newFixture(t, first.URL, "invented-key-a")
	status, _, body := gw.call("submit", testToken)
	expect(t, []any{status, body["error"]}, []any{502, "provider-redirect"}, "redirected submit")
	expect(t, len(second.calls()), 0, "redirect target calls")
	lines, _ := gw.spool()
	expect(t, lines[0]["outcome"], "unknown", "outcome")
}

// Requirement: every billable call is either durably spooled or counted as lost (fleet-connections.md §3).
// Risk: when the lost counter cannot be persisted either, the loss is silent after a restart.
func TestSpoolLossMarkerFailureIsReported(t *testing.T) {
	upstream := newProvider(t, func(string) int { return 200 })
	gw := newFixture(t, upstream.url, "invented-key-a")
	state := filepath.Join(gw.dir, "state")
	os.RemoveAll(filepath.Join(state, "spool"))
	os.WriteFile(filepath.Join(state, "spool"), nil, 0o600) // the spool directory is now a file: writes fail
	os.Mkdir(filepath.Join(state, "spool-lost.tmp"), 0o700) // and the lost counter cannot be written
	gw.call("web_search", testToken)
	expect(t, field(gw.health(), "spool"), map[string]any{"lost": 1, "lostRecorded": false}, "spool health")
}

// Requirement: a caller that gives up releases its provider request (fleet-connections.md §2).
// Risk: the provider request runs to the gateway timeout, and an idempotent tool fails over to another paid key;
// a request already sent must still be recorded with outcome unknown.
func TestCallerCancellationReachesProvider(t *testing.T) {
	released, stop := make(chan time.Duration, 4), make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		begun := time.Now()
		io.ReadAll(r.Body) // the server notices a closed connection only after the body is read
		select {
		case <-r.Context().Done():
		case <-stop:
		}
		released <- time.Since(begun)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(stop) }) // runs first: no provider handler outlives the test
	gw := newFixture(t, server.URL, "invented-key-a", "invented-key-b")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "POST", "http://"+gw.address+"/v1/call/tavily/web_search", strings.NewReader(`{"query":"q"}`))
	request.Header.Set("Authorization", "Bearer "+testToken)
	if _, err := http.DefaultClient.Do(request); err == nil {
		t.Fatal("caller was answered")
	}
	select {
	case held := <-released:
		if held > time.Second {
			t.Fatalf("provider request held %v after the caller left", held)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("provider request not released; %d requests", calls.Load())
	}
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		if lines, _ := gw.spool(); len(lines) == 1 && lines[0]["outcome"] != nil { // a line still being written reads as null
			expect(t, []any{lines[0]["outcome"], lines[0]["errorCode"]}, []any{"unknown", "caller-cancelled"}, "spooled cancel")
			expect(t, calls.Load(), int32(1), "provider requests")
			return
		}
	}
	t.Fatal("no spool line")
}

// Requirement: fleet-connections.md §5: a provider's text reply (Bugsink's Markdown stacktrace) reaches the caller as
// {"text": ...}, with the key still redacted. Risk: the caller's JSON-only client marks every such read invalid-response.
func TestTextReplyBecomesJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		io.WriteString(w, "# Error\n  at f (main.tsx:3) invented-key-a\n")
	}))
	t.Cleanup(server.Close)
	gw := newFixture(t, server.URL, "invented-key-a")
	// Without textReply a text answer stays as it was (the caller's client reports it invalid).
	if status, _, v := gw.raw("POST", "/v1/call/tavily/status", "Bearer "+testToken, `{"jobId":"j1"}`); status != 200 || v != nil {
		t.Fatalf("plain tool: %d %v", status, v)
	}
	snap := testSnapshot(server.URL, []keyState{{"invented-key-a", "active"}}, []string{testToken}, "all")
	status := field(snap, "providers", "tavily", "tools", "status").(map[string]any)
	status["textReply"] = true
	gw.write(snap)
	gw.adopt(func(h map[string]any) bool { return field(h, "snapshot", "revision") == gw.revision })
	code, _, v := gw.raw("POST", "/v1/call/tavily/status", "Bearer "+testToken, `{"jobId":"j1"}`)
	expect(t, []any{code, v}, []any{200, map[string]any{"text": "# Error\n  at f (main.tsx:3) [key]\n"}}, "text reply")
}

// Requirement: fleet-error-tracking.md, agent read access: each bugsink catalogue tool (coordinator providers.go) reaches
// the Bugsink API path it names with the key as bearer, and refuses ids that are not ids. Risk: a tool reads the wrong
// endpoint, or a crafted id walks to another API path with the read token.
func TestBugsinkCatalogue(t *testing.T) {
	src, err := os.ReadFile("../coordinator/providers.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	text = text[strings.Index(text, "const providersJSON = `")+len("const providersJSON = `"):]
	var catalogue map[string]map[string]any
	if err := json.Unmarshal([]byte(text[:strings.Index(text, "`")]), &catalogue); err != nil {
		t.Fatal(err)
	}
	type seen struct{ uri, auth, accept string }
	got := make(chan seen, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get("Accept")}
		if r.Header.Get("Accept") == "text/markdown" {
			w.Header().Set("Content-Type", "text/markdown")
			io.WriteString(w, "frames")
			return
		}
		io.WriteString(w, `{"results":[]}`)
	}))
	t.Cleanup(server.Close)
	bugsink := catalogue["bugsink"]
	bugsink["baseUrl"] = server.URL
	tools := []string{}
	for name := range bugsink["tools"].(map[string]any) {
		tools = append(tools, name)
	}
	gw := newFixture(t, server.URL, "invented-bugsink-key")
	os.MkdirAll(filepath.Join(gw.dir, "keys/bugsink"), 0o700)
	os.WriteFile(filepath.Join(gw.dir, "keys/bugsink", fp("invented-bugsink-key")), []byte("invented-bugsink-key\n"), 0o600)
	snap := testSnapshot(server.URL, []keyState{{"invented-bugsink-key", "active"}}, nil, "all")
	snap["providers"].(map[string]any)["bugsink"] = bugsink
	snap["connections"].(map[string]any)["bugsink"] = map[string]any{"provider": "bugsink", "tools": tools, "intake": "all",
		"keys": []map[string]any{{"id": fp("invented-bugsink-key"), "state": "active"}}, "blocked": []string{}}
	snap["tokens"] = []map[string]any{{"sha256": sha(testToken), "executionId": "exec-1", "projectId": "fleet", "connections": []string{"bugsink"}}}
	gw.write(snap)
	gw.adopt(func(h map[string]any) bool { return field(h, "snapshot", "revision") == gw.revision })

	issue, event := "0f8fad5b-d9cb-469f-a165-70867728950e", "7c9e6679-7425-40de-944b-e07fc1f90ae7"
	for tool, c := range map[string]struct{ body, uri, accept string }{
		"projects":         {`{}`, "/api/canonical/0/projects/", "application/json"},
		"issues":           {`{"project":"3","state":"unresolved"}`, "/api/canonical/0/issues/?project=3&state=unresolved&sort=last_seen&order=desc", "application/json"},
		"issue_get":        {`{"issue":"` + issue + `"}`, "/api/canonical/0/issues/" + issue + "/", "application/json"},
		"issue_events":     {`{"issue":"` + issue + `"}`, "/api/canonical/0/events/?issue=" + issue + "&order=desc", "application/json"},
		"event_get":        {`{"event":"` + event + `"}`, "/api/canonical/0/events/" + event + "/", "application/json"},
		"event_stacktrace": {`{"event":"` + event + `"}`, "/api/canonical/0/events/" + event + "/stacktrace/", "text/markdown"},
	} {
		status, _, v := gw.raw("POST", "/v1/call/bugsink/"+tool, "Bearer "+testToken, c.body)
		if status != 200 || v == nil {
			t.Fatalf("%s: %d %v", tool, status, v)
		}
		s := <-got
		expect(t, s, seen{c.uri, "Bearer invented-bugsink-key", c.accept}, tool)
	}
	for tool, body := range map[string]string{"issue_get": `{"issue":"../projects"}`, "issues": `{"project":"3&x=1","state":"open"}`, "event_get": `{"event":"1"}`} {
		if status, _, _ := gw.raw("POST", "/v1/call/bugsink/"+tool, "Bearer "+testToken, body); status != 400 {
			t.Fatalf("%s %s: %d", tool, body, status)
		}
	}
	if len(tools) != 6 {
		t.Fatalf("bugsink tools: %v", tools)
	}
}

// Requirement: fleet-connections.md §5 path fields and §8 YouTrack: each youtrack catalogue tool (coordinator providers.go) reaches the
// YouTrack REST path it names with the key as bearer; a search query is query-escaped, and an issue id that is not an id
// is refused; a 403 is not recorded on the key. Risk: a query adds or ends a parameter, a crafted id walks to another
// API path with the agents' token, or every refused action marks the only key as broken.
func TestYouTrackCatalogue(t *testing.T) {
	src, err := os.ReadFile("../coordinator/providers.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	text = text[strings.Index(text, "const providersJSON = `")+len("const providersJSON = `"):]
	var catalogue map[string]map[string]any
	if err := json.Unmarshal([]byte(text[:strings.Index(text, "`")]), &catalogue); err != nil {
		t.Fatal(err)
	}
	type seen struct{ method, path, query, skip, auth, body string }
	got := make(chan seen, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- seen{r.Method, r.URL.Path, r.URL.Query().Get("query"), r.URL.Query().Get("$skip"), r.Header.Get("Authorization"), string(b)}
		if strings.Contains(string(b), "State Done") || r.URL.Path == "/api/issues/TGT-403" {
			w.WriteHeader(403)
			io.WriteString(w, `{"error":"forbidden"}`)
			return
		}
		io.WriteString(w, `[]`)
	}))
	t.Cleanup(server.Close)
	yt := catalogue["youtrack"]
	yt["baseUrl"] = server.URL
	tools := []string{}
	for name := range yt["tools"].(map[string]any) {
		tools = append(tools, name)
	}
	gw := newFixture(t, server.URL, "invented-youtrack-key")
	os.MkdirAll(filepath.Join(gw.dir, "keys/youtrack"), 0o700)
	os.WriteFile(filepath.Join(gw.dir, "keys/youtrack", fp("invented-youtrack-key")), []byte("invented-youtrack-key\n"), 0o600)
	snap := testSnapshot(server.URL, []keyState{{"invented-youtrack-key", "active"}}, nil, "all")
	snap["providers"].(map[string]any)["youtrack"] = yt
	snap["connections"].(map[string]any)["youtrack"] = map[string]any{"provider": "youtrack", "tools": tools, "intake": "all",
		"keys": []map[string]any{{"id": fp("invented-youtrack-key"), "state": "active"}}, "blocked": []string{}}
	snap["tokens"] = []map[string]any{{"sha256": sha(testToken), "executionId": "exec-1", "projectId": "fleet", "connections": []string{"youtrack"}}}
	gw.write(snap)
	gw.adopt(func(h map[string]any) bool { return field(h, "snapshot", "revision") == gw.revision })

	query := "project: TGT #Unresolved & fields=x?y {skip} ../a sort by: created asc"
	for tool, c := range map[string]struct {
		body string
		want seen
	}{
		"projects":       {`{}`, seen{"GET", "/api/admin/projects", "", "", "", ""}},
		"issues_search":  {`{"query":"` + query + `","skip":"50"}`, seen{"GET", "/api/issues", query, "50", "", ""}},
		"issue_get":      {`{"issue":"TGT-12"}`, seen{"GET", "/api/issues/TGT-12", "", "", "", ""}},
		"issue_comments": {`{"issue":"TGT-12"}`, seen{"GET", "/api/issues/TGT-12/comments", "", "", "", ""}},
		"issue_create":   {`{"project":{"id":"0-1"},"summary":"s"}`, seen{"POST", "/api/issues", "", "", "", `{"project":{"id":"0-1"},"summary":"s"}`}},
		"issue_update":   {`{"issue":"TGT-12","summary":"s"}`, seen{"POST", "/api/issues/TGT-12", "", "", "", `{"summary":"s"}`}},
		"comment_add":    {`{"issue":"TGT-12","text":"t"}`, seen{"POST", "/api/issues/TGT-12/comments", "", "", "", `{"text":"t"}`}},
		"command":        {`{"query":"State Open","issues":[{"idReadable":"TGT-12"}]}`, seen{"POST", "/api/commands", "", "", "", `{"issues":[{"idReadable":"TGT-12"}],"query":"State Open"}`}},
	} {
		status, _, _ := gw.raw("POST", "/v1/call/youtrack/"+tool, "Bearer "+testToken, c.body)
		if status != 200 {
			t.Fatalf("%s: %d", tool, status)
		}
		c.want.auth = "Bearer invented-youtrack-key"
		expect(t, <-got, c.want, tool)
	}
	for tool, body := range map[string]string{"issue_get": `{"issue":"../admin/users"}`, "issue_comments": `{"issue":"TGT-1?x"}`, "issues_search": `{"query":"a","skip":"1&x"}`} {
		if status, _, _ := gw.raw("POST", "/v1/call/youtrack/"+tool, "Bearer "+testToken, body); status != 400 {
			t.Fatalf("%s %s: %d", tool, body, status)
		}
	}
	// An action the account may not do answers 403; that refuses the call, not the shared key.
	if status, _, _ := gw.raw("POST", "/v1/call/youtrack/command", "Bearer "+testToken, `{"query":"State Done","issues":[{"idReadable":"TGT-12"}]}`); status != 403 {
		t.Fatalf("forbidden command: %d", status)
	}
	<-got
	lines, _ := gw.spool()
	last := lines[len(lines)-1]
	expect(t, []any{last["tool"], last["status"], last["keyProblem"]}, []any{"command", float64(403), nil}, "a 403 on a write is not a key problem")
	// A 403 on a read can mean the key lost access, so it is still recorded on the key.
	if status, _, _ := gw.raw("POST", "/v1/call/youtrack/issue_get", "Bearer "+testToken, `{"issue":"TGT-403"}`); status != 403 {
		t.Fatalf("forbidden read: %d", status)
	}
	<-got
	lines, _ = gw.spool()
	last = lines[len(lines)-1]
	expect(t, []any{last["tool"], field(last, "keyProblem", "key")}, []any{"issue_get", fp("invented-youtrack-key")}, "a 403 on a read is a key problem")
	// A page offset sent as a number is taken as its digits.
	if status, _, _ := gw.raw("POST", "/v1/call/youtrack/issues_search", "Bearer "+testToken, `{"query":"a","skip":100}`); status != 200 {
		t.Fatalf("numeric skip: %d", status)
	}
	expect(t, (<-got).skip, "100", "numeric skip")
	if len(tools) != 8 {
		t.Fatalf("youtrack tools: %v", tools)
	}
}

func TestPathPatternsAndUsageField(t *testing.T) {
	model := "^[A-Za-z0-9._-]+/[A-Za-z0-9._-]+$"
	field := "usageMetadata.totalTokenCount"
	tl := tool{Path: "/models/{model}", PathPatterns: map[string]string{"model": model}, UsageField: &field}
	for value, want := range map[string]bool{`"org/name-1.5"`: true, `"org/../x"`: false, `"name"`: false, `"a/b/c"`: false, `"./x"`: false} {
		body := map[string]json.RawMessage{"model": json.RawMessage(value)}
		path, ok := fillPath(tl, body)
		if ok != want || ok && (path != "/models/"+value[1:len(value)-1] || body["model"] != nil) {
			t.Fatalf("%s: path %q ok %v", value, path, ok)
		}
	}
	if _, ok := fillPath(tool{Path: "/t/{jobId}"}, map[string]json.RawMessage{"jobId": json.RawMessage(`"a/b"`)}); ok {
		t.Fatal("an unpatterned field took a slash")
	}
	if n, ok := usageAt([]byte(`{"usageMetadata":{"totalTokenCount":42}}`), field); !ok || n != 42 {
		t.Fatalf("usage %d %v", n, ok)
	}
	if _, ok := usageAt([]byte(`{"usageMetadata":{}}`), field); ok {
		t.Fatal("missing usage read")
	}
}

// ---------------------------------------------------------------- file tools (fleet-connections.md §9)

// A fixture catalogue with one upload and one download tool on the tavily provider (the YouTrack catalogue gains
// them in its own change). The provider is fake: it stores uploads, answers lookups and serves blobs.

type seenUpload struct {
	auth, contentType  string
	length, read       int64 // Content-Length and the bytes the provider read
	parts              int
	field, name, media string
	sha                string
}

type fileProvider struct {
	url     string
	lock    sync.Mutex
	uploads []seenUpload
	lookups []string // Authorization of each lookup
	fetches []string // Authorization of each blob fetch
	other   atomic.Int32
	// Overrides; nil keeps the default behaviour.
	meta     func() map[string]any                    // lookup reply
	blob     func(w http.ResponseWriter)              // the file at /blob
	onUpload func(w http.ResponseWriter, read func()) // the upload handler: read() reads the multipart
}

var blobBytes = []byte(strings.Repeat("invented file bytes ", 50))

func newFileProvider(t *testing.T) *fileProvider {
	p := &fileProvider{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/attachments"):
			u := seenUpload{auth: r.Header.Get("Authorization"), contentType: r.Header.Get("Content-Type"), length: r.ContentLength}
			read := func() {
				if reader, err := r.MultipartReader(); err == nil {
					for {
						part, err := reader.NextPart()
						if err != nil {
							break
						}
						hash := sha256.New()
						n, _ := io.Copy(hash, part)
						u.parts++
						u.field, u.name, u.media, u.sha, u.read = part.FormName(), part.FileName(), part.Header.Get("Content-Type"), hex.EncodeToString(hash.Sum(nil)), n
					}
				}
				p.lock.Lock()
				p.uploads = append(p.uploads, u)
				p.lock.Unlock()
			}
			if p.onUpload != nil {
				p.onUpload(w, read)
				return
			}
			read()
			io.WriteString(w, `[{"id":"85-1","name":"stored","size":1,"mimeType":"image/png"}]`)
		case strings.Contains(r.URL.Path, "/meta/"):
			p.lock.Lock()
			p.lookups = append(p.lookups, r.Header.Get("Authorization"))
			p.lock.Unlock()
			meta := map[string]any{"id": "85-1", "name": "экран 1.png", "size": len(blobBytes), "mimeType": "image/png", "url": "/blob/85-1?sign=invented"}
			if p.meta != nil {
				meta = p.meta()
			}
			json.NewEncoder(w).Encode(meta)
		case r.URL.Path == "/blob/85-1":
			p.lock.Lock()
			p.fetches = append(p.fetches, r.Header.Get("Authorization"))
			p.lock.Unlock()
			if p.blob != nil {
				p.blob(w)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(blobBytes)))
			w.Write(blobBytes)
		default:
			p.other.Add(1)
			io.WriteString(w, "elsewhere")
		}
	}))
	t.Cleanup(server.Close)
	p.url = server.URL
	return p
}

func (p *fileProvider) seen() ([]seenUpload, []string, []string) {
	p.lock.Lock()
	defer p.lock.Unlock()
	return append([]seenUpload(nil), p.uploads...), append([]string(nil), p.lookups...), append([]string(nil), p.fetches...)
}

func newFileFixture(t *testing.T, limits map[string]any) (*fixture, *fileProvider) {
	p := newFileProvider(t)
	gw := newFixture(t, p.url, "invented-key-a")
	gw.write(fileSnapshot(p.url, limits, "all"))
	gw.adoptWritten()
	return gw, p
}

func fileSnapshot(url string, limits map[string]any, intake string) map[string]any {
	snap := testSnapshot(url, []keyState{{"invented-key-a", "active"}}, []string{testToken}, intake)
	tools := field(snap, "providers", "tavily", "tools").(map[string]any)
	tools["attach"] = map[string]any{"method": "POST", "path": "/files/{issue}/attachments?fields=id,name", "unit": "calls", "idempotent": false, "file": "upload", "forbiddenIsCall": true}
	tools["fetch"] = map[string]any{"method": "GET", "path": "/files/{issue}/meta/{attachment}", "unit": "calls", "idempotent": true, "file": "download",
		"pathPatterns": map[string]any{"attachment": "^[0-9]{1,9}-[0-9]{1,12}$"}, "fileFrom": map[string]any{"url": "url", "bytes": "size", "name": "name", "mediaType": "mimeType"}}
	conn := field(snap, "connections", "tavily").(map[string]any)
	conn["tools"] = append(conn["tools"].([]string), "attach", "fetch")
	if limits != nil {
		conn["limits"] = limits
	}
	return snap
}

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// upload posts body as a file; length -1 sends it chunked (no Content-Length).
func (f *fixture) upload(body io.Reader, length int64, sum string, args map[string]any, header map[string]string) (int, http.Header, map[string]any, error) {
	request, _ := http.NewRequest("POST", "http://"+f.address+"/v1/call/tavily/attach", body)
	request.ContentLength = length
	encoded, _ := json.Marshal(args)
	request.Header.Set("Authorization", "Bearer "+testToken)
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("X-Fleet-Args", base64.RawURLEncoding.EncodeToString(encoded))
	request.Header.Set("X-Fleet-Sha256", sum)
	for k, v := range header {
		request.Header.Set(k, v)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	var v map[string]any
	json.NewDecoder(response.Body).Decode(&v)
	return response.StatusCode, response.Header, v, nil
}

func (f *fixture) uploadBytes(data []byte, args map[string]any) (int, map[string]any) {
	status, _, v, err := f.upload(bytes.NewReader(data), int64(len(data)), shaHex(data), args, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	return status, v
}

func (f *fixture) parts() []string {
	left, _ := filepath.Glob(filepath.Join(f.dir, "state/files/*.part"))
	return left
}

func (f *fixture) download(args string, accept bool) (*http.Response, error) {
	request, _ := http.NewRequest("POST", "http://"+f.address+"/v1/call/tavily/fetch", strings.NewReader(args))
	request.Header.Set("Authorization", "Bearer "+testToken)
	if accept {
		request.Header.Set("Accept", "application/octet-stream")
	}
	return http.DefaultClient.Do(request)
}

func lastLine(t *testing.T, gw *fixture) map[string]any {
	t.Helper()
	lines, _ := gw.spool()
	if len(lines) == 0 {
		t.Fatal("no spool line")
	}
	return lines[len(lines)-1]
}

// Requirement: §9 upload: the staged file reaches the provider as one multipart part with the caller's UTF-8 name,
// its exact bytes, a correct Content-Length and the key as bearer; the spool line records the transfer and its sha256.
// Risk: a second part, a mangled Cyrillic name, a body the provider reads short, or an upload with no byte record.
func TestUploadMultipartHasOneFilePart(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	data := bytes.Repeat([]byte("png bytes\x00\xff"), 10_000)
	name := "запись экрана 1.png"
	status, reply := gw.uploadBytes(data, map[string]any{"issue": "TGT-12", "name": name, "mediaType": "image/png"})
	expect(t, status, 200, "status")
	if reply != nil {
		t.Fatalf("a list reply decodes as no object: %v", reply)
	}
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 1, "provider requests")
	u := uploads[0]
	expect(t, []any{u.parts, u.field, u.name, u.media, u.sha, u.read}, []any{1, "file", name, "image/png", shaHex(data), len(data)}, "part")
	expect(t, u.auth, "Bearer invented-key-a", "key")
	if !strings.HasPrefix(u.contentType, "multipart/form-data; boundary=") || u.length <= int64(len(data)) {
		t.Fatalf("request: %s, length %d", u.contentType, u.length)
	}
	line := lastLine(t, gw)
	expect(t, []any{line["outcome"], line["transfer"]}, []any{"ok", map[string]any{"direction": "upload", "bytes": len(data), "mediaType": "image/png", "name": name, "sha256": shaHex(data)}}, "spool transfer")
	expect(t, len(gw.parts()), 0, "staged files")
}

// pattern is a deterministic body that holds no key value.
type pattern struct{ left, at int64 }

func (r *pattern) Read(b []byte) (int, error) {
	if r.left == 0 {
		return 0, io.EOF
	}
	n := min(int64(len(b)), r.left)
	for i := range n {
		b[i] = byte('a' + (r.at+i)%26)
	}
	r.at += n
	r.left -= n
	return int(n), nil
}

// Requirement: §9 upload memory: a 63 MB recording sits on disk once and in memory only as copy buffers.
// Risk: a buffered body (io.ReadAll or a bytes.Buffer) that grows with each concurrent upload.
func TestUploadLargeBodyFlatMemory(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	const size = 64 << 20
	hash := sha256.New()
	io.Copy(hash, &pattern{left: size})
	sum := hex.EncodeToString(hash.Sum(nil))
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	var peak atomic.Uint64
	done := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-done:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapInuse > peak.Load() {
					peak.Store(m.HeapInuse)
				}
			}
		}
	}()
	status, _, _, err := gw.upload(&pattern{left: size}, size, sum, map[string]any{"issue": "TGT-12", "name": "screen.mov", "mediaType": "video/quicktime"}, nil)
	close(done)
	if err != nil || status != 200 {
		t.Fatalf("upload: %d %v", status, err)
	}
	uploads, _, _ := p.seen()
	expect(t, []any{uploads[0].sha, uploads[0].read}, []any{sum, size}, "provider bytes")
	if growth := int64(peak.Load()) - int64(before.HeapInuse); growth > 8<<20 {
		t.Fatalf("heap in use grew by %d bytes during a %d byte upload", growth, size)
	}
}

// Requirement: §9 limits: an upload over the connection's fileBytes, or without Content-Length, is refused before its
// body is read. Risk: the gateway stages (or sends) a file it must refuse, or waits for a body of unknown length.
func TestUploadOversizeRefusedBeforeProvider(t *testing.T) {
	gw, p := newFileFixture(t, map[string]any{"fileBytes": 1000})
	data := bytes.Repeat([]byte("x"), 2000)
	status, _, v, err := gw.upload(bytes.NewReader(data), int64(len(data)), shaHex(data), map[string]any{"issue": "TGT-12", "name": "big.bin"}, map[string]string{"Expect": "100-continue"})
	if err != nil {
		t.Fatal(err)
	}
	expect(t, []any{status, v["error"]}, []any{413, "file-too-large"}, "oversize")
	status, _, v, err = gw.upload(io.MultiReader(bytes.NewReader(data[:10])), -1, shaHex(data[:10]), map[string]any{"issue": "TGT-12", "name": "small.bin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, []any{status, v["error"]}, []any{411, "length-required"}, "no length")
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider requests")
	expect(t, len(gw.parts()), 0, "staged files")
}

// Requirement: §9 stall: a caller that stops sending is refused after stallMs and nothing reaches the provider.
// Risk: a half-sent file held open forever, a temp file left behind, or a partial file posted.
func TestUploadStallRemovesTempFile(t *testing.T) {
	gw, p := newFileFixture(t, map[string]any{"stallMs": 300})
	reader, writer := io.Pipe()
	defer writer.Close()
	go writer.Write(bytes.Repeat([]byte("y"), 1000))
	status, _, v, err := gw.upload(reader, 5000, strings.Repeat("0", 64), map[string]any{"issue": "TGT-12", "name": "slow.bin"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, []any{status, v["error"]}, []any{408, "stalled"}, "stall")
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider requests")
	expect(t, len(gw.parts()), 0, "staged files")
	expect(t, lastLine(t, gw)["errorCode"], "stalled", "spool")
}

// Requirement: §9 stall is a progress timeout, not a total deadline. Risk: a slow but steady upload over a poor link
// is cut although bytes keep arriving.
func TestUploadSlowSteadySucceeds(t *testing.T) {
	gw, p := newFileFixture(t, map[string]any{"stallMs": 1000})
	data := bytes.Repeat([]byte("z"), 300)
	reader, writer := io.Pipe()
	go func() {
		for i := 0; i < len(data); i += 10 {
			writer.Write(data[i : i+10])
			time.Sleep(50 * time.Millisecond)
		}
		writer.Close()
	}()
	status, _, _, err := gw.upload(reader, int64(len(data)), shaHex(data), map[string]any{"issue": "TGT-12", "name": "steady.bin"}, nil)
	if err != nil || status != 200 {
		t.Fatalf("steady upload: %d %v", status, err)
	}
	uploads, _, _ := p.seen()
	expect(t, uploads[0].sha, shaHex(data), "provider bytes")
}

// Requirement: §9 key scan: a file holding a key value the gateway holds, or the caller's own token, is refused,
// also when the value is split across two reads. Risk: the file tool becomes a way to post a key to YouTrack.
func TestUploadKeySplitAcrossReadsRefused(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	for _, secret := range []string{"invented-key-a", testToken} {
		data := append(append(bytes.Repeat([]byte("."), 32<<10-5), secret...), bytes.Repeat([]byte("."), 1000)...)
		reader, writer := io.Pipe()
		go func() {
			writer.Write(data[:32<<10]) // ends inside the value
			time.Sleep(100 * time.Millisecond)
			writer.Write(data[32<<10:])
			writer.Close()
		}()
		status, _, v, err := gw.upload(reader, int64(len(data)), shaHex(data), map[string]any{"issue": "TGT-12", "name": "log.txt"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		expect(t, []any{status, v["error"]}, []any{422, "secret-found"}, secret)
		if strings.Contains(fmt.Sprint(v), secret) {
			t.Fatal("the refusal names the value")
		}
	}
	// The name goes to the provider too.
	status, v := gw.uploadBytes([]byte("clean"), map[string]any{"issue": "TGT-12", "name": testToken + ".txt"})
	expect(t, []any{status, v["error"]}, []any{422, "secret-found"}, "a token in the name")
	if _, text := gw.spool(); strings.Contains(text, testToken) {
		t.Fatal("the caller's token in the spool")
	}
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider requests")
	expect(t, len(gw.parts()), 0, "staged files")
	if _, text := gw.spool(); strings.Contains(text, "invented-key-a") {
		t.Fatal("key value in the spool")
	}
}

// Requirement: §9: the staged bytes must match the client's X-Fleet-Sha256. Risk: a file changed between the client's
// hash and its send is posted, so tool_run records a sha of other bytes.
func TestUploadShaMismatchRefused(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	data := []byte("invented report")
	status, _, v, err := gw.upload(bytes.NewReader(data), int64(len(data)), shaHex([]byte("other bytes")), map[string]any{"issue": "TGT-12", "name": "r.txt"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	expect(t, []any{status, v["error"]}, []any{400, "sha-mismatch"}, "mismatch")
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider requests")
}

// Requirement: §9: an old JSON client that reaches a file tool gets file-expected, for an upload sent as JSON and for
// a download without Accept: application/octet-stream. Risk: an old client posts JSON as a file, or reads raw bytes as
// an invalid reply after the provider was called.
func TestJSONToFileToolRefused(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	_, _, v := gw.raw("POST", "/v1/call/tavily/attach", "Bearer "+testToken, `{"issue":"TGT-12","path":"/tmp/x"}`)
	expect(t, v["error"], "file-expected", "upload as JSON")
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, false)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	expect(t, []any{response.StatusCode, response.Header.Get("X-Fleet-Error")}, []any{400, "file-expected"}, "download without Accept")
	uploads, lookups, _ := p.seen()
	expect(t, []int{len(uploads), len(lookups)}, []int{0, 0}, "provider requests")
}

// Requirement: §9 name rules: the name goes into a quoted multipart header, so CR, LF, control characters, ", / and
// names over 255 bytes are refused. Risk: a crafted name adds a header or a part, or names a path.
func TestUploadBadFileNameRefused(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	for _, name := range []string{"", "a\rb", "a\nb", "a\x01b", `a"b`, `notes\`, "invoice\u202efdp.exe", "a\u0085b", "..", "  ", "a/b", strings.Repeat("я", 128)} {
		status, v := gw.uploadBytes([]byte("x"), map[string]any{"issue": "TGT-12", "name": name})
		expect(t, []any{status, v["error"]}, []any{400, "bad-file-name"}, fmt.Sprintf("%q", name))
	}
	status, v := gw.uploadBytes([]byte("x"), map[string]any{"issue": "TGT-12", "name": "ok.txt", "extra": 1})
	expect(t, []any{status, v["error"]}, []any{400, "bad-request"}, "an argument outside the path fields")
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider requests")
}

// Requirement: §9: an upload is not idempotent; a provider that read the file and gave no answer leaves the outcome
// unknown, and the file is not sent again. Risk: a retry or failover creates a duplicate attachment.
func TestUploadProviderCutIsUnknown(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	p.onUpload = func(w http.ResponseWriter, read func()) {
		read()
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}
	status, v := gw.uploadBytes([]byte("invented"), map[string]any{"issue": "TGT-12", "name": "a.txt"})
	expect(t, []any{status, v["error"]}, []any{502, "provider-unreachable"}, "cut")
	expect(t, lastLine(t, gw)["outcome"], "unknown", "outcome")
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 1, "provider requests")
}

// Requirement: §9 download: the lookup carries the key; the file fetch from the signed URL carries none.
// Risk: the key leaves the gateway on a request it does not need, to a URL the provider's reply chose.
func TestDownloadFetchCarriesNoKey(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || !bytes.Equal(data, blobBytes) {
		t.Fatalf("download: %v %d bytes", err, len(data))
	}
	meta, _ := base64.RawURLEncoding.DecodeString(response.Header.Get("X-Fleet-File"))
	expect(t, string(meta), `{"bytes":1000,"mediaType":"image/png","name":"экран 1.png"}`, "file header")
	_, lookups, fetches := p.seen()
	expect(t, []any{lookups, fetches}, []any{[]string{"Bearer invented-key-a"}, []string{""}}, "authorization")
}

// Requirement: §9 download URL: only a path on the provider's own host is fetched. Risk: a lookup reply with an
// absolute or protocol-relative URL makes the gateway fetch from another host.
func TestDownloadAbsoluteURLRefused(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	for _, link := range []string{"http://other/x", "//other/x", "blob/85-1"} {
		p.meta = func() map[string]any {
			return map[string]any{"name": "a", "size": 3, "mimeType": "text/plain", "url": link}
		}
		response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		expect(t, []any{response.StatusCode, response.Header.Get("X-Fleet-Error")}, []any{502, "provider-body"}, link)
	}
	_, _, fetches := p.seen()
	expect(t, []any{len(fetches), p.other.Load()}, []any{0, 0}, "fetches")
}

// Requirement: §9 download: a redirect of the file fetch is not followed. Risk: the provider (or a crafted signed
// URL) sends the gateway to another URL.
func TestDownloadRedirectNotFollowed(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	p.blob = func(w http.ResponseWriter) { w.Header().Set("Location", "/elsewhere"); w.WriteHeader(302) }
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	expect(t, []any{response.StatusCode, response.Header.Get("X-Fleet-Error")}, []any{502, "provider-redirect"}, "redirect")
	expect(t, p.other.Load(), int32(0), "redirect target requests")
}

// Requirement: §9 limits: a download over replyBytes is refused from the lookup size, before the fetch.
// Risk: the gateway streams a file the connection's limit excludes.
func TestDownloadOversizeRefusedBeforeFetch(t *testing.T) {
	gw, p := newFileFixture(t, map[string]any{"replyBytes": 10})
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	expect(t, []any{response.StatusCode, response.Header.Get("X-Fleet-Error")}, []any{413, "reply-too-large"}, "oversize")
	_, _, fetches := p.seen()
	expect(t, len(fetches), 0, "fetches")
}

// Requirement: §9: a provider body shorter than the lookup size aborts the caller's connection, and the completed line
// records download-cut with the bytes actually sent. Risk: the caller receives a short file that ends cleanly.
func TestDownloadShortBodyAborts(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	p.blob = func(w http.ResponseWriter) {
		w.Write(blobBytes[:300])
		w.(http.Flusher).Flush() // chunked: no Content-Length to compare
	}
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err == nil {
		t.Fatalf("a short file ended cleanly after %d bytes", len(data))
	}
	line := lastLine(t, gw)
	expect(t, []any{line["phase"], line["outcome"], line["errorCode"], field(line, "transfer", "bytes"), field(line, "transfer", "sha256")},
		[]any{"completed", "error", "download-cut", 300, nil}, "completed line")
	// A chunked body longer than the lookup size is not this file either: the caller never gets all its bytes.
	p.blob = func(w http.ResponseWriter) {
		w.Write(blobBytes)
		w.(http.Flusher).Flush()
		time.Sleep(100 * time.Millisecond) // the file's bytes arrive alone first
		w.Write([]byte("more"))
	}
	if response, err = gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true); err != nil {
		t.Fatal(err)
	}
	data, err = io.ReadAll(response.Body)
	response.Body.Close()
	if err == nil {
		t.Fatalf("a longer body ended cleanly after %d bytes", len(data))
	}
	line = lastLine(t, gw)
	expect(t, []any{line["outcome"], line["errorCode"], field(line, "transfer", "sha256")}, []any{"error", "provider-body", nil}, "longer body")
}

// Requirement: §9: file bytes are never redacted; the sha256 in the completed line is that of the bytes sent.
// Risk: key redaction rewrites a binary file that happens to contain the key's bytes.
func TestDownloadBytesUnchanged(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	data := []byte("before invented-key-a after")
	p.meta = func() map[string]any {
		return map[string]any{"name": "k.bin", "size": len(data), "mimeType": "application/octet-stream", "url": "/blob/85-1"}
	}
	p.blob = func(w http.ResponseWriter) { w.Write(data) }
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(response.Body)
	response.Body.Close()
	expect(t, string(got), string(data), "bytes")
	expect(t, field(lastLine(t, gw), "transfer", "sha256"), shaHex(data), "sha")
}

// Requirement: §9: staged uploads a crash left behind are removed when the gateway starts. Risk: files fill state_dir.
func TestStagingLeftoversRemovedAtStart(t *testing.T) {
	gw, _ := newFileFixture(t, nil)
	gw.halt()
	os.WriteFile(filepath.Join(gw.dir, "state/files/left.part"), []byte("x"), 0o600)
	gw.start()
	expect(t, len(gw.parts()), 0, "staged files")
}

// Requirement: §9 download accounting: the started line (outcome unknown, lookup bytes, no sha) is durable before the
// caller gets the first header; the completed line has the same callId, ok, bytes and sha256 and nothing counted twice.
// Risk: a deploy cut mid-stream leaves no tool_run, or ingest counts usage or key problems twice.
func TestDownloadWritesTwoLines(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	release := make(chan struct{})
	p.blob = func(w http.ResponseWriter) {
		w.Header().Set("Content-Length", strconv.Itoa(len(blobBytes)))
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-release
		w.Write(blobBytes)
	}
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	started := lastLine(t, gw)
	close(release)
	data, _ := io.ReadAll(response.Body)
	response.Body.Close()
	expect(t, len(data), len(blobBytes), "bytes")
	expect(t, []any{started["callId"], started["phase"], started["outcome"], started["errorCode"], started["status"], field(started, "transfer", "bytes"), field(started, "transfer", "sha256"), started["keyFingerprint"]},
		[]any{response.Header.Get("X-Fleet-Call-Id"), "started", "unknown", "download-unfinished", 200, len(blobBytes), nil, fp("invented-key-a")}, "started line")
	lines, _ := gw.spool()
	expect(t, len(lines), 2, "lines")
	done := lines[1]
	expect(t, []any{done["callId"], done["phase"], done["outcome"], done["errorCode"], done["transfer"], done["usage"], done["disabled"], done["keyProblem"]},
		[]any{started["callId"], "completed", "ok", nil, map[string]any{"direction": "download", "bytes": len(blobBytes), "mediaType": "image/png", "name": "экран 1.png", "sha256": shaHex(blobBytes)}, nil, nil, nil}, "completed line")
}

// Requirement: §9 drain: a stop cancels uploads still staging with 503 draining; nothing reaches the provider and the
// temp file goes. Risk: a deploy waits for a slow upload or posts half a file.
func TestDrainCancelsStagingUpload(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	reader, writer := io.Pipe()
	defer writer.Close()
	go writer.Write([]byte("partial"))
	type result struct {
		status int
		v      map[string]any
		err    error
	}
	answered := make(chan result, 1)
	go func() {
		status, _, v, err := gw.upload(reader, 5000, strings.Repeat("0", 64), map[string]any{"issue": "TGT-12", "name": "big.mov"}, nil)
		answered <- result{status, v, err}
	}()
	for end := time.Now().Add(5 * time.Second); len(gw.parts()) == 0; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the upload did not start staging")
		}
	}
	begun := time.Now()
	gw.halt()
	writer.Close() // a client blocked writing its body reads its answer only once the body ends
	r := <-answered
	if r.err == nil {
		expect(t, []any{r.status, r.v["error"]}, []any{503, "draining"}, "drained upload")
	}
	if held := time.Since(begun); held > 2*time.Second {
		t.Fatalf("the stop waited %v for a staging upload", held)
	}
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider requests")
	expect(t, len(gw.parts()), 0, "staged files")
}

// Requirement: §9 drain: an upload whose post to the provider is in flight finishes within the drain, while one still
// staging at the stop is refused with draining and never posted. Risk: a deploy cuts a post (unknown outcome) it
// could have let finish, or posts an upload after the stop.
func TestDrainLetsPostFinish(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	p.onUpload = func(w http.ResponseWriter, read func()) {
		close(entered)
		<-release
		read()
		io.WriteString(w, `[{"id":"85-2"}]`)
	}
	posted := make(chan int, 1)
	go func() {
		first := []byte("first file")
		status, _, _, _ := gw.upload(bytes.NewReader(first), int64(len(first)), shaHex(first), map[string]any{"issue": "TGT-12", "name": "a.txt"}, nil)
		posted <- status
	}()
	<-entered
	reader, writer := io.Pipe()
	defer writer.Close()
	go writer.Write([]byte("second"))
	staged := make(chan int, 1)
	go func() {
		status, _, _, _ := gw.upload(reader, 5000, strings.Repeat("0", 64), map[string]any{"issue": "TGT-12", "name": "b.txt"}, nil)
		staged <- status
	}()
	for end := time.Now().Add(5 * time.Second); len(gw.parts()) < 2; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("the second upload did not start staging")
		}
	}
	stopped := make(chan struct{})
	go func() { close(gw.stop); <-gw.done; gw.stop = nil; close(stopped) }()
	var status int
	select {
	case status = <-staged:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("the staging upload was not refused at the stop")
	}
	writer.Close()
	if status != 503 {
		t.Fatalf("the staging upload answered %d, not draining", status)
	}
	close(release)
	expect(t, <-posted, 200, "the post in flight")
	<-stopped
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 1, "provider requests")
}

// Requirement: §9 key scan finds a value wherever the reads split it, also over several short reads.
// Risk: a value cut by a read boundary, or spread over reads shorter than itself, passes the scan.
func TestScannerFindsValueAcrossAnySplit(t *testing.T) {
	value := []byte("invented-key-value")
	data := append(append(bytes.Repeat([]byte("."), 40), value...), bytes.Repeat([]byte("."), 40)...)
	for size := 1; size <= len(data); size++ {
		for offset := 0; offset < size; offset++ {
			s := newScanner([][]byte{[]byte("other-pattern-longer-than-the-value"), value})
			found := s.write(data[:offset])
			for i := offset; i < len(data) && !found; i += size {
				found = s.write(data[i:min(i+size, len(data))])
			}
			if !found {
				t.Fatalf("missed with reads of %d after %d", size, offset)
			}
		}
	}
	if s := newScanner([][]byte{value}); s.write(data[:57]) || s.write(data[58:]) {
		t.Fatal("found a value with a byte missing")
	}
}

// Requirement: §9 stall: once the provider starts answering an upload (a 1xx, or part of its status line), the rest of
// its answer must still progress within stallMs. Risk: a provider that sends one byte or a 100 Continue and stops holds
// the call, its staged file and the caller's connection forever.
func TestUploadProviderStallsAfterFirstByte(t *testing.T) {
	gw, p := newFileFixture(t, map[string]any{"stallMs": 300})
	done := make(chan struct{})
	defer close(done)
	for _, start := range []string{"HTTP/1.1 100 Continue\r\n\r\n", "HTTP/1.1 2"} {
		p.onUpload = func(w http.ResponseWriter, read func()) {
			read()
			conn, buffered, _ := w.(http.Hijacker).Hijack()
			buffered.WriteString(start)
			buffered.Flush()
			<-done
			conn.Close()
		}
		begun, answered := time.Now(), make(chan []any, 1)
		go func() {
			data := []byte("invented")
			status, _, v, _ := gw.upload(bytes.NewReader(data), int64(len(data)), shaHex(data), map[string]any{"issue": "TGT-12", "name": "a.txt"}, nil)
			answered <- []any{status, v["error"]}
		}()
		select {
		case got := <-answered:
			expect(t, got, []any{504, "provider-stalled"}, fmt.Sprintf("%q", start))
		case <-time.After(3 * time.Second):
			t.Fatalf("%q: the call hangs after the provider's first byte", start)
		}
		if held := time.Since(begun); held > 1200*time.Millisecond {
			t.Fatalf("%q: the call took %v with stallMs 300", start, held)
		}
		expect(t, lastLine(t, gw)["outcome"], "unknown", "outcome")
	}
}

// Requirement: §9 upload: a call admitted before a long staging is admitted again before the key is attached.
// Risk: a token revoked, intake paused or budget reached while the file was arriving still posts it with the key.
func TestUploadAdmittedAgainBeforePost(t *testing.T) {
	gw, p := newFileFixture(t, nil)
	data := []byte("a file that arrives slowly")
	reader, writer := io.Pipe()
	answered := make(chan []any, 1)
	go func() {
		status, _, v, _ := gw.upload(reader, int64(len(data)), shaHex(data), map[string]any{"issue": "TGT-12", "name": "slow.txt"}, nil)
		answered <- []any{status, v["error"]}
	}()
	writer.Write(data[:5])
	gw.write(fileSnapshot(p.url, nil, "paused"))
	gw.adoptWritten()
	writer.Write(data[5:])
	writer.Close()
	expect(t, <-answered, []any{403, "intake-paused"}, "paused while staging")
	uploads, _, _ := p.seen()
	expect(t, len(uploads), 0, "provider requests")
}

// Requirement: §9 stall: an empty file's body check is bounded by stallMs like every other provider read.
// Risk: a provider that answers a zero-size attachment and then sends nothing holds the call and its connection forever.
func TestDownloadEmptyFileProviderStalls(t *testing.T) {
	gw, p := newFileFixture(t, map[string]any{"stallMs": 300})
	release := make(chan struct{})
	defer close(release)
	p.meta = func() map[string]any {
		return map[string]any{"name": "empty.txt", "size": 0, "mimeType": "text/plain", "url": "/blob/85-1"}
	}
	p.blob = func(w http.ResponseWriter) {
		w.WriteHeader(200)
		w.(http.Flusher).Flush() // chunked, no length: the body is still to come
		<-release
	}
	answered := make(chan []any, 1)
	go func() {
		response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
		if err != nil {
			answered <- []any{0, err.Error()}
			return
		}
		response.Body.Close()
		answered <- []any{response.StatusCode, response.Header.Get("X-Fleet-Error")}
	}()
	select {
	case got := <-answered:
		expect(t, got, []any{504, "provider-stalled"}, "stalled empty file")
	case <-time.After(3 * time.Second):
		t.Fatal("the download of an empty file hangs on a silent provider")
	}
	// An empty file the provider ends at once is a whole download.
	p.blob = func(w http.ResponseWriter) { w.Header().Set("Content-Length", "0") }
	response, err := gw.download(`{"issue":"TGT-12","attachment":"85-1"}`, true)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	expect(t, []any{response.StatusCode, len(data), err == nil, field(lastLine(t, gw), "transfer", "sha256")}, []any{200, 0, true, shaHex(nil)}, "empty file")
}
