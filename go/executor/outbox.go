package main

// Outbox delivery: rows are queued by the command loop and flushed on a separate goroutine, so polling and accepting
// commands never waits on coordinator latency.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"fleet/contract"
	"fleet/errtrack"
	"fleet/mcpserve"
)

// requeueUnauthorized: a 401 is a credential condition of the host, not of the row: at start those rows go back to the
// queue.
// requeueUnauthorized also re-queues event rows set aside for their size (see the oversize condition below).
func requeueUnauthorized(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// An event batch refused for its size (400 "Invalid JSON body" before the coordinator said 413) goes again: it is cut
	// to the cap when sent (shrinkEvents).
	oversize := `route='/api/hosts/events' AND (status=413 OR status=400 AND reason LIKE '%Invalid JSON body%' AND length(CAST(body AS BLOB))>` + fmt.Sprint(maxRequestBytes) + `)`
	if _, err := tx.Exec(`INSERT INTO outbox(route,body) SELECT route,body FROM outbox_failed WHERE status=401 OR ` + oversize + ` ORDER BY rowid; DELETE FROM outbox_failed WHERE status=401 OR ` + oversize + `;`); err != nil {
		return err
	}
	return tx.Commit()
}

// Answer kinds: one classification of a coordinator answer, shared by the outbox and the transfer client. Edge is a
// condition of the host's edge (a redirect, a 2xx that is not JSON, or the coordinator's 401), never a bad row.
type answerKind int

const (
	answerOK answerKind = iota
	answerEdge
	answerReject
	answerRetry
)

type answer struct {
	kind        answerKind
	value       json.RawMessage
	status      int
	text        string // edge detail, reject body or retry error
	timeout     bool   // the request ran out of time
	unreachable bool   // no connection to the coordinator could be made (dial refused or timed out)
}

type class int

const (
	classOK class = iota
	classEdge
	classRetry
	classReject
)

// statusClass is the one status table for coordinator answers (hostToken) and store answers.
func statusClass(status int, hostToken bool) class {
	switch {
	case status >= 300 && status <= 399:
		return classEdge
	case status == 401 && hostToken:
		return classEdge
	case status == 429 || status >= 500:
		return classRetry
	case status >= 400:
		return classReject
	}
	return classOK
}

// location is a redirect's Location without its query or fragment.
func location(r *http.Response) string {
	l := r.Header.Get("Location")
	if i := strings.IndexAny(l, "?#"); i >= 0 {
		l = l[:i]
	}
	return l
}

// transportError: an exchange that failed after the request was made (no answer, or a 2xx body cut while it is read).
type transportError struct{ error }

func (e transportError) Unwrap() error { return e.error }

// outcome is the one classifier of a failed exchange or transfer step (outbox rows, polls, transfer parts and store
// calls): a transport failure, a network-shaped I/O error or a retryable failure is retried; anything else (a missing
// or unreadable local file, a permanent failure) is rejected.
func outcome(err error) answer {
	var f *failure
	if errors.As(err, &f) {
		return answer{kind: map[bool]answerKind{true: answerRetry, false: answerReject}[f.retry], text: f.msg}
	}
	var ne net.Error
	var op *net.OpError
	var te transportError
	a := answer{kind: answerReject, text: err.Error(), timeout: errors.As(err, &ne) && ne.Timeout(), unreachable: errors.As(err, &op) && op.Op == "dial"}
	if errors.As(err, &te) || errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.ECONNABORTED) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ENOTCONN) || errors.Is(err, syscall.EPIPE) || errors.Is(err, syscall.ETIMEDOUT) ||
		errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) {
		a.kind = answerRetry
	}
	return a
}

// transportAnswer: a request or body read that failed in transport.
func transportAnswer(err error) answer { return outcome(transportError{err}) }

