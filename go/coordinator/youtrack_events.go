package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

const youtrackOverlap = 2 * time.Minute

var youtrackActivityID = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)

type youtrackSignal struct {
	IssueID  string `json:"issueId"`
	Signal   string `json:"signal"`
	Revision int64  `json:"revision"`
	EventRef string `json:"eventRef,omitempty"` // activity ID, or the immutable comment ID for a comment
}

func (s youtrackSignal) valid() bool {
	return youtrackEntityID.MatchString(s.IssueID) && s.Revision > 0 &&
		(s.Signal == "assignment" || s.Signal == "comment") &&
		(s.EventRef == "" || youtrackActivityID.MatchString(s.EventRef))
}

func (s youtrackSignal) key() string {
	return "hook:" + sha256hex(jsonBytes(s))
}

func (a *App) youtrackWebhook(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	id := r.PathValue("binding")
	if !ok || len(bearer) < 32 || !botIDPattern.MatchString(id) {
		writeError(w, &HTTPError{Code: 401, Msg: "Unauthorized", Refused: true})
		return
	}
	b, err := a.youtrackBinding(ctx, id)
	if err != nil && !errors.Is(err, errNotFound) {
		writeError(w, err)
		return
	}
	if b.Disconnected || subtle.ConstantTimeCompare([]byte(sha256hex([]byte(bearer))), []byte(b.TokenSha256)) != 1 {
		writeError(w, &HTTPError{Code: 401, Msg: "Unauthorized", Refused: true})
		return
	}
	var s youtrackSignal
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	d.DisallowUnknownFields()
	if d.Decode(&s) != nil || d.Decode(new(json.RawMessage)) != io.EOF || !s.valid() {
		writeError(w, fail(400, "Invalid YouTrack signal"))
		return
	}
	if !youtrackScopeContains(b, s.IssueID) {
		writeError(w, errNotFound)
		return
	}
	if err := a.youtrackReceive(ctx, b, s); err != nil {
		writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func (a *App) youtrackBinding(ctx context.Context, id string) (db.YoutrackBinding, error) {
	var b db.YoutrackBinding
	err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		b, err = q.db().YoutrackBindingGet(q.ctx(), id)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		err = errNotFound
	}
	return b, err
}

func youtrackScopeContains(b db.YoutrackBinding, id string) bool {
	return youtrackEntityID.MatchString(id) && (len(b.IssueIds) == 0 || slices.Contains(b.IssueIds, id))
}

func youtrackEnabled(q *Request, b db.YoutrackBinding) (db.Trigger, botFull, error) {
	t, err := q.db().TriggerGet(q.ctx(), b.ID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (!youtrackTriggerFor(t, b) || !t.Enabled) {
		return t, botFull{}, fail(409, "YouTrack trigger is paused or unavailable")
	}
	if err != nil {
		return t, botFull{}, err
	}
	bot, err := readBotFull(q, b.BotID)
	if err != nil {
		return t, bot, err
	}
	if bot.Paused || bot.ProjectId != b.ProjectID {
		return t, bot, fail(409, "YouTrack bot is paused or unavailable")
	}
	if err := youtrackGrant(q, b.ProjectID, len(b.IssueIds) == 0); err != nil {
		return t, bot, err
	}
	return t, bot, nil
}

type youtrackRef struct {
	ID string `json:"id"`
}

type youtrackVisibility struct {
	Type string `json:"$type"`
}

func (v *youtrackVisibility) restricted() bool {
	return v != nil && v.Type != "UnlimitedVisibility"
}

type youtrackIssue struct {
	ID         string              `json:"id"`
	Key        string              `json:"idReadable"`
	Project    youtrackRef         `json:"project"`
	Updated    int64               `json:"updated"`
	Resolved   *int64              `json:"resolved"`
	Visibility *youtrackVisibility `json:"visibility"`
	Fields     []struct {
		Name  string      `json:"name"`
		Value youtrackRef `json:"value"`
	} `json:"customFields"`
}

func (i youtrackIssue) assignee() string {
	for _, f := range i.Fields {
		if f.Name == "Assignee" {
			return f.Value.ID
		}
	}
	return ""
}

func youtrackReadIssue(ctx context.Context, call youtrackCall, b db.YoutrackBinding, id string) (youtrackIssue, error) {
	var empty youtrackIssue
	if !youtrackScopeContains(b, id) {
		return empty, errNotFound
	}
	return youtrackResolveIssue(ctx, call, b, id)
}

func youtrackResolveIssue(ctx context.Context, call youtrackCall, b db.YoutrackBinding, ref string) (youtrackIssue, error) {
	var empty youtrackIssue
	issue, err := youtrackRead[youtrackIssue](ctx, call, "handoff_issue", map[string]string{"issue": ref})
	if err != nil {
		return empty, err
	}
	if issue.ID != ref && !strings.EqualFold(issue.Key, ref) || issue.Project.ID != b.RemoteProjectID || issue.Visibility.restricted() {
		return empty, errNotFound
	}
	if issue.Updated <= 0 || !youtrackIssueKey.MatchString(issue.Key) || !youtrackEntityID.MatchString(issue.ID) {
		return empty, fail(503, "YouTrack issue metadata is incomplete")
	}
	return issue, nil
}

type youtrackActivity struct {
	ID     string      `json:"id"`
	Type   string      `json:"$type"`
	At     int64       `json:"timestamp"`
	Author youtrackRef `json:"author"`
	Field  struct {
		Name string `json:"name"`
	} `json:"field"`
	Added  json.RawMessage `json:"added"`
	Target struct {
		ID         string              `json:"id"`
		Issue      youtrackRef         `json:"issue"`
		Deleted    bool                `json:"deleted"`
		Visibility *youtrackVisibility `json:"visibility"`
	} `json:"target"`
}

func (e youtrackActivity) kind() string {
	switch {
	case e.Type == "CustomFieldActivityItem" && e.Field.Name == "Assignee":
		return "assignment"
	case e.Type == "CommentActivityItem" && youtrackEntityID.MatchString(e.Target.ID) &&
		!e.Target.Deleted && !e.Target.Visibility.restricted():
		// CommentsCategory includes removed comments. Only a creation has an added value.
		if added, _ := jsonAs[[]json.RawMessage](e.Added); len(added) > 0 {
			return "comment"
		}
	}
	return ""
}

func (e youtrackActivity) issueID() string {
	if e.Type == "CommentActivityItem" {
		return e.Target.Issue.ID
	}
	return e.Target.ID
}

func (e youtrackActivity) assignedTo(id string) bool {
	if added, ok := jsonAs[[]youtrackRef](e.Added); ok {
		return len(added) == 1 && added[0].ID == id
	}
	ref, _ := jsonAs[youtrackRef](e.Added)
	return ref.ID == id
}

func (s youtrackSignal) matches(e youtrackActivity) bool {
	if s.Signal != e.kind() {
		return false
	}
	if s.EventRef != "" {
		return s.EventRef == e.ID || s.Signal == "comment" && s.EventRef == e.Target.ID
	}
	return s.Revision == e.At
}

type youtrackPage[T any] struct {
	Activities  []T    `json:"activities"`
	HasAfter    *bool  `json:"hasAfter"`
	AfterCursor string `json:"afterCursor"`
}

func youtrackActivities[T any](ctx context.Context, call youtrackCall, tool string, args map[string]string, visit func(T)) error {
	seen := map[string]bool{}
	for {
		page, err := youtrackRead[youtrackPage[T]](ctx, call, tool, args)
		if err != nil {
			return err
		}
		if page.HasAfter == nil || page.Activities == nil {
			return fail(503, "YouTrack activity page is incomplete")
		}
		for _, event := range page.Activities {
			visit(event)
		}
		if !*page.HasAfter {
			return nil
		}
		if page.AfterCursor == "" || seen[page.AfterCursor] {
			return fail(503, "YouTrack activity cursor did not advance")
		}
		seen[page.AfterCursor] = true
		args["cursor"] = page.AfterCursor
		tool = strings.TrimSuffix(tool, "_next") + "_next"
	}
}

func youtrackEvents(ctx context.Context, call youtrackCall, b db.YoutrackBinding, issueID string, from, through int64) (map[string][]youtrackActivity, error) {
	tool := "handoff_project_events"
	args := map[string]string{"start": fmt.Sprint(from), "end": fmt.Sprint(through)}
	if issueID == "" {
		args["query"] = "project: " + b.RemoteProjectKey
	} else {
		tool, args["issue"] = "handoff_issue_events", issueID
	}
	out := map[string][]youtrackActivity{}
	complete := true
	err := youtrackActivities(ctx, call, tool, args, func(e youtrackActivity) {
		if e.At < from || e.At > through || !youtrackActivityID.MatchString(e.ID) || e.kind() == "" {
			return
		}
		id := e.issueID()
		if id == "" && issueID != "" {
			id = issueID // an issue's own activity endpoint already scopes its rows
		}
		if !youtrackEntityID.MatchString(id) {
			complete = false
			return
		}
		if (issueID == "" || id == issueID) && youtrackScopeContains(b, id) {
			out[id] = append(out[id], e)
		}
	})
	if err == nil && !complete {
		err = fail(503, "YouTrack activity returned no immutable issue ID")
	}
	return out, err
}

func (a *App) youtrackHandleSignal(ctx context.Context, b db.YoutrackBinding, s youtrackSignal) error {
	now := nowMs()
	if s.Revision < b.StartedMs {
		return nil // this binding has no authority over pre-enrollment events
	}
	if s.Revision > now {
		return fail(409, "YouTrack event revision is ahead of Fleet")
	}
	var received bool
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		_, err := q.db().YoutrackReceiptGet(q.ctx(), db.YoutrackReceiptGetParams{BindingID: b.ID, IssueID: s.IssueID, RemoteEvent: s.key()})
		received = err == nil
		if received {
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		_, _, err = youtrackEnabled(q, b)
		return err
	}); err != nil || received {
		return err
	}
	call, release, err := youtrackBoundOpen(ctx, a, b)
	if err != nil {
		return err
	}
	defer release()
	if _, err := youtrackReadIssue(ctx, call, b, s.IssueID); err != nil {
		return err
	}
	from := max(b.StartedMs, min(b.CursorMs, s.Revision)-youtrackOverlap.Milliseconds())
	events, err := youtrackEvents(ctx, call, b, s.IssueID, from, now)
	if err != nil {
		return err
	}
	list := events[s.IssueID]
	if !slices.ContainsFunc(list, s.matches) {
		if b.CursorMs > s.Revision+youtrackOverlap.Milliseconds() {
			return nil // the overlap pass has already handled any real activity at this revision
		}
		return fail(409, "YouTrack activity is not yet confirmed")
	}
	return a.youtrackProcessIssue(ctx, call, b, s.IssueID, list, from, now, &s)
}

func youtrackReconcile(ctx context.Context, a *App, at time.Time) error {
	var bindings []db.YoutrackBindingsActiveRow
	if err := a.store.Read(ctx, nil, func(q *Request) (err error) {
		bindings, err = q.db().YoutrackBindingsActive(q.ctx())
		return err
	}); err != nil {
		return err
	}
	var result error
	for _, row := range bindings {
		b := row.YoutrackBinding
		err := a.youtrackReconcileBinding(ctx, b, at.UnixMilli())
		if err != nil {
			result = errors.Join(result, fmt.Errorf("YouTrack binding %s: %w", b.ID, err))
		}
		if recordErr := a.store.Write(ctx, nil, []string{"bot:" + b.BotID}, func(q *Request) error {
			if err != nil {
				return q.db().TriggerSetError(q.ctx(), db.TriggerSetErrorParams{ID: b.ID, Error: "YouTrack reconciliation incomplete"})
			}
			return q.db().YoutrackClearTriggerError(q.ctx(), b.ID)
		}); recordErr != nil {
			result = errors.Join(result, recordErr)
		}
	}
	return result
}

func (a *App) youtrackReconcileBinding(ctx context.Context, b db.YoutrackBinding, through int64) error {
	if through <= b.CursorMs {
		return nil
	}
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		return youtrackGrant(q, b.ProjectID, len(b.IssueIds) == 0)
	}); err != nil {
		return err
	}
	call, release, err := youtrackBoundOpen(ctx, a, b)
	if err != nil {
		return err
	}
	defer release()
	ids := b.IssueIds
	if len(ids) == 0 {
		ids = []string{""}
	}
	from := max(b.StartedMs, b.CursorMs-youtrackOverlap.Milliseconds())
	var failed error
	for _, id := range ids {
		if id != "" {
			if _, err := youtrackReadIssue(ctx, call, b, id); err != nil {
				if !errors.Is(err, errNotFound) {
					failed = errors.Join(failed, err)
				}
				continue
			}
		}
		events, err := youtrackEvents(ctx, call, b, id, from, through)
		if errors.Is(err, errNotFound) && id != "" {
			continue // an allowlisted issue was removed or is no longer visible
		}
		if err != nil {
			failed = errors.Join(failed, err)
			continue
		}
		for issueID, list := range events {
			if !slices.ContainsFunc(list, func(e youtrackActivity) bool { return e.kind() == "assignment" || e.Author.ID == b.ReviewerUserID }) {
				continue
			}
			err := a.youtrackProcessIssue(ctx, call, b, issueID, list, from, through, nil)
			var deferred *youtrackDeferred
			if errors.As(err, &deferred) {
				err = a.youtrackReceive(ctx, b, youtrackSignal{IssueID: issueID, Signal: deferred.event.kind(),
					EventRef: deferred.event.ID, Revision: deferred.event.At})
			}
			failed = errors.Join(failed, err)
		}
	}
	if failed != nil {
		return failed
	}
	return a.store.Write(ctx, nil, []string{"bot:" + b.BotID}, func(q *Request) error {
		return q.db().YoutrackBindingAdvance(q.ctx(), db.YoutrackBindingAdvanceParams{ID: b.ID, PreviousMs: b.CursorMs, CursorMs: through, Revision: b.Revision})
	})
}

