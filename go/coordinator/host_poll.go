package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/errtrack"

	"github.com/jackc/pgx/v5"
)

// POST /api/hosts/poll (SL5). Decoupled from GLOBAL: heartbeat writes and readiness checks run under "host:"+hostID
// only when host state/stores changed or >= 30s elapsed; global dispatch/promote/reconcile/problem sweeps run asynchronously
// in the single-flight background dispatcher. Supports ?wait=<duration> (clamped 0..25s) long-polling woken immediately
// via per-host broadcast channels when commands are queued.

const (
	hostHeartbeatFlushMs = 30000
	hostLoadStaleMs      = 2 * 60 * 1000
	maxHostPollWait      = 25 * time.Second
)

type hostLoadObservation struct {
	Load1         *float64                `json:"load1,omitempty"`
	Cpus          *int64                  `json:"cpus,omitempty"`
	MemFree       *float64                `json:"memFree,omitempty"`
	MemTotal      *float64                `json:"memTotal,omitempty"`
	SwapUsed      *float64                `json:"swapUsed,omitempty"`
	SwapTotal     *float64                `json:"swapTotal,omitempty"`
	MemCompressed *float64                `json:"memCompressed,omitempty"`
	TmpUsed       *float64                `json:"tmpUsed,omitempty"`
	TmpTotal      *float64                `json:"tmpTotal,omitempty"`
	Top           []contract.HostConsumer `json:"top,omitempty"`      // largest first; from a heartbeat span: pid 0
	DiskFree      *float64                `json:"diskFree,omitempty"` // heartbeat spans only; a poll's load keeps the last one
	UpdatedAt     int64                   `json:"updatedAt,omitempty"`
}

func hostLoadFromContract(l *contract.HostLoad, now int64) *hostLoadObservation {
	if l == nil {
		return nil
	}
	return &hostLoadObservation{
		Load1:         l.Load1,
		Cpus:          l.Cpus,
		MemFree:       l.MemFree,
		MemTotal:      l.MemTotal,
		SwapUsed:      l.SwapUsed,
		SwapTotal:     l.SwapTotal,
		MemCompressed: l.MemCompressed,
		TmpUsed:       l.TmpUsed,
		TmpTotal:      l.TmpTotal,
		Top:           l.Top,
		UpdatedAt:     now,
	}
}

// hostOverloadReason: why dispatch places no new run on the host, or "": load above the CPU count or free memory below
// ruleMemFreeShare. Swap alone does not refuse placement: macOS keeps swap long after pressure passes, so a host with
// free memory would stay refused; swap above ruleSwapUsedShare is named in the reason and fires host-saturation
// (docs/designs/fleet-host-cleanup.md#6-host-saturation). The reason names the top three memory consumers.
func hostOverloadReason(hostID string, load *hostLoadObservation, now int64) string {
	if load == nil {
		return ""
	}
	if load.UpdatedAt > 0 && now-load.UpdatedAt >= hostLoadStaleMs {
		return ""
	}
	var why string
	switch {
	case load.Cpus != nil && *load.Cpus > 0 && load.Load1 != nil && *load.Load1 > float64(*load.Cpus):
		why = fmt.Sprintf("load %.2f on %d cores", *load.Load1, *load.Cpus)
	case load.MemTotal != nil && *load.MemTotal > 0 && load.MemFree != nil && *load.MemFree < ruleMemFreeShare*(*load.MemTotal):
		why = fmt.Sprintf("free memory %.0f%% below %.0f%%", 100*(*load.MemFree)/(*load.MemTotal), 100*ruleMemFreeShare)
	default:
		return ""
	}
	if load.SwapTotal != nil && *load.SwapTotal > 0 && load.SwapUsed != nil && *load.SwapUsed > ruleSwapUsedShare*(*load.SwapTotal) {
		why += fmt.Sprintf(", swap %s/%s GB", gb(*load.SwapUsed), gb(*load.SwapTotal))
	}
	reason := fmt.Sprintf("%s is overloaded (%s)", hostID, why)
	if top := topConsumers(load.Top, 3); top != "" {
		reason += "; top: " + top
	}
	return reason
}

// gb: bytes as whole gigabytes (GiB), at least one decimal below 10 GB so small hosts do not read 0.
func gb(b float64) string {
	g := b / (1 << 30)
	if g < 10 {
		return strconv.FormatFloat(math.Round(g*10)/10, 'f', -1, 64)
	}
	return strconv.FormatFloat(math.Round(g), 'f', 0, 64)
}

