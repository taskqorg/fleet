package main

// subscriptions and their host bindings (docs/designs/fleet-people-access.md). A subscription is a row of table managed_account (record kind 'managed-account'); logins stay
// on the holder's host, Fleet keeps only what hosts observe.

import (
	"encoding/json"
	"errors"
	"fleet/contract"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"

	"fleet/coordinator/db"
	"regexp"
	"strings"
)

var accountName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)

func init() {
	op("account_list", func(q *Request, _ *contract.AccountListRequest) (contract.AccountListReply, error) {
		return replyAs[contract.AccountListReply](accountList(q))
	})
	op("account_create", func(q *Request, r *contract.AccountCreateRequest) (contract.AccountCreateReply, error) {
		return replyAs[contract.AccountCreateReply](accountCreate(q, r))
	})
	op("account_hosts", func(q *Request, r *contract.AccountHostsRequest) (contract.AccountHostsReply, error) {
		return replyAs[contract.AccountHostsReply](accountHostsOp(q, r))
	})
	op("account_discover", func(q *Request, r *contract.AccountDiscoverRequest) (contract.AccountDiscoverReply, error) {
		return replyAs[contract.AccountDiscoverReply](accountInspect(q, r.AccountId, deref(r.HostId), nil))
	})
	op("account_connect", func(q *Request, r *contract.AccountConnectRequest) (contract.AccountConnectReply, error) {
		return replyAs[contract.AccountConnectReply](accountInspect(q, r.AccountId, deref(r.HostId), &r.Binding))
	})
	op("account_login_handoff", func(q *Request, r *contract.AccountLoginHandoffRequest) (contract.AccountLoginHandoffReply, error) {
		return accountLoginHandoff(q, r)
	})
	op("account_update", func(q *Request, r *contract.AccountUpdateRequest) (contract.AccountUpdateReply, error) {
		return replyAs[contract.AccountUpdateReply](accountUpdate(q, r))
	})
	hostRoute(contract.RouteAccounts, func(q *Request, b *contract.AccountObservation) (any, error) {
		if !q.app.holderMaySee(q.host.Holder, "") {
			return nil, fail(403, "Account observations need an owner-held host")
		}
		return accountReport(q, b, q.hostID())
	})
}

func defaultHostID(q *Request) string { return q.app.hostID }

func defaultAccountBinding() string { return os.Getenv("FLEET_DEFAULT_ACCOUNT_BINDING") }

// account: a managed_account row (record kind managed-account) with the payload fields read here. Writers keep the
// bridge's shape (typed_records.go): the columns, then every other record field in payload under its record name.
type account struct {
	db.ManagedAccount
	accountFields
}

// accountFields: the payload fields of a subscription read in Go; a value of another JSON type reads as absent.
type accountFields struct {
	Bindings   []accountBinding `json:"-"`
	LowPct     json.RawMessage  `json:"lowPct"`
	Plan       json.RawMessage  `json:"plan"`
	PlanWeight json.RawMessage  `json:"planWeight"`
	SwitchPct  json.RawMessage  `json:"switchPct"`
	Take       json.RawMessage  `json:"take"`
}

// accountBinding: one host binding of a subscription (accountReport writes it).
type accountBinding struct {
	Binding    *string         `json:"binding"`
	HostID     *string         `json:"hostId"`
	LastCheck  json.RawMessage `json:"lastCheck"`
	ObservedAt json.RawMessage `json:"observedAt"`
	Platform   *string         `json:"platform"`
	Readiness  *string         `json:"readiness"`
	Roots      json.RawMessage `json:"roots"`
	// J5: the models the host's probe found (nil: not probed) and the models a run proved it rejects (model -> ms).
	Models           []string         `json:"models"`
	ModelsObservedAt *int64           `json:"modelsObservedAt"`
	RejectedModels   map[string]int64 `json:"rejectedModels"`
	raw              json.RawMessage  // the binding as stored
}

// accountOf decodes a row's payload; a binding that is not an object is left out.
func accountOf(r db.ManagedAccount) account {
	a := account{ManagedAccount: r}
	var p struct {
		accountFields
		Bindings json.RawMessage `json:"bindings"`
	}
	_ = json.Unmarshal(r.Payload, &p)
	a.accountFields = p.accountFields
	if bs, ok := jsonAs[[]json.RawMessage](p.Bindings); ok {
		a.Bindings = bindingsIn(bs)
	}
	return a
}

// takeProjects: the projects of the subscription's take rule.
func (a account) takeProjects() []string {
	t, _ := jsonAs[struct {
		Projects []json.RawMessage `json:"projects"`
	}](a.Take)
	out := []string{}
	for _, p := range t.Projects {
		if s, ok := jsonAs[string](p); ok {
			out = append(out, s)
		}
	}
	return out
}

// accountsAll: every subscription, in insertion order.
func accountsAll(q *Request) ([]account, error) {
	rs, err := q.db().AccountRows(q.ctx())
	out := make([]account, 0, len(rs))
	for _, r := range rs {
		out = append(out, accountOf(r))
	}
	return out, err
}

// harnessAllowed: whether host allows harness (a host that is not registered allows every one).
func harnessAllowed(q *Request, host, harness string) (bool, error) {
	ok, err := q.db().AccountHostHarnessAllowed(q.ctx(), db.AccountHostHarnessAllowedParams{HostID: host, Harness: harness})
	if errors.Is(err, pgx.ErrNoRows) {
		return true, nil
	}
	return ok, err
}

// accountJobExecution: the execution record of an inspection's host job, as executionWrite writes it.
type accountJobExecution struct {
	ID          string `json:"id"`
	ExecutionID string `json:"executionId"`
	SessionID   string `json:"sessionId"`
	HostID      string `json:"hostId"`
	State       string `json:"state"`
	CreatedAt   int64  `json:"createdAt"`
	Actor       string `json:"actor"`
	AccountID   string `json:"accountId"`
}

