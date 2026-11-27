package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

const (
	ytTestProject     = "0-44"
	ytTestBot         = "1-2"
	ytTestReviewer    = "1-3"
	ytTestOther       = "1-4"
	ytTestFirst       = "2-10"
	ytTestMissed      = "2-11"
	ytTestRemoteOther = "2-12"
	ytTestSecret      = "invented-Fleet-test-webhook-token-0123456789"
)

func ytTestIssue(id, project, assignee string, at int64) youtrackIssue {
	raw := map[string]any{"id": id, "idReadable": "FLT-" + strings.TrimPrefix(id, "2-"), "project": map[string]string{"id": project},
		"updated": at, "customFields": []map[string]any{{"name": "Assignee", "value": map[string]string{"id": assignee}}}}
	issue, _ := jsonAs[youtrackIssue](jsonBytes(raw))
	return issue
}

func ytTestAssignment(issue, event, actor, assignee string, at int64) youtrackActivity {
	e := youtrackActivity{ID: event, Type: "CustomFieldActivityItem", At: at, Author: youtrackRef{ID: actor}}
	e.Target.ID, e.Field.Name = issue, "Assignee"
	e.Added = jsonBytes([]youtrackRef{{ID: assignee}})
	return e
}

func ytTestComment(issue, event, comment, actor string, at int64) youtrackActivity {
	e := youtrackActivity{ID: event, Type: "CommentActivityItem", At: at, Author: youtrackRef{ID: actor}}
	e.Target.ID, e.Target.Issue.ID = comment, issue
	e.Added = jsonBytes([]youtrackRef{{ID: comment}})
	return e
}

func TestYoutrackHandoffSelection(t *testing.T) {
	b := db.YoutrackBinding{ReviewerUserID: ytTestReviewer, BotUserID: ytTestBot}
	first := ytTestAssignment(ytTestFirst, "assign.1", ytTestReviewer, ytTestBot, 100)
	later := ytTestAssignment(ytTestFirst, "assign.2", ytTestOther, ytTestBot, 110)
	echo := ytTestComment(ytTestFirst, "comment.1", "4-1", ytTestBot, 150)
	human := ytTestComment(ytTestFirst, "comment.2", "4-2", ytTestReviewer, 200)
	hint := youtrackSignal{IssueID: ytTestFirst, Signal: "comment", Revision: 200, EventRef: "4-2"}
	link := db.YoutrackTask{TaskID: strPtr("task"), AssignmentAuthorized: true, DecisionMs: 100, AssignmentMs: 100}
	for _, tc := range []struct {
		name   string
		link   db.YoutrackTask
		events []youtrackActivity
		hint   *youtrackSignal
		want   string
	}{
		{"assignment enables a new task", db.YoutrackTask{}, []youtrackActivity{first}, nil, first.ID},
		{"comment alone creates no task", db.YoutrackTask{}, []youtrackActivity{human}, nil, ""},
		{"bot-authored echo", link, []youtrackActivity{echo}, nil, ""},
		{"reviewer comment on active handoff", link, []youtrackActivity{first, human}, &hint, human.ID},
		{"later assignment by an unapproved actor supersedes", link, []youtrackActivity{later, human, first}, nil, ""},
		{"older assignment cannot rearm superseded handoff", db.YoutrackTask{TaskID: link.TaskID, DecisionMs: 100, AssignmentMs: 110}, []youtrackActivity{first, human}, nil, ""},
		{"replayed assignment", link, []youtrackActivity{first}, nil, ""},
		{"newer assignment covers older comment", link, []youtrackActivity{human, ytTestAssignment(ytTestFirst, "assign.3", ytTestReviewer, ytTestBot, 210)}, &hint, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e, ambiguous := youtrackLatestAssignment(b, tc.events)
			if ambiguous {
				t.Fatal("unexpected ambiguity")
			}
			got := ""
			if choice := youtrackChoose(b, tc.link, e, tc.events, tc.hint); choice != nil {
				got = choice.ID
			}
			if got != tc.want {
				t.Fatalf("action %q, want %q", got, tc.want)
			}
		})
	}
	first.At = later.At
	if _, ambiguous := youtrackLatestAssignment(b, []youtrackActivity{first, later}); !ambiguous {
		t.Fatal("same-time conflicting actor observations authorized a wake")
	}
}

