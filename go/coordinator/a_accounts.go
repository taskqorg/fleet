package main

// Area A: subscriptions and their host bindings (docs/designs/fleet-people-access.md). A subscription is a 'managed-account' document; logins stay
// on the holder's host, Fleet keeps only what hosts observe.

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

var accountName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

func init() {
	operation("account_list", true, accountList)
	operation("account_create", false, accountCreate)
	operation("account_hosts", true, accountHostsOp)
	operation("account_discover", false, func(q *Request) (any, error) { return accountInspect(q, q.obj(), true) })
	operation("account_connect", false, func(q *Request) (any, error) { return accountInspect(q, q.obj(), false) })
	operation("account_login_handoff", true, accountLoginHandoff)
	operation("account_update", false, accountUpdate)
	route("POST /api/hosts/accounts", func(q *Request) (any, error) {
		if !q.app.hostMaySee(q.host, nil) {
			return nil, fail(403, "Account observations need an owner-held host")
		}
		b := q.obj()
		return accountReport(q, b, astr(b["hostId"]))
	})
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
			seen = beat["lastSeenAt"]
			connectivity = "offline"
			if float64(now())-num(beat["lastSeenAt"]) < 30000 {
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
			"onboardingSupported": inspector != "" && platform == "darwin", "reason": reason})
	}
	return out, nil
}

func accountHostsOp(q *Request) (any, error) {
	b := q.obj()
	harness := astr(nonNil(b["harness"], "codex"))
	if b["accountId"] != nil {
		a, err := aget(q, b["accountId"], "managed-account")
		if err != nil {
			return nil, err
		}
		harness = astr(a["harness"])
	}
	hs, err := accountHosts(q, harness)
	return doc{"hosts": hs}, err
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
			e, err := aget(q, i["executionId"], "execution")
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
	same, err := aRows(q, "SELECT data FROM document WHERE kind='managed-account' AND data->>'name'=$1", b["name"])
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
	if holder != director && !q.person.Owner {
		return nil, fail(403, "Only the owner creates a subscription for another person")
	}
	text := func(k string) string { s, _ := b[k].(string); return s }
	a := doc{"id": uuid(), "name": b["name"], "harness": harness, "holder": holder, "kind": "personal", "plan": text("plan"), "intake": "all",
		"take": doc{"projects": []any{}}, "switchPct": nil, "comment": text("comment"), "createdAt": now(), "bindings": []any{},
		"login": "Native login on the holder's host, under its protected binding environment: " + loginCommands[harness] + ". Never paste codes or tokens into Fleet. Inspect an existing authorized login first; creation does not authenticate."}
	return a, put(q, "managed-account", a)
}

func integrationHost(q *Request) (string, error) {
	return hostWithRole(q, "inspector-for:"+defaultHostID(q))
}

// accountInspect: a read-only inspection command to the inspector of the default host.
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
	a, err := aget(q, b["accountId"], "managed-account")
	if err != nil {
		return nil, err
	}
	host := astr(nonNil(b["hostId"], defaultHostID(q)))
	integ, err := integrationHost(q)
	if err != nil {
		return nil, err
	}
	if host != defaultHostID(q) || integ == "" {
		return nil, fail(409, "Selected host has no supported local Mac inspector")
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
			e, err := aget(q, x["executionId"], "execution")
			if err != nil {
				return nil, err
			}
			if s := astr(e["state"]); s != "completed" && s != "failed" && s != "stopped" && s != "unknown" {
				return x, nil
			}
		}
	}
	sid, id := uuid(), uuid()
	mode := "status"
	if catalogue {
		mode = "catalogue"
	}
	if err := put(q, "session", doc{"id": sid, "createdAt": now(), "actor": "program-worker"}); err != nil {
		return nil, err
	}
	if err := put(q, "execution", doc{"id": id, "executionId": id, "sessionId": sid, "hostId": integ, "state": "queued", "createdAt": now(), "actor": "program-worker", "accountId": a["id"]}); err != nil {
		return nil, err
	}
	if err := put(q, "command", doc{"id": uuid(), "executionId": id, "sessionId": sid, "hostId": integ, "kind": "account-inspect", "status": "pending",
		"payload": doc{"accountId": a["id"], "harness": a["harness"], "binding": binding, "nativeHostId": host, "mode": mode}}); err != nil {
		return nil, err
	}
	r := doc{"id": uuid(), "accountId": a["id"], "hostId": host, "binding": binding, "executionId": id, "createdAt": now(),
		"loginAction": "Read-only inspection; no authentication changes. Await host result, then explicit login handoff only if needed."}
	return r, put(q, "account-inspection", r)
}

var loginArgv = map[string]any{"codex": []any{"login", "--device-auth"}, "claude": []any{"auth", "login", "--claudeai"}, "antigravity": []any{"acp", "--login"}, "acp": nil}

func accountLoginHandoff(q *Request) (any, error) {
	b := q.obj()
	a, err := aget(q, b["accountId"], "managed-account")
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
	return doc{"observedAt": now(), "basedOnCheckAt": basedOn, "hostId": host["id"], "binding": b["binding"], "environment": binding["environment"], "executable": binding["executable"],
		"command": command, "requiredUserInteraction": interaction, "warning": warning, "quota": "unknown", "authChanged": false}, nil
}

func accountUpdate(q *Request) (any, error) { return accountUpdateBody(q, q.obj()) }

