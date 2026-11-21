package main

// dispatch (docs/designs/fleet-people-access.md, slice 6), the one late choice point, with enqueue and startCommand. On every host poll each
// queued agent execution gets a ranked list of (subscription, host) candidates; the best unskipped one wins, and the poll
// claims it when the winner is the polling host. With none left the execution waits with the first skip reason, and one
// needs-you question is open per cause.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	onStart(func(ctx context.Context, a *App) {
		startDispatchLoop(ctx, a)
	})
}

func (a *App) currentDispatchReqGen() uint64 {
	if a == nil {
		return 0
	}
	a.ensureRuntime()
	a.dispatchMu.Lock()
	defer a.dispatchMu.Unlock()
	return a.dispatchReqGen
}

func (a *App) wakeDispatch() {
	_ = a.triggerDispatch()
}

func (a *App) triggerDispatch() uint64 {
	if a == nil {
		return 0
	}
	a.ensureRuntime()
	a.dispatchMu.Lock()
	a.dispatchReqGen++
	gen := a.dispatchReqGen
	a.dispatchMu.Unlock()
	select {
	case a.dispatchCh <- struct{}{}:
	default:
	}
	return gen
}

func (a *App) waitDispatchCaughtUp(ctx context.Context, target uint64) error {
	if a == nil {
		return nil
	}
	a.ensureRuntime()
	if !a.dispatchLoopStarted.Load() {
		return a.dispatchPass(ctx, true)
	}
	for {
		a.dispatchMu.Lock()
		done := a.dispatchDoneGen >= target
		ch := a.dispatchDoneCh
		a.dispatchMu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ch:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// startDispatchLoop runs the dispatch pass on every wake (triggerDispatch), at start, and as a fallback every
// FLEET_DISPATCH_FALLBACK_MS. A woken pass sweeps; a fallback pass sweeps only when sessions or stalls wait. Each pass
// completes the request generation it read at its start, which waitDispatchCaughtUp waits on; a request during the
// pass leaves a wake, so the next pass runs at once.
func startDispatchLoop(ctx context.Context, a *App) {
	a.ensureRuntime()
	a.dispatchLoopStarted.Store(true)
	everyWake(ctx, "dispatch", envMs("FLEET_DISPATCH_FALLBACK_MS", 5000), a.dispatchCh, true, func(ctx context.Context, woken bool) error {
		a.dispatchMu.Lock()
		target := a.dispatchReqGen
		explicit := woken || target > a.dispatchDoneGen
		a.dispatchMu.Unlock()
		err := a.dispatchPass(ctx, explicit)
		if err != nil && ctx.Err() == nil {
			log.Printf("dispatch pass: %v", err)
		}
		a.dispatchMu.Lock()
		if target > a.dispatchDoneGen {
			a.dispatchDoneGen = target
		}
		close(a.dispatchDoneCh)
		a.dispatchDoneCh = make(chan struct{})
		a.dispatchMu.Unlock()
		return err
	})
}

func (a *App) dispatchPass(ctx context.Context, explicit bool) error {
	var sessionIDs []string
	var needsSweep bool
	var pipelinePending bool
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		var err error
		if sessionIDs, err = q.db().DispPendingSessions(q.ctx()); err != nil {
			return err
		}
		if explicit || len(sessionIDs) > 0 {
			needsSweep = true
			return nil
		}
		needsSweep, err = q.db().DispNeedsSweep(q.ctx(), dispatchStallCauses)
		return err
	}); err != nil {
		return err
	}
	if !needsSweep {
		return nil
	}
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		var err error
		pipelinePending, err = q.db().DispPipelinePending(q.ctx())
		return err
	}); err != nil {
		return err
	}
	locks := make([]string, 0, 2+len(sessionIDs))
	if pipelinePending {
		locks = append(locks, GLOBAL)
	}
	locks = append(locks, DISPATCH)
	for _, sid := range sessionIDs {
		locks = append(locks, "session:"+sid)
	}
	return runTxLocks(ctx, a, nil, true, locks, func(q *Request) error {
		if err := a.delegationReconcile(q); err != nil {
			return err
		}
		ids, err := hostIDs(q)
		if err != nil {
			return err
		}
		envs, err := a.projectEnvironmentsCached(q)
		if err != nil {
			return err
		}
		if err := detectProblems(q, ids, envs); err != nil {
			return err
		}
		if err := agentPromote(q); err != nil {
			return err
		}
		return a.dispatchRun(q, "")
	})
}

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
	hostIDs  []string // the ids of byID in host order
	byID     map[string]doc
	views    map[string]dispatchHost // the fields dispatch reads of each host in byID
	seen     map[string]float64
	caps     map[string][]string
	roots    map[string][]string
	conns    map[string][]string // git connection names each host reported; absent: unknown
	accounts []dispatchAccount
	readings map[string][]Reading
	orders   map[string]contract.DispatchOrder // the directors' order setting, read once per pass
}