func classify(r *http.Response, err error) answer {
	if err != nil {
		return transportAnswer(err)
	}
	defer r.Body.Close()
	body, rerr := io.ReadAll(r.Body) // drained so the pooled connection is reused
	s := r.StatusCode
	switch statusClass(s, true) {
	case classEdge:
		if s == 401 {
			return answer{kind: answerEdge, status: 401, text: "the host token is not accepted"}
		}
		return answer{kind: answerEdge, status: s, text: location(r)}
	case classOK:
		// A 2xx body cut while it is read (the request deadline, or a heartbeat poll cancelled when its exchange ends) is
		// a transport failure, not an edge answer: its partial JSON is no login or app page. Other statuses keep their
		// class; their body is only the reason text.
		if rerr != nil && !json.Valid(body) {
			a := transportAnswer(rerr)
			a.text = fmt.Sprintf("%d answer cut after %d bytes read: %v", s, len(body), rerr)
			return a
		}
		if !json.Valid(body) {
			return answer{kind: answerEdge, status: s, text: notJSON(r, body)}
		}
		return answer{kind: answerOK, value: json.RawMessage(body), status: s}
	case classRetry:
		return answer{kind: answerRetry, status: s, text: fmt.Sprintf("%d %s", s, body)}
	}
	return answer{kind: answerReject, status: s, text: string(body)}
}

// notJSON describes a complete 2xx answer that is not JSON (an Access login page, the app shell, a proxy page) by its
// content type, length and first bytes (quoted), so the log names what answered.
func notJSON(r *http.Response, body []byte) string {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		ct = "no content type"
	}
	return fmt.Sprintf("an answer that is not JSON: %s, %d bytes: %q", ct, len(body), body[:min(len(body), 120)])
}

// redirected is the poll's edge answer, as an error the exchange loop recognizes.
type redirected struct {
	status int
	detail string
}

func (r redirected) Error() string {
	return fmt.Sprintf("coordinator edge answered %d (%s)", r.status, r.detail)
}

// Backoffs in progress, one map with one backoff(): an edge condition by route (a coordinator route, or "store <host>":
// one log line when a route starts and one when it recovers, and a host problem edge-redirect with every poll until
// then) and a retried outbox row by rowKey.
type hold struct {
	status   int
	detail   string
	attempts int
	nextAt   int64
	firstAt  int64 // the first failed delivery of the row (retryFailed); 0 for held lanes
}

// serverFailLimit: how long a row may fail with a coordinator server error (5xx other than 503) on every attempt
// before it is set aside like a rejected row. A deterministic 500 otherwise holds its lane, the receipts behind it and
// the update drain forever (2026-11-26: task_get's created_task scan failed, and pds18-native's finished run kept its
// completion receipt and 41 event batches in the outbox for two hours).
const serverFailLimit = 30 * 60 * 1000

// serverFailedLong: the row has failed since at least serverFailLimit and the coordinator answered a server error.
func serverFailedLong(n int64, a answer) bool {
	if a.status < 500 || a.status == http.StatusServiceUnavailable || a.unreachable || a.timeout {
		return false
	}
	holdMu.Lock()
	defer holdMu.Unlock()
	h, ok := holds[rowKey(n)]
	return ok && h.firstAt > 0 && nowMs()-h.firstAt >= serverFailLimit
}

var (
	holdMu sync.Mutex
	holds  = map[string]*hold{}
)

// rowKey: an outbox row's backoff key; never a route ("/...") or a store ("store ...").
func rowKey(n int64) string { return "row " + strconv.FormatInt(n, 10) }

func nowMs() int64 { return time.Now().UnixMilli() }

// backoff of transfers and edge-held routes: 2 s doubling to 5 min.
func backoff(attempts int64) int64 {
	if attempts < 0 {
		attempts = 0
	}
	if attempts > 8 {
		attempts = 8
	}
	return min(2000*(int64(1)<<attempts), 300_000)
}

var pollRoute = contract.RoutePoll.Path

