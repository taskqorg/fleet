package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// titleFixture: conversations as the list sees them: a first message and a finished first reply (s-done), the same with
// a title a person set (s-named), one whose reply is still running (s-running), one whose call fails (s-fail), a host
// command session (s-host) and a pipeline script session from before migration 72 (s-legacy) beside a pipeline agent
// session with a message (s-agent).
const titleFixture = `
INSERT INTO document(kind,id,data) VALUES
 ('session','s-done','{"id":"s-done","createdAt":1}'),
 ('session','s-named','{"id":"s-named","createdAt":1,"title":"Chosen by a person","titleBy":"owner@trial.test"}'),
 ('session','s-running','{"id":"s-running","createdAt":1}'),
 ('session','s-child','{"id":"s-child","createdAt":1}'),
 ('session','s-long','{"id":"s-long","createdAt":1}'),
 ('session','s-arch','{"id":"s-arch","createdAt":1,"archivedAt":1}'),
 ('session','s-fail','{"id":"s-fail","createdAt":1}'),
 ('session','s-host','{"id":"s-host","createdAt":1,"actor":"program-worker"}'),
 ('session','s-legacy','{"id":"s-legacy","createdAt":1,"pipelineRunId":"r1"}'),
 ('session','s-agent','{"id":"s-agent","createdAt":1,"pipelineRunId":"r1"}'),
 ('message','m-done','{"id":"m-done","sessionId":"s-done","text":"# STANDING RULES\nFix the login redirect loop"}'),
 ('message','m-named','{"id":"m-named","sessionId":"s-named","text":"Something else"}'),
 ('message','m-running','{"id":"m-running","sessionId":"s-running","text":"Still going"}'),
 ('message','m-child','{"id":"m-child","sessionId":"s-child","text":"Child ended first"}'),
 ('message','m-long','{"id":"m-long","sessionId":"s-long","text":"Long steered run"}'),
 ('message','m-arch','{"id":"m-arch","sessionId":"s-arch","text":"Archived one"}'),
 ('message','m-fail','{"id":"m-fail","sessionId":"s-fail","text":"FAIL this one"}'),
 ('message','m-agent','{"id":"m-agent","sessionId":"s-agent","text":"Review the step output"}');
INSERT INTO execution(id,kind,state,session_id,started_at,ended_at,data) VALUES
 ('e-done','agent_run','succeeded','s-done',now()-interval '2 min',now()-interval '1 min','{"id":"e-done"}'),
 ('e-named','agent_run','succeeded','s-named',now()-interval '2 min',now()-interval '1 min','{"id":"e-named"}'),
 ('e-running','agent_run','running','s-running',now()-interval '20 s',NULL,'{"id":"e-running"}'),
 ('e-long','agent_run','running','s-long',now()-interval '10 min',NULL,'{"id":"e-long"}'),
 ('e-child-main','agent_run','running','s-child',now()-interval '30 s',NULL,'{"id":"e-child-main"}'),
 ('e-child-sub','child_agent','succeeded','s-child',now()-interval '2 min',now()-interval '1 min','{"id":"e-child-sub"}'),
 ('e-arch','agent_run','succeeded','s-arch',now()-interval '2 min',now()-interval '1 min','{"id":"e-arch"}'),
 ('e-fail','agent_run','succeeded','s-fail',now()-interval '2 min',now()-interval '1 min','{"id":"e-fail"}'),
 ('e-host','host_job','succeeded','s-host',now()-interval '2 min',now()-interval '1 min','{"id":"e-host","actor":"program-worker"}'),
 ('e-legacy','host_job','succeeded','s-legacy',now()-interval '2 min',now()-interval '1 min','{"id":"e-legacy","actor":"program-worker"}'),
 ('e-agent','agent_run','succeeded','s-agent',now()-interval '2 min',now()-interval '1 min','{"id":"e-agent"}');
`