// topConsumers: the first n consumers as "command 20 GB, postgres 2 GB (fleet)"; the executor sends them largest first.
func topConsumers(top []contract.HostConsumer, n int) string {
	parts := make([]string, 0, n)
	for _, c := range top {
		if len(parts) == n {
			break
		}
		p := c.Command + " " + gb(c.MemBytes) + " GB"
		if c.Fleet != nil && *c.Fleet {
			p += " (fleet)"
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// parseHostTop: the heartbeat's fleet.top attribute ("command:MB,...", a trailing * marks a Fleet process) as
// consumers with pid 0; entries that do not parse are skipped.
func parseHostTop(raw string) []contract.HostConsumer {
	var out []contract.HostConsumer
	for _, e := range strings.Split(raw, ",") {
		e = strings.TrimSpace(e)
		fleet := strings.HasSuffix(e, "*")
		e = strings.TrimSuffix(e, "*")
		i := strings.LastIndexByte(e, ':')
		if i <= 0 {
			continue
		}
		mb, err := strconv.ParseFloat(e[i+1:], 64)
		if err != nil || mb < 0 {
			continue
		}
		c := contract.HostConsumer{Command: e[:i], MemBytes: mb * (1 << 20)}
		if fleet {
			c.Fleet = ptr(true)
		}
		out = append(out, c)
	}
	return out
}

type hostHeartbeat struct {
	lastSeenAt      int64
	activePolls     int
	lastPersistedAt int64
	stateSig        string
	storesSig       string
	load            *hostLoadObservation
	overloaded      bool
	// Poll health (host_poll_health.go): pollClean, no failure is recorded in the database; pollProblem, its
	// needs-you problem is open; pollFailures is the sequence number of its last failed poll (pollFailureSeq, never
	// reused after a cache clear), so a success does not mark clean over a later failure.
	pollClean    bool
	pollProblem  bool
	pollFailures uint64
	pollRecorded *pollRecord // the failure last written, so a repeat within a minute needs no transaction
}

type hostWakeEntry struct {
	mu sync.Mutex
	ch chan struct{}
}

func (e *hostWakeEntry) current() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.ch == nil {
		e.ch = make(chan struct{})
	}
	return e.ch
}

func (e *hostWakeEntry) wake() {
	e.mu.Lock()
	if e.ch != nil {
		close(e.ch)
	}
	e.ch = make(chan struct{})
	e.mu.Unlock()
}

func init() {
	hostRouteObserved(contract.RoutePoll, pollObserved, func(q *Request, b *contract.PollRequest) (contract.PollReply, error) { return q.app.pollOnce(q, b) })
}

func (a *App) hostWakeEntryFor(hostID string) *hostWakeEntry {
	a.ensureRuntime()
	a.pollMu.Lock()
	defer a.pollMu.Unlock()
	e := a.hostWake[hostID]
	if e == nil {
		e = &hostWakeEntry{ch: make(chan struct{})}
		a.hostWake[hostID] = e
	}
	return e
}

func (a *App) hostWakeChan(hostID string) <-chan struct{} {
	return a.hostWakeEntryFor(hostID).current()
}

func (a *App) wakeHost(hostID string) {
	if hostID == "" {
		return
	}
	a.hostWakeEntryFor(hostID).wake()
}

func (a *App) wakeAllHosts() {
	a.ensureRuntime()
	a.pollMu.Lock()
	entries := make([]*hostWakeEntry, 0, len(a.hostWake))
	for _, e := range a.hostWake {
		entries = append(entries, e)
	}
	a.pollMu.Unlock()
	for _, e := range entries {
		e.wake()
	}
}

func (a *App) enterHostPoll(hostID string) {
	a.ensureRuntime()
	now := nowMs()
	a.pollMu.Lock()
	hb := a.heartbeatOf(hostID)
	hb.activePolls++
	if now > hb.lastSeenAt {
		hb.lastSeenAt = now
	}
	a.pollMu.Unlock()
}

func (a *App) exitHostPoll(hostID string) {
	a.ensureRuntime()
	now := nowMs()
	a.pollMu.Lock()
	if hb := a.heartbeats[hostID]; hb != nil {
		if hb.activePolls > 0 {
			hb.activePolls--
		}
		if now > hb.lastSeenAt {
			hb.lastSeenAt = now
		}
	}
	a.pollMu.Unlock()
}

func (a *App) heartbeatNeedsFlush(hostID, stateSig, storesSig string, now int64) (flushObs, flushStores, stateChanged bool) {
	a.ensureRuntime()
	a.pollMu.Lock()
	defer a.pollMu.Unlock()
	hb := a.heartbeats[hostID]
	if hb == nil || hb.lastPersistedAt == 0 {
		return true, true, true
	}
	if hb.stateSig != stateSig {
		stateChanged = true
		flushObs = true
	}
	if hb.storesSig != storesSig {
		flushStores = true
	}
	if now-hb.lastPersistedAt >= hostHeartbeatFlushMs {
		flushObs = true
		flushStores = true
	}
	if !flushObs && now > hb.lastSeenAt {
		hb.lastSeenAt = now
	}
	return flushObs, flushStores, stateChanged
}

func (a *App) commitHostHeartbeat(hostID, stateSig, storesSig string, persistedAt int64) {
	a.ensureRuntime()
	a.pollMu.Lock()
	hb := a.heartbeatOf(hostID)
	hb.lastPersistedAt = persistedAt
	hb.stateSig = stateSig
	hb.storesSig = storesSig
	if persistedAt > hb.lastSeenAt {
		hb.lastSeenAt = persistedAt
	}
	a.pollMu.Unlock()
}

func (a *App) clearHostCache(hostID string) {
	if hostID == "" {
		return
	}
	a.ensureRuntime()
	a.pollMu.Lock()
	delete(a.heartbeats, hostID)
	e := a.hostWake[hostID]
	a.pollMu.Unlock()
	if e != nil {
		e.wake()
	}
}

func (a *App) effectiveHostSeen(hostID string, dbSeen float64) float64 {
	if a == nil {
		return dbSeen
	}
	a.ensureRuntime()
	a.pollMu.Lock()
	hb := a.heartbeats[hostID]
	var cached float64
	if hb != nil {
		if hb.activePolls > 0 {
			cached = float64(nowMs())
		} else {
			cached = float64(hb.lastSeenAt)
		}
	}
	a.pollMu.Unlock()
	if cached > dbSeen {
		return cached
	}
	return dbSeen
}

func (a *App) recordHostLoad(hostID string, load *hostLoadObservation, overloaded, onlyIfNewer bool) (changed bool) {
	if a == nil || hostID == "" || load == nil {
		return false
	}
	a.ensureRuntime()
	a.pollMu.Lock()
	defer a.pollMu.Unlock()
	hb := a.heartbeatOf(hostID)
	if onlyIfNewer && hb.load != nil && hb.load.UpdatedAt >= load.UpdatedAt {
		return false
	}
	prevKnown := hb.load != nil
	prevOverloaded := hb.overloaded
	cp := *load
	if cp.DiskFree == nil && hb.load != nil {
		cp.DiskFree = hb.load.DiskFree
	}
	hb.load = &cp
	hb.overloaded = overloaded
	return (!prevKnown && overloaded) || (prevKnown && prevOverloaded != overloaded)
}

func (a *App) effectiveHostLoad(hostID string, dbLoad *hostLoadObservation) *hostLoadObservation {
	if a == nil {
		return dbLoad
	}
	a.ensureRuntime()
	a.pollMu.Lock()
	var cached *hostLoadObservation
	if hb := a.heartbeats[hostID]; hb != nil && hb.load != nil {
		cp := *hb.load
		cached = &cp
	}
	a.pollMu.Unlock()
	if cached == nil {
		return dbLoad
	}
	if dbLoad == nil || cached.UpdatedAt >= dbLoad.UpdatedAt {
		// The heartbeat's top list is short (command:MB, pid 0); the poll's full entries name pid, user and age.
		if dbLoad != nil && len(dbLoad.Top) > 0 && cached.UpdatedAt-dbLoad.UpdatedAt < hostLoadStaleMs {
			cached.Top = dbLoad.Top
		}
		return cached
	}
	return dbLoad
}

func parsePollWait(r *http.Request) time.Duration {
	if r == nil {
		return 0
	}
	q := r.URL.Query()
	var d time.Duration
	if raw := q.Get("wait"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil {
			d = parsed
		} else if ms, err := strconv.ParseInt(raw, 10, 64); err == nil {
			d = time.Duration(ms) * time.Millisecond
		}
	} else if rawMs := q.Get("waitMs"); rawMs != "" {
		if ms, err := strconv.ParseInt(rawMs, 10, 64); err == nil {
			d = time.Duration(ms) * time.Millisecond
		}
	}
	if d <= 0 {
		return 0
	}
	if d > maxHostPollWait {
		return maxHostPollWait
	}
	return d
}

// hostProblem: one problem of a host observation.
type hostProblem struct {
	Kind     string  `json:"kind"`
	Route    string  `json:"route"`
	Status   int64   `json:"status"`
	Location *string `json:"location"`
	Detail   string  `json:"detail"`
}

// hostProblems: the edge problems a host reports (at most 20, texts cut), redirects only.
func hostProblems(list []contract.EdgeProblem) []hostProblem {
	var out []hostProblem
	cut := func(t string, n int) string {
		if r := []rune(t); len(r) > n {
			return string(r[:n])
		}
		return t
	}
	for _, p := range list {
		if p.Kind != contract.ProblemKindEdgeRedirect || p.Status == 0 {
			continue
		}
		var loc *string
		if p.Location != nil {
			l := cut(*p.Location, 500)
			loc = &l
		}
		out = append(out, hostProblem{Kind: string(p.Kind), Route: cut(p.Route, 200), Status: int64(p.Status), Location: loc, Detail: cut(p.Detail, 500)})
		if len(out) == 20 {
			break
		}
	}
	return out
}

func (a *App) pollReadyCommands(ctx context.Context, host string, caps []string) ([]contract.Command, error) {
	var pipelinePending bool
	var hasPending bool
	var removed bool
	if err := a.store.Read(ctx, nil, func(rq *Request) error {
		if h, err := hostRecordByID(rq, host); err != nil {
			return err
		} else if h != nil && h.removed() {
			removed = true
			return nil
		}
		n, err := rq.db().CommandsPendingCount(rq.ctx(), db.CommandsPendingCountParams{Kinds: pipelineDispatchKinds, HostID: &host})
		if err != nil {
			return err
		}
		hasPending, pipelinePending = n.N > 0, n.Pipeline
		return nil
	}); err != nil {
		return nil, err
	}
	if removed {
		return nil, fail(410, "host-removed")
	}
	if !hasPending {
		return []contract.Command{}, nil
	}
	out := []contract.Command{}
	var hostDoc hostRecord // none yet: no holder
	locks := []string{"host:" + host}
	if pipelinePending {
		locks = append([]string{GLOBAL}, locks...)
	}
	err := a.store.Write(ctx, nil, locks, func(wq *Request) error {
		h, err := hostRecordByID(wq, host)
		if err != nil {
			return err
		}
		if h != nil && h.removed() {
			return fail(410, "host-removed")
		}
		if h != nil {
			hostDoc = *h
		}
		pending, err := wq.db().HostPollPending(wq.ctx(), db.HostPollPendingParams{HostID: host})
		if err != nil {
			return err
		}
		out = []contract.Command{}
		prepared := map[string]pollPreparedEntry{}
		afterCommit(wq, func() { pollPrepared.keep(host, prepared, time.Now()) })
		for _, row := range pending {
			cv := pollCommandOf(row, host)
			c := cv.gate()
			// A command waits for an executor that honours it (contract.MissingCapability), also if this host went back a release.
			readOnly, _ := jsonBool(row.ReadOnly)
			branch, isBool := jsonBool(row.Branch)
			if _, missing := contract.MissingCapability(caps, contract.StartNeeds{ReadOnly: cv.Kind == "start" && readOnly,
				Branchless: isBool && !branch && !isNull(row.PayloadProjectID), WorkspaceKey: row.WorkspaceKey != "", Attachments: row.HasAttachments}); missing {
				continue
			}
			// Each command is prepared under its own savepoint: one command that cannot be handed off is stopped with
			// its reason, and the rest of the poll goes on (2026-11-17: one stored run failed every poll for 2.5 h).
			if err := exec(wq, "SAVEPOINT poll_command"); err != nil {
				return err
			}
			fresh, err := a.pollReadyCommand(wq, hostDoc, cv, prepared)
			if err != nil {
				reason, outcome := pollCommandRefusal(err)
				if outcome == pollFails {
					return err // a deadlock, a lost connection, the request leaving: the whole poll fails and is retried
				}
				// Its writes roll back; its afterCommit/afterRollback hooks (a publish, a pipeline write guard) stay, and are idempotent.
				if err := exec(wq, "ROLLBACK TO SAVEPOINT poll_command"); err != nil {
					return err
				}
				if err := exec(wq, "RELEASE SAVEPOINT poll_command"); err != nil {
					return err
				}
				if outcome == pollRetriesCommand {
					// It stays pending for the next poll. A failure that keeps coming is a bug: one log line and one
					// Bugsink event (its issue reaches Needs you) per command and error class, not one per poll.
					if pollCommandWaits.note(cv.ID, errorClass(err)) {
						log.Printf("host %s command %s waits: %v", host, cv.ID, err)
						cerr, kind := err, cv.Kind
						capture := func() {
							errtrack.Capture(cerr, map[string]string{"route": "POST /api/hosts/poll", "host": host, "command": kind})
						}
						afterCommit(wq, capture)
						afterRollback(wq, capture)
					}
					continue
				}
				pollCommandWaits.forget(cv.ID)
				if err := a.refusePollCommand(wq, c, reason); err != nil {
					return err
				}
				log.Printf("host %s command %s stopped: %s", host, cv.ID, reason)
				if !strings.HasPrefix(reason, "refused:") {
					cerr, kind := err, cv.Kind
					afterCommit(wq, func() { // once, for the attempt that committed the stop
						errtrack.Capture(cerr, map[string]string{"route": "POST /api/hosts/poll", "host": host, "command": kind})
					})
				}
				continue
			}
			if err := exec(wq, "RELEASE SAVEPOINT poll_command"); err != nil {
				return err
			}
			pollCommandWaits.forget(cv.ID) // prepared: no longer waiting on an error
			if fresh != nil {
				out = append(out, *fresh)
			}
		}
		return nil
	})
	return out, err
}

var stopWithheldKinds = []string{"start", "input", "script", "steer", "delivery-step"}

// pollCommandView: the fields of a pending command the poll's gates read (HostPollPending).
type pollCommandView struct {
	ID          string
	Kind        string
	ExecutionID string
	HostID      string
	Status      string
	HandedOut   *bool
	ProjectID   json.RawMessage // its payload's projectId
}

func pollCommandOf(row db.HostPollPendingRow, host string) pollCommandView {
	return pollCommandView{ID: row.ID, Kind: row.Kind, ExecutionID: deref(row.ExecutionID), HostID: host, Status: row.Status,
		HandedOut: row.HandedOut, ProjectID: row.PayloadProjectID}
}

// gate: the command record the gates read (agentReady, handoffPipelineCommand, refusePollCommand), without its
// payload: a pipeline handoff that writes the payload reads it itself.
func (cv pollCommandView) gate() pollGateCommand {
	return pollGateCommand{ID: cv.ID, Kind: cv.Kind, ExecutionID: cv.ExecutionID, HostID: cv.HostID, Status: cv.Status}
}

type pollGateCommand struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	ExecutionID string `json:"executionId,omitempty"`
	HostID      string `json:"hostId"`
	Status      string `json:"status"`
}

// pollPreparedEntry: a command as a poll handed it out, at the stored revision it was read at.
type pollPreparedEntry struct {
	revision string
	cmd      contract.Command
}

// pollPrepared: per host, the reply of each pending command the last committed poll handed out. A host gets a pending
// command again on every poll until its receipt (its reply may have been lost); a command whose stored row did not
// change since (HostPollCommandRevision) is answered from here, not read and decoded again (a start is ~100 KB: about
// 85 ms a command a poll under -race, 2026-11-21). Each poll replaces its host's entries, so only pending ones stay; a
// host that has not polled for pollPreparedIdle (gone, removed, replaced) loses its entries at another host's poll.
var pollPrepared = &preparedCommands{m: map[string]preparedHost{}}

const pollPreparedIdle = 10 * time.Minute

type preparedCommands struct {
	mu sync.Mutex
	m  map[string]preparedHost
}

type preparedHost struct {
	at      time.Time
	entries map[string]pollPreparedEntry
}

func (p *preparedCommands) get(host, id, revision string) (contract.Command, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.m[host].entries[id]
	return e.cmd, ok && e.revision == revision
}

// keep makes entries host's prepared commands (after the poll that read them committed, at now).
func (p *preparedCommands) keep(host string, entries map[string]pollPreparedEntry, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for h, x := range p.m {
		if now.Sub(x.at) > pollPreparedIdle {
			delete(p.m, h)
		}
	}
	if len(entries) == 0 {
		delete(p.m, host)
		return
	}
	p.m[host] = preparedHost{now, entries}
}

// pollExecutionView: the fields of a command's execution the poll reads.
type pollExecutionView struct {
	TaskStopRequested *bool           `json:"taskStopRequested"`
	ProjectID         json.RawMessage `json:"projectId"`
	DirectedBy        json.RawMessage `json:"directedBy"`
}

// pollReadyCommand: command c as the host gets it now, or nil while it is not ready. The ready filter may rewrite the
// command (delegation inputs), so the reply is the command as stored after it.
func (a *App) pollReadyCommand(wq *Request, hostDoc hostRecord, cv pollCommandView, prepared map[string]pollPreparedEntry) (*contract.Command, error) {
	ok, err := agentReady(wq, cv.Kind, cv.ExecutionID)
	if err != nil || !ok {
		return nil, err
	}
	if inList(stopWithheldKinds, cv.Kind) {
		// This row lock is shared with stop. A committed poll may have
		// reached the executor even when its HTTP response is lost.
		locked, err := wq.db().HostPollLockExecution(wq.ctx(), cv.ExecutionID)
		if err != nil {
			return nil, err
		}
		if !locked {
			// A busy row is retried. A missing row still goes through
			// the poll's permanent 404 refusal and retires its command.
			_, err := getExecution(wq, cv.ExecutionID)
			return nil, err
		}
		// The command as it is under the lock (a stop may have settled it, a writer changed it).
		status, err := commandStatus(wq, cv.ID)
		if err != nil {
			return nil, err
		}
		if status == "" {
			return nil, errNotFound
		}
		rows, err := wq.db().HostPollPending(wq.ctx(), db.HostPollPendingParams{HostID: cv.HostID, ID: &cv.ID})
		if err != nil || len(rows) == 0 {
			return nil, err // no longer pending here
		}
		cv = pollCommandOf(rows[0], cv.HostID)
	}
	e, err := executionRawGet(wq, cv.ExecutionID) // open: the delegation and handoff gates may rewrite it
	if err != nil {
		return nil, err
	}
	ev := agentView[pollExecutionView](e)
	// A previously handed-out start may already be in flight. Stop it on its
	// owning host, while withholding further delivery of work after the stop.
	if ev.TaskStopRequested != nil && *ev.TaskStopRequested && inList(stopWithheldKinds, cv.Kind) {
		return nil, nil
	}
	// Held-host trust: work that carries project content goes only to a host whose holder may see the project.
	if k := cv.Kind; k == "start" || k == "script" || k == "delivery-step" || k == string(contract.CommandKindPublishing) {
		if !a.hostMayRun(hostDoc, nonNil(rawAny(ev.ProjectID), rawAny(cv.ProjectID)), rawAny(ev.DirectedBy)) {
			return nil, nil
		}
	}
	if cv.Kind != "stop" {
		ok, err := a.delegationReady(wq, e)
		if err != nil || !ok {
			return nil, err
		}
	}
	if ok, err := handoffPipelineCommand(wq, e, cv.gate()); err != nil || !ok {
		return nil, err
	}
	if cv.HandedOut != nil && !*cv.HandedOut {
		if err := wq.db().CommandHandOut(wq.ctx(), cv.ID); err != nil {
			return nil, err
		}
	}
	revision, err := wq.db().HostPollCommandRevision(wq.ctx(), cv.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	cmd, ok := pollPrepared.get(cv.HostID, cv.ID, revision)
	if !ok {
		out, err := commandRawByID(wq, cv.ID)
		if err != nil {
			return nil, err
		}
		delete(out, "handedOut") // coordinator-only delivery fence, keep executor replays byte-identical
		if cmd, err = asT[contract.Command](out); err != nil {
			return nil, fail(410, "command does not match the host protocol: "+err.Error()) // stopped with the reason
		}
	}
	prepared[cv.ID] = pollPreparedEntry{revision, cmd}
	return &cmd, nil
}

// pollCommandRefusal: what a command's failed preparation does. The command's own lasting failure (an authorization
// refusal, a stored row that is gone) stops it with the reason; anything else (a conflict, a race, a busy
// dependency, a database or other error) leaves it pending for the next poll; a serialization failure, a deadlock, a
// lost connection or the request leaving fail the poll as before (and are retried).
func pollCommandRefusal(err error) (string, pollCommandOutcome) {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", pollFails
	}
	var he *HTTPError
	if errors.As(err, &he) {
		switch he.Code {
		case 403:
			return "refused: " + he.Msg, pollStopsCommand // an authorization refusal: never handed over, never widened
		case 404, 410:
			return "handoff failed: " + he.Msg, pollStopsCommand // a stored row that is gone, or does not fit contract.Command
		}
		return "", pollRetriesCommand // a conflict, a race or a busy dependency: the next poll tries again
	}
	code := pgCode(err)
	switch {
	// Class 40 (serialization, deadlock), 08 (connection) and 57 (operator intervention, statement timeout) fail the
	// poll for its retry; a lost connection without a code fails the savepoint rollback, and with it the poll.
	case strings.HasPrefix(code, "40") || strings.HasPrefix(code, "08") || strings.HasPrefix(code, "57"):
		return "", pollFails
	}
	// Any other database error may be the coordinator's (a statement or schema bug after a deploy): the command waits,
	// never destroyed for it.
	return "", pollRetriesCommand
}

// commandWaits: the error class each waiting command was last reported with (pollCommandWaits), so a command that keeps
// failing is logged and sent to Bugsink once per class, not once per poll. An entry goes when its command prepares or
// is stopped; the map holds at most commandWaitsCap entries (when full it starts over, which only repeats reports).
type commandWaits struct {
	mu sync.Mutex
	m  map[string]string
}

const commandWaitsCap = 4096

var pollCommandWaits = &commandWaits{m: map[string]string{}}

// note records that command id waits on class; true when that is new (report it).
func (w *commandWaits) note(id, class string) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.m[id] == class {
		return false
	}
	if _, ok := w.m[id]; !ok && len(w.m) >= commandWaitsCap {
		w.m = map[string]string{}
	}
	w.m[id] = class
	return true
}

