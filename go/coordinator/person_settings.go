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
	if email == nil || strings.ToLower(*email) == me {
		if me == "" {
			return ownerEmail(q), nil
		}
		return me, nil
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
	// section 8: an agent may propose order soonest_reset, never set it; the other preferences are open to agents (R7).
	if r.Order != nil && q.agent != "" {
		return contract.PersonSettings{}, fail(403, "Only a person may set the dispatch order")
	}
	if r.Order != nil && !slices.Contains(contract.DispatchOrderValues, *r.Order) {
		return contract.PersonSettings{}, fail(400, "order: list or soonest_reset")
	}
	if err := checkPrefs(r); err != nil {
		return contract.PersonSettings{}, err
	}
	// One writer per person: a set reads the document and writes it whole, so concurrent sets of different fields keep both.
	if err := exec(q, "SELECT pg_advisory_xact_lock(hashtext($1))", "person-setting:"+email); err != nil {
		return contract.PersonSettings{}, err
	}
	prev, err := personSettings(q, email)
	if err != nil {
		return contract.PersonSettings{}, err
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
	by := actor(q, "operator")
	_, err = unscoped(q, func() (struct{}, error) {
		if err := put(q, "person-setting", doc{"id": "person-setting:" + email, "email": email, "order": string(next.Order), "pins": next.Pins,
			"views": next.Views, "focus": next.Focus, "focusPresets": next.FocusPresets, "widths": next.Widths, "rowPins": next.RowPins}); err != nil {
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
			{"focusPresets", prev.FocusPresets, next.FocusPresets}, {"widths", prev.Widths, next.Widths}, {"rowPins", prev.RowPins, next.RowPins}} {
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

// checkPrefs: names are present and unique (views per list), widths are not negative.
func checkPrefs(r *contract.PersonSettingsSetRequest) error {
	seen := map[string]bool{}
	for _, v := range r.Views {
		if strings.TrimSpace(v.List) == "" || strings.TrimSpace(v.Name) == "" {
			return fail(400, "views: list and name are required")
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
