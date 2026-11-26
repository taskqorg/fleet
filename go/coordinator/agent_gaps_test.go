package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	out := agentGapsReplyOf(r, rows, &contract.AgentGapsRequest{}, from, now, 7)
	if out.Total != 4 || len(out.Classes) != 2 {
		t.Fatalf("total %d classes %d", out.Total, len(out.Classes))
	}
	jq := out.Classes[0]
	if jq.Class != "missing-command:jq" || jq.Count != 3 || jq.PreviousCount != 1 || jq.Executions != 2 || len(jq.Examples) != 2 {
		t.Fatalf("jq: %+v", jq)
	}
	if len(jq.Daily) != 7 || jq.Daily[0].Count != 1 || jq.Daily[6].Count != 2 || jq.Daily[3].Count != 0 {
		t.Fatalf("daily: %+v", jq.Daily)
	}
	if jq.Setting.Kind != "host-tools" || jq.Hosts[0].Key != host || jq.Hosts[0].Count != 3 || deref(jq.Examples[0].Path) != "/conversations/s1" {
		t.Fatalf("setting, hosts or path: %+v", jq)
	}
	if one := agentGapsReplyOf(r, rows, &contract.AgentGapsRequest{Class: ptr("sandbox-write")}, from, now, 7); len(one.Classes) != 1 || one.Total != 1 {
		t.Fatalf("class filter: %+v", one)
	}
	if fam := agentGapsReplyOf(r, rows, &contract.AgentGapsRequest{Class: ptr("missing-command")}, from, now, 7); len(fam.Classes) != 1 || fam.Total != 3 {
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
	if err != nil || len(runs) != 1 || runs[0].ID != "x1" {
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
		{ID: "call:1", ExecutionID: "x1", ProjectID: ptr("fleet"), Source: "gateway", At: gapTime(time.Now()), Class: "gateway-unknown-tool", Family: "gateway-unknown-tool", Tool: "github/commit_status", Line: "unknown-tool", Rules: "r2"},
	} {
		if err := q.AgentGapPut(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := q.AgentGapRows(ctx, db.AgentGapRowsParams{Since: since, Until: gapTime(time.Now().Add(time.Hour))})
	if err != nil || len(rows) != 2 || rows[0].ProfileID != "codex-worker" || deref(rows[0].HostID) != "pds18-native" || rows[1].Rules != "r2" {
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
}