func (w *commandWaits) forget(id string) {
	w.mu.Lock()
	delete(w.m, id)
	w.mu.Unlock()
}

func (w *commandWaits) size() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.m)
}

// errorClass: a stable class of err (its HTTP status and reason code, or its SQLSTATE), never its text.
func errorClass(err error) string {
	var he *HTTPError
	if errors.As(err, &he) {
		r, _ := he.extraReason()
		return fmt.Sprintf("http_%d:%s", he.Code, r)
	}
	if code := pgCode(err); code != "" {
		return "sqlstate_" + code
	}
	// Digits (ids, times, revisions) do not make a new class; the cap bounds the rest.
	m := []rune(errorDigits.ReplaceAllString(err.Error(), "#"))
	if len(m) > 120 {
		m = m[:120]
	}
	return "go:" + string(m)
}

var errorDigits = regexp.MustCompile(`[0-9]+`)

type pollCommandOutcome int

const (
	pollFails pollCommandOutcome = iota
	pollRetriesCommand
	pollStopsCommand
)

// refusePollCommand settles a command that will never be handed off: it is stopped with its reason. A pipeline's start
// or script whose plan entry is still prepared (the handoff gate is the only way to a host, so no host has seen it)
// also ends its execution, cancelled with the reason, so the step does not wait on a command that cannot come. A
// command that may have reached a host already (a redelivery, any other kind) leaves its execution to its receipts and
// the lost-host sweep.
func (a *App) refusePollCommand(wq *Request, c pollGateCommand, reason string) error {
	if err := stopPipelineCommand(wq, c, reason); err != nil {
		return err
	}
	cmd := agentView[agentCommand](c)
	if !pipelineDispatchKind(cmd.Kind) {
		return nil
	}
	e, err := executionRawMaybe(wq, cmd.ExecutionID)
	if err != nil || e == nil {
		return err
	}
	native := agentView[pipelineRefused](e)
	if native.PipelineRunID == "" || executionEnds(native.State) || native.State == "running" {
		return nil
	}
	run, err := getRun(wq, native.PipelineRunID)
	if pipelineFormatUnsupported(err) || err == nil && run == nil {
		return nil // no run to read: nothing proves the command never went out
	}
	if err != nil {
		return err
	}
	index := pipelineStepIndex(run, native.PipelineStepID)
	if index < 0 {
		return nil
	}
	if d := run.entry(index).Dispatch; d == nil || d.Phase != contract.PipelineDispatchPhasePrepared || deref(d.CommandId) != cmd.ID {
		return nil
	}
	now := nowMs()
	ex := e
	ex["state"], ex["stopDetail"], ex["finishedAt"], ex["updatedAt"] = jsonBytes("cancelled"), jsonBytes(reason), jsonBytes(now), jsonBytes(now)
	delete(ex, "waitReason")
	delete(ex, "waitDetail")
	if err := executionPutRaw(wq, ex); err != nil {
		return err
	}
	// The dispatch pass runs the pipeline engine's sweeps: the step sees its native execution end.
	afterCommit(wq, func() {
		a.wakeDispatch()
	})
	return wq.db().HostPollClearWaitReason(wq.ctx(), native.ID)
}

