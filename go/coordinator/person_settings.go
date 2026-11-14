package main

// Person settings (fleet-people-access.md section 8 "Dispatch"): order list | soonest_reset on the director, default
// list. The same document holds the person's app preferences: pins, saved views, focus and focus presets, column widths
// and row pins (fleet-people-access.md section 8 "Preferences"). One person-setting document per email; each change writes setting-change.

import (
	"encoding/json"
	"fleet/contract"
	"maps"
	"slices"
	"sort"
	"strings"
)

func init() {
	operation("person_settings_get", true, typedOp(personSettingsGet))
	operation("person_settings_set", false, typedOp(personSettingsSet))
}

// settingsEmail: the caller, or another person for the owner only.
func settingsEmail(q *Request, email *string) (string, error) {
	me := ""
	if q.person != nil {
		me = q.person.Email
	}
	if q.agent != "" && me == "" {
		return "", fail(403, "An agent without a director has no settings")
	}
	if email == nil || strings.ToLower(*email) == me {
		if me == "" {
			return ownerEmail(q), nil
		}
		return me, nil
	}
	// An agent acts for its director only, never on another person's settings, even with the owner's rights.
	if q.agent != "" {
		return "", fail(403, "An agent reads and sets only its director's settings")
	}
	if q.person != nil && !q.person.Owner {
		return "", fail(403, "Only the owner reads or sets another person's settings")
	}
	e := strings.ToLower(*email)
	if q.peopleSnapshot().person(e) == nil {
		return "", fail(400, "Unknown person "+*email)
	}
	return e, nil
}

// personSettings: the stored document read as settings; order list and empty collections when unset.
func personSettings(q *Request, email string) (contract.PersonSettings, error) {
	ps := contract.PersonSettings{Email: email, Order: contract.DispatchOrderList}
	d, err := unscoped(q, func() (doc, error) { return maybe(q, "person-setting:"+email, "person-setting") })
	if err != nil {
		return ps, err
	}
	if d != nil {
		// ponytail: the document holds the settings fields as written; a JSON round trip reads them back typed.
		raw, _ := json.Marshal(d)
		// ponytail: only person_settings_set writes this document, through checked typed fields; a field that does not
		// read (a hand edit) stays at its default instead of locking the person out of get, set and dispatch.
		_ = json.Unmarshal(raw, &ps)
		ps.Email = email
		if !slices.Contains(contract.DispatchOrderValues, ps.Order) {
			ps.Order = contract.DispatchOrderList
		}
	}
	ps.Pins = orEmpty(ps.Pins)
	ps.Focus = orEmpty(ps.Focus)
	if ps.Views == nil {
		ps.Views = []contract.SavedView{}
	}
	if ps.FocusPresets == nil {
		ps.FocusPresets = []contract.FocusPreset{}
	}
	if ps.Widths == nil {
		ps.Widths = map[string]int64{}
	}
	if ps.RowPins == nil {
		ps.RowPins = map[string][]string{}
	}
	return ps, nil
}