// accountInspectionJob: a program-worker session and host job on host with a pending account-inspect command
// (account_discover, account_connect, subscription_limits_refresh); the execution's id.
func accountInspectionJob(q *Request, host string, p contract.AccountInspectPayload) (string, error) {
	sid, eid := newID(), newID()
	if err := sessionPut(q, &agentSession{ID: sid, CreatedAt: nowMs(), Actor: "program-worker"}); err != nil {
		return "", err
	}
	if err := executionWrite(q, accountJobExecution{ID: eid, ExecutionID: eid, SessionID: sid, HostID: host, State: "preparing", CreatedAt: nowMs(),
		Actor: "program-worker", AccountID: p.AccountId}); err != nil {
		return "", err
	}
	return eid, commandPut(q, &agentCommand{ID: newID(), ExecutionID: eid, SessionID: sid, HostID: host, Kind: string(contract.CommandKindAccountInspect),
		Status: "pending", Payload: jsonBytes(p)})
}

// accountPayload: the payload fields of managed_account stored in payload.
type accountPayload struct {
	Kind       *string           `json:"kind"`
	Plan       *string           `json:"plan"`
	Comment    *string           `json:"comment"`
	Login      *string           `json:"login"`
	Take       json.RawMessage   `json:"take"`
	SwitchPct  json.RawMessage   `json:"switchPct"`
	PlanWeight json.RawMessage   `json:"planWeight,omitempty"`
	LowPct     json.RawMessage   `json:"lowPct,omitempty"`
	CreatedAt  json.RawMessage   `json:"createdAt,omitempty"`
	UpdatedAt  *int64            `json:"updatedAt,omitempty"`
	Bindings   []json.RawMessage `json:"bindings"`
}

// accountRec: a subscription (managed_account columns and payload fields). Bindings stay as stored; bindings() reads them.
type accountRec struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Harness   string  `json:"harness"`
	Holder    string  `json:"holder"`
	Intake    string  `json:"intake"`
	Provider  *string `json:"provider,omitempty"`
	ProjectID *string `json:"projectId,omitempty"`
	accountPayload
}

func accountRecOf(r db.ManagedAccount) (accountRec, error) {
	a := accountRec{
		ID:        r.ID,
		Name:      deref(r.Name),
		Harness:   deref(r.Harness),
		Holder:    deref(r.Holder),
		Intake:    deref(r.Intake),
		Provider:  r.Provider,
		ProjectID: r.ProjectID,
	}
	if err := lenientDecode(r.Payload, &a.accountPayload); err != nil {
		return a, err
	}
	return a, nil
}

func accountGet(q *Request, id string) (accountRec, error) {
	if id == "" {
		return accountRec{}, errNotFound
	}
	r, err := q.db().AccountRowOf(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return accountRec{}, errNotFound
	}
	if err != nil {
		return accountRec{}, err
	}
	return accountRecOf(r)
}

