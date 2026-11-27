package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func shippedGapRules(t *testing.T) *gapRules {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "agent-gaps.json"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := parseGapRules(raw)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Requirement: a failed agent tool call or gateway call gets the class of what the agent lacked, with the line that
// says so; a shell command's own failure and a call counted elsewhere are no gap. Risk: "command not found: jq",
// sandbox EPERM and an unknown gateway tool sat only in raw events for weeks (agent-garden audit, 2026-11-26). The
// texts are failures of PDS18's last week.
func TestAgentGapRules(t *testing.T) {
	r := shippedGapRules(t)
	for _, c := range []struct {
		f     gapFailure
		class string
		line  string
	}{
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "ok\n/bin/bash: line 1: jq: command not found\n"}, "missing-command:jq", "/bin/bash: line 1: jq: command not found"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "zsh:1: command not found: tail"}, "missing-command:tail", "zsh:1: command not found: tail"},
		{gapFailure{Source: "agent", Tool: "Bash", Text: "node preinstall: /bin/sh: 1: npm: not found"}, "missing-command:npm", "node preinstall: /bin/sh: 1: npm: not found"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "zsh:2: no such file or directory: /usr/bin/rg"}, "missing-command:rg", "zsh:2: no such file or directory: /usr/bin/rg"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "zsh:4: no such file or directory: /private/tmp/x/pg.conf"}, "", ""},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "ModuleNotFoundError: No module named 'PIL'"}, "missing-module:PIL", "ModuleNotFoundError: No module named 'PIL'"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "go: could not create module cache: mkdir /x/go: operation not permitted"}, "sandbox-write", "go: could not create module cache: mkdir /x/go: operation not permitted"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "FATAL:  could not create shared memory segment: Operation not permitted"}, "sandbox-shm", "FATAL:  could not create shared memory segment: Operation not permitted"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "zsh:kill:1: kill 82131 failed: operation not permitted"}, "", ""},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "fatal: unable to access 'https://github.com/x.git/': Could not resolve host: github.com"}, "sandbox-network", "fatal: unable to access 'https://github.com/x.git/': Could not resolve host: github.com"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "curl: (7) Failed to connect to 127.0.0.1 port 4174 after 2 ms"}, "", ""},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "To get started with GitHub CLI, please run:  gh auth login"}, "auth-unready", "To get started with GitHub CLI, please run:  gh auth login"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "ERROR: run `glab auth login` to authenticate"}, "auth-unready", "ERROR: run `glab auth login` to authenticate"},
		{gapFailure{Source: "agent", Tool: "Bash", Text: `glab: oauth2: "invalid_grant"`}, "auth-unready", `glab: oauth2: "invalid_grant"`},
		{gapFailure{Source: "agent", Tool: "Bash", Text: "<tool_use_error>Error: No such tool available: Bash. Bash is disabled for this session.</tool_use_error>"}, "tool-unavailable:Bash", "<tool_use_error>Error: No such tool available: Bash. Bash is disabled for this session.</tool_use_error>"},
		{gapFailure{Source: "agent", Tool: "evaluate", Connection: "project-browser", Text: "Mcp error: -32602: Browser operation failed, denied or timed out; outcome unknown"}, "browser-unready", "Mcp error: -32602: Browser operation failed, denied or timed out; outcome unknown"},
		{gapFailure{Source: "agent", Tool: "task_page", Connection: "fleet", Text: `{"error":"Coordinator rejected delegation","message":"Unknown filter field archived","statusCode":400}`}, "fleet-refused:task_page", `{"error":"Coordinator rejected delegation","message":"Unknown filter field archived","statusCode":400}`},
		{gapFailure{Source: "agent", Tool: "board_read", Text: `{"instruction":"Outcome unknown; use agent_tool_result with this requestKey."}`}, "fleet-unconfirmed:board_read", `{"instruction":"Outcome unknown; use agent_tool_result with this requestKey."}`},
		// A shell command that prints a refusal it read from a file is still only a failed command.
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: `rg: "Coordinator rejected delegation"`}, "", ""},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "FAIL tests/ui/x.test.ts\nAssertionError"}, "", ""},
		{gapFailure{Source: "agent", Tool: "web_search", Text: "Call error: http-432 {\"detail\":\"exceeds your plan\"}"}, "", ""},
		{gapFailure{Source: "agent", Tool: "Read", Text: "File does not exist. Note: your current working directory is /w."}, "", ""},
		{gapFailure{Source: "agent", Tool: "code", Text: "[code]\nTypeError: fleet.artifacts_list is not a function"}, "other", "TypeError: fleet.artifacts_list is not a function"},
		{gapFailure{Source: "agent", Tool: "commandExecution", Text: "config: not found"}, "", ""},
		{gapFailure{Source: "agent", Tool: "web_search", Connection: "tavily", Text: "tool call error: tool call failed\n Call error: http-432"}, "", ""},
		{gapFailure{Source: "agent", Tool: "result", Text: `{"type":"result","subtype":"error_max_turns"}`}, "", ""},
		{gapFailure{Source: "agent", Tool: "turn.failed", Text: "The 'gpt-6.2-sol' model is not supported when using Codex"}, "model-unsupported", "The 'gpt-6.2-sol' model is not supported when using Codex"},
		{gapFailure{Source: "gateway", Tool: "web_search", Connection: "tavily", Text: "provider-432"}, "gateway-quota", "provider-432"},
		{gapFailure{Source: "gateway", Tool: "commit_status", Connection: "github", Text: "unknown-tool"}, "gateway-unknown-tool", "unknown-tool"},
		{gapFailure{Source: "gateway", Tool: "pull_get", Connection: "github", Text: "not-granted"}, "mcp-refused:github/pull_get", "not-granted"},
		{gapFailure{Source: "gateway", Tool: "issue_get", Connection: "youtrack", Text: "provider-404"}, "gateway-rejected:youtrack/issue_get", "provider-404"},
		{gapFailure{Source: "gateway", Tool: "x", Connection: "y", Text: "caller-cancelled"}, "", ""},
		{gapFailure{Source: "gateway", Tool: "x", Connection: "y", Text: "secret-found"}, "other", "secret-found"},
		// Dispatch: a queued run no host can take until a setting changes (2026-11-26, a DAM turn on a broken glab login).
		{gapFailure{Source: "dispatch", Tool: "forge", Text: "project forge connection unavailable on owner-mac"}, "auth-unready", "project forge connection unavailable on owner-mac"},
		{gapFailure{Source: "dispatch", Tool: "roots", Text: "project files not under FLEET_PROJECT_ROOTS of windows"}, "placement-blocked:roots", "project files not under FLEET_PROJECT_ROOTS of windows"},
	} {
		class, line, ok := r.classify(c.f)
		if !ok {
			class = ""
		}
		if class != c.class || line != c.line {
			t.Errorf("%s %q: got %q %q, want %q %q", c.f.Tool, c.f.Text, class, line, c.class, c.line)
		}
	}
	for name, fam := range r.Families {
		if fam.Title == "" || fam.Setting.Kind == "" || fam.Setting.Ref == "" || fam.Setting.Text == "" {
			t.Errorf("family %s: a title and a setting (kind, ref, text) are required", name)
		}
	}
}

