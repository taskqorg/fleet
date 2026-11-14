package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"fleet/contract"
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
	longRoute("POST /api/hosts/poll", func(q *Request) (any, error) { return q.app.poll(q) })
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
	hb := a.heartbeats[hostID]
	if hb == nil {
		hb = &hostHeartbeat{}
		a.heartbeats[hostID] = hb
	}
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
	hb := a.heartbeats[hostID]
	if hb == nil {
		hb = &hostHeartbeat{}
		a.heartbeats[hostID] = hb
	}
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

func (a *App) pollReadyCommands(ctx context.Context, host string, caps []string) ([]doc, error) {
	var hasPending bool
	var removed bool
	if err := runTx(ctx, a, nil, false, "", func(rq *Request) error {
		if h, err := maybe(rq, "host:"+host, "host"); err != nil {
			return err
		} else if h != nil && hostRemoved(h) {
			removed = true
			return nil
		}
		var cnt int
		if err := rq.tx.QueryRow(rq.ctx(), "SELECT count(*) FROM document WHERE kind='command' AND data->>'hostId'=$1 AND data->>'status'='pending'", host).Scan(&cnt); err != nil {
			return err
		}
		hasPending = cnt > 0
		return nil
	}); err != nil {
		return nil, err
	}
	if removed {
		return nil, fail(410, "host-removed")
	}
	if !hasPending {
		return []doc{}, nil
	}
	out := []doc{}
	var hostDoc doc
	err := runTx(ctx, a, nil, true, "host:"+host, func(wq *Request) error {
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
		pending, err := rows(wq, "SELECT data FROM document WHERE kind='command' AND data->>'hostId'=$1 AND data->>'status'='pending' ORDER BY seq", host)
		if err != nil {
			return err
		}
		out = []doc{}
		for _, c := range pending {
			if astr(mapOf(c["payload"])["workspaceKey"]) != "" && !inList(caps, "workspace-key-v1") {
				continue
			}
			ok, err := agentReady(wq, c)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			e, err := get(wq, c["executionId"], "execution")
			if err != nil {
				return err
			}
			// Held-host trust: work that carries project content goes only to a host whose holder may see the project.
			if k := astr(c["kind"]); k == "start" || k == "script" || k == "delivery-step" {
				if !a.hostMayRun(hostDoc, nonNil(e["projectId"], mapOf(c["payload"])["projectId"]), e["directedBy"]) {
					continue
				}
			}
			if c["kind"] != "stop" {
				ok, err := a.delegationReady(wq, e)
				if err != nil {
					return err
				}
				if !ok {
					continue
				}
			}
			// The ready filter may rewrite the command (delegation inputs): reply with it as stored now.
			fresh, err := get(wq, c["id"], "command")
			if err != nil {
				return err
			}
			out = append(out, fresh)
		}
		return nil
	})
	return out, err
}

func (a *App) poll(q *Request) (any, error) {
	var b contract.PollRequest
	if err := decode(q.body, &b); err != nil {
		return nil, err
	}
	host := hostOf(q)
	a.enterHostPoll(host)
	defer a.exitHostPoll(host)

	open := q.obj() // the open parts (stores, update) as the host's plain JSON
	now := nowMs()
	obs := doc{"id": host, "holder": q.host["holder"]}
	if b.Platform.Valid() {
		obs["platform"] = string(b.Platform)
	}
	if b.Capabilities != nil {
		obs["capabilities"] = b.Capabilities
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
				if hasUpd {
					// Look at update question only when the update result changes.
					if prev, err := get(wq, host, "host-observation"); err != nil && !errors.Is(err, errNotFound) {
						return err
					} else if js(mapOf(prev)["update"]) != js(upd) {
						if err := updateQuestion(wq, host, q.host["holder"], updResult); err != nil {
							return err
						}
					}
				}
				if err := put(wq, "host-observation", obs); err != nil {
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
			return nil, err
		}
	}

	wait := parsePollWait(q.r)
	out := []doc{}
	var err error
	if wait == 0 {
		target := flushGen
		if cur := a.currentDispatchReqGen(); cur > target {
			target = cur
		}
		if target > 0 || !a.dispatchLoopStarted.Load() {
			if err := a.waitDispatchCaughtUp(ctx, target); err != nil {
				return nil, err
			}
		}
		out, err = a.pollReadyCommands(ctx, host, b.Capabilities)
		if err != nil {
			return nil, err
		}
	} else {
		deadline := time.NewTimer(wait)
		defer deadline.Stop()
		for {
			wakeCh := a.hostWakeChan(host)
			out, err = a.pollReadyCommands(ctx, host, b.Capabilities)
			if err != nil {
				return nil, err
			}
			if len(out) > 0 {
				break
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-a.stoppedCh():
				out = []doc{}
				goto replyNow
			case <-deadline.C:
				out, err = a.pollReadyCommands(ctx, host, b.Capabilities)
				if err != nil {
					return nil, err
				}
				goto replyNow
			case <-wakeCh:
			}
		}
	}
replyNow:
	reply := contract.PollReply{Target: healthyTarget(), AutoUpdate: ptr(autoUpdateOf(q.host))}
	return doc{"commands": out, "target": reply.Target, "autoUpdate": reply.AutoUpdate}, nil
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
