package main

// Access (docs/designs/fleet-connections.md §11, System › Access): one read of every subscription login, model API
// key and service key Fleet has or offers, with its state in plain words and the next step; a read-only check; the
// start of adding access (a key, a host login, a counted monthly limit); and deleting a subscription nothing uses.
// Checks never start a login: a login starts only from the host login step the owner presses.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("access_status", func(q *Request, _ *contract.AccessStatusRequest) (contract.AccessStatusReply, error) {
		return accessStatus(q)
	})
	op("access_check", accessCheck)
	op("access_onboard", accessOnboard)
	op("account_delete", accountDelete)
}

// accessGuide: what one provider needs from its owner, and the one cheap read-only call that shows a key works.
type accessGuide struct {
	group      contract.AccessGroup
	label      string
	keyURL     string
	billingURL string
	docsURL    string
	note       string
	probe      string // the tool access_check calls; "" when the provider has none without arguments
	probeArgs  json.RawMessage
	balance    string // dotted field of the probe reply that holds the credit balance
}

var accessGuides = map[string]accessGuide{
	"anthropic": {group: contract.AccessGroupModelKey, label: "Anthropic", keyURL: "https://console.anthropic.com/settings/keys",
		billingURL: "https://console.anthropic.com/settings/billing", probe: "list_models", probeArgs: json.RawMessage(`{"limit":1}`),
		note: "A key bills its Anthropic organization's credits, not a Claude subscription."},
	"openai": {group: contract.AccessGroupModelKey, label: "OpenAI", keyURL: "https://platform.openai.com/api-keys",
		billingURL: "https://platform.openai.com/settings/organization/billing/overview", probe: "list_models", probeArgs: json.RawMessage(`{}`),
		note: "A key bills its OpenAI organization (project limits), not a ChatGPT subscription."},
	"google-ai": {group: contract.AccessGroupModelKey, label: "Google AI", keyURL: "https://aistudio.google.com/apikey",
		billingURL: "https://aistudio.google.com/billing", docsURL: "https://ai.google.dev/gemini-api/docs/rate-limits", probe: "models_list", probeArgs: json.RawMessage(`{}`),
		note: "Create the key in a project with billing (Google AI Ultra credits apply there); on the free tier image and video models get no quota."},
	"tavily": {group: contract.AccessGroupService, label: "Tavily", keyURL: "https://app.tavily.com/home", probe: "web_search",
		probeArgs: json.RawMessage(`{"query":"fleet access check","max_results":1}`), note: "A check spends one search credit."},
	"meshy": {group: contract.AccessGroupService, label: "Meshy", keyURL: "https://www.meshy.ai/settings/api", probe: "balance",
		probeArgs: json.RawMessage(`{}`), balance: "balance"},
	"tripo": {group: contract.AccessGroupService, label: "Tripo", keyURL: "https://platform.tripo3d.ai/api-keys", probe: "balance",
		probeArgs: json.RawMessage(`{}`), balance: "data.balance"},
	"typesafe": {group: contract.AccessGroupService, label: "TypeSafe", keyURL: "https://app.typesafe.ai", probe: "list_models", probeArgs: json.RawMessage(`{}`)},
	"huggingface": {group: contract.AccessGroupService, label: "Hugging Face", keyURL: "https://huggingface.co/settings/tokens", probe: "inference",
		probeArgs: json.RawMessage(`{"model":"distilbert/distilbert-base-uncased-finetuned-sst-2-english","inputs":"ok"}`)},
	"github": {group: contract.AccessGroupService, label: "GitHub", keyURL: "https://github.com/settings/personal-access-tokens",
		note: "A fine-grained read-only token; the organization refuses tokens that live longer than 366 days."},
	"github-pins": {group: contract.AccessGroupService, label: "GitHub pin proposals", keyURL: "https://github.com/settings/personal-access-tokens/new",
		probe: "commit_status", probeArgs: json.RawMessage(`{"owner":"taskqorg","repo":"fleet","ref":"main"}`),
		note: "A fine-grained token: resource owner taskqorg, only the repository taskqorg/fleet, repository permissions Commit statuses and Pull requests read and write, nothing else, expiry 366 days. The coordinator writes with it only on automation/<kind>-pins proposals; the check reads main's status."},
	"gitlab":   {group: contract.AccessGroupService, label: "GitLab", keyURL: "https://gitlab.com/-/user_settings/personal_access_tokens"},
	"youtrack": {group: contract.AccessGroupService, label: "YouTrack", probe: "projects", probeArgs: json.RawMessage(`{}`)},
	"bugsink":  {group: contract.AccessGroupService, label: "Bugsink", probe: "projects", probeArgs: json.RawMessage(`{}`)},
	"telegram": {group: contract.AccessGroupService, label: "Telegram", keyURL: "https://t.me/BotFather", probe: "get_me", probeArgs: json.RawMessage(`{}`),
		note: "A bot token from @BotFather (/newbot). For groups, turn off its privacy mode there (/setprivacy) so it reads every message."},
}