// Requirement: the failure text is the tool's own: an MCP result's text, or, when Codex keeps the output empty, the
// item's error; the tool's server comes from the MCP name or the Codex item. Risk: Codex MCP failures stored output
// null (the error sat in item.error), and they all read as unclassified.
func TestAgentGapEventFailures(t *testing.T) {
	var codex, claude rawObject
	json.Unmarshal([]byte(`{"item":{"type":"mcpToolCall","tool":"evaluate","server":"project-browser","result":null,
		"error":{"message":"tool call failed\n    Mcp error: -32602: Browser operation failed, denied or timed out"}},
		"fleetNormalized":{"blocks":[{"kind":"text","text":"x"},{"kind":"tool","name":"evaluate","state":"failed","output":null}]}}`), &codex)
	json.Unmarshal([]byte(`{"fleetNormalized":{"blocks":[{"kind":"tool","name":"mcp__fleet__task_page","state":"failed",
		"output":{"content":[{"type":"text","text":"{\"error\":\"Coordinator rejected delegation\"}"}]}},
		{"kind":"tool","name":"Read","state":"completed","output":"ok"}]}}`), &claude)
	r := shippedGapRules(t)
	got := eventFailures(codex)
	if len(got) != 1 || got[0].index != 1 || got[0].Connection != "project-browser" || got[0].Tool != "evaluate" {
		t.Fatalf("codex: %+v", got)
	}
	if c, _, _ := r.classify(got[0].gapFailure); c != "browser-unready" {
		t.Fatalf("codex class %q", c)
	}
	got = eventFailures(claude)
	if len(got) != 1 || got[0].Connection != "fleet" || got[0].Tool != "task_page" || gapToolName(got[0].gapFailure) != "fleet/task_page" {
		t.Fatalf("claude: %+v", got)
	}
	if c, _, _ := r.classify(got[0].gapFailure); c != "fleet-refused:task_page" {
		t.Fatalf("claude class %q", c)
	}
}

