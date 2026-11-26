// Problems (docs/designs/fleet-system-management.md#problems-anomalies-with-a-bot-owner): rule findings, dispatch
// stalls and direct signals as problem rows keyed by cause, each owned by a bot by duty; the only writer of problem
// rows (DESIGN_RULES.md S6). A problem is not a question: a source never asks anyone. One open row per cause; a raise
// of an open cause counts one more occurrence; a cause that returns within problemReopenHours of closing reopens its
// row with its owner, work and conversation. The owner bot asks the owner only through problem_escalate.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

const (
	problemReopenHours = 24 // problems.reopenHours
	problemStuckWakes  = 3  // problems.stuckWakes
	problemStuckHours  = 48 // problems.stuckHours
)

// Problem is one current cause of a source.
type Problem struct {
	Cause, Text, Rule string
	ProjectID         string     // "" for a system cause
	RefreshText       bool       // Refresh the text while this problem is open.
	Unowned           bool       // No duty owns it (the watchdog's: the duty bot itself is down).
	Count             int64      // >0: the source's own tally of occurrences (agent gaps), not one per raise
	SeenAt            *time.Time // with Count: the last occurrence
	Owner             string     // the owner bot whatever the duty (a bot's own failed sessions)
	Data              any        // a struct whose JSON fields join the problem's data: hostId, storeId, executionId, exemplars, value
}

// problemData: the problem's data: cause, source and rule, then the problem's own fields over them.
func problemData(source string, p Problem) ([]byte, error) {
	data := map[string]json.RawMessage{"cause": jsonBytes(p.Cause)}
	if source != "" {
		data["source"] = jsonBytes(source)
	}
	if p.Rule != "" {
		data["rule"] = jsonBytes(p.Rule)
	}
	own, err := openObject(jsonBytes(p.Data))
	if err != nil {
		return nil, fmt.Errorf("problem %s data: %w", p.Cause, err)
	}
	for k, v := range own {
		data[k] = v
	}
	return json.Marshal(data)
}

