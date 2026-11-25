package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"fleet/contract"
)

func (e *testCoordEnv) post(t *testing.T, path string, body []byte, headers map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequestWithContext(e.ctx, "POST", e.server.URL+path, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

func envelope(dsn string, event map[string]any) []byte {
	ev, _ := json.Marshal(event)
	return []byte(`{"event_id":"0123456789abcdef0123456789abcdef","dsn":"` + dsn + `","sdk":{"name":"sentry.javascript.browser"},"trace":{"user_id":"spoof@x"}}` + "\n" +
		`{"type":"event","length":` + fmt.Sprint(len(ev)) + `}` + "\n" + string(ev) + "\n" +
		`{"type":"session"}` + "\n" + `{"did":"spoof@x"}` + "\n" +
		`{"type":"client_report"}` + "\n" + `{"discarded_events":[]}` + "\n")
}

// Requirement: fleet-error-tracking.md, project mapping: errors.bugsink takes Bugsink project names only. Risk: a typo
// or an injected string becomes a mapping that never matches, silently.
func TestErrorsSetting(t *testing.T) {
	s, unknown, err := projectSettings(doc{"errors": doc{"bugsink": []any{"fleet-web", "fleet-coordinator"}}})
	if err != nil || len(unknown) != 0 || len(anys(mapOf(s["errors"])["bugsink"])) != 2 {
		t.Fatalf("%v %v %v", s["errors"], unknown, err)
	}
	if s, _, _ := projectSettings(doc{}); len(anys(mapOf(s["errors"])["bugsink"])) != 0 {
		t.Fatalf("default: %v", s["errors"])
	}
	if s, _, err := projectSettings(doc{"errors": doc{"ignore": []any{"ResizeObserver loop"}}}); err != nil || len(anys(mapOf(s["errors"])["ignore"])) != 1 {
		t.Fatalf("ignore: %v %v", s["errors"], err)
	}
	for _, bad := range []any{"", " ", 3} {
		if _, _, err := projectSettings(doc{"errors": doc{"ignore": []any{bad}}}); err == nil || !strings.Contains(err.Error(), "errors.ignore") {
			t.Fatalf("ignore %v: %v", bad, err)
		}
	}
	for _, bad := range []any{"Fleet Web", "", 3, "../x"} {
		if _, _, err := projectSettings(doc{"errors": doc{"bugsink": []any{bad}}}); err == nil || !strings.Contains(err.Error(), "errors.bugsink") {
			t.Fatalf("%v: %v", bad, err)
		}
	}
}

// Requirement: fleet-error-tracking.md, Owner Needs you: a problem closes when issue_get reports the issue resolved or
// Bugsink's own 404; any other answer (a gateway refusal's 404, an unreadable reply) is an error. Risk: a moved API or a
// refused tool withdraws every held problem at once.
func TestIssueClosed(t *testing.T) {
	code := func(s string) *string { return &s }
	for name, c := range map[string]struct {
		line         contract.CallLine
		closed, fail bool
	}{
		"resolved":     {contract.CallLine{Outcome: contract.CallOutcomeOk, Output: json.RawMessage(`{"is_resolved":true}`)}, true, false},
		"open":         {contract.CallLine{Outcome: contract.CallOutcomeOk, Output: json.RawMessage(`{"is_resolved":false}`)}, false, false},
		"no field":     {contract.CallLine{Outcome: contract.CallOutcomeOk, Output: json.RawMessage(`{}`)}, false, true},
		"bugsink 404":  {contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: code("http-404"), Output: json.RawMessage(`{"detail":"No Issue matches the given query."}`)}, true, false},
		"other 404":    {contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: code("http-404"), Output: json.RawMessage(`{"text":"Not Found"}`)}, false, true},
		"unknown tool": {contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: code("unknown-tool"), Output: json.RawMessage(`{"detail":"x"}`)}, false, true},
	} {
		closed, err := issueClosed(c.line, "i")
		if closed != c.closed || (err != nil) != c.fail {
			t.Fatalf("%s: %v %v", name, closed, err)
		}
	}
}

// Requirement: fleet-error-tracking.md, agent read access: the gateway reads Bugsink at FLEET_BUGSINK_INGEST, the one
// loopback setting the tunnel uses too, and other providers keep their catalogue address. Risk: moving Bugsink's port
// breaks agent reads and the resolution pass until a code release.
func TestSnapshotProviders(t *testing.T) {
	t.Setenv("FLEET_BUGSINK_INGEST", "")
	if got := snapshotProviders()[bugsinkConn].BaseUrl; got != "http://127.0.0.1:9510" {
		t.Fatalf("default %s", got)
	}
	t.Setenv("FLEET_BUGSINK_INGEST", "http://127.0.0.1:9999/")
	p := snapshotProviders()
	if p[bugsinkConn].BaseUrl != "http://127.0.0.1:9999" || p["github"].BaseUrl != "https://api.github.com" || gatewayProviders[bugsinkConn].BaseUrl != "http://127.0.0.1:9510" {
		t.Fatalf("override: %s %s %s", p[bugsinkConn].BaseUrl, p["github"].BaseUrl, gatewayProviders[bugsinkConn].BaseUrl)
	}
}
