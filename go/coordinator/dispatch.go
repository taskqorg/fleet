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
	"maps"
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
	if err := a.store.Read(ctx, nil, func(q *Request) error {
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
	if err := a.store.Read(ctx, nil, func(q *Request) error {
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
	return a.store.Write(ctx, nil, locks, func(q *Request) error {
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

type candidateSubscription struct {
	ID     string  `json:"id"`
	Name   *string `json:"name"`
	Holder *string `json:"holder"`
}

type candidate struct {
	Subscription *candidateSubscription `json:"subscription"`
	Host         string                 `json:"host"`
	Skipped      string                 `json:"skipped,omitempty"`
	Cause        string                 `json:"cause,omitempty"`
	Warnings     []string               `json:"warnings"`
	Stop         bool                   `json:"stop,omitempty"`
	binding      *resolvedAccount
}

type dispatchContext struct {
	hostIDs  []string                // the ids of hosts in host order
	hosts    map[string]dispatchHost // registered execution hosts (roles empty)
	seen     map[string]float64
	caps     map[string][]string
	roots    map[string][]string
	conns    map[string][]string // git connection names each host reported; absent: unknown
	accounts []dispatchAccountRec
	readings map[string][]Reading
	orders   map[string]contract.DispatchOrder // the directors' order setting, read once per pass
}

// dispatchHost: the fields dispatch reads of a live execution host record (hostsLive).
type dispatchHost struct {
	ID        string
	Holder    *string
	Intake    *string
	Harnesses map[string]struct {
		Allowed *bool `json:"allowed"`
	}
}

// hostSeen: the fields dispatch reads of a host observation.
type hostSeen struct {
	Capabilities   []string  `json:"capabilities"`
	GitConnections *[]string `json:"gitConnections"`
	ProjectRoots   *[]string `json:"projectRoots"`
}

// dispatchAccountRec: a subscription (managed_account) decoded once for dispatch.
type dispatchAccountRec struct {
	ID             string            `json:"-"`
	Name           *string           `json:"-"`
	Holder         *string           `json:"-"`
	Harness        *string           `json:"-"`
	SwitchPct      *float64          `json:"switchPct"`
	Take           json.RawMessage   `json:"take"`
	Bindings       []json.RawMessage `json:"bindings"`
	takeProjects   []string
	parsedBindings []accountBinding
}

func loadDispatchContext(q *Request) (*dispatchContext, error) {
	hs, err := hostsLive(q)
	if err != nil {
		return nil, err
	}
	k := &dispatchContext{hosts: map[string]dispatchHost{}, caps: map[string][]string{}, seen: map[string]float64{}, roots: map[string][]string{}, conns: map[string][]string{}, orders: map[string]contract.DispatchOrder{}}
	for _, h := range hs {
		if !h.executes() {
			continue
		}
		v := dispatchHost{ID: h.ID, Holder: h.Holder, Intake: h.Intake}
		_ = lenientDecode(h.Harnesses, &v.Harnesses)
		k.hostIDs = append(k.hostIDs, v.ID)
		k.hosts[v.ID] = v
	}
	obs, err := q.db().HostObservations(q.ctx())
	if err != nil {
		return nil, err
	}
	for _, r := range obs {
		var o hostSeen
		if err := lenientDecode(r.Payload, &o); err != nil {
			return nil, err
		}
		k.caps[r.ID] = nonNilStrings(o.Capabilities)
		k.seen[r.ID] = q.app.effectiveHostSeen(r.ID, timeMs(r.LastSeenAt))
		if o.GitConnections != nil {
			k.conns[r.ID] = nonNilStrings(*o.GitConnections)
		}
		if o.ProjectRoots != nil {
			k.roots[r.ID] = nonNilStrings(*o.ProjectRoots)
		}
	}
	accountRows, err := q.db().AccountRows(q.ctx())
	if err != nil {
		return nil, err
	}
	accounts := make([]dispatchAccountRec, 0, len(accountRows))
	for _, r := range accountRows {
		var a dispatchAccountRec
		if err := lenientDecode(r.Payload, &a); err != nil {
			return nil, err
		}
		a.ID, a.Name, a.Holder, a.Harness = r.ID, r.Name, r.Holder, r.Harness
		a.takeProjects = takeProjectsOf(a.Take)
		a.parsedBindings = bindingsIn(a.Bindings)
		accounts = append(accounts, a)
	}
	k.accounts = accounts
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

func accountHarness(a dispatchAccountRec) string {
	if deref(a.Harness) != "" {
		return *a.Harness
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
func causeOf(a dispatchAccountRec, host, message string) string {
	switch {
	case strings.Contains(message, "Paused"):
		return "subscription:" + a.ID + ":paused"
	case strings.Contains(message, "not allowed on host"):
		return "placement:" + host + ":" + deref(a.Harness)
	case strings.Contains(message, "held by"):
		return "subscription:" + a.ID + ":not-taken"
	case strings.Contains(message, "ready binding"):
		return "subscription:" + a.ID + ":not-ready"
	}
	return "subscription:" + a.ID + ":refused"
}

// subOf: the subscription a candidate names, as route decisions record it.
func subOf(a *dispatchAccountRec) *candidateSubscription {
	if a == nil {
		return nil
	}
	return &candidateSubscription{ID: a.ID, Name: a.Name, Holder: a.Holder}
}

// dispatchSpec: an execution's dispatch record (enqueue): where and how it starts.
type dispatchSpec struct {
	ProjectID        *string         `json:"projectId,omitempty"`
	Harness          string          `json:"harness,omitempty"`
	Host             *string         `json:"host,omitempty"`
	Last             *string         `json:"last,omitempty"`
	Account          *string         `json:"account,omitempty"`
	Access           *string         `json:"access,omitempty"`
	Branch           *bool           `json:"branch,omitempty"`
	WorkspaceKey     *string         `json:"workspaceKey,omitempty"`
	WorkingDirectory *string         `json:"workingDirectory,omitempty"`
	Input            *string         `json:"input,omitempty"`
	Payload          json.RawMessage `json:"payload,omitempty"`
	rest             recordRest
}

func (d *dispatchSpec) UnmarshalJSON(b []byte) error {
	type plain dispatchSpec
	return restDecode(b, (*plain)(d), &d.rest)
}

func (d dispatchSpec) MarshalJSON() ([]byte, error) {
	type plain dispatchSpec
	return restEncode(plain(d), d.rest)
}

type dispatchPayload struct {
	Inputs      json.RawMessage   `json:"inputs"`
	Attachments []any             `json:"attachments"`
	WebRoute    *string           `json:"webRoute"`
	Skills      []json.RawMessage `json:"skills"`
}

func (d dispatchSpec) payload() dispatchPayload {
	var p dispatchPayload
	if jsonGiven(d.Payload) {
		_ = lenientDecode(d.Payload, &p)
	}
	return p
}

// projectSnapshot: the project as an execution froze it at enqueue (projectContext).
type projectSnapshot struct {
	Settings json.RawMessage `json:"settings"`
	Context  []struct {
		Path string `json:"path"`
		Text string `json:"text"`
	} `json:"context"`
	Revision   json.RawMessage `json:"revision"`
	ContentSha json.RawMessage `json:"contentSha"`
}

// dispatchAgentProfile: the agentConfig fields dispatch reads.
type dispatchAgentProfile struct {
	FleetOperations bool            `json:"fleetOperations"`
	Connections     json.RawMessage `json:"connections"`
}

// dispatchExecution: the execution record fields dispatch reads and writes; other fields stay in rest.
type dispatchExecution struct {
	ID                    string          `json:"id"`
	SessionID             string          `json:"sessionId,omitempty"`
	HostID                *string         `json:"hostId,omitempty"`
	State                 string          `json:"state,omitempty"`
	StopRequested         *bool           `json:"stopRequested,omitempty"`
	StopDetail            string          `json:"stopDetail,omitempty"`
	WaitReason            *string         `json:"waitReason,omitempty"`
	WaitDetail            *string         `json:"waitDetail,omitempty"`
	DeliveryID            string          `json:"deliveryId,omitempty"`
	DirectedBy            *string         `json:"directedBy,omitempty"`
	TakenFrom             json.RawMessage `json:"takenFrom,omitempty"`
	Model                 string          `json:"model,omitempty"`
	Effort                json.RawMessage `json:"effort,omitempty"`
	AgentConfig           json.RawMessage `json:"agentConfig,omitempty"`
	ProjectSnapshot       json.RawMessage `json:"projectSnapshot,omitempty"`
	WorkspaceSnapshot     json.RawMessage `json:"workspaceSnapshot,omitempty"`
	Dispatch              *dispatchSpec   `json:"dispatch,omitempty"`
	PipelineRunID         *string         `json:"pipelineRunId,omitempty"`
	PipelineStepID        *string         `json:"pipelineStepId,omitempty"`
	ExperimentID          *string         `json:"experimentId,omitempty"`
	RequestedAccount      json.RawMessage `json:"requestedAccount,omitempty"`
	EffectiveAccount      json.RawMessage `json:"effectiveAccount,omitempty"`
	ConnectionsRefused    []string        `json:"connectionsRefused,omitempty"`
	ContinuationKey       string          `json:"continuationKey,omitempty"`
	ForkOfNativeSessionID string          `json:"forkOfNativeSessionId,omitempty"`
	HandoffID             string          `json:"handoffId,omitempty"`
	rest                  recordRest
}

func (e *dispatchExecution) UnmarshalJSON(b []byte) error {
	type plain dispatchExecution
	return restDecode(b, (*plain)(e), &e.rest)
}

func (e dispatchExecution) MarshalJSON() ([]byte, error) {
	type plain dispatchExecution
	return restEncode(plain(e), e.rest)
}

func (e *dispatchExecution) projectID() *string {
	if e.Dispatch == nil {
		return nil
	}
	return e.Dispatch.ProjectID
}

// rankCandidates: the ordered candidate list of one queued execution.
func (a *App) rankCandidates(q *Request, e *dispatchExecution, k *dispatchContext) ([]*candidate, error) {
	var x dispatchSpec
	if e.Dispatch != nil {
		x = *e.Dispatch
	}
	dir := deref(e.DirectedBy)
	projectID := x.ProjectID
	harness := x.Harness
	order := []string{}
	if deref(x.Host) != "" {
		order = []string{*x.Host}
	} else {
		for _, id := range append([]string{deref(x.Last), a.hostID}, k.hostIDs...) {
			if _, ok := k.hosts[id]; ok && !inList(order, id) {
				order = append(order, id)
			}
		}
	}
	named := func(v string) func(*dispatchAccountRec) bool {
		return func(s *dispatchAccountRec) bool {
			if s.ID == v || (s.Name != nil && *s.Name == v) {
				return true
			}
			for _, b := range s.parsedBindings {
				if b.Binding != nil && *b.Binding == v {
					return true
				}
			}
			return false
		}
	}
	find := func(list []dispatchAccountRec, f func(*dispatchAccountRec) bool) *dispatchAccountRec {
		for i := range list {
			if f(&list[i]) {
				return &list[i]
			}
		}
		return nil
	}
	var settings runSettings
	var snapSettings json.RawMessage
	if jsonGiven(e.ProjectSnapshot) {
		var snap projectSnapshot
		_ = lenientDecode(e.ProjectSnapshot, &snap)
		snapSettings = snap.Settings
	}
	if jsonIsObject(snapSettings) {
		_ = lenientDecode(snapSettings, &settings)
	} else if projectID != nil {
		_ = lenientDecode(jsonBytes(projectSettingsOf(q, *projectID)), &settings)
	}
	requirements := settings.Execution
	prof, _ := profileOf(e.AgentConfig)
	if requirements != nil && len(requirements.Profiles) > 0 && !slices.Contains(requirements.Profiles, prof.ID) {
		requirements = nil
	}
	cos := settings.Environments
	accounts := []dispatchAccountRec{}
	for _, s := range k.accounts {
		if accountHarness(s) == harness {
			accounts = append(accounts, s)
		}
	}
	out := []*candidate{}
	var subs []*dispatchAccountRec
	if acc := deref(x.Account); acc != "" {
		s := find(k.accounts, named(acc))
		if s != nil && accountHarness(*s) != harness {
			host := a.hostID
			if len(order) > 0 {
				host = order[0]
			}
			return []*candidate{{Subscription: subOf(s), Host: host, Warnings: []string{}, Skipped: fmt.Sprintf("%s is a %s subscription, not %s", deref(s.Name), deref(s.Harness), harness), Cause: "subscription:" + s.ID + ":harness"}}, nil
		}
		subs = []*dispatchAccountRec{s}
	} else {
		list := nonNilStrings(settings.Subscriptions)
		pos := func(s *dispatchAccountRec) int {
			for i, n := range list {
				if s.Name != nil && *s.Name == n {
					return i
				}
			}
			for i, n := range list {
				if s.ID == n {
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
		mine := func(s *dispatchAccountRec) bool {
			return sameText(s.Holder, e.DirectedBy) || full && s.Holder != nil && *s.Holder == owner || projectID != nil && inList(s.takeProjects, *projectID)
		}
		weight := func(s *dispatchAccountRec) int {
			w := pos(s)
			if !sameText(s.Holder, e.DirectedBy) {
				w += 1000000
			}
			return w
		}
		ranked := []*dispatchAccountRec{}
		for i := range accounts {
			if mine(&accounts[i]) {
				ranked = append(ranked, &accounts[i])
			}
		}
		sort.SliceStable(ranked, func(i, j int) bool { return weight(ranked[i]) < weight(ranked[j]) })
		var def *dispatchAccountRec
		if harness == "codex" && defaultAccountBinding() != "" {
			def = find(accounts, named(defaultAccountBinding()))
		}
		useDef := def != nil && mine(def)
		if useDef {
			subs = append(subs, def)
		}
		for _, s := range ranked {
			if def == nil || s.ID != def.ID {
				subs = append(subs, s)
			}
		}
		if !useDef {
			subs = append(subs, nil)
		}
	}
	dp := x.payload()
	for _, s := range subs {
		for _, id := range order {
			hv, ok := k.hosts[id]
			if s != nil {
				bound := false
				for _, b := range s.parsedBindings {
					if b.HostID != nil && *b.HostID == id {
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
			if !ok {
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
			if mc, missing := contract.MissingCapability(k.caps[id], contract.StartNeeds{ReadOnly: deref(x.Access) == "read", Branchless: x.Branch != nil && !*x.Branch && projectID != nil,
				WorkspaceKey: deref(x.WorkspaceKey) != "", Attachments: len(dp.Attachments) > 0}); missing {
				switch mc {
				case contract.CapabilityStartAccessV1:
					skip(id+" executor lacks start-access-v1 for a read-only or branchless start; update required", "host:"+id+":start-access")
				case contract.CapabilityWorkspaceKeyV1:
					skip(id+" executor lacks workspace-key-v1; update required", "host:"+id+":workspace-version")
				default:
					skip(id+" executor lacks "+string(mc)+"; update required", "host:"+id+":"+string(mc)+"-version")
				}
				continue
			}
			if !a.holderMayRun(deref(hv.Holder), deref(projectID), dir) {
				skip(fmt.Sprintf("%s is held by %s, who has no grant on %v", id, deref(hv.Holder), jsText(projectID)), "host:"+id+":trust")
				continue
			}
			if deref(hv.Intake) == "paused" {
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
						of = " of " + *projectID
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
				name := deref(s.Name)
				b, err := resolveAccount(q, &name, harness, id, optAny(e.DirectedBy), optAny(projectID))
				if err != nil {
					var he *HTTPError
					if !asHTTP(err, &he) {
						return nil, err
					}
					skip(he.Msg, causeOf(*s, id, he.Msg))
					continue
				}
				c.binding = b
				if over := readingOverSwitch(s, k.readings[s.ID]); over != "" {
					skip(over, "subscription:"+s.ID+":limit")
					continue
				}
				if deref(s.Harness) == "claude" && !sameText(s.Holder, hv.Holder) {
					c.Warnings = append(c.Warnings, fmt.Sprintf("%s can read this login (Claude on a host held by someone else)", deref(hv.Holder)))
				}
			} else {
				if harness != "codex" && deref(x.Account) == "" {
					skip(fmt.Sprintf("no %s subscription of %v applies", harness, jsText(e.DirectedBy)), fmt.Sprintf("director:%v:%s", jsText(e.DirectedBy), harness))
					continue
				}
				if dir != "" && deref(hv.Holder) != dir {
					skip(fmt.Sprintf("%s login belongs to %s", id, deref(hv.Holder)), "host:"+id+":holder")
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
			subs := map[string]bool{}
			for _, c := range out {
				if c.Skipped == "" && c.Subscription != nil {
					subs[c.Subscription.ID] = true
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
		ids := map[string]bool{}
		for _, o := range live {
			if sameText(o.Subscription.Holder, c.Subscription.Holder) {
				ids[o.Subscription.ID] = true
			}
		}
		if len(ids) > 1 {
			c.Warnings = append(c.Warnings, "two personal subscriptions of one holder in one order list (provider rate-limit terms)")
		}
	}
	reached, err := budgetsReached(q, deref(projectID), dir)
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

// jsText: an optional string as a JavaScript template literal prints it ("null" when absent).
func jsText(v *string) string {
	if v == nil {
		return "null"
	}
	return *v
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

func recordDecision(q *Request, e *dispatchExecution, polling string, cs []*candidate, winner *candidate) error {
	var w []byte
	if winner != nil {
		w = jsonBytes(struct {
			Subscription *candidateSubscription `json:"subscription"`
			Host         string                 `json:"host"`
		}{winner.Subscription, winner.Host})
	}
	return q.db().DispRouteDecision(q.ctx(), db.DispRouteDecisionParams{ExecutionID: e.ID, ProjectID: e.projectID(), HostID: strPtr(polling), Candidates: jsonBytes(cs), Winner: w})
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
		e, err := executionGetAs[dispatchExecution](q, id)
		if err != nil {
			return err
		}
		if !claimable(&e) {
			continue
		}
		if sid := e.SessionID; sid != "" && !txHasLock(q.tx, "session:"+sid) {
			if canAcquireTxLock(q.tx, "session:"+sid) {
				if err := acquireTxLock(q, "session:"+sid); err != nil {
					return err
				}
				// Re-read after acquiring the session lock so any concurrent session write is visible (a stop does not take it:
				// the row lock of claimRow serializes with that).
				e, err = executionGetAs[dispatchExecution](q, id)
				if err != nil {
					return err
				}
				if !claimable(&e) {
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
		cs, err := a.rankCandidates(q, &e, k)
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
			if e.PipelineRunID != nil && !txHasLock(q.tx, GLOBAL) {
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
			claimed, err := claimRow(q, id)
			if err != nil {
				return err
			}
			if claimed == nil {
				continue
			}
			claimed.WaitReason, claimed.WaitDetail = nil, nil
			delete(claimed.rest, "waitReason")
			delete(claimed.rest, "waitDetail")
			// A refused start ends this execution only: returning would fail the whole pass, and every later pass,
			// while the execution stays queued (PDS18 outage, 2026-11-12). Its partial writes roll back first.
			queued := *claimed
			queued.rest = maps.Clone(claimed.rest)
			if err := exec(q, "SAVEPOINT start_command"); err != nil {
				return err
			}
			if _, err := a.startCommand(q, claimed, best.Host, best.binding); err != nil {
				var he *HTTPError
				if !asHTTP(err, &he) {
					return err // database or transport: retried by the next pass
				}
				if err := exec(q, "ROLLBACK TO SAVEPOINT start_command"); err != nil {
					return err
				}
				queued.State, queued.StopDetail = "cancelled", "start refused: "+he.Msg
				if err := executionPut(q, &queued); err != nil {
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
			if err := recordDecision(q, claimed, firstString(polling, best.Host), cs, best); err != nil {
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
			claimed, err := claimRow(q, id)
			if err != nil {
				return err
			}
			if claimed == nil {
				continue
			}
			kind := waitKindOf(cause)
			claimed.State, claimed.StopDetail = "cancelled", reason
			claimed.WaitReason, claimed.WaitDetail = &kind, &reason
			delete(claimed.rest, "waitReason")
			delete(claimed.rest, "waitDetail")
			if err := executionPut(q, claimed); err != nil {
				return err
			}
			if err := recordDecision(q, claimed, polling, cs, nil); err != nil {
				return err
			}
			continue
		}
		cur := &e
		if deref(cur.WaitDetail) != reason {
			claimed, err := claimRow(q, id)
			if err != nil {
				return err
			}
			if claimed == nil {
				continue
			}
			cur = claimed
		}
		// Checked again on the locked row: an overlapping pass may have written the same wait meanwhile.
		if deref(cur.WaitDetail) != reason {
			kind := waitKindOf(cause)
			cur.WaitReason, cur.WaitDetail = &kind, &reason
			delete(cur.rest, "waitReason")
			delete(cur.rest, "waitDetail")
			if err := executionPut(q, cur); err != nil {
				return err
			}
			if err := q.db().DispWaitReason(q.ctx(), db.DispWaitReasonParams{ID: id, WaitReason: &kind}); err != nil {
				return err
			}
			if err := recordDecision(q, cur, polling, cs, nil); err != nil {
				return err
			}
		}
		var holder string
		if m := causeHost.FindStringSubmatch(cause); m != nil {
			if hv, ok := k.hosts[m[1]]; ok {
				holder = deref(hv.Holder)
			}
		} else if first != nil && first.Subscription != nil {
			holder = deref(first.Subscription.Holder)
		}
		if _, ok := causes[cause]; !ok {
			causes[cause] = stall{reason, deref(cur.DirectedBy), holder, deref(cur.projectID())}
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
func claimRow(q *Request, id string) (*dispatchExecution, error) {
	if err := q.db().DispExecLock(q.ctx(), id); err != nil {
		return nil, err
	}
	e, err := executionGetAs[dispatchExecution](q, id)
	if err != nil {
		return nil, err
	}
	if !claimable(&e) {
		return nil, nil
	}
	return &e, nil
}

// claimable: queued agent work no host has and no stop asked for.
func claimable(e *dispatchExecution) bool {
	return e.Dispatch != nil && e.HostID == nil && e.State == "preparing" && !isTrue(e.StopRequested)
}

// dispatchSession: the session record fields startCommand reads and writes; other fields stay in rest.
type dispatchSession struct {
	ID                  string                     `json:"id"`
	DeliveryID          string                     `json:"deliveryId,omitempty"`
	TaskID              string                     `json:"taskId,omitempty"`
	PipelineRunID       *string                    `json:"pipelineRunId,omitempty"`
	Account             *string                    `json:"account,omitempty"`
	AgentConfig         json.RawMessage            `json:"agentConfig,omitempty"`
	Setup               *string                    `json:"setup,omitempty"`
	NativeSessionID     *string                    `json:"nativeSessionId,omitempty"`
	ForkNativeSessionID string                     `json:"forkNativeSessionId,omitempty"`
	PendingHandoffID    string                     `json:"pendingHandoffId,omitempty"`
	Role                *string                    `json:"role,omitempty"`
	Access              *string                    `json:"access,omitempty"`
	Branch              *bool                      `json:"branch,omitempty"`
	Starter             string                     `json:"starter,omitempty"`
	WorkspaceSnapshots  map[string]json.RawMessage `json:"workspaceSnapshots,omitempty"`
	row                 db.Session                 `json:"-"`
	rest                recordRest
}

type dispatchSessionPayload struct {
	DeliveryID          string                     `json:"deliveryId,omitempty"`
	Account             *string                    `json:"account,omitempty"`
	AgentConfig         json.RawMessage            `json:"agentConfig,omitempty"`
	Setup               *string                    `json:"setup,omitempty"`
	NativeSessionID     *string                    `json:"nativeSessionId,omitempty"`
	ForkNativeSessionID string                     `json:"forkNativeSessionId,omitempty"`
	PendingHandoffID    string                     `json:"pendingHandoffId,omitempty"`
	Role                *string                    `json:"role,omitempty"`
	Access              *string                    `json:"access,omitempty"`
	Branch              *bool                      `json:"branch,omitempty"`
	Starter             string                     `json:"starter,omitempty"`
	WorkspaceSnapshots  map[string]json.RawMessage `json:"workspaceSnapshots,omitempty"`
}

func (s *dispatchSession) UnmarshalJSON(b []byte) error {
	type plain dispatchSession
	return restDecode(b, (*plain)(s), &s.rest)
}

func (s dispatchSession) MarshalJSON() ([]byte, error) {
	type plain dispatchSession
	return restEncode(plain(s), s.rest)
}

func dispatchSessionByID(q *Request, id string) (dispatchSession, error) {
	var s dispatchSession
	if id == "" {
		return s, errNotFound
	}
	row, err := q.db().SessionByID(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return s, errNotFound
	}
	if err != nil {
		return s, err
	}
	if len(row.Payload) > 0 {
		if err := json.Unmarshal(row.Payload, &s); err != nil {
			return s, err
		}
	}
	s.ID = row.ID
	s.TaskID = deref(row.TaskID)
	s.PipelineRunID = row.PipelineRunID
	s.row = row
	return s, nil
}

func dispatchSessionPut(q *Request, s *dispatchSession) error {
	p := dispatchSessionPayload{
		DeliveryID:          s.DeliveryID,
		Account:             s.Account,
		AgentConfig:         s.AgentConfig,
		Setup:               s.Setup,
		NativeSessionID:     s.NativeSessionID,
		ForkNativeSessionID: s.ForkNativeSessionID,
		PendingHandoffID:    s.PendingHandoffID,
		Role:                s.Role,
		Access:              s.Access,
		Branch:              s.Branch,
		Starter:             s.Starter,
		WorkspaceSnapshots:  s.WorkspaceSnapshots,
	}
	payload, err := restEncode(p, s.rest)
	if err != nil {
		return err
	}
	row := s.row
	row.ID = s.ID
	row.TaskID = strPtr(s.TaskID)
	row.PipelineRunID = s.PipelineRunID
	return sessionPutRow(q, row, payload)
}

// dispatchStartCommand: the typed start command record written by startCommand.
type dispatchStartCommand struct {
	ID          string          `json:"id"`
	ExecutionID string          `json:"executionId"`
	SessionID   string          `json:"sessionId"`
	HostID      string          `json:"hostId"`
	Kind        string          `json:"kind"`
	Status      string          `json:"status"`
	HandedOut   bool            `json:"handedOut"`
	Payload     json.RawMessage `json:"payload"`
}

// startCommand: the start command of a claimed execution, from the session as it is now and the chosen binding.
func (a *App) startCommand(q *Request, e *dispatchExecution, host string, binding *resolvedAccount) (dispatchStartCommand, error) {
	var none dispatchStartCommand
	liveSession, err := dispatchSessionByID(q, e.SessionID)
	if err != nil {
		return none, err
	}
	if err := taskAllowsWork(q, firstString(e.DeliveryID, liveSession.DeliveryID, liveSession.TaskID)); err != nil {
		return none, err
	}
	var d dispatchSpec
	if e.Dispatch != nil {
		d = *e.Dispatch
	}
	dp := d.payload()
	e.HostID = &host
	if liveSession.Account != nil || binding != nil {
		if liveSession.Account != nil {
			e.RequestedAccount = jsonBytes(*liveSession.Account)
		} else {
			e.RequestedAccount = json.RawMessage("null")
		}
		if binding != nil {
			e.EffectiveAccount = jsonBytes(binding)
		} else {
			e.EffectiveAccount = jsonBytes(struct {
				Binding *string `json:"binding"`
				Legacy  bool    `json:"legacy"`
			}{liveSession.Account, true})
		}
	}
	if binding != nil && binding.Taken {
		if e.DirectedBy != nil {
			e.TakenFrom = jsonBytes(*e.DirectedBy)
		} else {
			e.TakenFrom = json.RawMessage("null")
		}
		holder := binding.Holder
		e.DirectedBy = &holder
	}
	var cfg map[string]json.RawMessage
	var prof dispatchAgentProfile
	if jsonIsObject(liveSession.AgentConfig) {
		_ = json.Unmarshal(liveSession.AgentConfig, &cfg)
		_ = lenientDecode(liveSession.AgentConfig, &prof)
	}
	setup := "run"
	if liveSession.Setup != nil {
		setup = *liveSession.Setup
	}
	effort, _ := jsonText(e.Effort)
	// The start payload is contract.StartPayload, field by field: the enqueue extras (inputs, skills, webRoute) are named
	// fields, never a blind merge over the fields above (C7).
	p := &contract.StartPayload{Harness: ptr(harnessOfProfile(liveSession.AgentConfig)), Text: strPtr(d.Input), Setup: strPtr(setup),
		WorkspaceKey: strPtr(d.WorkspaceKey), WorkingDirectory: strPtr(d.WorkingDirectory), Model: strPtr(e.Model), Effort: strPtr(effort),
		Instructions: ptr(defaultInstructions), NativeSessionId: liveSession.NativeSessionID} // "" still reaches the executor's check
	if liveSession.Account != nil || binding != nil {
		if binding != nil {
			p.Account = strPtr(binding.Binding)
		} else {
			p.Account = strPtr(liveSession.Account)
		}
	}
	if pid := deref(d.ProjectID); pid != "" {
		if !jsonGiven(e.ProjectSnapshot) { // queued before project snapshots: freeze the project as it is now
			snapObj, err := projectContext(q, pid)
			if err != nil {
				return none, err
			}
			e.ProjectSnapshot = jsonBytes(snapObj)
		}
	}
	var snapshot *projectSnapshot
	if jsonGiven(e.ProjectSnapshot) {
		var snap projectSnapshot
		if lenientDecode(e.ProjectSnapshot, &snap) == nil {
			snapshot = &snap
		}
	}
	if pid := deref(d.ProjectID); pid != "" {
		p.ProjectId = &pid
		if snapshot != nil && len(snapshot.Settings) > 0 {
			_ = json.Unmarshal(snapshot.Settings, &p.Project)
		}
	}
	var snapSettings json.RawMessage
	if snapshot != nil {
		snapSettings = snapshot.Settings
	}
	if gitNetworkOf(q, struct {
		AgentConfig     json.RawMessage `json:"agentConfig"`
		ProjectID       *string         `json:"projectId"`
		Role            *string         `json:"role"`
		ProjectSettings json.RawMessage `json:"projectSettings"`
	}{liveSession.AgentConfig, d.ProjectID, liveSession.Role, snapSettings}) {
		p.GitNetwork = ptr(true)
	}
	if cfg != nil {
		p.AgentConfig, p.Instructions = cfg, ptr("")
		if prof.FleetOperations { // the profile opts in to every operation its director may call (fleet-pm-agent.md §1)
			if dir := q.peopleSnapshot().person(firstString(deref(e.DirectedBy), ownerEmail(q))); dir != nil {
				p.FleetOperations = rawList(fleetOperations(dir))
			}
		}
	}
	if len(dp.Inputs) > 0 {
		if err := json.Unmarshal(dp.Inputs, &p.Inputs); err != nil {
			return none, failf(400, "execution %s: pipeline inputs are not [{artifactId, path}]: %v", e.ID, err)
		}
	}
	if len(dp.Attachments) > 0 {
		inputs, attachments := startAttachments(e.ID, dp.Attachments)
		p.Inputs, p.Attachments = append(p.Inputs, inputs...), attachments
	}
	p.Skills, p.WebRoute = dp.Skills, strPtr(dp.WebRoute)
	if snapshot != nil {
		instruction := ""
		if p.Instructions != nil {
			instruction = *p.Instructions
		}
		for _, f := range snapshot.Context {
			instruction += "\n\nProject context " + f.Path + ":\n" + f.Text
		}
		p.Instructions = &instruction
		workspaces := liveSession.WorkspaceSnapshots
		if workspaces == nil {
			workspaces = map[string]json.RawMessage{}
		}
		key := host + ":" + deref(d.ProjectID) + ":" + deref(d.WorkspaceKey)
		if ws := workspaces[key]; len(ws) == 0 || string(ws) == "null" {
			workspaces[key] = jsonBytes(struct {
				ExecutionID      string          `json:"executionId"`
				ProjectID        *string         `json:"projectId"`
				WorkspaceKey     *string         `json:"workspaceKey"`
				Project          json.RawMessage `json:"project"`
				Revision         json.RawMessage `json:"revision"`
				ContentSha       json.RawMessage `json:"contentSha"`
				WorkingDirectory *string         `json:"workingDirectory"`
			}{e.ID, d.ProjectID, d.WorkspaceKey, nullRaw(snapshot.Settings), nullRaw(snapshot.Revision), nullRaw(snapshot.ContentSha), d.WorkingDirectory})
			liveSession.WorkspaceSnapshots = workspaces
			if err := dispatchSessionPut(q, &liveSession); err != nil {
				return none, err
			}
		}
		e.WorkspaceSnapshot = workspaces[key]
	}
	// The session's preset (presets.go): read access, no branch and its starter instruction, on every turn.
	if deref(liveSession.Access) == "read" {
		p.ReadOnly = ptr(true)
	}
	if liveSession.Branch != nil && !*liveSession.Branch {
		p.Branch = ptr(false)
	}
	if st := liveSession.Starter; st != "" {
		p.Instructions = ptr(strings.TrimSpace(deref(p.Instructions) + "\n\nThis conversation's preset. " + st))
	}
	// agentDecorateStart completes the payload for the session's dispatch project/checkout and clears any pending handoff.
	sess := agentSession{ID: liveSession.ID, AgentConfig: liveSession.AgentConfig, Account: deref(liveSession.Account),
		ProjectID: deref(d.ProjectID), WorkingDirectory: deref(d.WorkingDirectory), WorkspaceKey: deref(d.WorkspaceKey),
		NativeSessionID: deref(liveSession.NativeSessionID), ForkNativeSessionID: liveSession.ForkNativeSessionID, PendingHandoffID: liveSession.PendingHandoffID}
	ex := agentExecution{ID: e.ID}
	if err := agentDecorateStart(q, &sess, &ex, p); err != nil {
		return none, err
	}
	e.ContinuationKey = ex.ContinuationKey
	if ex.ForkOfNativeSessionID != "" {
		e.ForkOfNativeSessionID = ex.ForkOfNativeSessionID
	}
	if ex.HandoffID != "" {
		e.HandoffID = ex.HandoffID
	}
	if sess.PendingHandoffID == "" {
		liveSession.PendingHandoffID = ""
		delete(liveSession.rest, "pendingHandoffId")
	}
	if granted := jsonStrings(prof.Connections); len(granted) > 0 && e.PipelineRunID == nil {
		minted, err := mintGrantFor(q, mintRun{ExecutionID: &e.ID, DirectedBy: e.DirectedBy, ProjectID: d.ProjectID,
			ExperimentID: e.ExperimentID, PipelineRunID: liveSession.PipelineRunID}, granted)
		if err != nil {
			return none, err
		}
		if minted.Token != nil {
			p.RunToken, p.Connections = strPtr(minted.Token), rawList(minted.Connections)
		}
		if len(minted.Refused) > 0 {
			e.ConnectionsRefused = minted.Refused
		}
	}
	command := dispatchStartCommand{
		ID:          newID(),
		ExecutionID: e.ID,
		SessionID:   liveSession.ID,
		HostID:      host,
		Kind:        string(contract.CommandKindStart),
		Status:      "pending",
		HandedOut:   false,
		Payload:     jsonBytes(p), // the payload as completed above
	}
	if err := executionPut(q, e); err != nil {
		return none, err
	}
	if err := commandPut(q, &agentCommand{ID: command.ID, ExecutionID: command.ExecutionID, SessionID: command.SessionID, HostID: command.HostID, Kind: command.Kind, Status: command.Status, HandedOut: ptr(command.HandedOut), Payload: command.Payload}); err != nil {
		return none, err
	}
	if err := preparePipelineCommand(q, e, command); err != nil {
		return none, err
	}
	if binding != nil && binding.Taken {
		task := liveSession.DeliveryID
		if task == "" {
			t, err := q.db().DispExecTask(q.ctx(), e.ID)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				return none, err
			}
			task = deref(t)
		}
		if task != "" {
			var takenFrom string
			_ = json.Unmarshal(e.TakenFrom, &takenFrom)
			if err := takeTask(q, task, binding.Holder, takenFrom); err != nil {
				return none, err
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
