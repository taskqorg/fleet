package main

// subscriptions and their host bindings (docs/designs/fleet-people-access.md). A subscription is a 'managed-account' document; logins stay
// on the holder's host, Fleet keeps only what hosts observe.

import (
	"fleet/contract"
	"fmt"
	"os"
	"regexp"
	"strings"
)

var accountName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

func init() {
	operation("account_list", true, shaped[contract.AccountListRequest, contract.AccountListReply](accountList))
	operation("account_create", false, shaped[contract.AccountCreateRequest, contract.AccountCreateReply](accountCreate))
	operation("account_hosts", true, shaped[contract.AccountHostsRequest, contract.AccountHostsReply](accountHostsOp))
	operation("account_discover", false, shaped[contract.AccountDiscoverRequest, contract.AccountInspection](func(q *Request) (any, error) { return accountInspect(q, q.obj(), true) }))
	operation("account_connect", false, shaped[contract.AccountConnectRequest, contract.AccountInspection](func(q *Request) (any, error) { return accountInspect(q, q.obj(), false) }))
	operation("account_login_handoff", true, shaped[contract.AccountLoginHandoffRequest, contract.AccountLoginHandoffReply](accountLoginHandoff))
	operation("account_update", false, shaped[contract.AccountUpdateRequest, contract.AccountUpdateReply](accountUpdate))
	route("POST /api/hosts/accounts", typed(func(q *Request, b *contract.AccountObservation) (any, error) {
		if !q.app.hostMaySee(q.host, nil) {
			return nil, fail(403, "Account observations need an owner-held host")
		}
		return accountReport(q, b, q.hostID())
	}))
	route("GET /api/accounts", accountList)
	route("POST /api/accounts", accountCreate)
	route("POST /api/accounts/{id}", func(q *Request) (any, error) {
		b := q.obj()
		b["accountId"] = q.r.PathValue("id")
		return accountUpdateBody(q, b)
	})
}

func defaultHostID(q *Request) string { return q.app.hostID }

func defaultAccountBinding() string { return os.Getenv("FLEET_DEFAULT_ACCOUNT_BINDING") }

func harnessAllowed(q *Request, host, harness string) (bool, error) {
	h, err := hostByID(q, host)
	if err != nil || h == nil {
		return true, err
	}
	hs, _ := h["harnesses"].(map[string]any)
	x, _ := hs[harness].(map[string]any)
	return x["allowed"] != false, nil
}