// accessOffered: providers a person adds keys for even when Fleet has none yet (owner's list, 2026-11-25; Telegram for
// the Telegram channel, docs/designs/fleet-telegram.md).
var accessOffered = []string{"anthropic", "openai", "google-ai", "tavily", "meshy", "tripo", "telegram"}

var harnessLogin = map[string]string{"codex": "ChatGPT login", "claude": "Claude login", "antigravity": "Google login", "acp": "ACP login"}

func accessStatus(q *Request) (contract.AccessStatusReply, error) {
	out := contract.AccessStatusReply{CheckedAt: nowMs(), Items: []contract.AccessItem{}}
	done := phase(q.baseCtx(), "profiles")
	profiles, err := accessProfiles(q)
	done()
	if err != nil {
		return out, err
	}
	done = phase(q.baseCtx(), "subscriptions")
	subs, err := accessSubscriptions(q, profiles)
	done()
	if err != nil {
		return out, err
	}
	done = phase(q.baseCtx(), "connections")
	keys, err := accessConnections(q, profiles)
	done()
	if err != nil {
		return out, err
	}
	forge, err := accessForgeLogins(q)
	if err != nil {
		return out, err
	}
	out.Items = append(append(subs, keys...), forge...)
	for _, it := range out.Items {
		switch it.State {
		case contract.AccessStateWorks:
			out.Counts.Works++
		case contract.AccessStateNotAdded:
			out.Counts.NotAdded++
		case contract.AccessStateFailing, contract.AccessStateNeedsLogin, contract.AccessStateNeedsKey:
			out.Counts.NeedYou++
		}
	}
	return out, nil
}

// accessProfile: what access_status reads of an agent profile: its harness and its granted connections.
type accessProfile struct {
	ID          string   `json:"id"`
	Harness     string   `json:"harness"`
	Connections []string `json:"connections"`
}

func accessProfiles(q *Request) ([]accessProfile, error) {
	ps, err := listProfiles(q)
	if err != nil {
		return nil, err
	}
	out := make([]accessProfile, 0, len(ps))
	for _, p := range ps {
		v, _ := jsonAs[accessProfile](jsonBytes(p))
		if v.Harness == "" {
			v.Harness = "codex"
		}
		out = append(out, v)
	}
	return out, nil
}

func accessSubscriptions(q *Request, profiles []accessProfile) ([]contract.AccessItem, error) {
	done := phase(q.baseCtx(), "accounts")
	as, err := accountRecs(q)
	done()
	if err != nil {
		return nil, err
	}
	if q.person != nil && !fullAccess(q.person) {
		as = slices.DeleteFunc(as, func(a accountRec) bool { return a.Holder != q.person.Email })
	}
	// The limit column needs each subscription's latest percent per period, not subscription_limits' history, burn
	// and projection: the latest readings (limit_reading_latest) answer it in a millisecond instead of reading a week
	// of limit reads. Same rule as subscription_limits' periods: a reading of the default window (limitsDefaults) whose
	// period has not reset. Latest is by event order (limit_reading_latest), as everywhere latestReadings is read.
	done = phase(q.baseCtx(), "readings")
	latest, err := latestReadings(withTx(q), q.app)
	done()
	if err != nil {
		return nil, err
	}
	now := nowMs()
	from := limitsDefaults(now).from
	left := map[string]contract.LimitPeriod{}
	for id, rs := range latest {
		left[id] = bindingPeriod(rs, from)
	}
	out := []contract.AccessItem{}
	for _, a := range as {
		out = append(out, accessSubscription(a, left[a.ID], profiles))
	}
	return out, nil
}

// bindingPeriod: the window closest to exhaustion among a subscription's current readings (percent used; a reading of
// the default window whose period has not reset): the highest percent used, and of equal ones the one that resets
// first. A fresh 5-hour window never hides a nearly full weekly one. Empty when no reading counts.
func bindingPeriod(rs []Reading, from int64) contract.LimitPeriod {
	var best *Reading
	for i := range rs {
		r := &rs[i]
		if r.Pct == nil || r.State != "known" || r.At < from {
			continue
		}
		resets := func(x *Reading) float64 {
			if x.ResetsAt == nil {
				return 1e18
			}
			return *x.ResetsAt
		}
		if best == nil || *r.Pct > *best.Pct || *r.Pct == *best.Pct && resets(r) < resets(best) {
			best = r
		}
	}
	if best == nil {
		return contract.LimitPeriod{}
	}
	pct, name := *best.Pct, best.Period
	if name == "" {
		name = "unknown" // as limitHistory names it
	}
	return contract.LimitPeriod{Period: name, Pct: &pct}
}