// problemRaise opens p, or counts one more occurrence of its open row; it answers the row's id.
func problemRaiseID(q *Request, source string, p Problem) (string, error) {
	data, err := problemData(source, p)
	if err != nil {
		return "", err
	}
	open, err := q.db().ProblemOpenByCause(q.ctx(), p.Cause)
	if err == nil {
		var text *string
		if p.RefreshText {
			text = &p.Text
		}
		if p.Count > 0 {
			return open.ID, problemTally(q, open.ID, p, text, data)
		}
		return open.ID, q.db().ProblemSeen(q.ctx(), db.ProblemSeenParams{ID: open.ID, Text: text, Data: data})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	var owner *string
	if p.Owner != "" {
		owner = &p.Owner
	} else if !p.Unowned {
		if owner, err = problemOwner(q, p.ProjectID); err != nil {
			return "", err
		}
	}
	since := tsOf(new(time.Now().Add(-problemReopenHours * time.Hour)))
	id, err := q.db().ProblemRecentClosed(q.ctx(), db.ProblemRecentClosedParams{Cause: p.Cause, Since: since})
	if err == nil {
		return id, q.db().ProblemReopen(q.ctx(), db.ProblemReopenParams{ID: id, Text: p.Text, Data: data, OwnerBotID: owner})
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	id = newID()
	if err := q.db().ProblemInsert(q.ctx(), db.ProblemInsertParams{ID: id, Cause: p.Cause, Source: source, Rule: p.Rule,
		ProjectID: optStr(p.ProjectID), Text: p.Text, Data: data, OwnerBotID: owner}); err != nil || p.Count <= 0 {
		return id, err
	}
	return id, problemTally(q, id, p, nil, nil)
}

// problemTally: the source's own count and last occurrence on the problem.
func problemTally(q *Request, id string, p Problem, text *string, data []byte) error {
	seen := time.Now()
	if p.SeenAt != nil {
		seen = *p.SeenAt
	}
	return q.db().ProblemTally(q.ctx(), db.ProblemTallyParams{ID: id, Count: int32(min(p.Count, 1<<31-1)), SeenAt: tsOf(&seen), Text: text, Data: data})
}

// problemRaise: problemRaiseID without the id.
func problemRaise(q *Request, source string, p Problem) error {
	_, err := problemRaiseID(q, source, p)
	return err
}

// problemOpen: a one-off sender's cause (span writer, backup).
func problemOpen(q *Request, cause string, projectID any, text string) error {
	pid, _ := projectID.(string)
	return problemRaise(q, "direct", Problem{Cause: cause, Text: text, ProjectID: pid})
}

// problemClose: the cause is gone: its open problem closes as cleared.
func problemClose(q *Request, cause string) error {
	return problemsClose(q, []string{cause})
}

// problemsClose: problemClose for many causes.
func problemsClose(q *Request, causes []string) error {
	if len(causes) == 0 {
		return nil
	}
	return problemsCloseOf(q, db.ProblemsOpenOfParams{Causes: causes})
}

// problemsClosePrefix: problemClose for every open cause starting with prefix.
func problemsClosePrefix(q *Request, prefix string) error {
	return problemsCloseOf(q, db.ProblemsOpenOfParams{Prefix: &prefix})
}

func problemsCloseOf(q *Request, of db.ProblemsOpenOfParams) error {
	rows, err := q.db().ProblemsOpenOf(q.ctx(), of)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	return problemsCloseIDs(q, ids, "cleared")
}

func problemsCloseIDs(q *Request, ids []string, reason string) error {
	if len(ids) == 0 {
		return nil
	}
	return q.db().ProblemsClose(q.ctx(), db.ProblemsCloseParams{Ids: ids, Reason: reason})
}

// problemsSet: the full current set of the causes matching pattern (a regular expression), one pass of a source that
// sees all of them at once (dispatch, pins): an open cause no longer present closes at once, a new one opens.
func problemsSet(q *Request, source, pattern string, current []Problem) error {
	held, err := q.db().ProblemsOpenOf(q.ctx(), db.ProblemsOpenOfParams{Pattern: &pattern})
	if err != nil {
		return err
	}
	now, have, gone := map[string]bool{}, map[string]bool{}, []string{}
	for _, p := range current {
		now[p.Cause] = true
	}
	for _, h := range held {
		have[h.Cause] = true
		if !now[h.Cause] {
			gone = append(gone, h.ID)
		}
	}
	if err := problemsCloseIDs(q, gone, "cleared"); err != nil {
		return err
	}
	for _, p := range current {
		if !have[p.Cause] {
			have[p.Cause] = true
			if err := problemRaise(q, source, p); err != nil {
				return fmt.Errorf("problem %s: %w", p.Cause, err)
			}
		}
	}
	return nil
}

// problemsSync: the full current set of one source (rules, delivery, grants): new causes open; an open cause absent
// for problemCleanRuns runs in a row closes; a present one resets its miss count.
func problemsSync(q *Request, source string, current []Problem) error {
	now := map[string]Problem{}
	for _, p := range current {
		now[p.Cause] = p
	}
	held, err := q.db().ProblemsOpenOfSource(q.ctx(), source)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, h := range held {
		have[h.Cause] = true
		if p, present := now[h.Cause]; present {
			var text *string
			if p.RefreshText {
				text = &p.Text
			}
			if err := q.db().ProblemPresent(q.ctx(), db.ProblemPresentParams{ID: h.ID, Text: text}); err != nil {
				return err
			}
			continue
		}
		if int(h.Misses)+1 >= problemCleanRuns {
			if err := problemsCloseIDs(q, []string{h.ID}, "cleared"); err != nil {
				return err
			}
			continue
		}
		if err := q.db().ProblemMissed(q.ctx(), db.ProblemMissedParams{ID: h.ID, Misses: h.Misses + 1}); err != nil {
			return err
		}
	}
	for _, p := range current {
		if have[p.Cause] {
			continue
		}
		if err := problemRaise(q, source, p); err != nil {
			return fmt.Errorf("problem %s: %w", p.Cause, err)
		}
	}
	return nil
}

// problemDuty: one bot's problem duty.
type problemDuty struct {
	bot, project string
	system       bool
	projects     []string
}

func problemDuties(q *Request) ([]problemDuty, error) {
	rows, err := q.db().BotDuties(q.ctx())
	if err != nil {
		return nil, err
	}
	out := make([]problemDuty, 0, len(rows))
	for _, r := range rows {
		var d contract.BotDuty
		if json.Unmarshal(r.Duty, &d) != nil || d.Problems == nil {
			continue
		}
		pd := problemDuty{bot: r.ID, project: r.ProjectID, projects: d.Problems.Projects}
		if d.Problems.System != nil {
			pd.system = *d.Problems.System
		}
		out = append(out, pd)
	}
	return out, nil
}

// problemOwner: the bot whose duty names the project, else the first system duty bot; nil when no bot has a duty.
func problemOwner(q *Request, projectID string) (*string, error) {
	duties, err := problemDuties(q)
	if err != nil {
		return nil, err
	}
	if projectID != "" {
		for _, d := range duties {
			if slices.Contains(d.projects, projectID) {
				return &d.bot, nil
			}
		}
	}
	for _, d := range duties {
		if d.system {
			return &d.bot, nil
		}
	}
	return nil, nil
}

// problemsAdopt: a duty saved after problems opened: the bot takes the open problems without an owner that its duty
// names (a system duty: those without a project or of a project no other duty claims).
func problemsAdopt(q *Request, botID string, d *contract.BotDuty) error {
	if d == nil || d.Problems == nil {
		return nil
	}
	duties, err := problemDuties(q)
	if err != nil {
		return err
	}
	claimed := []string{}
	for _, o := range duties {
		if o.bot != botID {
			claimed = append(claimed, o.projects...)
		}
	}
	system := d.Problems.System != nil && *d.Problems.System
	return q.db().ProblemsSetOwner(q.ctx(), db.ProblemsSetOwnerParams{Bot: botID, System: system, Claimed: claimed,
		Projects: append([]string{}, d.Problems.Projects...)})
}

// Operations.

func init() {
	onStart(func(ctx context.Context, a *App) {
		every(ctx, "problems-watch", envMs("FLEET_PROBLEMS_WATCH_TICK_MS", 60_000), func(ctx context.Context) error {
			return a.store.Write(ctx, nil, []string{"problems:watch"}, problemsWatch)
		})
	})
	op("problems_list", problemsList)
	op("problem_get", problemGetOp)
	op("problem_update", problemUpdateOp)
	op("problem_escalate", problemEscalateOp)
	op("problem_talk", problemTalkOp)
}

func problemCode(seq int64) string { return "P-" + strconv.FormatInt(seq, 10) }

// problemFind: a problem by id, code (P-12) or open cause.
func problemFind(q *Request, id, cause string) (db.Problem, error) {
	var p db.Problem
	var err error
	switch {
	case cause != "":
		p, err = q.db().ProblemOpenGet(q.ctx(), cause)
	case strings.HasPrefix(strings.ToUpper(id), "P-"):
		n, perr := strconv.ParseInt(id[2:], 10, 64)
		if perr != nil {
			return p, fail(400, "Expected a problem code like P-12")
		}
		p, err = q.db().ProblemBySeq(q.ctx(), n)
	case id != "":
		p, err = q.db().ProblemGet(q.ctx(), id)
	default:
		return p, fail(400, "Expected id or cause")
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return p, errNotFound
	}
	return p, err
}

func problemOut(q *Request, p db.Problem) contract.ProblemRow {
	return problemOutNamed(q, p, map[string]*string{}, true)
}

// problemOutNamed: problemOut with the owners' names read once per call (names: bot id to name, nil when unknown);
// conversation: also look up the owner's problem conversation (one problem; a list leaves it out: the session read
// under its row policies costs most of a second a row on prod).
func problemOutNamed(q *Request, p db.Problem, names map[string]*string, conversation bool) contract.ProblemRow {
	out := contract.ProblemRow{Id: p.ID, Code: problemCode(p.Seq), Cause: p.Cause, Source: p.Source, ProjectId: p.ProjectID,
		Text: p.Text, Data: p.Data, State: contract.ProblemState(p.State), Phase: contract.ProblemPhase(p.Phase),
		OwnerBotId: p.OwnerBotID, Note: p.Note, Work: p.Work, QuestionId: p.QuestionID, Count: int64(p.Count),
		Reopened: int64(p.Reopened), OpenedAt: p.OpenedAt.UTC().Format(time.RFC3339Nano), SeenAt: p.SeenAt.UTC().Format(time.RFC3339Nano),
		PhaseAt: p.PhaseAt.UTC().Format(time.RFC3339Nano), ClosedAt: formatTimePtr(p.ClosedAt), CloseReason: p.CloseReason}
	if p.Rule != "" {
		out.Rule = &p.Rule
	}
	if p.OwnerBotID != nil {
		name, ok := names[*p.OwnerBotID]
		if !ok {
			if n, err := q.db().ProblemOwnerName(q.ctx(), *p.OwnerBotID); err == nil {
				name = &n
			}
			names[*p.OwnerBotID] = name
		}
		out.OwnerName = name
		if !conversation {
			return out
		}
		if id, err := q.db().BotConversationBySubject(q.ctx(), db.BotConversationBySubjectParams{BotID: p.OwnerBotID, Subject: strPtr(problemSubject(p.ID))}); err == nil {
			out.ConversationId = &id
		}
	}
	return out
}

// problemsSubject: the owner bot's conversation for its batches of problems (one message per pass).
const problemsSubject = "problems"

// problemSubject: the owner bot's conversation subject for one problem (a person's words, a long investigation).
func problemSubject(id string) string { return "problem:" + id }

func problemsList(q *Request, r *contract.ProblemsListRequest) (contract.ProblemsListReply, error) {
	out := contract.ProblemsListReply{Problems: []contract.ProblemRow{}}
	lim := 50
	if r.Limit != nil {
		if *r.Limit < 1 || *r.Limit > 200 {
			return out, fail(400, "Expected limit 1 to 200")
		}
		lim = int(*r.Limit)
	}
	state := ptr("open")
	if r.State != nil {
		state = ptr(string(*r.State))
		if *state == "all" {
			state = nil
		}
	}
	var phase *string
	if r.Phase != nil {
		phase = ptr(string(*r.Phase))
	}
	var before *int64
	if r.Before != nil {
		p, err := problemFind(q, *r.Before, "")
		if err != nil {
			return out, err
		}
		before = &p.Seq
	}
	rows, err := q.db().ProblemsList(q.ctx(), db.ProblemsListParams{State: state, Phase: phase, OwnerBotID: r.OwnerBotId,
		ProjectID: r.ProjectId, Before: before, Lim: int32(lim + 1)})
	if err != nil {
		return out, err
	}
	if len(rows) > lim {
		rows, out.HasMore = rows[:lim], true
	}
	names := map[string]*string{}
	for _, p := range rows {
		out.Problems = append(out.Problems, problemOutNamed(q, p, names, false))
	}
	return out, nil
}

func problemGetOp(q *Request, r *contract.ProblemGetRequest) (contract.ProblemGetReply, error) {
	p, err := problemFind(q, deref(r.Id), deref(r.Cause))
	if err != nil {
		return contract.ProblemGetReply{}, err
	}
	return contract.ProblemGetReply{Problem: problemOut(q, p)}, nil
}

// problemCaller: the bot and conversation of the agent run calling, empty for a person.
func problemCaller(q *Request) (bot, session string) {
	eid, ok := strings.CutPrefix(q.agent, "execution:")
	if !ok || eid == "" {
		return "", ""
	}
	e, err := getExecution(q, eid)
	if err != nil {
		return "", ""
	}
	bot, _ = q.db().ExecutionBotID(q.ctx(), eid)
	return bot, e.SessionID
}

// problemMayAct: the owner bot (its run), or a person with full access (the owner).
func problemMayAct(q *Request, p db.Problem) error {
	if bot, _ := problemCaller(q); bot != "" {
		if p.OwnerBotID != nil && *p.OwnerBotID == bot {
			return nil
		}
		return fail(403, "Only the problem's owner bot or the owner acts on it")
	}
	if q.agent == "" && fullAccess(q.person) {
		return nil
	}
	return fail(403, "Only the problem's owner bot or the owner acts on it")
}

func problemUpdateOp(q *Request, r *contract.ProblemUpdateRequest) (contract.ProblemUpdateReply, error) {
	p, err := problemFind(q, r.Id, "")
	if err != nil {
		return contract.ProblemUpdateReply{}, err
	}
	if err := problemMayAct(q, p); err != nil {
		return contract.ProblemUpdateReply{}, err
	}
	if p.State != "open" {
		return contract.ProblemUpdateReply{}, fail(409, "The problem is closed")
	}
	var phase *string
	if r.Phase != nil {
		phase = ptr(string(*r.Phase))
	}
	if phase != nil || r.Note != nil || r.Work != nil {
		if err := q.db().ProblemUpdate(q.ctx(), db.ProblemUpdateParams{ID: p.ID, Phase: phase, Note: r.Note, Work: r.Work}); err != nil {
			return contract.ProblemUpdateReply{}, err
		}
	}
	if r.Close != nil {
		reason := string(*r.Close)
		if reason == "duplicate" {
			if r.DuplicateOf == nil {
				return contract.ProblemUpdateReply{}, fail(400, "Expected duplicateOf with close duplicate")
			}
			other, err := problemFind(q, *r.DuplicateOf, "")
			if err != nil {
				return contract.ProblemUpdateReply{}, err
			}
			if other.ID == p.ID {
				return contract.ProblemUpdateReply{}, fail(400, "A problem is not a duplicate of itself")
			}
			note := "Duplicate of " + problemCode(other.Seq)
			if err := q.db().ProblemUpdate(q.ctx(), db.ProblemUpdateParams{ID: p.ID, Note: &note}); err != nil {
				return contract.ProblemUpdateReply{}, err
			}
		}
		if err := problemsCloseIDs(q, []string{p.ID}, reason); err != nil {
			return contract.ProblemUpdateReply{}, err
		}
		if err := audit(q, "problem:"+problemCode(p.Seq), "close", nil, reason, auditExtra{ProjectID: p.ProjectID}); err != nil {
			return contract.ProblemUpdateReply{}, err
		}
	}
	p, err = q.db().ProblemGet(q.ctx(), p.ID)
	return contract.ProblemUpdateReply{Problem: problemOut(q, p)}, err
}

func problemEscalateOp(q *Request, r *contract.ProblemEscalateRequest) (contract.ProblemEscalateReply, error) {
	p, err := problemFind(q, r.Id, "")
	if err != nil {
		return contract.ProblemEscalateReply{}, err
	}
	bot, session := problemCaller(q)
	if bot == "" || p.OwnerBotID == nil || *p.OwnerBotID != bot {
		return contract.ProblemEscalateReply{}, fail(403, "Only the problem's owner bot escalates it")
	}
	if p.State != "open" {
		return contract.ProblemEscalateReply{}, fail(409, "The problem is closed")
	}
	if p.QuestionID != nil {
		if open, err := q.db().ProblemQuestionOpen(q.ctx(), *p.QuestionID); err != nil {
			return contract.ProblemEscalateReply{}, err
		} else if open {
			return contract.ProblemEscalateReply{}, fail(409, "The problem already has an open escalation")
		}
	}
	if len(r.Options) > 4 {
		return contract.ProblemEscalateReply{}, fail(400, "Expected at most 4 options")
	}
	var options []byte
	if len(r.Options) > 0 {
		options = jsonBytes(r.Options)
	}
	by := actor(q, "fleet")
	data := jsonBytes(struct {
		ProblemID  string `json:"problemId"`
		Problem    string `json:"problem"`
		Escalation string `json:"escalation"`
	}{p.ID, problemCode(p.Seq), string(r.Kind)})
	text := fmt.Sprintf("%s (%s, %s)", strings.TrimSpace(r.Text), problemCode(p.Seq), escalationWords(string(r.Kind)))
	qid, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", Text: text, Options: options, AskedBy: &by,
		ProjectID: deref(p.ProjectID), SessionID: optStr(session), Data: data})
	if err != nil {
		return contract.ProblemEscalateReply{}, err
	}
	if err := q.db().ProblemUpdate(q.ctx(), db.ProblemUpdateParams{ID: p.ID, Phase: ptr("escalated"), QuestionID: &qid}); err != nil {
		return contract.ProblemEscalateReply{}, err
	}
	p, err = q.db().ProblemGet(q.ctx(), p.ID)
	return contract.ProblemEscalateReply{Problem: problemOut(q, p), QuestionId: qid}, err
}

