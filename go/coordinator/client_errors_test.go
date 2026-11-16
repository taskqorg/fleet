package main

import (
	"strings"
	"testing"
	"time"
)

// Requirement: fleet-telemetry.md section 9: one browser error keeps one fingerprint across hosts and rebuilds that only
// move its frame (origin, asset hash, line and column). Risk: every deploy splits one error into new groups and problems.
func TestClientTopFrame(t *testing.T) {
	v8a := "RangeError: Invalid time value\n    at Date.toISOString (<anonymous>)\n    at xy (https://fleet.ufobe.com/assets/main-ABCD1234.js:1:20055)"
	v8b := "RangeError: Invalid time value\n    at Date.toISOString (<anonymous>)\n    at xy (http://127.0.0.1:4100/assets/main-ZZZZ9999.js:3:11)"
	if a, b := clientTopFrame(v8a), clientTopFrame(v8b); a != b || a != "at xy (/assets/main.js)" {
		t.Fatalf("%q %q", a, b)
	}
	if got := clientTopFrame("RangeError: x\n    at xy (https://h/assets/main-ABCD1234.js:1:2)"); got != "at xy (/assets/main.js)" {
		t.Fatal(got)
	}
	if got := clientTopFrame("render@https://h/assets/chunk-QWERTY12.js:10:4\n@https://h/x.js:1:1"); got != "render@/assets/chunk.js" {
		t.Fatal(got)
	}
	if got := clientTopFrame("Error: mail me at a@b.c\n"); got != "" {
		t.Fatal(got)
	}
	if got := clientTopFrame("TypeError: x\n    at Array.map (<anonymous>)"); got != "at Array.map (<anonymous>)" {
		t.Fatal(got)
	}
}

func clientReport(over map[string]any) map[string]any {
	r := map[string]any{"name": "RangeError", "message": "Invalid time value", "stack": "RangeError: Invalid time value\n    at fmt (https://fleet.test/assets/main-ABCD1234.js:1:20)",
		"path": "/delivery?sort=-since", "version": "ui-1", "userAgent": "Chromium", "at": time.Now().UTC().Format(time.RFC3339Nano), "count": 1, "source": "error"}
	for k, v := range over {
		if v == nil {
			delete(r, k)
		} else {
			r[k] = v
		}
	}
	return r
}

func personHeaders(e *testCoordEnv, email string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": email}
}

