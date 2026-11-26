package main

// Agent gap findings (docs/designs/fleet-telemetry.md#10-agent-gaps): a class above the finding threshold of
// config/agent-gaps.json opens a problem (cause gap:<class>, source agent-gaps). Investigate starts a PM conversation
// with the evidence; agent_gaps_link names the pull request that changes the class's setting; once that release runs
// on the coordinator and every host (the change point), the finding compares the class's failures per agent run over
// the same window before and after, and closes as fixed or asks again as still failing.
// agentGapFindingTransition is the only writer of agent_gap_finding.state (S5).

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5/pgtype"
)

func init() {
	op("agent_gaps_link", agentGapsLink)
}

const gapSource = "agent-gaps"

var agentGapFindingTransitions = map[string][]string{
	"open":          {"investigating", "dismissed", "linked"},
	"investigating": {"linked", "dismissed"},
	"linked":        {"watching", "investigating"},
	"watching":      {"fixed", "still-failing", "linked"},
	"still-failing": {"investigating", "linked", "dismissed"},
	"fixed":         nil,
	"dismissed":     nil,
}

// gapFindingRules: the finding section of config/agent-gaps.json.
type gapFindingRules struct {
	Days          int     `json:"days"`
	MinCount      int64   `json:"minCount"`
	MinExecutions int64   `json:"minExecutions"`
	WatchDays     int     `json:"watchDays"`
	FixedRatio    float64 `json:"fixedRatio"`
}

func (f gapFindingRules) valid() error {
	if f.Days < 1 || f.Days > gapMaxDays || f.MinCount < 1 || f.MinExecutions < 1 || f.WatchDays < 1 || f.WatchDays > gapMaxDays || f.FixedRatio < 0 || f.FixedRatio >= 1 {
		return fmt.Errorf("agent-gaps.json finding: days and watchDays 1 to %d, minCount and minExecutions at least 1, fixedRatio in [0, 1)", gapMaxDays)
	}
	return nil
}

// gapFinding: one agent_gap_finding row.
type gapFinding db.AgentGapFindingsRow

func gapCause(class string) string { return "gap:" + class }

func tsOf(t *time.Time) (p pgtype.Timestamptz) {
	if t != nil {
		p = gapTime(*t)
	}
	return
}

// agentGapFindingTransition writes f moved to state to (f.State is the stored state, "" for a new finding); a move the
// table does not allow is a 409.
func agentGapFindingTransition(q *Request, f gapFinding, to string) (gapFinding, error) {
	if f.State != "" && f.State != to && !slices.Contains(agentGapFindingTransitions[f.State], to) {
		return f, fail(409, fmt.Sprintf("finding %s cannot move from %s to %s", f.Class, f.State, to))
	}
	if f.State == "" && to != "open" {
		return f, fail(409, "a finding starts open")
	}
	f.State = to
	if f.OpenedAt.IsZero() {
		f.OpenedAt = time.Now()
	}
	err := q.db().AgentGapFindingPut(q.ctx(), db.AgentGapFindingPutParams{Class: f.Class, ProjectID: ciProject, State: f.State, QuestionID: f.QuestionID, SessionID: f.SessionID,
		Pr: f.Pr, PrUrl: f.PrUrl, Release: f.Release, ChangeAt: tsOf(f.ChangeAt), BeforeCount: f.BeforeCount, AfterCount: f.AfterCount,
		BeforeRuns: f.BeforeRuns, AfterRuns: f.AfterRuns, VerdictAt: tsOf(f.VerdictAt), Note: f.Note, OpenedAt: gapTime(f.OpenedAt)})
	return f, err
}

// agentGapFindingDelete: a finding whose class went quiet before anyone acted, or a fixed one whose class came back.
func agentGapFindingDelete(q *Request, class string) error {
	return q.db().AgentGapFindingDelete(q.ctx(), class)
}

func agentGapFindingOf(q *Request, class string) (*gapFinding, error) {
	fs, err := q.db().AgentGapFindings(q.ctx())
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		if f.Class == class {
			g := gapFinding(f)
			return &g, nil
		}
	}
	return nil, nil
}