// escalationWords: an escalation's kind in the question's words.
func escalationWords(kind string) string {
	switch kind {
	case "product":
		return "a product decision"
	case "authority":
		return "needs your authority"
	}
	return "only you can do this"
}

// problemChangePart: a changed problem's line for its owner bot's wake, in the problem's conversation; ok is false for
// another bot's problem or a closed one.
func problemChangePart(q *Request, id, bot string) (threadPart, bool, error) {
	p, err := q.db().ProblemGet(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return threadPart{}, false, nil
	}
	if err != nil || p.OwnerBotID == nil || *p.OwnerBotID != bot || p.State != "open" {
		return threadPart{}, false, err
	}
	// Idempotent: a problem the bot was woken about and that has not opened again or changed phase since is no news.
	if p.WokenAt != nil && !p.PhaseAt.After(*p.WokenAt) && !p.OpenedAt.After(*p.WokenAt) {
		return threadPart{}, false, nil
	}
	what := "new problem"
	switch {
	case p.Reopened > 0 && p.Phase == "new":
		what = "problem back"
	case p.Phase != "new":
		what = "problem " + p.Phase
	}
	if p.WokenAt == nil && p.Phase == "new" && p.Reopened == 0 {
		what = "problem not started yet"
	}
	// One batch, one conversation: every problem of a pass goes to the bot's problems conversation as one message.
	return threadPart{problemsSubject, fmt.Sprintf("%s %s ×%d (cause %s, phase %s): %s",
		what, problemCode(p.Seq), p.Count, p.Cause, p.Phase, cutRunes(p.Text, 300))}, true, nil
}