// Requirement: fleet-telemetry.md section 9: any signed-in person reports (sizes checked strictly, the person from the
// session), reports group by fingerprint with counts, people and versions, a new group opens one needs-you problem, only
// full access reads, storage stays bounded per group and in age. Risk: a render loop floods storage or needs-you, a
// non-owner reads other people's errors, or an oversized report is silently cut.
func TestClientErrorOps(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner, member, agents := e.ownerHeaders(), personHeaders(e, "member@trial.test"), personHeaders(e, "agents@trial.test")
	report := func(h map[string]string, over map[string]any) (int, map[string]any) {
		return e.callJSON(t, "POST", "/api/ops/client_error_report", clientReport(over), h)
	}

	for name, over := range map[string]map[string]any{
		"oversized stack": {"stack": strings.Repeat("x", 8193)}, "oversized message": {"message": strings.Repeat("x", 2001)},
		"missing name": {"name": ""}, "absent name": {"name": nil}, "relative path": {"path": "delivery"}, "bad time": {"at": "yesterday"},
		"zero count": {"count": 0}, "bad source": {"source": "console"}, "oversized agent": {"userAgent": strings.Repeat("x", 513)},
	} {
		if code, body := report(member, over); code != 400 {
			t.Fatalf("%s: %d %v", name, code, body)
		}
	}

	code, first := report(member, map[string]any{"count": 3})
	if code != 200 || first["first"] != true || first["count"] != float64(3) {
		t.Fatalf("first report: %d %v", code, first)
	}
	fp := first["fingerprint"].(string)
	// The same error from another host and build: same group.
	code, again := report(owner, map[string]any{"stack": "RangeError: Invalid time value\n    at fmt (http://127.0.0.1:9/assets/main-ZZZZ9999.js:7:1)", "version": "ui-2", "path": "/tasks"})
	if code != 200 || again["fingerprint"] != fp || again["first"] != false || again["count"] != float64(4) {
		t.Fatalf("repeat: %d %v", code, again)
	}
	if code, other := report(member, map[string]any{"message": "Other"}); code != 200 || other["fingerprint"] == fp || other["first"] != true {
		t.Fatalf("other: %d %v", code, other)
	}

	// Reading needs full access: a member reports but cannot read; the agents' person (grant *) and the owner read.
	if code, _ := e.callJSON(t, "POST", "/api/ops/client_errors_list", map[string]any{}, member); code != 403 {
		t.Fatalf("member list: %d", code)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/client_error_get", map[string]any{"fingerprint": fp}, member); code != 403 {
		t.Fatalf("member get: %d", code)
	}
	code, list := e.callJSON(t, "POST", "/api/ops/client_errors_list", map[string]any{}, agents)
	errs, _ := list["errors"].([]any)
	if code != 200 || len(errs) != 2 {
		t.Fatalf("list: %d %v", code, list)
	}
	code, got := e.callJSON(t, "POST", "/api/ops/client_error_get", map[string]any{"fingerprint": fp}, owner)
	g, _ := got["error"].(map[string]any)
	if code != 200 || g["count"] != float64(4) || g["lastPath"] != "/tasks" || g["lastVersion"] != "ui-2" || g["topFrame"] != "at fmt (/assets/main.js)" {
		t.Fatalf("get: %d %v", code, got)
	}
	if p := g["people"].([]any); len(p) != 2 || p[0] != "member@trial.test" || p[1] != testOwnerEmail {
		t.Fatalf("people: %v", p)
	}
	if v := g["versions"].([]any); len(v) != 2 || v[1] != "ui-2" {
		t.Fatalf("versions: %v", v)
	}
	if rs := got["reports"].([]any); len(rs) != 2 || rs[0].(map[string]any)["person"] != testOwnerEmail || rs[1].(map[string]any)["count"] != float64(3) {
		t.Fatalf("reports: %v", rs)
	}
	qid, _ := g["questionId"].(string)
	if qid == "" {
		t.Fatalf("no problem question: %v", g)
	}
	var open int
	if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT count(*) FROM question WHERE cause=$1 AND state='open' AND project_id IS NULL AND addressed_to=$2", clientErrorCause+fp, testOwnerEmail).Scan(&open); err != nil || open != 1 {
		t.Fatalf("problem questions: %d %v", open, err)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/client_error_get", map[string]any{"fingerprint": "nope"}, owner); code != 404 {
		t.Fatalf("unknown get: %d", code)
	}

	// Filters: version and since.
	code, list = e.callJSON(t, "POST", "/api/ops/client_errors_list", map[string]any{"version": "ui-2"}, owner)
	if errs, _ = list["errors"].([]any); code != 200 || len(errs) != 1 || errs[0].(map[string]any)["fingerprint"] != fp {
		t.Fatalf("version filter: %d %v", code, list)
	}
	code, list = e.callJSON(t, "POST", "/api/ops/client_errors_list", map[string]any{"since": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)}, owner)
	if errs, _ = list["errors"].([]any); code != 200 || len(errs) != 0 {
		t.Fatalf("since filter: %d %v", code, list)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/client_errors_list", map[string]any{"limit": 501}, owner); code != 400 {
		t.Fatalf("limit: %d", code)
	}

	// Bounded per group: the latest clientErrorReportsKept reports stay; the count keeps every repeat.
	for i := 0; i < clientErrorReportsKept+5; i++ {
		report(owner, nil)
	}
	code, got = e.callJSON(t, "POST", "/api/ops/client_error_get", map[string]any{"fingerprint": fp}, owner)
	if rs := got["reports"].([]any); code != 200 || len(rs) != clientErrorReportsKept || got["error"].(map[string]any)["count"] != float64(4+clientErrorReportsKept+5) {
		t.Fatalf("kept reports: %d %d %v", code, len(rs), got["error"])
	}

	// Bounded in age: a group unseen past retention goes with its problem when a new group arrives.
	if _, err := e.ownerStore.pool.Exec(e.ctx, "UPDATE client_error SET last_seen=now()-interval '31 days' WHERE fingerprint=$1", fp); err != nil {
		t.Fatal(err)
	}
	if code, _ := report(owner, map[string]any{"message": "Third"}); code != 200 {
		t.Fatal(code)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/client_error_get", map[string]any{"fingerprint": fp}, owner); code != 404 {
		t.Fatalf("pruned get: %d", code)
	}
	var state string
	if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT state::text FROM question WHERE id=$1", qid).Scan(&state); err != nil || state != "withdrawn" {
		t.Fatalf("pruned problem: %s %v", state, err)
	}
	var reports int
	if err := e.ownerStore.pool.QueryRow(e.ctx, "SELECT count(*) FROM client_error_report WHERE fingerprint=$1", fp).Scan(&reports); err != nil || reports != 0 {
		t.Fatalf("pruned reports: %d %v", reports, err)
	}
}
