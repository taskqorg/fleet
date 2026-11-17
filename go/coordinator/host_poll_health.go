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
	f := pollFailure{status: 500, reason: "internal", message: err.Error()}
	var he *HTTPError
	if errors.As(err, &he) {
		f.status, f.reason = he.Code, fmt.Sprintf("http_%d", he.Code)
		if r, ok := he.Extra["reason"].(string); ok && pollReasonCode.MatchString(r) {
			f.reason = r
		}
	} else if pgCode(err) == "42501" {
		f.status, f.reason = 403, "http_403"
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
// A bad body (400) is the request's; a 5xx is captured by writeError; a bad credential never reaches the handler.
func internalPollError(f pollFailure) bool { return f.status > 400 && f.status < 500 }

func (a *App) pollHealth(host string) *hostHeartbeat {
	a.ensureRuntime()
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
	if internalPollError(f) {
		errtrack.Capture(err, map[string]string{"route": "POST /api/hosts/poll", "host": host, "status": fmt.Sprint(f.status), "reason": f.reason})
	}
	ctx = context.WithoutCancel(ctx)
	a.pollMu.Lock()
	hb := a.pollHealth(host)
	hb.pollClean = false
	hb.pollFailures++
	a.pollMu.Unlock()
	now := nowMs()
	var since float64
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
		if prev != nil && num(prev["status"]) == float64(f.status) && prev["reason"] == f.reason && prev["message"] == f.message {
			return nil // unchanged: no write, no change row
		}
		o["pollError"] = doc{"status": f.status, "reason": f.reason, "message": f.message, "since": since}
		return put(wq, "host-observation", o)
	})
	if werr != nil {
		log.Printf("host %s poll failure not recorded: %v", host, werr)
		return
	}
	if float64(now)-since < hostPollProblemAfterMs {
		return
	}
	a.pollMu.Lock()
	hb = a.pollHealth(host)
	opened := hb.pollProblem
	hb.pollProblem = true
	a.pollMu.Unlock()
	if opened {
		return
	}
	text := fmt.Sprintf("Host %s: its polls fail with %s, so it gets no commands and no update target.", host, f.reason)
	if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
		if h, err := problemHeld(q, hostPollCause(host)); err != nil || h != nil {
			return err
		}
		return problemAsk(q, "direct", Problem{Cause: hostPollCause(host), Text: text, Data: doc{"hostId": host, "reason": f.reason}})
	}); err != nil {
		a.pollMu.Lock()
		a.pollHealth(host).pollProblem = false
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
	a.pollMu.Lock()
	hb := a.pollHealth(host)
	clean, gen := hb.pollClean, hb.pollFailures
	a.pollMu.Unlock()
	if clean {
		return
	}
	ctx = context.WithoutCancel(ctx)
	// Under the host's lock only, never GLOBAL: polls stay off the global lock (lock_poll_concurrency_test.go). The
	// problem's close is one row update of its own cause. A problem opened before a restart is closed by the first good
	// poll after it; problemClose of no question is a no-op.
	if err := runTx(ctx, a, nil, true, "host:"+host, func(wq *Request) error {
		if err := exec(wq, "UPDATE document SET data=data-'pollError',updated_at=now() WHERE kind='host-observation' AND id=$1 AND data ? 'pollError'", host); err != nil {
			return err
		}
		return problemClose(wq, hostPollCause(host))
	}); err != nil {
		log.Printf("host %s poll failure not cleared: %v", host, err)
		return
	}
	a.pollMu.Lock()
	if hb = a.pollHealth(host); hb.pollFailures == gen { // no failure recorded meanwhile
		hb.pollClean, hb.pollProblem = true, false
	}
	a.pollMu.Unlock()
}