func edgeRedirect(route string, status int, detail string) {
	holdMu.Lock()
	defer holdMu.Unlock()
	attempts := 0
	if e, ok := holds[route]; ok {
		// An answer inside the route's backoff (lanes that asked together, a transfer retrying) is the same attempt.
		if route != pollRoute && nowMs() < e.nextAt {
			e.status, e.detail = status, detail
			return
		}
		attempts = e.attempts + 1
	}
	if attempts == 0 {
		fmt.Fprintf(os.Stderr, "executor: the edge answers %s with %d (%s); rows stay queued and are retried\n", route, status, detail)
	}
	wait := int64(2000)
	if route != pollRoute {
		wait = backoff(int64(attempts))
	}
	holds[route] = &hold{status: status, detail: detail, attempts: attempts, nextAt: nowMs() + wait}
}

func edgeOK(route string) {
	holdMu.Lock()
	defer holdMu.Unlock()
	if _, ok := holds[route]; ok {
		delete(holds, route)
		if h, ok := strings.CutPrefix(route, "store "); ok {
			fmt.Fprintf(os.Stderr, "executor: store %s reachable again\n", h)
		} else {
			fmt.Fprintf(os.Stderr, "executor: the edge delivers %s again\n", route)
		}
	}
}

func observe(route string, a answer) {
	switch a.kind {
	case answerEdge:
		edgeRedirect(route, a.status, a.text)
	case answerRetry:
	default:
		edgeOK(route)
	}
}

// edgeClearIdle clears the given routes (and store keys) once no transfer is left to ask them again.
func edgeClearIdle(routes []string) {
	holdMu.Lock()
	defer holdMu.Unlock()
	for r := range holds {
		if strings.HasPrefix(r, "store ") || contains(routes, r) {
			fmt.Fprintf(os.Stderr, "executor: nothing left for %s; edge signal cleared\n", r)
			delete(holds, r)
		}
	}
}

func contains[T comparable](s []T, v T) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func edgeWait(route string) bool {
	holdMu.Lock()
	defer holdMu.Unlock()
	e, ok := holds[route]
	return ok && nowMs() < e.nextAt
}