func accessSubscription(a accountRec, period contract.LimitPeriod, profiles []accessProfile) contract.AccessItem {
	kind := harnessLogin[a.Harness]
	if kind == "" {
		kind = a.Harness + " login"
	}
	it := contract.AccessItem{Id: "account:" + a.ID, Group: contract.AccessGroupSubscription, Name: a.Name, Provider: a.Harness, Kind: kind,
		Holder: strPtr(a.Holder), Intake: strPtr(a.Intake), Hosts: []contract.AccessHost{}}
	if n := firstString(deref(a.Comment), deref(a.Plan)); n != "" {
		it.Note = &n
	}
	ready, notReady := []string{}, []contract.AccessHost{}
	for _, b := range a.bindings() {
		h := contract.AccessHost{HostId: deref(b.HostID), Binding: deref(b.Binding), Readiness: firstString(deref(b.Readiness), "unknown")}
		if b.Models != nil {
			n := int64(len(b.Models))
			h.Models = &n
		}
		if lc, ok := jsonAs[struct {
			Reason string `json:"reason"`
		}](b.LastCheck); ok && lc.Reason != "" && lc.Reason != "ready" && lc.Reason != "login-started" {
			h.Reason = &lc.Reason
		}
		if at, ok := jsonAs[int64](b.ObservedAt); ok && at > 0 {
			h.ObservedAt = &at
		}
		h.Login = b.Login
		it.Hosts = append(it.Hosts, h)
		if h.Readiness == "ready" {
			ready = append(ready, h.HostId)
		} else {
			notReady = append(notReady, h)
		}
	}
	where := []string{}
	for _, h := range it.Hosts {
		where = append(where, h.HostId)
	}
	if len(where) > 0 {
		w := strings.Join(where, ", ")
		it.Where = &w
	}
	for _, p := range profiles {
		if p.Harness == a.Harness {
			it.UsedBy = append(it.UsedBy, p.ID)
		}
	}
	login := strings.Join(loginArgv[a.Harness], " ")
	it.Guide = &contract.AccessGuide{Login: &login}
	switch {
	case a.Intake == "paused":
		it.State, it.StateText = contract.AccessStatePaused, "Takes no new work (intake paused)"
		it.Next = &contract.AccessNext{Action: contract.AccessActionCheck, Label: "Use in work"}
	case len(it.Hosts) == 0:
		it.State, it.StateText = contract.AccessStateNeedsLogin, "No host is signed in yet"
		it.Next = &contract.AccessNext{Action: contract.AccessActionLogin, Label: "Sign in on a host"}
	case len(ready) == 0:
		h := notReady[0]
		it.State, it.StateText = contract.AccessStateNeedsLogin, "Needs a login on "+h.HostId
		if h.Reason != nil {
			it.StateText += " (" + *h.Reason + ")"
		}
		it.Next = &contract.AccessNext{Action: contract.AccessActionLogin, Label: "Sign in on " + h.HostId, HostId: &h.HostId, Binding: &h.Binding}
	default:
		it.State, it.StateText = contract.AccessStateWorks, "Works on "+strings.Join(ready, ", ")
		if len(notReady) > 0 {
			h := notReady[0]
			it.StateText += "; needs a login on " + h.HostId
			it.Next = &contract.AccessNext{Action: contract.AccessActionLogin, Label: "Sign in on " + h.HostId, HostId: &h.HostId, Binding: &h.Binding}
		}
	}
	if period.Pct != nil {
		pct := 100 - *period.Pct
		if pct < 0 {
			pct = 0
		}
		// The binding window by name and what is left of it ("week 6% left", as the approved System canvas words it).
		it.Limit = &contract.AccessLimit{Text: fmt.Sprintf("%s %.0f%% left", period.Period, pct), LeftPct: &pct}
	} else {
		it.Limit = &contract.AccessLimit{Text: "unknown"}
	}
	var latest int64
	for _, h := range it.Hosts {
		if h.ObservedAt != nil && *h.ObservedAt > latest {
			latest = *h.ObservedAt
		}
	}
	if latest > 0 {
		it.LastCheck = &contract.AccessCheck{At: latest, Ok: len(ready) > 0, Text: it.StateText}
	}
	return it
}

