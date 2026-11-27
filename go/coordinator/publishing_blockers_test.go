package main

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// T-FLT-143: the normal worker route reports a single bot-owned operational
// Problem. Only a real structured decision reaches a person. An owner answer
// reaches the duty bot once, even if two surfaces submit it at the same time.
func TestPublishingBlockerAuthorityLifecycle(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	const (
		project     = "ci-authority-fixture"
		botProject  = "desiign"
		memberEmail = "member@trial.test"
		starEmail   = "agents@trial.test"
		bot         = "ci-duty-bot"
		task        = "ci-worker-task"
		worker      = "ci-worker-session"
		duty        = "ci-duty-session"
		run         = "ci-worker-execution"
		botRun      = "ci-duty-execution"
	)
	sha := strings.Repeat("a", 40)
	settings := doc{"id": project, "name": "CI escalations", "contextFiles": []string{"rules.md"},
		"repos": []doc{{"id": "main", "remote": "https://github.com/example/repo", "forge": "github"}}}
	var roster contract.PeopleSetReply
	e.opInto(t, "people_set", doc{"people": []doc{
		{"email": testOwnerEmail, "name": "Owner", "roles": []string{"operator", "architect"}, "owner": true},
		{"email": memberEmail, "name": "Member", "roles": []string{"operator"}, "grants": []doc{{"kind": "project", "id": project}}},
		{"email": starEmail, "name": "Full access", "roles": []string{"operator"}, "grants": []doc{{"kind": "*", "id": "*"}}},
		{"email": "architect@trial.test", "name": "All projects", "roles": []string{"architect"}, "grants": []doc{{"kind": "project", "id": "*"}}},
	}}, h, &roster)
	var created projectWriteResult
	e.opInto(t, "project_create", doc{"requestId": "ci-escalation", "id": project, "content": doc{
		"settings": settings, "contextFiles": []doc{{"path": "rules.md", "text": "Fixture for CI escalation."}},
	}}, h, &created)

	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	write := func(fn func(*Request) error) {
		t.Helper()
		if err := e.app.store.Write(e.ctx, nil, []string{GLOBAL}, fn); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO bot(id,project_id,director,revision) VALUES ($1,$2,$3,1)`, bot, botProject, testOwnerEmail)
	exec(`INSERT INTO bot_revision(bot_id,revision,name,purpose,profile_id,instructions,soul,schedule_time,schedule_zone,wake_on_needs_you,duty)
		VALUES ($1,1,'HAL0','Operations','coding-worker','','','','UTC',true,'{"problems":{"system":true}}')`, bot)
	exec(`INSERT INTO task(id,project_id,title,state) VALUES ($1,$2,'Escalation fixture','in_progress')`, task, project)
	exec(`INSERT INTO bot_task (bot_id,task_id,project_id,link_kind) VALUES ($1,$2,$3,'managed')`, bot, task, project)
	write(func(q *Request) error {
		profile := jsonBytes(agentProfile{ID: "coding-worker", Harness: "codex"})
		if err := sessionPut(q, &agentSession{ID: worker, ProjectID: project, TaskID: task, DirectedBy: memberEmail, AgentConfig: profile}); err != nil {
			return err
		}
		if err := sessionPut(q, &agentSession{ID: duty, ProjectID: botProject, BotID: bot, DirectedBy: testOwnerEmail, AgentConfig: profile}); err != nil {
			return err
		}
		for _, execution := range []agentExecution{
			{ID: run, SessionID: worker, ProjectID: project, TaskID: task, DirectedBy: memberEmail, State: "running", AgentConfig: profile},
			{ID: botRun, SessionID: duty, ProjectID: botProject, DirectedBy: testOwnerEmail, State: "running", AgentConfig: profile},
		} {
			if err := executionWrite(q, &execution); err != nil {
				return err
			}
		}
		return nil
	})
	exec(`UPDATE execution SET bot_id=$1 WHERE id=$2`, bot, botRun)
	exec(`INSERT INTO telegram_person(email,telegram_user_id) VALUES ($1,4100), ('member@trial.test',4101)`, testOwnerEmail)
	exec(`INSERT INTO telegram_room(id,chat_id,thread_id,kind) VALUES ('4100:0',4100,0,'private'), ('4101:0',4101,0,'private'), ('-4102:0',-4102,0,'forum')`)
	exec(`INSERT INTO telegram_work(id,room_id,session_id,created_by,title) VALUES ('ci-authority-group','-4102:0',$1,$2,'CI fixture')`, duty, testOwnerEmail)

	asAgent := func(id, operation string, body any) (any, error) {
		t.Helper()
		email := testOwnerEmail
		if id == run {
			email = memberEmail
		}
		p := e.app.peopleSnapshot().person(email)
		q := scopedRequest(e.ctx, e.app, p)
		q.agent = "execution:" + id
		return e.app.runOp(q, operation, jsonBytes(body), nil)
	}
	report := func(head string, attempt int64, op contract.PublishingBlockerOperation) contract.ProblemRow {
		t.Helper()
		b, err := asAgent(run, "publishing_report_blocker", contract.PublishingReportBlockerRequest{
			ProjectId: ptr(project), SourceExecutionId: ptr(run), Operation: op, Reason: "access_unavailable",
			Number: ptr(int64(7)), ExpectedHeadSha: &head, RunId: ptr(int64(12)), JobId: ptr(int64(21)), Attempt: &attempt,
		})
		if err != nil {
			t.Fatal(err)
		}
		return b.(contract.PublishingReportBlockerReply).Problem
	}
	p := report(sha, 1, "ci_logs")
	if got := report(sha, 1, "ci_logs"); got.Id != p.Id || got.Count != 2 || deref(got.OwnerBotId) != bot {
		t.Fatalf("duplicate blocker did not stay with HAL0: %+v", got)
	}
	var evidence publishingBlockerData
	if err := json.Unmarshal(p.Data, &evidence); err != nil ||
		evidence.Delivery.Task != task || evidence.Delivery.Execution != run || evidence.Delivery.Branch != "fleet/"+worker ||
		evidence.Delivery.Repo != "example/repo" || evidence.Delivery.Job != 21 || evidence.Delivery.Attempt != 1 {
		t.Fatalf("problem missing actionable scope: %s, %v", p.Data, err)
	}
	var home contract.HomeViewReply
	e.opInto(t, "home_view", doc{}, h, &home)
	if len(home.Questions) != 0 {
		t.Fatalf("operational reports reached people: %+v", home.Questions)
	}

	decision := &contract.DeliveryAuthorityDecision{
		Authority: "project_grant", Check: "Go checks", Diagnosis: "The provider denied the log read.",
		Attempts: "Checked status and available diagnostics.", Uncertainty: "The failing step is unknown.",
		Waiting: "The worker needs evidence to diagnose the failure.",
		Choices: []contract.DeliveryAuthorityChoice{
			{Label: "Approve read scope", Consequence: "The owner can grant the project access to job metadata."},
			{Label: "Keep current scope", Consequence: "The worker continues with the available evidence."},
		},
		Recommendation: "Keep current scope",
	}
	ask := contract.ProblemEscalateRequest{Id: p.Code, Kind: "authority", Text: "May the project read Go failure evidence?", DeliveryAuthority: decision}
	expectCode := func(err error, code int) {
		t.Helper()
		var h *HTTPError
		if !errors.As(err, &h) || h.Code != code {
			t.Fatalf("got %v, want HTTP %d", err, code)
		}
	}
	_, err := asAgent(run, "problem_escalate", ask)
	expectCode(err, 403)
	_, err = asAgent(botRun, "problem_escalate", contract.ProblemEscalateRequest{Id: p.Code, Kind: "authority", Text: "Copy the log or click rerun"})
	expectCode(err, 400)
	escalate := func(r contract.ProblemEscalateRequest) string {
		t.Helper()
		x, err := asAgent(botRun, "problem_escalate", r)
		if err != nil {
			t.Fatal(err)
		}
		return x.(contract.ProblemEscalateReply).QuestionId
	}
	first := escalate(ask)
	_, err = asAgent(botRun, "problem_escalate", ask)
	expectCode(err, 409)
	other := report(sha, 1, "status")
	otherAsk := ask
	otherAsk.Id = other.Code
	_, err = asAgent(botRun, "problem_escalate", otherAsk)
	expectCode(err, 409)

	var detail contract.TaskDetail
	var view contract.SessionViewReply
	e.opInto(t, "task_get", doc{"taskId": task}, h, &detail)
	e.opInto(t, "session_view", doc{"sessionId": duty}, h, &view)
	e.opInto(t, "home_view", doc{}, h, &home)
	choices := []string{"Keep current scope", "Approve read scope"}
	if len(detail.Questions) != 1 || len(view.Questions) != 1 || len(home.Questions) != 1 {
		t.Fatalf("decision unavailable in a web surface: task %d, conversation %d, home %d", len(detail.Questions), len(view.Questions), len(home.Questions))
	}
	card := detail.Questions[0]
	if card.Id != first || view.Questions[0].Id != first || home.Questions[0].Id != first ||
		card.Text != view.Questions[0].Text || card.Text != home.Questions[0].Text ||
		!slices.Equal(view.Questions[0].Options, choices) || !slices.Equal(tgChoices(card.Options), choices) ||
		!strings.Contains(card.Text, p.Code) || !strings.Contains(card.Text, "HAL0") ||
		!strings.Contains(card.Text, "Recommended: Keep current scope") || strings.Contains(card.Text, sha) {
		t.Fatalf("inconsistent decision presentation: %+v", card)
	}
	write(func(q *Request) error {
		ordinary, err := q.db().TelegramQuestionsToPush(q.ctx())
		if err != nil {
			return err
		}
		owner, err := q.db().TelegramDeliveryAuthorityToPush(q.ctx(), testOwnerEmail)
		if err != nil {
			return err
		}
		private, err := q.db().TelegramNeedsAll(q.ctx(), false)
		if err != nil {
			return err
		}
		if len(ordinary) != 0 || len(owner) != 1 || owner[0].ChatID != 4100 || owner[0].ID != first ||
			owner[0].Text != card.Text || len(private) != 0 {
			t.Fatalf("decision went to an unauthorized Telegram surface: %+v / %+v / %+v", ordinary, owner, private)
		}
		return telegramQuestionMessage(q, "q:"+first, first, string(card.Kind), card.Text, "en", 4100, 0, "", duty, nil)
	})
	exec(`UPDATE telegram_outbox SET state='sent', telegram_message_id=1 WHERE id=$1`, "q:"+first)
	checkPrivateHistory := func(id string) {
		t.Helper()
		for _, email := range []string{memberEmail, starEmail, "architect@trial.test"} {
			request := scopedRequest(e.ctx, e.app, e.app.peopleSnapshot().person(email))
			data, err := e.app.runOp(request, "history_view", jsonBytes(contract.HistoryViewRequest{TaskId: ptr(task)}), nil)
			if err != nil {
				t.Fatal(err)
			}
			if history := string(jsonBytes(data)); strings.Contains(history, "question:"+id) ||
				strings.Contains(history, ask.Text) || strings.Contains(history, choices[0]) {
				t.Fatalf("owner decision reached project history for %s", email)
			}
		}
	}
	checkPrivateHistory(first)
	for _, email := range []string{memberEmail, starEmail, "architect@trial.test"} {
		p := e.app.peopleSnapshot().person(email)
		request := scopedRequest(e.ctx, e.app, p)
		if err := e.app.store.Read(e.ctx, request, func(q *Request) error {
			if rows, err := questionRows(q, db.TaskQuestionsParams{ID: &first}, nil, nil); err != nil || len(rows) != 0 {
				t.Fatalf("owner card visible to %s: %+v / %v", email, rows, err)
			}
			reason, err := waitReason(q, task)
			if err == nil && reason != "question" {
				t.Fatalf("owner card stopped blocking the task for %s: %s", email, reason)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	scopedMember := scopedRequest(e.ctx, e.app, e.app.peopleSnapshot().person(memberEmail))
	_, err = e.app.runOp(scopedMember, "questions_answer", jsonBytes(contract.QuestionsAnswerRequest{QuestionId: first, Answer: choices[0]}), nil)
	expectCode(err, 404)
	_, err = asAgent(botRun, "questions_answer", contract.QuestionsAnswerRequest{QuestionId: first, Answer: choices[0]})
	expectCode(err, 403)

	// A historical success from a delayed outbox cannot clear a newer report.
	for _, tc := range []struct {
		at   time.Time
		want string
	}{{time.Now().Add(-time.Hour), "open"}, {time.Now(), "closed"}} {
		write(func(q *Request) error {
			b := contract.PublishingHostRecord{Provider: "github", Host: "github.com", Repo: "example/repo",
				Branch: "fleet/" + worker, Number: 7, HeadSha: sha, State: "open", ProjectId: ptr(project), Action: ptr("status"),
				Result: jsonBytes(doc{"observedAt": tc.at})}
			if err := observePublishingBlockers(q, &b); err != nil {
				return err
			}
			got, err := q.db().ProblemGet(q.ctx(), other.Id)
			if err == nil && got.State != tc.want {
				t.Fatalf("success receipt from %v changed problem to %q, want %q", tc.at, got.State, tc.want)
			}
			return err
		})
	}

	observe := func(head string, attempt int64) {
		t.Helper()
		facts := githubDeliveryFacts(ghRepo{
			open: []ghPull{{Number: 7, Head: ghPullHead{Ref: "fleet/" + worker, SHA: head}}},
			finished: []ghRun{{ID: 12, Event: "pull_request", Branch: "fleet/" + worker,
				HeadSHA: head, Attempt: attempt, Status: "completed", Conclusion: "failure"}},
		}, time.Now())
		s := deliveryState{repos: map[string]*deliveryRepo{"example/repo": {projects: []string{project}, ci: facts}}}
		write(func(q *Request) error {
			if err := observeDeliveryProblems(q, s); err != nil {
				return err
			}
			return deliverySave(q, s)
		})
	}
	observe(sha, 2)
	write(func(q *Request) error {
		qu, err := q.db().TaskQuestion(q.ctx(), first)
		if err != nil {
			return err
		}
		edits, err := q.db().TelegramQuestionsEnded(q.ctx())
		if err != nil {
			return err
		}
		if qu.State != db.QuestionStateWithdrawn || len(edits) != 1 ||
			edits[0].ID != first || edits[0].State != "withdrawn" || edits[0].EditID != "qend:"+first {
			t.Fatalf("stale card was not withdrawn in Telegram and web: %s / %+v", qu.State, edits)
		}
		return nil
	})
	e.opInto(t, "home_view", doc{}, h, &home)
	if len(home.Questions) != 0 {
		t.Fatalf("withdrawn decision stayed in Needs you: %+v", home.Questions)
	}

	answer := func(id string, value string) int {
		status, _ := e.callJSON(t, "POST", "/api/ops/questions_answer", contract.QuestionsAnswerRequest{QuestionId: id, Answer: value}, h)
		return status
	}
	if code := answer(first, choices[0]); code != 409 {
		t.Fatalf("stale run accepted an answer: %d", code)
	}
	p = report(sha, 2, "ci_logs")
	ask.Id = p.Code
	second := escalate(ask)
	nextSHA := strings.Repeat("b", 40)
	observe(nextSHA, 1)
	if code := answer(second, choices[0]); code != 409 {
		t.Fatalf("stale head accepted an answer: %d", code)
	}
	p = report(nextSHA, 1, "ci_logs")
	ask.Id = p.Code
	third := escalate(ask)
	exec(`UPDATE bot SET paused=true WHERE id=$1`, bot)
	if code := answer(third, choices[0]); code != 409 {
		t.Fatalf("a paused bot silently accepted a decision: %d", code)
	}
	exec(`UPDATE bot SET paused=false WHERE id=$1`, bot)

	var wg sync.WaitGroup
	results := [2]int{}
	for i := range results {
		wg.Go(func() { results[i] = answer(third, choices[0]) })
	}
	wg.Wait()
	if results != [2]int{200, 200} || answer(third, choices[1]) != 409 {
		t.Fatalf("answer retry semantics failed: %+v", results)
	}
	checkPrivateHistory(third)
	write(func(q *Request) error {
		queued, err := queuedMessages(q, db.QueuedMessagesParams{SessionID: ptr(duty)})
		if err != nil {
			return err
		}
		if len(queued) != 1 || queued[0].RequestID != "reply:"+third || !strings.Contains(queued[0].Text, nextSHA) {
			t.Fatalf("expected one scoped continuation, got %+v", queued)
		}
		p, err := q.db().ProblemGet(q.ctx(), p.Id)
		if err != nil {
			return err
		}
		if p.Phase != "working" || p.QuestionID != nil || p.WokenAt == nil || p.PhaseAt.After(*p.WokenAt) {
			t.Fatalf("problem change can wake the bot again: %+v", p)
		}
		return nil
	})
}

// T-FLT-143: a replaced head pipeline withdraws its decision. A different
// pipeline ref or an incomplete listing cannot masquerade as a replacement.
func TestGitLabDeliveryAuthorityEvidence(t *testing.T) {
	d := deliveryBlocker{Provider: "gitlab", Host: "gitlab.example.test", Repo: "group/repo", Branch: "fleet/session",
		Number: 15, Head: strings.Repeat("b", 40), Run: 75, Operation: "ci_logs"}
	mr := glMR{IID: d.Number, SourceBranch: d.Branch, SHA: d.Head, HeadPipeline: &glMRPipeline{ID: d.Run, SHA: d.Head, Status: "failed"}}
	old := glPipeline{ID: d.Run, Ref: d.Branch, SHA: d.Head, Status: "failed"}
	replaced := mr
	replaced.HeadPipeline = &glMRPipeline{ID: d.Run + 1, SHA: d.Head, Status: "pending"}
	synthetic := glPipeline{ID: d.Run + 2, Ref: d.Branch, SHA: d.Head, Status: "pending"}
	for _, tc := range []struct {
		name  string
		repo  glRepo
		stale bool
	}{
		{"same scope", glRepo{open: []glMR{mr}, failed: []glPipeline{old}}, false},
		{"replaced pipeline", glRepo{open: []glMR{replaced}}, true},
		{"unassociated pipeline on the same branch", glRepo{open: []glMR{mr}, pending: []glPipeline{synthetic}}, false},
		{"closed in complete list", glRepo{open: []glMR{}}, true},
		{"incomplete list", glRepo{open: make([]glMR, 30)}, false},
		{"unknown reads", glRepo{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := gitLabDeliveryFacts(tc.repo, time.Now()).conflicts(d, true); got != tc.stale {
				t.Fatalf("stale = %v, want %v", got, tc.stale)
			}
		})
	}
}
