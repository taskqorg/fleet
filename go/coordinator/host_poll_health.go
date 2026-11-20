package main

// Host poll health (docs/design-system/pages/hosts.md, docs/designs/fleet-delivery-pipeline.md §6). A host whose polls
// fail still moves lastSeenAt (the observation is written before commands are prepared), so it looks healthy while it gets
// no commands and no update target (2026-11-17: 2.5 h of 409s). The coordinator records the failure itself, since the
// executor cannot reach it: the last failed poll (status, reason code, message cut) and its first-failure time live in
// the host's observation as pollError, shown by hosts_list and system_versions. After hostPollProblemAfterMs of failures
// a needs-you problem opens (fixed text: host id and reason code); the next successful poll clears the record and closes it.

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"sync/atomic"

	"fleet/errtrack"
)

const (
	hostPollProblemAfterMs = 2 * 60 * 1000
	hostPollMessageRunes   = 300
	// The record's at (last failure) is rewritten at most this often while nothing else changes, and a gap longer than
	// hostPollRunGapMs between failures starts a new run (a host that stopped polling while it failed).
	hostPollAtEveryMs = 60 * 1000
	hostPollRunGapMs  = 5 * 60 * 1000
)

var pollReasonCode = regexp.MustCompile(`^[a-z0-9_.-]{1,64}$`)

func hostPollCause(host string) string { return "problem:host-poll:" + host }

// pollFailure: what a failed poll answers. ok false: no failure of the host (the host left, or it is removed).
type pollFailure struct {
	status          int
	reason, message string
}

func pollFailureOf(err error) (pollFailure, bool) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return pollFailure{}, false
	}
	code, msg, he := errorAnswer(err) // what the host was answered
	f := pollFailure{status: code, reason: fmt.Sprintf("http_%d", code), message: msg}
	if he == nil && code == 500 {
		f.reason = "internal"
	}
	if r, ok := he.extraReason(); ok && pollReasonCode.MatchString(r) {
		f.reason = r
	}
	if f.status == 410 {
		return pollFailure{}, false // a removed host: the executor exits; nothing to surface
	}
	if r := []rune(f.message); len(r) > hostPollMessageRunes {
		f.message = string(r[:hostPollMessageRunes])
	}
	return f, true
}

// internalPollError: a 4xx the request did not cause (a 409 of a stored row, a 403 of a stored authority), for Bugsink.
// A bad body (400) and a bad credential (401) are the request's; a 5xx is captured by writeError.
func internalPollError(f pollFailure) bool { return f.status > 401 && f.status < 500 }

// extraReason: the error body's reason code, if it has one; a nil error has none.
func (e *HTTPError) extraReason() (string, bool) {
	if e == nil {
		return "", false
	}
	r, ok := e.Extra["reason"].(string)
	return r, ok
}

// pollFailureSeq numbers failed polls across hosts; a host's entry keeps the last one (hostHeartbeat.pollFailures).
var pollFailureSeq atomic.Uint64

// heartbeatOf: the host's cache entry, created when missing; the caller holds pollMu.
func (a *App) heartbeatOf(host string) *hostHeartbeat {
	hb := a.heartbeats[host]
	if hb == nil {
		hb = &hostHeartbeat{}
		a.heartbeats[host] = hb
	}
	return hb
}