func accessConnections(q *Request, profiles []accessProfile) ([]contract.AccessItem, error) {
	done := phase(q.baseCtx(), "conns")
	conns, err := apiConnectionAll(q)
	done()
	if err != nil {
		return nil, err
	}
	done = phase(q.baseCtx(), "keys")
	keys, err := connKeysList(q, nil)
	done()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(conns))
	for _, c := range conns {
		names = append(names, c.Name)
	}
	done = phase(q.baseCtx(), "lastcalls")
	calls, err := q.db().AccessLastCalls(q.ctx(), names)
	done()
	if err != nil {
		return nil, err
	}
	last := map[string]db.AccessLastCallsRow{}
	for _, c := range calls {
		last[c.Connection] = c
	}
	done = phase(q.baseCtx(), "budgets")
	budgets, err := budgetsList(q, &contract.BudgetsListRequest{})
	done()
	if err != nil {
		return nil, err
	}
	has := map[string]bool{}
	out := []contract.AccessItem{}
	for _, c := range conns {
		has[c.Provider] = true
		ks := slices.DeleteFunc(slices.Clone(keys), func(k connKey) bool { return k.ConnectionID != c.Name })
		var b *contract.Budget
		for i := range budgets.Budgets {
			if x := budgets.Budgets[i]; x.Scope == "connection" && x.ScopeId == c.Name {
				b = &budgets.Budgets[i]
			}
		}
		out = append(out, accessConnection(c, ks, last[c.Name], b, profiles))
	}
	for _, p := range accessOffered {
		if has[p] {
			continue
		}
		g := accessGuides[p]
		out = append(out, contract.AccessItem{Id: "provider:" + p, Group: g.group, Name: g.label, Provider: p, Kind: "key",
			State: contract.AccessStateNotAdded, StateText: "Not added", Guide: g.view(),
			Next: &contract.AccessNext{Action: contract.AccessActionAddConnection, Label: "Add " + g.label}})
	}
	sort.SliceStable(out, func(i, j int) bool { return accessGroupOrder(out[i].Group) < accessGroupOrder(out[j].Group) })
	return out, nil
}

func accessGroupOrder(g contract.AccessGroup) int {
	return slices.Index([]contract.AccessGroup{contract.AccessGroupSubscription, contract.AccessGroupModelKey, contract.AccessGroupService}, g)
}

func (g accessGuide) view() *contract.AccessGuide {
	v := &contract.AccessGuide{}
	for _, f := range []struct {
		s string
		p **string
	}{{g.keyURL, &v.KeyUrl}, {g.billingURL, &v.BillingUrl}, {g.docsURL, &v.DocsUrl}, {g.note, &v.Note}} {
		if f.s != "" {
			s := f.s
			*f.p = &s
		}
	}
	return v
}

// creditStatus: a provider status that says the account's plan or credits ran out (Tavily: 432 plan limit, 433
// pay-as-you-go limit), so the owner tops up or upgrades instead of replacing the key.
func creditStatus(status int64) bool { return status == 432 || status == 433 }

