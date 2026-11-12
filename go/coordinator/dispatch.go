package main

// dispatch (docs/designs/fleet-people-access.md, slice 6), the one late choice point, with enqueue and startCommand. On every host poll each
// queued agent execution gets a ranked list of (subscription, host) candidates; the best unskipped one wins, and the poll
// claims it when the winner is the polling host. With none left the execution waits with the first skip reason, and one
// needs-you question is open per cause.

import (
	"encoding/json"
	"fleet/contract"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const onlineMs = 30000

const defaultInstructions = "Follow the user request and its scope. Discuss, answer questions, or review existing results when requested; do not create or modify a website unless requested. When creating a static website, deliver output/index.html and retain all source assets. Do not spawn subagents."

type candidate struct {
	Subscription doc      `json:"subscription"`
	Host         string   `json:"host"`
	Skipped      string   `json:"skipped,omitempty"`
	Cause        string   `json:"cause,omitempty"`
	Warnings     []string `json:"warnings"`
	Stop         bool     `json:"stop,omitempty"`
	binding      doc
}

type dispatchContext struct {
	hosts    []doc
	byID     map[string]doc
	seen     map[string]float64
	roots    map[string][]string
	conns    map[string][]string // git connection names each host reported; absent: unknown
	accounts []doc
	readings map[string][]Reading
	orders   map[string]contract.DispatchOrder // the directors' order setting, read once per pass
}

func loadDispatchContext(q *Request) (*dispatchContext, error) {
	hs, err := hostList(q)
	if err != nil {
		return nil, err
	}
	k := &dispatchContext{byID: map[string]doc{}, seen: map[string]float64{}, roots: map[string][]string{}, conns: map[string][]string{}, orders: map[string]contract.DispatchOrder{}}
	for _, h := range hs {
		if len(anys(h["roles"])) == 0 {
			k.hosts = append(k.hosts, h)
			k.byID[astr(h["id"])] = h
		}
	}
	obs, err := all(q, "host-observation", nil)
	if err != nil {
		return nil, err
	}
	for _, o := range obs {
		k.seen[astr(o["id"])] = num(o["lastSeenAt"])
		if o["gitConnections"] != nil {
			k.conns[astr(o["id"])] = strs(o["gitConnections"])
		}
		if o["projectRoots"] != nil {
			k.roots[astr(o["id"])] = strs(o["projectRoots"])
		}
	}
	if k.accounts, err = all(q, "managed-account", nil); err != nil {
		return nil, err
	}
	k.readings, err = latestReadings(q.ctx(), q.app)
	return k, err
}

func subscriptionsOf(q *Request, projectID any) []string {
	return strs(projectSetting(q, projectID, "subscriptions"))
}

// checkouts: the project's environments, each {hostId, checkoutPath}.
func checkouts(q *Request, projectID any) []map[string]any {
	out := []map[string]any{}
	for _, x := range anys(projectSetting(q, projectID, "environments")) {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

// repoConnection: the git connection of the repository a project runs on a host (its environment's repo, else the
// first repo), as the executor's gitRepo chooses it; empty when that repo declares none.
func repoConnection(repos []any, cos []map[string]any, host string) string {
	var repo any
	for _, co := range cos {
		if co["hostId"] == host {
			repo = co["repo"]
			break
		}
	}
	for _, r := range repos {
		m := mapOf(r)
		if repo == nil || m["id"] == repo {
			return astr(m["connection"])
		}
	}
	return ""
}

func accountHarness(a doc) string {
	if h := astr(a["harness"]); h != "" {
		return h
	}
	return "codex"
}

// Wait reasons, one list for tasks and executions (waitReason; the free text is waitDetail). The first three are the
// "waits for a person" kinds of OSC 7501 blocked (go/executor/status.go): a grant or decision (permission), an answer
// (question), a login or subscription (auth). The others wait on Fleet itself.
var waitReasons = []string{"permission", "question", "auth", "dependency", "artifacts", "placement"}

// waitKindOf: the wait reason of a dispatch stall cause (dispatch causes, causeOf).
func waitKindOf(cause string) string {
	switch {
	case strings.HasSuffix(cause, ":trust"), strings.HasSuffix(cause, ":holder"), strings.HasSuffix(cause, ":not-taken"), strings.HasPrefix(cause, "budget:"):
		return "permission" // a holder's grant, a take rule or a raised budget
	case strings.HasPrefix(cause, "director:"), strings.HasSuffix(cause, ":not-ready"):
		return "auth" // no subscription of the director, or a login to complete
	}
	return "placement" // offline, paused, roots, placement, limits: Fleet retries
}

// strPtr: a string value as an optional contract field; nil for anything else or "" (strp keeps "").
func strPtr(v any) *string {
	if s, ok := v.(string); ok && s != "" {
		return &s
	}
	return nil
}

// rawMap and rawList: Fleet-built JSON values as the contract's open fields. nil and empty stay absent (omitempty);
// a profile (agentConfig) is never empty, and the executor reads an absent list as empty.
func rawMap(v any) map[string]json.RawMessage {
	var m map[string]json.RawMessage
	if v != nil {
		json.Unmarshal(jsonBytes(v), &m)
	}
	return m
}

func rawList(v any) []json.RawMessage {
	var l []json.RawMessage
	if v != nil {
		json.Unmarshal(jsonBytes(v), &l)
	}
	return l
}

// causeOf: a skip reason's stall cause key. ponytail: keyed on resolve's messages; structured codes if they start to change.
func causeOf(a doc, host, message string) string {
	id := astr(a["id"])
	switch {
	case strings.Contains(message, "Paused"):
		return "subscription:" + id + ":paused"
	case strings.Contains(message, "not allowed on host"):
		return "placement:" + host + ":" + astr(a["harness"])
	case strings.Contains(message, "held by"):
		return "subscription:" + id + ":not-taken"
	case strings.Contains(message, "ready binding"):
		return "subscription:" + id + ":not-ready"
	}
	return "subscription:" + id + ":refused"
}

func subOf(a doc) doc {
	if a == nil {
		return nil
	}
	return doc{"id": a["id"], "name": a["name"], "holder": a["holder"]}
}

// rankCandidates: the ordered candidate list of one queued execution.
func (a *App) rankCandidates(q *Request, e doc, k *dispatchContext) ([]*candidate, error) {
	x := mapOf(e["dispatch"])
	director := e["directedBy"]
	dir := astr(director)
	projectID := x["projectId"]
	harness := astr(x["harness"])
	order := []string{}
	if h := astr(x["host"]); h != "" {
		order = []string{h}
	} else {
		for _, id := range append([]string{astr(x["last"]), a.hostID}, func() []string {
			ids := []string{}
			for _, h := range k.hosts {
				ids = append(ids, astr(h["id"]))
			}
			return ids
		}()...) {
			if _, ok := k.byID[id]; ok && !inList(order, id) {
				order = append(order, id)
			}
		}
	}
	named := func(v string) func(doc) bool {
		return func(s doc) bool {
			if s["id"] == v || s["name"] == v {
				return true
			}
			for _, b := range bindingsOf(s) {
				if b["binding"] == v {
					return true
				}
			}
			return false
		}
	}
	find := func(list []doc, f func(doc) bool) doc {
		for _, s := range list {
			if f(s) {
				return s
			}
		}
		return nil
	}
	settings := mapOf(mapOf(e["projectSnapshot"])["settings"])
	if settings == nil && projectID != nil {
		settings = projectSettingsOf(q, astr(projectID))
	}
	cos := []map[string]any{}
	for _, v := range anys(settings["environments"]) {
		cos = append(cos, mapOf(v))
	}
	accounts := []doc{}
	for _, s := range k.accounts {
		if accountHarness(s) == harness {
			accounts = append(accounts, s)
		}
	}
	out := []*candidate{}
	var subs []doc
	if acc := astr(x["account"]); acc != "" {
		s := find(k.accounts, named(acc))
		if s != nil && accountHarness(s) != harness {
			host := a.hostID
			if len(order) > 0 {
				host = order[0]
			}
			return []*candidate{{Subscription: subOf(s), Host: host, Warnings: []string{}, Skipped: fmt.Sprintf("%s is a %s subscription, not %s", s["name"], s["harness"], harness), Cause: "subscription:" + astr(s["id"]) + ":harness"}}, nil
		}
		subs = []doc{s}
	} else {
		list := strs(settings["subscriptions"])
		pos := func(s doc) int {
			for i, n := range list {
				if n == s["name"] {
					return i
				}
			}
			for i, n := range list {
				if n == s["id"] {
					return i
				}
			}
			return 100000
		}
		// A full-access director (the owner, or a grant on *, as site-agents has) may take any subscription, as in
		// resolveAccount (people-access §5, development rule); without it such a director with no logins of its own
		// never placed (live 2026-11-12: a site-agents PM stayed preparing).
		// Only owner-held subscriptions: another person's personal login still needs its holder's take rule.
		p, owner := q.peopleSnapshot().person(dir), ownerEmail(q)
		full := p != nil && fullAccess(p)
		mine := func(s doc) bool {
			return s["holder"] == director || full && s["holder"] == owner || inList(strs(mapOf(s["take"])["projects"]), astr(projectID)) && projectID != nil
		}
		weight := func(s doc) int {
			w := pos(s)
			if s["holder"] != director {
				w += 1000000
			}
			return w
		}
		ranked := []doc{}
		for _, s := range accounts {
			if mine(s) {
				ranked = append(ranked, s)
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return weight(ranked[i]) < weight(ranked[j]) })
		var def doc
		if harness == "codex" && defaultAccountBinding() != "" {
			def = find(accounts, named(defaultAccountBinding()))
		}
		useDef := def != nil && mine(def)
		if useDef {
			subs = append(subs, def)
		}
		for _, s := range ranked {
			if def == nil || s["id"] != def["id"] {
				subs = append(subs, s)
			}
		}
		if !useDef {
			subs = append(subs, nil)
		}
	}
	for _, s := range subs {
		for _, id := range order {
			h := k.byID[id]
			if s != nil {
				bound := false
				for _, b := range bindingsOf(s) {
					if b["hostId"] == id {
						bound = true
					}
				}
				if !bound {
					continue
				}
			}
			c := &candidate{Subscription: subOf(s), Host: id, Warnings: []string{}}
			out = append(out, c)
			skip := func(reason, cause string) { c.Skipped, c.Cause = reason, cause }
			if h == nil {
				skip(id+" is offline", "host:"+id+":offline")
				continue
			}
			if !a.hostMayRun(h, projectID, director) {
				skip(fmt.Sprintf("%s is held by %s, who has no grant on %v", id, h["holder"], jsText(projectID)), "host:"+id+":trust")
				continue
			}
			if h["intake"] == "paused" {
				skip(id+" is Paused", "host:"+id+":paused")
				continue
			}
			roots, haveRoots := k.roots[id]
			checkout := astr(x["workingDirectory"])
			if checkout == "" {
				for _, co := range cos {
					if co["hostId"] == id {
						checkout = astr(co["checkoutPath"])
						break
					}
				}
			}
			if haveRoots && checkout != "" {
				inside := false
				for _, r := range roots {
					if checkout == r || strings.HasPrefix(checkout, strings.TrimSuffix(r, "/")+"/") {
						inside = true
					}
				}
				if !inside {
					of := ""
					if projectID != nil {
						of = " of " + astr(projectID)
					}
					skip(fmt.Sprintf("checkout %s%s is outside FLEET_PROJECT_ROOTS on %s", checkout, of, id), fmt.Sprintf("host:%s:roots:%v", id, jsText(projectID)))
					continue
				}
			}
			// The repository's git connection must be on the host, else the run fails after setup (2026-11-11).
			if have, known := k.conns[id]; known {
				if need := repoConnection(anys(settings["repos"]), cos, id); need != "" && !slices.Contains(have, need) {
					skip(fmt.Sprintf("%s has no git connection %s", id, need), fmt.Sprintf("host:%s:connection:%s", id, need))
					continue
				}
			}
			if s != nil {
				name := astr(s["name"])
				b, err := resolveAccount(q, &name, harness, id, director, projectID)
				if err != nil {
					var he *HTTPError
					if !asHTTP(err, &he) {
						return nil, err
					}
					skip(he.Msg, causeOf(s, id, he.Msg))
					continue
				}
				c.binding = b
				if over := readingOverSwitch(s, k.readings[astr(s["id"])]); over != "" {
					skip(over, "subscription:"+astr(s["id"])+":limit")
					continue
				}
				if s["harness"] == "claude" && s["holder"] != h["holder"] {
					c.Warnings = append(c.Warnings, fmt.Sprintf("%s can read this login (Claude on a host held by someone else)", h["holder"]))
				}
			} else {
				if harness != "codex" && astr(x["account"]) == "" {
					skip(fmt.Sprintf("no %s subscription of %v applies", harness, jsText(director)), fmt.Sprintf("director:%v:%s", jsText(director), harness))
					continue
				}
				if dir != "" && h["holder"] != dir {
					skip(fmt.Sprintf("%s login belongs to %s", id, h["holder"]), "host:"+id+":holder")
					continue
				}
				if hs := mapOf(mapOf(h["harnesses"])[harness]); hs["allowed"] == false {
					skip(fmt.Sprintf("%s is not allowed on host %s", harness, id), "placement:"+id+":"+harness)
					continue
				}
			}
			if !(float64(nowMs())-k.seen[id] < onlineMs) {
				skip(id+" is offline", "host:"+id+":offline")
				continue
			}
			if harness == "antigravity" || harness == "acp" {
				c.Warnings = append(c.Warnings, "ACP-driven harness: provider terms note (terms research §2)")
			}
		}
	}
	// The director's order setting (section 8): soonest_reset uses limit about to reset first, with the terms warning.
	if dir != "" {
		order, ok := k.orders[dir]
		if !ok {
			o, err := dispatchOrder(q, dir)
			if err != nil {
				return nil, err
			}
			order, k.orders[dir] = o, o
		}
		if order == contract.DispatchOrderSoonestReset {
			soonestReset(out, k.readings)
			subs := map[any]bool{}
			for _, c := range out {
				if c.Skipped == "" && c.Subscription != nil {
					subs[c.Subscription["id"]] = true
				}
			}
			for _, c := range out {
				if len(subs) > 1 && c.Skipped == "" && c.Subscription != nil {
					c.Warnings = append(c.Warnings, "order soonest_reset: round-robin across accounts may conflict with provider terms")
				}
			}
		}
	}
	live := []*candidate{}
	for _, c := range out {
		if c.Subscription != nil && c.Skipped == "" {
			live = append(live, c)
		}
	}
	for _, c := range live {
		ids := map[any]bool{}
		for _, o := range live {
			if o.Subscription["holder"] == c.Subscription["holder"] {
				ids[o.Subscription["id"]] = true
			}
		}
		if len(ids) > 1 {
			c.Warnings = append(c.Warnings, "two personal subscriptions of one holder in one order list (provider rate-limit terms)")
		}
	}
	reached, err := budgetsReached(q, e)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(reached, func(i, j int) bool { return reached[i].action == "stop" && reached[j].action != "stop" })
	for _, b := range reached {
		if b.action == "warn" {
			for _, c := range out {
				c.Warnings = append(c.Warnings, b.reason)
			}
			continue
		}
		for _, c := range out {
			if c.Skipped == "" {
				c.Skipped, c.Cause, c.Stop = b.reason, "budget:"+b.id+":reached", b.action == "stop"
			}
		}
	}
	return out, nil
}

// jsText: a value as a JavaScript template literal prints it (null for nil).
func jsText(v any) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprint(v)
}

func asHTTP(err error, he **HTTPError) bool {
	h, ok := err.(*HTTPError)
	*he = h
	return ok
}

func recordDecision(q *Request, e doc, polling string, cs []*candidate, winner *candidate) error {
	var w any
	if winner != nil {
		w = js(doc{"subscription": winner.Subscription, "host": winner.Host})
	}
	return exec(q, "INSERT INTO route_decision(execution_id,project_id,host_id,candidates,winner) VALUES($1,$2,$3,$4,$5)", e["id"], mapOf(e["dispatch"])["projectId"], polling, js(cs), w)
}

var causeHost = regexp.MustCompile(`^host:([^:]+)`)

// dispatchRun: one dispatch pass for a poll of the polling host.
func (a *App) dispatchRun(q *Request, polling string) error {
	var ids []string
	// Only the stall causes this pass raises: a key, budget-ask or other area's question keeps its cause.
	var held []map[string]any
	if err := q.tx.QueryRow(q.ctx(), "SELECT (SELECT coalesce(array_agg(id ORDER BY seq),'{}') FROM execution WHERE host_id IS NULL AND state='preparing' AND kind='agent_run'),(SELECT coalesce(json_agg(json_build_object('id',id,'cause',cause,'state',state)),'[]') FROM question WHERE state IN ('open','answered') AND cause ~ '^(subscription|placement|host|director|execution):|^budget:.*:reached$')").Scan(&ids, &held); err != nil {
		return err
	}
	type stall struct{ reason, director, ask, projectID any }
	causes := map[string]stall{}
	causeOrder := []string{}
	var k *dispatchContext
	if len(ids) > 0 {
		var err error
		if k, err = loadDispatchContext(q); err != nil {
			return err
		}
	}
	for _, id := range ids {
		e, err := get(q, id, "execution")
		if err != nil {
			return err
		}
		if e["dispatch"] == nil || e["hostId"] != nil || e["state"] != "preparing" || e["stopRequested"] == true {
			continue
		}
		cs, err := a.rankCandidates(q, e, k)
		if err != nil {
			return err
		}
		var best *candidate
		for _, c := range cs {
			if c.Skipped == "" {
				best = c
				break
			}
		}
		if best != nil {
			if best.Host != polling {
				continue
			}
			delete(e, "waitReason")
			delete(e, "waitDetail")
			if _, err := a.startCommand(q, e, best.Host, best.binding); err != nil {
				return err
			}
			if err := recordDecision(q, e, polling, cs, best); err != nil {
				return err
			}
			if err := exec(q, "UPDATE execution SET wait_reason=NULL WHERE id=$1", e["id"]); err != nil {
				return err
			}
			continue
		}
		reason, cause := "no host can run this work", "execution:"+id+":no-candidate"
		var first *candidate
		if len(cs) > 0 {
			first = cs[0]
			if first.Skipped != "" {
				reason = first.Skipped
			}
			if first.Cause != "" {
				cause = first.Cause
			}
		}
		if first != nil && first.Stop {
			e["state"], e["stopDetail"], e["waitReason"], e["waitDetail"] = "cancelled", reason, waitKindOf(cause), reason
			if err := put(q, "execution", e); err != nil {
				return err
			}
			if err := recordDecision(q, e, polling, cs, nil); err != nil {
				return err
			}
			continue
		}
		if e["waitDetail"] != reason {
			e["waitReason"], e["waitDetail"] = waitKindOf(cause), reason
			if err := put(q, "execution", e); err != nil {
				return err
			}
			if err := exec(q, "UPDATE execution SET wait_reason=$2 WHERE id=$1", e["id"], e["waitReason"]); err != nil {
				return err
			}
			if err := recordDecision(q, e, polling, cs, nil); err != nil {
				return err
			}
		}
		var holder any
		if m := causeHost.FindStringSubmatch(cause); m != nil {
			if h := k.byID[m[1]]; h != nil {
				holder = h["holder"]
			}
		} else if first != nil && first.Subscription != nil {
			holder = first.Subscription["holder"]
		}
		if _, ok := causes[cause]; !ok {
			causes[cause] = stall{reason, e["directedBy"], holder, mapOf(e["dispatch"])["projectId"]}
			causeOrder = append(causeOrder, cause)
		}
	}
	heldCause := map[string]bool{}
	for _, h := range held {
		c := astr(h["cause"])
		heldCause[c] = true
		if _, ok := causes[c]; ok {
			continue
		}
		sql := "UPDATE question SET cause=NULL WHERE id=$1"
		if h["state"] == "open" {
			sql = "UPDATE question SET state='withdrawn',cause=NULL WHERE id=$1"
		}
		if err := exec(q, sql, h["id"]); err != nil {
			return err
		}
	}
	for _, cause := range causeOrder {
		if heldCause[cause] {
			continue
		}
		c := causes[cause]
		tail := " Change the setting to let it start."
		if c.ask != nil && c.ask != c.director {
			tail = fmt.Sprintf(" Ask %v, or change the setting.", c.ask)
		}
		if err := openCauseQuestion(q, cause, c.director, c.projectID, fmt.Sprintf("Work is waiting: %v.%s", c.reason, tail)); err != nil {
			return err
		}
	}
	return nil
}

// openCauseQuestion opens a needs-you operator_decision question keyed by cause, addressed to one person.
func openCauseQuestion(q *Request, cause string, addressedTo, projectID any, text string) error {
	qid, err := askQuestion(q, doc{"kind": "operator_decision", "text": text, "options": []any{"ok"}, "askedBy": "fleet", "projectId": projectID, "data": doc{"cause": cause}})
	if err != nil {
		return err
	}
	return exec(q, "UPDATE question SET cause=$2,addressed_to=$3 WHERE id=$1", qid, cause, addressedTo)
}

// startCommand: the start command of a claimed execution, from the session as it is now and the chosen binding.
func (a *App) startCommand(q *Request, e doc, host string, binding doc) (doc, error) {
	s, err := get(q, e["sessionId"], "session")
	if err != nil {
		return nil, err
	}
	d := mapOf(e["dispatch"])
	e["hostId"] = host
	if s["account"] != nil || binding != nil {
		e["requestedAccount"] = s["account"]
		if binding != nil {
			e["effectiveAccount"] = binding
		} else {
			e["effectiveAccount"] = doc{"binding": s["account"], "legacy": true}
		}
	}
	if binding != nil && binding["taken"] == true {
		e["takenFrom"], e["directedBy"] = e["directedBy"], binding["holder"]
	}
	cfg := mapOf(s["agentConfig"])
	// The start payload is contract.StartPayload, field by field: the enqueue extras (inputs, skills, webRoute) are named
	// fields, never a blind merge over the fields above (C7).
	p := &contract.StartPayload{Harness: ptr(harnessOf(s)), Text: strPtr(d["input"]), Setup: strPtr(nonNil(s["setup"], "run")),
		WorkingDirectory: strPtr(s["workingDirectory"]), Model: strPtr(e["model"]), Effort: strPtr(e["effort"]),
		Instructions: ptr(defaultInstructions), NativeSessionId: strp(s, "nativeSessionId")} // "" still reaches the executor's check
	if s["account"] != nil || (binding != nil && s["nativeSessionId"] == nil) {
		if binding != nil {
			p.Account = strPtr(binding["binding"])
		} else {
			p.Account = strPtr(s["account"])
		}
	}
	if pid := astr(s["projectId"]); pid != "" {
		snapshot := mapOf(e["projectSnapshot"])
		if snapshot == nil {
			return nil, fail(409, "project_context_unknown: queued execution predates project snapshots")
		}
		st := mapOf(snapshot["settings"])
		p.ProjectId, p.Project = &pid, rawMap(st)
	}
	networkSession := mapOf(clone(s))
	if snapshot := mapOf(e["projectSnapshot"]); snapshot != nil {
		networkSession["projectSettings"] = snapshot["settings"]
	}
	if gitNetworkOf(q, networkSession) {
		p.GitNetwork = ptr(true)
	}
	if cfg != nil {
		p.AgentConfig, p.Instructions = rawMap(cfg), ptr("")
		if cfg["fleetOperations"] == true { // the profile opts in to every operation its director may call (fleet-pm-agent.md §1)
			if dir := q.peopleSnapshot().person(firstString(e["directedBy"], ownerEmail(q))); dir != nil {
				p.FleetOperations = rawList(fleetOperations(dir))
			}
		}
	}
	extra := mapOf(d["payload"])
	if v, ok := extra["inputs"]; ok {
		if err := json.Unmarshal(jsonBytes(v), &p.Inputs); err != nil {
			return nil, fmt.Errorf("execution %v: pipeline inputs are not [{artifactId, path}]: %w", e["id"], err)
		}
	}
	p.Skills, p.WebRoute = rawList(extra["skills"]), strPtr(extra["webRoute"])
	if snapshot := mapOf(e["projectSnapshot"]); snapshot != nil {
		instruction := ""
		if p.Instructions != nil {
			instruction = *p.Instructions
		}
		for _, f := range anys(snapshot["context"]) {
			m := mapOf(f)
			instruction += "\n\nProject context " + astr(m["path"]) + ":\n" + astr(m["text"])
		}
		// Stored JSON arrays can also be typed docs before their first database round-trip.
		if xs, ok := snapshot["context"].([]doc); ok {
			for _, m := range xs {
				instruction += "\n\nProject context " + astr(m["path"]) + ":\n" + astr(m["text"])
			}
		}
		p.Instructions = &instruction
		workspaces := mapOf(s["workspaceSnapshots"])
		if workspaces == nil {
			workspaces = doc{}
		}
		if workspaces[host] == nil {
			workspaces[host] = doc{"executionId": e["id"], "project": snapshot["settings"], "revision": snapshot["revision"], "contentSha": snapshot["contentSha"], "workingDirectory": s["workingDirectory"]}
			s["workspaceSnapshots"] = workspaces
			if err := put(q, "session", s); err != nil {
				return nil, err
			}
		}
	}
	command := doc{"id": newID(), "executionId": e["id"], "sessionId": s["id"], "hostId": host, "kind": string(contract.CommandKindStart), "payload": p, "status": "pending"}
	if err := agentDecorate(q, s, e, p); err != nil {
		return nil, err
	}
	if granted := strs(cfg["connections"]); len(granted) > 0 {
		run, err := mint(q, doc{"executionId": e["id"], "directedBy": e["directedBy"], "projectId": s["projectId"], "experimentId": e["experimentId"], "pipelineRunId": s["pipelineRunId"]}, granted)
		if err != nil {
			return nil, err
		}
		if run != nil {
			if run["token"] != nil {
				p.RunToken, p.Connections = strPtr(run["token"]), rawList(run["connections"])
			}
			if len(anys(run["refused"])) > 0 {
				e["connectionsRefused"] = run["refused"]
			}
		}
	}
	if err := put(q, "execution", e); err != nil {
		return nil, err
	}
	if err := put(q, "command", command); err != nil {
		return nil, err
	}
	if binding != nil && binding["taken"] == true {
		task := astr(s["deliveryId"])
		if task == "" {
			ms, err := queryRows(q, "SELECT task_id FROM execution WHERE id=$1", e["id"])
			if err != nil {
				return nil, err
			}
			if len(ms) > 0 {
				task = astr(ms[0]["task_id"])
			}
		}
		if task != "" {
			if err := takeTaskFrom(q, task, astr(binding["holder"]), e["takenFrom"]); err != nil {
				return nil, err
			}
		}
	}
	return command, nil
}