// problemAnswerAfter (runAnswerAfter): an answered escalation hands its problem back to the owner bot (phase working);
// the answer itself reaches the bot in the conversation that asked.
func problemAnswerAfter(q *Request, questionID string, data json.RawMessage) error {
	var d struct {
		ProblemID string `json:"problemId"`
	}
	if json.Unmarshal(data, &d) != nil || d.ProblemID == "" {
		return nil
	}
	p, err := q.db().ProblemGet(q.ctx(), d.ProblemID)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && (p.QuestionID == nil || *p.QuestionID != questionID || p.State != "open") {
		return nil
	}
	if err != nil {
		return err
	}
	return q.db().ProblemUpdate(q.ctx(), db.ProblemUpdateParams{ID: p.ID, Phase: ptr("working"), ClearQuestion: true})
}

// problemTalkOp: a person's message to the problem's owner bot, in the problem's conversation (created on the first
// message), with the problem's line first so the bot knows what it is about.
func problemTalkOp(q *Request, r *contract.ProblemTalkRequest) (contract.ProblemTalkReply, error) {
	var out contract.ProblemTalkReply
	text := strings.TrimSpace(r.Text)
	if text == "" {
		return out, fail(400, "Enter a message")
	}
	p, err := problemFind(q, r.Id, "")
	if err != nil {
		return out, err
	}
	if p.OwnerBotID == nil {
		return out, fail(409, "No bot owns this problem: give a bot the duty first")
	}
	b, err := readBotFull(q, *p.OwnerBotID)
	if err != nil {
		return out, err
	}
	if err := botDirects(q, b.BotRecord); err != nil {
		return out, err
	}
	dedup := ""
	if rid := strings.TrimSpace(deref(r.RequestId)); rid != "" {
		dedup = "problem:" + p.ID + ":" + rid
	}
	head := fmt.Sprintf("About %s ×%d (cause %s, phase %s): %s\n\n", problemCode(p.Seq), p.Count, p.Cause, p.Phase, cutRunes(p.Text, 400))
	_, sid, admitted, err := admitBotWake(q, &b, botWakeReq{Cause: "manual", DedupKey: dedup, Text: head + text, SentBy: director(q),
		Source: "problem", Thread: problemSubject(p.ID)})
	if err != nil {
		return out, err
	}
	return contract.ProblemTalkReply{Problem: problemOut(q, p), SessionId: sid, Admitted: admitted}, nil
}

