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
)

func init() {
	gatedObjOp("nav_counts", true, typedOp(navCounts)) // the current grants, as search and the lists it counts
}

// navView: a view a counter can count; label says what is counted, in words for the accessible name.
type navView struct {
	name, label string
	states      []string
}

// navSystemViews: the system presets of each list section (OR-33), as the app shows them (app/src/system-views.ts; the
// outside test tests/go/nav-counts.test.ts compares the two); the first named in navDefaults is the counter default.
// Results and Hosts have no saved views: one fixed counter each, which may only be turned off.
var navSystemViews = map[string][]navView{
	"tasks":         {{"Needs me", "need me", []string{"failed", "waiting"}}, {"Running", "running", []string{"queued", "in_progress", "waiting", "held", "paused"}}},
	"conversations": {{"Needs me", "need me", []string{"failed", "waiting"}}, {"Running", "running", []string{"preparing", "running", "waiting"}}},
	"delivery":      {{"Needs me", "need me", deliveryFailed}, {"In flight", "in flight", deliveryInFlight()}, {"Installed", "installed", deliveryDone}},
	"results":       {{"Last 24 h", "new in 24 h", nil}},
	"hosts":         {{"Offline", "offline", nil}},
}

// Delivery stages that are done and failed; every other stage is in flight, so a new stage counts as in flight (as
// app/src/system-views.ts derives it from DELIVERY_STAGES).
var deliveryDone, deliveryFailed = []string{"installed", "merged"}, []string{"pr_failed", "mq_failed"}

func deliveryInFlight() []string {
	out := []string{}
	for _, s := range contract.DeliveryStageValues {
		if !slices.Contains(deliveryDone, string(s)) {
			out = append(out, string(s))
		}
	}
	return out
}

var navDefaults = map[string]string{"tasks": "Running", "conversations": "Running", "delivery": "In flight", "results": "Last 24 h", "hosts": "Offline"}

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

// navList: a saved view's filter as the list operation takes it (task_page, sessions_list): its states, its search and
// the projects it names; ignored projects are left out whatever it names.
func navList(v map[string]any, states []string, s navScope) doc {
	f := doc{}
	if len(states) > 0 {
		st := []any{}
		for _, x := range states {
			st = append(st, x)
		}
		f["state"] = st
	}
	if q, _ := v["q"].(string); strings.TrimSpace(q) != "" {
		f["search"] = q
	}
	if ps, _ := v["project"].([]any); len(ps) > 0 {
		f["projectId"] = ps
	}
	if len(s.ignored) > 0 {
		f["excludeProjectId"] = nullProjects(s.ignored)
	}
	if v["archived"] == true {
		f["archived"] = true
	}
	return f
}

// navSessionBase: the sessions as sessionBase gives them to sessionWhere without a search (id, code, data, project and
// the state of the latest execution).
const navSessionBase = `WITH x AS (SELECT d.id, d.code, d.data, d.project_id, ` + sessionState + ` AS state
 FROM document d WHERE d.kind='session')`

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
		var saved map[string]any
		if view.name == "" && name != "" {
			if i := slices.IndexFunc(ps.Views, func(x contract.SavedView) bool { return x.List == section && x.Name == name }); i >= 0 &&
				json.Unmarshal(jsonBytes(ps.Views[i].View), &saved) == nil {
				view = navView{name: name, label: name, states: strs(saved["state"])}
			}
		}
		if view.name == "" {
			view, saved = def, nil
		}
		// A saved view that names its projects is that list whatever the focus (withFocus in the app): one number, no split.
		sc := s
		if named, _ := saved["project"].([]any); len(named) > 0 && section != "delivery" {
			sc = navScope{ignored: s.ignored}
		}
		in, outside, err := navCount(q, section, view, saved, sc)
		// A saved view the list filters no longer take (a state since renamed) is refused (400) before any statement runs:
		// the section counts its default rather than failing every counter. Any other failure fails the read.
		var refused *HTTPError
		if err != nil && len(saved) > 0 && errors.As(err, &refused) && refused.Code == 400 {
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
func navCount(q *Request, section string, v navView, saved map[string]any, s navScope) (int64, int64, error) {
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
		p := []any{time.Now().Add(-24 * time.Hour)}
		c := projectNotIn([]string{"tombstoned_at IS NULL", "created_at>=$1"}, &p, "project_id", s.ignored)
		return splitCount(q, "SELECT count(*) FILTER (WHERE "+s.split(&p, "project_id")+"),count(*) FROM artifact"+where(c), p)
	case "hosts":
		// Hosts have no project: one number. Offline is the Hosts list's own judgement: not seen within staleAfterMs.
		r, err := hostsList(q, false)
		if err != nil {
			return 0, 0, err
		}
		n, now := int64(0), float64(nowMs())
		hs, _ := r.(doc)["hosts"].([]doc)
		for _, h := range hs {
			if seen := num(h["lastSeenAt"]); seen <= 0 || now-seen >= hostStaleMs {
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
func navDelivery(q *Request, v navView, saved map[string]any, s navScope) (int64, int64, error) {
	deliveryMu.Lock()
	last := deliveryLast
	deliveryMu.Unlock()
	full := fullAccess(q.person)
	may := func(p string) bool { return (full || mayAccessProjectID(q.person, p)) && !s.ignores(p) }
	text, _ := saved["q"].(string)
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