func (a *App) youtrackProcessIssue(ctx context.Context, call youtrackCall, b db.YoutrackBinding, id string, events []youtrackActivity, from, through int64, hint *youtrackSignal) error {
	var err error
	events, err = a.youtrackReplyHistory(ctx, call, b, id, events, from)
	if err != nil {
		return err
	}
	issue, err := youtrackReadIssue(ctx, call, b, id)
	if errors.Is(err, errNotFound) && hint == nil {
		return nil
	}
	if err != nil {
		return err
	}
	if issue.Updated > through {
		return fail(409, "YouTrack issue changed during verification")
	}
	return a.store.Write(ctx, nil, []string{GLOBAL, "bot:" + b.BotID}, func(q *Request) error {
		saved, err := q.db().YoutrackBindingGet(q.ctx(), b.ID)
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (saved.Disconnected || saved.Revision != b.Revision) {
			return errNotFound
		}
		if err != nil {
			return err
		}
		return youtrackApply(q, b, issue, events, hint)
	})
}

func youtrackApply(q *Request, b db.YoutrackBinding, issue youtrackIssue, events []youtrackActivity, hint *youtrackSignal) error {
	t, bot, err := youtrackEnabled(q, b)
	if err != nil {
		return err
	}
	link, err := q.db().YoutrackTaskGet(q.ctx(), db.YoutrackTaskGetParams{Instance: b.Instance, IssueID: issue.ID})
	hasLink := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	assignment, ambiguous := youtrackLatestAssignment(b, events)
	active := issue.Resolved == nil && issue.assignee() == b.BotUserID
	var wokeEvent string
	if !hasLink || link.BindingID == b.ID && link.TaskID != nil {
		if issue.Updated < link.IssueUpdatedMs {
			return fail(409, "YouTrack returned an older issue observation")
		}
		if active && !ambiguous {
			action := youtrackChoose(b, link, assignment, events, hint)
			if action != nil {
				wokeEvent, err = youtrackAdmit(q, b, &bot, t, issue, link, *action, events)
				if err != nil {
					return err
				}
			}
		}
		if hasLink {
			if err := youtrackObserve(q, b, link, issue, assignment, active, ambiguous); err != nil {
				return err
			}
		}
	}
	for _, e := range events {
		if hint != nil && !hint.matches(e) || hint == nil && e.Author.ID != b.ReviewerUserID {
			continue
		}
		if err := youtrackReceipt(q, b, issue.ID, "event:"+e.ID, e.At, e.ID == wokeEvent); err != nil {
			return err
		}
	}
	if hint != nil {
		return youtrackReceipt(q, b, issue.ID, hint.key(), hint.Revision, wokeEvent != "")
	}
	return nil
}

