package main

// Navigation counters (docs/design-system/controls/nav-rail.md "Counters"): one read gives every section's counter for
// the caller, an agent's for its director. Each section counts the view its person setting navCounters names (a system
// preset or a saved view of that list; the section's default when unset or when the saved view is gone) with its list's
// own filters: taskWhere for Tasks, sessionWhere for Conversations, the delivery_status rows for Delivery, the
// artifact_list conditions for Results, hosts_list liveness for Hosts. With a project focus each count splits into the
// items in it and the rest; ignored projects are never counted, also when a saved view names one (OR-34).

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("nav_counts", navCounts) // the current grants, as search and the lists it counts
}

// navView: a view a counter can count; label says what is counted, in words for the accessible name.
type navView struct {
	name, label string
	states      []string
}

// navSystemViews: the system presets of each list section (OR-33), from contracts/vocabulary.json, the file the app's
// system-views.ts reads; the first named in navDefaults is the counter default. A preset with notState counts every
// state of its list except those, so a new state joins it. Results and Hosts have no saved views: one fixed counter
// each, which may only be turned off.
var navSystemViews = func() map[string][]navView {
	out := map[string][]navView{}
	for section, ps := range contract.Vocabulary.SystemViews {
		for _, p := range ps {
			states := p.State
			if p.NotState != nil {
				states = []string{}
				for _, s := range contract.Vocabulary.Lists[section].States {
					if !slices.Contains(p.NotState, s) {
						states = append(states, s)
					}
				}
			}
			out[section] = append(out[section], navView{p.Name, p.Label, states})
		}
	}
	return out
}()

var navDefaults = contract.Vocabulary.NavDefaults

// navCounterOff: the navCounters value that turns a section's counter off; a saved view named "-" cannot be counted.
const navCounterOff = "-"

// navSystemView: a section's system view by name.
func navSystemView(section, name string) (navView, bool) {
	i := slices.IndexFunc(navSystemViews[section], func(v navView) bool { return v.name == name })
	if i < 0 {
		return navView{}, false
	}
	return navSystemViews[section][i], true
}

// checkNavCounters: each key is a counted section; each value is empty (the default), off, a system view of that
// section, or a saved view of that list in the person's views after this change (views reads them).
func checkNavCounters(set map[string]string, views []contract.SavedView) error {
	for k, v := range set {
		if _, ok := navSystemViews[k]; !ok {
			return fail(400, "navCounters: "+k+" has no counter (tasks, conversations, delivery, results, hosts)")
		}
		if _, isSystem := navSystemView(k, v); v == "" || v == navCounterOff || isSystem {
			continue
		}
		if k == "results" || k == "hosts" || !slices.ContainsFunc(views, func(x contract.SavedView) bool { return x.List == k && x.Name == v }) {
			return fail(400, "navCounters: "+k+": no view "+v)
		}
	}
	return nil
}

// navScope: the person's focus without ignored projects, and the ignored ones; '-' is work outside projects.
type navScope struct{ focus, ignored []string }

func (s navScope) inFocus(project string) bool {
	if project == "" {
		project = "-"
	}
	return slices.Contains(s.focus, project)
}

func (s navScope) ignores(project string) bool {
	if project == "" {
		project = "-"
	}
	return slices.Contains(s.ignored, project)
}

// split: the count condition "in the focus" on col (always true without a focus) as an SQL expression with its parameter.
func (s navScope) split(p *[]any, col string) string {
	if len(s.focus) == 0 {
		return "true"
	}
	*p = append(*p, s.focus)
	return fmt.Sprintf("coalesce(%s,'-')=ANY($%d)", col, len(*p))
}

// navSaved: the fields of a saved view a counter reads (person settings views, checked by viewProblem when saved); a null
// project is work outside projects.
type navSaved struct {
	Q        string    `json:"q"`
	Project  []*string `json:"project"`
	State    []string  `json:"state"`
	Archived bool      `json:"archived"`
}

