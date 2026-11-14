package main

// Outbox delivery: rows are queued by the command loop and flushed on a separate goroutine, so polling and accepting
// commands never waits on coordinator latency.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fleet/contract"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// requeueUnauthorized: a 401 is a credential condition of the host, not of the row: at start those rows go back to the
// queue.
func requeueUnauthorized(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO outbox(route,body) SELECT route,body FROM outbox_failed WHERE status=401 ORDER BY rowid; DELETE FROM outbox_failed WHERE status=401;`); err != nil {
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
	kind   answerKind
	value  json.RawMessage
	status int
	text   string // edge detail, reject body or retry error
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

func classify(r *http.Response, err error) answer {
	if err != nil {
		return answer{kind: answerRetry, text: err.Error()}
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body) // drained so the pooled connection is reused
	s := r.StatusCode
	switch statusClass(s, true) {
	case classEdge:
		if s == 401 {
			return answer{kind: answerEdge, status: 401, text: "the host token is not accepted"}
		}
		return answer{kind: answerEdge, status: s, text: location(r)}
	case classOK:
		if !json.Valid(body) {
			return answer{kind: answerEdge, status: s, text: "an answer that is not JSON"}
		}
		return answer{kind: answerOK, value: json.RawMessage(body), status: s}
	case classRetry:
		return answer{kind: answerRetry, status: s, text: fmt.Sprintf("%d %s", s, body)}
	}
	return answer{kind: answerReject, status: s, text: string(body)}
}

// redirected is the poll's edge answer, as an error the exchange loop recognizes.
type redirected struct {
	status int
	detail string
}

func (r redirected) Error() string {
	return fmt.Sprintf("coordinator edge answered %d (%s)", r.status, r.detail)
}

// Edge conditions in progress, by route (a coordinator route, or "store <host>"): one log line when a route starts and one
// when it recovers, and a host problem edge-redirect with every poll until then.
type edgeState struct {
	status   int
	detail   string
	attempts int
	nextAt   int64
}

var (
	edgeMu sync.Mutex
	edges  = map[string]*edgeState{}
)

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

const pollRoute = "/api/hosts/poll"

func edgeRedirect(route string, status int, detail string) {
	edgeMu.Lock()
	defer edgeMu.Unlock()
	attempts := 0
	if e, ok := edges[route]; ok {
		attempts = e.attempts + 1
	}
	if attempts == 0 {
		fmt.Fprintf(os.Stderr, "executor: the edge answers %s with %d (%s); rows stay queued and are retried\n", route, status, detail)
	}
	wait := int64(2000)
	if route != pollRoute {
		wait = backoff(int64(attempts))
	}
	edges[route] = &edgeState{status, detail, attempts, nowMs() + wait}
}

func edgeOK(route string) {
	edgeMu.Lock()
	defer edgeMu.Unlock()
	if _, ok := edges[route]; ok {
		delete(edges, route)
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
	edgeMu.Lock()
	defer edgeMu.Unlock()
	for r := range edges {
		if strings.HasPrefix(r, "store ") || contains(routes, r) {
			fmt.Fprintf(os.Stderr, "executor: nothing left for %s; edge signal cleared\n", r)
			delete(edges, r)
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
	edgeMu.Lock()
	defer edgeMu.Unlock()
	e, ok := edges[route]
	return ok && nowMs() < e.nextAt
}

// edgeProblems is the poll's problems list; the poll's own route is left out.
func edgeProblems(except string) []contract.EdgeProblem {
	edgeMu.Lock()
	defer edgeMu.Unlock()
	keys := make([]string, 0, len(edges))
	for k := range edges {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := []contract.EdgeProblem{}
	for _, route := range keys {
		if route == except {
			continue
		}
		e := edges[route]
		p := contract.EdgeProblem{Kind: contract.ProblemKindEdgeRedirect, Route: route, Status: int64(e.status), Detail: e.detail}
		if e.status >= 300 && e.status < 400 {
			loc := e.detail
			p.Location, p.Detail = &loc, "redirect to "+e.detail
		}
		out = append(out, p)
	}
	return out
}

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

// One pooled client: keep-alive avoids a TLS handshake per row; redirects are not followed.
var outboxClient = &http.Client{Timeout: 5 * time.Second, CheckRedirect: noRedirect}

// pollClient allows 25s coordinator long-polls plus network margin while preserving outboxClient's 5s timeout for heartbeats and receipts.
var pollClient = &http.Client{Timeout: 35 * time.Second, CheckRedirect: noRedirect}

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

func queue(db execer, route string, v any) error {
	_, err := db.Exec("INSERT INTO outbox(route,body) VALUES(?,?)", route, enc(v))
	return err
}

var heldOnly atomic.Bool

// flush delivers queued rows in order. A 4xx row is moved to outbox_failed and reported as an executor event; the
// following rows keep flowing. A row with an edge answer stays queued and holds, for this flush, its route and its
// execution. 5xx and network errors stop the flush and retry later.
func flush(db *sql.DB, url, token, host string) error {
	type row struct {
		n           int64
		route, body string
	}
	rs, err := db.Query("SELECT rowid,route,body FROM outbox ORDER BY rowid")
	if err != nil {
		return err
	}
	var rows []row
	for rs.Next() {
		var r row
		if err := rs.Scan(&r.n, &r.route, &r.body); err != nil {
			rs.Close()
			return err
		}
		rows = append(rows, r)
	}
	rs.Close()
	routes, executions := map[string]bool{}, map[string]bool{}
	held, sent, single := false, false, false
	for i := 0; i < len(rows); {
		r := rows[i]
		value := decM(r.body)
		execution, hasExec := value["executionId"].(string)
		hold := func() {
			routes[r.route] = true
			if hasExec {
				executions[execution] = true
			}
			held = true
			i++
		}
		// A row behind a held execution waits without holding its route.
		if hasExec && executions[execution] {
			i++
			continue
		}
		if routes[r.route] || edgeWait(r.route) {
			hold()
			continue
		}
		// Consecutive event rows of one execution go in one request.
		batch := []row{r}
		if r.route == "/api/hosts/events" && !single {
			for i+len(batch) < len(rows) {
				nx := rows[i+len(batch)]
				next := decM(nx.body)
				events, _ := value["events"].([]any)
				if nx.route != r.route || !jEq(next["executionId"], value["executionId"]) || len(events) >= 500 {
					break
				}
				value["events"] = append(events, jArr(next, "events")...)
				batch = append(batch, nx)
			}
		}
		a := send(url, token, r.route, value)
		var response any
		switch a.kind {
		case answerOK:
			response = a.value
		case answerRetry:
			if len(batch) > 1 {
				single = true // A large idempotent event batch may exceed the transport deadline; let individual rows progress.
				continue
			}
			return fmt.Errorf("%s", a.text)
		case answerEdge:
			hold()
			continue
		case answerReject:
			reason := fmt.Sprintf("coordinator %d %s", a.status, a.text)
			_, deleg := value["delegation"]
			_, key := value["agentToolKey"]
			if deleg || key {
				var problem struct {
					Message string `json:"message"`
				}
				_ = json.Unmarshal([]byte(a.text), &problem)
				response = M{"error": "Coordinator rejected delegation", "statusCode": a.status, "message": problem.Message}
			} else if len(batch) > 1 {
				single = true // retry this batch row by row so only the poisoned row is set aside
				continue
			} else {
				now := nowMs()
				tx, err := db.Begin()
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
				var batch struct {
					Events []struct {
						Type string `json:"type"`
					} `json:"events"`
				}
				json.Unmarshal([]byte(enc(value)), &batch)
				for _, e := range batch.Events {
					if e.Type == string(contract.FleetEventTypeOutboxFailed) {
						own = true
					}
				}
				if hasExec && !own {
					// ponytail: seq offset 1e12+rowid keeps clear of harness sequences.
					data := contract.OutboxFailed{Route: r.route, Status: int64(a.status), Reason: reason}
					ev := contract.HostEvent{Id: fmt.Sprintf("outbox-failed-%d", r.n), Seq: 1_000_000_000_000 + r.n, Type: string(contract.FleetEventTypeOutboxFailed),
						Timestamp: now, Data: json.RawMessage(enc(data))}
					if err := queue(tx, "/api/hosts/events", contract.HostEvents{HostId: host, ExecutionId: execution, Events: []contract.HostEvent{ev}}); err != nil {
						tx.Rollback()
						return err
					}
				}
				if err := tx.Commit(); err != nil {
					return err
				}
				i++
				continue
			}
		}
		i += len(batch)
		sent = true
		k, hasKey := value["agentToolKey"].(string)
		if !hasKey {
			if d, ok := value["delegation"]; ok {
				k, hasKey = jStr(value, "commandId")+":"+jStr(d, "requestId"), true
			}
		}
		if hasKey {
			if _, err := db.Exec("UPDATE delegation_requests SET response=? WHERE id=?", enc(response), k); err != nil {
				return err
			}
		}
		for _, b := range batch {
			if _, err := db.Exec("DELETE FROM outbox WHERE rowid=?", b.n); err != nil {
				return err
			}
		}
	}
	heldOnly.Store(held && !sent)
	return nil
}

// spawnOutbox runs delivery on its own goroutine until the process exits.
func spawnOutbox(db *sql.DB, url, token, host string) {
	go func() {
		for {
			if err := flush(db, url, token, host); err != nil {
				fmt.Fprintln(os.Stderr, "executor outbox delivery failed; rows retained, retry pending")
			}
			d := 100 * time.Millisecond
			if heldOnly.Load() {
				d = 2 * time.Second
			}
			time.Sleep(d)
		}
	}()
}
