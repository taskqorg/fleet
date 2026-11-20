package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
	"fleet/errtrack"
)

// POST /api/hosts/poll (SL5). Decoupled from GLOBAL: heartbeat writes and readiness checks run under "host:"+hostID
// only when host state/stores changed or >= 30s elapsed; global dispatch/promote/reconcile/problem sweeps run asynchronously
// in the single-flight background dispatcher. Supports ?wait=<duration> (clamped 0..25s) long-polling woken immediately
// via per-host broadcast channels when commands are queued.

const (
	hostHeartbeatFlushMs = 30000
	maxHostPollWait      = 25 * time.Second
)

type hostHeartbeat struct {
	lastSeenAt      int64
	activePolls     int
	lastPersistedAt int64
	stateSig        string
	storesSig       string
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

// hostProblems: the edge problems a host reports (at most 20, texts cut), redirects only.
func hostProblems(list []contract.EdgeProblem) []any {
	var out []any
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
		var loc any
		if p.Location != nil {
			loc = cut(*p.Location, 500)
		}
		out = append(out, doc{"kind": string(p.Kind), "route": cut(p.Route, 200), "status": p.Status, "location": loc, "detail": cut(p.Detail, 500)})
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
	if err := runTx(ctx, a, nil, false, "", func(rq *Request) error {
		if h, err := maybe(rq, "host:"+host, "host"); err != nil {
			return err
		} else if h != nil && hostRemoved(h) {
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
	var hostDoc doc
	locks := []string{"host:" + host}
	if pipelinePending {
		locks = append([]string{GLOBAL}, locks...)
	}
	err := runTxLocks(ctx, a, nil, true, locks, func(wq *Request) error {
		h, err := maybe(wq, "host:"+host, "host")
		if err != nil {
			return err
		}
		if h != nil && hostRemoved(h) {
			return fail(410, "host-removed")
		}
		hostDoc = h
		if hostDoc == nil {
			hostDoc = doc{"hostId": host}
		}
		pending, err := all(wq, "command", doc{"hostId": host, "status": "pending"})
		if err != nil {
			return err
		}
		out = []contract.Command{}
		for _, c := range pending {
			// A command waits for an executor that honours it (contract.MissingCapability), also if this host went back a release.
			pl := mapOf(c["payload"])
			if _, missing := contract.MissingCapability(caps, contract.StartNeeds{ReadOnly: c["kind"] == "start" && pl["readOnly"] == true,
				Branchless: pl["branch"] == false && pl["projectId"] != nil, WorkspaceKey: astr(pl["workspaceKey"]) != "", Attachments: len(anys(pl["attachments"])) > 0}); missing {
				continue
			}
			// Each command is prepared under its own savepoint: one command that cannot be handed off is stopped with
			// its reason, and the rest of the poll goes on (2026-11-17: one stored run failed every poll for 2.5 h).
			if err := exec(wq, "SAVEPOINT poll_command"); err != nil {
				return err
			}
			fresh, err := a.pollReadyCommand(wq, hostDoc, c)
			if err != nil {
				reason, outcome := pollCommandRefusal(err)
				if outcome == pollFails {
					return err // a deadlock, a lost connection, the request leaving: the whole poll fails and is retried
				}
				// Its writes roll back; its onEnd hooks (a publish, a pipeline write guard) stay, and are idempotent.
				if err := exec(wq, "ROLLBACK TO SAVEPOINT poll_command"); err != nil {
					return err
				}
				if err := exec(wq, "RELEASE SAVEPOINT poll_command"); err != nil {
					return err
				}
				if outcome == pollRetriesCommand {
					// It stays pending for the next poll. A failure that keeps coming is a bug: one log line and one
					// Bugsink event (its issue reaches Needs you) per command and error class, not one per poll.
					if pollCommandWaits.note(astr(c["id"]), errorClass(err)) {
						log.Printf("host %s command %s waits: %v", host, astr(c["id"]), err)
						cerr, kind := err, astr(c["kind"])
						onEnd(wq.tx, func(bool) {
							errtrack.Capture(cerr, map[string]string{"route": "POST /api/hosts/poll", "host": host, "command": kind})
						})
					}
					continue
				}
				pollCommandWaits.forget(astr(c["id"]))
				if err := a.refusePollCommand(wq, c, reason); err != nil {
					return err
				}
				log.Printf("host %s command %s stopped: %s", host, astr(c["id"]), reason)
				if !strings.HasPrefix(reason, "refused:") {
					cerr, kind := err, astr(c["kind"])
					onEnd(wq.tx, func(committed bool) { // once, for the attempt that committed the stop
						if committed {
							errtrack.Capture(cerr, map[string]string{"route": "POST /api/hosts/poll", "host": host, "command": kind})
						}
					})
				}
				continue
			}
			if err := exec(wq, "RELEASE SAVEPOINT poll_command"); err != nil {
				return err
			}
			pollCommandWaits.forget(astr(c["id"])) // prepared: no longer waiting on an error
			if fresh != nil {
				out = append(out, *fresh)
			}
		}
		return nil
	})
	return out, err
}

var stopWithheldKinds = []string{"start", "input", "script", "steer", "delivery-step"}

// pollReadyCommand: command c as the host gets it now, or nil while it is not ready. The ready filter may rewrite the
// command (delegation inputs), so the reply is the command as stored after it.
func (a *App) pollReadyCommand(wq *Request, hostDoc, c doc) (*contract.Command, error) {
	ok, err := agentReady(wq, c)
	if err != nil || !ok {
		return nil, err
	}
	if inList(stopWithheldKinds, astr(c["kind"])) {
		// This row lock is shared with stop. A committed poll may have
		// reached the executor even when its HTTP response is lost.
		locked, err := has(wq, "SELECT 1 FROM execution WHERE id=$1 FOR UPDATE SKIP LOCKED", c["executionId"])
		if err != nil {
			return nil, err
		}
		if !locked {
			// A busy row is retried. A missing row still goes through
			// the poll's permanent 404 refusal and retires its command.
			_, err := get(wq, c["executionId"], "execution")
			return nil, err
		}
		c, err = get(wq, c["id"], "command")
		if err != nil || c["status"] != "pending" {
			return nil, err
		}
	}
	e, err := get(wq, c["executionId"], "execution")
	if err != nil {
		return nil, err
	}
	// A previously handed-out start may already be in flight. Stop it on its
	// owning host, while withholding further delivery of work after the stop.
	if e["taskStopRequested"] == true && inList(stopWithheldKinds, astr(c["kind"])) {
		return nil, nil
	}
	// Held-host trust: work that carries project content goes only to a host whose holder may see the project.
	if k := astr(c["kind"]); k == "start" || k == "script" || k == "delivery-step" {
		if !a.hostMayRun(hostDoc, nonNil(e["projectId"], mapOf(c["payload"])["projectId"]), e["directedBy"]) {
			return nil, nil
		}
	}
	if c["kind"] != "stop" {
		ok, err := a.delegationReady(wq, e)
		if err != nil || !ok {
			return nil, err
		}
	}
	if ok, err := handoffPipelineCommand(wq, e, c); err != nil || !ok {
		return nil, err
	}
	if c["handedOut"] == false {
		if err := wq.db().CommandHandOut(wq.ctx(), astr(c["id"])); err != nil {
			return nil, err
		}
	}
	out, err := get(wq, c["id"], "command")
	if err != nil {
		return nil, err
	}
	delete(out, "handedOut") // coordinator-only delivery fence, keep executor replays byte-identical
	cmd, err := asT[contract.Command](out)
	if err != nil {
		return nil, fail(410, "command does not match the host protocol: "+err.Error()) // stopped with the reason
	}
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
func (a *App) refusePollCommand(wq *Request, c doc, reason string) error {
	if err := stopPipelineCommand(wq, c, reason); err != nil {
		return err
	}
	if !pipelineDispatchKind(c["kind"]) {
		return nil
	}
	e, err := maybe(wq, c["executionId"], "execution")
	if err != nil || e == nil || e["pipelineRunId"] == nil || terminal(e) || e["state"] == "running" {
		return err
	}
	run, err := getRun(wq, astr(e["pipelineRunId"]))
	if pipelineFormatUnsupported(err) || err == nil && run == nil {
		return nil // no run to read: nothing proves the command never went out
	}
	if err != nil {
		return err
	}
	index := pipelineStepIndex(run, astr(e["pipelineStepId"]))
	if index < 0 {
		return nil
	}
	if d := mapOf(pipelinePlanEntry(run, index)["dispatch"]); d["phase"] != "prepared" || d["commandId"] != c["id"] {
		return nil
	}
	now := nowMs()
	e["state"], e["stopDetail"], e["finishedAt"], e["updatedAt"] = "cancelled", reason, now, now
	delete(e, "waitReason")
	delete(e, "waitDetail")
	if err := put(wq, "execution", e); err != nil {
		return err
	}
	// The dispatch pass runs the pipeline engine's sweeps: the step sees its native execution end.
	onEnd(wq.tx, func(committed bool) {
		if committed {
			a.wakeDispatch()
		}
	})
	return exec(wq, "UPDATE execution SET wait_reason=NULL WHERE id=$1", e["id"])
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
		a.notePollFailure(q.ctx(), host, q.host["holder"], err)
	} else {
		a.notePollSuccess(q.ctx(), host)
	}
	return out, err
}

func (a *App) pollOnce(q *Request, b *contract.PollRequest) (contract.PollReply, error) {
	host := hostOf(q)
	open := q.obj() // the open parts (stores, update) as the host's plain JSON
	now := nowMs()
	obs := doc{"id": host, "holder": q.host["holder"]}
	if b.Platform.Valid() {
		obs["platform"] = string(b.Platform)
	}
	if b.Capabilities != nil {
		obs["capabilities"] = b.Capabilities
	}
	if b.HarnessVersions != nil {
		obs["harnessVersions"] = b.HarnessVersions
	}
	if b.GitConnections != nil {
		obs["gitConnections"] = b.GitConnections
	}
	if b.ProjectRoots != nil {
		obs["projectRoots"] = b.ProjectRoots
	}
	ps := hostProblems(b.Problems)
	if b.Version != nil {
		v := *b.Version
		obs["version"] = v
		// N-1 is served without a flag; N-2 and older show version-behind (work still runs).
		if prev := ownRelease().Previous; olderVersion(v, prev) {
			ps = append(ps, doc{"kind": string(contract.ProblemKindVersionBehind), "route": "", "status": 0, "location": nil,
				"detail": "executor " + v + ", coordinator serves " + prev + " and newer"})
		}
	}
	if ps != nil {
		obs["problems"] = ps
	}
	upd := open["update"]
	if host != "" && host == pds18Host() {
		upd = mergeUpdate(upd) // PDS18: the coordinator side's result from fleet-update joins the executor's
	}
	updResult, hasUpd := decodeUpdate(upd)
	if hasUpd {
		obs["update"] = upd
	}

	stateSig := canonical(obs)
	storesSig := canonical(open["stores"])
	flushObs, flushStores, stateChanged := a.heartbeatNeedsFlush(host, stateSig, storesSig, now)
	// Store changes persist with host observations without triggering global state-change work.
	obs["stores"] = open["stores"]
	flushObs = flushObs || flushStores

	var flushGen uint64
	ctx := q.ctx()
	if flushObs || flushStores {
		obs["lastSeenAt"] = now
		if err := runTx(ctx, a, nil, true, "host:"+host, func(wq *Request) error {
			if flushStores {
				if err := storesObserve(wq.ctx(), wq.tx, host, open["stores"]); err != nil {
					return err
				}
			}
			if flushObs {
				prev, err := maybe(wq, host, "host-observation")
				if err != nil {
					return err
				}
				// Look at update question only when the update result changes.
				if hasUpd && js(mapOf(prev)["update"]) != js(upd) {
					if err := updateQuestion(wq, host, q.host["holder"], updResult); err != nil {
						return err
					}
				}
				// A recorded poll failure stays until a successful poll clears it (host_poll_health.go).
				row := obs
				if pe := mapOf(prev)["pollError"]; pe != nil {
					row = doc{"pollError": pe}
					for k, v := range obs {
						row[k] = v
					}
				}
				if err := put(wq, "host-observation", row); err != nil {
					return err
				}
			}
			onEnd(wq.tx, func(committed bool) {
				if !committed {
					return
				}
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
			if len(out) > 0 {
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
	return contract.PollReply{Commands: out, Target: healthyTarget(), AutoUpdate: ptr(autoUpdateOf(q.host))}, nil
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
	onEnd(q.tx, func(committed bool) {
		if committed {
			q.app.invalidateEnvCache()
			q.app.wakeDispatch()
		}
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
		for _, x := range anys(st["environments"]) {
			hs = append(hs, astr(mapOf(x)["hostId"]))
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
