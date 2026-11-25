package main

// hosts and the host operations (docs/designs/fleet-people-access.md). A host is a row of table host (record kind
// 'host', id 'host:<id>', typed_records.go) {hostId, name, holder, platform, roles, secretHash, intake, harnesses,
// createdAt}; only the secret's hash is kept. Its state moves through hostTransition (host_states.go).

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"math"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/harnessrelease"
)

const hostStaleMs = 30000

var (
	hostID    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,79}$`)
	hexHash   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	hostRoles = regexp.MustCompile(`^(publication|inspector-for:[a-z][a-z0-9-]{0,79})$`)
)

func init() {
	op("hosts_register", hostsRegister)
	op("hosts_update", hostsUpdate)
	op("hosts_rotate_secret", hostsRotateSecret)
	op("hosts_list", func(q *Request, r *contract.HostsListRequest) (contract.HostsListReply, error) {
		return hostsListReply(q, r.IncludeRemoved != nil && *r.IncludeRemoved)
	})
	op("route_decisions", routeDecisions)
}

// hostPayload: the fields of a host record kept in payload (typed_records.go), under their record names.
type hostPayload struct {
	Name          *string         `json:"name,omitempty"`
	Platform      *string         `json:"platform,omitempty"`
	Roles         []string        `json:"roles"`
	Harnesses     json.RawMessage `json:"harnesses,omitempty"`
	CreatedAt     *float64        `json:"createdAt,omitempty"`
	AutoUpdate    json.RawMessage `json:"autoUpdate,omitempty"`
	MaxConcurrent *int64          `json:"maxConcurrent,omitempty"`
	RemovedAt     *float64        `json:"removedAt,omitempty"`
}

// hostRecord: a row of table host with its bare id. As JSON it is the host record as the record API shows it, the id
// bare (hostList).
type hostRecord struct {
	ID                string  `json:"id"`
	State             string  `json:"-"`
	Holder            *string `json:"holder,omitempty"`
	Intake            *string `json:"intake,omitempty"`
	SecretHash        *string `json:"secretHash,omitempty"`
	RemovedSecretHash *string `json:"removedSecretHash,omitempty"`
	ProjectID         *string `json:"projectId,omitempty"`
	hostPayload
}

func hostRecordOf(r db.HostRecordsRow) (hostRecord, error) {
	h := hostRecord{ID: r.HostID, State: r.State, Holder: r.Holder, Intake: r.Intake, SecretHash: r.SecretHash, RemovedSecretHash: r.RemovedSecretHash, ProjectID: r.ProjectID}
	if err := json.Unmarshal(r.Payload, &h.hostPayload); err != nil {
		return h, fmt.Errorf("host %s payload: %w", r.HostID, err)
	}
	return h, nil
}

func (h hostRecord) removed() bool  { return h.State == "removed" }
func (h hostRecord) holder() string { return deref(h.Holder) }
func (h hostRecord) executes() bool { return len(h.Roles) == 0 } // runs agent work: no program-worker role
func (h hostRecord) autoUpdate() string {
	var s string
	if json.Unmarshal(h.AutoUpdate, &s) == nil && s != "" {
		return s
	}
	return "on"
}

// hostView: the host as the host operations answer it, without its secret's hash.
func (h hostRecord) view() contract.HostRecord {
	return contract.HostRecord{AutoUpdate: h.AutoUpdate, CreatedAt: valueOr(h.CreatedAt), Harnesses: h.Harnesses, Holder: h.Holder, Id: h.ID,
		Intake: deref(h.Intake), MaxConcurrent: h.MaxConcurrent, Name: h.Name, Platform: h.Platform, Roles: h.Roles}
}

// valueOr: *p, or the zero value when p is nil.
func valueOr[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}

// hostRecords: every host record, removed ones (tombstones, host_remove.go) too, in record order.
func hostRecords(q *Request) ([]hostRecord, error) {
	rows, err := q.db().HostRecords(q.ctx())
	if err != nil {
		return nil, err
	}
	out := make([]hostRecord, 0, len(rows))
	for _, r := range rows {
		h, err := hostRecordOf(r)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

// hostsLive: the hosts that are not removed.
func hostsLive(q *Request) ([]hostRecord, error) {
	all, err := hostRecords(q)
	out := []hostRecord{}
	for _, h := range all {
		if !h.removed() {
			out = append(out, h)
		}
	}
	return out, err
}

// hostRecordByID: the host record of bare id, removed or not; nil when there is none.
func hostRecordByID(q *Request, id string) (*hostRecord, error) {
	r, err := q.db().HostRecordOf(q.ctx(), "host:"+id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	h, err := hostRecordOf(db.HostRecordsRow(r))
	return &h, err
}

// liveHost: the host of bare id unless it is removed; nil when there is none.
func liveHost(q *Request, id string) (*hostRecord, error) {
	h, err := hostRecordByID(q, id)
	if err != nil || h == nil || h.removed() {
		return nil, err
	}
	return h, nil
}

func hostIDs(q *Request) ([]string, error) {
	hs, err := hostsLive(q)
	out := []string{}
	for _, h := range hs {
		out = append(out, h.ID)
	}
	return out, err
}

// executionHostIDs: hosts that run agent work (no program-worker role).
func executionHostIDs(q *Request) ([]string, error) {
	hs, err := hostsLive(q)
	out := []string{}
	for _, h := range hs {
		if h.executes() {
			out = append(out, h.ID)
		}
	}
	return out, err
}

func hostWithRole(q *Request, role string) (string, error) {
	hs, err := hostsLive(q)
	for _, h := range hs {
		if slices.Contains(h.Roles, role) {
			return h.ID, err
		}
	}
	return "", err
}

// holderMaySee: a host held by the owner sees every project; another holder's host only the projects of the holder's grants.
func (a *App) holderMaySee(holder, projectID string) bool {
	p, err := a.resolveAccess(context.Background(), holder)
	if err != nil || p == nil {
		return false
	}
	return mayAccessProjectID(p, projectID)
}

// holderMayRun: projectless work may use its director's own host, never another ordinary holder's host.
func (a *App) holderMayRun(holder, projectID, director string) bool {
	if a.holderMaySee(holder, projectID) {
		return true
	}
	if projectID != "" || director == "" || holder != director {
		return false
	}
	p, err := a.resolveAccess(context.Background(), director)
	return err == nil && p != nil
}

func mintSecret() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// holderNoticeData: the data of the question that tells a holder someone changed a setting of their row.
type holderNoticeData struct {
	Notice string `json:"notice"`
}

// notifyHolder: someone with full access changed a setting of a row another person holds; tell the holder with a
// question addressed to them. what is the change without its subject ("changed autoUpdate of your host x."): the
// notice names who made it (an agent person such as Site agents too), never "the owner" by default.
func notifyHolder(q *Request, holder, what string) error {
	if q.person == nil || !fullAccess(q.person) || q.person.Email == holder {
		return nil
	}
	by := q.person.Email
	text := personName(q.person) + " " + what
	if open, err := q.db().HolderNoticeOpen(q.ctx(), db.HolderNoticeOpenParams{Holder: holder, Text: text}); err != nil || open {
		return err // 2026-11-26: seven identical notices for seven hold changes of one host
	}
	id, err := askQuestionAs(q, questionAsk{Kind: "operator_decision", Text: text, Options: jsonBytes([]string{"ok"}), AskedBy: &by,
		Data: jsonBytes(holderNoticeData{Notice: "owner-override"})})
	if err != nil {
		return err
	}
	return q.db().AddressQuestion(q.ctx(), db.AddressQuestionParams{Holder: holder, ID: id})
}

// harnessPermissions: a host's harnesses setting, {<name>: {allowed}}; ok false when raw is not one.
func harnessPermissions(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return nil, false
	}
	for _, x := range m {
		var h struct {
			Allowed *bool `json:"allowed"`
		}
		if json.Unmarshal(x, &h) != nil || h.Allowed == nil {
			return nil, false
		}
	}
	return m, true
}

// given: a request field that is present and not null.
func given(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }

// sameJSON: a and b are the same JSON value (nil is null).
func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	json.Unmarshal(a, &x)
	json.Unmarshal(b, &y)
	return js(x) == js(y)
}

// secretOf: the hash to store, and the minted secret when the host did not bring its own hash.
func secretOf(q *Request, own *string, self string) (hash, secret string, err error) {
	if own != nil {
		if !hexHash.MatchString(*own) {
			return "", "", fail(400, "secretSha256 must be 64 hex characters")
		}
		hash = *own
	} else {
		secret = mintSecret()
		hash = secretHash(secret)
	}
	if hash == secretHash(q.app.browserSecret) {
		return "", "", fail(400, "A host secret must differ from the browser secret")
	}
	hs, err := hostsLive(q)
	for _, h := range hs {
		if h.ID != self && deref(h.SecretHash) == hash {
			return "", "", fail(409, "Secret already used by another host")
		}
	}
	return hash, secret, err
}

// wakeDispatchAfter: a host write changes what dispatch may place (as put of a host record does).
func wakeDispatchAfter(q *Request) {
	if q.app != nil {
		afterCommit(q, func() { q.app.wakeDispatch() })
	}
}

func hostsRegister(q *Request, b *contract.HostsRegisterRequest) (contract.HostWriteReply, error) {
	var out contract.HostWriteReply
	id := b.Id
	if !hostID.MatchString(id) {
		return out, fail(400, "Expected host id")
	}
	if h, err := liveHost(q, id); err != nil || h != nil {
		if err == nil {
			err = fail(409, "Host exists")
		}
		return out, err
	}
	holder := q.person.Email
	if b.Holder != nil {
		holder = strings.ToLower(*b.Holder)
	}
	if q.peopleSnapshot().person(holder) == nil {
		return out, fail(400, "Holder is not a person")
	}
	if b.SecretSha256 != nil && !hexHash.MatchString(*b.SecretSha256) {
		return out, fail(400, "secretSha256 must be 64 hex characters")
	}
	roles := []string{}
	for _, r := range b.Roles {
		if !hostRoles.MatchString(r) {
			return out, fail(400, "roles: publication or inspector-for:<hostId>")
		}
		roles = append(roles, r)
	}
	harnesses := json.RawMessage("{}")
	if given(b.Harnesses) {
		if _, ok := harnessPermissions(b.Harnesses); !ok {
			return out, fail(400, "harnesses: {<name>: {allowed}}")
		}
		harnesses = b.Harnesses
	}
	hash, secret, err := secretOf(q, b.SecretSha256, "")
	if err != nil {
		return out, err
	}
	name, platform := id, "unknown"
	if b.Name != nil {
		name = *b.Name
	}
	if b.Platform != nil {
		platform = *b.Platform
	}
	if b.MaxConcurrent != nil && *b.MaxConcurrent < 1 {
		return out, fail(400, "maxConcurrent: positive integer or null")
	}
	created := float64(nowMs())
	h := hostRecord{ID: id, Holder: &holder, Intake: ptr("all"), SecretHash: &hash,
		hostPayload: hostPayload{Name: &name, Platform: &platform, Roles: roles, Harnesses: harnesses, MaxConcurrent: b.MaxConcurrent, CreatedAt: &created}}
	if err := q.db().HostRegister(q.ctx(), db.HostRegisterParams{ID: "host:" + id, HostID: id, Holder: h.Holder, Intake: h.Intake, SecretHash: h.SecretHash,
		Payload: jsonBytes(h.hostPayload)}); err != nil {
		return out, err
	}
	// The id of a removed host is registered again (host_states.go); a new row is already active.
	if err := hostTransition(q, "host:"+id, hostStateActive); err != nil {
		return out, err
	}
	wakeDispatchAfter(q)
	out.Host = h.view()
	if secret != "" {
		out.Secret = &secret
	}
	return out, nil
}

func hostsUpdate(q *Request, b *contract.HostsUpdateRequest) (contract.HostsUpdateReply, error) {
	var out contract.HostsUpdateReply
	h, err := liveHost(q, b.Id)
	if err != nil {
		return out, err
	}
	if h == nil {
		return out, fail(404, "Unknown host")
	}
	if q.person == nil || (!fullAccess(q.person) && q.person.Email != h.holder()) {
		return out, fail(403, "Only the holder or the owner changes this host")
	}
	type setting struct {
		field          string
		previous, next json.RawMessage
	}
	var next []setting
	if b.Intake != nil {
		if *b.Intake != "all" && *b.Intake != "spare" && *b.Intake != "paused" {
			return out, fail(400, "intake: all, spare or paused")
		}
		var prev json.RawMessage
		if h.Intake != nil {
			prev = jsonBytes(*h.Intake)
		}
		next = append(next, setting{"intake", prev, jsonBytes(*b.Intake)})
	}
	if given(b.AutoUpdate) {
		var v any
		json.Unmarshal(b.AutoUpdate, &v)
		if !validAutoUpdate(v) {
			return out, fail(400, "autoUpdate: on, off or an RFC 3339 time ending in Z")
		}
		next = append(next, setting{"autoUpdate", h.AutoUpdate, b.AutoUpdate})
	}
	if given(b.Harnesses) {
		add, ok := harnessPermissions(b.Harnesses)
		if !ok {
			return out, fail(400, "harnesses: {<name>: {allowed}}")
		}
		m := map[string]json.RawMessage{}
		json.Unmarshal(h.Harnesses, &m) // not an object: the given ones alone
		maps.Copy(m, add)
		next = append(next, setting{"harnesses", h.Harnesses, jsonBytes(m)})
	}
	if len(b.MaxConcurrent) > 0 {
		var prev json.RawMessage
		if h.MaxConcurrent != nil {
			prev = jsonBytes(*h.MaxConcurrent)
		}
		if string(b.MaxConcurrent) == "null" {
			next = append(next, setting{"maxConcurrent", prev, json.RawMessage("null")})
		} else {
			var v float64
			if json.Unmarshal(b.MaxConcurrent, &v) != nil || v < 1 || v != float64(int64(v)) {
				return out, fail(400, "maxConcurrent: positive integer or null")
			}
			next = append(next, setting{"maxConcurrent", prev, jsonBytes(int64(v))})
		}
	}
	changed := []string{}
	patch := map[string]json.RawMessage{}
	var intake *string
	for _, f := range next {
		if f.field == "intake" {
			intake = b.Intake
		} else {
			patch[f.field] = f.next
		}
		if sameJSON(f.previous, f.next) {
			continue
		}
		changed = append(changed, f.field)
		if err := audit(q, "host:"+h.ID, f.field, f.previous, f.next, auditExtra{Holder: deref(h.Holder)}); err != nil {
			return out, err
		}
	}
	if len(changed) > 0 {
		if err := notifyHolder(q, h.holder(), fmt.Sprintf("changed %s of your host %s.", strings.Join(changed, ", "), h.ID)); err != nil {
			return out, err
		}
	}
	if err := q.db().HostPatch(q.ctx(), db.HostPatchParams{ID: "host:" + h.ID, Intake: intake, Patch: jsonBytes(patch)}); err != nil {
		return out, err
	}
	wakeDispatchAfter(q)
	if intake != nil {
		h.Intake = intake
	}
	if v, ok := patch["autoUpdate"]; ok {
		h.AutoUpdate = v
	}
	if v, ok := patch["harnesses"]; ok {
		h.Harnesses = v
	}
	if v, ok := patch["maxConcurrent"]; ok {
		if isNull(v) {
			h.MaxConcurrent = nil
		} else {
			var n int64
			if json.Unmarshal(v, &n) == nil {
				h.MaxConcurrent = &n
			}
		}
	}
	out.Host = h.view()
	return out, nil
}

func hostsRotateSecret(q *Request, b *contract.HostsRotateSecretRequest) (contract.HostWriteReply, error) {
	var out contract.HostWriteReply
	h, err := liveHost(q, b.Id)
	if err != nil {
		return out, err
	}
	if h == nil {
		return out, fail(404, "Unknown host")
	}
	hash, secret, err := secretOf(q, b.SecretSha256, h.ID)
	if err != nil {
		return out, err
	}
	if err := q.db().HostSetSecret(q.ctx(), db.HostSetSecretParams{ID: "host:" + h.ID, SecretHash: &hash}); err != nil {
		return out, err
	}
	wakeDispatchAfter(q)
	out.Host = h.view()
	if secret != "" {
		out.Secret = &secret
	}
	return out, nil
}

// The coordinator's pins are the target. A host on an older release may still report older observed versions.
var hostHarnessTargets = sync.OnceValue(func() map[string]map[string]harnessrelease.Asset {
	dir := releaseDir()
	out := map[string]map[string]harnessrelease.Asset{}
	out["linux"], _ = harnessrelease.Read(dir, "linux-amd64")
	out["win32"] = out["linux"]
	out["darwin"], _ = harnessrelease.Read(dir, "darwin-arm64")
	return out
})

// hostObservationPayload: the parts of a host observation (host_poll.go) the host list shows.
type hostObservationPayload struct {
	Platform        *string                       `json:"platform"`
	Version         *string                       `json:"version"`
	Capabilities    []string                      `json:"capabilities"`
	HarnessVersions []contract.HostHarnessVersion `json:"harnessVersions"`
	Problems        []contract.HostProblem        `json:"problems"`
	PollError       *contract.HostPollError       `json:"pollError"`
	Stores          json.RawMessage               `json:"stores"`
	Update          json.RawMessage               `json:"update"`
	Load            *hostLoadObservation          `json:"load"`
	Pacing          *contract.HostUpdatePacing    `json:"pacing"`
}

type hostObservation struct {
	ID         string
	LastSeenAt *float64 // ms, as the record API shows last_seen_at
	hostObservationPayload
}

func hostObservations(q *Request) ([]hostObservation, error) {
	rows, err := q.db().HostObservations(q.ctx())
	if err != nil {
		return nil, err
	}
	out := make([]hostObservation, 0, len(rows))
	for _, r := range rows {
		o := hostObservation{ID: r.ID}
		if r.LastSeenAt != nil {
			o.LastSeenAt = ptr(math.Round(float64(r.LastSeenAt.UnixMicro()) / 1000))
		}
		if err := json.Unmarshal(r.Payload, &o.hostObservationPayload); err != nil {
			return nil, fmt.Errorf("host observation %s: %w", r.ID, err)
		}
		out = append(out, o)
	}
	return out, nil
}

func hostHarnessVersions(o hostObservation, live bool) []contract.HostHarnessVersion {
	assets := hostHarnessTargets()[deref(o.Platform)]
	if len(assets) == 0 && live {
		return o.HarnessVersions
	}
	if len(assets) == 0 {
		return nil
	}
	observed := map[string]*string{}
	if live {
		for _, v := range o.HarnessVersions {
			observed[v.Harness] = v.Observed
		}
	}
	rows := make([]contract.HostHarnessVersion, 0, len(assets))
	for _, h := range contract.Vocabulary.Harnesses { // the harnesses Fleet ships a binary for (contracts/vocabulary.json)
		if h.Assets {
			rows = append(rows, contract.HostHarnessVersion{Harness: h.ID, Expected: assets[h.ID].Version, Observed: observed[h.ID]})
		}
	}
	return rows
}

func hostsListReply(q *Request, includeRemoved bool) (contract.HostsListReply, error) {
	var reply contract.HostsListReply
	records, err := hostRecords(q)
	if err != nil {
		return reply, err
	}
	observed, err := hostObservations(q)
	if err != nil {
		return reply, err
	}
	live := make(map[string]hostRecord, len(records))
	removedAt := map[string]*float64{}
	seenID := make(map[string]bool, len(records)+len(observed))
	ids := make([]string, 0, len(records)+len(observed))
	for _, r := range records {
		if r.removed() {
			removedAt[r.ID] = r.RemovedAt
		} else {
			live[r.ID] = r
			if !seenID[r.ID] {
				seenID[r.ID] = true
				ids = append(ids, r.ID)
			}
		}
	}
	obsByID := make(map[string]*hostObservation, len(observed))
	for i := range observed {
		o := &observed[i]
		obsByID[o.ID] = o
		if _, gone := removedAt[o.ID]; !seenID[o.ID] && !gone {
			seenID[o.ID] = true
			ids = append(ids, o.ID)
		}
	}
	if includeRemoved {
		for _, r := range records {
			if r.removed() && !seenID[r.ID] {
				seenID[r.ID] = true
				ids = append(ids, r.ID)
			}
		}
	}
	running, err := q.db().RunningByHostHarness(q.ctx())
	if err != nil {
		return reply, err
	}
	runningByHost := make(map[string]map[string]int64)
	runningTotal := make(map[string]int64)
	for _, r := range running {
		m := runningByHost[r.HostID]
		if m == nil {
			m = map[string]int64{}
			runningByHost[r.HostID] = m
		}
		harness := r.Harness
		if harness == "" {
			harness = "unknown"
		}
		m[harness] = int64(r.N)
		runningTotal[r.HostID] += int64(r.N)
	}
	queued, err := q.db().PendingCommandsByHost(q.ctx())
	if err != nil {
		return reply, err
	}
	queuedByHost := make(map[string]int64, len(queued))
	for _, r := range queued {
		queuedByHost[r.HostID] = int64(r.N)
	}
	runs, err := hostRuns(q)
	if err != nil {
		return reply, err
	}
	now := nowMs()
	out := make([]contract.HostRow, 0, len(ids))
	for _, id := range ids {
		o := obsByID[id]
		by := runningByHost[id]
		if by == nil {
			by = map[string]int64{}
		}
		r, configured := live[id]
		h := contract.HostRow{Id: id, Configured: configured, Capabilities: []string{}, Problems: []contract.HostProblem{},
			RunningByHarness: by, Running: runningTotal[id], Slots: jsonBytes(nil), Queued: queuedByHost[id], Stores: "unknown", StoresReason: ptr("not observed"), AutoUpdate: jsonBytes("on")}
		if configured {
			h.AutoUpdate = jsonBytes(r.autoUpdate())
			h.Slots = jsonBytes(r.MaxConcurrent)
		}
		if at, gone := removedAt[id]; gone {
			h.RemovedAt, h.Configured = at, false
		}
		if id == pds18Host() {
			h.Update = jsonBytes(mergeUpdate(nil))
		}
		if o != nil {
			seen := q.app.effectiveHostSeen(id, valueOr(o.LastSeenAt))
			if seen > 0 {
				h.LastSeenAt = &seen
			} else {
				h.LastSeenAt = o.LastSeenAt
			}
			h.Platform, h.Version = o.Platform, o.Version
			stores, reason := hostStoreStatus(o.Stores, seen, now)
			h.Stores, h.StoresReason = stores, &reason
			if id == pds18Host() {
				var polled any
				json.Unmarshal(o.Update, &polled)
				h.Update = jsonBytes(mergeUpdate(polled))
			} else {
				h.Update = o.Update
			}
			if o.Capabilities != nil {
				h.Capabilities = o.Capabilities
			}
			polling := seen > 0 && float64(now)-seen < hostStaleMs
			// The last failed poll, until a successful one (host_poll_health.go); like problems, only while the host
			// polls: a host that stopped is offline, and an old failure would hide that.
			h.HarnessVersions = hostHarnessVersions(*o, polling && o.PollError == nil)
			if polling && o.PollError != nil {
				h.PollError = o.PollError
			}
			// A host that stopped polling shows no problems: its staleness is the signal.
			if polling && o.Problems != nil {
				h.Problems = o.Problems
			}
			if polling {
				h.Pacing = o.Pacing
			}
			if l := q.app.effectiveHostLoad(id, o.Load); polling && l != nil && now-l.UpdatedAt < hostLoadStaleMs {
				h.Load = &contract.HostLoadReading{Load1: l.Load1, Cpus: l.Cpus, MemFree: l.MemFree, MemTotal: l.MemTotal, SwapUsed: l.SwapUsed, SwapTotal: l.SwapTotal,
					MemCompressed: l.MemCompressed, TmpUsed: l.TmpUsed, TmpTotal: l.TmpTotal, Top: l.Top, DiskFree: l.DiskFree, At: float64(l.UpdatedAt)}
			}
		}
		h.Runs = runs[id]
		if h.Runs == nil {
			h.Runs = []contract.HostRun{}
		}
		h.SlotLimit = hostSlotLimit(r.MaxConcurrent, configured, h.Load)
		out = append(out, h)
	}
	return contract.HostsListReply{StaleAfterMs: hostStaleMs, ServerNow: now, Hosts: out}, nil
}

// hostRuns: the runs the caller may see per host, oldest start first. Slot identity is this order, not a stored slot:
// executors run a host's work as one pool with a count limit and number no slots, so a stored slot would be a second
// fact that could disagree with the pool. Live fills tile i with runs[i] (docs/design-system/pages/live.md).
func hostRuns(q *Request) (map[string][]contract.HostRun, error) {
	rs, err := q.db().HostRuns(q.ctx())
	out := map[string][]contract.HostRun{}
	for _, r := range rs {
		out[r.HostID] = append(out[r.HostID], contract.HostRun{Id: r.ID, State: r.State, StartedAt: optTime(r.StartedAt)})
	}
	return out, err
}

// hostSlotLimit: how many runs the host takes at once, as dispatch counts it (hostConcurrencyLimit): the host's
// maxConcurrent, else the CPU count of a fresh load reading; nil when neither is known.
func hostSlotLimit(maxConcurrent *int64, configured bool, load *contract.HostLoadReading) *int64 {
	if configured && maxConcurrent != nil && *maxConcurrent > 0 {
		return maxConcurrent
	}
	if load != nil && load.Cpus != nil && *load.Cpus > 0 {
		return load.Cpus
	}
	return nil
}

func routeDecisions(q *Request, r *contract.RouteDecisionsRequest) (contract.RouteDecisionsReply, error) {
	out := contract.RouteDecisionsReply{Decisions: []contract.RouteDecision{}}
	add := func(executionID string, at time.Time, hostID *string, candidates, winner []byte) {
		w := json.RawMessage(winner)
		if w == nil {
			w = json.RawMessage("null")
		}
		out.Decisions = append(out.Decisions, contract.RouteDecision{ExecutionId: executionID, At: timeText(at), HostId: hostID,
			Candidates: candidates, Winner: w})
	}
	// A subscription's skips (J5): the newest decisions of the window that skipped it on some host.
	if sub := deref(r.SubscriptionId); sub != "" {
		if len(sub) > 200 || r.ExecutionId != nil {
			return out, fail(400, "Expected executionId or subscriptionId")
		}
		since := time.Now().Add(-24 * time.Hour)
		if r.Since != nil {
			since = time.UnixMilli(*r.Since)
		}
		rs, err := q.db().RouteDecisionsOfSubscription(q.ctx(), db.RouteDecisionsOfSubscriptionParams{Since: pgtype.Timestamptz{Time: since, Valid: true}, SubscriptionID: sub})
		for _, x := range rs {
			add(x.ExecutionID, x.At, x.HostID, x.Candidates, x.Winner)
		}
		return out, err
	}
	id := deref(r.ExecutionId)
	if id == "" || len(id) > 200 {
		return out, fail(400, "Expected executionId")
	}
	rs, err := q.db().RouteDecisionsOf(q.ctx(), id)
	for _, x := range rs {
		add(x.ExecutionID, x.At, x.HostID, x.Candidates, x.Winner)
	}
	return out, err
}

// hostStoreStatus uses only checks reported by this host, never global store reachability; reports is their JSON
// (json.RawMessage as stored, or a value that encodes to it).
func hostStoreStatus(reports any, seen float64, now int64) (string, string) {
	if seen <= 0 {
		return "unknown", "not observed"
	}
	if float64(now)-seen >= hostStaleMs {
		return "unknown", "host observation stale"
	}
	var list []json.RawMessage
	if json.Unmarshal(jsonBytes(reports), &list) != nil || len(list) == 0 {
		return "unknown", "no store checks reported"
	}
	yes, no := false, false
	for _, v := range list {
		var r map[string]json.RawMessage
		if json.Unmarshal(v, &r) != nil || r == nil {
			return "unknown", "invalid store check"
		}
		var reachable bool
		if x, ok := r["reachable"]; !ok || json.Unmarshal(x, &reachable) != nil || string(x) == "null" {
			return "unknown", "missing reachability"
		}
		if reachable {
			yes = true
		} else {
			no = true
		}
	}
	if yes && no {
		return "mixed", "some reported stores unreachable"
	}
	if no {
		return "unreachable", "all reported stores unreachable"
	}
	return "reachable", "all reported store checks succeeded"
}