// dispatchHost: the fields dispatch reads of a host record (hostList).
type dispatchHost struct {
	ID        string            `json:"id"`
	Holder    string            `json:"holder"`
	Roles     []json.RawMessage `json:"roles"`
	Harnesses map[string]struct {
		Allowed *bool `json:"allowed"`
	} `json:"harnesses"`
}

// hostSeen: the fields dispatch reads of a host observation.
type hostSeen struct {
	ID             string    `json:"id"`
	LastSeenAt     float64   `json:"lastSeenAt"`
	Capabilities   []string  `json:"capabilities"`
	GitConnections *[]string `json:"gitConnections"`
	ProjectRoots   *[]string `json:"projectRoots"`
}

// dispatchAccount: a subscription (managed-account record) with the fields dispatch reads; rec is the record itself.
type dispatchAccount struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Holder  string `json:"holder"`
	Harness string `json:"harness"`
	Take    struct {
		Projects []string `json:"projects"`
	} `json:"take"`
	rec doc
}

func loadDispatchContext(q *Request) (*dispatchContext, error) {
	hs, err := hostList(q)
	if err != nil {
		return nil, err
	}
	k := &dispatchContext{byID: map[string]doc{}, views: map[string]dispatchHost{}, caps: map[string][]string{}, seen: map[string]float64{}, roots: map[string][]string{}, conns: map[string][]string{}, orders: map[string]contract.DispatchOrder{}}
	for _, h := range hs {
		v, _ := asT[dispatchHost](h) // a field of another type reads as absent
		if len(v.Roles) == 0 {
			k.hostIDs = append(k.hostIDs, v.ID)
			k.byID[v.ID], k.views[v.ID] = h, v
		}
	}
	obs, err := recAll[doc](q, recHostObservation, nil)
	if err != nil {
		return nil, err
	}
	for _, x := range obs {
		o, _ := asT[hostSeen](x)
		k.caps[o.ID] = nonNilStrings(o.Capabilities)
		k.seen[o.ID] = q.app.effectiveHostSeen(o.ID, o.LastSeenAt)
		if o.GitConnections != nil {
			k.conns[o.ID] = nonNilStrings(*o.GitConnections)
		}
		if o.ProjectRoots != nil {
			k.roots[o.ID] = nonNilStrings(*o.ProjectRoots)
		}
	}
	accounts, err := recAll[doc](q, recManagedAccount, nil)
	if err != nil {
		return nil, err
	}
	for _, x := range accounts {
		s, _ := asT[dispatchAccount](x)
		s.rec = x
		k.accounts = append(k.accounts, s)
	}
	// The dispatch transaction's own connection: a second pool connection, taken while this transaction holds the
	// global lock, waits forever once every other pool connection waits for that lock.
	k.readings, err = latestReadings(withTx(q), q.app)
	return k, err
}