type ytTestProvider struct {
	mu            sync.Mutex
	issues        map[string]youtrackIssue
	events        map[string][]youtrackActivity
	outage        bool
	hideComment   bool
	commentTexts  []string
	postRefusals  []string
	activityReads int
}

func (f *ytTestProvider) open(_ context.Context, _ *App, _ string) (youtrackCall, func(), error) {
	return f.call, func() {}, nil
}

func (f *ytTestProvider) call(_ context.Context, tool string, args any) contract.CallLine {
	f.mu.Lock()
	defer f.mu.Unlock()
	ok := func(v any) contract.CallLine {
		return contract.CallLine{Outcome: contract.CallOutcomeOk, Output: jsonBytes(v)}
	}
	switch tool {
	case "handoff_identity":
		return ok(youtrackRef{ID: ytTestBot})
	case "handoff_reviewer":
		return ok(map[string]any{"id": ytTestReviewer, "login": "invented-reviewer"})
	case "handoff_project":
		m := args.(map[string]string)
		return ok(map[string]any{"id": m["project"], "shortName": "FLT", "archived": false})
	case "handoff_issue":
		m := args.(map[string]string)
		if issue, exists := f.issues[m["issue"]]; exists {
			return ok(issue)
		}
		for _, issue := range f.issues {
			if issue.Key == m["issue"] {
				return ok(issue)
			}
		}
		return contract.CallLine{Outcome: contract.CallOutcomeError, Status: 404, ErrorCode: strPtr("http-404")}
	case "handoff_issue_events", "handoff_project_events", "handoff_issue_events_next", "handoff_project_events_next":
		f.activityReads++
		if f.outage {
			return contract.CallLine{Outcome: contract.CallOutcomeError, Status: 503}
		}
		m := args.(map[string]string)
		start, _ := strconv.ParseInt(m["start"], 10, 64)
		end, _ := strconv.ParseInt(m["end"], 10, 64)
		page := []youtrackActivity{}
		for issue, list := range f.events {
			if m["issue"] != "" && issue != m["issue"] {
				continue
			}
			for _, event := range list {
				if event.At >= start && event.At <= end {
					page = append(page, event)
				}
			}
		}
		const firstPage = 2
		hasAfter := false
		if m["cursor"] == "next:page" {
			page = page[firstPage:]
		} else if len(page) > firstPage {
			page, hasAfter = page[:firstPage], true
		}
		return ok(map[string]any{"activities": page, "hasAfter": hasAfter, "afterCursor": "next:page"})
	case "handoff_post_comment":
		if len(f.postRefusals) > 0 {
			code := f.postRefusals[0]
			f.postRefusals = f.postRefusals[1:]
			return contract.CallLine{Outcome: contract.CallOutcomeError, ErrorCode: &code}
		}
		f.commentTexts = append(f.commentTexts, args.(map[string]string)["text"])
		return contract.CallLine{Outcome: contract.CallOutcomeUnknown, Status: 502}
	case "handoff_own_comments":
		page := []map[string]any{}
		if !f.hideComment {
			for i, text := range f.commentTexts {
				page = append(page, map[string]any{"author": youtrackRef{ID: ytTestBot},
					"target": map[string]any{"id": "4-" + strconv.Itoa(i+1), "text": text, "created": nowMs()}})
			}
		}
		return ok(map[string]any{"activities": page, "hasAfter": false})
	}
	return contract.CallLine{Outcome: contract.CallOutcomeError}
}