// Requirement: the read counts each class per day over the whole window (zero days included), compares it with the
// window before, breaks it down and names its setting, with one example per execution. Risk: a sparkline without its
// empty days, or five examples of one run, hide whether a fix cut the class.
func TestAgentGapsReply(t *testing.T) {
	r := shippedGapRules(t)
	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	now := from.AddDate(0, 0, 6).Add(10 * time.Hour)
	host, harness, sess := "pds18-native", "codex", "s1"
	row := func(id, exec, class string, at time.Time) db.AgentGapRowsRow {
		return db.AgentGapRowsRow{ID: id, ExecutionID: exec, Source: "agent", At: at, Class: class, Family: gapFamilyOf(class), Tool: "commandExecution",
			Line: "jq: command not found", Rules: r.digest, HostID: &host, Harness: &harness, ProfileID: "codex-worker", SessionID: &sess}
	}
	rows := []db.AgentGapRowsRow{ // newest first
		row("a", "x1", "missing-command:jq", now.Add(-time.Hour)),
		row("b", "x1", "missing-command:jq", now.Add(-2*time.Hour)),
		row("c", "x2", "missing-command:jq", from.Add(time.Hour)),
		row("d", "x3", "sandbox-write", from.Add(2*time.Hour)),
		row("e", "x4", "missing-command:jq", from.Add(-time.Hour)), // the window before
	}
	out := agentGapsReplyOf(r, rows, &contract.AgentGapsRequest{}, nil, from, now, 7)
	if out.Total != 4 || len(out.Classes) != 2 {
		t.Fatalf("total %d classes %d", out.Total, len(out.Classes))
	}
	jq := out.Classes[0]
	if out.Runs != 3 || out.Calls != 0 || jq.Label != "jq missing" || jq.Source != "agent" || *jq.AboveThreshold {
		t.Fatalf("runs %d calls %d label %q source %q above %v", out.Runs, out.Calls, jq.Label, jq.Source, *jq.AboveThreshold)
	}
	if jq.Class != "missing-command:jq" || jq.Count != 3 || jq.PreviousCount != 1 || jq.Executions != 2 || len(jq.Examples) != 2 {
		t.Fatalf("jq: %+v", jq)
	}
	if len(jq.Daily) != 7 || jq.Daily[0].Count != 1 || jq.Daily[6].Count != 2 || jq.Daily[3].Count != 0 {
		t.Fatalf("daily: %+v", jq.Daily)
	}
	if jq.Setting.Kind != "host-tools" || jq.Hosts[0].Key != host || jq.Hosts[0].Count != 3 || deref(jq.Examples[0].Path) != "/conversations/s1" {
		t.Fatalf("setting, hosts or path: %+v", jq)
	}
	if one := agentGapsReplyOf(r, rows, &contract.AgentGapsRequest{Class: ptr("sandbox-write")}, nil, from, now, 7); len(one.Classes) != 1 || one.Total != 1 {
		t.Fatalf("class filter: %+v", one)
	}
	if fam := agentGapsReplyOf(r, rows, &contract.AgentGapsRequest{Class: ptr("missing-command")}, nil, from, now, 7); len(fam.Classes) != 1 || fam.Total != 3 {
		t.Fatalf("family filter: %+v", fam)
	}
}