// pipelineRefused: the fields of a refused command's execution refusePollCommand reads.
type pipelineRefused struct {
	ID             string `json:"id"`
	State          string `json:"state"`
	PipelineRunID  string `json:"pipelineRunId"`
	PipelineStepID string `json:"pipelineStepId"`
}

// pollObserved runs one host poll, its body's decoding included, and records its health: a failure the host cannot
// report (it gets no reply to report with) is recorded here, and the next success clears it (host_poll_health.go). The
// host is seen before its body is read: a host whose every poll fails still shows as polling (with pollError).
func pollObserved(q *Request, run func() (any, error)) (any, error) {
	a, host := q.app, hostOf(q)
	a.enterHostPoll(host)
	defer a.exitHostPoll(host)
	out, err := run()
	if err != nil && q.ctx().Err() != nil {
		// The host left (a restart mid long-poll): whatever error that surfaced as, it is not a failure to record.
	} else if err != nil {
		a.notePollFailure(q.ctx(), host, q.host.Holder, err)
	} else {
		a.notePollSuccess(q.ctx(), host)
	}
	return out, err
}

func (a *App) pollOnce(q *Request, b *contract.PollRequest) (contract.PollReply, error) {
	host := hostOf(q)
	open := q.obj() // the open parts (stores, update) as the host's plain JSON
	stores := rawAny(open.Stores)
	now := nowMs()
	obs := hostPollObservation{ID: host, Holder: q.host.Holder}
	obs.Version = b.Version
	if b.Platform.Valid() {
		obs.Platform = string(b.Platform)
	}
	if b.Capabilities != nil {
		obs.Capabilities = &b.Capabilities
	}
	if b.HarnessVersions != nil {
		obs.HarnessVersions = &b.HarnessVersions
	}
	if b.ToolVersions != nil {
		obs.ToolVersions = &b.ToolVersions
	}
	if b.GitConnections != nil {
		obs.GitConnections = &b.GitConnections
	}
	if b.ProjectRoots != nil {
		obs.ProjectRoots = &b.ProjectRoots
	}
	if b.ForgeLogins != nil {
		obs.ForgeLogins = &b.ForgeLogins
	}
	if b.ActiveExecutions != nil {
		active := append([]string(nil), b.ActiveExecutions...)
		sort.Strings(active)
		obs.ActiveExecutions = &active
	}
	ps := hostProblems(b.Problems)
	if b.Version != nil {
		v := *b.Version
		// N-1 is served without a flag; N-2 and older show version-behind (work still runs), unless the executor has not
		// changed since (release pacing).
		if rel := ownRelease(); versionBehind(v, rel) {
			ps = append(ps, hostProblem{Kind: string(contract.ProblemKindVersionBehind), Detail: "executor " + v + ", coordinator serves " + rel.Previous + " and newer"})
		}
	}
	// Release pacing (update_pacing.go): the target this poll offers, decided once for the observation and the reply.
	offered, offeredRel := healthyRelease()
	pace := paceUpdate(offered, offeredRel, b.Version)
	obs.Pacing = pace.view
	obs.Problems = ps
	upd := rawAny(open.Update)
	if host != "" && host == pds18Host() {
		upd = mergeUpdate(upd) // PDS18: the coordinator side's result from fleet-update joins the executor's
	}
	updResult, hasUpd := decodeUpdate(upd)
	if hasUpd {
		obs.Update = upd
	}

	stateSig := canonical(obs)
	storesSig := canonical(stores)
	flushObs, flushStores, stateChanged := a.heartbeatNeedsFlush(host, stateSig, storesSig, now)
	loadObs := hostLoadFromContract(b.Load, now)
	if loadObs != nil {
		overloaded := hostOverloadReason(host, loadObs, now) != ""
		if a.recordHostLoad(host, loadObs, overloaded, false) {
			flushObs = true
			stateChanged = true
		}
		obs.Load = loadObs
	}
	// Store changes persist with host observations without triggering global state-change work.
	obs.Stores = stores
	flushObs = flushObs || flushStores

	var flushGen uint64
	ctx := q.ctx()
	if flushObs || flushStores {
		obs.LastSeenAt = now
		if err := a.store.Write(ctx, nil, []string{"host:" + host}, func(wq *Request) error {
			if flushStores {
				if err := storesObserve(wq.ctx(), wq.tx, host, stores); err != nil {
					return err
				}
			}
			if flushObs {
				var prev *struct {
					Update    json.RawMessage `json:"update"`
					PollError json.RawMessage `json:"pollError"`
				}
				if r, err := wq.db().HostObservationOf(wq.ctx(), host); err == nil {
					var p struct {
						Update    json.RawMessage `json:"update"`
						PollError json.RawMessage `json:"pollError"`
					}
					if err := lenientDecode(r.Payload, &p); err != nil {
						return err
					}
					prev = &p
				} else if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
				var prevUpdate any
				row := obs
				if prev != nil {
					prevUpdate = rawAny(prev.Update)
					// A recorded poll failure stays until a successful poll clears it (host_poll_health.go).
					if !isNull(prev.PollError) {
						row.PollError = prev.PollError
					}
				}
				// Look at update question only when the update result changes.
				if hasUpd && js(prevUpdate) != js(upd) {
					if err := updateQuestion(wq, host, q.host.Holder, updResult); err != nil {
						return err
					}
				}
				// The update history (host_updates.go): a row per changed result, compared with the host's newest row, so
				// the first flush after a coordinator start records a host's current result once.
				if hasUpd {
					if err := hostUpdateRecord(wq, host, q.host.Holder, obs.Platform, b.Version, b.HarnessVersions, updResult); err != nil {
						return err
					}
				}
				if err := wq.db().HostObservationPut(wq.ctx(), db.HostObservationPutParams{
					ID:         row.ID,
					Holder:     &row.Holder,
					LastSeenAt: floatMsPtr(float64(row.LastSeenAt)),
					Payload:    jsonBytes(row.hostPollObservationPayload),
				}); err != nil {
					return err
				}
			}
			afterCommit(wq, func() {
				a.commitHostHeartbeat(host, stateSig, storesSig, now)
				if stateChanged {
					flushGen = a.triggerDispatch()
				}
			})
			return nil
		}); err != nil {
			return contract.PollReply{}, err
		}
	}

	if err := recordDiagnosis(q, host, b.Diagnosis); err != nil {
		return contract.PollReply{}, err
	}
	wait := parsePollWait(q.r)
	out := []contract.Command{}
	var err error
	if wait == 0 {
		target := flushGen
		if cur := a.currentDispatchReqGen(); cur > target {
			target = cur
		}
		if target > 0 || !a.dispatchLoopStarted.Load() {
			if err := a.waitDispatchCaughtUp(ctx, target); err != nil {
				return contract.PollReply{}, err
			}
		}
		out, err = a.pollReadyCommands(ctx, host, b.Capabilities)
		if err != nil {
			return contract.PollReply{}, err
		}
	} else {
		deadline := time.NewTimer(wait)
		defer deadline.Stop()
		for {
			wakeCh := a.hostWakeChan(host)
			out, err = a.pollReadyCommands(ctx, host, b.Capabilities)
			if err != nil {
				return contract.PollReply{}, err
			}
			if len(out) > 0 || pollWantsDiagnosis(q, host) {
				break
			}
			select {
			case <-ctx.Done():
				return contract.PollReply{}, ctx.Err()
			case <-a.stoppedCh():
				out = []contract.Command{}
				goto replyNow
			case <-deadline.C:
				out, err = a.pollReadyCommands(ctx, host, b.Capabilities)
				if err != nil {
					return contract.PollReply{}, err
				}
				goto replyNow
			case <-wakeCh:
			}
		}
	}
replyNow:
	reply := contract.PollReply{Commands: out, Target: pace.target, AutoUpdate: ptr(autoUpdateOf(q.host))}
	if pollWantsDiagnosis(q, host) {
		reply.Diagnose = ptr(true)
	}
	return reply, nil
}