func youtrackReceipt(q *Request, b db.YoutrackBinding, issue, event string, at int64, woke bool) error {
	return q.db().YoutrackReceiptInsert(q.ctx(), db.YoutrackReceiptInsertParams{BindingID: b.ID, ProjectID: b.ProjectID,
		IssueID: issue, RemoteEvent: event, ObservedMs: at, Woke: woke})
}

// A provider time tie with contradictory assignee facts must never authorize a wake.
func youtrackLatestAssignment(b db.YoutrackBinding, events []youtrackActivity) (*youtrackActivity, bool) {
	var last *youtrackActivity
	var ambiguous bool
	for i := range events {
		e := &events[i]
		if e.kind() != "assignment" {
			continue
		}
		if last == nil || e.At > last.At {
			last, ambiguous = e, false
		} else if e.At == last.At && (e.Author != last.Author || e.assignedTo(b.BotUserID) != last.assignedTo(b.BotUserID)) {
			ambiguous = true
		}
	}
	return last, ambiguous
}

func youtrackHandoffByReviewer(b db.YoutrackBinding, assignment *youtrackActivity) bool {
	return assignment != nil && assignment.Author.ID == b.ReviewerUserID && assignment.assignedTo(b.BotUserID)
}

func youtrackChoose(b db.YoutrackBinding, link db.YoutrackTask, assignment *youtrackActivity, events []youtrackActivity, hint *youtrackSignal) *youtrackActivity {
	if assignment != nil && !youtrackHandoffByReviewer(b, assignment) {
		return nil
	}
	if assignment != nil && assignment.At >= link.AssignmentMs && assignment.At > link.DecisionMs &&
		(hint == nil || hint.matches(*assignment)) {
		return assignment
	}
	if link.TaskID == nil || !link.AssignmentAuthorized || assignment != nil && assignment.At > link.DecisionMs {
		return nil
	}
	var last *youtrackActivity
	for i := range events {
		e := &events[i]
		if e.kind() == "comment" && e.Author.ID == b.ReviewerUserID && e.At > max(link.DecisionMs, link.AssignmentMs) &&
			(hint == nil || hint.matches(*e)) && (last == nil || e.At > last.At || e.At == last.At && e.ID > last.ID) {
			last = e
		}
	}
	return last
}

