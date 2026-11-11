package main

// Person settings (fleet-people-access.md section 8 "Dispatch"): order list | soonest_reset on the director, default
// list. One person-setting document per email; each change writes setting-change.

import (
	"fleet/contract"
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
	if q.app.people[e] == nil {
		return "", fail(400, "Unknown person "+*email)
	}
	return e, nil
}

// personSettings: the stored document read as settings; order list and no pins when unset.
func personSettings(q *Request, email string) (contract.PersonSettings, error) {
	ps := contract.PersonSettings{Email: email, Order: contract.DispatchOrderList, Pins: []string{}}
	d, err := unscoped(q, func() (doc, error) { return maybe(q, "person-setting:"+email, "person-setting") })
	if err != nil || d == nil {
		return ps, err
	}
	if o := contract.DispatchOrder(astr(d["order"])); slices.Contains(contract.DispatchOrderValues, o) {
		ps.Order = o
	}
	ps.Pins = strs(d["pins"])
	return ps, nil
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
	prev, err := personSettings(q, email)
	if err != nil {
		return contract.PersonSettings{}, err
	}
	next := prev
	if r.Order != nil {
		next.Order = *r.Order
	}
	if r.Pins != nil {
		next.Pins = []string{}
		for _, id := range r.Pins {
			if !slices.Contains(next.Pins, id) {
				next.Pins = append(next.Pins, id)
			}
		}
	}
	by := actor(q, "operator")
	_, err = unscoped(q, func() (struct{}, error) {
		if err := put(q, "person-setting", doc{"id": "person-setting:" + email, "email": email, "order": string(next.Order), "pins": next.Pins}); err != nil {
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
		if !slices.Equal(next.Pins, prev.Pins) {
			return struct{}{}, change("pins", prev.Pins, next.Pins)
		}
		return struct{}{}, nil
	})
	return next, err
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
