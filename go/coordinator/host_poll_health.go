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
	a.pollMu.Lock()
	hb := a.heartbeatOf(host)
	hb.pollClean = false
	hb.pollFailures = pollFailureSeq.Add(1)
	a.pollMu.Unlock()
	now := nowMs()
	var since float64
	var changed bool
	werr := runTx(ctx, a, nil, true, "host:"+host, func(wq *Request) error {
		o, err := maybe(wq, host, "host-observation")
		if err != nil {
			return err
		}
		if o == nil {
			o = doc{"id": host, "holder": holder}
		}
		prev := mapOf(o["pollError"])
		since = float64(now)
		if s := num(prev["since"]); s > 0 {
			since = s
		}
		changed = prev == nil || num(prev["status"]) != float64(f.status) || prev["reason"] != f.reason
		if !changed && prev["message"] == f.message {
			return nil // unchanged: no write, no change row
		}
		o["pollError"] = doc{"status": f.status, "reason": f.reason, "message": f.message, "since": since}
		return put(wq, "host-observation", o)
	})
	if werr != nil {
		log.Printf("host %s poll failure not recorded: %v", host, werr)
	}
	// One Bugsink event per run of one failure, not one per poll (a host retries every few seconds).
	if internalPollError(f) && changed {
		errtrack.Capture(err, map[string]string{"route": "POST /api/hosts/poll", "host": host, "status": fmt.Sprint(f.status), "reason": f.reason})
	}
	if werr != nil {
		return
	}
	if float64(now)-since < hostPollProblemAfterMs {
		return
	}
	a.pollMu.Lock()
	hb = a.heartbeatOf(host)
	opened := hb.pollProblem
	hb.pollProblem = true
	a.pollMu.Unlock()
	if opened {
		return
	}
	text := fmt.Sprintf("Host %s: its polls fail with %s, so it gets no commands and no update target.", host, f.reason)
	// Under GLOBAL and the host's lock: a success clears under the host's lock, so the problem opens only while the
	// record it describes is still there, and a success after it closes it.
	if err := runTxLocks(ctx, a, nil, true, []string{GLOBAL, "host:" + host}, func(q *Request) error {
		o, err := maybe(q, host, "host-observation")
		if err != nil || o == nil || o["pollError"] == nil {
			return err
		}
		if h, err := problemHeld(q, hostPollCause(host)); err != nil || h != nil {
			return err
		}
		return problemAsk(q, "direct", Problem{Cause: hostPollCause(host), Text: text, Data: doc{"hostId": host, "reason": f.reason}})
	}); err != nil {
		a.pollMu.Lock()
		a.heartbeatOf(host).pollProblem = false
		a.pollMu.Unlock()
		log.Printf("host %s poll problem: %v", host, err)
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
		if err := exec(wq, "UPDATE document SET data=data-'pollError',updated_at=now() WHERE kind='host-observation' AND id=$1 AND data ? 'pollError'", host); err != nil {
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
		hb.pollClean, hb.pollProblem = true, false
	}
	a.pollMu.Unlock()
}
