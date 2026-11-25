package main

// Person settings (fleet-people-access.md section 8 "Dispatch"): order list | soonest_reset on the director, default
// list. The same document holds the person's app preferences: pins, saved views, focus and focus presets, ignored projects,
// column widths, open side panels, row pins and the navigation counter views (fleet-people-access.md section 8 "Preferences"). One person-setting document per email; each change writes setting-change.

import (
	"encoding/json"
	"errors"
	"fleet/contract"
	"fleet/coordinator/db"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

func init() {
	op("person_settings_get", personSettingsGet)
	op("person_settings_set", personSettingsSet)
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
	raw, err := unscoped(q, func() ([]byte, error) { return q.db().PersonSettingPayload(q.ctx(), "person-setting:"+email) })
	if errors.Is(err, pgx.ErrNoRows) {
		raw, err = nil, nil
	}
	if err != nil {
		return ps, err
	}
	if raw != nil {
		// ponytail: only person_settings_set writes this payload, through checked typed fields; a field that does not
		// read (a hand edit) stays at its default instead of locking the person out of get, set and dispatch.
		_ = json.Unmarshal(raw, &ps)
		ps.Email = email
		if !slices.Contains(contract.DispatchOrderValues, ps.Order) {
			ps.Order = contract.DispatchOrderList
		}
	}
	ps.Pins = orEmpty(ps.Pins)
	ps.Focus = orEmpty(ps.Focus)
	ps.Ignored = orEmpty(ps.Ignored)
	if ps.Views == nil {
		ps.Views = []contract.SavedView{}
	}
	if ps.FocusPresets == nil {
		ps.FocusPresets = []contract.FocusPreset{}
	}
	if ps.Widths == nil {
		ps.Widths = map[string]int64{}
	}
	if ps.LastViews == nil {
		ps.LastViews = map[string]string{}
	}
	if ps.RowPins == nil {
		ps.RowPins = map[string][]string{}
	}
	if ps.ListGroups == nil {
		ps.ListGroups = map[string][]string{}
	}
	if ps.NavCounters == nil {
		ps.NavCounters = map[string]string{}
	}
	if ps.Panels == nil {
		ps.Panels = map[string]bool{}
	}
	return ps, nil
}

// personSettingPayload: the person-setting row's payload, the settings fields under their record names (email is the
// row's column).
type personSettingPayload struct {
	Order        contract.DispatchOrder `json:"order"`
	Pins         []string               `json:"pins"`
	Views        []contract.SavedView   `json:"views"`
	Focus        []string               `json:"focus"`
	FocusPresets []contract.FocusPreset `json:"focusPresets"`
	Ignored      []string               `json:"ignored"`
	Widths       map[string]int64       `json:"widths"`
	RowPins      map[string][]string    `json:"rowPins"`
	NavCollapsed bool                   `json:"navCollapsed"`
	RailBots     contract.RailBots      `json:"railBots"`
	NavCounters  map[string]string      `json:"navCounters"`
	LastViews    map[string]string      `json:"lastViews"`
	ListGroups   map[string][]string    `json:"listGroups"`
	Panels       map[string]bool        `json:"panels"`
	Timezone     string                 `json:"timezone,omitempty"`
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
	// A newly named project (focus, a focus preset or an ignored one) must exist and be visible to the caller; one already
	// stored may since be gone and stays.
	known := append(slices.Clone(prev.Focus), prev.Ignored...)
	for _, p := range prev.FocusPresets {
		known = append(known, p.Projects...)
	}
	named := append(slices.Clone(r.Focus), r.Ignored...)
	if r.Ignore != nil && r.Ignore.Ignored {
		named = append(named, r.Ignore.Project)
	}
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
	// Ignored projects (OR-34): an ignored project leaves the focus, so the focus never holds one.
	if r.Ignored != nil {
		next.Ignored = unique(r.Ignored)
	}
	if r.Ignore != nil {
		had := slices.Contains(next.Ignored, r.Ignore.Project)
		if r.Ignore.Ignored && !had {
			next.Ignored = append(slices.Clone(next.Ignored), r.Ignore.Project)
		} else if !r.Ignore.Ignored {
			next.Ignored = slices.DeleteFunc(slices.Clone(next.Ignored), func(x string) bool { return x == r.Ignore.Project })
		}
	}
	focused := len(next.Focus) > 0
	next.Focus = slices.DeleteFunc(slices.Clone(next.Focus), func(x string) bool { return slices.Contains(next.Ignored, x) })
	// An empty focus is every project: ignoring the last focused projects would widen the person's lists, not narrow them.
	// Only a change that clears the focus itself (focus: []) may do it.
	if focused && len(next.Focus) == 0 {
		return contract.PersonSettings{}, fail(400, "focus: every project in it is ignored, and an empty focus is every project; focus another project first")
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
	if r.LastViews != nil {
		next.LastViews = maps.Clone(prev.LastViews)
		for k, v := range r.LastViews {
			if v == "" {
				delete(next.LastViews, k)
			} else {
				next.LastViews[k] = v
			}
		}
	}
	if r.ListGroups != nil {
		next.ListGroups = maps.Clone(prev.ListGroups)
		for k, keys := range r.ListGroups {
			if len(keys) == 0 {
				delete(next.ListGroups, k)
			} else {
				next.ListGroups[k] = unique(keys)
			}
		}
		if len(next.ListGroups) > maxListGroupLists {
			return contract.PersonSettings{}, fail(400, "listGroups: at most 64 lists in all")
		}
	}
	if r.NavCollapsed != nil {
		next.NavCollapsed = *r.NavCollapsed
	}
	// The person's time zone (agents and Telegram show times in it): an IANA name Go knows, or empty to clear.
	if r.Timezone != nil {
		if !knownZone(*r.Timezone) {
			return contract.PersonSettings{}, fail(400, "timezone: an IANA time zone name such as Europe/Nicosia")
		}
		next.Timezone = *r.Timezone
	}
	if r.RailBots != nil {
		if r.RailBots.Recent < 0 {
			return contract.PersonSettings{}, fail(400, "railBots.recent: 0 or more")
		}
		next.RailBots = *r.RailBots
	}
	// Side panels (side-panel.md): open or closed by panel key, merged.
	if r.Panels != nil {
		next.Panels = maps.Clone(prev.Panels)
		maps.Copy(next.Panels, r.Panels)
		if len(next.Panels) > maxPanels {
			return contract.PersonSettings{}, fail(400, "panels: at most 64 panels in all")
		}
	}
	// The navigation counter views (nav_counts.go): merged by section; a saved view must be in the views after this change,
	// and a removed one leaves its counter (the default counts again), so a later view of the same name is not counted
	// unchosen.
	if r.NavCounters != nil || r.Views != nil || r.ViewDelete != nil {
		next.NavCounters = maps.Clone(prev.NavCounters)
		for k, v := range next.NavCounters {
			if _, isSystem := navSystemView(k, v); v != navCounterOff && !isSystem &&
				!slices.ContainsFunc(next.Views, func(x contract.SavedView) bool { return x.List == k && x.Name == v }) {
				delete(next.NavCounters, k)
			}
		}
	}
	if r.NavCounters != nil {
		if err := checkNavCounters(r.NavCounters, next.Views); err != nil {
			return contract.PersonSettings{}, err
		}
		for k, v := range r.NavCounters {
			if v == "" {
				delete(next.NavCounters, k)
			} else {
				next.NavCounters[k] = v
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
	_, err = unscoped(q, func() (struct{}, error) {
		payload := personSettingPayload{Order: next.Order, Pins: next.Pins, Views: next.Views, Focus: next.Focus, FocusPresets: next.FocusPresets,
			Ignored: next.Ignored, Widths: next.Widths, RowPins: next.RowPins, NavCollapsed: next.NavCollapsed, RailBots: next.RailBots, NavCounters: next.NavCounters,
			LastViews: next.LastViews, ListGroups: next.ListGroups, Panels: next.Panels, Timezone: next.Timezone}
		if err := q.db().PersonSettingPut(q.ctx(), db.PersonSettingPutParams{ID: "person-setting:" + email, Email: email, Payload: jsonBytes(payload)}); err != nil {
			return struct{}{}, err
		}
		change := func(field string, previous, value any) error {
			return audit(q, "person:"+email, field, previous, value, auditExtra{Holder: email})
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
			{"focusPresets", prev.FocusPresets, next.FocusPresets}, {"ignored", prev.Ignored, next.Ignored}, {"widths", prev.Widths, next.Widths}, {"rowPins", prev.RowPins, next.RowPins}, {"navCollapsed", prev.NavCollapsed, next.NavCollapsed}, {"railBots", prev.RailBots, next.RailBots}, {"panels", prev.Panels, next.Panels}, {"navCounters", prev.NavCounters, next.NavCounters}, {"timezone", prev.Timezone, next.Timezone}} {
			// lastViews is where the person was and listGroups which groups they folded, not settings: they change as the person
			// moves around a list and are not audited.
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
	var head struct {
		View any `json:"view"`
	}
	_ = json.Unmarshal(raw, &head)
	if name, _ := head.View.(string); name != v.List {
		return fail(400, "views: "+v.Name+": view.view must equal list "+v.List)
	}
	return nil
}

var sectionID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)

// listGroupsKey: <section>:<grouping>, a section id and a grouping name (a lower-case slug; pinned is the Pinned group).
var listGroupsKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}:[a-z][a-z0-9_-]{0,31}$`)

const maxListGroupLists, maxListGroupKeys, maxGroupKeyBytes = 64, 256, 256

// panelKey: a side panel's key, a lower-case slug (session-context); at most maxPanels are stored.
var panelKey = regexp.MustCompile(`^[a-z][a-z0-9-]{0,47}$`)

const maxPanels = 64

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
	if r.Ignore != nil && r.Ignore.Project == "" {
		return fail(400, "ignore: project is required")
	}
	// ignore.ignored and rowPin.pinned decode to false when absent, which would restore the project or unpin the row:
	// each must be given.
	if r.Ignore != nil || r.RowPin != nil {
		var raw struct {
			Ignore struct {
				Ignored *bool `json:"ignored"`
			} `json:"ignore"`
			RowPin struct {
				Pinned *bool `json:"pinned"`
			} `json:"rowPin"`
		}
		bad := json.Unmarshal(q.body, &raw) != nil
		if r.Ignore != nil && (bad || raw.Ignore.Ignored == nil) {
			return fail(400, "ignore: ignored is required")
		}
		if r.RowPin != nil && (bad || raw.RowPin.Pinned == nil) {
			return fail(400, "rowPin: pinned is required")
		}
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
	if len(r.Panels) > maxPanels {
		return fail(400, "panels: at most 64 panels")
	}
	for k := range r.Panels {
		if !panelKey.MatchString(k) {
			return fail(400, "panels: "+k+" is not a panel key (a lower-case slug)")
		}
		if k == "nav" {
			return fail(400, "panels: the navigation is navCollapsed, not panels.nav")
		}
	}
	// A last view is that section's own list: /<id> or /<id>?<query>, short, never another origin; section ids are short
	// lower-case words and a request names at most 64 of them.
	if len(r.LastViews) > 64 {
		return fail(400, "lastViews: at most 64 sections")
	}
	// A list's group keys are the server's or the page's own (project ids, state ranks, agent names, '' for an empty one): short, no
	// control characters, at most 256 per list; a request names at most 64 lists and the stored document holds at most 64.
	if len(r.ListGroups) > maxListGroupLists {
		return fail(400, "listGroups: at most 64 lists")
	}
	for k, keys := range r.ListGroups {
		if !listGroupsKey.MatchString(k) {
			return fail(400, "listGroups: "+k+" is not <section>:<grouping>")
		}
		if len(keys) > maxListGroupKeys {
			return fail(400, "listGroups: "+k+" names more than 256 groups")
		}
		for _, g := range keys {
			if len(g) > maxGroupKeyBytes || !utf8.ValidString(g) || strings.IndexFunc(g, unicode.IsControl) >= 0 {
				return fail(400, "listGroups: "+k+": a group key is longer than 256 bytes or has control characters")
			}
		}
	}
	for k, v := range r.LastViews {
		if !sectionID.MatchString(k) {
			return fail(400, "lastViews: "+k+" is not a section id")
		}
		if v != "" && (len(v) > 2048 || !(v == "/"+k || strings.HasPrefix(v, "/"+k+"?")) || strings.Contains(v, "//")) {
			return fail(400, "lastViews: "+k+" must be that section's list path")
		}
	}
	return nil
}

// soonestReset (section 8): ready candidates sorted by the earliest weekly resetsAt among those with weekly left above 0,
// so limit about to reset is used first; the others keep their order after them. Skipped candidates are untouched.
func soonestReset(cs []*candidate, readings map[string][]Reading) {
	key := func(c *candidate) float64 {
		best := -1.0
		var id string
		if c.Subscription != nil {
			id = c.Subscription.ID
		}
		list := readings[id]
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

// knownZone: empty (unknown) or a zone name time.LoadLocation loads; "Local" (the coordinator's own zone) is refused.
func knownZone(tz string) bool {
	if tz == "" {
		return true
	}
	if tz == "Local" || len(tz) > 64 {
		return false
	}
	_, err := time.LoadLocation(tz)
	return err == nil
}

// zoneInstruction: the line an agent gets about its director's time zone, so the times it writes for them are theirs;
// UTC, said as UTC, when the zone is unknown. Empty for work no person directs.
func zoneInstruction(q *Request, email string) (string, error) {
	if email == "" {
		return "", nil
	}
	ps, err := personSettings(q, email)
	if err != nil {
		return "", err
	}
	if ps.Timezone == "" || !knownZone(ps.Timezone) {
		return "The time zone of the person you work for is unknown: write times in UTC and mark them UTC.", nil
	}
	return "The person you work for is in time zone " + ps.Timezone + ": write times for them in that zone, without naming it unless asked; tool and log times are usually UTC, convert them.", nil
}