// navList: a saved view's filter as the list operation takes it (task_page, sessions_list): its states, its search and
// the projects it names; ignored projects are left out whatever it names. v is nil for a system view.
func navList(v *navSaved, states []string, s navScope) doc {
	f := doc{}
	if len(states) > 0 {
		st := []any{}
		for _, x := range states {
			st = append(st, x)
		}
		f["state"] = st
	}
	if v != nil && strings.TrimSpace(v.Q) != "" {
		f["search"] = v.Q
	}
	if v != nil && len(v.Project) > 0 {
		ps := []any{}
		for _, p := range v.Project {
			if p == nil {
				ps = append(ps, nil)
			} else {
				ps = append(ps, *p)
			}
		}
		f["projectId"] = ps
	}
	if len(s.ignored) > 0 {
		f["excludeProjectId"] = nullProjects(s.ignored)
	}
	if v != nil && v.Archived {
		f["archived"] = true
	}
	return f
}

// navSessionBase: the sessions as sessionBase gives them to sessionWhere without a search (id, code, project, archive,
// actor and the state of the latest execution).
var navSessionBase = `WITH x AS (SELECT d.id, d.code, d.project_id, d.archived_at, d.actor, ` + sessionState + ` AS state
 FROM session d)`

func splitCount(q *Request, sql string, p []any) (int64, int64, error) {
	var in, all int64
	err := q.tx.QueryRow(q.ctx(), sql, p...).Scan(&in, &all)
	return in, all - in, err
}

func navCounts(q *Request, _ *contract.NavCountsRequest) (contract.NavCountsReply, error) {
	out := contract.NavCountsReply{Sections: map[string]contract.NavCount{}}
	email, err := settingsEmail(q, nil)
	if err != nil {
		return out, err
	}
	ps, err := personSettings(q, email)
	if err != nil {
		return out, err
	}
	s := navScope{ignored: ps.Ignored}
	for _, f := range ps.Focus {
		if !slices.Contains(ps.Ignored, f) {
			s.focus = append(s.focus, f)
		}
	}
	out.Focused = len(s.focus) > 0
	for _, section := range []string{"tasks", "conversations", "delivery", "results", "hosts"} {
		name := ps.NavCounters[section]
		if name == navCounterOff {
			continue
		}
		// The counted view: the named system preset or saved view, else the default (a saved view since removed, or one that
		// no longer reads, counts it too).
		def, _ := navSystemView(section, navDefaults[section])
		view, _ := navSystemView(section, name)
		var saved *navSaved
		if view.name == "" && name != "" {
			var sv navSaved
			if i := slices.IndexFunc(ps.Views, func(x contract.SavedView) bool { return x.List == section && x.Name == name }); i >= 0 &&
				json.Unmarshal(jsonBytes(ps.Views[i].View), &sv) == nil {
				saved, view = &sv, navView{name: name, label: name, states: sv.State}
			}
		}
		if view.name == "" {
			view, saved = def, nil
		}
		// A saved view that names its projects is that list whatever the focus (withFocus in the app): one number, no split.
		sc := s
		if saved != nil && len(saved.Project) > 0 && section != "delivery" {
			sc = navScope{ignored: s.ignored}
		}
		in, outside, err := navCount(q, section, view, saved, sc)
		// A saved view the list filters no longer take (a state since renamed) is refused (400) before any statement runs:
		// the section counts its default rather than failing every counter. Any other failure fails the read.
		var refused *HTTPError
		if err != nil && saved != nil && errors.As(err, &refused) && refused.Code == 400 {
			view, sc = def, s
			in, outside, err = navCount(q, section, view, nil, sc)
		}
		if err != nil {
			return out, err
		}
		out.Sections[section] = contract.NavCount{View: view.name, Label: view.label, InFocus: in, Outside: outside, Split: len(sc.focus) > 0 && section != "hosts"}
	}
	return out, nil
}