func accessConnection(c apiConnection, keys []connKey, call db.AccessLastCallsRow, b *contract.Budget, profiles []accessProfile) contract.AccessItem {
	g, known := accessGuides[c.Provider]
	if !known {
		g = accessGuide{group: contract.AccessGroupService, label: c.Provider}
	}
	active, problem, credits := 0, "", false
	for _, k := range keys {
		if k.State == "active" {
			active++
		}
	}
	for _, k := range keys {
		switch k.State {
		case "active":
			// With one active key, a call that succeeded after the complaint (a top-up, a new billing cycle) went through
			// that key, so the complaint is over; with several, the success may be another key's.
			if k.Problem != nil && problem == "" && (active > 1 || k.Problem.At > call.OkStartedAt) {
				problem = fmt.Sprintf("Key %s: HTTP %d", k.Fingerprint, k.Problem.Status)
				if creditStatus(k.Problem.Status) {
					// An account problem, not a bad key: the plan or the credits ran out (Tavily 432 and 433).
					problem, credits = fmt.Sprintf("Key %s: the plan's credits are used up (HTTP %d)", k.Fingerprint, k.Problem.Status), true
				}
			}
		case "disabled":
			if problem == "" {
				problem = "Key " + k.Fingerprint + " disabled: " + deref(k.DisabledReason)
			}
		}
	}
	n := int64(active)
	it := contract.AccessItem{Id: "connection:" + c.Name, Group: g.group, Name: c.Name, Provider: c.Provider, Kind: "key",
		Holder: strPtr(c.Holder), Intake: strPtr(c.Intake), Keys: &n, Guide: g.view()}
	if c.Comment != "" {
		it.Note = &c.Comment
	}
	where := "only its holder's work"
	switch {
	case slices.Contains(c.Access.Projects, "*"):
		where = "all projects"
	case len(c.Access.Projects) > 0:
		where = "project " + strings.Join(c.Access.Projects, ", ")
	}
	it.Where = &where
	for _, p := range profiles {
		if slices.Contains(p.Connections, c.Name) {
			it.UsedBy = append(it.UsedBy, p.ID)
		}
	}
	if call.LastID != "" && slices.Contains([]string{"succeeded", "failed", "cancelled"}, call.LastState) {
		ok := call.LastState == "succeeded"
		text := "ok"
		if !ok {
			text = call.LastState
			if f := contract.PlainCause(call.LastProblem, ""); f != nil {
				text = f.Text + " (" + call.LastProblem + ")"
			}
		}
		chk := &contract.AccessCheck{At: call.LastStartedAt, Ok: ok, Text: text, Tool: strPtr(call.LastTool)}
		if call.OkStartedAt > 0 {
			chk.LastOkAt = &call.OkStartedAt
		}
		it.LastCheck = chk
	}
	switch {
	case active == 0:
		it.State, it.StateText = contract.AccessStateNeedsKey, "No active key"
		it.Next = &contract.AccessNext{Action: contract.AccessActionAddKey, Label: "Paste a key", Url: it.Guide.KeyUrl}
	case c.Intake == "paused":
		it.State, it.StateText = contract.AccessStatePaused, "Takes no new calls (intake paused)"
	case problem != "" && credits:
		it.State, it.StateText = contract.AccessStateFailing, problem
		// Tavily's dashboard (the key page) shows the plan, the credits left and the upgrade.
		it.Next = &contract.AccessNext{Action: contract.AccessActionBilling, Label: "Top up or upgrade the plan", Url: it.Guide.KeyUrl}
		if it.Guide.BillingUrl != nil {
			it.Next.Url = it.Guide.BillingUrl
		}
	case problem != "":
		it.State, it.StateText = contract.AccessStateFailing, problem
		it.Next = &contract.AccessNext{Action: contract.AccessActionAddKey, Label: "Replace the key", Url: it.Guide.KeyUrl}
	case it.LastCheck != nil && !it.LastCheck.Ok:
		it.State, it.StateText = contract.AccessStateFailing, "Last call failed: "+it.LastCheck.Text
		it.Next = &contract.AccessNext{Action: contract.AccessActionCheck, Label: "Check again"}
		if (strings.Contains(call.LastProblem, "429") || strings.Contains(call.LastProblem, "432") || strings.Contains(call.LastProblem, "433")) && it.Guide.BillingUrl != nil {
			it.Next = &contract.AccessNext{Action: contract.AccessActionBilling, Label: "Check billing", Url: it.Guide.BillingUrl}
		}
	case it.LastCheck == nil:
		it.State, it.StateText = contract.AccessStateUnknown, "Not checked yet"
		it.Next = &contract.AccessNext{Action: contract.AccessActionCheck, Label: "Check"}
	default:
		it.State, it.StateText = contract.AccessStateWorks, "Works"
	}
	if b != nil {
		v := contract.AccessBudget{Id: b.Id, Limit: b.Limit, Unit: b.Unit, Period: b.Period}
		text := fmt.Sprintf("limit %s %s", strconv.FormatFloat(b.Limit, 'f', -1, 64), b.Unit)
		if b.Period != nil {
			text += " per " + *b.Period
		}
		if b.Spend != nil && b.Spend.Value != nil {
			v.Spent = b.Spend.Value
			text = fmt.Sprintf("%s of %s", strconv.FormatFloat(*b.Spend.Value, 'f', 0, 64), text)
		}
		it.Limit = &contract.AccessLimit{Text: text, Budget: &v}
	}
	return it
}