func accountUpdateBody(q *Request, b doc) (any, error) {
	a, err := aget(q, b["accountId"], "managed-account")
	if err != nil {
		return nil, err
	}
	p := q.person
	if p != nil && !p.Owner && p.Email != a["holder"] {
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
	if v, ok := b["take"]; ok && v != nil {
		t, _ := v.(map[string]any)
		ps, isList := t["projects"].([]any)
		if !isList {
			return nil, fail(400, "take: {projects: [ids]}")
		}
		holder := q.app.people[astr(a["holder"])]
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
		if p != nil && !p.Owner {
			return nil, fail(403, "Only the owner changes the holder")
		}
		h := strings.ToLower(astr(v))
		if q.app.people[h] == nil {
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
		if err := put(q, "setting-change", doc{"id": uuid(), "target": "account:" + astr(a["id"]), "holder": nonNil(next["holder"], a["holder"]), "field": f, "previous": a[f], "value": next[f], "actor": actor, "at": now()}); err != nil {
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
	a["updatedAt"] = now()
	return a, put(q, "managed-account", a)
}

var checkReasons = []string{"ready", "login-absent", "executable-missing", "binding-unprovisioned", "binding-absent", "status-unknown", "catalogue-only", "configuration-error"}

// accountReport: a host's observation of one binding of a subscription.
func accountReport(q *Request, b doc, host string) (any, error) {
	a, err := aget(q, b["accountId"], "managed-account")
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
	if host == "" || !inList(known, host) || !accountName.MatchString(astr(b["binding"])) || !inList([]string{"darwin", "win32", "linux"}, astr(b["platform"])) || !inList([]string{"ready", "unavailable", "unknown"}, astr(b["readiness"])) {
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
			if o["id"] != a["id"] && x["hostId"] == host && x["binding"] == b["binding"] {
				return nil, fail(409, "Host binding already belongs to another managed account")
			}
		}
	}
	var previous map[string]any
	for _, x := range bindingsOf(a) {
		if x["hostId"] == host && x["binding"] == b["binding"] {
			previous = x
		}
	}
	observedAt := now()
	reason := "legacy-observation"
	if inList(checkReasons, astr(b["reason"])) {
		reason = astr(b["reason"])
	}
	var native any
	if v, ok := b["nativeLoginObserved"].(bool); ok {
		native = v
	}
	details := doc{"reason": reason, "nativeLoginObserved": native, "quota": "unknown", "observedAt": observedAt}
	var roots any
	if r, ok := b["roots"].(map[string]any); ok {
		m := doc{}
		for _, k := range []string{"home", "nativeHome", "credentialHome", "credentialAuthority", "executable"} {
			if s, ok := r[k].(string); ok && len(s) < 2000 {
				m[k] = s
			}
		}
		roots = m
	} else if previous != nil {
		roots = previous["roots"]
	}
	retained := reason == "catalogue-only" && previous != nil && previous["readiness"] == "ready"
	readiness, basis := b["readiness"], "Latest host status and behavior approval"
	if retained {
		readiness, basis = "ready", "Prior qualified ready binding retained; catalogue is not new authentication proof"
	}
	last := doc{"readiness": b["readiness"]}
	for k, v := range details {
		last[k] = v
	}
	binding := doc{"hostId": host, "binding": b["binding"], "platform": b["platform"], "readiness": readiness, "observedAt": observedAt, "roots": roots,
		"lastCheck": last, "readinessBasis": basis, "authority": "host-owned native registry; no credentials transferred"}
	check := doc{"id": uuid(), "accountId": a["id"], "hostId": host, "binding": b["binding"], "readiness": b["readiness"], "roots": roots}
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
	if current == nil || current["binding"] == b["binding"] || b["readiness"] == "ready" {
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
func accountCatalogue(q *Request, b doc, host string) (any, error) {
	a, err := aget(q, b["accountId"], "managed-account")
	if err != nil {
		return nil, err
	}
	list, ok := b["bindings"].([]any)
	if host != defaultHostID(q) || !ok || len(list) > 100 {
		return nil, fail(400, "Invalid host catalogue")
	}
	out := []any{}
	for _, x := range list {
		m, _ := x.(map[string]any)
		env, isEnv := m["environment"].(map[string]any)
		_, isExe := m["executable"].(string)
		valid := accountName.MatchString(astr(m["name"])) && isExe && isEnv
		for k, v := range env {
			s, isStr := v.(string)
			if !inList([]string{"HOME", "CODEX_HOME", "CLAUDE_CONFIG_DIR", "GEMINI_HOME"}, k) || !isStr || !strings.HasPrefix(s, "/") {
				valid = false
			}
		}
		if !valid {
			return nil, fail(400, "Invalid safe host binding metadata")
		}
		out = append(out, doc{"name": m["name"], "executable": m["executable"], "environment": env, "behaviorApproved": m["behaviorApproved"] == true})
	}
	err = put(q, "account-catalogue", doc{"id": "catalogue:" + host + ":" + astr(a["harness"]), "hostId": host, "harness": a["harness"], "observedAt": now(), "bindings": out})
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
	takes := inList(strs(mapOf(a["take"])["projects"]), astr(projectID))
	dir := astr(director)
	if selection == nil && dir != "" && a["holder"] != dir && !(astr(projectID) != "" && takes) {
		return nil, nil
	}
	if a["intake"] == "paused" {
		return nil, fail(409, fmt.Sprintf("Subscription %s is Paused", a["name"]))
	}
	taken := dir != "" && a["holder"] != dir
	if taken && !(astr(projectID) != "" && takes) {
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

func mapOf(v any) map[string]any { m, _ := v.(map[string]any); return m }

func (p *Person) mayAccess(projectID string) bool {
	if p.Owner {
		return true
	}
	for _, g := range p.Grants {
		if g.Kind == "project" && g.ID == projectID && projectID != "" {
			return true
		}
	}
	return false
}
