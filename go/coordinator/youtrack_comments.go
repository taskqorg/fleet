package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

const youtrackComments = "youtrack-comments"

func init() {
	kitRegister(&kitQueue{
		Name: youtrackComments, Period: time.Second, Limit: 16, Due: youtrackCommentsDue, Step: youtrackCommentStep,
		Retry: kitRetry{Table: "youtrack_comment_work", Key: "id",
			Guard: "youtrack_comment_work.attempts=$5::int AND youtrack_comment_work.confirmed_at IS NULL",
			Class: youtrackCommentError, OnFail: youtrackCommentBeforePost},
	})
}

func youtrackCommentID(b db.YoutrackBinding, issue, milestone string) string {
	return sha256hex([]byte(b.ID + " " + issue + " " + milestone))
}

func youtrackQueueComment(q *Request, b db.YoutrackBinding, issue, milestone, kind string) error {
	n, err := q.db().YoutrackCommentInsert(q.ctx(), db.YoutrackCommentInsertParams{
		ID: youtrackCommentID(b, issue, milestone), BindingID: b.ID, IssueID: issue,
		ProjectID: b.ProjectID, Milestone: milestone, Kind: kind,
	})
	if err != nil || n == 0 {
		return err
	}
	return kitNotify(q, youtrackComments)
}

func youtrackCommentSync(q *Request, r *contract.YoutrackCommentSyncRequest) (contract.YoutrackCommentSyncReply, error) {
	var out contract.YoutrackCommentSyncReply
	if r.TaskId == "" || r.MilestoneId != "link" && !botIDPattern.MatchString(r.MilestoneId) {
		return out, fail(400, "Expected a task and Fleet milestone ID")
	}
	link, err := q.db().YoutrackTaskByID(q.ctx(), &r.TaskId)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, errNotFound
	}
	if err != nil {
		return out, err
	}
	b, err := q.db().YoutrackBindingGet(q.ctx(), link.BindingID)
	if err != nil {
		return out, err
	}
	if err := youtrackCommentCaller(q, b, r.TaskId); err != nil {
		return out, err
	}
	if err := acquireTxLock(q, "task:"+r.TaskId); err != nil {
		return out, err
	}
	id := youtrackCommentID(b, link.IssueID, r.MilestoneId)
	work, err := q.db().YoutrackCommentGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		if r.MilestoneId == "link" {
			return out, errNotFound
		}
		if b.Disconnected {
			return out, fail(409, "YouTrack binding is disconnected")
		}
		kind, err := youtrackMilestone(q, b, r.TaskId, r.MilestoneId)
		if err != nil {
			return out, err
		}
		if err := youtrackQueueComment(q, b, link.IssueID, r.MilestoneId, kind); err != nil {
			return out, err
		}
	} else if err != nil {
		return out, err
	}
	out.State, out.LastError = "queued", work.LastError
	if work.SkipReason != nil {
		out.State, out.LastError = "skipped", work.SkipReason
	} else if work.ConfirmedAt != nil {
		out.State, out.CommentId = "confirmed", work.CommentID
	} else if work.SentAt != nil {
		out.State = "unknown"
		if r.Recheck != nil && *r.Recheck {
			if err := kitSchedule(q, youtrackComments, work.ID, time.Now()); err != nil {
				return out, err
			}
			return out, kitNotify(q, youtrackComments)
		}
	}
	return out, nil
}

func youtrackCommentCaller(q *Request, b db.YoutrackBinding, task string) error {
	if q.person == nil || !mayAccessProjectID(q.person, b.ProjectID) {
		return errNotFound
	}
	if q.agent == "" {
		if fullAccess(q.person) || q.person.Email == b.ReviewerPerson {
			return nil
		}
		return fail(403, "Use the YouTrack reviewer or bound PM bot")
	}
	e, err := getExecution(q, strings.TrimPrefix(q.agent, "execution:"))
	if err != nil {
		return err
	}
	s, err := sessionByID(q, e.SessionID)
	if err != nil {
		return err
	}
	if s.BotID != b.BotID || s.ProjectID != b.ProjectID {
		return fail(403, "This execution is outside the YouTrack binding")
	}
	subject, err := q.db().YoutrackSessionSubject(q.ctx(), s.ID)
	if err != nil {
		return err
	}
	if deref(subject) != "task:"+task {
		return fail(403, "Use the task's bound PM conversation")
	}
	return nil
}

var errYoutrackMilestoneClosed = fail(409, "Milestone is closed or superseded")