// notePollFailure records a failed poll of host (holder: the host's holder, for row scope) and opens the problem once
// the failures have lasted hostPollProblemAfterMs. Its own transactions: the poll's transaction has failed.
func (a *App) notePollFailure(ctx context.Context, host string, holder any, err error) {
	f, ok := pollFailureOf(err)
	if !ok || host == "" {
		return
	}
	ctx = context.WithoutCancel(ctx)
	a.ensureRuntime()
	now := nowMs()
	a.pollMu.Lock()
	hb := a.heartbeatOf(host)
	hb.pollClean = false
	hb.pollFailures = pollFailureSeq.Add(1)
	// The same failure recorded under a minute ago: nothing to write, no transaction (a host retries every few seconds).
	last := hb.pollRecorded
	same := last != nil && last.status == f.status && last.reason == f.reason && last.message == f.message && float64(now)-last.at < hostPollAtEveryMs
	a.pollMu.Unlock()
	if same {
		if float64(now)-last.since >= hostPollProblemAfterMs {
			a.openPollProblem(ctx, host, f.reason)
		}
		return
	}
	var since, at float64
	var changed, observed bool
	werr := runTx(ctx, a, nil, true, "host:"+host, func(wq *Request) error {
		observed, changed = false, false
		o, err := maybe(wq, host, "host-observation")
		if err != nil {
			return err
		}
		if o == nil {
			return nil // never observed: no observation is made up for it (its existence means an observed host)
		}
		observed = true
		prev := mapOf(o["pollError"])
		// A run of failures continues while they keep coming; a record left by a host that stopped polling while it
		// failed (no success cleared it) starts a new run.
		since = float64(now)
		if s, at := num(prev["since"]), num(prev["at"]); s > 0 && float64(now)-at < hostPollRunGapMs {
			since = s
		}
		changed = prev == nil || since != num(prev["since"]) || num(prev["status"]) != float64(f.status) || prev["reason"] != f.reason
		at = float64(now)
		if !changed && prev["message"] == f.message && float64(now)-num(prev["at"]) < hostPollAtEveryMs {
			at = num(prev["at"]) // the stored one: the cache never runs ahead of the record
			return nil           // unchanged and recent: no write, no change row
		}
		o["pollError"] = doc{"status": f.status, "reason": f.reason, "message": f.message, "since": since, "at": float64(now)}
		return put(wq, "host-observation", o)
	})
	if werr == nil && observed {
		a.pollMu.Lock()
		a.heartbeatOf(host).pollRecorded = &pollRecord{f, since, at}
		a.pollMu.Unlock()
	}
	if werr != nil {
		log.Printf("host %s poll failure not recorded: %v", host, werr)
	}
	// One Bugsink event per run of one failure, not one per poll (a host retries every few seconds).
	if internalPollError(f) && changed {
		errtrack.Capture(err, map[string]string{"route": "POST /api/hosts/poll", "host": host, "status": fmt.Sprint(f.status), "reason": f.reason})
	}
	if werr != nil || !observed || float64(now)-since < hostPollProblemAfterMs {
		return
	}
	a.openPollProblem(ctx, host, f.reason)
}

// pollRecord: the failure last written for a host (hostHeartbeat.pollRecorded).
type pollRecord struct {
	pollFailure
	since, at float64
}

// openPollProblem opens the host's poll problem once, naming the reason of the failure that reached the threshold.
func (a *App) openPollProblem(ctx context.Context, host, reason string) {
	a.pollMu.Lock()
	opened := a.heartbeatOf(host).pollProblem
	a.pollMu.Unlock()
	if opened {
		return
	}
	text := fmt.Sprintf("Host %s: its polls fail with %s, so it gets no commands and no update target.", host, reason)
	// Under GLOBAL and the host's lock: a success clears under the host's lock, so the problem opens only while the
	// record it describes is still there, and a success after it closes it. The flag is set only for a problem held.
	held := false
	if err := runTxLocks(ctx, a, nil, true, []string{GLOBAL, "host:" + host}, func(q *Request) error {
		held = false
		o, err := maybe(q, host, "host-observation")
		if err != nil || o == nil || o["pollError"] == nil {
			return err
		}
		if h, err := problemHeld(q, hostPollCause(host)); err != nil || h != nil {
			held = h != nil
			return err
		}
		held = true
		return problemAsk(q, "direct", Problem{Cause: hostPollCause(host), Text: text, Data: doc{"hostId": host, "reason": reason}})
	}); err != nil {
		log.Printf("host %s poll problem: %v", host, err)
		return
	}
	if held {
		a.pollMu.Lock()
		a.heartbeatOf(host).pollProblem = true
		a.pollMu.Unlock()
	}
}

// notePollSuccess clears a recorded failure and closes its problem. Free while the host is known clean; the first
// successful poll after a coordinator start checks the database once.
func (a *App) notePollSuccess(ctx context.Context, host string) {
	if host == "" {
		return
	}
	a.ensureRuntime()
	a.pollMu.Lock()
	hb := a.heartbeatOf(host)
	clean, gen := hb.pollClean, hb.pollFailures
	a.pollMu.Unlock()
	if clean {
		return
	}
	ctx = context.WithoutCancel(ctx)
	// Under the host's lock only, never GLOBAL: polls stay off the global lock (lock_poll_concurrency_test.go). The
	// problem's close is one row update of its own cause. A problem opened before a restart is closed by the first good
	// poll after it; problemClose of no question is a no-op. A failure noted since this poll read its generation is not
	// cleared: that failure counts first, then writes under the same host lock.
	stale := false
	if err := runTx(ctx, a, nil, true, "host:"+host, func(wq *Request) error {
		a.pollMu.Lock()
		stale = a.heartbeatOf(host).pollFailures != gen
		a.pollMu.Unlock()
		if stale {
			return nil
		}
		if err := wq.db().ClearObservationPollError(wq.ctx(), host); err != nil {
			return err
		}
		return problemClose(wq, hostPollCause(host))
	}); err != nil {
		log.Printf("host %s poll failure not cleared: %v", host, err)
		return
	}
	if stale {
		return
	}
	a.pollMu.Lock()
	if hb = a.heartbeatOf(host); hb.pollFailures == gen { // no failure recorded meanwhile
		hb.pollClean, hb.pollProblem, hb.pollRecorded = true, false, nil
	}
	a.pollMu.Unlock()
}