func youtrackObserve(q *Request, b db.YoutrackBinding, link db.YoutrackTask, issue youtrackIssue, assignment *youtrackActivity, active, ambiguous bool) error {
	observedMs, authorized := link.AssignmentMs, active && link.AssignmentAuthorized
	if assignment != nil && assignment.At >= observedMs {
		observedMs, authorized = assignment.At, active && !ambiguous && youtrackHandoffByReviewer(b, assignment)
	}
	if observedMs == link.AssignmentMs && authorized == link.AssignmentAuthorized && issue.Updated == link.IssueUpdatedMs {
		return nil
	}
	return q.db().YoutrackTaskObserve(q.ctx(), db.YoutrackTaskObserveParams{Instance: b.Instance, IssueID: issue.ID,
		AssignmentMs: observedMs, AssignmentAuthorized: authorized, IssueUpdatedMs: issue.Updated})
}

// A reviewer may leave a reply long before assigning the issue back. Read from the open milestone's notice
// when it predates this scan's overlapping cursor. Only comment metadata is needed to match the handoff.
func (a *App) youtrackReplyHistory(ctx context.Context, call youtrackCall, b db.YoutrackBinding, id string, events []youtrackActivity, from int64) ([]youtrackActivity, error) {
	assignment, ambiguous := youtrackLatestAssignment(b, events)
	if ambiguous || !youtrackHandoffByReviewer(b, assignment) {
		return events, nil
	}
	var postedAt *int64
	err := a.store.Read(ctx, nil, func(q *Request) error {
		link, err := q.db().YoutrackTaskGet(q.ctx(), db.YoutrackTaskGetParams{Instance: b.Instance, IssueID: id})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && (link.BindingID != b.ID || link.TaskID == nil || assignment.At <= link.DecisionMs) {
			return nil
		}
		if err != nil {
			return err
		}
		wait, err := q.db().YoutrackTaskWait(q.ctx(), link.TaskID)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		postedAt = wait.ProviderCreatedMs
		return err
	})
	if err != nil || postedAt == nil || *postedAt >= from {
		return events, err
	}
	if _, err := youtrackReadIssue(ctx, call, b, id); err != nil {
		return nil, err
	}
	older, err := youtrackEvents(ctx, call, b, id, *postedAt, from-1)
	if err != nil {
		return nil, err
	}
	for _, e := range older[id] {
		if e.kind() == "comment" && e.Author.ID == b.ReviewerUserID {
			events = append(events, e)
		}
	}
	return events, nil
}