func nonNilStrings(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// projectRepo and projectEnvironment: a project's repository and its environment on a host (project settings).
type projectRepo struct {
	ID         string `json:"id"`
	Connection string `json:"connection"`
	Remote     string `json:"remote"`
}

type projectEnvironment struct {
	HostID       string  `json:"hostId"`
	CheckoutPath string  `json:"checkoutPath"`
	Repo         *string `json:"repo"`
}

// runSettings: the project settings dispatch reads.
type runSettings struct {
	Execution *struct {
		Profiles []string `json:"profiles"`
		Forge    bool     `json:"forge"`
		Tools    []string `json:"tools"`
	} `json:"execution"`
	Environments  []projectEnvironment `json:"environments"`
	Subscriptions []string             `json:"subscriptions"`
	Repos         []projectRepo        `json:"repos"`
}

// repoConnection: the git connection of the repository a project runs on a host (its environment's repo, else the
// first repo), as the executor's gitRepo chooses it; empty when that repo declares none.
func repoConnection(repos []projectRepo, cos []projectEnvironment, host string) string {
	var repo *string
	for _, co := range cos {
		if co.HostID == host {
			repo = co.Repo
			break
		}
	}
	for _, r := range repos {
		if repo == nil || r.ID == *repo {
			return r.Connection
		}
	}
	return ""
}

func accountHarness(a dispatchAccount) string {
	if a.Harness != "" {
		return a.Harness
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

// strPtr: a string value (or *string) as an optional field; nil for anything else or "" (strp keeps "").
func strPtr(v any) *string {
	switch s := v.(type) {
	case string:
		if s != "" {
			return &s
		}
	case *string:
		if s != nil && *s != "" {
			return s
		}
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
func causeOf(a dispatchAccount, host, message string) string {
	switch {
	case strings.Contains(message, "Paused"):
		return "subscription:" + a.ID + ":paused"
	case strings.Contains(message, "not allowed on host"):
		return "placement:" + host + ":" + a.Harness
	case strings.Contains(message, "held by"):
		return "subscription:" + a.ID + ":not-taken"
	case strings.Contains(message, "ready binding"):
		return "subscription:" + a.ID + ":not-ready"
	}
	return "subscription:" + a.ID + ":refused"
}

// subOf: the subscription a candidate names, as route decisions record it.
func subOf(a *dispatchAccount) doc {
	if a == nil {
		return nil
	}
	return docOf(struct {
		ID     any `json:"id"`
		Name   any `json:"name"`
		Holder any `json:"holder"`
	}{a.rec["id"], a.rec["name"], a.rec["holder"]})
}

// dispatchSpec: an execution's dispatch record (enqueue): where and how it starts.
type dispatchSpec struct {
	ProjectID        *string                    `json:"projectId"`
	Harness          string                     `json:"harness"`
	Host             string                     `json:"host"`
	Last             string                     `json:"last"`
	Account          string                     `json:"account"`
	Access           string                     `json:"access"`
	Branch           *bool                      `json:"branch"`
	WorkspaceKey     *string                    `json:"workspaceKey"`
	WorkingDirectory *string                    `json:"workingDirectory"`
	Input            *string                    `json:"input"`
	Payload          map[string]json.RawMessage `json:"payload"`
}

// projectSnapshot: the project as an execution froze it at enqueue (projectContext).
type projectSnapshot struct {
	Settings doc `json:"settings"`
	Context  []struct {
		Path string `json:"path"`
		Text string `json:"text"`
	} `json:"context"`
	Revision   json.RawMessage `json:"revision"`
	ContentSha json.RawMessage `json:"contentSha"`
}

// queuedRun: the fields dispatch reads of a queued execution record.
type queuedRun struct {
	ID              string           `json:"id"`
	SessionID       string           `json:"sessionId"`
	DirectedBy      *string          `json:"directedBy"`
	Dispatch        dispatchSpec     `json:"dispatch"`
	ProjectSnapshot *projectSnapshot `json:"projectSnapshot"`
	AgentConfig     struct {
		ID string `json:"id"`
	} `json:"agentConfig"`
}

// queuedOf: the typed view of an execution record; a field of another type reads as absent.
func queuedOf(e doc) queuedRun {
	v, _ := asT[queuedRun](e)
	return v
}

// rankCandidates: the ordered candidate list of one queued execution.
func (a *App) rankCandidates(q *Request, e doc, k *dispatchContext) ([]*candidate, error) {
	run := queuedOf(e)
	x := run.Dispatch
	director := optAny(run.DirectedBy)
	dir := deref(run.DirectedBy)
	projectID := optAny(x.ProjectID)
	harness := x.Harness
	order := []string{}
	if x.Host != "" {
		order = []string{x.Host}
	} else {
		for _, id := range append([]string{x.Last, a.hostID}, k.hostIDs...) {
			if _, ok := k.byID[id]; ok && !inList(order, id) {
				order = append(order, id)
			}
		}
	}
	named := func(v string) func(*dispatchAccount) bool {
		return func(s *dispatchAccount) bool {
			if s.rec["id"] == v || s.rec["name"] == v {
				return true
			}
			for _, b := range bindingsOf(s.rec) {
				if b["binding"] == v {
					return true
				}
			}
			return false
		}
	}
	find := func(list []dispatchAccount, f func(*dispatchAccount) bool) *dispatchAccount {
		for i := range list {
			if f(&list[i]) {
				return &list[i]
			}
		}
		return nil
	}
	var settingsDoc doc
	if run.ProjectSnapshot != nil {
		settingsDoc = run.ProjectSnapshot.Settings
	}
	if settingsDoc == nil && projectID != nil {
		settingsDoc = projectSettingsOf(q, *x.ProjectID)
	}
	settings, _ := asT[runSettings](settingsDoc) // a field of another type reads as absent
	requirements := settings.Execution
	if requirements != nil && len(requirements.Profiles) > 0 && !slices.Contains(requirements.Profiles, run.AgentConfig.ID) {
		requirements = nil
	}
	cos := settings.Environments
	accounts := []dispatchAccount{}
	for _, s := range k.accounts {
		if accountHarness(s) == harness {
			accounts = append(accounts, s)
		}
	}
	out := []*candidate{}
	var subs []*dispatchAccount
	if acc := x.Account; acc != "" {
		s := find(k.accounts, named(acc))
		if s != nil && accountHarness(*s) != harness {
			host := a.hostID
			if len(order) > 0 {
				host = order[0]
			}
			return []*candidate{{Subscription: subOf(s), Host: host, Warnings: []string{}, Skipped: fmt.Sprintf("%s is a %s subscription, not %s", s.rec["name"], s.rec["harness"], harness), Cause: "subscription:" + s.ID + ":harness"}}, nil
		}
		subs = []*dispatchAccount{s}
	} else {
		list := nonNilStrings(settings.Subscriptions)
		pos := func(s *dispatchAccount) int {
			for i, n := range list {
				if n == s.rec["name"] {
					return i
				}
			}
			for i, n := range list {
				if n == s.rec["id"] {
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
		mine := func(s *dispatchAccount) bool {
			return s.rec["holder"] == director || full && s.rec["holder"] == owner || inList(nonNilStrings(s.Take.Projects), deref(x.ProjectID)) && projectID != nil
		}
		weight := func(s *dispatchAccount) int {
			w := pos(s)
			if s.rec["holder"] != director {
				w += 1000000
			}
			return w
		}
		ranked := []*dispatchAccount{}
		for i := range accounts {
			if mine(&accounts[i]) {
				ranked = append(ranked, &accounts[i])
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return weight(ranked[i]) < weight(ranked[j]) })
		var def *dispatchAccount
		if harness == "codex" && defaultAccountBinding() != "" {
			def = find(accounts, named(defaultAccountBinding()))
		}
		useDef := def != nil && mine(def)
		if useDef {
			subs = append(subs, def)
		}
		for _, s := range ranked {
			if def == nil || s.rec["id"] != def.rec["id"] {
				subs = append(subs, s)
			}
		}
		if !useDef {
			subs = append(subs, nil)
		}
	}
	for _, s := range subs {
		for _, id := range order {
			h, hv := k.byID[id], k.views[id]
			if s != nil {
				bound := false
				for _, b := range bindingsOf(s.rec) {
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
			if requirements != nil && requirements.Forge && (len(settings.Repos) == 0 || !contract.HasCapability(k.caps[id], contract.ForgeCapability(strings.TrimSuffix(strings.TrimPrefix(settings.Repos[0].Remote, "https://github.com/"), ".git")))) {
				skip("project forge connection unavailable on "+id, "host:"+id+":forge")
				continue
			}
			missingTool := ""
			if requirements != nil {
				for _, tool := range requirements.Tools {
					if !contract.HasCapability(k.caps[id], contract.ToolCapability(tool)) {
						missingTool = tool
						break
					}
				}
			}
			if missingTool != "" {
				skip("coding tool unavailable: "+missingTool+" on "+id, "host:"+id+":tool:"+missingTool)
				continue
			}
			// A read-only or branchless start (a preset), a workspace key or message attachments run only where the
			// executor honours them (contract.MissingCapability, the same gate the poll applies).
			var attachments []json.RawMessage
			json.Unmarshal(x.Payload["attachments"], &attachments)
			if c, missing := contract.MissingCapability(k.caps[id], contract.StartNeeds{ReadOnly: x.Access == "read", Branchless: x.Branch != nil && !*x.Branch && projectID != nil,
				WorkspaceKey: deref(x.WorkspaceKey) != "", Attachments: len(attachments) > 0}); missing {
				switch c {
				case contract.CapabilityStartAccessV1:
					skip(id+" executor lacks start-access-v1 for a read-only or branchless start; update required", "host:"+id+":start-access")
				case contract.CapabilityWorkspaceKeyV1:
					skip(id+" executor lacks workspace-key-v1; update required", "host:"+id+":workspace-version")
				default:
					skip(id+" executor lacks "+string(c)+"; update required", "host:"+id+":"+string(c)+"-version")
				}
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
			checkout := deref(x.WorkingDirectory)
			if checkout == "" {
				for _, co := range cos {
					if co.HostID == id {
						checkout = co.CheckoutPath
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
						of = " of " + *x.ProjectID
					}
					skip(fmt.Sprintf("checkout %s%s is outside FLEET_PROJECT_ROOTS on %s", checkout, of, id), fmt.Sprintf("host:%s:roots:%v", id, jsText(projectID)))
					continue
				}
			}
			// The repository's git connection must be on the host, else the run fails after setup (2026-11-11).
			if have, known := k.conns[id]; known {
				if need := repoConnection(settings.Repos, cos, id); need != "" && !slices.Contains(have, need) {
					skip(fmt.Sprintf("%s has no git connection %s", id, need), fmt.Sprintf("host:%s:connection:%s", id, need))
					continue
				}
			}
			if s != nil {
				name := s.Name
				b, err := resolveAccount(q, &name, harness, id, director, projectID)
				if err != nil {
					var he *HTTPError
					if !asHTTP(err, &he) {
						return nil, err
					}
					skip(he.Msg, causeOf(*s, id, he.Msg))
					continue
				}
				c.binding = b
				if over := readingOverSwitch(s.rec, k.readings[s.ID]); over != "" {
					skip(over, "subscription:"+s.ID+":limit")
					continue
				}
				if s.rec["harness"] == "claude" && s.rec["holder"] != h["holder"] {
					c.Warnings = append(c.Warnings, fmt.Sprintf("%s can read this login (Claude on a host held by someone else)", h["holder"]))
				}
			} else {
				if harness != "codex" && x.Account == "" {
					skip(fmt.Sprintf("no %s subscription of %v applies", harness, jsText(director)), fmt.Sprintf("director:%v:%s", jsText(director), harness))
					continue
				}
				if dir != "" && h["holder"] != dir {
					skip(fmt.Sprintf("%s login belongs to %s", id, h["holder"]), "host:"+id+":holder")
					continue
				}
				if hs := hv.Harnesses[harness]; hs.Allowed != nil && !*hs.Allowed {
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
	reached, err := budgetsReached(q, deref(x.ProjectID), dir)
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
	if c, ok := err.(*codedError); ok {
		*he = &c.HTTPError
		return true
	}
	h, ok := err.(*HTTPError)
	*he = h
	return ok
}

func recordDecision(q *Request, e doc, polling string, cs []*candidate, winner *candidate) error {
	run := queuedOf(e)
	var w []byte
	if winner != nil {
		w = jsonBytes(struct {
			Subscription doc    `json:"subscription"`
			Host         string `json:"host"`
		}{winner.Subscription, winner.Host})
	}
	return q.db().DispRouteDecision(q.ctx(), db.DispRouteDecisionParams{ExecutionID: run.ID, ProjectID: run.Dispatch.ProjectID, HostID: strPtr(polling), Candidates: jsonBytes(cs), Winner: w})
}

var causeHost = regexp.MustCompile(`^host:([^:]+)`)

// dispatchRun: one dispatch pass; when polling == "", evaluates and assigns every host.
func (a *App) dispatchRun(q *Request, polling string) error {
	ids, err := q.db().DispQueuedIDs(q.ctx())
	if err != nil {
		return err
	}
	type stall struct{ reason, director, ask, projectID string }
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
		e, err := recGet[doc](q, recExecution, id)
		if err != nil {
			return err
		}
		if !claimable(e) {
			continue
		}
		if sid := queuedOf(e).SessionID; sid != "" && !txHasLock(q.tx, "session:"+sid) {
			if canAcquireTxLock(q.tx, "session:"+sid) {
				if err := acquireTxLock(q, "session:"+sid); err != nil {
					return err
				}
				// Re-read after acquiring the session lock so any concurrent session write is visible (a stop does not take it:
				// the row lock of claimRow serializes with that).
				e, err = recGet[doc](q, recExecution, id)
				if err != nil {
					return err
				}
				if !claimable(e) {
					continue
				}
			} else {
				onEnd(q.tx, func(committed bool) {
					if committed {
						a.wakeDispatch()
					}
				})
				continue
			}
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
			if e["pipelineRunId"] != nil && !txHasLock(q.tx, GLOBAL) {
				onEnd(q.tx, func(committed bool) {
					if committed {
						a.wakeDispatch()
					}
				})
				continue
			}
			if polling != "" && best.Host != polling {
				continue
			}
			// Lock the row and read it again before the claim: a stop (or any writer) committed since the read above
			// wins, and none commits between here and this claim. Only before a write, never per candidate: a row lock
			// gives the pass a transaction id, which holds back the change stream until it ends (stream.go visible).
			if e, err = claimRow(q, id); err != nil {
				return err
			}
			if e == nil {
				continue
			}
			delete(e, "waitReason")
			delete(e, "waitDetail")
			// A refused start ends this execution only: returning would fail the whole pass, and every later pass,
			// while the execution stays queued (PDS18 outage, 2026-11-12). Its partial writes roll back first.
			queued := docOf(e)
			if err := exec(q, "SAVEPOINT start_command"); err != nil {
				return err
			}
			if _, err := a.startCommand(q, e, best.Host, best.binding); err != nil {
				var he *HTTPError
				if !asHTTP(err, &he) {
					return err // database or transport: retried by the next pass
				}
				if err := exec(q, "ROLLBACK TO SAVEPOINT start_command"); err != nil {
					return err
				}
				queued["state"], queued["stopDetail"] = "cancelled", "start refused: "+he.Msg
				if err := recPut(q, recExecution, &queued); err != nil {
					return err
				}
				if err := q.db().DispWaitReason(q.ctx(), db.DispWaitReasonParams{ID: id}); err != nil {
					return err
				}
				continue
			}
			if err := exec(q, "RELEASE SAVEPOINT start_command"); err != nil {
				return err
			}
			if err := recordDecision(q, e, firstString(polling, best.Host), cs, best); err != nil {
				return err
			}
			if err := q.db().DispWaitReason(q.ctx(), db.DispWaitReasonParams{ID: id}); err != nil {
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
			// As before a claim: a stop committed since the unlocked read wins.
			if e, err = claimRow(q, id); err != nil {
				return err
			}
			if e == nil {
				continue
			}
			e["state"], e["stopDetail"], e["waitReason"], e["waitDetail"] = "cancelled", reason, waitKindOf(cause), reason
			if err := recPut(q, recExecution, &e); err != nil {
				return err
			}
			if err := recordDecision(q, e, polling, cs, nil); err != nil {
				return err
			}
			continue
		}
		if e["waitDetail"] != reason {
			if e, err = claimRow(q, id); err != nil {
				return err
			}
			if e == nil {
				continue
			}
		}
		// Checked again on the locked row: an overlapping pass may have written the same wait meanwhile.
		if e["waitDetail"] != reason {
			kind := waitKindOf(cause)
			e["waitReason"], e["waitDetail"] = kind, reason
			if err := recPut(q, recExecution, &e); err != nil {
				return err
			}
			if err := q.db().DispWaitReason(q.ctx(), db.DispWaitReasonParams{ID: id, WaitReason: &kind}); err != nil {
				return err
			}
			if err := recordDecision(q, e, polling, cs, nil); err != nil {
				return err
			}
		}
		var holder string
		if m := causeHost.FindStringSubmatch(cause); m != nil {
			if _, ok := k.byID[m[1]]; ok {
				holder = k.views[m[1]].Holder
			}
		} else if first != nil && first.Subscription != nil {
			holder, _ = first.Subscription["holder"].(string)
		}
		if _, ok := causes[cause]; !ok {
			run := queuedOf(e)
			causes[cause] = stall{reason, deref(run.DirectedBy), holder, deref(run.Dispatch.ProjectID)}
			causeOrder = append(causeOrder, cause)
		}
	}
	// Only the stall causes this pass raises: a key, budget-ask or other area's question keeps its cause.
	stalls := make([]Problem, 0, len(causeOrder))
	for _, cause := range causeOrder {
		c := causes[cause]
		tail := " Change the setting to let it start."
		if c.ask != "" && c.ask != c.director {
			tail = fmt.Sprintf(" Ask %v, or change the setting.", c.ask)
		}
		stalls = append(stalls, Problem{Cause: cause, Text: fmt.Sprintf("Work is waiting: %v.%s", c.reason, tail), ProjectID: c.projectID, AddressedTo: c.director, Options: []any{"ok"}})
	}
	return problemsSet(q, "", dispatchStallCauses, stalls)
}

// dispatchStallCauses: the causes a dispatch pass raises and closes (problemsSet).
const dispatchStallCauses = `^(subscription|placement|host|director|execution):|^budget:.*:reached$`

// claimRow locks an execution's row for this transaction and returns it while it is still claimable, nil otherwise.
// executionStop takes the same row lock; every dispatch write of a queued execution goes through here.
func claimRow(q *Request, id string) (doc, error) {
	if err := q.db().DispExecLock(q.ctx(), id); err != nil {
		return nil, err
	}
	e, err := recGet[doc](q, recExecution, id)
	if err != nil {
		return nil, err
	}
	if !claimable(e) {
		return nil, nil
	}
	return e, nil
}

// claimable: queued agent work no host has and no stop asked for.
func claimable(e doc) bool {
	return e["dispatch"] != nil && e["hostId"] == nil && e["state"] == "preparing" && e["stopRequested"] != true
}

// startSession: the fields startCommand reads of the session record.
type startSession struct {
	ID                 string                     `json:"id"`
	DeliveryID         string                     `json:"deliveryId"`
	Starter            string                     `json:"starter"`
	WorkspaceSnapshots map[string]json.RawMessage `json:"workspaceSnapshots"`
}

// startBinding: the fields startCommand reads of the chosen binding (resolveAccount).
type startBinding struct {
	Binding *string `json:"binding"`
	Holder  string  `json:"holder"`
	Taken   bool    `json:"taken"`
}

// startCommand: the start command of a claimed execution, from the session as it is now and the chosen binding.
func (a *App) startCommand(q *Request, e doc, host string, binding doc) (doc, error) {
	sessionID, _ := e["sessionId"].(string)
	liveSession, err := recGet[doc](q, recSession, sessionID)
	if err != nil {
		return nil, err
	}
	if err := taskAllowsWork(q, firstString(e["deliveryId"], liveSession["deliveryId"], liveSession["taskId"])); err != nil {
		return nil, err
	}
	run := queuedOf(e)
	d := run.Dispatch
	sv, _ := asT[startSession](liveSession)
	bv, _ := asT[startBinding](binding)
	s := docOf(liveSession) // a copy: the start reads the dispatch's project and checkout
	s["projectId"], s["workingDirectory"], s["workspaceKey"] = optAny(d.ProjectID), optAny(d.WorkingDirectory), optAny(d.WorkspaceKey)
	e["hostId"] = host
	if s["account"] != nil || binding != nil {
		e["requestedAccount"] = s["account"]
		if binding != nil {
			e["effectiveAccount"] = binding
		} else {
			e["effectiveAccount"] = docOf(struct {
				Binding any  `json:"binding"`
				Legacy  bool `json:"legacy"`
			}{s["account"], true})
		}
	}
	if binding != nil && bv.Taken {
		e["takenFrom"], e["directedBy"] = e["directedBy"], binding["holder"]
	}
	cfg := rawMap(s["agentConfig"])
	// The start payload is contract.StartPayload, field by field: the enqueue extras (inputs, skills, webRoute) are named
	// fields, never a blind merge over the fields above (C7).
	p := &contract.StartPayload{Harness: ptr(harnessOf(s)), Text: strPtr(d.Input), Setup: strPtr(nonNil(s["setup"], "run")),
		WorkspaceKey: strPtr(d.WorkspaceKey), WorkingDirectory: strPtr(d.WorkingDirectory), Model: strPtr(e["model"]), Effort: strPtr(e["effort"]),
		Instructions: ptr(defaultInstructions), NativeSessionId: strp(s, "nativeSessionId")} // "" still reaches the executor's check
	if s["account"] != nil || binding != nil {
		if binding != nil {
			p.Account = strPtr(bv.Binding)
		} else {
			p.Account = strPtr(s["account"])
		}
	}
	if pid := deref(d.ProjectID); pid != "" {
		if e["projectSnapshot"] == nil { // queued before project snapshots: freeze the project as it is now
			snapshot, err := projectContext(q, pid)
			if err != nil {
				return nil, err
			}
			e["projectSnapshot"] = snapshot
		}
		snap, _ := asT[projectSnapshot](e["projectSnapshot"])
		p.ProjectId, p.Project = &pid, rawMap(snap.Settings)
	}
	snapshot, _ := asT[*projectSnapshot](e["projectSnapshot"])
	networkSession := docOf(s)
	if snapshot != nil {
		networkSession["projectSettings"] = snapshot.Settings
	}
	if gitNetworkOf(q, networkSession) {
		p.GitNetwork = ptr(true)
	}
	if cfg != nil {
		p.AgentConfig, p.Instructions = cfg, ptr("")
		if string(cfg["fleetOperations"]) == "true" { // the profile opts in to every operation its director may call (fleet-pm-agent.md §1)
			if dir := q.peopleSnapshot().person(firstString(e["directedBy"], ownerEmail(q))); dir != nil {
				p.FleetOperations = rawList(fleetOperations(dir))
			}
		}
	}
	extra := d.Payload
	if v, ok := extra["inputs"]; ok {
		if err := json.Unmarshal(v, &p.Inputs); err != nil {
			return nil, failf(400, "execution %v: pipeline inputs are not [{artifactId, path}]: %v", e["id"], err)
		}
	}
	var list []any
	json.Unmarshal(extra["attachments"], &list)
	if len(list) > 0 {
		inputs, attachments := startAttachments(run.ID, list)
		p.Inputs, p.Attachments = append(p.Inputs, inputs...), attachments
	}
	var webRoute *string
	json.Unmarshal(extra["webRoute"], &webRoute)
	p.Skills, p.WebRoute = rawList(extra["skills"]), strPtr(webRoute)
	if snapshot != nil {
		instruction := ""
		if p.Instructions != nil {
			instruction = *p.Instructions
		}
		for _, f := range snapshot.Context {
			instruction += "\n\nProject context " + f.Path + ":\n" + f.Text
		}
		p.Instructions = &instruction
		workspaces := sv.WorkspaceSnapshots
		if workspaces == nil {
			workspaces = map[string]json.RawMessage{}
		}
		key := host + ":" + deref(d.ProjectID) + ":" + deref(d.WorkspaceKey)
		if ws := workspaces[key]; len(ws) == 0 || string(ws) == "null" {
			workspaces[key] = jsonBytes(struct {
				ExecutionID      any             `json:"executionId"`
				ProjectID        any             `json:"projectId"`
				WorkspaceKey     any             `json:"workspaceKey"`
				Project          doc             `json:"project"`
				Revision         json.RawMessage `json:"revision"`
				ContentSha       json.RawMessage `json:"contentSha"`
				WorkingDirectory any             `json:"workingDirectory"`
			}{e["id"], optAny(d.ProjectID), optAny(d.WorkspaceKey), snapshot.Settings, nullRaw(snapshot.Revision), nullRaw(snapshot.ContentSha), s["workingDirectory"]})
			liveSession["workspaceSnapshots"] = workspaces
			if err := recPut(q, recSession, &liveSession); err != nil {
				return nil, err
			}
		}
		e["workspaceSnapshot"] = workspaces[key]
	}
	// The session's preset (presets.go): read access, no branch and its starter instruction, on every turn.
	if s["access"] == "read" {
		p.ReadOnly = ptr(true)
	}
	if s["branch"] == false {
		p.Branch = ptr(false)
	}
	if st := sv.Starter; st != "" {
		p.Instructions = ptr(strings.TrimSpace(deref(p.Instructions) + "\n\nThis conversation's preset. " + st))
	}
	command := docOf(struct {
		ID          string `json:"id"`
		ExecutionID any    `json:"executionId"`
		SessionID   any    `json:"sessionId"`
		HostID      string `json:"hostId"`
		Kind        string `json:"kind"`
		Status      string `json:"status"`
		HandedOut   bool   `json:"handedOut"`
	}{newID(), e["id"], s["id"], host, string(contract.CommandKindStart), "pending", false})
	command["payload"] = p // the payload itself: agentDecorate completes it below
	if err := agentDecorate(q, s, e, p); err != nil {
		return nil, err
	}
	if granted := jsonStrings(cfg["connections"]); len(granted) > 0 && e["pipelineRunId"] == nil {
		minted, err := mint(q, docOf(struct {
			ExecutionID   any `json:"executionId"`
			DirectedBy    any `json:"directedBy"`
			ProjectID     any `json:"projectId"`
			ExperimentID  any `json:"experimentId"`
			PipelineRunID any `json:"pipelineRunId"`
		}{e["id"], e["directedBy"], s["projectId"], e["experimentId"], s["pipelineRunId"]}), granted)
		if err != nil {
			return nil, err
		}
		if minted != nil {
			grant, _ := asT[struct {
				Token       *string           `json:"token"`
				Connections json.RawMessage   `json:"connections"`
				Refused     []json.RawMessage `json:"refused"`
			}](minted)
			if grant.Token != nil {
				p.RunToken, p.Connections = strPtr(grant.Token), rawList(grant.Connections)
			}
			if len(grant.Refused) > 0 {
				e["connectionsRefused"] = minted["refused"]
			}
		}
	}
	if err := recPut(q, recExecution, &e); err != nil {
		return nil, err
	}
	if err := recPut(q, recCommand, &command); err != nil {
		return nil, err
	}
	if err := preparePipelineCommand(q, e, command); err != nil {
		return nil, err
	}
	if binding != nil && bv.Taken {
		task := sv.DeliveryID
		if task == "" {
			t, err := q.db().DispExecTask(q.ctx(), run.ID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return nil, err
			}
			task = deref(t)
		}
		if task != "" {
			if err := takeTaskFrom(q, task, bv.Holder, e["takenFrom"]); err != nil {
				return nil, err
			}
		}
	}
	return command, nil
}

// nullRaw: an absent JSON value as null.
func nullRaw(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage("null")
	}
	return v
}