func bindingsOf(a doc) []map[string]any {
	out := []map[string]any{}
	for _, x := range anys(a["bindings"]) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// accountHosts: the hosts a subscription of this harness may bind to, with their inspector and readiness.
func accountHosts(q *Request, harness string) ([]doc, error) {
	ids := []string{}
	pub, err := hostWithRole(q, "publication")
	if err != nil {
		return nil, err
	}
	exec, err := executionHostIDs(q)
	if err != nil {
		return nil, err
	}
	for _, id := range append([]string{defaultHostID(q), pub}, exec...) {
		if id != "" && !inList(ids, id) {
			ids = append(ids, id)
		}
	}
	accounts, err := all(q, "managed-account", nil)
	if err != nil {
		return nil, err
	}
	out := []doc{}
	for _, id := range ids {
		inspector, err := hostWithRole(q, "inspector-for:"+id)
		if err != nil {
			return nil, err
		}
		look := id
		if inspector != "" {
			look = inspector
		}
		beat, err := maybe(q, look, "host-observation")
		if err != nil {
			return nil, err
		}
		var previous map[string]any
		for _, a := range accounts {
			for _, b := range bindingsOf(a) {
				if previous == nil && b["hostId"] == id {
					previous = b
				}
			}
		}
		platform := "unknown"
		if beat != nil && beat["platform"] != nil {
			platform = astr(beat["platform"])
		} else if previous != nil && previous["platform"] != nil {
			platform = astr(previous["platform"])
		}
		allowed, err := harnessAllowed(q, id, harness)
		if err != nil {
			return nil, err
		}
		var seen any
		connectivity := "unknown"
		if beat != nil {
			eff := q.app.effectiveHostSeen(look, num(beat["lastSeenAt"]))
			if eff > 0 {
				seen = eff
			} else {
				seen = beat["lastSeenAt"]
			}
			connectivity = "offline"
			if float64(nowMs())-eff < 30000 {
				connectivity = "online"
			}
		}
		reason := "Local Mac inspector; connectivity is not authentication"
		switch {
		case !allowed:
			reason = fmt.Sprintf("%s is not allowed on this host (host setting harnesses.%s.allowed)", harness, harness)
		case inList(exec, id):
			reason = "Native executor host; accounts bound by host observation, login on the host"
		case inspector == "":
			reason = "Remote native executable/account not provisioned for onboarding"
		case platform != "darwin":
			reason = "Await host platform/readiness metadata"
		}
		var insp any
		if inspector != "" {
			insp = inspector
		}
		out = append(out, doc{"id": id, "inspector": insp, "platform": platform, "lastSeenAt": seen, "connectivity": connectivity, "eligible": allowed,
			"onboardingSupported": inList(exec, id) || (inspector != "" && platform == "darwin"), "reason": reason})
	}
	return out, nil
}

func accountHostsOp(q *Request) (any, error) {
	b := q.obj()
	harness := astr(nonNil(b["harness"], "codex"))
	if b["accountId"] != nil {
		a, err := get(q, b["accountId"], "managed-account")
		if err != nil {
			return nil, err
		}
		harness = astr(a["harness"])
	}
	hs, err := accountHosts(q, harness)
	return doc{"hosts": hs}, err
}

// Retained inspections predate host_job and may still be tool_run; the general execution reader excludes those.
func accountInspectionExecution(q *Request, inspection doc) (doc, error) {
	return one(q, `SELECT e.data || jsonb_build_object('state',e.state,'observation',e.observation)
 FROM execution e JOIN document i ON i.id=$1 AND i.kind='account-inspection' AND e.id=i.data->>'executionId'
 WHERE e.kind IN ('host_job','tool_run') AND e.data->>'actor'='program-worker'
 AND e.data->>'accountId'=i.data->>'accountId' AND e.project_id IS NOT DISTINCT FROM i.project_id`, inspection["id"])
}

func accountList(q *Request) (any, error) {
	cats, err := all(q, "account-catalogue", nil)
	if err != nil {
		return nil, err
	}
	as, err := all(q, "managed-account", nil)
	if err != nil {
		return nil, err
	}
	changes, err := all(q, "setting-change", nil)
	if err != nil {
		return nil, err
	}
	for _, a := range as {
		ins, err := all(q, "account-inspection", doc{"accountId": a["id"]})
		if err != nil {
			return nil, err
		}
		for _, i := range ins {
			e, err := accountInspectionExecution(q, i)
			if err != nil {
				return nil, err
			}
			i["state"], i["error"] = e["state"], nonNil(e["error"])
		}
		checks, err := all(q, "account-check", doc{"accountId": a["id"]})
		if err != nil {
			return nil, err
		}
		history := []doc{}
		for _, h := range changes {
			if h["target"] == "account:"+astr(a["id"]) {
				history = append(history, h)
			}
		}
		a["inspections"], a["checks"], a["history"] = ins, checks, history
	}
	return doc{"catalogues": cats, "accounts": as}, nil
}

var loginCommands = map[string]string{"codex": "codex login --device-auth", "claude": "claude auth login --claudeai",
	"antigravity": "agy acp --login (may replace the shared native principal; explicit review required)", "acp": "the bound agent's own login command"}

func accountCreate(q *Request) (any, error) {
	b := q.obj()
	harness := astr(b["harness"])
	ok := accountName.MatchString(astr(b["name"])) && inList([]string{"codex", "claude", "antigravity", "acp"}, harness)
	for k := range b {
		if !inList([]string{"name", "harness", "holder", "plan", "comment"}, k) {
			ok = false
		}
	}
	if !ok {
		return nil, fail(400, "Expected account name and harness")
	}
	same, err := rows(q, "SELECT data FROM document WHERE kind='managed-account' AND data->>'name'=$1", b["name"])
	if err != nil {
		return nil, err
	}
	if len(same) > 0 {
		return nil, fail(409, "Account name exists")
	}
	director := q.person.Email
	holder := director
	if s, ok := b["holder"].(string); ok {
		holder = strings.ToLower(s)
	}
	if holder != director && !fullAccess(q.person) {
		return nil, fail(403, "Only the owner creates a subscription for another person")
	}
	text := func(k string) string { s, _ := b[k].(string); return s }
	a := doc{"id": newID(), "name": b["name"], "harness": harness, "holder": holder, "kind": "personal", "plan": text("plan"), "intake": "all",
		"take": doc{"projects": []any{}}, "switchPct": nil, "comment": text("comment"), "createdAt": nowMs(), "bindings": []any{},
		"login": "Native login on the holder's host, under its protected binding environment: " + loginCommands[harness] + ". Never paste codes or tokens into Fleet. Inspect an existing authorized login first; creation does not authenticate."}
	return a, put(q, "managed-account", a)
}

func integrationHost(q *Request) (string, error) {
	return hostWithRole(q, "inspector-for:"+defaultHostID(q))
}

// accountInspect: a read-only inspection command to the selected host or its inspector.
func accountInspect(q *Request, b doc, catalogue bool) (any, error) {
	allowedKeys := []string{"accountId", "hostId", "binding"}
	if catalogue {
		allowedKeys = allowedKeys[:2]
	}
	for k := range b {
		if !inList(allowedKeys, k) {
			return nil, fail(400, "Only host/account/binding identifiers accepted")
		}
	}
	a, err := get(q, b["accountId"], "managed-account")
	if err != nil {
		return nil, err
	}
	host := astr(nonNil(b["hostId"], defaultHostID(q)))
	integ, err := hostWithRole(q, "inspector-for:"+host)
	if err != nil {
		return nil, err
	}
	if integ == "" {
		integ = host
	}
	hs, err := accountHosts(q, astr(a["harness"]))
	if err != nil {
		return nil, err
	}
	var h doc
	for _, x := range hs {
		if x["id"] == host {
			h = x
		}
	}
	switch {
	case h == nil:
		return nil, fail(404, "Unknown host")
	case h["eligible"] != true:
		return nil, fail(409, astr(h["reason"]))
	case h["connectivity"] != "online":
		return nil, fail(409, "Host "+astr(h["connectivity"])+"; native login/readiness unknown")
	case h["onboardingSupported"] != true:
		return nil, fail(409, astr(h["reason"]))
	}
	var binding any
	if !catalogue {
		if !accountName.MatchString(astr(b["binding"])) {
			return nil, fail(400, "Expected host-owned binding identifier")
		}
		binding = b["binding"]
		others, err := all(q, "managed-account", nil)
		if err != nil {
			return nil, err
		}
		for _, o := range others {
			for _, x := range bindingsOf(o) {
				if o["id"] != a["id"] && x["hostId"] == host && x["binding"] == binding {
					return nil, fail(409, "Host binding already belongs to another managed account")
				}
			}
		}
	}
	olds, err := all(q, "account-inspection", nil)
	if err != nil {
		return nil, err
	}
	for _, x := range olds {
		if x["accountId"] == a["id"] && x["hostId"] == host && x["binding"] == binding {
			e, err := accountInspectionExecution(q, x)
			if err != nil {
				return nil, err
			}
			if !terminal(e) && !unconfirmed(e) {
				return x, nil
			}
		}
	}
	sid, id := newID(), newID()
	mode := "status"
	if catalogue {
		mode = "catalogue"
	}
	if err := put(q, "session", doc{"id": sid, "createdAt": nowMs(), "actor": "program-worker"}); err != nil {
		return nil, err
	}
	if err := put(q, "execution", doc{"id": id, "executionId": id, "sessionId": sid, "hostId": integ, "state": "preparing", "createdAt": nowMs(), "actor": "program-worker", "accountId": a["id"]}); err != nil {
		return nil, err
	}
	if err := put(q, "command", doc{"id": newID(), "executionId": id, "sessionId": sid, "hostId": integ, "kind": string(contract.CommandKindAccountInspect), "status": "pending",
		"payload": contract.AccountInspectPayload{AccountId: astr(a["id"]), Harness: astr(a["harness"]), Binding: strPtr(binding), NativeHostId: strPtr(host), Mode: &mode}}); err != nil {
		return nil, err
	}
	r := doc{"id": newID(), "accountId": a["id"], "hostId": host, "binding": binding, "executionId": id, "createdAt": nowMs(),
		"loginAction": "Read-only inspection; no authentication changes. Await host result, then explicit login handoff only if needed."}
	return r, put(q, "account-inspection", r)
}

var loginArgv = map[string]any{"codex": []any{"login", "--device-auth"}, "claude": []any{"auth", "login", "--claudeai"}, "antigravity": []any{"acp", "--login"}, "acp": nil}

func accountLoginHandoff(q *Request) (any, error) {
	b := q.obj()
	a, err := get(q, b["accountId"], "managed-account")
	if err != nil {
		return nil, err
	}
	hs, err := accountHosts(q, astr(a["harness"]))
	if err != nil {
		return nil, err
	}
	var host doc
	for _, h := range hs {
		if h["id"] == b["hostId"] {
			host = h
		}
	}
	if host == nil || host["eligible"] != true || host["onboardingSupported"] != true || host["connectivity"] != "online" {
		return nil, fail(409, "No eligible supported online native login host")
	}
	cats, err := all(q, "account-catalogue", doc{"hostId": host["id"], "harness": a["harness"]})
	if err != nil {
		return nil, err
	}
	var binding map[string]any
	if len(cats) > 0 {
		for _, x := range anys(cats[0]["bindings"]) {
			if m, _ := x.(map[string]any); m["name"] == b["binding"] {
				binding = m
			}
		}
	}
	if binding == nil {
		return nil, fail(409, "Discover a protected host binding first; browser cannot supply roots")
	}
	var existing map[string]any
	for _, x := range bindingsOf(a) {
		if x["hostId"] == host["id"] && x["binding"] == b["binding"] {
			existing = x
		}
	}
	checks, err := all(q, "account-check", doc{"accountId": a["id"], "hostId": host["id"], "binding": b["binding"]})
	if err != nil {
		return nil, err
	}
	var latest doc
	if len(checks) > 0 {
		latest = checks[len(checks)-1]
	}
	reason, basedOn := any(nil), any(nil)
	if latest != nil {
		reason, basedOn = latest["reason"], latest["observedAt"]
	}
	if existing != nil {
		if reason == nil {
			if lc, ok := existing["lastCheck"].(map[string]any); ok {
				reason = lc["reason"]
			}
		}
		basedOn = nonNil(basedOn, existing["observedAt"])
	}
	needed := reason == "login-absent"
	var command, warning any
	interaction := "Login need is not established: inspect status first; missing executable/config requires host provisioning."
	if needed {
		command = loginArgv[astr(a["harness"])]
		interaction = "On the selected Mac, run this executable/argv with exactly the displayed host-owned environment, complete the native flow locally, then Recheck. Never paste credentials/codes into Fleet."
	} else if existing != nil && existing["readiness"] == "ready" {
		interaction = "Existing ready binding: no login needed; recheck metadata only."
	}
	if a["harness"] == "antigravity" {
		warning = "Native-current login may replace the shared principal; explicit operator review before any login."
	}
	return doc{"observedAt": nowMs(), "basedOnCheckAt": basedOn, "hostId": host["id"], "binding": b["binding"], "environment": binding["environment"], "executable": binding["executable"],
		"command": command, "requiredUserInteraction": interaction, "warning": warning, "quota": "unknown", "authChanged": false}, nil
}

func accountUpdate(q *Request) (any, error) { return accountUpdateBody(q, q.obj()) }

func accountUpdateBody(q *Request, b doc) (any, error) {
	a, err := get(q, b["accountId"], "managed-account")
	if err != nil {
		return nil, err
	}
	p := q.person
	if p != nil && !fullAccess(p) && p.Email != a["holder"] {
		return nil, fail(403, "Only the holder or the owner changes this subscription")
	}
	next := doc{}
	order := []string{}
	set := func(k string, v any) { next[k] = v; order = append(order, k) }
	if v, ok := b["intake"]; ok && v != nil {
		if v != "all" && v != "paused" {
			return nil, fail(400, "intake: all or paused")
		}
		set("intake", v)
	}
	for _, k := range []string{"plan", "comment"} {
		if v, ok := b[k]; ok && v != nil {
			s, isStr := v.(string)
			if !isStr || len(s) > 2000 {
				return nil, fail(400, k+": text")
			}
			set(k, s)
		}
	}
	if v, ok := b["switchPct"]; ok {
		if f, isNum := v.(float64); v != nil && !(isNum && f > 0 && f <= 100) {
			return nil, fail(400, "switchPct: 1-100 or null")
		}
		set("switchPct", v)
	}
	// fleet-people-access.md section 8: planWeight (default 1) weights the pool; lowPct (default off) marks Low.
	if v, ok := b["planWeight"]; ok {
		// Bounded so the weighted pool sums stay finite numbers; no plan is a trillion base plans.
		if f, isNum := v.(float64); v != nil && !(isNum && f > 0 && f <= 1e12) {
			return nil, fail(400, "planWeight: a positive number up to 1e12, or null (1)")
		}
		set("planWeight", v)
	}
	if v, ok := b["lowPct"]; ok {
		if f, isNum := v.(float64); v != nil && !(isNum && f > 0 && f <= 100) {
			return nil, fail(400, "lowPct: 1-100 or null (off)")
		}
		set("lowPct", v)
	}
	if v, ok := b["take"]; ok && v != nil {
		t, _ := v.(map[string]any)
		ps, isList := t["projects"].([]any)
		if !isList {
			return nil, fail(400, "take: {projects: [ids]}")
		}
		holder := q.peopleSnapshot().person(astr(a["holder"]))
		uniq := []any{}
		for _, x := range ps {
			s, isStr := x.(string)
			if !isStr {
				return nil, fail(400, "take: {projects: [ids]}")
			}
			if holder == nil || !holder.mayAccess(s) {
				return nil, fail(400, fmt.Sprintf("%s has no grant on %s; a take needs one", a["holder"], s))
			}
			if !inList(strs(uniq), s) {
				uniq = append(uniq, s)
			}
		}
		set("take", doc{"projects": uniq})
	}
	if v, ok := b["holder"]; ok && v != nil {
		if p != nil && !fullAccess(p) {
			return nil, fail(403, "Only the owner changes the holder")
		}
		h := strings.ToLower(astr(v))
		if q.peopleSnapshot().person(h) == nil {
			return nil, fail(400, "Holder is not a person")
		}
		set("holder", h)
	}
	actor := "operator"
	if p != nil {
		actor = p.Email
	}
	changed := []string{}
	for _, f := range order {
		if js(a[f]) == js(next[f]) {
			continue
		}
		changed = append(changed, f)
		if err := put(q, "setting-change", doc{"id": newID(), "target": "account:" + astr(a["id"]), "holder": nonNil(next["holder"], a["holder"]), "field": f, "previous": a[f], "value": next[f], "actor": actor, "at": nowMs()}); err != nil {
			return nil, err
		}
	}
	if len(changed) > 0 {
		if err := notifyHolder(q, astr(a["holder"]), fmt.Sprintf("The owner changed %s of your subscription %s.", strings.Join(changed, ", "), a["name"])); err != nil {
			return nil, err
		}
	}
	if h, ok := next["holder"]; ok && h != a["holder"] {
		if err := notifyHolder(q, astr(h), fmt.Sprintf("The owner made you the holder of subscription %s.", a["name"])); err != nil {
			return nil, err
		}
	}
	for k, v := range next {
		a[k] = v
	}
	a["updatedAt"] = nowMs()
	return a, put(q, "managed-account", a)
}

var checkReasons = []string{"ready", "login-absent", "executable-missing", "binding-unprovisioned", "binding-absent", "status-unknown", "catalogue-only", "configuration-error"}

// accountReport: a host's observation of one binding of a subscription.
func accountReport(q *Request, b *contract.AccountObservation, host string) (any, error) {
	a, err := get(q, b.AccountId, "managed-account")
	if err != nil {
		return nil, err
	}
	integ, err := integrationHost(q)
	if err != nil {
		return nil, err
	}
	pub, err := hostWithRole(q, "publication")
	if err != nil {
		return nil, err
	}
	exec, err := executionHostIDs(q)
	if err != nil {
		return nil, err
	}
	known := append([]string{defaultHostID(q), integ, pub}, exec...)
	if host == "" || !inList(known, host) || !accountName.MatchString(b.Binding) || !inList([]string{"ready", "unavailable", "unknown"}, b.Readiness) {
		return nil, fail(400, "Invalid host account observation")
	}
	if ok, err := harnessAllowed(q, host, astr(a["harness"])); err != nil || !ok {
		if err == nil {
			err = fail(409, fmt.Sprintf("%s is not allowed on host %s", a["harness"], host))
		}
		return nil, err
	}
	others, err := all(q, "managed-account", nil)
	if err != nil {
		return nil, err
	}
	for _, o := range others {
		for _, x := range bindingsOf(o) {
			if o["id"] != a["id"] && x["hostId"] == host && x["binding"] == b.Binding {
				return nil, fail(409, "Host binding already belongs to another managed account")
			}
		}
	}
	var previous map[string]any
	for _, x := range bindingsOf(a) {
		if x["hostId"] == host && x["binding"] == b.Binding {
			previous = x
		}
	}
	observedAt := nowMs()
	reason := "legacy-observation"
	if inList(checkReasons, b.Reason) {
		reason = b.Reason
	}
	var native any
	if b.NativeLoginObserved != nil {
		native = *b.NativeLoginObserved
	}
	details := doc{"reason": reason, "nativeLoginObserved": native, "quota": "unknown", "observedAt": observedAt}
	var roots any
	if r := b.Roots; r != nil {
		m := doc{}
		for k, s := range map[string]*string{"home": r.Home, "nativeHome": r.NativeHome, "credentialHome": r.CredentialHome, "credentialAuthority": r.CredentialAuthority, "executable": r.Executable} {
			if s != nil && len(*s) < 2000 {
				m[k] = *s
			}
		}
		roots = m
	} else if previous != nil {
		roots = previous["roots"]
	}
	retained := reason == "catalogue-only" && previous != nil && previous["readiness"] == "ready"
	readiness, basis := b.Readiness, "Latest host status and behavior approval"
	if retained {
		readiness, basis = "ready", "Prior qualified ready binding retained; catalogue is not new authentication proof"
	}
	last := doc{"readiness": b.Readiness}
	for k, v := range details {
		last[k] = v
	}
	binding := doc{"hostId": host, "binding": b.Binding, "platform": b.Platform, "readiness": readiness, "observedAt": observedAt, "roots": roots,
		"lastCheck": last, "readinessBasis": basis, "authority": "host-owned native registry; no credentials transferred"}
	check := doc{"id": newID(), "accountId": a["id"], "hostId": host, "binding": b.Binding, "readiness": b.Readiness, "roots": roots}
	for k, v := range details {
		check[k] = v
	}
	if err := put(q, "account-check", check); err != nil {
		return nil, err
	}
	var current map[string]any
	for _, x := range bindingsOf(a) {
		if current == nil && x["hostId"] == host {
			current = x
		}
	}
	if current == nil || current["binding"] == b.Binding || b.Readiness == "ready" {
		kept := []any{}
		for _, x := range bindingsOf(a) {
			if x["hostId"] != host {
				kept = append(kept, x)
			}
		}
		a["bindings"] = append(kept, binding)
	}
	return a, put(q, "managed-account", a)
}

// accountCatalogue: the safe binding metadata the default host's inspector reports.
func accountCatalogue(q *Request, b *contract.AccountCatalogue, host string) (any, error) {
	a, err := get(q, b.AccountId, "managed-account")
	if err != nil {
		return nil, err
	}
	if host != defaultHostID(q) || len(b.Bindings) > 100 {
		return nil, fail(400, "Invalid host catalogue")
	}
	out := []any{}
	for _, m := range b.Bindings {
		env := doc{}
		for k, v := range map[string]*string{"HOME": m.Environment.HOME, "CODEX_HOME": m.Environment.CODEXHOME, "CLAUDE_CONFIG_DIR": m.Environment.CLAUDECONFIGDIR, "GEMINI_HOME": m.Environment.GEMINIHOME} {
			if v == nil {
				continue
			}
			if !strings.HasPrefix(*v, "/") {
				return nil, fail(400, "Invalid safe host binding metadata")
			}
			env[k] = *v
		}
		if !accountName.MatchString(m.Name) {
			return nil, fail(400, "Invalid safe host binding metadata")
		}
		out = append(out, doc{"name": m.Name, "executable": m.Executable, "environment": env, "behaviorApproved": m.BehaviorApproved})
	}
	err = put(q, "account-catalogue", doc{"id": "catalogue:" + host + ":" + astr(a["harness"]), "hostId": host, "harness": a["harness"], "observedAt": nowMs(), "bindings": out})
	return doc{"ok": true}, err
}

// resolveAccount: the ready binding of a subscription on a host for this director; nil when none applies.
func resolveAccount(q *Request, selection *string, harness, host string, director, projectID any) (doc, error) {
	requested := ""
	if selection != nil {
		requested = *selection
	} else if harness == "codex" {
		requested = defaultAccountBinding()
	}
	if requested == "" {
		return nil, nil
	}
	as, err := all(q, "managed-account", nil)
	if err != nil {
		return nil, err
	}
	var a doc
	for _, x := range as {
		match := x["id"] == requested || x["name"] == requested
		for _, b := range bindingsOf(x) {
			if b["hostId"] == host && b["binding"] == requested {
				match = true
			}
		}
		if match {
			a = x
			break
		}
	}
	if a == nil {
		return nil, nil
	}
	if a["harness"] != harness {
		return nil, fail(409, "Selected account harness mismatch")
	}
	dir := astr(director)
	// A take rule covers its projects; a director with full access (the owner, or a grant on *, as site-agents has
	// in development) may take any subscription. Either way the execution records takenFrom, the real director.
	takes := inList(strs(mapOf(a["take"])["projects"]), astr(projectID)) && astr(projectID) != "" || q.peopleSnapshot().person(dir) != nil && fullAccess(q.peopleSnapshot().person(dir))
	if selection == nil && dir != "" && a["holder"] != dir && !takes {
		return nil, nil
	}
	if a["intake"] == "paused" {
		return nil, fail(409, fmt.Sprintf("Subscription %s is Paused", a["name"]))
	}
	taken := dir != "" && a["holder"] != dir
	if taken && !takes {
		return nil, fail(409, fmt.Sprintf("Subscription %s is held by %s; work directed by %s needs a take", a["name"], a["holder"], dir))
	}
	if ok, err := harnessAllowed(q, host, harness); err != nil || !ok {
		if err == nil {
			err = fail(409, fmt.Sprintf("%s is not allowed on host %s", harness, host))
		}
		return nil, err
	}
	var b map[string]any
	for _, x := range bindingsOf(a) {
		if b == nil && x["hostId"] == host {
			b = x
		}
	}
	if b == nil || b["readiness"] != "ready" {
		return nil, fail(409, "Selected account has no observed ready binding on this host")
	}
	return doc{"accountId": a["id"], "accountName": a["name"], "binding": b["binding"], "hostId": host, "observedAt": b["observedAt"], "holder": a["holder"], "taken": taken}, nil
}

func (p *Person) mayAccess(projectID string) bool {
	if fullAccess(p) {
		return true
	}
	for _, g := range p.Grants {
		if g.Kind == "project" && g.ID == projectID && projectID != "" {
			return true
		}
	}
	return false
}