// navCount: the items of one section's counter view in the focus and outside it.
func navCount(q *Request, section string, v navView, saved *navSaved, s navScope) (int64, int64, error) {
	switch section {
	case "tasks":
		p := []any{}
		c, err := taskWhere(navList(saved, v.states, s), &p, true)
		if err != nil {
			return 0, 0, err
		}
		return splitCount(q, "SELECT count(*) FILTER (WHERE "+s.split(&p, "t.project_id")+"),count(*) FROM task t"+where(c), p)
	case "conversations":
		p, f := []any{}, navList(saved, v.states, s)
		c, err := sessionWhere(f, &p, true)
		if err != nil {
			return 0, 0, err
		}
		// Without a search the count needs only each session's state: not sessionBase's message and activity scans,
		// which this read, repeated on execution changes, would run every few seconds.
		base := sessionBase
		if f["search"] == nil {
			base = navSessionBase
		}
		return splitCount(q, base+" SELECT count(*) FILTER (WHERE "+s.split(&p, "x.project_id")+"),count(*) FROM x"+where(c), p)
	case "delivery":
		return navDelivery(q, v, saved, s)
	case "results":
		// The Results list with since:1d: every result of the last 24 hours it shows, in any state (artifact_list).
		n, err := q.db().NavResultsCount(q.ctx(), db.NavResultsCountParams{Focus: nonNilStrs(s.focus), Since: time.Now().Add(-24 * time.Hour), Ignored: nonNilStrs(s.ignored)})
		return n.InFocus, n.Total - n.InFocus, err
	case "hosts":
		// Hosts have no project: one number. Offline is the Hosts list's own judgement: not seen within staleAfterMs.
		r, err := hostsListReply(q, false)
		if err != nil {
			return 0, 0, err
		}
		n, now := int64(0), float64(nowMs())
		for _, h := range r.Hosts {
			if h.LastSeenAt == nil || *h.LastSeenAt <= 0 || now-*h.LastSeenAt >= hostStaleMs {
				n++
			}
		}
		return n, 0, nil
	}
	return 0, 0, nil
}

// navDelivery: the pull requests of delivery_status in the view's stages and search, as the Delivery list shows them for
// the person's scope: in the focus is what the list shows with the focus, outside what only every project adds. A view's
// picked pace day (extra at) is not counted: the counter is the pull requests now.
func navDelivery(q *Request, v navView, saved *navSaved, s navScope) (int64, int64, error) {
	deliveryMu.Lock()
	last := deliveryLast
	deliveryMu.Unlock()
	full := fullAccess(q.person)
	may := func(p string) bool { return (full || mayAccessProjectID(q.person, p)) && !s.ignores(p) }
	text := ""
	if saved != nil {
		text = saved.Q
	}
	match := func(it contract.DeliveryItem) bool {
		return (len(v.states) == 0 || slices.Contains(v.states, string(it.Stage))) &&
			matchSearch(text, fmt.Sprintf("%s #%d %s", it.Title, it.Number, it.Repo), map[string]string{"state": string(it.Stage), "project": it.ProjectId})
	}
	// Two passes, as the list reads them: an item names the first listing project the caller sees, which the focus changes.
	count := func(may func(string) bool) (n int64) {
		for _, it := range deliveryReply(last, may).Items {
			if match(it) {
				n++
			}
		}
		return n
	}
	all := count(may)
	if len(s.focus) == 0 {
		return all, 0, nil
	}
	in := count(func(p string) bool { return may(p) && s.inFocus(p) })
	return in, max(0, all-in), nil
}

// matchSearch: the list search syntax for rows filtered in the browser (packages/ui/src/search.ts matchSearch): every
// word in text; agent, host and project terms contained in that field, state equal; a term for a field the row lacks
// matches nothing.
func matchSearch(query, text string, fields map[string]string) bool {
	s := parseSearchQuery(strings.ToLower(query))
	if !hasText(text, s.text) {
		return false
	}
	for k, want := range s.fields {
		got, ok := fields[k]
		if !ok || got == "" || (k == "state" && strings.ToLower(got) != want) || (k != "state" && !strings.Contains(strings.ToLower(got), want)) {
			return false
		}
	}
	return true
}
