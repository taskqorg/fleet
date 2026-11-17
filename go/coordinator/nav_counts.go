package main

// Navigation counters (docs/design-system/controls/nav-rail.md "Counters"): one read gives every section's counter for
// the caller, an agent's for its director. Each section counts the view its person setting navCounters names (a system
// preset or a saved view of that list; the section's default when unset or when the saved view is gone) with its list's
// own filters: taskWhere for Tasks, sessionWhere for Conversations, the delivery_status rows for Delivery, the
// artifact_list conditions for Results, hosts_list liveness for Hosts. With a project focus each count splits into the
// items in it and the rest; ignored projects are never counted, also when a saved view names one (OR-34).

import (
	"encoding/json"
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
	"delivery": {{"Needs me", "need me", []string{"pr_failed", "mq_failed"}},
		{"In flight", "in flight", []string{"pr_open", "pr_ci", "pr_failed", "mq_wait", "mq_ci", "mq_failed", "releasing", "installing"}},
		{"Installed", "installed", []string{"installed", "merged"}}},
	"results": {{"Last 24 h", "new in 24 h", nil}},
	"hosts":   {{"Offline", "offline", nil}},
}

var navDefaults = map[string]string{"tasks": "Running", "conversations": "Running", "delivery": "In flight", "results": "Last 24 h", "hosts": "Offline"}

// navCounterOff: the navCounters value that turns a section's counter off.
const navCounterOff = "-"

// checkNavCounters: each key is a counted section; each value is empty (the default), off, a system view of that
// section, or a saved view of that list in the person's views after this change (views reads them).
func checkNavCounters(set map[string]string, views []contract.SavedView) error {
	for k, v := range set {
		sys, ok := navSystemViews[k]
		if !ok {
			return fail(400, "navCounters: "+k+" has no counter (tasks, conversations, delivery, results, hosts)")
		}
		if v == "" || v == navCounterOff || slices.ContainsFunc(sys, func(x navView) bool { return x.name == v }) {
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
		// The counted view: the named system preset or saved view, else the default (a saved view since removed counts it too).
		view, saved := navView{}, map[string]any{}
		for _, v := range navSystemViews[section] {
			if v.name == name {
				view = v
			}
		}
		if view.name == "" && name != "" {
			if i := slices.IndexFunc(ps.Views, func(x contract.SavedView) bool { return x.List == section && x.Name == name }); i >= 0 {
				saved = map[string]any{}
				_ = json.Unmarshal(jsonBytes(ps.Views[i].View), &saved) // checked by checkView when it was saved
				view = navView{name: name, label: name, states: strs(saved["state"])}
			}
		}
		if view.name == "" {
			for _, v := range navSystemViews[section] {
				if v.name == navDefaults[section] {
					view = v
				}
			}
		}
		in, outside, err := navCount(q, section, view, saved, s)
		if err != nil {
			return out, err
		}
		out.Sections[section] = contract.NavCount{View: view.name, Label: view.label, InFocus: in, Outside: outside}
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
		p := []any{}
		c, err := sessionWhere(navList(saved, v.states, s), &p, true)
		if err != nil {
			return 0, 0, err
		}
		return splitCount(q, sessionBase+" SELECT count(*) FILTER (WHERE "+s.split(&p, "x.project_id")+"),count(*) FROM x"+where(c), p)
	case "delivery":
		return navDelivery(q, v, saved, s)
	case "results":
		// The Results list with since:1d: stored, pending and failed results of the last 24 hours (artifact_list).
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
// the person's scope: in the focus is what the list shows with the focus, outside what only every project adds.
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
	focused := map[string]bool{}
	if len(s.focus) > 0 {
		for _, it := range deliveryReply(last, func(p string) bool { return may(p) && s.inFocus(p) }).Items {
			if match(it) {
				focused[fmt.Sprintf("%s#%d", strings.ToLower(it.Repo), it.Number)] = true
			}
		}
	}
	all := int64(0)
	for _, it := range deliveryReply(last, may).Items {
		if match(it) {
			all++
		}
	}
	if len(s.focus) == 0 {
		return all, 0, nil
	}
	in := int64(len(focused))
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