// edgeProblems is the poll's problems list; the poll's own route is left out.
func edgeProblems(except string) []contract.EdgeProblem {
	holdMu.Lock()
	defer holdMu.Unlock()
	keys := make([]string, 0, len(holds))
	for k := range holds {
		if !strings.HasPrefix(k, "row ") {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	out := []contract.EdgeProblem{}
	for _, route := range keys {
		if route == except {
			continue
		}
		e := holds[route]
		p := contract.EdgeProblem{Kind: contract.ProblemKindEdgeRedirect, Route: route, Status: int64(e.status), Detail: e.detail}
		if e.status >= 300 && e.status < 400 {
			loc := e.detail
			p.Location, p.Detail = &loc, "redirect to "+e.detail
		}
		out = append(out, p)
	}
	return out
}

// outboxClient: heartbeat polls and one-off posts (span batches included) stay responsive with a 5 s deadline.
var outboxClient = httpClient(5 * time.Second)

// pollClient allows 25s coordinator long-polls plus network margin.
var pollClient = httpClient(35 * time.Second)

// deliveryClient delivers journaled outbox rows with the held-poll deadline. A cancelled row is rolled back by the
// coordinator and resent whole, so a deadline shorter than its ingest never lets it through and holds every row
// behind it, the terminal receipt included.
var deliveryClient = httpClient(pollClient.Timeout)

var boardReadDeliveryClient = httpClient(contract.BoardReadMaxWaitSeconds*time.Second + boardReadHTTPMargin)

func outboxDeliveryClient(route, body string) *http.Client {
	if route == contract.RouteAgentTools.Path {
		var envelope contract.AgentToolsRequest
		if json.Unmarshal([]byte(body), &envelope) == nil {
			rpc := mcpserve.Parse(envelope.Request)
			if rpc.Method == "tools/call" && rpc.Params.Name == "board_read" {
				wait := boardReadWaitSeconds(rpc.Params.Name, decRaw(rpc.Params.Arguments))
				if wait > 0 && time.Duration(wait)*time.Second+boardReadHTTPMargin > deliveryClient.Timeout {
					return boardReadDeliveryClient
				}
			}
		}
	}
	return deliveryClient
}

func postJSON(c *http.Client, url, token string, v any) (*http.Response, error) {
	return postJSONCtx(context.Background(), c, url, token, v)
}

func postJSONCtx(ctx context.Context, c *http.Client, url, token string, v any) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewBufferString(enc(v)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	return c.Do(req)
}

func sendWithClient(ctx context.Context, c *http.Client, url, token, reqRoute, metricRoute string, v any) answer {
	a := classify(postJSONCtx(ctx, c, url+reqRoute, token, v))
	observe(metricRoute, a)
	return a
}

func send(url, token, route string, v any) answer {
	return sendWithClient(context.Background(), outboxClient, url, token, route, route, v)
}

func sendPoll(ctx context.Context, url, token, reqRoute string, v any) answer {
	return sendWithClient(ctx, pollClient, url, token, reqRoute, pollRoute, v)
}

func post(url, token, route string, v any) (json.RawMessage, error) {
	return answerValue(send(url, token, route, v))
}

func answerValue(a answer) (json.RawMessage, error) {
	switch a.kind {
	case answerOK:
		return a.value, nil
	case answerEdge:
		return nil, redirected{a.status, a.text}
	case answerReject:
		return nil, fmt.Errorf("coordinator %d %s", a.status, a.text)
	}
	return nil, fmt.Errorf("%s", a.text)
}

// queueTo writes one outbox row for host route r (contracts/host-protocol.schema.json routes), its body r's request.
func queueTo[Req, Rep any](db execer, r contract.HostRoute[Req, Rep], v Req) error {
	return queue(db, r.Path, v)
}

// queue: one outbox row for a route chosen at run time (the agent's delegation envelopes); queueTo when it is known.
func queue(db execer, route string, v any) error {
	_, err := db.Exec("INSERT INTO outbox(route,body) VALUES(?,?)", route, enc(v))
	return err
}

var heldOnly atomic.Bool

// Rows that got a retry answer (5xx, 429, timeout), by rowKey: each waits out its own backoff while other rows flow.
func retryWait(n int64) bool {
	holdMu.Lock()
	defer holdMu.Unlock()
	r, ok := holds[rowKey(n)]
	return ok && nowMs() < r.nextAt
}

func retryFailed(n int64) {
	holdMu.Lock()
	defer holdMu.Unlock()
	attempts, first := 0, nowMs()
	if r, ok := holds[rowKey(n)]; ok {
		attempts = r.attempts + 1
		if r.firstAt > 0 {
			first = r.firstAt
		}
	}
	holds[rowKey(n)] = &hold{attempts: attempts, nextAt: nowMs() + backoff(int64(attempts)), firstAt: first}
}

func retryDone(n int64) {
	holdMu.Lock()
	defer holdMu.Unlock()
	delete(holds, rowKey(n))
}

// Outbox lanes: the rows of one execution are one lane, delivered in rowid order; a row without an execution is in
// its route's lane. Lanes are delivered concurrently (one events request carries one execution,
// contracts/host-protocol.schema.json HostEvents), so an agent tool, delegation or project request waits only behind
// its own execution's earlier rows, never behind other executions' event backlog (17 Nov 2026: with 10-20 executions
// per host their event rows interleaved, went one request each, and agent tool calls waited minutes behind them).
type outboxRow struct {
	n                          int64
	route, body, execution, ln string
	loaded                     bool // its body was read: still queued
}

// laneBusy: lanes with a delivery in flight, by journal. A scan skips them; their newer rows go with their next run, in
// order.
type laneID struct {
	db  *sql.DB
	key string
}

var (
	laneMu   sync.Mutex
	laneBusy = map[laneID]bool{}
	// stops, by journal: a lane got unreachable, 429 or 503. Until then every lane, of any pass, holds; after it one
	// request at a time probes the coordinator until one is answered (as the sequential flush did), so a coordinator
	// that asks to slow down is not asked by every lane at once.
	stopMu sync.Mutex
	stops  = map[*sql.DB]*coordinatorStop{}
	// lastSent: when a row was last delivered (Unix ms); the longer interval needs a quiet outbox.
	lastSent atomic.Int64
)

type coordinatorStop struct {
	until   int64
	probing bool
}

func stoppedUntil(db *sql.DB) bool {
	stopMu.Lock()
	defer stopMu.Unlock()
	s := stops[db]
	return s != nil && nowMs() < s.until
}

// mayAsk: whether a lane may send now; after a stop only the one probing request (probe).
func mayAsk(db *sql.DB) (ok, probe bool) {
	stopMu.Lock()
	defer stopMu.Unlock()
	s := stops[db]
	if s == nil {
		return true, false
	}
	if nowMs() < s.until || s.probing {
		return false, false
	}
	s.probing = true
	return true, true
}

// answered records whether the coordinator as a whole took the request (any answer but unreachable, 429 or 503). Only
// the probe's answer lifts a stop: a request sent before the stop says nothing about the coordinator since.
func answered(db *sql.DB, took, probe bool) {
	stopMu.Lock()
	defer stopMu.Unlock()
	if took {
		if probe {
			delete(stops, db)
		}
		return
	}
	if stops[db] != nil && !probe {
		return // a request sent before the stop: the stop and its probe stand
	}
	stops[db] = &coordinatorStop{until: nowMs() + 2000}
}

// probeOpen: a probe whose answer says nothing about the coordinator; the stop stands and the next request probes.
func probeOpen(db *sql.DB, probe bool) {
	stopMu.Lock()
	defer stopMu.Unlock()
	if s := stops[db]; probe && s != nil {
		s.probing = false
	}
}

// outboxPass is one scan's state, shared by the lanes it started.
type outboxPass struct {
	db               *sql.DB
	url, token, host string
	mu               sync.Mutex
	routes           map[string]bool // routes held for this pass
	held, sent       bool
	started          int
	stopErr, failErr error // the coordinator as a whole cannot take rows; a journal failure
	retryErr         error
}

func (p *outboxPass) stopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.stopErr != nil || p.failErr != nil {
		return true
	}
	return stoppedUntil(p.db)
}

func (p *outboxPass) routeHeld(route string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.routes[route]
}

// startFlush scans the outbox and starts every idle lane on its own goroutine. wait returns once they finish: it sets
// the delivery interval (heldOnly) and reports the pass's error. A 4xx row is moved to outbox_failed and reported as an
// executor event; the lane's following rows keep flowing. A row with an edge answer stays queued and holds, for this
// pass, its route and its lane. A row with a 5xx, 429 or timeout stays queued with its own backoff and holds only its
// execution (its route when it has none): one slow row must not stop other executions' events and receipts (16 Nov
// 2026: a cancelled project operation at the head held 435 rows and the update drain). When the coordinator as a whole
// cannot be reached (dial refused or timed out) or answers 429 or 503, every lane holds for 2 s, then one request probes
// it (mayAsk).
func startFlush(db *sql.DB, url, token, host string) (wait func() error, err error) {
	// The scan reads lane keys only (in SQL; a body that is not JSON is in its route's lane) and claims idle lanes under
	// laneMu: a lane run that ends meanwhile releases its lane after the scan, so no claimed lane holds rows its earlier
	// run already sent. Bodies are read for the claimed lanes only.
	laneMu.Lock()
	// A span batch (up to 1000 spans) has no execution: its body is not parsed on every scan.
	rs, err := db.Query(`SELECT rowid,route,CASE WHEN route=? THEN '' WHEN json_valid(body) THEN CASE WHEN json_type(body,'$.executionId')='text'
		THEN json_extract(body,'$.executionId') ELSE '' END ELSE '' END FROM outbox ORDER BY rowid`, contract.RouteSpans.Path)
	if err != nil {
		laneMu.Unlock()
		return nil, err
	}
	lanes := map[string][]outboxRow{}
	var order []string
	empty := true
	for rs.Next() {
		var r outboxRow
		if err := rs.Scan(&r.n, &r.route, &r.execution); err != nil {
			rs.Close()
			laneMu.Unlock()
			return nil, err
		}
		empty = false
		r.ln = "route " + r.route
		if r.execution != "" {
			r.ln = "execution " + r.execution
		}
		if laneBusy[laneID{db, r.ln}] {
			continue
		}
		if _, ok := lanes[r.ln]; !ok {
			order = append(order, r.ln)
		}
		lanes[r.ln] = append(lanes[r.ln], r)
	}
	err = rs.Err()
	rs.Close()
	if err == nil {
		for _, key := range order {
			laneBusy[laneID{db, key}] = true
		}
	}
	laneMu.Unlock()
	if err != nil {
		return nil, err
	}
	p := &outboxPass{db: db, url: url, token: token, host: host, routes: map[string]bool{}}
	var wg sync.WaitGroup
	for _, key := range order {
		k := laneID{db, key}
		p.started++
		wg.Add(1)
		rows := lanes[key]
		errtrack.Go("outbox.lane", func() {
			defer wg.Done()
			defer func() { laneMu.Lock(); delete(laneBusy, k); laneMu.Unlock() }()
			if err := p.lane(rows); err != nil {
				p.mu.Lock()
				if p.failErr == nil {
					p.failErr = err
				}
				p.mu.Unlock()
			}
		})
	}
	return func() error {
		wg.Wait()
		p.mu.Lock()
		defer p.mu.Unlock()
		switch {
		case p.failErr != nil:
			return p.failErr
		case p.stopErr != nil:
			// The coordinator as a whole is unreachable or asks to slow down: the longer interval, unless the stop
			// ended while a slow lane of this pass was still delivering.
			heldOnly.Store(stoppedUntil(db))
			return p.stopErr
		}
		// The longer interval only when nothing moved, no other lane of the journal is still delivering and no row was
		// delivered within that interval; a scan that found only busy lanes sets none.
		if p.started > 0 || empty {
			laneMu.Lock()
			others := false
			for k := range laneBusy {
				others = others || k.db == db
			}
			laneMu.Unlock()
			heldOnly.Store(p.held && !p.sent && !others && nowMs()-lastSent.Load() > 2000)
		}
		return p.retryErr
	}, nil
}

// bodies reads the claimed rows' bodies; only their lane deletes them, so every one is still queued.
func bodies(db *sql.DB, rows []outboxRow) error {
	ids := make([]int64, len(rows))
	at := map[int64]int{}
	for i, r := range rows {
		ids[i], at[r.n] = r.n, i
	}
	rs, err := db.Query("SELECT rowid,body FROM outbox WHERE rowid IN (SELECT value FROM json_each(?))", enc(ids))
	if err != nil {
		return err
	}
	defer rs.Close()
	for rs.Next() {
		var n int64
		var body string
		if err := rs.Scan(&n, &body); err != nil {
			return err
		}
		rows[at[n]].body, rows[at[n]].loaded = body, true
	}
	return rs.Err()
}

// flush delivers the queued rows of every idle lane and returns when they are done.
func flush(db *sql.DB, url, token, host string) error {
	wait, err := startFlush(db, url, token, host)
	if err != nil {
		return err
	}
	return wait()
}

// lane delivers one lane's rows in order until they are sent or one holds the lane. Bodies are read once its head row
// may go, so a held lane does not reread its backlog on every scan.
func (p *outboxPass) lane(rows []outboxRow) error {
	db := p.db
	single, read := false, false
	for i := 0; i < len(rows); {
		if p.stopped() {
			p.mu.Lock()
			p.held = true
			p.mu.Unlock()
			return nil
		}
		r := rows[i]
		// hold ends the lane; route also holds the route for this pass's other lanes.
		hold := func(route bool) {
			p.mu.Lock()
			if route {
				p.routes[r.route] = true
			}
			p.held = true
			p.mu.Unlock()
		}
		if p.routeHeld(r.route) || edgeWait(r.route) {
			hold(true)
			return nil
		}
		if retryWait(r.n) {
			hold(false)
			return nil
		}
		if stoppedUntil(db) {
			hold(false)
			return nil
		}
		if !read {
			if err := bodies(db, rows); err != nil {
				return err
			}
			read, r = true, rows[i]
		}
		if !r.loaded { // left the queue since the scan: nothing to send
			i++
			continue
		}
		value := decM(r.body)
		// Consecutive event rows of the lane go in one request, under the coordinator's body cap.
		batch := []outboxRow{r}
		if r.route == contract.RouteEvents.Path && !single {
			size := len(r.body)
			for i+len(batch) < len(rows) {
				nx := rows[i+len(batch)]
				events, _ := value["events"].([]any)
				if !nx.loaded || nx.route != r.route || nx.execution != r.execution || len(events) >= 500 || size+len(nx.body) > maxRequestBytes {
					break
				}
				size += len(nx.body)
				value["events"] = append(events, jArr(decM(nx.body), "events")...)
				batch = append(batch, nx)
			}
		}
		if r.route == contract.RouteEvents.Path {
			shrinkEvents(value, maxRequestBytes) // one event over the cap is cut, never refused whole (2026-11-25: 1-4 MB events lost)
		}
		ok, probe := mayAsk(db)
		if !ok {
			hold(false)
			return nil
		}
		a := sendWithClient(context.Background(), outboxDeliveryClient(r.route, r.body), p.url, p.token, r.route, r.route, value)
		// A span batch the telemetry store refuses for now (429, 503) holds only its own lane, never the coordinator as a
		// whole; it says nothing about the coordinator either, so a probe it was stays open for the next request.
		busy := a.kind == answerRetry && (a.status == http.StatusTooManyRequests || a.status == http.StatusServiceUnavailable)
		stop := a.kind == answerRetry && a.unreachable || busy && r.route != spansRoute
		if busy && r.route == spansRoute {
			probeOpen(db, probe)
		} else {
			answered(db, !stop, probe)
		}
		serverFailed := a.kind == answerRetry && !stop && len(batch) == 1 && serverFailedLong(r.n, a)
		if serverFailed {
			a.kind = answerReject // set aside below, reported as outbox.failed on its execution
		}
		var response any
		switch a.kind {
		case answerOK:
			response = a.value
		case answerRetry:
			if stop {
				p.mu.Lock()
				if p.stopErr == nil {
					p.stopErr = fmt.Errorf("%s: %s", r.route, a.text)
				}
				p.mu.Unlock()
				return nil
			}
			if len(batch) > 1 && a.timeout {
				single = true // A large idempotent event batch may exceed the transport deadline; let individual rows progress.
				continue
			}
			for _, b := range batch {
				retryFailed(b.n)
			}
			p.mu.Lock()
			if p.retryErr == nil {
				p.retryErr = fmt.Errorf("%s: %s", r.route, a.text)
			}
			p.mu.Unlock()
			hold(false)
			return nil
		case answerEdge:
			hold(true)
			return nil
		case answerReject:
			reason := fmt.Sprintf("coordinator %d %s", a.status, a.text)
			_, deleg := value["delegation"]
			_, key := value["agentToolKey"]
			if deleg || key {
				problem := typedRaw[struct {
					Message string `json:"message"`
				}]([]byte(a.text))
				response = M{"error": "Coordinator rejected delegation", "statusCode": a.status, "message": problem.Message}
				if serverFailed {
					// The waiting agent gets the error; the owner sees the set-aside row on the run (outbox.failed).
					if err := p.poisoned(r, value, a, reason+" (server error on every retry for 30 minutes)"); err != nil {
						return err
					}
				}
			} else if len(batch) > 1 {
				single = true // retry this batch row by row so only the poisoned row is set aside
				continue
			} else {
				if err := p.poisoned(r, value, a, reason); err != nil {
					return err
				}
				retryDone(r.n)
				i++
				continue
			}
		}
		for _, b := range batch {
			retryDone(b.n)
		}
		i += len(batch)
		p.mu.Lock()
		p.sent = true
		p.mu.Unlock()
		lastSent.Store(nowMs())
		k, hasKey := value["agentToolKey"].(string)
		if !hasKey {
			if _, ok := value["delegation"]; ok {
				d := typed[struct {
					CommandId  string `json:"commandId"`
					Delegation struct {
						RequestId string `json:"requestId"`
					} `json:"delegation"`
				}](value) // a delegation row is never batched: its body is value
				k, hasKey = d.CommandId+":"+d.Delegation.RequestId, true
			}
		}
		if hasKey {
			if _, err := db.Exec("UPDATE delegation_requests SET response=? WHERE id=?", enc(response), k); err != nil {
				return err
			}
			wake(db, k)
		}
		for _, b := range batch {
			if _, err := db.Exec("DELETE FROM outbox WHERE rowid=?", b.n); err != nil {
				return err
			}
		}
	}
	return nil
}

// poisoned moves a rejected row to outbox_failed and reports it as an event of its execution.
func (p *outboxPass) poisoned(r outboxRow, value M, a answer, reason string) error {
	now := nowMs()
	tx, err := p.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec("INSERT INTO outbox_failed(route,body,status,reason,failed_at) VALUES(?,?,?,?,?)", r.route, r.body, a.status, reason, now); err != nil {
		tx.Rollback()
		return err
	}
	if _, err := tx.Exec("DELETE FROM outbox WHERE rowid=?", r.n); err != nil {
		tx.Rollback()
		return err
	}
	own := false
	// Only the event types are read: a batch that does not decode otherwise still counts by its types.
	batch := typed[struct {
		Events []struct {
			Type string `json:"type"`
		} `json:"events"`
	}](value)
	for _, e := range batch.Events {
		if e.Type == string(contract.FleetEventTypeOutboxFailed) {
			own = true
		}
	}
	if r.execution != "" && !own {
		// ponytail: seq offset 1e12+rowid keeps clear of harness sequences.
		data := contract.OutboxFailed{Route: r.route, Status: int64(a.status), Reason: reason}
		ev := contract.HostEvent{Id: fmt.Sprintf("outbox-failed-%d", r.n), Seq: 1_000_000_000_000 + r.n, Type: string(contract.FleetEventTypeOutboxFailed),
			Timestamp: now, Data: json.RawMessage(enc(data))}
		if err := queueTo(tx, contract.RouteEvents, contract.HostEvents{HostId: p.host, ExecutionId: r.execution, Events: []contract.HostEvent{ev}}); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// spawnOutbox runs delivery on its own goroutines until the process exits: every 100 ms (2 s while every row is held or
// the coordinator cannot take rows) a scan starts the lanes that are not delivering yet, so a new row of an idle lane
// leaves at once while other lanes are still sending.
func spawnOutbox(db *sql.DB, url, token, host string) {
	errtrack.Go("outbox.scan", func() {
		for {
			wait, err := startFlush(db, url, token, host)
			if err == nil {
				errtrack.Go("outbox.flush", func() { logFlush(wait()) })
			} else {
				logFlush(err)
			}
			time.Sleep(100 * time.Millisecond)
			if heldOnly.Load() {
				time.Sleep(1900 * time.Millisecond)
			}
		}
	})
}

func logFlush(err error) {
	if err != nil {
		// The first failed row's route and answer: a row that keeps failing holds executor update drain.
		fmt.Fprintf(os.Stderr, "executor outbox delivery failed; rows retained, retry pending: %v\n", err)
	}
}