const (
	dutyNewMinutes = 30 // duty.newMinutes
	dutyCause      = "duty:%s:down"
)

// problemsWatch: the watchdog (rule 6): a working problem past problems.stuckHours turns stuck; a duty bot whose
// problem trigger fails, or whose oldest new problem waits duty.newMinutes with no run of the bot since, is down: one
// problem without an owner and one only-you question to the owner, never one per waiting problem. Both close when the
// bot runs again.
func problemsWatch(q *Request) error {
	if err := q.db().ProblemsStale(q.ctx(), problemStuckHours); err != nil {
		return err
	}
	if err := botBreaker(q, time.Now()); err != nil {
		return err
	}
	if err := botFailures(q); err != nil {
		return err
	}
	bots, err := q.db().ProblemDutyHealth(q.ctx())
	if err != nil {
		return err
	}
	now := time.Now()
	for _, b := range bots {
		cause := fmt.Sprintf(dutyCause, b.ID)
		reason := ""
		switch {
		case b.Failing > 0:
			reason = "its triggers fail"
		case b.OldestNew.Valid && now.Sub(b.OldestNew.Time) > dutyNewMinutes*time.Minute && (!b.LastRun.Valid || b.LastRun.Time.Before(b.OldestNew.Time)):
			reason = fmt.Sprintf("it has not run for %s while problems wait", now.Sub(b.OldestNew.Time).Round(time.Minute))
		}
		if reason == "" {
			if err := dutyRecovered(q, cause); err != nil {
				return err
			}
			continue
		}
		text := fmt.Sprintf("%s is not working: %s. %d problems wait for it. A PM can fix it or take the problems duty: tell one on Home.", b.Name, reason, b.Waiting)
		// One down problem while it lasts: an open one only gets the current text, never one more occurrence a minute.
		p, err := q.db().ProblemOpenGet(q.ctx(), cause)
		if errors.Is(err, pgx.ErrNoRows) {
			var id string
			if id, err = problemRaiseID(q, "watchdog", Problem{Cause: cause, Text: text, Unowned: true}); err == nil {
				p, err = q.db().ProblemGet(q.ctx(), id)
			}
		} else if err == nil {
			err = q.db().ProblemPresent(q.ctx(), db.ProblemPresentParams{ID: p.ID, Text: &text})
		}
		if err != nil {
			return err
		}
		if p.QuestionID != nil {
			if open, err := q.db().ProblemQuestionOpen(q.ctx(), *p.QuestionID); err != nil {
				return err
			} else if open {
				continue
			}
		}
		fleet := "fleet"
		data := jsonBytes(struct {
			ProblemID  string `json:"problemId"`
			Problem    string `json:"problem"`
			Escalation string `json:"escalation"`
		}{p.ID, problemCode(p.Seq), "only-you"})
		qid, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", Text: text + " (" + problemCode(p.Seq) + ", only you can do this)", AskedBy: &fleet, Data: data})
		if err != nil {
			return err
		}
		if err := q.db().ProblemUpdate(q.ctx(), db.ProblemUpdateParams{ID: p.ID, Phase: ptr("escalated"), QuestionID: &qid}); err != nil {
			return err
		}
	}
	return nil
}