func (a *App) invalidateEnvCache() {
	if a == nil {
		return
	}
	a.ensureRuntime()
	a.envCacheGen.Add(1)
	a.envMu.Lock()
	a.envCacheOK = false
	a.envCacheData = nil
	a.envMu.Unlock()
}

func invalidateEnvCacheOnCommit(q *Request) {
	if q == nil || q.app == nil || q.tx == nil {
		return
	}
	afterCommit(q, func() {
		q.app.invalidateEnvCache()
		q.app.wakeDispatch()
	})
}

func (a *App) projectEnvironmentsCached(q *Request) (map[string][]string, error) {
	a.ensureRuntime()
	a.envMu.RLock()
	if a.envCacheOK {
		out := make(map[string][]string, len(a.envCacheData))
		for k, v := range a.envCacheData {
			out[k] = append([]string{}, v...)
		}
		a.envMu.RUnlock()
		return out, nil
	}
	a.envMu.RUnlock()

	gen := a.envCacheGen.Load()
	envs, err := projectEnvironments(q)
	if err != nil {
		return nil, err
	}
	a.envMu.Lock()
	if a.envCacheGen.Load() == gen {
		copyMap := make(map[string][]string, len(envs))
		for k, v := range envs {
			copyMap[k] = append([]string{}, v...)
		}
		a.envCacheData = copyMap
		a.envCacheOK = true
	}
	a.envMu.Unlock()
	return envs, nil
}

