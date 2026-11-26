package main

// Home view (home_view, docs/design-system/pages/home.md, docs/designs/fleet-data-layer.md): the Home screen in one
// read. It returns rows only for what Home shows as cards (open and failed sessions, open questions, the bots) and
// counts the rest here: the pulse's sessions done today, the quiet summary's done, merged and failed since the last
// visit, and the blocker chips grouped by cause and host. No finished session or run is sent to be counted.

import (
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

	// Questions: every open question is a card's question, oldest first; problems are a summary, never questions.
	qs, err := questionsList(q, "", "", "open", "", r.ProjectIds, r.ExcludeProjectIds, nil)
	if err != nil {
		return out, err
	}
	out.Questions = append(out.Questions, qs...)
	if out.Problems, err = homeProblems(q); err != nil {
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
		// The plain cause of its problem whatever its state: an open run with a binding problem names it too.
		b := contract.HomeBlocker{Kind: contract.HomeBlockerKindRun, Cause: "run", Text: "", HostId: e.HostID, At: at}
		if c := contract.PlainCause(deref(e.Problem), ""); c != nil {
			b.Cause, b.Text = c.Cause, c.Text
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

	// Pull requests merged since the visit: publishing_status's items (delivery's, then the publishing records), counted
	// here from memory without building the projects' publishing capabilities.
	_, allowed, err := publishingProjectFilter(q, nil, r.ProjectIds, r.ExcludeProjectIds)
	if err != nil {
		return out, err
	}
	out.Counts.MergedSince = homeMerged(allowed, since)
	return out, nil
}

// homeMerged: the pull requests of allowed projects merged since since, one per project, repository and number, as
// publishingStatus merges delivery's items with the publishing records (a record without a merge time keeps the item's).
func homeMerged(allowed map[string]bool, since time.Time) int64 {
	merged := map[string]string{}
	key := func(project, repo string, n int64) string {
		return project + "|" + strings.ToLower(repo) + "#" + itoa(int(n))
	}
	deliveryMu.Lock()
	last := deliveryLast
	deliveryMu.Unlock()
	for _, it := range deliveryReply(last, func(p string) bool { return allowed[p] }).Items {
		if allowed[it.ProjectId] {
			merged[key(it.ProjectId, it.Repo, it.Number)] = deref(it.Merged)
		}
	}
	pubMu.Lock()
	for _, rec := range pubRecords {
		if allowed[rec.ProjectId] && rec.Merged != nil {
			merged[key(rec.ProjectId, rec.Repo, rec.Number)] = *rec.Merged
		}
	}
	pubMu.Unlock()
	var n int64
	for _, m := range merged {
		if m != "" && textMs(m) >= since.UnixMilli() {
			n++
		}
	}
	return n
}

// homeDone: the succeeded sessions of the scope with activity since today and since since.
func homeDone(q *Request, f rowFilter, today, since time.Time) (int64, int64, error) {
	sql, p, err := sqlSessionDone(f, today, since)
	if err != nil {
		return 0, 0, err
	}
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

// homeProblems: the pulse line's problems: how many are open, who owns them, the stuck ones and those no bot owns.
func homeProblems(q *Request) (*contract.HomeProblems, error) {
	rows, err := q.db().ProblemsSummary(q.ctx())
	if err != nil {
		return nil, err
	}
	out := &contract.HomeProblems{Owners: []contract.HomeProblemOwner{}}
	by := map[string]int64{}
	for _, r := range rows {
		out.Open += int64(r.N)
		if r.Phase == "stuck" {
			out.Stuck += int64(r.N)
		}
		if r.Owner == "" {
			out.Unowned += int64(r.N)
			continue
		}
		by[r.Owner] += int64(r.N)
	}
	for id, n := range by {
		name := id
		if b, err := readBotFull(q, id); err == nil {
			name = b.Settings.Name
		}
		out.Owners = append(out.Owners, contract.HomeProblemOwner{BotId: id, Name: name, N: n})
	}
	sort.Slice(out.Owners, func(i, j int) bool {
		if out.Owners[i].N != out.Owners[j].N {
			return out.Owners[i].N > out.Owners[j].N
		}
		return out.Owners[i].Name < out.Owners[j].Name
	})
	if out.Stuck > 0 {
		ps, err := q.db().ProblemsList(q.ctx(), db.ProblemsListParams{State: ptr("open"), Phase: ptr("stuck"), Lim: 1})
		if err != nil {
			return nil, err
		}
		if len(ps) > 0 {
			out.FirstStuck = &contract.HomeProblemFirst{Code: problemCode(ps[0].Seq), Text: ps[0].Text}
		}
	}
	return out, nil
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
