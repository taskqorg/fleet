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

// dispatchOrder: the director's order setting; list when unset.
func dispatchOrder(q *Request, email string) (contract.DispatchOrder, error) {
	d, err := unscoped(q, func() (doc, error) { return maybe(q, "person-setting:"+email, "person-setting") })
	if err != nil || d == nil {
		return contract.DispatchOrderList, err
	}
	if o := contract.DispatchOrder(astr(d["order"])); slices.Contains(contract.DispatchOrderValues, o) {
		return o, nil
	}
	return contract.DispatchOrderList, nil
}

func personSettingsGet(q *Request, r *contract.PersonSettingsGetRequest) (contract.PersonSettings, error) {
	email, err := settingsEmail(q, r.Email)
	if err != nil {
		return contract.PersonSettings{}, err
	}
	o, err := dispatchOrder(q, email)
	return contract.PersonSettings{Email: email, Order: o}, err
}

func personSettingsSet(q *Request, r *contract.PersonSettingsSetRequest) (contract.PersonSettings, error) {
	email, err := settingsEmail(q, r.Email)
	if err != nil {
		return contract.PersonSettings{}, err
	}
	if !slices.Contains(contract.DispatchOrderValues, r.Order) {
		return contract.PersonSettings{}, fail(400, "order: list or soonest_reset")
	}
	prev, err := dispatchOrder(q, email)
	if err != nil {
		return contract.PersonSettings{}, err
	}
	by := actor(q, "operator")
	_, err = unscoped(q, func() (struct{}, error) {
		if err := put(q, "person-setting", doc{"id": "person-setting:" + email, "email": email, "order": string(r.Order)}); err != nil {
			return struct{}{}, err
		}
		return struct{}{}, put(q, "setting-change", doc{"id": newID(), "target": "person:" + email, "holder": email, "field": "order", "previous": string(prev), "value": string(r.Order), "actor": by, "at": nowMs()})
	})
	return contract.PersonSettings{Email: email, Order: r.Order}, err
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