// accessCheck: one item now, read-only (a long operation: the gateway call holds no transaction). A connection: its
// provider's cheap call through the gateway (recorded as a tool_run like any other); a subscription: a status inspection
// of each host login. An Antigravity login is inspected only where an earlier probe found it signed in: agy may open a
// browser sign-in for a login it lacks, and a check never starts a login.
func accessCheck(q *Request, r *contract.AccessCheckRequest) (contract.AccessCheckReply, error) {
	var no contract.AccessCheckReply
	kind, id, _ := strings.Cut(r.Id, ":")
	read := func(fn func(q *Request) error) error { return q.app.store.Read(q.ctx(), q, fn) }
	switch kind {
	case "account":
		queued := false
		if err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
			a, err := accountGet(q, id)
			if err != nil {
				return err
			}
			if q.person != nil && !fullAccess(q.person) && q.person.Email != a.Holder {
				return fail(403, "Only the holder or the owner checks this subscription")
			}
			for _, b := range a.bindings() {
				if a.Harness == "antigravity" && len(b.Models) == 0 {
					continue
				}
				if _, err := accountInspect(q, a.ID, deref(b.HostID), b.Binding); err != nil {
					return err
				}
				queued = true
			}
			return nil
		}); err != nil {
			return no, err
		}
		var it contract.AccessItem
		err := read(func(q *Request) (err error) { it, err = accessItem(q, r.Id); return })
		return contract.AccessCheckReply{Item: it, Queued: queued}, err
	case "forge": // the host checks its forge login itself (forge_login.go); this reads its latest answer
		var it contract.AccessItem
		err := read(func(q *Request) (err error) { it, err = accessItem(q, r.Id); return })
		return contract.AccessCheckReply{Item: it}, err
	case "connection":
		var c apiConnection
		var it contract.AccessItem
		if err := read(func(q *Request) (err error) {
			if c, err = connNamed(q, id); err == nil {
				it, err = accessItem(q, r.Id)
			}
			return
		}); err != nil {
			return no, err
		}
		if it.State == contract.AccessStateNeedsKey {
			return contract.AccessCheckReply{Item: it}, nil // nothing to call with
		}
		g := accessGuides[c.Provider]
		if g.probe == "" {
			return no, fail(409, "Fleet has no read-only check for "+c.Provider+"; its last call shows whether it works")
		}
		if !slices.Contains(c.Tools, g.probe) {
			// A connection made before its provider gained the check tool lacks it: the holder or owner grants this
			// read-only tool (recorded as a tools change, as connections_set_tools does); anyone else gets the reason.
			if q.person != nil && !fullAccess(q.person) && q.person.Email != c.Holder {
				return no, fail(409, c.Name+" does not grant its check tool "+g.probe+"; its holder adds it under Tools")
			}
			if err := q.app.store.Write(q.ctx(), q, []string{GLOBAL}, func(q *Request) error {
				_, err := connectionsSetTools(q, &contract.ConnectionsSetToolsRequest{Connection: c.Name, Tools: append(slices.Clone(c.Tools), g.probe)})
				return err
			}); err != nil {
				return no, err
			}
		}
		project := ""
		if !slices.Contains(c.Access.Projects, "*") && len(c.Access.Projects) > 0 {
			project = c.Access.Projects[0]
		}
		if err := accessProbe(q, c, project, g); err != nil {
			return no, err
		}
		err := read(func(q *Request) (err error) { it, err = accessItem(q, r.Id); return })
		return contract.AccessCheckReply{Item: it}, err
	}
	return no, fail(400, "Expected an access_status item id (account:<id> or connection:<name>)")
}

// accessProbe: the provider's check call, outside the request's transaction (a gateway call must not hold it). Its
// outcome is a tool_run, which publishes no change, so a change on the connection then tells open access_status reads.
func accessProbe(q *Request, c apiConnection, project string, g accessGuide) error {
	ctx, cancel := context.WithTimeout(q.ctx(), time.Minute)
	defer cancel()
	token, release, err := readTokenFor(ctx, q.app, c.Name, project)
	if err != nil {
		return fail(409, err.Error())
	}
	defer release()
	gatewayCall(ctx, token, c.Name, g.probe, g.probeArgs, true)
	if _, err = connIngestPass(ctx, q.app); err != nil {
		return err
	}
	return q.app.store.Write(ctx, nil, nil, func(q *Request) error {
		return q.db().AccessConnectionChecked(q.ctx(), db.AccessConnectionCheckedParams{ID: c.ID, Holder: strPtr(c.Holder)})
	})
}

// Keys added since the last pass of accessProbeAddedKeys (connections_key_add, by any path: Access, Keys, a key
// request's answer): each is checked at once, so a pasted key shows works or its failure without a manual check.
var addedKeys = struct {
	sync.Mutex
	names map[string]bool
}{names: map[string]bool{}}

// accessKeyAdded queues connection's check once the key's transaction committed; only the running coordinator
// checks (tests and tools have no gateway to call).
func accessKeyAdded(q *Request, c apiConnection) {
	if accessGuides[c.Provider].probe == "" {
		return
	}
	afterCommit(q, func() {
		a := runningApp
		if a == nil {
			return
		}
		addedKeys.Lock()
		addedKeys.names[c.Name] = true
		addedKeys.Unlock()
		accessProbeAddedKeys(a)
	})
}