// dutyRecovered: the bot works again: its down problem closes and the question, if still open, is withdrawn.
func dutyRecovered(q *Request, cause string) error {
	p, err := q.db().ProblemOpenGet(q.ctx(), cause)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if p.QuestionID != nil {
		if err := questionsWithdraw(q, []string{*p.QuestionID}); err != nil {
			return err
		}
	}
	return problemsCloseIDs(q, []string{p.ID}, "cleared")
}

// problemTriggerParts: after the changes, the owner's problems it never started (adopted with a duty, or a wake that
// was lost) join its wake, one conversation each; and every problem in this wake counts one wake, so a problem woken
// problems.stuckWakes times with no update of its owner turns stuck (problem_update resets the count).
func problemTriggerParts(q *Request, bot string, seen, owned map[string]bool) ([]threadPart, error) {
	ids, err := q.db().ProblemsUnstarted(q.ctx(), bot)
	if err != nil {
		return nil, err
	}
	var parts []threadPart
	for _, id := range ids {
		if seen["problem"+id] {
			continue
		}
		seen["problem"+id] = true
		if part, ok, err := problemChangePart(q, id, bot); err != nil {
			return nil, err
		} else if ok {
			parts, owned[id] = append(parts, part), true
		}
	}
	woken := []string{}
	for k := range seen {
		if id, ok := strings.CutPrefix(k, "problem"); ok && owned[id] {
			woken = append(woken, id)
		}
	}
	if len(woken) > 0 {
		if err := q.db().ProblemQuietWake(q.ctx(), db.ProblemQuietWakeParams{Ids: woken, StuckWakes: problemStuckWakes}); err != nil {
			return nil, err
		}
	}
	if len(parts) > 0 || len(woken) > 0 {
		parts = append(parts, threadPart{problemsSubject, "Work these here: problem_get for the evidence, then problem_update (phase, note, work) or problem_escalate; open a problem's own conversation only for a long investigation."})
	}
	return parts, nil
}

