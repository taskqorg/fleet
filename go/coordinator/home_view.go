package main

// Home view (home_view, docs/design-system/pages/home.md, docs/designs/fleet-data-layer.md): the Home screen in one
// read. It returns rows only for what Home shows as cards (open and failed sessions, open questions, the bots) and
// counts the rest here: the pulse's sessions done today, the quiet summary's done, merged and failed since the last
// visit, and the blocker chips grouped by cause and host. No finished session or run is sent to be counted.

import (
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	op("home_view", homeView)
}

// homeSessions: the open sessions and the failed ones (app: openStates('session') and failed), at most homeRows.
var homeSessionStates = []string{"idle", "preparing", "running", "waiting", "failed"}

const homeRows = 200

func homeView(q *Request, r *contract.HomeViewRequest) (contract.HomeViewReply, error) {
	out := contract.HomeViewReply{Sessions: []contract.SessionRow{}, Failures: []contract.HomeFailure{}, Questions: []contract.QuestionRow{},
		Blockers: []contract.HomeBlocker{}, Bots: []contract.BotListItem{}}
	if err := excludeCheck(r.ExcludeProjectIds); err != nil {
		return out, err
	}
	if slices.Contains(r.ProjectIds, "") {
		return out, fail(400, "projectIds: an empty id; '-' is work outside projects")
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	if r.DayStart != nil {
		dayStart = time.UnixMilli(*r.DayStart)
	}

	// The scope as a list filter ('-' is work outside projects).
	f := rowFilter{}
	if len(r.ProjectIds) > 0 {
		f.HasProjects = true
		f.Projects, f.ProjectsNull = projectSplit(r.ProjectIds)
	}
	if len(r.ExcludeProjectIds) > 0 {
		f.HasExclude, f.Exclude = true, r.ExcludeProjectIds
	}

	// The last Home visit: the quiet summary counts from it (from the day's start without one). The app records a visit
	// when the person leaves Home, so it stays put while Home is open and rereads.
	since := dayStart
	if q.person != nil {
		at, err := q.db().PersonVisitGet(q.ctx(), db.PersonVisitGetParams{Email: q.person.Email, Page: "home"})
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return out, err
		default:
			out.Counts.VisitAt = ptr(at.UnixMilli())
			since = at
		}
	}

	// Cards: the open and failed sessions, newest activity first.
	sf := f
	sf.States = homeSessionStates
	o, err := sessionOrderOf(ptr("none"), ptr("-activity"))
	if err != nil {
		return out, err
	}
	sql, p, err := sqlSessionPage(sf, o, nil, homeRows)
	if out.Sessions, err = sessionListRows(q, sql, p, err); err != nil {
		return out, err
	}
	failed := []string{}
	for _, s := range out.Sessions {
		if s.State == "failed" {
			failed = append(failed, s.Id)
		}
	}
	if len(failed) > 0 {
		rs, err := q.db().HomeSessionFailures(q.ctx(), failed)
		if err != nil {
			return out, err
		}
		for _, x := range rs {
			c := contract.PlainCause(x.Problem, "")
			if c == nil {
				continue
			}
			out.Failures = append(out.Failures, contract.HomeFailure{SessionId: x.SessionID, Cause: c.Cause, Text: c.Text, HostId: x.HostID, At: homeMs(x.At)})
		}
	}

	// Sessions done today and since the visit: counted here, never sent.
	if out.Counts.DoneToday, out.Counts.DoneSince, err = homeDone(q, f, dayStart, since); err != nil {
		return out, err
	}

	// Questions: a problem question is a blocker; any other one is a card's question, oldest first.
	qs, err := questionsList(q, "", "", "open", "", r.ProjectIds, r.ExcludeProjectIds, nil)
	if err != nil {
		return out, err
	}
	blockers := map[string]*contract.HomeBlocker{}
	add := func(key string, b contract.HomeBlocker) {
		if had := blockers[key]; had != nil {
			had.N++
			if b.At > had.At {
				had.At, had.Text = b.At, b.Text
			}
			return
		}
		b.N = 1
		blockers[key] = &b
	}
	for _, x := range qs {
		cause := questionCause(x)
		if !strings.HasPrefix(cause, "problem:") {
			out.Questions = append(out.Questions, x)
			continue
		}
		// A host problem names its host and kind (problem:host:owner-mac:saturated); the others group by family.
		parts := strings.Split(cause, ":")
		family := "unknown"
		if len(parts) > 1 {
			family = parts[1]
		}
		key, host := "problem:"+family, (*string)(nil)
		if family == "host" {
			key = strings.Join(parts[:min(4, len(parts))], ":")
			if len(parts) > 2 && parts[2] != "" {
				host = &parts[2]
			}
		}
		add(key, contract.HomeBlocker{Kind: contract.HomeBlockerKindProblem, Cause: key, Text: x.Text, HostId: host, At: textMs(x.AskedAt)})
	}
	sort.SliceStable(out.Questions, func(i, j int) bool { return textMs(out.Questions[i].AskedAt) < textMs(out.Questions[j].AskedAt) })

	// Failed runs that need a look (executions_list needsYou): blockers by plain cause and host, and the failures since
	// the visit.
	rp := db.TaskExecutionsPageParams{NeedsYou: true}
	rp.Projects, rp.ProjectsNull = projectSplit(r.ProjectIds)
	rp.Excluded, rp.ExcludeNull = projectSplit(r.ExcludeProjectIds)
	runs, err := q.db().TaskExecutionsPage(q.ctx(), rp)
	if err != nil {
		return out, err
	}
	for _, e := range runs {
		seen := e.StartedAt
		if e.LastSeenAt != nil {
			seen = e.LastSeenAt
		}
		at := homeMs(seen)
		if e.State == db.ExecutionStateFailed && at >= since.UnixMilli() {
			out.Counts.FailedSince++
		}
		b := contract.HomeBlocker{Kind: contract.HomeBlockerKindRun, Cause: "run", Text: "", HostId: e.HostID, At: at}
		if e.State == db.ExecutionStateFailed || e.State == db.ExecutionStateCancelled {
			if c := contract.PlainCause(deref(e.Problem), ""); c != nil {
				b.Cause, b.Text = c.Cause, c.Text
			}
		}
		add("run:"+b.Cause+":"+deref(e.HostID), b)
	}
	for _, b := range blockers {
		out.Blockers = append(out.Blockers, *b)
	}
	sort.Slice(out.Blockers, func(i, j int) bool {
		if out.Blockers[i].At != out.Blockers[j].At {
			return out.Blockers[i].At > out.Blockers[j].At
		}
		return out.Blockers[i].Cause+deref(out.Blockers[i].HostId) < out.Blockers[j].Cause+deref(out.Blockers[j].HostId)
	})

	bots, err := botList(q, &contract.BotListRequest{ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds})
	if err != nil {
		return out, err
	}
	out.Bots = bots.Bots

	// Pull requests merged since the visit: delivery's items, counted here.
	pub, err := publishingStatus(q, &contract.PublishingStatusRequest{ProjectIds: r.ProjectIds, ExcludeProjectIds: r.ExcludeProjectIds})
	if err != nil {
		return out, err
	}
	for _, it := range pub.Items {
		if it.Merged != nil && textMs(*it.Merged) >= since.UnixMilli() {
			out.Counts.MergedSince++
		}
	}
	return out, nil
}