func orEmpty(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// unique: xs without repeats, in first-seen order.
func unique(xs []string) []string {
	out := []string{}
	for _, x := range xs {
		if !slices.Contains(out, x) {
			out = append(out, x)
		}
	}
	return out
}

// dispatchOrder: the director's order setting; list when unset.
func dispatchOrder(q *Request, email string) (contract.DispatchOrder, error) {
	ps, err := personSettings(q, email)
	return ps.Order, err
}

func personSettingsGet(q *Request, r *contract.PersonSettingsGetRequest) (contract.PersonSettings, error) {
	email, err := settingsEmail(q, r.Email)
	if err != nil {
		return contract.PersonSettings{}, err
	}
	return personSettings(q, email)
}

// personSettingsSet changes the fields the request names; each changed field writes one setting-change.
func personSettingsSet(q *Request, r *contract.PersonSettingsSetRequest) (contract.PersonSettings, error) {
	email, err := settingsEmail(q, r.Email)
	if err != nil {
		return contract.PersonSettings{}, err
	}
	if r.Order != nil && !slices.Contains(contract.DispatchOrderValues, *r.Order) {
		return contract.PersonSettings{}, fail(400, "order: list or soonest_reset")
	}
	if err := checkPrefs(q, r); err != nil {
		return contract.PersonSettings{}, err
	}
	// One writer per person: a set reads the document and writes it whole, so concurrent sets of different fields keep both.
	if err := acquireTxLock(q, "person-setting:"+email); err != nil {
		return contract.PersonSettings{}, err
	}
	prev, err := personSettings(q, email)
	if err != nil {
		return contract.PersonSettings{}, err
	}
	// section 8: an agent may propose order soonest_reset, never change it; the other preferences are open to agents (R7).
	if r.Order != nil && *r.Order != prev.Order && q.agent != "" {
		return contract.PersonSettings{}, fail(403, "Only a person may change the dispatch order")
	}
	// A newly named project (focus or a focus preset) must exist and be visible to the caller; one already stored may since
	// be gone and stays.
	known := slices.Clone(prev.Focus)
	for _, p := range prev.FocusPresets {
		known = append(known, p.Projects...)
	}
	named := slices.Clone(r.Focus)
	if r.FocusPresetPut != nil {
		named = append(named, r.FocusPresetPut.Projects...)
	}
	for _, p := range r.FocusPresets {
		named = append(named, p.Projects...)
	}
	for _, id := range named {
		if id != "-" && !slices.Contains(known, id) && (projectSettingsOf(q, id) == nil || (q.person != nil && !mayAccessProjectID(q.person, id))) {
			return contract.PersonSettings{}, fail(400, "unknown project "+id)
		}
	}
	next := prev
	if r.Order != nil {
		next.Order = *r.Order
	}
	if r.Pins != nil {
		next.Pins = unique(r.Pins)
	}
	if r.Focus != nil {
		next.Focus = unique(r.Focus)
	}
	if r.Views != nil {
		next.Views = r.Views
	}
	if r.FocusPresets != nil {
		next.FocusPresets = r.FocusPresets
	}
	// Per-item changes apply to the stored lists under the person lock, so concurrent saves from several devices or agents
	// never drop each other's items.
	if r.ViewPut != nil || r.ViewDelete != nil {
		vs := []contract.SavedView{}
		put := false
		for _, v := range next.Views {
			switch {
			case r.ViewDelete != nil && v.List == r.ViewDelete.List && v.Name == r.ViewDelete.Name:
			case r.ViewPut != nil && v.List == r.ViewPut.List && v.Name == r.ViewPut.Name:
				vs, put = append(vs, *r.ViewPut), true
			default:
				vs = append(vs, v)
			}
		}
		if r.ViewPut != nil && !put {
			vs = append(vs, *r.ViewPut)
		}
		next.Views = vs
	}
	if r.FocusPresetPut != nil || r.FocusPresetDelete != nil {
		ps := []contract.FocusPreset{}
		put := false
		for _, p := range next.FocusPresets {
			switch {
			case r.FocusPresetDelete != nil && p.Name == *r.FocusPresetDelete:
			case r.FocusPresetPut != nil && p.Name == r.FocusPresetPut.Name:
				ps, put = append(ps, contract.FocusPreset{Name: p.Name, Projects: unique(r.FocusPresetPut.Projects)}), true
			default:
				ps = append(ps, p)
			}
		}
		if r.FocusPresetPut != nil && !put {
			ps = append(ps, contract.FocusPreset{Name: r.FocusPresetPut.Name, Projects: unique(r.FocusPresetPut.Projects)})
		}
		next.FocusPresets = ps
	}
	if r.Widths != nil {
		next.Widths = maps.Clone(prev.Widths)
		for k, w := range r.Widths {
			if w == 0 {
				delete(next.Widths, k)
			} else {
				next.Widths[k] = w
			}
		}
	}
	if r.NavCollapsed != nil {
		next.NavCollapsed = *r.NavCollapsed
	}
	if r.RowPins != nil {
		next.RowPins = maps.Clone(prev.RowPins)
		for k, ids := range r.RowPins {
			if len(ids) == 0 {
				delete(next.RowPins, k)
			} else {
				next.RowPins[k] = unique(ids)
			}
		}
	}
	if r.RowPin != nil {
		next.RowPins = maps.Clone(next.RowPins)
		ids := slices.Clone(next.RowPins[r.RowPin.List])
		if had := slices.Contains(ids, r.RowPin.Id); r.RowPin.Pinned && !had { // pinning a pinned row keeps its place
			ids = append(ids, r.RowPin.Id)
		} else if !r.RowPin.Pinned {
			ids = slices.DeleteFunc(ids, func(x string) bool { return x == r.RowPin.Id })
		}
		if len(ids) == 0 {
			delete(next.RowPins, r.RowPin.List)
		} else {
			next.RowPins[r.RowPin.List] = ids
		}
	}
	by := actor(q, "operator")
	_, err = unscoped(q, func() (struct{}, error) {
		if err := put(q, "person-setting", doc{"id": "person-setting:" + email, "email": email, "order": string(next.Order), "pins": next.Pins,
			"views": next.Views, "focus": next.Focus, "focusPresets": next.FocusPresets, "widths": next.Widths, "rowPins": next.RowPins, "navCollapsed": next.NavCollapsed}); err != nil {
			return struct{}{}, err
		}
		change := func(field string, previous, value any) error {
			return put(q, "setting-change", doc{"id": newID(), "target": "person:" + email, "holder": email, "field": field, "previous": previous, "value": value, "actor": by, "at": nowMs()})
		}
		if next.Order != prev.Order {
			if err := change("order", string(prev.Order), string(next.Order)); err != nil {
				return struct{}{}, err
			}
		}
		for _, f := range []struct {
			name            string
			previous, value any
		}{{"pins", prev.Pins, next.Pins}, {"views", prev.Views, next.Views}, {"focus", prev.Focus, next.Focus},
			{"focusPresets", prev.FocusPresets, next.FocusPresets}, {"widths", prev.Widths, next.Widths}, {"rowPins", prev.RowPins, next.RowPins}, {"navCollapsed", prev.NavCollapsed, next.NavCollapsed}} {
			a, _ := json.Marshal(f.previous)
			b, _ := json.Marshal(f.value)
			if string(a) != string(b) {
				if err := change(f.name, f.previous, f.value); err != nil {
					return struct{}{}, err
				}
			}
		}
		return struct{}{}, nil
	})
	return next, err
}

// checkView: a saved view has a list and a name and is a valid view of that list (viewProblem, the view_link check).
func checkView(v contract.SavedView) error {
	if strings.TrimSpace(v.List) == "" || strings.TrimSpace(v.Name) == "" {
		return fail(400, "views: list and name are required")
	}
	var view any
	raw, _ := json.Marshal(v.View)
	_ = json.Unmarshal(raw, &view)
	if p := viewProblem(view); p != "" {
		return fail(400, "views: "+v.Name+": "+p)
	}
	if name, _ := view.(map[string]any)["view"].(string); name != v.List {
		return fail(400, "views: "+v.Name+": view.view must equal list "+v.List)
	}
	return nil
}

// checkPrefs: names are present and unique (views per list), each view is a valid view of its list (viewProblem, the
// view_link check), widths are not negative.
func checkPrefs(q *Request, r *contract.PersonSettingsSetRequest) error {
	if r.ViewPut != nil {
		if err := checkView(*r.ViewPut); err != nil {
			return err
		}
	}
	if r.FocusPresetPut != nil && strings.TrimSpace(r.FocusPresetPut.Name) == "" {
		return fail(400, "focusPresetPut: name is required")
	}
	if r.RowPin != nil && (r.RowPin.List == "" || r.RowPin.Id == "") {
		return fail(400, "rowPin: list and id are required")
	}
	if r.ViewPut != nil && r.ViewDelete != nil && r.ViewPut.List == r.ViewDelete.List && r.ViewPut.Name == r.ViewDelete.Name {
		return fail(400, "viewPut and viewDelete name the same view")
	}
	if r.FocusPresetPut != nil && r.FocusPresetDelete != nil && r.FocusPresetPut.Name == *r.FocusPresetDelete {
		return fail(400, "focusPresetPut and focusPresetDelete name the same preset")
	}
	seen := map[string]bool{}
	for _, v := range r.Views {
		if err := checkView(v); err != nil {
			return err
		}
		if seen[v.List+"\x00"+v.Name] {
			return fail(400, "views: duplicate name "+v.Name+" in list "+v.List)
		}
		seen[v.List+"\x00"+v.Name] = true
	}
	names := map[string]bool{}
	for _, p := range r.FocusPresets {
		if strings.TrimSpace(p.Name) == "" || names[p.Name] {
			return fail(400, "focusPresets: names are required and unique")
		}
		names[p.Name] = true
	}
	for k, w := range r.Widths {
		if w < 0 {
			return fail(400, "widths: "+k+" is negative")
		}
	}
	return nil
}

// soonestReset (section 8): ready candidates sorted by the earliest weekly resetsAt among those with weekly left above 0,
// so limit about to reset is used first; the others keep their order after them. Skipped candidates are untouched.
func soonestReset(cs []*candidate, readings map[string][]Reading) {
	key := func(c *candidate) float64 {
		best := -1.0
		list := readings[astr(c.Subscription["id"])]
		for _, r := range list {
			if r.Period == "weekly" && r.State == "known" && r.Pct != nil && *r.Pct >= 100 {
				return -1 // the overall weekly limit is used up: a per-model window does not make it soon
			}
		}
		for _, r := range list {
			if r.State != "known" || r.ResetsAt == nil || r.Pct == nil || *r.Pct >= 100 || !(r.Period == "weekly" || strings.HasPrefix(r.Period, "weekly-")) {
				continue
			}
			if best < 0 || *r.ResetsAt < best {
				best = *r.ResetsAt
			}
		}
		return best
	}
	var ready []*candidate
	var idx []int
	for i, c := range cs {
		if c.Skipped == "" && c.Subscription != nil {
			ready, idx = append(ready, c), append(idx, i)
		}
	}
	sort.SliceStable(ready, func(i, j int) bool {
		ka, kb := key(ready[i]), key(ready[j])
		return ka >= 0 && (kb < 0 || ka < kb)
	})
	for n, i := range idx {
		cs[i] = ready[n]
	}
}