// gapProblemText: the question's text, what agents lack and where the fix goes.
func gapProblemText(c contract.AgentGapClass, days int, prefix string) string {
	hosts := []string{}
	for _, h := range c.Hosts {
		if len(hosts) < 3 {
			hosts = append(hosts, h.Key)
		}
	}
	return fmt.Sprintf("%sAgents lack something: %s (%s), %d failures in %d runs over %d days (hosts %s). Setting: %s. %s",
		prefix, c.Class, c.Title, c.Count, c.Executions, days, strings.Join(hosts, ", "), c.Setting.Ref, c.Setting.Text)
}

type gapProblemData struct {
	Class      string   `json:"class"`
	Count      int64    `json:"count"`
	Executions []string `json:"executions"`
}

// askGapProblem asks the finding's question (anew: an earlier question of the cause lets go first) and returns its id.
func askGapProblem(q *Request, c contract.AgentGapClass, text string) (*string, error) {
	cause := gapCause(c.Class)
	if err := problemClose(q, cause); err != nil {
		return nil, err
	}
	ex := []string{}
	for _, e := range c.Examples {
		ex = append(ex, e.ExecutionId)
	}
	if err := problemRaise(q, gapSource, Problem{Cause: cause, Text: text, Rule: "agent-gap", Data: gapProblemData{c.Class, c.Count, ex}}); err != nil {
		return nil, err
	}
	h, err := problemHeld(q, cause)
	if err != nil || h == nil {
		return nil, err
	}
	return &h.ID, nil
}

// agentGapFindingsPass: the finding half of the agent-gaps pass, one transaction.
func agentGapFindingsPass(q *Request, rules *gapRules, now time.Time) error {
	fr := rules.Finding
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -(fr.Days - 1))
	rows, err := q.db().AgentGapRows(q.ctx(), db.AgentGapRowsParams{Since: gapTime(from), Until: gapTime(now.Add(time.Minute))})
	if err != nil {
		return err
	}
	reply := agentGapsReplyOf(rules, rows, &contract.AgentGapsRequest{}, nil, from, now, fr.Days)
	classes := map[string]contract.AgentGapClass{}
	for _, c := range reply.Classes {
		classes[c.Class] = c
	}
	above := func(c contract.AgentGapClass) bool { return c.AboveThreshold != nil && *c.AboveThreshold }
	stored, err := q.db().AgentGapFindings(q.ctx())
	if err != nil {
		return err
	}
	findings := map[string]gapFinding{}
	for _, f := range stored {
		findings[f.Class] = gapFinding(f)
	}
	// New classes above the threshold, and fixed classes that came back.
	for _, c := range reply.Classes {
		if !above(c) {
			continue
		}
		f, held := findings[c.Class]
		prefix := ""
		if held && f.State == "fixed" {
			n, err := q.db().AgentGapClassCount(q.ctx(), db.AgentGapClassCountParams{Class: c.Class, Since: gapTime(*f.VerdictAt), Until: gapTime(now.Add(time.Minute))})
			if err != nil {
				return err
			}
			if int64(n) < fr.MinCount {
				continue
			}
			prefix = fmt.Sprintf("Back after its fix (#%d). ", deref64(f.Pr))
			if err := agentGapFindingDelete(q, c.Class); err != nil {
				return err
			}
		} else if held {
			continue
		}
		qid, err := askGapProblem(q, c, gapProblemText(c, fr.Days, prefix))
		if err != nil {
			return err
		}
		if _, err := agentGapFindingTransition(q, gapFinding{Class: c.Class, QuestionID: qid, Note: strings.TrimSpace(prefix)}, "open"); err != nil {
			return err
		}
	}
	hosts, err := deliveryHostsSeen(q)
	if err != nil {
		return err
	}
	var prs []contract.DeliveryPr
	for _, f := range findings {
		c, present := classes[f.Class]
		switch f.State {
		case "open", "dismissed":
			if present && above(c) {
				continue
			}
			if err := problemClose(q, gapCause(f.Class)); err != nil {
				return err
			}
			if err := agentGapFindingDelete(q, f.Class); err != nil {
				return err
			}
		case "linked":
			if prs == nil {
				if prs, err = dhLoad[contract.DeliveryPr](q, "delivery-pr"); err != nil {
					return err
				}
			}
			at, release, waiting, ok := gapChangePoint(prs, deref64(f.Pr), hosts, now)
			if !ok {
				if note := gapWaitNote(f, waiting); note != f.Note {
					f.Note = note
					if _, err := agentGapFindingTransition(q, f, "linked"); err != nil {
						return err
					}
				}
				continue
			}
			f.ChangeAt, f.Release, f.Note = &at, release, ""
			if _, err := agentGapFindingTransition(q, f, "watching"); err != nil {
				return err
			}
		case "watching":
			if err := gapVerdict(q, rules, f, c, now); err != nil {
				return err
			}
		}
	}
	return nil
}