func TestYoutrackProjectHandoffPostgres(t *testing.T) {
	e := openTestCoordinator(t, false)
	e.appStore.app = e.app
	provider := &ytTestProvider{issues: map[string]youtrackIssue{}, events: map[string][]youtrackActivity{}}
	oldOpen := youtrackOpen
	youtrackOpen = provider.open
	t.Cleanup(func() { youtrackOpen = oldOpen })
	var created json.RawMessage
	for _, project := range []string{"yt-pilot", "yt-other", "yt-no-grant"} {
		e.opInto(t, "project_create", map[string]any{"requestId": "create-" + project, "id": project,
			"content": map[string]any{"settings": map[string]any{"id": project, "name": project, "contextFiles": []string{"rules.md"}}, "contextFiles": []any{map[string]any{"path": "rules.md", "text": "Fixture."}}}}, e.ownerHeaders(), &created)
	}
	err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, func(q *Request) error {
		return apiConnectionPut(q, apiConnection{ID: "connection:youtrack", Name: "youtrack", Provider: "youtrack", Intake: "all",
			Tools: toolNames("youtrack"), Access: connAccess{Projects: []string{"yt-pilot", "yt-other"}}})
	})
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now().Add(-time.Hour).Truncate(time.Second)
	at := func(sec int) int64 { return base.Add(time.Duration(sec) * time.Second).UnixMilli() }
	provider.issues[ytTestFirst] = ytTestIssue(ytTestFirst, ytTestProject, ytTestReviewer, at(1))
	provider.issues[ytTestMissed] = ytTestIssue(ytTestMissed, ytTestProject, ytTestReviewer, at(1))
	provider.issues[ytTestRemoteOther] = ytTestIssue(ytTestRemoteOther, "0-99", ytTestReviewer, at(1))
	exec := func(sql string, args ...any) {
		t.Helper()
		if err := e.ownerStore.Write(e.ctx, nil, nil, func(q *Request) error { _, err := q.tx.Exec(q.ctx(), sql, args...); return err }); err != nil {
			t.Fatal(err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := e.ownerStore.Read(e.ctx, nil, func(q *Request) error { return q.tx.QueryRow(q.ctx(), sql, args...).Scan(&n) }); err != nil {
			t.Fatal(err)
		}
		return n
	}
	connect := func(r contract.YoutrackProjectConnectRequest) (int, map[string]any) {
		t.Helper()
		return e.callJSON(t, "POST", "/api/ops/youtrack_project_connect", r, e.ownerHeaders())
	}
	bindingID := newID()
	preview := true
	r := contract.YoutrackProjectConnectRequest{BindingId: bindingID, ProjectId: "yt-pilot", YoutrackProjectId: ytTestProject,
		IssueIds: []string{ytTestMissed, ytTestFirst, ytTestMissed}, ReviewerUserId: strPtr(ytTestReviewer), ReviewerPerson: testOwnerEmail,
		WebhookTokenSha256: sha256hex([]byte(ytTestSecret)), ZeroPii: true, Preview: &preview}
	if status, body := connect(r); status != 200 || body["applied"] != false {
		t.Fatalf("preview %d: %v", status, body)
	}
	if got := count("SELECT count(*) FROM bot WHERE project_id='yt-pilot'"); got != 0 {
		t.Fatalf("preview created %d bot(s)", got)
	}
	var savedLater contract.YoutrackProjectConnectReply
	r.Preview = nil
	e.opInto(t, "youtrack_project_connect", r, e.ownerHeaders(), &savedLater)
	if !savedLater.Applied || savedLater.BotId != bindingID || len(savedLater.IssueIds) != 2 {
		t.Fatalf("unexpected preset result: %+v", savedLater)
	}
	for range 2 {
		if status, body := connect(r); status != 200 || body["applied"] != true {
			t.Fatalf("replay %d: %v", status, body)
		}
	}
	if n := count("SELECT count(*) FROM trigger WHERE bot_id=$1", bindingID); n != 1 {
		t.Fatalf("idempotent preset created %d triggers", n)
	}
	bad := r
	bad.BindingId, bad.ProjectId = newID(), "yt-other"
	if status, body := connect(bad); status != 409 {
		t.Fatalf("overlap %d: %v", status, body)
	}
	bad.ProjectId = "yt-no-grant"
	if status, body := connect(bad); status != 403 {
		t.Fatalf("ungranted Fleet project %d: %v", status, body)
	}
	bad.ProjectId, bad.IssueIds = "yt-other", []string{ytTestRemoteOther}
	if status, body := connect(bad); status != 404 {
		t.Fatalf("wrong remote project %d: %v", status, body)
	}
	pollID := newID()
	var poll contract.TriggerSaveReply
	e.opInto(t, "trigger_save", map[string]any{"id": pollID, "botId": bindingID, "baseRevision": 0, "settings": map[string]any{
		"name": "Legacy YouTrack polling", "enabled": true, "when": "every", "everyMs": 600000}}, e.ownerHeaders(), &poll)
	r.PollingTriggerId, r.PollingTriggerRevision = &pollID, &poll.Trigger.Revision
	for range 2 {
		if status, body := connect(r); status != 200 {
			t.Fatalf("poll cutover %d: %v", status, body)
		}
	}
	if got := count("SELECT count(*) FROM trigger WHERE id=$1 AND enabled", pollID); got != 0 {
		t.Fatal("explicit poll retirement did not disable the legacy trigger")
	}

	exec("UPDATE youtrack_binding SET started_ms=$2,cursor_ms=$2 WHERE id=$1", bindingID, at(0))
	b, err := e.app.youtrackBinding(e.ctx, bindingID)
	if err != nil {
		t.Fatal(err)
	}
	fireCount := func() int { return count("SELECT fires FROM trigger WHERE id=$1", bindingID) }
	signal := func(issue, kind, ref string, at int64) youtrackSignal {
		return youtrackSignal{IssueID: issue, Signal: kind, Revision: at, EventRef: ref}
	}
	post := func(s youtrackSignal, secret string, want int) {
		t.Helper()
		status, body := e.post(t, youtrackHook+bindingID, jsonBytes(s), map[string]string{"Authorization": "Bearer " + secret})
		if status != want {
			t.Fatalf("webhook %s %s: %d (want %d): %s", s.Signal, s.EventRef, status, want, body)
		}
	}
	clock := time.Now()
	kit := newWorkKit(e.app, func() time.Time { return clock }, 17)
	pass := func(name string) error {
		if next := time.Now().Add(time.Second); next.After(clock) {
			clock = next
		}
		return kit.passQueue(e.ctx, kit.queue(name))
	}
	assertPass := func(name string) {
		t.Helper()
		if err := pass(name); err != nil {
			t.Fatal(err)
		}
	}
	taskFor := func(id string) db.YoutrackTask {
		t.Helper()
		var row db.YoutrackTask
		if err := e.app.store.Read(e.ctx, nil, func(q *Request) (err error) {
			row, err = q.db().YoutrackTaskGet(q.ctx(), db.YoutrackTaskGetParams{Instance: b.Instance, IssueID: id})
			return err
		}); err != nil {
			t.Fatal(err)
		}
		return row
	}

	handoff := signal(ytTestFirst, "assignment", "assign.first", at(2))
	post(handoff, "incorrect", http.StatusUnauthorized)
	post(signal(ytTestRemoteOther, "assignment", "remote", at(2)), ytTestSecret, http.StatusNotFound)
	status, _ := e.post(t, youtrackHook+bindingID, []byte(`{"issueId":"2-10","signal":"assignment","revision":50,"text":"invented payload"}`),
		map[string]string{"Authorization": "Bearer " + ytTestSecret})
	if status != http.StatusBadRequest {
		t.Fatalf("raw text accepted: %d", status)
	}

	// The workflow can send before the provider commits its activity. The inbox must persist the retry.
	post(handoff, ytTestSecret, http.StatusAccepted)
	post(handoff, ytTestSecret, http.StatusAccepted)
	if got := count("SELECT count(*) FROM youtrack_inbox WHERE binding_id=$1", bindingID); got != 1 {
		t.Fatalf("duplicate inbox receipts: %d", got)
	}
	if err := pass(youtrackInbox); err == nil {
		t.Fatal("uncommitted provider event was treated as confirmed")
	}
	if fireCount() != 0 {
		t.Fatal("unverified assignment woke the bot")
	}
	provider.issues[ytTestFirst] = ytTestIssue(ytTestFirst, ytTestProject, ytTestBot, at(2))
	provider.events[ytTestFirst] = []youtrackActivity{ytTestAssignment(ytTestFirst, handoff.EventRef, ytTestReviewer, ytTestBot, at(2))}
	clock = clock.Add(5 * time.Minute)
	assertPass(youtrackInbox)
	link := taskFor(ytTestFirst)
	if fireCount() != 1 || link.TaskID == nil {
		t.Fatalf("first handoff did not correlate and wake: %+v", link)
	}
	taskID := *link.TaskID
	if n := count("SELECT count(*) FROM session WHERE bot_id=$1 AND subject=$2", b.BotID, "task:"+taskID); n != 1 {
		t.Fatalf("task subject had %d conversations", n)
	}
	if n := count("SELECT count(*) FROM task WHERE id=$1 AND state='paused' AND pause_reason='held'", taskID); n != 1 {
		t.Fatal("new task was not held")
	}
	post(handoff, ytTestSecret, http.StatusAccepted)
	assertPass(youtrackInbox)
	if fireCount() != 1 {
		t.Fatal("webhook replay woke the bot")
	}

	echo := signal(ytTestFirst, "comment", "4-10", at(3))
	provider.events[ytTestFirst] = append(provider.events[ytTestFirst], ytTestComment(ytTestFirst, "comment.echo", echo.EventRef, ytTestBot, at(3)))
	human := signal(ytTestFirst, "comment", "4-11", at(4))
	provider.events[ytTestFirst] = append(provider.events[ytTestFirst], ytTestComment(ytTestFirst, "comment.human", human.EventRef, ytTestReviewer, at(4)))
	provider.issues[ytTestFirst] = ytTestIssue(ytTestFirst, ytTestProject, ytTestBot, at(4))
	post(echo, ytTestSecret, http.StatusAccepted)
	assertPass(youtrackInbox)
	if fireCount() != 1 {
		t.Fatal("bot echo woke the bot")
	}
	post(human, ytTestSecret, http.StatusAccepted)
	assertPass(youtrackInbox)
	if fireCount() != 2 || *taskFor(ytTestFirst).TaskID != taskID {
		t.Fatal("reviewer comment did not wake same task")
	}

	// A paused issue defers its approved comment without blocking another issue or pinning the cursor.
	exec("UPDATE task SET pause_reason='operator' WHERE id=$1", taskID)
	paused := ytTestComment(ytTestFirst, "comment.paused", "4-15", ytTestReviewer, at(10))
	provider.events[ytTestFirst] = append(provider.events[ytTestFirst], paused)
	provider.issues[ytTestFirst] = ytTestIssue(ytTestFirst, ytTestProject, ytTestBot, at(10))

	// The tracker missed a webhook. A failed check keeps its cursor, then a retry wakes once.
	missed := ytTestAssignment(ytTestMissed, "assign.missed", ytTestReviewer, ytTestBot, at(11))
	provider.issues[ytTestMissed] = ytTestIssue(ytTestMissed, ytTestProject, ytTestBot, at(11))
	provider.events[ytTestMissed] = []youtrackActivity{missed}
	provider.outage = true
	if err := youtrackReconcile(e.ctx, e.app, base.Add(12*time.Second)); err == nil {
		t.Fatal("provider outage was treated as quiet")
	}
	hold, err := e.app.youtrackBinding(e.ctx, bindingID)
	if err != nil || hold.CursorMs != at(0) {
		t.Fatalf("failed pass advanced cursor: %+v, %v", hold, err)
	}
	provider.outage = false
	for _, sec := range []int{12, 13} {
		if err := youtrackReconcile(e.ctx, e.app, base.Add(time.Duration(sec)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if fireCount() != 3 || taskFor(ytTestMissed).TaskID == nil {
		t.Fatal("missed webhook was not reconciled exactly once")
	}
	post(signal(ytTestMissed, "assignment", missed.ID, missed.At), ytTestSecret, http.StatusAccepted)
	assertPass(youtrackInbox)
	if fireCount() != 3 {
		t.Fatal("late webhook duplicated reconciliation")
	}
	if n := count("SELECT count(*) FROM youtrack_task WHERE binding_id=$1", bindingID); n != 2 {
		t.Fatalf("correlated %d tasks, want 2", n)
	}

	reconciled, err := e.app.youtrackBinding(e.ctx, bindingID)
	if err != nil || reconciled.CursorMs != at(13) || count("SELECT count(*) FROM youtrack_inbox WHERE binding_id=$1 AND event_ref=$2 AND disposition IS NULL", bindingID, paused.ID) != 1 {
		t.Fatalf("paused issue blocked reconciliation instead of deferring: %+v, %v", reconciled, err)
	}
	const carrySecret = "invented-Fleet-carry-new-webhook-token-0123456789"
	carry := r
	carryRev := int64(1)
	carry.BaseRevision = &carryRev
	carry.WebhookTokenSha256 = sha256hex([]byte(carrySecret))
	const addedIssue = "2-13"
	provider.issues[addedIssue] = ytTestIssue(addedIssue, ytTestProject, ytTestReviewer, at(13))
	carry.IssueIds = []string{ytTestFirst, ytTestMissed, addedIssue}
	if status, body := connect(carry); status != 200 || body["revision"] != float64(2) {
		t.Fatalf("carry config %d: %v", status, body)
	}
	// A kit step that read the old revision before rollover cannot finalize the carried entry afterwards.
	if err := e.app.store.Write(e.ctx, nil, nil, func(q *Request) error {
		var id string
		if err := q.tx.QueryRow(q.ctx(), "SELECT id FROM youtrack_inbox WHERE binding_id=$1 AND event_ref=$2", bindingID, paused.ID).Scan(&id); err != nil {
			return err
		}
		return q.db().YoutrackInboxFinish(q.ctx(), db.YoutrackInboxFinishParams{ID: id, BindingRevision: 1, Disposition: strPtr("superseded")})
	}); err != nil {
		t.Fatal(err)
	}
	if count("SELECT count(*) FROM youtrack_inbox WHERE event_ref=$1 AND binding_revision=2 AND disposition IS NULL", paused.ID) != 1 {
		t.Fatal("credential and selector changes lost the pending approved wake")
	}
	exec("UPDATE task SET pause_reason='held' WHERE id=$1", taskID)
	clock = clock.Add(5 * time.Minute)
	assertPass(youtrackInbox)
	if fireCount() != 4 {
		t.Fatal("durable deferred wake did not resume")
	}
	provider.postRefusals = []string{"unreachable", "http-403"}
	if err := pass(youtrackComments); err == nil {
		t.Fatal("expected known unsent failures")
	}
	if len(provider.commentTexts) != 0 || count("SELECT count(*) FROM youtrack_comment_work WHERE binding_id=$1 AND sent_at IS NULL", bindingID) != 2 {
		t.Fatal("a known refusal became unknown instead of clearing the send intent")
	}
	clock = clock.Add(5 * time.Minute)

	// An accepted POST whose response was lost is reconciled from the bot-authored marker, without a second POST.
	provider.hideComment = true
	if err := pass(youtrackComments); !errors.Is(err, errYoutrackCommentUnknown) {
		t.Fatalf("unknown write: %v", err)
	}
	clock = clock.Add(5 * time.Minute)
	if err := pass(youtrackComments); !errors.Is(err, errYoutrackCommentUnknown) {
		t.Fatalf("unknown write without marker: %v", err)
	}
	if len(provider.commentTexts) != 2 {
		t.Fatalf("replayed uncertain comments: %d writes", len(provider.commentTexts))
	}
	provider.hideComment = false
	clock = clock.Add(5 * time.Minute)
	// This fake provider returns both links on each read. A marker can confirm only the matching task effect.
	assertPass(youtrackComments)
	assertPass(youtrackComments)
	if n := count("SELECT count(*) FROM youtrack_comment_work WHERE binding_id=$1 AND confirmed_at IS NOT NULL", bindingID); n != 2 || len(provider.commentTexts) != 2 {
		t.Fatalf("confirmed %d comments with %d writes", n, len(provider.commentTexts))
	}

	// An open milestone needs both a reply newer than its confirmed notice and a verified return assignment.
	milestone := newID()
	exec("INSERT INTO question(id, kind, project_id, task_id, text, state, addressed_to) VALUES($1,'operator_decision',$2,$3,'Fixture question','open',$4)",
		milestone, b.ProjectID, taskID, b.ReviewerPerson)
	for range 2 {
		var reply contract.YoutrackCommentSyncReply
		e.opInto(t, "youtrack_comment_sync", contract.YoutrackCommentSyncRequest{TaskId: taskID, MilestoneId: milestone}, e.ownerHeaders(), &reply)
		if reply.State != "queued" {
			t.Fatalf("question comment: %+v", reply)
		}
	}
	if n := count("SELECT count(*) FROM youtrack_comment_work WHERE milestone=$1", milestone); n != 1 {
		t.Fatalf("question queued %d comments", n)
	}
	// Model the confirmed source timestamp of the fixed notice, distinct from the coordinator's send clock.
	exec("UPDATE youtrack_comment_work SET sent_at=clock_timestamp(), confirmed_at=clock_timestamp(), provider_created_ms=$2, comment_id='4-20' WHERE milestone=$1", milestone, at(15))
	for _, step := range []struct {
		at        int
		suffix    string
		assignee  string
		isComment bool
		want      int
	}{
		{14, "stale", ytTestBot, true, 4},
		{16, "reviewer", ytTestReviewer, false, 4},
		{17, "no-reply", ytTestBot, false, 4},
		{18, "back", ytTestReviewer, false, 4},
		{19, "reply", ytTestReviewer, true, 4},
		{20, "return", ytTestBot, false, 5},
		{21, "back-again", ytTestReviewer, false, 5},
		{22, "replay-reply", ytTestBot, false, 5},
	} {
		provider.issues[ytTestFirst] = ytTestIssue(ytTestFirst, ytTestProject, step.assignee, at(step.at))
		ev := ytTestAssignment(ytTestFirst, "assign."+step.suffix, ytTestReviewer, step.assignee, at(step.at))
		if step.isComment {
			ev = ytTestComment(ytTestFirst, "comment."+step.suffix, "4-"+strconv.Itoa(step.at+30), ytTestReviewer, at(step.at))
		}
		provider.events[ytTestFirst] = append(provider.events[ytTestFirst], ev)
		post(signal(ytTestFirst, ev.kind(), ev.ID, at(step.at)), carrySecret, http.StatusAccepted)
		assertPass(youtrackInbox)
		if got := fireCount(); got != step.want {
			t.Fatalf("milestone %s fired %d, want %d", step.suffix, got, step.want)
		}
	}

	// A mistaken or malicious request cannot read activity for another YouTrack project.
	moved := signal(ytTestFirst, "assignment", "move.case", at(25))
	provider.issues[ytTestFirst] = ytTestIssue(ytTestFirst, "0-99", ytTestBot, at(25))
	reads := provider.activityReads
	post(moved, carrySecret, http.StatusAccepted)
	assertPass(youtrackInbox)
	if fireCount() != 5 || provider.activityReads != reads {
		t.Fatal("foreign issue triggered activity reads or a wake")
	}
	if got := count("SELECT count(*) FROM youtrack_inbox WHERE binding_id=$1 AND disposition='outside_scope'", bindingID); got != 1 {
		t.Fatal("foreign event was not retained as denied")
	}
	// Reuse a managed legacy task by code while normalizing the tracker issue key and reviewer login.
	var legacy contract.TaskDetail
	e.opInto(t, "task_create", contract.TaskCreateRequest{ProjectId: &b.ProjectID, BotId: &b.BotID, Title: "Legacy tracker task"},
		e.ownerHeaders(), &legacy)
	adopt := r
	adopt.BindingId, adopt.BotId, adopt.YoutrackProjectId = newID(), &b.BotID, "0-99"
	adopt.IssueIds, adopt.IssueKeys, adopt.ReviewerUserId, adopt.ReviewerLogin = nil, []string{"FLT-12"}, nil, strPtr("invented-reviewer")
	adopt.PollingTriggerId, adopt.PollingTriggerRevision = nil, nil
	adopt.ExistingTasks = []contract.YoutrackExistingTask{{Issue: "FLT-12", Task: deref(legacy.Code)}}
	for range 2 {
		if status, body := connect(adopt); status != 200 || body["reviewerUserId"] != ytTestReviewer {
			t.Fatalf("legacy adoption %d: %v", status, body)
		}
	}
	if adopted := taskFor(ytTestRemoteOther); deref(adopted.TaskID) != legacy.Id || adopted.BindingID != adopt.BindingId {
		t.Fatalf("legacy task was replaced: %+v", adopted)
	}

	// Credential updates fence previously admitted credentials and keep the single issue correlation.
	const rotated = "invented-Fleet-rotated-webhook-token-01234567"
	provider.issues[ytTestFirst] = ytTestIssue(ytTestFirst, ytTestProject, ytTestBot, at(26))
	rot := r
	rot.PollingTriggerId, rot.PollingTriggerRevision = nil, nil
	rot.WebhookTokenSha256 = sha256hex([]byte(rotated))
	if status, _ := connect(rot); status != 409 {
		t.Fatalf("rotation without revision: %d", status)
	}
	rev := int64(2)
	rot.BaseRevision = &rev
	if status, body := connect(rot); status != 200 || body["revision"] != float64(3) {
		t.Fatalf("rotation %d: %v", status, body)
	}
	post(moved, carrySecret, http.StatusUnauthorized)
	post(moved, rotated, http.StatusAccepted)
	pendingNotice := newID()
	missedTaskID := taskFor(ytTestMissed).TaskID
	exec("INSERT INTO question(id,kind,project_id,task_id,text,state,addressed_to) VALUES($1,'operator_decision',$2,$3,'Fixture question','open',$4)", pendingNotice, b.ProjectID, missedTaskID, b.ReviewerPerson)
	var queued contract.YoutrackCommentSyncReply
	e.opInto(t, "youtrack_comment_sync", contract.YoutrackCommentSyncRequest{TaskId: *missedTaskID, MilestoneId: pendingNotice}, e.ownerHeaders(), &queued)
	var disconnected contract.YoutrackProjectDisconnectReply
	for range 2 {
		e.opInto(t, "youtrack_project_disconnect", contract.YoutrackProjectDisconnectRequest{BindingId: bindingID, BaseRevision: 3}, e.ownerHeaders(), &disconnected)
		if disconnected.Revision != 4 {
			t.Fatalf("disconnect revision: %+v", disconnected)
		}
	}
	post(moved, rotated, http.StatusUnauthorized)
	assertPass(youtrackComments)
	if n := count("SELECT count(*) FROM youtrack_comment_work WHERE milestone=$1 AND sent_at IS NULL AND skip_reason IS NULL", pendingNotice); n != 1 {
		t.Fatal("disconnect consumed a queued notice")
	}
	rot.BaseRevision = &disconnected.Revision
	rot.IssueIds = []string{ytTestMissed} // the first issue moved, leaving a narrower approved selector
	if status, body := connect(rot); status != 200 || body["revision"] != float64(5) {
		t.Fatalf("reconnect %d: %v", status, body)
	}
	if err := pass(youtrackComments); !errors.Is(err, errYoutrackCommentUnknown) {
		t.Fatalf("reconnected notice did not post: %v", err)
	}

	if id := deref(taskFor(ytTestFirst).TaskID); id != taskID {
		t.Fatal("credential lifecycle lost the correlated task")
	}

	if err := e.app.store.Read(e.ctx, scopedRequest(e.ctx, e.app, &Person{Email: "outsider@trial.test"}), func(q *Request) error {
		_, err := q.db().YoutrackBindingGet(q.ctx(), bindingID)
		return err
	}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("RLS exposed another project's binding: %v", err)
	}
}