func accountRecs(q *Request) ([]accountRec, error) {
	rs, err := q.db().AccountRows(q.ctx())
	if err != nil {
		return nil, err
	}
	out := make([]accountRec, 0, len(rs))
	for _, r := range rs {
		a, err := accountRecOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, nil
}

func accountPut(q *Request, a accountRec) error {
	if a.ID == "" {
		return fmt.Errorf("managed-account record without an id")
	}
	if err := q.db().AccountPut(q.ctx(), db.AccountPutParams{
		ID:        a.ID,
		Name:      &a.Name,
		Holder:    &a.Holder,
		Intake:    &a.Intake,
		Harness:   &a.Harness,
		Provider:  a.Provider,
		ProjectID: a.ProjectID,
		Payload:   jsonBytes(a.accountPayload),
	}); err != nil {
		return err
	}
	wakeDispatchAfter(q)
	return nil
}

// bindings: the host bindings that are objects, in order.
func (a accountRec) bindings() []accountBinding { return bindingsIn(a.Bindings) }

// binding: the first binding on host (named name when byName); nil when none.
func (a accountRec) binding(host string, name *string, byName bool) *accountBinding {
	for _, b := range a.bindings() {
		if deref(b.HostID) == host && (!byName || sameText(b.Binding, name)) {
			return &b
		}
	}
	return nil
}

func bindingsIn(raw []json.RawMessage) []accountBinding {
	out := []accountBinding{}
	for _, r := range raw {
		if b, ok := jsonAs[accountBinding](r); ok {
			b.raw = r
			out = append(out, b)
		}
	}
	return out
}

// sameText: both absent, or the same text.
func sameText(a, b *string) bool { return a == nil && b == nil || a != nil && b != nil && *a == *b }

// bindingsOf: a subscription record's bindings (dispatch.go reads binding and hostId).
func bindingsOf(a any) []accountBinding {
	rec, _ := asT[struct {
		Bindings []json.RawMessage `json:"bindings"`
	}](a)
	return bindingsIn(rec.Bindings)
}

// takeProjectsOf: the projects of a take rule; an entry that is not text is left out.
func takeProjectsOf(take json.RawMessage) []string {
	t, _ := jsonAs[struct {
		Projects []json.RawMessage `json:"projects"`
	}](take)
	out := []string{}
	for _, p := range t.Projects {
		if s, ok := jsonAs[string](p); ok {
			out = append(out, s)
		}
	}
	return out
}

// hostBeat: a host's observation as accounts read it.
type hostBeat struct {
	ID         string  `json:"id"`
	Holder     *string `json:"holder"`
	LastSeenAt float64 `json:"lastSeenAt"`
	Platform   *string `json:"platform"`
}

// accountHostBeat only trusts an observation of the currently configured holder.
func accountHostBeat(q *Request, id string) (*hostBeat, error) {
	h, err := liveHost(q, id)
	if err != nil || h == nil {
		return nil, err
	}
	r, err := q.db().HostObservationOf(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p struct {
		Platform *string `json:"platform"`
	}
	if err := lenientDecode(r.Payload, &p); err != nil {
		return nil, err
	}
	b := &hostBeat{ID: r.ID, Holder: r.Holder, LastSeenAt: timeMs(r.LastSeenAt), Platform: p.Platform}
	if h.holder() == "" || deref(b.Holder) != h.holder() {
		return nil, nil
	}
	return b, nil
}

func accountBeatOnline(q *Request, b *hostBeat) bool {
	if b == nil {
		return false
	}
	seen := q.app.effectiveHostSeen(b.ID, b.LastSeenAt)
	return seen > 0 && float64(nowMs())-seen < hostStaleMs
}

// accountHost: a host a subscription may bind to (account_hosts).
type accountHost struct {
	ID                  string   `json:"id"`
	Inspector           *string  `json:"inspector"`
	Platform            string   `json:"platform"`
	LastSeenAt          *float64 `json:"lastSeenAt"`
	Connectivity        string   `json:"connectivity"`
	Eligible            bool     `json:"eligible"`
	OnboardingSupported bool     `json:"onboardingSupported"`
	Reason              string   `json:"reason"`
}

// accountHosts: the hosts a subscription of this harness may bind to, with their inspector and readiness.
func accountHosts(q *Request, harness string) ([]accountHost, error) {
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
	accounts, err := accountRecs(q)
	if err != nil {
		return nil, err
	}
	out := []accountHost{}
	for _, id := range ids {
		inspector, err := hostWithRole(q, "inspector-for:"+id)
		if err != nil {
			return nil, err
		}
		allowed, err := harnessAllowed(q, id, harness)
		if err != nil {
			return nil, err
		}
		beat, err := accountHostBeat(q, id)
		if err != nil {
			return nil, err
		}
		native := inList(exec, id)
		// Prefer a fresh eligible native host. A configured inspector remains a fallback,
		// but its heartbeat and provider policy must describe the route actually dispatched.
		if inspector != "" {
			ib, err := accountHostBeat(q, inspector)
			if err != nil {
				return nil, err
			}
			ia, err := harnessAllowed(q, inspector, harness)
			if err != nil {
				return nil, err
			}
			if !(native && allowed && accountBeatOnline(q, beat)) && allowed && ia && (!native || accountBeatOnline(q, ib)) {
				beat = ib
			} else {
				inspector = ""
			}
		}
		look := id
		if inspector != "" {
			look = inspector
		}
		var previous *accountBinding
		for _, a := range accounts {
			if previous == nil {
				previous = a.binding(id, nil, false)
			}
		}
		platform := "unknown"
		if beat != nil && beat.Platform != nil {
			platform = *beat.Platform
		} else if previous != nil && previous.Platform != nil {
			platform = *previous.Platform
		}
		h := accountHost{ID: id, Platform: platform, Connectivity: "unknown", Eligible: allowed}
		if beat != nil {
			eff := q.app.effectiveHostSeen(look, beat.LastSeenAt)
			if eff > 0 {
				h.LastSeenAt = &eff
			} else if beat.LastSeenAt != 0 {
				h.LastSeenAt = &beat.LastSeenAt
			}
			h.Connectivity = "offline"
			if float64(nowMs())-eff < 30000 {
				h.Connectivity = "online"
			}
		}
		h.Reason = "Local Mac inspector; connectivity is not authentication"
		switch {
		case !allowed:
			h.Reason = fmt.Sprintf("%s is not allowed on this host (host setting harnesses.%s.allowed)", harness, harness)
		case native && inspector == "":
			h.Reason = "Native executor host; accounts bound by host observation, login on the host"
		case inspector == "":
			h.Reason = "Remote native executable/account not provisioned for onboarding"
		case platform != "darwin":
			h.Reason = "Await host platform/readiness metadata"
		}
		if inspector != "" {
			h.Inspector = &inspector
		}
		h.OnboardingSupported = native || (inspector != "" && platform == "darwin")
		out = append(out, h)
	}
	return out, nil
}

// accountHostOf: the host id of hosts (the last listed); nil when it is not one of them.
func accountHostOf(hs []accountHost, id string) *accountHost {
	var out *accountHost
	for i := range hs {
		if hs[i].ID == id {
			out = &hs[i]
		}
	}
	return out
}

func accountHostsOp(q *Request, r *contract.AccountHostsRequest) (any, error) {
	harness := "codex"
	if r.Harness != nil {
		harness = *r.Harness
	}
	if r.AccountId != nil {
		a, err := accountGet(q, *r.AccountId)
		if err != nil {
			return nil, err
		}
		harness = a.Harness
	}
	hs, err := accountHosts(q, harness)
	return struct {
		Hosts []accountHost `json:"hosts"`
	}{hs}, err
}

type accountInspectionPayload struct {
	Binding     *string         `json:"binding"`
	CreatedAt   json.RawMessage `json:"createdAt,omitempty"`
	LoginAction *string         `json:"loginAction,omitempty"`
}

// accountInspectionRec: an inspection as stored (account_inspection); accountInspectionView adds its execution's state.
type accountInspectionRec struct {
	ID          string  `json:"id"`
	AccountID   string  `json:"accountId"`
	HostID      string  `json:"hostId"`
	ExecutionID string  `json:"executionId"`
	ProjectID   *string `json:"projectId,omitempty"`
	accountInspectionPayload
}

func accountInspections(q *Request, accountID string) ([]accountInspectionRec, error) {
	rows, err := q.db().AccountInspections(q.ctx(), &accountID)
	if err != nil {
		return nil, err
	}
	out := make([]accountInspectionRec, 0, len(rows))
	for _, r := range rows {
		v := accountInspectionRec{
			ID:          r.ID,
			AccountID:   deref(r.AccountID),
			HostID:      deref(r.HostID),
			ExecutionID: deref(r.ExecutionID),
			ProjectID:   r.ProjectID,
		}
		if err := lenientDecode(r.Payload, &v.accountInspectionPayload); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

type accountInspectionView struct {
	accountInspectionRec
	State *string         `json:"state"`
	Error json.RawMessage `json:"error"`
}

// accountInspectionExec: the fields of an inspection's execution read here.
type accountInspectionExec struct {
	State       *string         `json:"state"`
	Observation string          `json:"observation"`
	Error       json.RawMessage `json:"error"`
}

// Retained inspections predate host_job and may still be tool_run; the general execution reader excludes those.
func accountInspectionExecution(q *Request, inspectionID string) (accountInspectionExec, error) {
	var e accountInspectionExec
	raw, err := q.db().AccountInspectionExecution(q.ctx(), inspectionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, errNotFound
	}
	if err != nil {
		return e, err
	}
	return e, json.Unmarshal(raw, &e)
}

// accountView: a subscription as account_list shows it.
type accountView struct {
	accountRec
	Inspections []accountInspectionView `json:"inspections"`
	Checks      []json.RawMessage       `json:"checks"`
	History     []json.RawMessage       `json:"history"`
}

type accountCataloguePayload struct {
	ObservedAt json.RawMessage   `json:"observedAt,omitempty"`
	Bindings   []json.RawMessage `json:"bindings"`
}

type accountCatalogueRec struct {
	ID        string  `json:"id"`
	HostID    *string `json:"hostId,omitempty"`
	Harness   *string `json:"harness,omitempty"`
	ProjectID *string `json:"projectId,omitempty"`
	accountCataloguePayload
}

func accountList(q *Request) (any, error) {
	catRows, err := q.db().AccountCatalogues(q.ctx(), db.AccountCataloguesParams{})
	if err != nil {
		return nil, err
	}
	cats := make([]json.RawMessage, 0, len(catRows))
	for _, cr := range catRows {
		c := accountCatalogueRec{ID: cr.ID, HostID: cr.HostID, Harness: cr.Harness, ProjectID: cr.ProjectID}
		if err := lenientDecode(cr.Payload, &c.accountCataloguePayload); err != nil {
			return nil, err
		}
		cats = append(cats, jsonBytes(c))
	}
	as, err := accountRecs(q)
	if err != nil {
		return nil, err
	}
	changes, err := q.db().AuditOfKind(q.ctx(), "account")
	if err != nil {
		return nil, err
	}
	out := []accountView{}
	for _, a := range as {
		v := accountView{accountRec: a, Inspections: []accountInspectionView{}, History: []json.RawMessage{}}
		ins, err := accountInspections(q, a.ID)
		if err != nil {
			return nil, err
		}
		for _, i := range ins {
			e, err := accountInspectionExecution(q, i.ID)
			if err != nil {
				return nil, err
			}
			errValue := e.Error
			if len(errValue) == 0 {
				errValue = json.RawMessage("null")
			}
			v.Inspections = append(v.Inspections, accountInspectionView{accountInspectionRec: i, State: e.State, Error: errValue})
		}
		chks, err := accountChecks(q, a.ID, nil, nil)
		if err != nil {
			return nil, err
		}
		v.Checks = make([]json.RawMessage, 0, len(chks))
		for _, chk := range chks {
			v.Checks = append(v.Checks, jsonBytes(chk))
		}
		for _, h := range changes {
			if h.Target == "account:"+a.ID {
				v.History = append(v.History, json.RawMessage(h.Data))
			}
		}
		out = append(out, v)
	}
	return struct {
		Catalogues []json.RawMessage `json:"catalogues"`
		Accounts   []accountView     `json:"accounts"`
	}{cats, out}, nil
}

// loginCommands: each harness's native login, as accounts tell the owner (contracts/vocabulary.json).
var loginCommands = func() map[string]string {
	out := map[string]string{}
	for _, h := range contract.Vocabulary.Harnesses {
		out[h.ID] = h.Login
	}
	return out
}()

func accountCreate(q *Request, r *contract.AccountCreateRequest) (any, error) {
	harness := r.Harness
	if !accountName.MatchString(r.Name) || !inList(contract.HarnessIDs(), harness) {
		return nil, fail(400, "Expected account name and harness")
	}
	same, err := q.db().AccountNameTaken(q.ctx(), &r.Name)
	if err != nil {
		return nil, err
	}
	if same {
		return nil, fail(409, "Account name exists")
	}
	director := q.person.Email
	holder := director
	if r.Holder != nil {
		holder = strings.ToLower(*r.Holder)
	}
	if holder != director && !fullAccess(q.person) {
		return nil, fail(403, "Only the owner creates a subscription for another person")
	}
	kind, plan, comment := "personal", deref(r.Plan), deref(r.Comment)
	login := "Native login on the holder's host, under its protected binding environment: " + loginCommands[harness] + ". Never paste codes or tokens into Fleet. Inspect an existing authorized login first; creation does not authenticate."
	a := accountRec{ID: newID(), Name: r.Name, Harness: harness, Holder: holder, Intake: "all",
		accountPayload: accountPayload{Kind: &kind, Plan: &plan, Comment: &comment, Login: &login,
			Take: json.RawMessage(`{"projects":[]}`), SwitchPct: json.RawMessage("null"), CreatedAt: jsonBytes(nowMs()), Bindings: []json.RawMessage{}}}
	return a, accountPut(q, a)
}

func integrationHost(q *Request) (string, error) {
	return hostWithRole(q, "inspector-for:"+defaultHostID(q))
}

// accountBound: the error when a subscription other than a already has binding name on host.
func accountBound(q *Request, a accountRec, host string, name *string) error {
	others, err := accountRecs(q)
	if err != nil {
		return err
	}
	for _, o := range others {
		if o.ID != a.ID && o.binding(host, name, true) != nil {
			return fail(409, "Host binding already belongs to another managed account")
		}
	}
	return nil
}

// accountInspect: a read-only inspection command to the selected host or its inspector; the native catalogue when
// binding is nil (account_discover), else the status of that binding (account_connect). host "" is the default host.
func accountInspect(q *Request, accountID, host string, wanted *string) (any, error) {
	catalogue := wanted == nil
	a, err := accountGet(q, accountID)
	if err != nil {
		return nil, err
	}
	if host == "" {
		host = defaultHostID(q)
	}
	hs, err := accountHosts(q, a.Harness)
	if err != nil {
		return nil, err
	}
	h := accountHostOf(hs, host)
	switch {
	case h == nil:
		return nil, fail(404, "Unknown host")
	case !h.Eligible:
		return nil, fail(409, h.Reason)
	case h.Connectivity != "online":
		return nil, fail(409, "Host "+h.Connectivity+"; native login/readiness unknown")
	case !h.OnboardingSupported:
		return nil, fail(409, h.Reason)
	}
	integ := host
	if h.Inspector != nil && *h.Inspector != "" {
		integ = *h.Inspector
	}
	var binding *string
	if !catalogue {
		if !accountName.MatchString(*wanted) {
			return nil, fail(400, "Expected host-owned binding identifier")
		}
		binding = wanted
		if err := accountBound(q, a, host, binding); err != nil {
			return nil, err
		}
	}
	olds, err := accountInspections(q, a.ID)
	if err != nil {
		return nil, err
	}
	for _, x := range olds {
		if x.AccountID == a.ID && x.HostID == host && sameText(x.Binding, binding) {
			e, err := accountInspectionExecution(q, x.ID)
			if err != nil {
				return nil, err
			}
			if !executionEnds(deref(e.State)) && e.Observation != "unconfirmed" {
				return x, nil
			}
		}
	}
	mode := "status"
	if catalogue {
		mode = "catalogue"
	}
	id, err := accountInspectionJob(q, integ, contract.AccountInspectPayload{AccountId: a.ID, Harness: a.Harness, Binding: binding, NativeHostId: &host, Mode: &mode})
	if err != nil {
		return nil, err
	}
	action := "Read-only inspection; no authentication changes. Await host result, then explicit login handoff only if needed."
	r := accountInspectionRec{ID: newID(), AccountID: a.ID, HostID: host, ExecutionID: id,
		accountInspectionPayload: accountInspectionPayload{Binding: binding, CreatedAt: jsonBytes(nowMs()), LoginAction: &action}}
	return r, q.db().AccountInspectionPut(q.ctx(), db.AccountInspectionPutParams{
		ID:          r.ID,
		AccountID:   &r.AccountID,
		ExecutionID: &r.ExecutionID,
		HostID:      &r.HostID,
		ProjectID:   r.ProjectID,
		Payload:     jsonBytes(r.accountInspectionPayload),
	})
}

// loginArgv: the login arguments of each harness's binary; nil where the bound agent logs in on its own.
var loginArgv = func() map[string][]string {
	out := map[string][]string{}
	for _, h := range contract.Vocabulary.Harnesses {
		out[h.ID] = h.LoginArgv
	}
	return out
}()

// catalogueBinding: one binding of a host's catalogue (account-catalogue), as accountCatalogue writes it.
type catalogueBinding struct {
	Name             *string         `json:"name"`
	Executable       json.RawMessage `json:"executable"`
	Environment      json.RawMessage `json:"environment"`
	BehaviorApproved json.RawMessage `json:"behaviorApproved,omitempty"`
}

type accountCheckPayload struct {
	Readiness           string          `json:"readiness"`
	Roots               json.RawMessage `json:"roots"`
	Reason              json.RawMessage `json:"reason"`
	NativeLoginObserved *bool           `json:"nativeLoginObserved"`
	Quota               string          `json:"quota"`
	ObservedAt          json.RawMessage `json:"observedAt"`
}

// accountCheckRec: an observation of one binding (account-check), as accountReport writes it.
type accountCheckRec struct {
	ID        string  `json:"id"`
	AccountID string  `json:"accountId"`
	HostID    string  `json:"hostId"`
	Binding   string  `json:"binding"`
	ProjectID *string `json:"projectId,omitempty"`
	accountCheckPayload
}

func accountChecks(q *Request, accountID string, hostID, binding *string) ([]accountCheckRec, error) {
	rows, err := q.db().AccountChecks(q.ctx(), db.AccountChecksParams{AccountID: accountID, HostID: hostID, Binding: binding})
	if err != nil {
		return nil, err
	}
	out := make([]accountCheckRec, 0, len(rows))
	for _, r := range rows {
		c := accountCheckRec{
			ID:        r.ID,
			AccountID: deref(r.AccountID),
			HostID:    deref(r.HostID),
			Binding:   deref(r.Binding),
			ProjectID: r.ProjectID,
		}
		if err := lenientDecode(r.Payload, &c.accountCheckPayload); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// isNull: an absent or JSON null value.
func isNull(v json.RawMessage) bool { return len(v) == 0 || string(v) == "null" }

// orNull: an absent value as JSON null.
func orNull(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return json.RawMessage("null")
	}
	return v
}

func accountLoginHandoff(q *Request, r *contract.AccountLoginHandoffRequest) (contract.AccountLoginHandoffReply, error) {
	var no contract.AccountLoginHandoffReply
	a, err := accountGet(q, r.AccountId)
	if err != nil {
		return no, err
	}
	hs, err := accountHosts(q, a.Harness)
	if err != nil {
		return no, err
	}
	host := accountHostOf(hs, r.HostId)
	if host == nil || !host.Eligible || !host.OnboardingSupported || host.Connectivity != "online" {
		return no, fail(409, "No eligible supported online native login host")
	}
	cats, err := q.db().AccountCatalogues(q.ctx(), db.AccountCataloguesParams{HostID: &host.ID, Harness: &a.Harness})
	if err != nil {
		return no, err
	}
	var binding *catalogueBinding
	if len(cats) > 0 {
		var cp accountCataloguePayload
		if err := lenientDecode(cats[0].Payload, &cp); err != nil {
			return no, err
		}
		for _, x := range cp.Bindings {
			if m, ok := jsonAs[catalogueBinding](x); ok && sameText(m.Name, r.Binding) {
				binding = &m
			}
		}
	}
	if binding == nil {
		return no, fail(409, "Discover a protected host binding first; browser cannot supply roots")
	}
	var existing *accountBinding
	for _, b := range a.bindings() { // the last matching one, as the record lists them
		if deref(b.HostID) == host.ID && sameText(b.Binding, r.Binding) {
			existing = &b
		}
	}
	checks, err := accountChecks(q, a.ID, &host.ID, r.Binding)
	if err != nil {
		return no, err
	}
	var reason, basedOn json.RawMessage
	if len(checks) > 0 {
		latest := checks[len(checks)-1]
		reason, basedOn = latest.Reason, latest.ObservedAt
	}
	if existing != nil {
		if isNull(reason) {
			if lc, ok := jsonAs[struct {
				Reason json.RawMessage `json:"reason"`
			}](existing.LastCheck); ok {
				reason = lc.Reason
			}
		}
		if isNull(basedOn) {
			basedOn = existing.ObservedAt
		}
	}
	reasonText, _ := jsonAs[string](reason)
	var command []string
	var warning *string
	interaction := "Login need is not established: inspect status first; missing executable/config requires host provisioning."
	if reasonText == "login-absent" {
		command = loginArgv[a.Harness]
		interaction = "On the selected Mac, run this executable/argv with exactly the displayed host-owned environment, complete the native flow locally, then Recheck. Never paste credentials/codes into Fleet."
	} else if existing != nil && deref(existing.Readiness) == "ready" {
		interaction = "Existing ready binding: no login needed; recheck metadata only."
	}
	if a.Harness == "antigravity" {
		w := "Native-current login may replace the shared principal; explicit operator review before any login."
		warning = &w
	}
	return contract.AccountLoginHandoffReply{ObservedAt: nowMs(), BasedOnCheckAt: orNull(basedOn), HostId: host.ID, Binding: jsonBytes(r.Binding),
		Environment: orNull(binding.Environment), Executable: orNull(binding.Executable), Command: jsonBytes(command), RequiredUserInteraction: jsonBytes(interaction),
		Warning: jsonBytes(warning), Quota: "unknown", AuthChanged: false}, nil
}

// accountUpdate: the request as typed; whether switchPct, planWeight and lowPct were sent (null clears them) is read
// from the body as sent.
func accountUpdate(q *Request, r *contract.AccountUpdateRequest) (any, error) {
	var sent struct {
		SwitchPct  json.RawMessage `json:"switchPct"`
		PlanWeight json.RawMessage `json:"planWeight"`
		LowPct     json.RawMessage `json:"lowPct"`
	}
	_ = json.Unmarshal(q.body, &sent)
	a, err := accountGet(q, r.AccountId)
	if err != nil {
		return nil, err
	}
	// Stricter than the declared holder:account rule, which an architect passes: only the holder or the owner changes it.
	p := q.person
	if p != nil && !fullAccess(p) && p.Email != a.Holder {
		return nil, fail(403, "Only the holder or the owner changes this subscription")
	}
	// Each change: its field, the stored and the new value, and how the record takes it.
	type change struct {
		field      string
		prev, next any
		apply      func(*accountRec)
	}
	changes := []change{}
	if r.Intake != nil {
		in := string(*r.Intake)
		if in != "all" && in != "paused" {
			return nil, fail(400, "intake: all or paused")
		}
		changes = append(changes, change{"intake", a.Intake, in, func(x *accountRec) { x.Intake = in }})
	}
	for _, f := range []struct {
		name string
		v    *string
		at   func(*accountRec) **string
	}{{"plan", r.Plan, func(x *accountRec) **string { return &x.Plan }}, {"comment", r.Comment, func(x *accountRec) **string { return &x.Comment }}} {
		if f.v == nil {
			continue
		}
		if len(*f.v) > 2000 {
			return nil, fail(400, f.name+": text")
		}
		v, at := *f.v, f.at
		changes = append(changes, change{f.name, *at(&a), v, func(x *accountRec) { *at(x) = &v }})
	}
	// fleet-people-access.md section 8: planWeight (default 1) weights the pool; lowPct (default off) marks Low.
	// planWeight is bounded so the weighted pool sums stay finite numbers; no plan is a trillion base plans.
	for _, f := range []struct {
		name, msg string
		sent      json.RawMessage
		v         *float64
		max       float64
		at        func(*accountRec) *json.RawMessage
	}{
		{"switchPct", "switchPct: 1-100 or null", sent.SwitchPct, r.SwitchPct, 100, func(x *accountRec) *json.RawMessage { return &x.SwitchPct }},
		{"planWeight", "planWeight: a positive number up to 1e12, or null (1)", sent.PlanWeight, r.PlanWeight, 1e12, func(x *accountRec) *json.RawMessage { return &x.PlanWeight }},
		{"lowPct", "lowPct: 1-100 or null (off)", sent.LowPct, r.LowPct, 100, func(x *accountRec) *json.RawMessage { return &x.LowPct }},
	} {
		if len(f.sent) == 0 {
			continue
		}
		if f.v != nil && !(*f.v > 0 && *f.v <= f.max) {
			return nil, fail(400, f.msg)
		}
		v, at := json.RawMessage(jsonBytes(f.v)), f.at
		changes = append(changes, change{f.name, *at(&a), v, func(x *accountRec) { *at(x) = v }})
	}
	if !isNull(r.Take) {
		t, ok := jsonAs[struct {
			Projects *[]json.RawMessage `json:"projects"`
		}](r.Take)
		if !ok || t.Projects == nil {
			return nil, fail(400, "take: {projects: [ids]}")
		}
		holder := q.peopleSnapshot().person(a.Holder)
		uniq := []string{}
		for _, x := range *t.Projects {
			s, isStr := jsonAs[string](x)
			if !isStr {
				return nil, fail(400, "take: {projects: [ids]}")
			}
			if holder == nil || !holder.mayAccess(s) {
				return nil, fail(400, fmt.Sprintf("%s has no grant on %s; a take needs one", a.Holder, s))
			}
			if !inList(uniq, s) {
				uniq = append(uniq, s)
			}
		}
		take := json.RawMessage(jsonBytes(struct {
			Projects []string `json:"projects"`
		}{uniq}))
		changes = append(changes, change{"take", a.Take, take, func(x *accountRec) { x.Take = take }})
	}
	newHolder := ""
	if r.Holder != nil {
		if p != nil && !fullAccess(p) {
			return nil, fail(403, "Only the owner changes the holder")
		}
		newHolder = strings.ToLower(*r.Holder)
		if q.peopleSnapshot().person(newHolder) == nil {
			return nil, fail(400, "Holder is not a person")
		}
		h := newHolder
		changes = append(changes, change{"holder", a.Holder, h, func(x *accountRec) { x.Holder = h }})
	}
	auditHolder := a.Holder
	if newHolder != "" {
		auditHolder = newHolder
	}
	changed := []string{}
	for _, c := range changes {
		if js(c.prev) == js(c.next) {
			continue
		}
		changed = append(changed, c.field)
		if err := audit(q, "account:"+a.ID, c.field, c.prev, c.next, auditExtra{Holder: auditHolder}); err != nil {
			return nil, err
		}
	}
	if len(changed) > 0 {
		if err := notifyHolder(q, a.Holder, fmt.Sprintf("The owner changed %s of your subscription %s.", strings.Join(changed, ", "), a.Name)); err != nil {
			return nil, err
		}
	}
	if newHolder != "" && newHolder != a.Holder {
		if err := notifyHolder(q, newHolder, fmt.Sprintf("The owner made you the holder of subscription %s.", a.Name)); err != nil {
			return nil, err
		}
	}
	for _, c := range changes {
		c.apply(&a)
	}
	now := nowMs()
	a.UpdatedAt = &now
	return a, accountPut(q, a)
}

var checkReasons = []string{"ready", "login-absent", "executable-missing", "binding-unprovisioned", "binding-absent", "status-unknown", "catalogue-only", "configuration-error"}

// accountLastCheck: a binding's last check as accountReport writes it.
type accountLastCheck struct {
	Readiness           string `json:"readiness"`
	Reason              string `json:"reason"`
	NativeLoginObserved *bool  `json:"nativeLoginObserved"`
	Quota               string `json:"quota"`
	ObservedAt          int64  `json:"observedAt"`
}

// accountBindingOut: a binding as accountReport writes it.
type accountBindingOut struct {
	HostID         string            `json:"hostId"`
	Binding        string            `json:"binding"`
	Platform       contract.Platform `json:"platform"`
	Readiness      string            `json:"readiness"`
	ObservedAt     int64             `json:"observedAt"`
	Roots          json.RawMessage   `json:"roots"`
	LastCheck      accountLastCheck  `json:"lastCheck"`
	ReadinessBasis string            `json:"readinessBasis"`
	Authority      string            `json:"authority"`
	// J5 (docs/designs/fleet-ia-and-journeys.md section 3): models probed at inspection; rejectedModels are models a run on
	// this binding failed with as not found. A fresh probe clears what runs learned.
	Models           []string         `json:"models,omitempty"`
	ModelsObservedAt *int64           `json:"modelsObservedAt,omitempty"`
	RejectedModels   map[string]int64 `json:"rejectedModels,omitempty"`
}

// accountReport: a host's observation of one binding of a subscription.
func accountReport(q *Request, b *contract.AccountObservation, host string) (any, error) {
	a, err := accountGet(q, b.AccountId)
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
	if ok, err := harnessAllowed(q, host, a.Harness); err != nil || !ok {
		if err == nil {
			err = fail(409, fmt.Sprintf("%s is not allowed on host %s", a.Harness, host))
		}
		return nil, err
	}
	if err := accountBound(q, a, host, &b.Binding); err != nil {
		return nil, err
	}
	var previous *accountBinding
	for _, x := range a.bindings() {
		if deref(x.HostID) == host && deref(x.Binding) == b.Binding {
			previous = &x
		}
	}
	observedAt := nowMs()
	reason := "legacy-observation"
	if inList(checkReasons, b.Reason) {
		reason = b.Reason
	}
	var roots json.RawMessage
	if r := b.Roots; r != nil {
		kept := *r
		for _, s := range []**string{&kept.Home, &kept.NativeHome, &kept.CredentialHome, &kept.CredentialAuthority, &kept.Executable} {
			if *s != nil && len(**s) >= 2000 {
				*s = nil
			}
		}
		roots = jsonBytes(kept)
	} else if previous != nil {
		roots = previous.Roots
	}
	roots = orNull(roots)
	retained := reason == "catalogue-only" && previous != nil && deref(previous.Readiness) == "ready"
	readiness, basis := b.Readiness, "Latest host status and behavior approval"
	if retained {
		readiness, basis = "ready", "Prior qualified ready binding retained; catalogue is not new authentication proof"
	}
	last := accountLastCheck{Readiness: b.Readiness, Reason: reason, NativeLoginObserved: b.NativeLoginObserved, Quota: "unknown", ObservedAt: observedAt}
	binding := accountBindingOut{HostID: host, Binding: b.Binding, Platform: b.Platform, Readiness: readiness, ObservedAt: observedAt, Roots: roots,
		LastCheck: last, ReadinessBasis: basis, Authority: "host-owned native registry; no credentials transferred"}
	switch {
	case b.Models != nil:
		// A malformed name is dropped, never the observation: readiness still counts.
		kept := []string{}
		for _, m := range b.Models {
			if m != "" && len(m) <= 200 && len(kept) < 200 {
				kept = append(kept, m)
			}
		}
		binding.Models, binding.ModelsObservedAt = kept, &observedAt
	case previous != nil:
		binding.Models, binding.ModelsObservedAt, binding.RejectedModels = previous.Models, previous.ModelsObservedAt, previous.RejectedModels
	}
	check := accountCheckRec{ID: newID(), AccountID: a.ID, HostID: host, Binding: b.Binding,
		accountCheckPayload: accountCheckPayload{Readiness: b.Readiness, Roots: roots, Reason: jsonBytes(reason), NativeLoginObserved: b.NativeLoginObserved,
			Quota: "unknown", ObservedAt: jsonBytes(observedAt)}}
	if err := q.db().AccountCheckPut(q.ctx(), db.AccountCheckPutParams{
		ID:        check.ID,
		AccountID: &check.AccountID,
		HostID:    &check.HostID,
		Binding:   &check.Binding,
		ProjectID: check.ProjectID,
		Payload:   jsonBytes(check.accountCheckPayload),
	}); err != nil {
		return nil, err
	}
	current := a.binding(host, nil, false)
	if current == nil || deref(current.Binding) == b.Binding || b.Readiness == "ready" {
		kept := []json.RawMessage{}
		for _, x := range a.bindings() {
			if deref(x.HostID) != host {
				kept = append(kept, x.raw)
			}
		}
		a.Bindings = append(kept, jsonBytes(binding))
	}
	if a.Bindings == nil {
		a.Bindings = []json.RawMessage{}
	}
	return a, accountPut(q, a)
}

// accountCatalogue: the safe binding metadata the default host's inspector reports.
func accountCatalogue(q *Request, b *contract.AccountCatalogue, host string) (any, error) {
	a, err := accountGet(q, b.AccountId)
	if err != nil {
		return nil, err
	}
	if host != defaultHostID(q) || len(b.Bindings) > 100 {
		return nil, fail(400, "Invalid host catalogue")
	}
	out := []catalogueBinding{}
	for _, m := range b.Bindings {
		env := map[string]string{}
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
		name := m.Name
		out = append(out, catalogueBinding{Name: &name, Executable: jsonBytes(m.Executable), Environment: jsonBytes(env), BehaviorApproved: jsonBytes(m.BehaviorApproved)})
	}
	err = q.db().AccountCataloguePut(q.ctx(), db.AccountCataloguePutParams{
		ID:      "catalogue:" + host + ":" + a.Harness,
		HostID:  &host,
		Harness: &a.Harness,
		Payload: jsonBytes(struct {
			ObservedAt int64              `json:"observedAt"`
			Bindings   []catalogueBinding `json:"bindings"`
		}{nowMs(), out}),
	})
	return struct {
		OK bool `json:"ok"`
	}{true}, err
}

// resolvedAccount: the binding resolveAccount chose (dispatch.go reads it as startBinding).
type resolvedAccount struct {
	AccountID   string          `json:"accountId"`
	AccountName string          `json:"accountName"`
	Binding     *string         `json:"binding"`
	HostID      string          `json:"hostId"`
	ObservedAt  json.RawMessage `json:"observedAt"`
	Holder      string          `json:"holder"`
	Taken       bool            `json:"taken"`
}

// resolveAccount: the ready binding of a subscription on a host for this director; nil when none applies. director and
// projectID are text or absent. dispatch.go keeps the reply on its candidate.
func resolveAccount(q *Request, selection *string, harness, host string, director, projectID any) (*resolvedAccount, error) {
	requested := ""
	if selection != nil {
		requested = *selection
	} else if harness == "codex" {
		requested = defaultAccountBinding()
	}
	if requested == "" {
		return nil, nil
	}
	as, err := accountRecs(q)
	if err != nil {
		return nil, err
	}
	var a *accountRec
	for i, x := range as {
		if x.ID == requested || x.Name == requested || x.binding(host, &requested, true) != nil {
			a = &as[i]
			break
		}
	}
	if a == nil {
		return nil, nil
	}
	if a.Harness != harness {
		return nil, fail(409, "Selected account harness mismatch")
	}
	dir, _ := director.(string)
	project, _ := projectID.(string)
	// A take rule covers its projects; a director with full access (the owner, or a grant on *, as site-agents has
	// in development) may take any subscription. Either way the execution records takenFrom, the real director.
	takes := inList(takeProjectsOf(a.Take), project) && project != "" || q.peopleSnapshot().person(dir) != nil && fullAccess(q.peopleSnapshot().person(dir))
	if selection == nil && dir != "" && a.Holder != dir && !takes {
		return nil, nil
	}
	if a.Intake == "paused" {
		return nil, fail(409, fmt.Sprintf("Subscription %s is Paused", a.Name))
	}
	taken := dir != "" && a.Holder != dir
	if taken && !takes {
		return nil, fail(409, fmt.Sprintf("Subscription %s is held by %s; work directed by %s needs a take", a.Name, a.Holder, dir))
	}
	if ok, err := harnessAllowed(q, host, harness); err != nil || !ok {
		if err == nil {
			err = fail(409, fmt.Sprintf("%s is not allowed on host %s", harness, host))
		}
		return nil, err
	}
	b := a.binding(host, nil, false)
	if b == nil || deref(b.Readiness) != "ready" {
		return nil, fail(409, "Selected account has no observed ready binding on this host")
	}
	return &resolvedAccount{AccountID: a.ID, AccountName: a.Name, Binding: b.Binding, HostID: host, ObservedAt: orNull(b.ObservedAt), Holder: a.Holder, Taken: taken}, nil
}

func (p *Person) mayAccess(projectID string) bool {
	return mayAccessProjectID(p, projectID)
}