// projectEnvironments reads current effective bundles, never shipped seed files.
func projectEnvironments(q *Request) (map[string][]string, error) {
	envs := map[string][]string{}
	ids, err := projectIDs(q)
	if err != nil {
		return nil, err
	}
	for _, p := range ids {
		d, err := projectContext(q, p)
		if err != nil {
			return nil, err
		}
		st, _, err := projectSettings(d)
		if err != nil {
			return nil, err
		}
		hs := []string{}
		for _, x := range agentView[struct {
			Environments []struct {
				HostID json.RawMessage `json:"hostId"`
			} `json:"environments"`
		}](st).Environments {
			id, _ := jsonString(x.HostID)
			hs = append(hs, id)
		}
		envs[p] = hs
	}
	return envs, nil
}

func safeInt(v any) any {
	f, ok := v.(float64)
	if ok && f == float64(int64(f)) && f >= -(1<<53-1) && f <= 1<<53-1 {
		return int64(f)
	}
	return nil
}

// hostList: every host that is not removed, as its open record with the bare id (dispatch.go reads it).
func hostList(q *Request) ([]rawObject, error) {
	hs, err := hostsLive(q)
	out := make([]rawObject, 0, len(hs))
	for _, h := range hs {
		var r rawObject
		if err := json.Unmarshal(jsonBytes(h), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, err
}

// hostMayRun: h is a host record; projectID and director are a record's values (text, else none).
func (a *App) hostMayRun(h hostRecord, projectID any, director any) bool {
	p, _ := projectID.(string)
	d, _ := director.(string)
	return a.holderMayRun(h.holder(), p, d)
}

// hostPollObservationPayload holds the payload fields of a host observation (host_observation.payload).
type hostPollObservationPayload struct {
	Platform         string                     `json:"platform,omitempty"`
	Capabilities     *[]string                  `json:"capabilities,omitempty"`
	HarnessVersions  *[]contract.HarnessVersion `json:"harnessVersions,omitempty"`
	ToolVersions     *[]contract.ToolVersion    `json:"toolVersions,omitempty"`
	GitConnections   *[]string                  `json:"gitConnections,omitempty"`
	ProjectRoots     *[]string                  `json:"projectRoots,omitempty"`
	ForgeLogins      *[]contract.ForgeLogin     `json:"forgeLogins,omitempty"`
	ActiveExecutions *[]string                  `json:"activeExecutions,omitempty"`
	Version          *string                    `json:"version,omitempty"`
	Problems         []hostProblem              `json:"problems,omitempty"`
	Update           any                        `json:"update,omitempty"`
	Stores           any                        `json:"stores"`
	Load             *hostLoadObservation       `json:"load,omitempty"`
	Drain            *contract.DrainReport      `json:"drain,omitempty"`
	Pacing           *contract.HostUpdatePacing `json:"pacing,omitempty"`
	PollError        json.RawMessage            `json:"pollError,omitempty"`
}

// hostPollObservation: what a poll observes of its host (the host-observation record): its platform, capabilities,
// versions, problems, update and stores; a recorded poll failure stays on it.
type hostPollObservation struct {
	ID         string `json:"id"`
	Holder     string `json:"holder"`
	LastSeenAt int64  `json:"lastSeenAt,omitempty"`
	hostPollObservationPayload
}

// pollOpen: the open parts of a poll body (stores, update) as the host's plain JSON.
type pollOpen struct {
	Stores json.RawMessage `json:"stores"`
	Update json.RawMessage `json:"update"`
}

func (q *Request) obj() pollOpen {
	var b pollOpen
	if q.body != nil {
		decode(q.body, &b)
	}
	return b
}