func insertTitleReplies(t *testing.T, e *testCoordEnv, ids string) {
	t.Helper()
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
	 INSERT INTO document(kind,id,project_id,data)
	 SELECT 'event', e.id||'-reply', e.project_id,
	  jsonb_build_object('executionId',e.id,'data','{"fleetNormalized":{"blocks":[{"kind":"text","markdown":"First agent reply"}]}}'::jsonb)
	 FROM execution e WHERE e.id IN (`+ids+`)`)
}

func migrationBody(t *testing.T, version string) string {
	t.Helper()
	m := regexp.MustCompile(`(?s)\n-- migration ` + version + `:.*?(\n-- migration \d+|$)`).FindString(schemaSQL)
	if m == "" {
		t.Fatalf("migration %s not found", version)
	}
	return strings.TrimSuffix(regexp.MustCompile(`\n-- migration \d+$`).ReplaceAllString(m, ""), "\n")
}

// Requirement: fleet-conversations.md, Titles: a conversation gets a short title from a small model after its first reply,
// stored on the session; a title a person or an agent set is never replaced, renaming is session_rename, the backfill is
// the same pass, a failed call is retried later, and a title is no activity. Host command sessions are no conversations
// (migration 72 marks the old pipeline ones). Risk: titles stay the first message (boilerplate) or "New conversation",
// a generated title overwrites a chosen one, the backfill reorders the list, a failing provider is called every pass,
// or empty host job sessions fill the Conversations list.
func TestSessionTitles(t *testing.T) {
	e := openTestCoordinator(t, false)
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", titleFixture)
	insertTitleReplies(t, e, "'e-done','e-named','e-running','e-long','e-child-sub','e-fail','e-agent','e-arch'")
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", migrationBody(t, "72"))
	owner := e.ownerHeaders()

	var mu sync.Mutex
	calls := map[string]int{}
	prompts := map[string]string{}
	old, ready := sessionTitleCall, titleReady
	t.Cleanup(func() { sessionTitleCall, titleReady = old, ready })
	titleReady = func() bool { return true }
	sessionTitleCall = func(_ context.Context, _ *App, _ string, prompt string) (string, string, error) {
		mu.Lock()
		defer mu.Unlock()
		key := strings.SplitN(strings.SplitN(prompt, "\n", 3)[1], " ", 2)[0]
		calls[key]++
		prompts[key] = prompt
		if strings.Contains(prompt, "FAIL") {
			return "", "c-fail", errors.New("provider 400 for this prompt")
		}
		return "\"Fix the login redirect loop.\"\nextra line", "c-" + key, nil
	}

	list := func() map[string]map[string]any {
		code, b := e.callJSON(t, "POST", "/api/ops/sessions_list", doc{"group": "none", "limit": 100}, owner)
		if code != 200 {
			t.Fatalf("sessions_list %d %v", code, b)
		}
		out := map[string]map[string]any{}
		for _, r := range b["sessions"].([]any) {
			m := r.(map[string]any)
			out[m["id"].(string)] = m
		}
		return out
	}
	before := list()
	for _, id := range []string{"s-host", "s-legacy"} {
		if _, ok := before[id]; ok {
			t.Fatalf("host command session %s listed as a conversation", id)
		}
	}
	if _, ok := before["s-agent"]; !ok {
		t.Fatal("a pipeline session with a message left the list")
	}
	if before["s-done"]["title"] != "# STANDING RULES\nFix the login redirect loop" {
		t.Fatalf("untitled session shows %v, want its first message", before["s-done"]["title"])
	}

	if err := sessionTitlePass(e.ctx, e.app); err != nil {
		t.Fatalf("a session's own failure is logged, not the pass's error: %v", err)
	}
	after := list()
	if got := after["s-done"]["title"]; got != "Fix the login redirect loop" {
		t.Fatalf("generated title %q", got)
	}
	if !strings.Contains(prompts["#"], "First reply:\nFirst agent reply") {
		t.Fatalf("prompt %q", prompts["#"])
	}
	if after["s-done"]["activityAt"] != before["s-done"]["activityAt"] {
		t.Fatalf("a generated title moved activity: %v -> %v", before["s-done"]["activityAt"], after["s-done"]["activityAt"])
	}
	if after["s-named"]["title"] != "Chosen by a person" || calls["Something"] != 0 {
		t.Fatalf("set title replaced or asked for: %v %v", after["s-named"]["title"], calls)
	}
	if calls["Still"] != 0 || calls["Review"] != 1 || calls["Child"] != 0 || calls["Archived"] != 0 || calls["Long"] != 1 {
		t.Fatalf("calls %v: none before the first reply ends (a child run does not count) or for an archived one; one for the pipeline agent conversation and for a first run going on for minutes", calls)
	}
	code, s := e.callJSON(t, "POST", "/api/ops/session_get", doc{"sessionId": "s-done"}, owner)
	if code != 200 || s["titleBy"] != "auto" || s["titleCall"] != "call:c-#" {
		t.Fatalf("session_get %d titleBy %v titleCall %v", code, s["titleBy"], s["titleCall"])
	}
	if got := psqlLines(t, e, `SELECT coalesce(data->>'title','-')||'|'||(data ? 'titleAttemptAt') FROM document WHERE id='s-fail'`); len(got) != 1 || got[0] != "-|true" {
		t.Fatalf("failed call: %v", got)
	}
	// A writer that read the session before the title and puts it back keeps the title (putDocument).
	if err := runTx(e.ctx, e.app, nil, true, NOLOCK, func(q *Request) error {
		return put(q, "session", doc{"id": "s-done", "createdAt": 1, "workingDirectory": "/w"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := psqlLines(t, e, `SELECT (data->>'title')||'|'||(data->>'titleBy')||'|'||(data->>'workingDirectory') FROM document WHERE id='s-done'`); len(got) != 1 || got[0] != "Fix the login redirect loop|auto|/w" {
		t.Fatalf("stale put lost the title: %v", got)
	}

	// A second pass asks for nothing: titled ones are done and the failed one waits for titleRetry.
	if err := sessionTitlePass(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	if calls["FAIL"] != 1 || calls["#"] != 1 {
		t.Fatalf("second pass calls %v", calls)
	}

	for _, bad := range []doc{{"sessionId": "s-done", "title": "  "}, {"sessionId": "s-done", "title": "-..."}, {"sessionId": "s-done", "title": "Run 5d370c31-c765-479f-b72c-87b55d36ed46"}, {"sessionId": "s-done", "title": strings.Repeat("x", 201)}} {
		if code, b := e.callJSON(t, "POST", "/api/ops/session_rename", bad, owner); code != 400 {
			t.Fatalf("session_rename %v = %d %v", bad, code, b)
		}
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/session_rename", doc{"sessionId": "nope", "title": "x"}, owner); code != 404 {
		t.Fatalf("unknown session %d", code)
	}
	// A person renames only a conversation they direct, even one they can read.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `INSERT INTO document(kind,id,data,project_id) VALUES
	 ('session','s-theirs','{"id":"s-theirs","projectId":"desiign","directedBy":"owner@trial.test"}','desiign'),
	 ('session','s-mine','{"id":"s-mine","projectId":"desiign","directedBy":"member@trial.test"}','desiign')`)
	member := map[string]string{"Authorization": owner["Authorization"], "X-Fleet-Person": "member@trial.test"}
	if code, b := e.callJSON(t, "POST", "/api/ops/session_rename", doc{"sessionId": "s-theirs", "title": "Mine now"}, member); code != 403 {
		t.Fatalf("rename of another's conversation %d %v", code, b)
	}
	if code, b := e.callJSON(t, "POST", "/api/ops/session_rename", doc{"sessionId": "s-mine", "title": "Mine"}, member); code != 200 || b["titleBy"] != "member@trial.test" {
		t.Fatalf("rename of one's own conversation %d %v", code, b)
	}
	code, r := e.callJSON(t, "POST", "/api/ops/session_rename", doc{"sessionId": "s-fail", "title": "  Login   redirect "}, owner)
	if code != 200 || r["title"] != "Login redirect" || r["titleBy"] != testOwnerEmail {
		t.Fatalf("session_rename %d %v", code, r)
	}
	if got := psqlLines(t, e, `SELECT (data->>'title')||'|'||(data ? 'titleAttemptAt')||'|'||(data->>'titleBy') FROM document WHERE id='s-fail'`); len(got) != 1 || got[0] != "Login redirect|false|"+testOwnerEmail {
		t.Fatalf("renamed session %v", got)
	}
	// Even with the retry due, a renamed session is never asked for again.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `UPDATE document SET data=data||'{"titleAttemptAt":1}' WHERE id='s-fail'`)
	if err := sessionTitlePass(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	if calls["FAIL"] != 1 || list()["s-fail"]["title"] != "Login redirect" {
		t.Fatalf("renamed session asked again: %v", calls)
	}

	// A failed first run with no reply does not win. A session moved after its first message does not send it to the new project.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
	 INSERT INTO document(kind,id,project_id,data) VALUES
	 ('session','s-resumed',NULL,'{"id":"s-resumed"}'),
	 ('message','m-resumed',NULL,'{"sessionId":"s-resumed","text":"Resumed execution"}'),
	 ('session','s-moved','prj-new','{"id":"s-moved","projectId":"prj-new"}'),
	 ('message','m-moved','prj-old','{"sessionId":"s-moved","text":"Old project text"}');
	 INSERT INTO execution(id,kind,state,session_id,project_id,started_at,ended_at,data) VALUES
	 ('e-resumed-failed','agent_run','failed','s-resumed',NULL,now()-interval '3 min',now(),'{}'),
	 ('e-resumed-success','agent_run','succeeded','s-resumed',NULL,now()-interval '1 min',now(),'{}'),
	 ('e-moved','agent_run','succeeded','s-moved','prj-new',now()-interval '2 min',now(),'{}')`)
	insertTitleReplies(t, e, "'e-resumed-success','e-moved'")
	if err := sessionTitlePass(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	if calls["Resumed"] != 1 || calls["Old"] != 0 {
		t.Fatalf("reply selection or project isolation: %v", calls)
	}

	// A failure the same for every session (the model unreachable) stops the batch after one call and uses no try.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `INSERT INTO document(kind,id,data) VALUES
	 ('session','s-o1','{"id":"s-o1"}'),('session','s-o2','{"id":"s-o2"}'),
	 ('message','m-o1','{"id":"m-o1","sessionId":"s-o1","text":"Outage one"}'),('message','m-o2','{"id":"m-o2","sessionId":"s-o2","text":"Outage two"}');
	 INSERT INTO execution(id,kind,state,session_id,started_at,ended_at,data) VALUES
	 ('e-o1','agent_run','succeeded','s-o1',now()-interval '2 min',now(),'{"id":"e-o1"}'),('e-o2','agent_run','succeeded','s-o2',now()-interval '2 min',now(),'{"id":"e-o2"}')`)
	insertTitleReplies(t, e, "'e-o1','e-o2'")
	outage := 0
	sessionTitleCall = func(context.Context, *App, string, string) (string, string, error) {
		outage++
		return "", "", fmt.Errorf("%w: google-ai/generate_content: gateway unavailable", errTitleShared)
	}
	if err := sessionTitlePass(e.ctx, e.app); err == nil || outage != 1 {
		t.Fatalf("outage: %d calls, pass error %v", outage, err)
	}
	// The session that met it waits titleRetryShared (not titleRetry); the other is not marked; neither uses a try.
	if got := psqlLines(t, e, `SELECT id||'|'||coalesce(data->>'titleAttempts','0')||'|'||coalesce(((data->>'titleAttemptAt')::bigint < (extract(epoch FROM now())*1000)::bigint-3000000)::text,'unmarked') FROM document WHERE id IN ('s-o1','s-o2') ORDER BY id`); strings.Join(got, ",") != "s-o1|0|unmarked,s-o2|0|true" {
		t.Fatalf("an outage: %v", got)
	}
	sessionTitleCall = func(context.Context, *App, string, string) (string, string, error) {
		return "", "uncertain-call", errTitleUnknown
	}
	if err := sessionTitlePass(e.ctx, e.app); !errors.Is(err, errTitleUnknown) {
		t.Fatalf("unknown call: %v", err)
	}
	if got := psqlLines(t, e, `SELECT data->>'titleUnknownCall' FROM document WHERE id='s-o1'`); len(got) != 1 || got[0] != "uncertain-call" {
		t.Fatalf("unknown call receipt: %v", got)
	}
	if err := sessionTitlePass(e.ctx, e.app); err != nil {
		t.Fatalf("unknown provider call was retried: %v", err)
	}

}

// Requirement: fleet-conversations.md, Titles: a model reply becomes one short line without quotes, markdown marks, a
// final period or a UUID (OR-38). Risk: a title carries a whole paragraph, a UUID or the model's quoting.
func TestCleanTitle(t *testing.T) {
	for in, want := range map[string]string{
		"\"Fix the login loop.\"\n\nBecause...":                       "Fix the login loop",
		"## **Review run 5d370c31-c765-479f-b72c-87b55d36ed46**":      "Review run",
		"  «Добавить ключ Anthropic»  ":                               "Добавить ключ Anthropic",
		"1. Fix the login loop":                                       "Fix the login loop",
		"Support for \"dark mode\"":                                   "Support for \"dark mode\"",
		"Fix session_123e4567-e89b-12d3-a456-426614174000_transcript": "Fix session__transcript",
		"Title: Fix login redirect":                                   "Fix login redirect",
		"Fix session (3f2a0c1d-1111-4222-8333-444455556666) crash":    "Fix session crash",
		"```text\nFix login\n```":                                     "Fix login",
		"```\nFix login\n```":                                         "Fix login",
		"Title:\nFix login":                                           "Fix login",
		"Название: Починить вход":                                     "Починить вход",
		"Fix bug (see 123e4567-e89b-12d3-a456-426614174000)":          "Fix bug",
		"Fix useEffect() cleanup":                                     "Fix useEffect() cleanup",
		"'a' vs 'b'":                                                  "'a' vs 'b'",
		"#635 Fix dispatch":                                           "#635 Fix dispatch",
		"-v flag in CLI":                                              "-v flag in CLI",
		"- Fix the flag":                                              "Fix the flag",
		"Update the users'":                                           "Update the users'",
		"'Quoted title'":                                              "Quoted title",
		"\"Fix login loop\".":                                         "Fix login loop",
		"\n\n":                                                        "",
		strings.Repeat("слово ", 30):                                  strings.TrimSpace(string([]rune(strings.Repeat("слово ", 30))[:79])) + "…",
	} {
		if got := cleanTitle(in); got != want {
			t.Errorf("cleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