// homeDone: the succeeded sessions of the scope with activity since today and since since.
func homeDone(q *Request, f rowFilter, today, since time.Time) (int64, int64, error) {
	p := []any{}
	f.States = []string{"succeeded"}
	c, err := sessionWhere(f, &p, true)
	if err != nil {
		return 0, 0, err
	}
	p = append(p, today, since)
	n, m := len(p)-1, len(p)
	c = append(c, "x.activity_at >= least($"+itoa(n)+"::timestamptz,$"+itoa(m)+"::timestamptz)")
	sql := `WITH x AS (SELECT d.id, d.project_id, d.archived_at, d.actor, coalesce(ss.state,'idle') AS state, greatest(d.updated_at,ss.activity_at) AS activity_at
 FROM session d LEFT JOIN session_summary ss ON ss.session_id=d.id)
 SELECT count(*) FILTER (WHERE x.activity_at >= $` + itoa(n) + `::timestamptz) AS today, count(*) FILTER (WHERE x.activity_at >= $` + itoa(m) + `::timestamptz) AS since FROM x WHERE ` + strings.Join(c, " AND ")
	type row struct {
		Today int64 `db:"today"`
		Since int64 `db:"since"`
	}
	rs, err := typedRows[row](q, sql, p)
	if err != nil || len(rs) == 0 {
		return 0, 0, err
	}
	return rs[0].Today, rs[0].Since, nil
}

// questionCause: a question row's cause as text ("" without one).
func questionCause(x contract.QuestionRow) string {
	var s string
	if len(x.Cause) > 0 && json.Unmarshal(x.Cause, &s) == nil {
		return s
	}
	return ""
}

// msOf: a nullable time as Unix milliseconds (0 without one).
func homeMs(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}

// textMs: a time as its JSON text (timeText) in Unix milliseconds (0 when it does not parse).
func textMs(s string) int64 {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return 0
	}
	return t.UnixMilli()
}