var errYoutrackWait = errors.New("YouTrack action awaits Fleet task readiness")

type youtrackDeferred struct{ event youtrackActivity }

func (*youtrackDeferred) Error() string { return errYoutrackWait.Error() }

func youtrackAdmit(q *Request, b db.YoutrackBinding, bot *botFull, trigger db.Trigger, issue youtrackIssue, link db.YoutrackTask, action youtrackActivity, events []youtrackActivity) (string, error) {
	_, err := q.db().YoutrackReceiptGet(q.ctx(), db.YoutrackReceiptGetParams{BindingID: b.ID, IssueID: issue.ID, RemoteEvent: "event:" + action.ID})
	if err == nil {
		return "", nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	if err := youtrackReviewerAccess(q, b); err != nil {
		return "", err
	}
	if err := botAuthorityCheck(q, bot); err != nil {
		return "", err
	}
	taskID := deref(link.TaskID)
	if taskID == "" {
		// Reserve the immutable issue first. A hidden correlation still occupies its global key.
		created, err := q.db().YoutrackTaskReserve(q.ctx(), db.YoutrackTaskReserveParams{Instance: b.Instance, IssueID: issue.ID,
			BindingID: b.ID, ProjectID: b.ProjectID, DecisionMs: action.At, AssignmentMs: action.At, IssueUpdatedMs: issue.Updated})
		if err != nil || created == 0 {
			return "", err
		}
		outcome := "Coordinate the verified YouTrack handoff for " + issue.Key + ". Keep issue content and attachments under project data policy and obtain Fleet result acceptance before closing YouTrack."
		taskID, err = createTask(q, newTask{ProjectID: &b.ProjectID, BotID: &b.BotID, Held: true,
			Title: "YouTrack " + issue.Key, ExpectedOutcome: &outcome, CreatedBy: "youtrack", DirectedBy: b.ReviewerPerson,
			CompletionRequirements: json.RawMessage(`{"acceptance":"human"}`)})
		if err != nil {
			return "", err
		}
		if err := q.db().YoutrackTaskAttach(q.ctx(), db.YoutrackTaskAttachParams{Instance: b.Instance, IssueID: issue.ID, TaskID: &taskID}); err != nil {
			return "", err
		}
		if err := youtrackQueueComment(q, b, issue.ID, "link", "link"); err != nil {
			return "", err
		}
	} else {
		if err := acquireTxLock(q, "task:"+taskID); err != nil {
			return "", err
		}
		ok, err := youtrackTaskActionable(q, link, action, events, b)
		if errors.Is(err, errYoutrackWait) {
			return "", &youtrackDeferred{event: action}
		}
		if err != nil || !ok {
			return "", err
		}
		if err := q.db().YoutrackTaskAdvance(q.ctx(), db.YoutrackTaskAdvanceParams{Instance: b.Instance, IssueID: issue.ID,
			DecisionMs: action.At}); err != nil {
			return "", err
		}
	}
	msg := fmt.Sprintf("Fleet verified a YouTrack %s for %s (https://%s/issue/%s), correlated with Fleet task %s. Check current assignment and project policy before acting. Fleet owns task correlation and the YouTrack link notice. Reuse the linked Fleet task and use youtrack_comment_sync for question and result notices and require Fleet human acceptance before closing the issue.", action.kind(), issue.Key, b.Instance, issue.Key, taskID)
	_, _, sent, err := admitBotWake(q, bot, botWakeReq{Cause: "needs_you", DedupKey: "youtrack:" + b.ID + ":" + sha256hex([]byte(issue.ID+" "+action.ID)),
		Text: msg, SentBy: "Fleet", Source: "trigger", CheckPause: true, Trigger: &trigger, Thread: "task:" + taskID})
	if err != nil {
		return "", err
	}
	if sent {
		return action.ID, q.db().YoutrackCountWake(q.ctx(), trigger.ID)
	}
	return "", nil
}

func youtrackTaskActionable(q *Request, link db.YoutrackTask, action youtrackActivity, events []youtrackActivity, b db.YoutrackBinding) (bool, error) {
	taskID := *link.TaskID
	task, err := q.db().TaskGet(q.ctx(), taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil || deref(task.ProjectID) != b.ProjectID {
		return false, err
	}
	switch task.State {
	case db.TaskStateCancelled, db.TaskStateCompleted, db.TaskStateFailed:
		return false, nil
	case db.TaskStatePaused:
		if deref(task.PauseReason) != "held" {
			return false, errYoutrackWait
		}
	}
	wait, err := q.db().YoutrackTaskWait(q.ctx(), &taskID)
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	if err != nil || action.kind() != "assignment" || wait.Kind == db.QuestionKindSecret ||
		wait.AddressedTo != nil &&
			*wait.AddressedTo != b.ReviewerPerson && *wait.AddressedTo != "bot:"+b.BotID {
		return false, err
	}
	if wait.ProviderCreatedMs == nil {
		return false, errYoutrackWait
	}
	// An open Fleet milestone requires a reviewer reply after its notice and a verified return assignment.
	return slices.ContainsFunc(events, func(e youtrackActivity) bool {
		return e.kind() == "comment" && e.Author.ID == b.ReviewerUserID && e.At > *wait.ProviderCreatedMs && e.At > link.DecisionMs && e.At <= action.At
	}), nil
}