var accessProbeAddedKeys = coalesced("access.added-keys", func(ctx context.Context, a *App) error {
	addedKeys.Lock()
	names := slices.Sorted(maps.Keys(addedKeys.names))
	clear(addedKeys.names)
	addedKeys.Unlock()
	// The gateway reads keys from the snapshot: write it now, not after the publish burst, so the check uses the new key.
	if err := writeSnapshot(ctx, a); err != nil {
		return err
	}
	var errs []error
	for _, name := range names {
		if _, err := accessCheck(backgroundRequest(ctx, a, nil), &contract.AccessCheckRequest{Id: "connection:" + name}); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
})

func accessItem(q *Request, id string) (contract.AccessItem, error) {
	s, err := accessStatus(q)
	if err != nil {
		return contract.AccessItem{}, err
	}
	for _, it := range s.Items {
		if it.Id == id {
			return it, nil
		}
	}
	return contract.AccessItem{}, errNotFound
}

func accessOnboard(q *Request, r *contract.AccessOnboardRequest) (contract.AccessOnboardReply, error) {
	switch r.Kind {
	case contract.AccessOnboardKindKey:
		return accessOnboardKey(q, r)
	case contract.AccessOnboardKindLogin:
		return accessOnboardLogin(q, r)
	case contract.AccessOnboardKindLimit:
		return accessOnboardLimit(q, r)
	}
	return contract.AccessOnboardReply{}, fail(400, "kind: key, login or limit")
}

func accessOnboardKey(q *Request, r *contract.AccessOnboardRequest) (contract.AccessOnboardReply, error) {
	var no contract.AccessOnboardReply
	provider := deref(r.Provider)
	if _, known := gatewayProviders[provider]; !known {
		return no, fail(400, "Expected a known provider")
	}
	name := firstString(deref(r.Connection), provider)
	c, err := apiConnectionMaybe(q, "connection:"+name)
	if err != nil {
		return no, err
	}
	created := false
	if c == nil {
		if q.person != nil && !fullAccess(q.person) {
			return no, fail(403, "Only the owner adds a connection; ask the owner")
		}
		v, err := connectionsCreate(q, &contract.ConnectionsCreateRequest{Name: name, Provider: provider})
		if err != nil {
			return no, err
		}
		nc := v.(apiConnection)
		nc.Access = connAccess{Projects: []string{"*"}}.stored() // the owner's own keys serve all work, as an import does
		if err := apiConnectionPut(q, nc); err != nil {
			return no, err
		}
		c, created = &nc, true
	} else if c.Provider != provider {
		return no, fail(409, "Connection "+name+" is a "+c.Provider+" connection")
	}
	g := accessGuides[provider]
	out := contract.AccessOnboardReply{Kind: string(r.Kind), Connection: &c.Name, Created: &created, Guide: g.view()}
	if g.keyURL != "" {
		out.Steps = append(out.Steps, "Open "+g.keyURL+" and create a key")
	}
	if g.billingURL != "" {
		out.Steps = append(out.Steps, "Make sure billing or credits are on: "+g.billingURL)
	}
	out.Steps = append(out.Steps, "Paste the key on System › Access › "+c.Name+" (Add and check); Fleet checks it at once")
	if r.ProjectId != nil && r.RequestId != nil {
		if q.person != nil && !fullAccess(q.person) && !hasRole(q.person, "architect") {
			return no, fail(403, "Asking the owner for a key needs the architect role")
		}
		v, err := connectionsKeyRequest(q, &contract.ConnectionsKeyRequestRequest{ProjectId: *r.ProjectId, RequestId: *r.RequestId, Connection: c.Name, Reason: r.Reason})
		if err != nil {
			return no, err
		}
		if kr, ok := v.(keyRequestReply); ok {
			out.QuestionId = &kr.ID
		} else if raw, ok := v.(json.RawMessage); ok {
			if x, ok := jsonAs[struct {
				ID string `json:"id"`
			}](raw); ok {
				out.QuestionId = &x.ID
			}
		}
	}
	return out, nil
}

func accessOnboardLogin(q *Request, r *contract.AccessOnboardRequest) (contract.AccessOnboardReply, error) {
	var no contract.AccessOnboardReply
	if r.AccountId == nil || r.HostId == nil || r.Binding == nil {
		return no, fail(400, "Expected accountId, hostId and binding")
	}
	a, err := accountGet(q, *r.AccountId)
	if err != nil {
		return no, err
	}
	h, err := accountLoginHandoff(q, &contract.AccountLoginHandoffRequest{AccountId: a.ID, HostId: *r.HostId, Binding: r.Binding})
	if err != nil {
		return no, err
	}
	login := strings.Join(loginArgv[a.Harness], " ")
	if r.Start != nil && *r.Start {
		// Only the holder or the owner starts a login, from the Sign in button; an agent asks the owner (below) instead.
		if q.agent != "" || (q.person != nil && !fullAccess(q.person) && q.person.Email != a.Holder) {
			return no, fail(403, "Only the holder or the owner starts a login")
		}
		if _, err := accountInspectMode(q, a.ID, h.HostId, r.Binding, "login"); err != nil {
			return no, err
		}
		started := true
		return contract.AccessOnboardReply{Kind: string(r.Kind), Started: &started, Guide: &contract.AccessGuide{Login: &login},
			Steps: []string{"Fleet started " + login + " on " + h.HostId + "; the sign-in link and code show here in a few seconds", "Open the link, enter the code and sign in as " + a.Name, "Press Check: Fleet reads who is signed in, the models and the quota"}}, nil
	}
	out := contract.AccessOnboardReply{Kind: string(r.Kind), Guide: &contract.AccessGuide{Login: &login},
		Steps: []string{"On System › Access, press Sign in on " + a.Name + " · " + h.HostId, "Open the link and enter the code Fleet shows; sign in as " + a.Name}}
	if text, ok := jsonAs[string](h.RequiredUserInteraction); ok && text != "" {
		out.Steps = append(out.Steps, text)
	}
	if r.ProjectId != nil && r.RequestId != nil {
		text := fmt.Sprintf("Sign %s (%s) in on host %s: System › Access, Sign in", a.Name, a.Harness, h.HostId)
		if r.Reason != nil && *r.Reason != "" {
			text += ". " + *r.Reason
		}
		rid := "access-login:" + *r.RequestId
		ask := questionAsk{Kind: string(contract.QuestionKindOperatorDecision), Text: text, ProjectID: *r.ProjectId, RequestID: &rid,
			Options: jsonBytes([]string{"Signed in", "Not now"}),
			Data: jsonBytes(struct {
				Access map[string]string `json:"access"`
			}{map[string]string{"accountId": a.ID, "hostId": h.HostId, "binding": *r.Binding}})}
		if strings.HasPrefix(q.agent, "execution:") {
			eid := strings.TrimPrefix(q.agent, "execution:")
			ask.ExecutionID = &eid
		}
		id, err := askQuestionAs(q, ask)
		if err != nil {
			return no, err
		}
		out.QuestionId = &id
	}
	return out, nil
}

func accessOnboardLimit(q *Request, r *contract.AccessOnboardRequest) (contract.AccessOnboardReply, error) {
	var no contract.AccessOnboardReply
	if q.person != nil && !fullAccess(q.person) {
		return no, fail(403, "Only the owner sets a limit")
	}
	if r.Connection == nil || r.Limit == nil || r.Limit.Amount <= 0 || r.Limit.Unit == "" {
		return no, fail(400, "Expected connection and limit {amount > 0, unit}")
	}
	c, err := connNamed(q, *r.Connection)
	if err != nil {
		return no, err
	}
	period := firstString(deref(r.Limit.Period), "month")
	req := contract.BudgetSetRequest{Scope: "connection", ScopeId: c.Name, Unit: r.Limit.Unit, Limit: r.Limit.Amount, Period: &period}
	bs, err := budgetsList(q, &contract.BudgetsListRequest{})
	if err != nil {
		return no, err
	}
	for _, b := range bs.Budgets {
		if b.Scope == "connection" && b.ScopeId == c.Name && b.Unit == r.Limit.Unit {
			id := b.Id
			req.Id = &id
		}
	}
	b, err := budgetSet(q, &req)
	if err != nil {
		return no, err
	}
	return contract.AccessOnboardReply{Kind: string(r.Kind), Connection: &c.Name, BudgetId: &b.Id,
		Steps: []string{fmt.Sprintf("Fleet counts %s's %s against %s per %s and warns when it is reached", c.Name, r.Limit.Unit, strconv.FormatFloat(r.Limit.Amount, 'f', -1, 64), period)}}, nil
}

// accountDelete: a subscription nothing uses (no host binding, inspection, check or run names it).
func accountDelete(q *Request, r *contract.AccountDeleteRequest) (contract.AccountDeleteReply, error) {
	out := contract.AccountDeleteReply{AccountId: r.AccountId}
	if q.person != nil && !fullAccess(q.person) {
		return out, fail(403, "Only the owner deletes a subscription")
	}
	a, err := accountGet(q, r.AccountId)
	if err != nil {
		return out, err
	}
	refs, err := q.db().AccountDeleteRefs(q.ctx(), a.ID)
	if err != nil {
		return out, err
	}
	reason := ""
	switch {
	case len(a.bindings()) > 0:
		reason = fmt.Sprintf("%d host binding(s)", len(a.bindings()))
	case refs.Inspections > 0:
		reason = fmt.Sprintf("%d inspection(s)", refs.Inspections)
	case refs.Checks > 0:
		reason = fmt.Sprintf("%d check(s)", refs.Checks)
	case refs.Runs > 0:
		reason = fmt.Sprintf("%d run(s)", refs.Runs)
	}
	if reason != "" {
		reason = a.Name + " is in use: " + reason
		out.Refused = &reason
		if r.Preview != nil && *r.Preview {
			return out, nil
		}
		return out, fail(409, reason)
	}
	if r.Preview != nil && *r.Preview {
		return out, nil
	}
	if _, err := q.db().AccountDelete(q.ctx(), a.ID); err != nil {
		return out, err
	}
	if err := audit(q, "deleted-account:"+a.ID, "account", a.Name, "deleted", auditExtra{}); err != nil {
		return out, err
	}
	out.Deleted = true
	return out, nil
}