func youtrackMilestone(q *Request, b db.YoutrackBinding, taskID, id string) (string, error) {
	m, err := q.db().TaskQuestion(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (deref(m.TaskID) != taskID || deref(m.ProjectID) != b.ProjectID) {
		return "", errNotFound
	}
	if err != nil {
		return "", err
	}
	if m.State != db.QuestionStateOpen || m.AddressedTo != nil &&
		*m.AddressedTo != b.ReviewerPerson && *m.AddressedTo != "bot:"+b.BotID {
		return "", errYoutrackMilestoneClosed
	}
	switch m.Kind {
	case db.QuestionKindOperatorDecision, db.QuestionKindAgentQuestion:
		return "question", nil
	case db.QuestionKindAcceptance:
		var data questionData
		if json.Unmarshal(m.Data, &data) != nil {
			return "", fail(409, "Result offer is invalid")
		}
		pending, err := q.db().TaskHasPendingWork(q.ctx(), m.TaskID)
		if err != nil {
			return "", err
		}
		selected, err := acceptanceResult(q, taskID, data.EarlierResult, pending, deref(m.AskedBy) == "fleet")
		if err != nil {
			return "", err
		}
		if selected.id != deref(m.ExecutionID) || !selected.matches(data.ArtifactIds) {
			return "", errYoutrackMilestoneClosed
		}
		return "result", nil
	default:
		return "", fail(400, "Use an ordinary task question or result offer")
	}
}

func youtrackCommentsDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rows, err := q.db().YoutrackCommentsDue(q.ctx(), db.YoutrackCommentsDueParams{At: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rows))
	for _, r := range rows {
		out = append(out, &kitClaim{Key: r.ID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

type youtrackPosted struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
}

func (p youtrackPosted) valid() bool {
	return youtrackEntityID.MatchString(p.ID) && p.Created > 0
}

type youtrackCommentResult struct {
	Post youtrackPosted
	Err  error
}

type youtrackNotPosted struct {
	At     time.Time
	Reason string
}

func (e *youtrackNotPosted) Error() string { return e.Reason }

func youtrackCommentError(err error) string {
	if errors.Is(err, errYoutrackCommentUnknown) {
		return "youtrack-comment-unknown"
	}
	var known *youtrackNotPosted
	if errors.As(err, &known) {
		return "youtrack-comment-not-posted"
	}
	return "youtrack-comment-blocked"
}

func youtrackCommentBeforePost(q *Request, c *kitClaim, err error) error {
	var known *youtrackNotPosted
	if !errors.As(err, &known) {
		return nil
	}
	return q.db().YoutrackCommentRestart(q.ctx(), db.YoutrackCommentRestartParams{ID: c.Key, At: &known.At})
}

func youtrackCommentStep(q *Request, now time.Time, c *kitClaim) error {
	work, err := q.db().YoutrackCommentGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (work.ConfirmedAt != nil || work.SkipReason != nil) {
		return nil
	}
	if err != nil {
		return err
	}
	if result, ok := c.Result.(youtrackCommentResult); ok {
		if result.Err != nil {
			return result.Err
		}
		return q.db().YoutrackCommentConfirm(q.ctx(), db.YoutrackCommentConfirmParams{ID: work.ID, At: &now, CommentID: &result.Post.ID, ProviderCreatedMs: &result.Post.Created})
	}
	b, err := q.db().YoutrackBindingGet(q.ctx(), work.BindingID)
	if err != nil {
		return err
	}
	if err := kitLock(q, "bot:"+b.BotID); err != nil {
		return err
	}
	link, err := q.db().YoutrackTaskGet(q.ctx(), db.YoutrackTaskGetParams{Instance: b.Instance, IssueID: work.IssueID})
	if err != nil {
		return err
	}
	if work.SentAt != nil {
		c.Call = func(ctx context.Context) any { return youtrackCommentLookup(ctx, q.app, b, work) }
		return nil
	}
	if link.BindingID != b.ID || link.TaskID == nil {
		return errKitSkip
	}
	if b.Disconnected {
		return errKitSkip
	}
	if err := kitLock(q, "task:"+*link.TaskID); err != nil {
		return err
	}
	_, bot, err := youtrackEnabled(q, b)
	if err != nil {
		return err
	}
	if err := botAuthorityCheck(q, &bot); err != nil {
		return err
	}
	if err := youtrackReviewerAccess(q, b); err != nil {
		return err
	}
	t, err := q.db().TaskGet(q.ctx(), *link.TaskID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && t.State == db.TaskStateCancelled {
		return errKitSkip
	}
	if err != nil {
		return err
	}
	if work.Kind != "link" {
		if _, err := youtrackMilestone(q, b, t.ID, work.Milestone); err != nil {
			if errors.Is(err, errNotFound) || errors.Is(err, errYoutrackMilestoneClosed) {
				return youtrackSkipComment(q, work.ID, "Fleet milestone is closed or superseded")
			}
			return err
		}
	}
	// A committed send intent always precedes the POST. After a process failure, its only recovery is a readback.
	// PG timestamps retain microseconds. Keep the intent identity unchanged through a known-not-sent recovery.
	now = now.Truncate(time.Microsecond)
	n, err := q.db().YoutrackCommentStart(q.ctx(), db.YoutrackCommentStartParams{ID: work.ID, At: &now})
	if err != nil || n == 0 {
		return err
	}
	work.SentAt = &now
	text := youtrackCommentText(work, firstString(deref(t.Code), t.ID))
	c.Call = func(ctx context.Context) any { return youtrackCommentPost(ctx, q.app, b, work, text) }
	return nil
}

func youtrackSkipComment(q *Request, id, reason string) error {
	return q.db().YoutrackCommentSkip(q.ctx(), db.YoutrackCommentSkipParams{ID: id, SkipReason: &reason})
}

func youtrackCommentMarker(w db.YoutrackCommentWork) string {
	return "<!-- Fleet handoff " + w.ID + " -->"
}

func youtrackCommentText(w db.YoutrackCommentWork, task string) string {
	title := "Fleet is tracking this issue."
	switch w.Kind {
	case "question":
		title = "Fleet has an open question for the project reviewer."
	case "result":
		title = "A Fleet result is ready for the project reviewer."
	}
	return title + " [Open the Fleet task](https://fleet.ufobe.com/tasks/" + url.PathEscape(task) + ").\n\n" + youtrackCommentMarker(w)
}

var errYoutrackCommentUnknown = errors.New("YouTrack comment outcome is unknown")

func youtrackCommentPost(ctx context.Context, a *App, b db.YoutrackBinding, work db.YoutrackCommentWork, text string) youtrackCommentResult {
	noPost := func(reason string) youtrackCommentResult {
		return youtrackCommentResult{Err: &youtrackNotPosted{At: *work.SentAt, Reason: reason}}
	}
	if err := a.store.Read(ctx, nil, func(q *Request) error {
		live, err := q.db().YoutrackBindingGet(q.ctx(), b.ID)
		if err != nil {
			return err
		}
		if live.Disconnected || live.Revision != b.Revision {
			return errNotFound
		}
		return youtrackGrant(q, b.ProjectID, len(b.IssueIds) == 0)
	}); err != nil {
		return noPost("YouTrack binding or project grant changed")
	}
	call, release, err := youtrackBoundOpen(ctx, a, b)
	if err != nil {
		return noPost("YouTrack connection unavailable")
	}
	defer release()
	issue, err := youtrackReadIssue(ctx, call, b, work.IssueID)
	if err != nil || issue.Resolved != nil || issue.assignee() != b.BotUserID && issue.assignee() != b.ReviewerUserID {
		return noPost("YouTrack issue is outside the active handoff")
	}
	line := call(ctx, "handoff_post_comment", map[string]string{"issue": work.IssueID, "text": text})
	if line.Outcome == contract.CallOutcomeOk {
		post, _ := jsonAs[youtrackPosted](line.Output)
		if post.valid() {
			return youtrackCommentResult{Post: post}
		}
	}
	if youtrackDefiniteRefusal(line) {
		return noPost("YouTrack gateway refused before sending the comment")
	}
	return youtrackCommentResult{Err: errYoutrackCommentUnknown}
}

func youtrackDefiniteRefusal(line contract.CallLine) bool {
	return line.Outcome == contract.CallOutcomeError && slices.Contains([]string{
		"unreachable", "not-granted", "intake-paused", "unknown-tool", "unauthenticated", "budget-reached", "bad-request", "no-key",
		"http-400", "http-401", "http-403", "http-404", "http-422", "http-429",
	}, deref(line.ErrorCode))
}

func youtrackCurrentGrant(ctx context.Context, a *App, b db.YoutrackBinding) error {
	return a.store.Read(ctx, nil, func(q *Request) error {
		return youtrackGrant(q, b.ProjectID, len(b.IssueIds) == 0)
	})
}

func youtrackCommentLookup(ctx context.Context, a *App, b db.YoutrackBinding, w db.YoutrackCommentWork) youtrackCommentResult {
	unknown := youtrackCommentResult{Err: errYoutrackCommentUnknown}
	if err := youtrackCurrentGrant(ctx, a, b); err != nil {
		return unknown
	}
	call, release, err := youtrackBoundOpen(ctx, a, b)
	if err != nil {
		return unknown
	}
	defer release()
	if _, err := youtrackReadIssue(ctx, call, b, w.IssueID); err != nil {
		return unknown
	}
	type written struct {
		Author youtrackRef `json:"author"`
		Target struct {
			youtrackPosted
			Deleted bool   `json:"deleted"`
			Text    string `json:"text"`
		} `json:"target"`
	}
	posts := map[string]youtrackPosted{}
	args := map[string]string{"issue": w.IssueID, "start": strconv.FormatInt(w.SentAt.Add(-youtrackOverlap).UnixMilli(), 10), "end": strconv.FormatInt(nowMs(), 10)}
	err = youtrackActivities(ctx, call, "handoff_own_comments", args, func(e written) {
		if e.Author.ID == b.BotUserID && !e.Target.Deleted && e.Target.valid() &&
			strings.HasSuffix(e.Target.Text, "\n\n"+youtrackCommentMarker(w)) {
			posts[e.Target.ID] = e.Target.youtrackPosted
		}
	})
	if err != nil || len(posts) != 1 {
		return unknown
	}
	for _, post := range posts {
		return youtrackCommentResult{Post: post}
	}
	return unknown
}