func deref64(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

// gapWaitNote: what a linked finding waits for, shown on the finding.
func gapWaitNote(f gapFinding, waiting []string) string {
	if len(waiting) == 0 {
		return fmt.Sprintf("Waiting for #%d to merge and release.", deref64(f.Pr))
	}
	return fmt.Sprintf("Waiting for #%d's release on: %s.", deref64(f.Pr), strings.Join(waiting, ", "))
}

// gapChangePoint: when pull request pr's release ran on the coordinator and every host seen in the last hour (the
// latest of those first runs); ok is false while it does not run everywhere yet, with the hosts it lacks (none while
// the pull request is not merged and released).
func gapChangePoint(prs []contract.DeliveryPr, pr int64, hosts []db.DeliveryHostsSeenRow, now time.Time) (time.Time, *string, []string, bool) {
	i := slices.IndexFunc(prs, func(p contract.DeliveryPr) bool { return p.Number == pr })
	if i < 0 || prs[i].Merged == "" || prs[i].Release == nil {
		return time.Time{}, nil, nil, false
	}
	p := prs[i]
	need := []string{"coordinator"}
	for _, h := range hosts {
		if h.Version != "" && now.UnixMilli()-int64(h.LastSeenMs) <= 3600_000 {
			need = append(need, h.ID)
		}
	}
	var last time.Time
	var waiting []string
	for _, id := range need {
		t, err := time.Parse(time.RFC3339, p.Installed[id])
		if err != nil {
			waiting = append(waiting, id)
			continue
		}
		if t.After(last) {
			last = t
		}
	}
	if len(waiting) > 0 {
		return time.Time{}, nil, waiting, false
	}
	return last.UTC(), p.Release, nil, true
}

// gapVerdict: once watchDays passed since the change point, the class's failures per agent run after it against the
// same span before it.
func gapVerdict(q *Request, rules *gapRules, f gapFinding, c contract.AgentGapClass, now time.Time) error {
	fr := rules.Finding
	w := time.Duration(fr.WatchDays) * 24 * time.Hour
	if f.ChangeAt == nil || now.Before(f.ChangeAt.Add(w)) {
		return nil
	}
	at := *f.ChangeAt
	count := func(since, until time.Time) (int32, int32, error) {
		n, err := q.db().AgentGapClassCount(q.ctx(), db.AgentGapClassCountParams{Class: f.Class, Since: gapTime(since), Until: gapTime(until)})
		if err != nil {
			return 0, 0, err
		}
		runs, err := q.db().AgentGapRunCount(q.ctx(), db.AgentGapRunCountParams{Since: gapTime(since), Until: gapTime(until)})
		return int32(n), int32(runs), err
	}
	bc, br, err := count(at.Add(-w), at)
	if err != nil {
		return err
	}
	ac, ar, err := count(at, at.Add(w))
	if err != nil {
		return err
	}
	f.BeforeCount, f.BeforeRuns, f.AfterCount, f.AfterRuns, f.VerdictAt = &bc, &br, &ac, &ar, &now
	if gapFixed(bc, br, ac, ar, fr.FixedRatio) {
		if err := problemClose(q, gapCause(f.Class)); err != nil {
			return err
		}
		_, err := agentGapFindingTransition(q, f, "fixed")
		return err
	}
	if c.Class == "" {
		c = contract.AgentGapClass{Class: f.Class, Title: rules.Families[gapFamilyOf(f.Class)].Title, Setting: rules.Families[gapFamilyOf(f.Class)].Setting, Count: int64(ac), Executions: int64(ar)}
	}
	text := fmt.Sprintf("Still failing after #%d (on every host since %s): %d failures in %d runs before, %d in %d runs after. ", deref64(f.Pr), at.Format("2006-01-02 15:04 MST"), bc, br, ac, ar)
	qid, err := askGapProblem(q, c, text+gapProblemText(c, rules.Finding.Days, ""))
	if err != nil {
		return err
	}
	f.QuestionID = qid
	_, err = agentGapFindingTransition(q, f, "still-failing")
	return err
}

// gapFixed: the rate after is at most ratio of the rate before (no failures after, when there were none before).
func gapFixed(beforeCount, beforeRuns, afterCount, afterRuns int32, ratio float64) bool {
	if afterCount == 0 {
		return true
	}
	if beforeCount == 0 || afterRuns == 0 {
		return false
	}
	return float64(afterCount)/float64(afterRuns) <= ratio*float64(beforeCount)/float64(max(beforeRuns, 1))
}

// agentGapAnswerAfter (runAnswerAfter): investigate starts the PM conversation, dismiss sets the finding aside.
func agentGapAnswerAfter(q *Request, id string) error {
	qu, err := q.db().AgentGapQuestion(q.ctx(), id)
	if err != nil || qu.Source != gapSource || qu.Class == "" {
		return nil
	}
	if err := acquireTxLock(q, "agent-gaps"); err != nil {
		return err
	}
	f, err := agentGapFindingOf(q, qu.Class)
	if err != nil || f == nil || deref(f.QuestionID) != id {
		return err
	}
	switch qu.Answer {
	case "dismiss":
		_, err := agentGapFindingTransition(q, *f, "dismissed")
		return err
	case "investigate":
		sid, err := startGapInvestigation(q, *f, id)
		if err != nil {
			f.Note = cutRunes("Investigate could not start: "+err.Error(), 300)
		} else {
			f.SessionID, f.Note = &sid, ""
		}
		to := "investigating"
		if f.State == "linked" {
			to = f.State
		}
		_, err = agentGapFindingTransition(q, *f, to)
		return err
	}
	return nil
}

// startGapInvestigation starts the PM conversation (preset investigate, project fleet) as the person who answered,
// in a savepoint: a refused start leaves the answer and notes why.
func startGapInvestigation(q *Request, f gapFinding, questionID string) (string, error) {
	rules, err := loadGapRules()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	from := today.AddDate(0, 0, -(rules.Finding.Days - 1))
	rows, err := q.db().AgentGapRows(q.ctx(), db.AgentGapRowsParams{Since: gapTime(from), Until: gapTime(now.Add(time.Minute))})
	if err != nil {
		return "", err
	}
	reply := agentGapsReplyOf(rules, rows, &contract.AgentGapsRequest{Class: &f.Class}, nil, from, now, rules.Finding.Days)
	text := gapInvestigationText(f.Class, reply, rules.Finding.Days)
	r := contract.SessionStartRequest{ProjectId: ptr(ciProject), Preset: ptr("investigate"), RequestId: ptr("agent-gap:" + questionID), Text: &text,
		Subject: ptr("Agent gap " + f.Class)}
	body := q.body
	q.body = jsonBytes(r)
	defer func() { q.body = body }()
	if err := exec(q, "SAVEPOINT agent_gap_investigate"); err != nil {
		return "", err
	}
	started, err := sessionStart(q, &r)
	if err != nil {
		if e := exec(q, "ROLLBACK TO SAVEPOINT agent_gap_investigate"); e != nil {
			return "", e
		}
		return "", err
	}
	return started.SessionId, exec(q, "RELEASE SAVEPOINT agent_gap_investigate")
}

// gapInvestigationText: the PM's first message: the evidence, the setting, and what to hand back.
func gapInvestigationText(class string, reply contract.AgentGapsReply, days int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Agent gap finding %s: what agents lack, from Fleet's own telemetry (agent_gaps).\n\n", class)
	for _, c := range reply.Classes {
		if c.Class != class {
			continue
		}
		fmt.Fprintf(&b, "%s: %d failures in %d agent runs over %d days (%d the %d days before).\n", c.Title, c.Count, c.Executions, days, c.PreviousCount, days)
		for _, l := range []struct {
			name string
			xs   []contract.AgentGapCount
		}{{"Hosts", c.Hosts}, {"Harnesses", c.Harnesses}, {"Profiles", c.Profiles}, {"Tools", c.Tools}, {"Projects", c.Projects}} {
			parts := []string{}
			for _, x := range l.xs {
				parts = append(parts, fmt.Sprintf("%s %d", x.Key, x.Count))
			}
			fmt.Fprintf(&b, "%s: %s\n", l.name, strings.Join(parts, ", "))
		}
		b.WriteString("\nExamples (execution, decisive line):\n")
		for _, e := range c.Examples {
			fmt.Fprintf(&b, "- %s %s %s: %s\n", e.ExecutionId, e.At, e.Tool, e.Line)
		}
		fmt.Fprintf(&b, "\nThe setting that controls it: %s (%s). %s\n", c.Setting.Ref, c.Setting.Kind, c.Setting.Text)
	}
	fmt.Fprintf(&b, "\nFind the cause from these executions (execution_events, telemetry_trace) and the setting. The fix goes into that setting, for every future agent, through a pull request in taskqorg/fleet (delegate the change to a worker). "+
		"Then call agent_gaps_link {\"class\": %q, \"pr\": <number>}: Fleet watches the class after the release reaches every host and closes the finding as fixed, or asks again. Read agent_gaps {\"class\": %q} for the current numbers.", class, class)
	return b.String()
}

func agentGapsLink(q *Request, r *contract.AgentGapsLinkRequest) (contract.AgentGapsLinkReply, error) {
	var none contract.AgentGapsLinkReply
	if r.Pr < 1 {
		return none, fail(400, "pr: a pull request number")
	}
	if err := acquireTxLock(q, "agent-gaps"); err != nil { // the findings pass writes whole findings under this lock
		return none, err
	}
	f, err := agentGapFindingOf(q, r.Class)
	if err != nil {
		return none, err
	}
	if f == nil {
		return none, errNotFound
	}
	prs, err := dhLoad[contract.DeliveryPr](q, "delivery-pr")
	if err != nil {
		return none, err
	}
	pr := r.Pr
	url := fmt.Sprintf("https://github.com/%s/%s/pull/%d", ciOwner, ciRepo, pr)
	if i := slices.IndexFunc(prs, func(p contract.DeliveryPr) bool { return p.Number == pr }); i >= 0 && prs[i].Url != "" {
		url = prs[i].Url
	}
	f.Pr, f.PrUrl, f.Release, f.ChangeAt = &pr, &url, nil, nil
	f.BeforeCount, f.BeforeRuns, f.AfterCount, f.AfterRuns, f.VerdictAt = nil, nil, nil, nil, nil
	g, err := agentGapFindingTransition(q, *f, "linked")
	if err != nil {
		return none, err
	}
	return contract.AgentGapsLinkReply{Finding: findingOut(g)}, nil
}

func findingOut(f gapFinding) contract.AgentGapFinding {
	t := func(p *time.Time) *string {
		if p == nil {
			return nil
		}
		return ptr(p.UTC().Format(time.RFC3339))
	}
	i := func(p *int32) *int64 {
		if p == nil {
			return nil
		}
		return ptr(int64(*p))
	}
	return contract.AgentGapFinding{Class: f.Class, State: contract.AgentGapFindingState(f.State), QuestionId: f.QuestionID, SessionId: f.SessionID, Pr: f.Pr,
		PrUrl: f.PrUrl, Release: f.Release, ChangeAt: t(f.ChangeAt), BeforeCount: i(f.BeforeCount), BeforeRuns: i(f.BeforeRuns), AfterCount: i(f.AfterCount),
		AfterRuns: i(f.AfterRuns), VerdictAt: t(f.VerdictAt), Note: optStr(f.Note), OpenedAt: f.OpenedAt.UTC().Format(time.RFC3339), UpdatedAt: f.UpdatedAt.UTC().Format(time.RFC3339)}
}
