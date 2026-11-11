// Gateway behaviour through the real server and a scripted provider (fleet-connections.md §2-3), ported from
// the Rust gateway's integration tests. Risk covered: a token, key or snapshot rule that passes in isolation but fails in the
// running server, and a key value that leaks to the caller or the spool.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		for _, l := range strings.Split(strings.TrimSpace(string(data)), "\n") {
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

// adopt waits a few snapshot polls (refreshEvery is 10 ms in tests).
func adopt() { time.Sleep(60 * time.Millisecond) }

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
	adopt()
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
	adopt()
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
	adopt()
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
	adopt()
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