// Requirement: the stored queries find an agent run's failed tool blocks and failed gateway calls, and the read joins
// each gap to its run's host, harness and profile. Risk: a wrong JSON path or join reads nothing on prod, so
// agent_gaps answers empty while agents keep failing.
func TestAgentGapQueries(t *testing.T) {
	sockDir, port, _ := ensureTestPostgres(t)
	name := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	cloneTestDB(t, name)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", name, sockDir, port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		"SET fleet.project_ids = '*'",
		`INSERT INTO execution(id, kind, state, host_id, harness, project_id, session_id, config_ref, started_at, ended_at)
		 VALUES ('x1', 'agent_run', 'failed', 'pds18-native', 'codex', 'fleet', NULL, '{"profileId":"codex-worker"}', now() - interval '1 hour', now())`,
		`INSERT INTO execution(id, kind, state, host_id, started_at) VALUES ('x2', 'agent_run', 'failed', 'owner-mac', now())`,
		`INSERT INTO execution(id, kind, state, parent_execution_id, project_id, started_at, data)
		 VALUES ('call:1', 'tool_run', 'failed', 'x1', 'fleet', now(), '{"tool":"commit_status","connection":"github","errorCode":"unknown-tool","outcome":"error"}')`,
		`INSERT INTO event(id, execution_id, at, payload) VALUES
		 ('event:x1:1', 'x1', now(), '{"data":{"fleetNormalized":{"blocks":[{"kind":"tool","name":"commandExecution","state":"failed","output":"/bin/bash: line 1: jq: command not found"}]}}}'),
		 ('event:x1:2', 'x1', now(), '{"data":{"fleetNormalized":{"blocks":[{"kind":"tool","name":"commandExecution","state":"completed","output":"ok"}]}}}')`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	q := db.New(conn)
	since := gapTime(time.Now().Add(-24 * time.Hour))
	runs, err := q.AgentGapRuns(ctx, db.AgentGapRunsParams{Since: since, After: "", Lim: 10})
	if err != nil || len(runs) != 2 || runs[0].ID != "x1" {
		t.Fatalf("runs %+v %v", runs, err)
	}
	evs, err := q.AgentGapFailedEvents(ctx, "x1")
	if err != nil || len(evs) != 1 || evs[0].ID != "event:x1:1" {
		t.Fatalf("failed events %+v %v", evs, err)
	}
	calls, err := q.AgentGapFailedCalls(ctx, since)
	if err != nil || len(calls) != 1 || calls[0].ErrorCode != "unknown-tool" || deref(calls[0].ParentExecutionID) != "x1" {
		t.Fatalf("failed calls %+v %v", calls, err)
	}
	if ok, err := q.AgentGapRunExists(ctx, "x1"); err != nil || !ok {
		t.Fatalf("run exists %v %v", ok, err)
	}
	for _, p := range []db.AgentGapPutParams{
		{ID: "event:x1:1#0", ExecutionID: "x1", ProjectID: ptr("fleet"), Source: "agent", At: gapTime(time.Now()), Class: "missing-command:jq", Family: "missing-command", Tool: "commandExecution", Line: "jq", Rules: "r1"},
		{ID: "event:x1:1#0", ExecutionID: "x1", ProjectID: ptr("fleet"), Source: "agent", At: gapTime(time.Now()), Class: "missing-command:jq", Family: "missing-command", Tool: "commandExecution", Line: "jq", Rules: "r2"},
		{ID: "event:x2:1#0", ExecutionID: "x2", Source: "agent", At: gapTime(time.Now()), Class: "sandbox-write", Family: "sandbox-write", Tool: "Bash", Line: "EPERM", Rules: "r2"},
		{ID: "call:1", ExecutionID: "x1", ProjectID: ptr("fleet"), Source: "gateway", At: gapTime(time.Now()), Class: "gateway-unknown-tool", Family: "gateway-unknown-tool", Tool: "github/commit_status", Line: "unknown-tool", Rules: "r2"},
	} {
		if err := q.AgentGapPut(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := q.AgentGapRows(ctx, db.AgentGapRowsParams{Since: since, Until: gapTime(time.Now().Add(time.Hour))})
	// A run without a profile (no config_ref) reads as no profile, not a scan error (prod, 2026-11-26).
	if err != nil || len(rows) != 3 || !slices.ContainsFunc(rows, func(r db.AgentGapRowsRow) bool { return r.ExecutionID == "x2" && r.ProfileID == "" }) {
		t.Fatalf("rows %+v %v", rows, err)
	}
	rows = slices.DeleteFunc(rows, func(r db.AgentGapRowsRow) bool { return r.ExecutionID == "x2" })
	if len(rows) != 2 || rows[0].ProfileID != "codex-worker" || deref(rows[0].HostID) != "pds18-native" || rows[1].Rules != "r2" {
		t.Fatalf("rows %+v %v", rows, err)
	}
	if n, err := q.AgentGapsClear(ctx, []string{"x1"}); err != nil || n != 1 {
		t.Fatalf("clear agent rows %d %v (the gateway row stays)", n, err)
	}
	if n, err := q.AgentGapsClearGateway(ctx, since); err != nil || n != 1 {
		t.Fatalf("clear gateway %d %v", n, err)
	}
	if err := q.AgentGapPassPut(ctx, db.AgentGapPassPutParams{Rules: "r2", Days: 30}); err != nil {
		t.Fatal(err)
	}
	if done, err := q.AgentGapPassDone(ctx, db.AgentGapPassDoneParams{Rules: "r2", Days: 30}); err != nil || !done {
		t.Fatalf("pass done %v %v", done, err)
	}
	if done, _ := q.AgentGapPassDone(ctx, db.AgentGapPassDoneParams{Rules: "r3", Days: 30}); done {
		t.Fatal("another digest counted as done")
	}

	t.Run("gateway rebuild preserves occurrence windows", func(t *testing.T) {
		testGatewayGapRebuild(t, ctx, conn, name, sockDir, port)
	})
}

// Requirement: legacy gateway failures use the call's retained time, even when a previous rebuild put them in a
// newer window. Risk: a narrow rebuild loses history, or a wide or repeated rebuild counts an old failure as new.
func testGatewayGapRebuild(t *testing.T, ctx context.Context, conn *pgx.Conn, name, sockDir string, port int) {
	t.Helper()
	t.Setenv("FLEET_PROJECT_CONFIG_DIRECTORY", filepath.Join("..", "..", "config"))
	store, err := openStore(ctx, testPGURL("fleet_app", name, sockDir, port))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	app := &App{store: store}
	now := time.Now().UTC().Truncate(time.Second)
	legacy, tavily, google := now.AddDate(0, 0, -16), now.AddDate(0, 0, -5), now.AddDate(0, 0, -2)
	if _, err := conn.Exec(ctx, `
		INSERT INTO execution(id, kind, state, started_at, updated_at, data) VALUES
		('call:legacy','tool_run','failed',NULL,$1,'{"tool":"generate_content","connection":"google-ai","errorCode":"provider-429"}'),
		('call:tavily','tool_run','failed',$2,$4,'{"tool":"web_search","connection":"tavily","errorCode":"provider-432"}'),
		('call:google','tool_run','failed',$3,$4,'{"tool":"generate_content","connection":"google-ai","errorCode":"provider-429"}')
	`, legacy, tavily, google, now); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO agent_gap(id, execution_id, source, at, class, family, rules)
		VALUES ('call:legacy','call:legacy','gateway',$1,'gateway-quota','gateway-quota','old')
	`, now); err != nil {
		t.Fatal(err)
	}
	for _, days := range []int{7, 30, 3} {
		if _, err := reclassifyGaps(ctx, app, nil, days); err != nil {
			t.Fatalf("rebuild %d days: %v", days, err)
		}
		for id, want := range map[string]time.Time{"call:legacy": legacy, "call:tavily": tavily, "call:google": google} {
			var got time.Time
			err := conn.QueryRow(ctx, "SELECT at FROM agent_gap WHERE id = $1", id).Scan(&got)
			if err != nil || !got.Equal(want) {
				t.Errorf("rebuild %d days: %s at %v, want %v (%v)", days, id, got, want, err)
			}
		}
		rows, err := db.New(conn).AgentGapRows(ctx, db.AgentGapRowsParams{
			Since: gapTime(now.AddDate(0, 0, -days)), Until: gapTime(now.Add(time.Minute)),
		})
		if err != nil {
			t.Fatal(err)
		}
		for id, want := range map[string]bool{"call:legacy": days == 30, "call:tavily": days >= 7, "call:google": true} {
			if got := slices.ContainsFunc(rows, func(row db.AgentGapRowsRow) bool { return row.ID == id }); got != want {
				t.Errorf("rebuild %d days: %s in current window = %v, want %v", days, id, got, want)
			}
		}
	}
}

// Requirement: a finding's class shows in the read with its finding even when the window holds no failure of it (the
// days after its fix). Risk: the class drops off the dashboard exactly when the fix worked, so nothing shows it went down.
func TestAgentGapsReplyFindingWithoutFailures(t *testing.T) {
	r := shippedGapRules(t)
	from := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	at := from.Add(30 * time.Hour)
	f := gapFinding{Class: "missing-command:jq", State: "watching", ChangeAt: &at, Pr: ptr(int64(1200)), OpenedAt: from}
	out := agentGapsReplyOf(r, nil, &contract.AgentGapsRequest{}, map[string]gapFinding{f.Class: f}, from, from.AddDate(0, 0, 6), 7)
	if len(out.Classes) != 1 || out.Classes[0].Count != 0 || len(out.Classes[0].Daily) != 7 || out.Classes[0].Finding == nil ||
		deref(out.Classes[0].Finding.ChangeAt) != "2026-11-21T06:00:00Z" || out.Classes[0].Setting.Kind != "host-tools" {
		t.Fatalf("%+v", out.Classes)
	}
	old := from.Add(-48 * time.Hour)
	f.State, f.VerdictAt = "fixed", &old
	if out := agentGapsReplyOf(r, nil, &contract.AgentGapsRequest{}, map[string]gapFinding{f.Class: f}, from, from.AddDate(0, 0, 6), 7); len(out.Classes) != 0 {
		t.Fatalf("a finding fixed before the window still shows: %+v", out.Classes)
	}
}

// Requirement: the change point is when the pull request's release ran on the coordinator and every host seen in the
// last hour, and the verdict compares failures per agent run. Risk: a fix judged before it reached the host that
// failed, or by raw counts on a quiet week, closes a gap that is still there.
func TestAgentGapWatch(t *testing.T) {
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	seen := float64(now.Add(-10 * time.Minute).UnixMilli())
	hosts := []db.DeliveryHostsSeenRow{{ID: "pds18-native", LastSeenMs: seen, Version: "1.2"}, {ID: "old", LastSeenMs: 1, Version: "1.0"}}
	pr := contract.DeliveryPr{Number: 7, Merged: "2026-11-25T00:00:00Z", Release: ptr("v1.2"), Installed: map[string]string{"coordinator": "2026-11-25T01:00:00Z"}}
	if _, _, waiting, ok := gapChangePoint([]contract.DeliveryPr{pr}, 7, hosts, now); ok || len(waiting) != 1 || waiting[0] != "pds18-native" {
		t.Fatalf("a change point before the release ran on pds18-native, or the wait does not name it: %v", waiting)
	}
	pr.Installed["pds18-native"] = "2026-11-25T03:00:00Z"
	at, rel, _, ok := gapChangePoint([]contract.DeliveryPr{pr}, 7, hosts, now)
	if !ok || at.Format(time.RFC3339) != "2026-11-25T03:00:00Z" || deref(rel) != "v1.2" {
		t.Fatalf("change point %v %v %v", at, deref(rel), ok)
	}
	for _, c := range []struct {
		bc, br, ac, ar int32
		fixed          bool
	}{{20, 100, 0, 100, true}, {20, 100, 5, 100, true}, {20, 100, 15, 100, false}, {20, 100, 5, 20, false}, {0, 100, 1, 100, false}} {
		if got := gapFixed(c.bc, c.br, c.ac, c.ar, 0.5); got != c.fixed {
			t.Errorf("%+v: fixed %v", c, got)
		}
	}
}

// Requirement: the findings lock is a lock scope the store knows. Risk: "unknown advisory lock scope" failed every
// findings pass and reclassification on prod (2026-11-26) while every unit test passed.
func TestAgentGapLock(t *testing.T) {
	if _, err := lockRank(gapLock); err != nil {
		t.Fatal(err)
	}
}

// Requirement: Investigate's conversation names its finding's question as its subject, a form sessionStart accepts.
// Risk: "Agent gap <class>" was refused on prod (2026-11-26) and no PM conversation started.
func TestAgentGapInvestigationSubject(t *testing.T) {
	if !subjectKey.MatchString("question:5c401dfa-afab-4407-8d56-b0e87e5b95c9") {
		t.Fatal("question:<id> is not a subject")
	}
}