const (
	breakerFactor      = 10.0 // breaker.factor
	breakerWindowMin   = 60   // breaker.window
	breakerFloor       = 20   // breaker.floor
	breakerCoolMinutes = 30   // breaker.coolMinutes
	burstCause         = "burst:%s"
)

// burstData: the breaker's state on its problem.
type burstData struct {
	Bot       string  `json:"bot"`
	Recent    int     `json:"recent"`
	Median    float64 `json:"median"`
	CoolMin   int     `json:"coolMinutes"`
	PausedAt  string  `json:"pausedAt,omitempty"`
	Probation string  `json:"probationUntil,omitempty"`
}

// botBreaker: the run-rate breaker (docs/designs/fleet-system-management.md, Run-rate breaker). A bot that started more
// than breakerFactor times its usual runs in the window (and at least breakerFloor) is paused and one problem without
// an owner names it; after the cool time the bot resumes on probation, and a trip within the window pauses it again
// for twice as long. A person who resumes it starts the probation too. The problem closes after a window under the
// threshold. No concurrency cap: only the rate trips it.
func botBreaker(q *Request, now time.Time) error {
	rates, err := q.db().BotRunRates(q.ctx(), breakerWindowMin)
	if err != nil {
		return err
	}
	for _, b := range rates {
		cause := fmt.Sprintf(burstCause, b.ID)
		open, err := q.db().ProblemOpenGet(q.ctx(), cause)
		var held *db.Problem
		var st burstData
		if err == nil {
			held = &open
			_ = json.Unmarshal(open.Data, &st)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		usual := max(b.Median, 1)
		recent := int(b.Recent)
		if held != nil && st.Probation != "" {
			// On probation only the runs since the resume count, not the burst it paused for.
			until, _ := time.Parse(time.RFC3339, st.Probation)
			since := until.Add(-breakerWindowMin * time.Minute)
			n, err := q.db().BotRunsSince(q.ctx(), db.BotRunsSinceParams{Bot: b.ID, Since: tsOf(&since)})
			if err != nil {
				return err
			}
			recent = int(n)
		}
		tripped := recent >= breakerFloor && float64(recent) > breakerFactor*usual
		ours := b.Paused && b.PausedBy == "fleet:burst"
		switch {
		case ours && held == nil:
			// Its burst problem went (closed by a person): the breaker's pause goes with it.
			if err := q.db().BotBreakerPause(q.ctx(), db.BotBreakerPauseParams{ID: b.ID, Paused: false}); err != nil {
				return err
			}
			if err := audit(q, "bot:"+b.ID, "paused", true, false, auditExtra{Reason: "burst problem closed"}); err != nil {
				return err
			}
		case tripped && !b.Paused:
			cool := breakerCoolMinutes
			if held != nil && st.Probation != "" {
				cool = max(st.CoolMin, breakerCoolMinutes) * 2 // tripped again on probation
			}
			if err := q.db().BotBreakerPause(q.ctx(), db.BotBreakerPauseParams{ID: b.ID, Paused: true}); err != nil {
				return err
			}
			if err := audit(q, "bot:"+b.ID, "paused", false, true, auditExtra{Reason: "burst"}); err != nil {
				return err
			}
			st = burstData{Bot: b.ID, Recent: recent, Median: b.Median, CoolMin: cool, PausedAt: now.UTC().Format(time.RFC3339)}
			text := fmt.Sprintf("%s started %d runs in the last hour, %.0f times its usual %.0f: Fleet paused it for %d minutes. Its running turns finish; resume it on its page or tell a PM.",
				b.Name, recent, float64(recent)/usual, usual, cool)
			if _, err := problemRaiseID(q, "breaker", Problem{Cause: cause, Text: text, RefreshText: true, Unowned: true, Data: st}); err != nil {
				return err
			}
		case held != nil && ours && st.PausedAt != "":
			at, _ := time.Parse(time.RFC3339, st.PausedAt)
			if now.Before(at.Add(time.Duration(st.CoolMin) * time.Minute)) {
				continue
			}
			if err := q.db().BotBreakerPause(q.ctx(), db.BotBreakerPauseParams{ID: b.ID, Paused: false}); err != nil {
				return err
			}
			if err := audit(q, "bot:"+b.ID, "paused", true, false, auditExtra{Reason: "burst probation"}); err != nil {
				return err
			}
			st.PausedAt, st.Probation = "", now.Add(breakerWindowMin*time.Minute).UTC().Format(time.RFC3339)
			if err := q.db().ProblemTally(q.ctx(), db.ProblemTallyParams{ID: held.ID, Count: held.Count, SeenAt: tsOf(&now), Data: jsonBytes(st)}); err != nil {
				return err
			}
		case held != nil && !b.Paused && st.Probation == "":
			// A person resumed it: probation from now.
			st.PausedAt, st.Probation = "", now.Add(breakerWindowMin*time.Minute).UTC().Format(time.RFC3339)
			if err := q.db().ProblemTally(q.ctx(), db.ProblemTallyParams{ID: held.ID, Count: held.Count, SeenAt: tsOf(&now), Data: jsonBytes(st)}); err != nil {
				return err
			}
		case held != nil && !b.Paused && st.Probation != "":
			until, _ := time.Parse(time.RFC3339, st.Probation)
			if now.After(until) && !tripped {
				if err := problemsCloseIDs(q, []string{held.ID}, "cleared"); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// botFailures: a bot's conversations whose latest run failed, by plain cause, are that bot's problems
// (run-failed:<bot>:<cause>, ×N), not cards on a person's Home; a cause gone closes its problem.
func botFailures(q *Request) error {
	rows, err := q.db().BotFailedSessions(q.ctx())
	if err != nil {
		return err
	}
	type key struct{ bot, cause string }
	agg := map[key]*Problem{}
	n := map[key]int{}
	order := []key{}
	for _, r := range rows {
		c := contract.PlainCause(r.Cause, "")
		k := key{r.BotID, c.Cause}
		if agg[k] == nil {
			agg[k] = &Problem{Cause: "run-failed:" + r.BotID + ":" + c.Cause, Owner: r.BotID, RefreshText: true}
			order = append(order, k)
		}
		n[k] += int(r.N)
		agg[k].Text = fmt.Sprintf("%s's conversations failed: %s (%d).", r.Name, strings.TrimSuffix(c.Text, "."), n[k])
	}
	current := make([]Problem, 0, len(order))
	for _, k := range order {
		current = append(current, *agg[k])
	}
	return problemsSet(q, "bot-failures", "^run-failed:", current)
}

var problemCodeRe = regexp.MustCompile(`\bP-(\d+)\b`)

// problemsMarkWoken: the problems a sent batch names (by code) are woken for their owner bot.
func problemsMarkWoken(q *Request, bot string, parts []string) error {
	var seqs []int64
	for _, p := range parts {
		for _, m := range problemCodeRe.FindAllStringSubmatch(p, -1) {
			if n, err := strconv.ParseInt(m[1], 10, 64); err == nil {
				seqs = append(seqs, n)
			}
		}
	}
	if len(seqs) == 0 {
		return nil
	}
	return q.db().ProblemsWokenBySeq(q.ctx(), db.ProblemsWokenBySeqParams{Seqs: seqs, Bot: bot})
}
