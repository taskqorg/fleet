package main

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"fleet/contract"
)

// answer is the outcome of one call, ready for the caller and for the spool.
type answer struct {
	status      int
	body        []byte
	retryAfter  string
	outcome     string
	err         string
	key         string
	usage       *int64
	tokens      *contract.CallTokens // a model tool's split (usageFields), with model
	model       string
	disabled    int // HTTP status that disabled `key`, or 0
	attemptedAt int64
	// A provider 403 on this key, or a plan or credit limit (432, 433): the provider's message and status. Recorded, never
	// disabling (one bad body must not take out keys; credits come back with a top-up or the next billing cycle).
	problem       *string
	problemStatus int
	// What a project access call read (the SQL text, the file path): its spool line's detail.
	detail string
}

func refusal(status int, outcome, code, reason string) *answer {
	body, _ := json.Marshal(map[string]string{"error": code, "reason": reason})
	return &answer{status: status, body: body, outcome: outcome, err: code}
}

// body is a provider request body: JSON bytes, or a file body (an upload's multipart) opened afresh for each send.
type body struct {
	data                   []byte
	uncertainOnServerError bool
	// A file body: its reader, length and content type. stall bounds each read of it, answer the wait for the response
	// headers after its last byte.
	open          func() io.Reader
	length        int64
	contentType   string
	stall, answer time.Duration
}

// errStalled: a file body's provider stopped reading it, or did not answer within answerMs of its last byte.
// errUnsent: the timeout came before the provider read any byte of the file body, so it cannot have stored it.
// errNoBody: the request failed (no connection, a reset) before the provider read any byte of the file.
var errStalled, errUnsent, errNoBody = errors.New("provider stalled"), errors.New("provider read nothing"), errors.New("file not sent")

// progress resets the stall timer on each read of a file body, and switches it to the answer wait at its end. Each
// response progress (a first byte, every 1xx) resets it to the stall limit, so a provider that starts answering and
// stops is still cut after stallMs, also when the body ends after that (no answer wait once answering began). Once
// the request returned (done), reads no longer touch the timer.
type progress struct {
	io.Reader
	timer           *time.Timer
	stall, answer   time.Duration
	lock            sync.Mutex
	read, answering bool
	done            bool // the request returned: later reads no longer touch the timer
}

func (p *progress) Read(b []byte) (int, error) {
	p.lock.Lock()
	reader := p.Reader
	p.lock.Unlock()
	n, err := reader.Read(b)
	p.lock.Lock()
	defer p.lock.Unlock()
	p.read = true
	switch {
	case p.done:
	case err == io.EOF && !p.answering:
		p.timer.Reset(p.answer)
	default:
		p.timer.Reset(p.stall) // also while the provider already answers: a body still sent is progress
	}
	return n, err
}

// reopen starts the body again for a transport retry.
func (p *progress) reopen(r io.Reader) {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.Reader = r
	p.read = false // the transport retries only a body it wrote nothing of
}

// answered: the provider sent response bytes; the rest of its headers must follow within the stall limit.
func (p *progress) answered() {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.answering = true
	p.timer.Reset(p.stall)
}

// finish ends the timer's tracking of the body (the request returned) and reports whether any byte of it was read.
func (p *progress) finish() bool {
	p.lock.Lock()
	defer p.lock.Unlock()
	p.done = true
	return p.read
}

// providerURL: the request URL of a tool path; a keyPath provider (Telegram's /bot<key>) carries the key in it.
func providerURL(p provider, value, path string) string {
	if k := deref(p.KeyPath); k != "" {
		return p.BaseUrl + k + value + path
	}
	return p.BaseUrl + path
}

// keyInHeader: the key value a header carries; none when the URL carries it.
func keyInHeader(p provider, value string) string {
	if deref(p.KeyPath) != "" {
		return ""
	}
	return value
}

// send makes one provider request; the timeout covers the answer and, separately, reading its body.
// Cancelling caller ends the request too; it does not undo what the provider already did.
func (g *gateway) send(caller context.Context, method, url, keyHeader, value string, b body, headers map[string]string) (*http.Response, []byte, error) {
	ctx, cancel := context.WithCancel(caller)
	defer cancel()
	var timedOut atomic.Bool
	var reader io.Reader
	var file *progress
	wait := g.config.requestTimeout
	switch {
	case b.open != nil:
		file = &progress{Reader: b.open(), stall: b.stall, answer: b.answer}
		reader, wait = file, b.stall
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
			GotFirstResponseByte: file.answered,
			Got1xxResponse:       func(int, textproto.MIMEHeader) error { file.answered(); return nil }})
	case b.data != nil:
		reader = bytes.NewReader(b.data)
	}
	timer := time.AfterFunc(wait, func() { timedOut.Store(true); cancel() })
	defer timer.Stop()
	if file != nil {
		file.lock.Lock()
		file.timer = timer
		file.lock.Unlock()
	}
	request, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case value == "": // the key rides in the URL path (keyPath): no key header
	case keyHeader != "":
		request.Header.Set(keyHeader, value)
	default:
		request.Header.Set("Authorization", "Bearer "+value)
	}
	request.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	if file != nil {
		// After the tool's headers: the multipart boundary is never replaced.
		request.ContentLength = b.length
		// A kept-alive connection the provider closed before any byte was written is retried by the transport.
		request.GetBody = func() (io.ReadCloser, error) { file.reopen(b.open()); return io.NopCloser(file), nil }
		request.Header.Set("Content-Type", b.contentType)
	}
	response, err := g.client.Do(request)
	if file != nil {
		// Always called: it also stops late body reads from touching the timer re-armed for the reply below.
		sent := file.finish()
		switch {
		case err != nil && timedOut.Load() && !sent:
			return nil, nil, errUnsent
		case err != nil && timedOut.Load():
			return nil, nil, errStalled
		case err != nil && !sent:
			return nil, nil, errNoBody // also when the caller left before the transport read any byte
		}
	}
	if err != nil {
		if timedOut.Load() {
			return nil, nil, context.DeadlineExceeded
		}
		return nil, nil, err
	}
	defer response.Body.Close()
	timer.Reset(g.config.requestTimeout)
	data, err := io.ReadAll(io.LimitReader(response.Body, providerBytes+1))
	if err == nil && len(data) > providerBytes {
		err = errors.New("too large")
	}
	return response, data, errBody{err}.orNil()
}

// errBody marks a failure after the provider answered, so the caller tells it from no answer.
type errBody struct{ error }

func (e errBody) orNil() error {
	if e.error == nil {
		return nil
	}
	return e
}

const (
	defaultCooldownSec      = int64(60)
	defaultQuotaCooldownSec = int64(3600)
)

func hasFailoverStatus(p *contract.GatewayKeyPolicy, status int) bool {
	return p != nil && slices.Contains(p.FailoverStatuses, int64(status))
}

func cooldownDurationSec(p *contract.GatewayKeyPolicy, status int, retryAfterHeader string) int64 {
	cooldownSec := defaultCooldownSec
	quotaCooldownSec := defaultQuotaCooldownSec
	if p != nil {
		if p.CooldownSeconds != nil && *p.CooldownSeconds > 0 {
			cooldownSec = *p.CooldownSeconds
		}
		if p.QuotaCooldownSeconds != nil && *p.QuotaCooldownSeconds > 0 {
			quotaCooldownSec = *p.QuotaCooldownSeconds
		}
	}
	if ra, err := strconv.ParseInt(strings.TrimSpace(retryAfterHeader), 10, 64); err == nil && ra > 0 {
		return max(ra, cooldownSec)
	}
	if status == 432 {
		return quotaCooldownSec
	}
	return cooldownSec
}

func (g *gateway) setCooldown(id string, setAt, until int64) {
	g.cooldownLock.Lock()
	defer g.cooldownLock.Unlock()
	if cur, ok := g.cooldown[id]; ok && cur.until > until {
		return
	}
	g.cooldown[id] = cooldownKey{until: until, setAt: setAt}
}

func (g *gateway) advanceCurrent(name string) {
	g.currentLock.Lock()
	g.current[name]++
	g.currentLock.Unlock()
}

// orderedKeys returns candidate key IDs in the order dictated by c.KeyPolicy, filtering out disabled and
// cooling-down keys. If active non-disabled keys exist but all are cooling down, it returns nil and a positive
// Retry-After duration in seconds.
func (g *gateway) orderedKeys(name string, c connection) ([]string, int64) {
	g.disabledLock.Lock()
	active := make([]contract.GatewayKey, 0, len(c.Keys))
	for _, k := range c.Keys {
		if d, off := g.disabled[k.Id]; k.State == "active" && (!off || k.EnabledAt != nil && *k.EnabledAt > d.at) {
			active = append(active, k)
		}
	}
	g.disabledLock.Unlock()
	if len(active) == 0 {
		return nil, 0
	}

	now := nowMs()
	var earliestUntil int64
	ready := make([]contract.GatewayKey, 0, len(active))
	g.cooldownLock.Lock()
	for _, k := range active {
		if cd, ok := g.cooldown[k.Id]; ok {
			if now >= cd.until || (k.EnabledAt != nil && *k.EnabledAt > cd.setAt) {
				delete(g.cooldown, k.Id)
				ready = append(ready, k)
			} else if earliestUntil == 0 || cd.until < earliestUntil {
				earliestUntil = cd.until
			}
			continue
		}
		ready = append(ready, k)
	}
	g.cooldownLock.Unlock()
	if len(ready) == 0 {
		return nil, max(int64(1), (earliestUntil-now+999)/1000)
	}

	strategy := contract.GatewayKeyStrategySticky
	if c.KeyPolicy != nil && c.KeyPolicy.Strategy != nil && *c.KeyPolicy.Strategy != "" {
		strategy = *c.KeyPolicy.Strategy
	}
	n := len(ready)
	out := make([]string, n)
	switch strategy {
	case contract.GatewayKeyStrategyRoundRobin:
		g.currentLock.Lock()
		start := g.current[name]
		g.current[name] = start + 1
		g.currentLock.Unlock()
		start = ((start % n) + n) % n
		for i := range ready {
			out[i] = ready[(start+i)%n].Id
		}
	case contract.GatewayKeyStrategyLeastUsed:
		g.currentLock.Lock()
		offset := g.current[name]
		g.current[name] = offset + 1
		g.currentLock.Unlock()
		offset = ((offset % n) + n) % n
		type candidate struct {
			id    string
			usage int64
			tie   int
		}
		g.usedLock.Lock()
		cands := make([]candidate, n)
		for i, k := range ready {
			u := g.used[k.Id]
			if k.Usage != nil {
				u += *k.Usage
			}
			cands[i] = candidate{id: k.Id, usage: u, tie: (i - offset + n) % n}
		}
		slices.SortFunc(cands, func(a, b candidate) int {
			if diff := cmp.Compare(a.usage, b.usage); diff != 0 {
				return diff
			}
			return cmp.Compare(a.tie, b.tie)
		})
		g.used[cands[0].id]++
		g.usedLock.Unlock()
		for i, cand := range cands {
			out[i] = cand.id
		}
	default:
		g.currentLock.Lock()
		start := g.current[name]
		g.currentLock.Unlock()
		start = ((start % n) + n) % n
		for i := range ready {
			out[i] = ready[(start+i)%n].Id
		}
	}
	return out, 0
}

func (g *gateway) call(ctx context.Context, name string, c connection, p provider, t tool, path string, b body) *answer {
	keys, retryAfterSec := g.orderedKeys(name, c)
	if len(keys) == 0 {
		if retryAfterSec > 0 {
			a := refusal(503, "error", "all-keys-cooling-down", "all active keys are cooling down; retry later")
			a.retryAfter = strconv.FormatInt(retryAfterSec, 10)
			return a
		}
		return refusal(503, "error", "no-usable-key", "no active key; the holder or the owner must act")
	}
	lastFailure := ""
	for step, id := range keys {
		hasNext := step+1 < len(keys)
		value, ok := g.value(id)
		if !ok {
			lastFailure = "key " + id + " has no value file"
			continue
		}
		attemptedAt := nowMs()
		response, data, err := g.send(ctx, t.Method, providerURL(p, value, path), deref(p.KeyHeader), keyInHeader(p, value), b, t.Headers)
		var cut errBody
		switch {
		case errors.Is(err, errNoBody) && ctx.Err() != nil:
			a := refusal(499, "error", "caller-cancelled", "the caller cancelled the call before the file was sent")
			a.key = id
			return a
		case errors.Is(err, errNoBody):
			a := refusal(502, "error", "provider-unreachable", "no byte of the file reached the provider; nothing was stored")
			a.key = id
			return a
		case err != nil && ctx.Err() != nil:
			// The caller left: no failover, and a request already sent may still have been charged.
			a := refusal(499, "unknown", "caller-cancelled", "the caller cancelled the call")
			a.key = id
			return a
		case errors.Is(err, errUnsent):
			// No byte of the file was read: the provider cannot have stored it.
			a := refusal(504, "error", "provider-stalled", "the provider did not take the file in time; nothing was stored")
			a.key = id
			return a
		case errors.Is(err, errStalled):
			// Part of the file, or all of it, reached the provider: a write may have happened.
			a := refusal(504, "unknown", "provider-stalled", "the provider stopped reading the file or did not answer in time")
			a.key = id
			return a
		case errors.Is(err, context.DeadlineExceeded) && response == nil:
			a := refusal(504, "unknown", "provider-timeout", "the provider did not answer in time")
			a.key = id
			return a
		case errors.As(err, &cut):
			a := refusal(502, "unknown", "provider-body", "the provider reply was cut off, too large or too slow")
			a.key = id
			return a
		case err != nil:
			// No HTTP answer on this key: try the next one only when sending again cannot charge twice.
			var op *net.OpError
			lastFailure = "transport"
			if errors.As(err, &op) && op.Op == "dial" {
				lastFailure = "connect"
			}
			if !t.Idempotent {
				a := refusal(502, "unknown", "provider-unreachable", "no answer; the call is not sent again")
				a.key = id
				return a
			}
			g.advanceCurrent(name)
			continue
		}
		status := response.StatusCode
		if status >= 300 && status <= 399 {
			// Not followed; whether the provider acted before redirecting is not known.
			a := refusal(502, "unknown", "provider-redirect", fmt.Sprintf("the provider answered HTTP %d; the redirect is not followed", status))
			a.key = id
			return a
		}
		if (status == 401 && !isInsufficientScope401(response, data)) || status == 402 {
			if b.open == nil && hasNext && c.KeyPolicy != nil && len(c.KeyPolicy.FailoverStatuses) > 0 {
				g.onDisabled(id, status, attemptedAt)
				g.advanceCurrent(name)
				continue
			}
			a := refusal(502, "error", fmt.Sprintf("key-disabled-%d", status), fmt.Sprintf("the provider refused key %s with HTTP %d; the key is disabled", id, status))
			a.key, a.disabled, a.attemptedAt = id, status, attemptedAt
			return a
		}
		if hasFailoverStatus(c.KeyPolicy, status) {
			cdSec := cooldownDurationSec(c.KeyPolicy, status, response.Header.Get("Retry-After"))
			now := nowMs()
			g.setCooldown(id, now, now+cdSec*1000)
			g.advanceCurrent(name)
			if b.open == nil && hasNext {
				continue
			}
		}
		// Success and every other provider reply, quota and 429 included, reach the caller unchanged, except that a key
		// value the provider echoed is replaced: the caller never sees a key.
		data = bytes.ReplaceAll(data, []byte(value), []byte("[key]"))
		// A text tool's reply (Bugsink's Markdown stacktrace) reaches the caller as {"text": ...}: callers read JSON only.
		if t.TextReply != nil && *t.TextReply && status >= 200 && status <= 299 && !json.Valid(data) {
			data, _ = json.Marshal(map[string]string{"text": strings.ToValidUTF8(string(data), "\ufffd")})
		}
		a := &answer{status: status, body: data, retryAfter: response.Header.Get("Retry-After"), outcome: "ok", key: id, attemptedAt: attemptedAt}
		field := "usage." + t.Unit
		if t.UsageField != nil {
			field = *t.UsageField
		}
		counts := t.UsageWhen == nil || stringAt(data, t.UsageWhen.Field) == t.UsageWhen.Equals
		if f := t.UsageFields; f != nil && counts && status >= 200 && status <= 299 {
			if tokens, model, ok := tokensAt(data, f); ok {
				if total, ok := tokenSum(tokens.Input, tokens.Output, ptrValue(tokens.CacheRead), ptrValue(tokens.CacheWrite)); ok {
					a.usage, a.tokens, a.model = &total, tokens, model
				}
			}
		}
		// Without a split (no usageFields, or a reply they do not read), the usage amount alone: a connection budget
		// still counts a model call whose reply names no model.
		if a.usage == nil && (t.UsageFields == nil || t.UsageField != nil && status >= 200 && status <= 299) {
			if amount, ok := usageAt(data, field); ok && counts {
				a.usage = &amount
			}
		}
		if status < 200 || status > 299 {
			a.outcome, a.err = "error", fmt.Sprintf("provider-%d", status)
		}
		if status >= 500 && (b.uncertainOnServerError || b.open != nil || c.Provider == "anthropic" && t.Path == "/v1/messages") {
			// The provider may have completed the action before reporting a server error.
			a.outcome = "unknown"
		}
		if status == 401 || (status == 403 && (t.ForbiddenIsCall == nil || !*t.ForbiddenIsCall)) || status == 432 || status == 433 {
			// Redact over the whole body first, so a key cut at the 500-character mark cannot leave a prefix behind.
			message := []rune(strings.ReplaceAll(strings.ToValidUTF8(string(data), "\ufffd"), value, "[key]"))
			text := string(message[:min(len(message), 500)])
			a.problem, a.problemStatus = &text, status
		}
		return a
	}
	return refusal(502, "error", "provider-unreachable", "no key reached the provider; last failure: "+lastFailure)
}

func isInsufficientScope401(resp *http.Response, body []byte) bool {
	if resp != nil {
		if www := strings.ToLower(resp.Header.Get("WWW-Authenticate")); strings.Contains(www, "insufficient_scope") {
			return true
		}
	}
	lower := strings.ToLower(string(body))
	return strings.Contains(lower, "insufficient_scope") ||
		strings.Contains(lower, "insufficient scope") ||
		strings.Contains(lower, "requires higher privileges")
}

func (g *gateway) onDisabled(id string, status int, attemptedAt int64) {
	g.disabledLock.Lock()
	defer g.disabledLock.Unlock()
	if g.disabled[id].at > attemptedAt {
		return
	}
	g.disabled[id] = disabledKey{status, attemptedAt}
	var text strings.Builder
	for k, d := range g.disabled {
		fmt.Fprintf(&text, "%s %d %d\n", k, d.status, d.at)
	}
	if err := writeAtomic(filepath.Join(g.config.StateDir, "disabled-keys"), []byte(text.String())); err != nil {
		logf("disabled key %s not persisted: %v", id, err)
	}
	logf("key %s disabled after HTTP %d", id, status)
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	data, ok := body.([]byte)
	if !ok {
		data, _ = json.Marshal(body)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(data)
}

// writeRaw answers file bytes as they are: no redaction, no textReply (a download's stream).
func writeRaw(w http.ResponseWriter, length int64, headers map[string]string) {
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.FormatInt(length, 10))
	w.WriteHeader(200)
	http.NewResponseController(w).Flush() // the headers leave now, not with the first file bytes
}

// request is one /v1/call request after its route, token and tool were resolved.
type request struct {
	id, connection, tool string
	run                  *token
	bearer               string
	c                    connection
	p                    provider
	t                    tool
	offered              bool
	fileClient           bool
	usageKey             string
	phase                *contract.CallPhase
	startedMs            int64
	begun                time.Time
}

// resolve reads the call's connection, provider and tool from the current snapshot.
func (g *gateway) resolve(q *request) {
	s := g.snapshot.Load()
	c, known := s.Connections[q.connection]
	q.c, q.p = c, s.Providers[c.Provider]
	if q.run != nil {
		q.run = s.tokens[q.run.Sha256] // nil once revoked
	}
	if q.tool == "issue_get" && c.Provider == "youtrack" && q.run != nil && (!q.fileClient || !contains(q.run.FileDownloads, q.connection)) {
		if legacy, ok := s.Providers["youtrack-legacy-attachments"]; ok {
			q.p = legacy
		}
	}
	q.t, q.offered = q.p.Tools[q.tool]
	if q.t.CoordinatorRequires != nil {
		q.offered = q.offered && known && q.run != nil && q.run.SystemActor != nil && q.t.ServiceActor != nil &&
			*q.run.SystemActor == *q.t.ServiceActor && q.run.ProjectId != "" && q.run.DirectedBy == "" &&
			contains(c.Tools, *q.t.CoordinatorRequires)
	} else {
		q.offered = q.offered && known && contains(c.Tools, q.tool)
	}
}

// admit refuses a call its token, grant, tool, intake or budget does not allow; nil admits it.
func admit(q *request) *answer {
	switch {
	case q.run == nil:
		return refusal(401, "error", "unauthenticated", "a valid run token is required")
	case !contains(q.run.Connections, q.connection):
		return refusal(403, "error", "not-granted", "this run is not granted the connection")
	case !q.offered:
		return refusal(404, "error", "unknown-tool", "the connection has no such tool")
	case q.c.Intake != "all":
		return refusal(403, "error", "intake-paused", "the connection's intake is paused")
	}
	if scope := blockedScope(q.c, q.run); scope != "" {
		return refusal(403, "error", "budget-reached", "a budget on "+scope+" is reached; ask the owner to raise it")
	}
	return nil
}

// serveJSON answers an admitted call with a JSON body: a JSON tool's provider call, or a download (nil: streamed).
func (g *gateway) serveJSON(w http.ResponseWriter, r *http.Request, q *request, raw []byte, readErr error) *answer {
	t := q.t
	var args map[string]json.RawMessage
	if readErr != nil || json.Unmarshal(raw, &args) != nil || args == nil {
		return refusal(400, "error", "bad-request", "the body must be a JSON object of at most 1 MiB")
	}
	if q.p.Transport != nil {
		return g.serveAccess(w, r.Context(), q, args, r.Header.Get("Accept"))
	}
	if _, supplied := args["skip"]; !supplied && q.c.Provider == "youtrack" && q.tool == "issues_search" {
		args["skip"] = json.RawMessage(`0`)
	}
	if t.UsageOnce != nil {
		var v string
		json.Unmarshal(args[*t.UsageOnce], &v)
		q.usageKey = q.connection + ":" + v
	}
	var path string
	c := q.c
	projectCreate := q.tool == "project_create" && (c.Provider == "youtrack" || c.Provider == "youtrack-legacy-attachments")
	if projectCreate {
		var err error
		if path, err = youtrackProjectPath(t.Path, args); err != nil {
			return refusal(400, "error", "bad-request", err.Error())
		}
		if c.KeyPolicy != nil {
			// Project creation must neither replay after an HTTP reply nor cool down the shared key.
			policy := *c.KeyPolicy
			policy.FailoverStatuses = nil
			c.KeyPolicy = &policy
		}
	} else {
		var ok bool
		if path, ok = fillPath(t, args); !ok {
			return refusal(400, "error", "bad-request", "a path field is missing or not an id")
		}
		if c.Provider == "anthropic" {
			if path, ok = anthropicPath(t.Path, args); !ok {
				return refusal(400, "error", "bad-request", "Anthropic input is invalid or unsupported; only nonstreaming Messages and model pages are supported")
			}
		}
	}
	var sent []byte
	if t.Method != "GET" {
		sent, _ = json.Marshal(args)
	}
	if t.File != nil && *t.File == contract.GatewayFileModeDownload {
		// An old client would read the file bytes as an invalid JSON reply.
		if !strings.Contains(r.Header.Get("Accept"), "application/octet-stream") {
			return refusal(400, "error", "file-expected", "a download tool answers file bytes; send Accept: application/octet-stream")
		}
		return g.serveDownload(w, r, q, path, sent)
	}
	return g.call(r.Context(), q.connection, c, q.p, t, path, body{data: sent, uncertainOnServerError: projectCreate})
}

// line is the spool record of an answered call.
func (g *gateway) line(q *request, a *answer) contract.CallLine {
	line := contract.CallLine{CallId: q.id, Connection: q.connection, Tool: q.tool, Status: int64(a.status),
		Outcome: contract.CallOutcome(a.outcome), StartedAt: q.startedMs, Ms: time.Since(q.begun).Milliseconds()}
	if q.run != nil {
		line.ExecutionId, line.TokenSha256 = &q.run.ExecutionId, &q.run.Sha256
	}
	if a.key != "" {
		line.KeyFingerprint = &a.key
	}
	if a.err != "" {
		line.ErrorCode = &a.err
	}
	if a.detail != "" {
		line.Detail = &a.detail
	}
	if a.tokens != nil {
		line.Tokens, line.Model = a.tokens, &a.model
	}
	if a.usage != nil {
		line.Usage = &contract.CallUsage{Unit: q.t.Unit, Amount: *a.usage}
		if q.usageKey != "" {
			line.UsageKey = &q.usageKey
		}
	}
	if (a.disabled != 0 || a.problem != nil) && a.attemptedAt != 0 {
		line.ProviderAttemptAt = &a.attemptedAt
	}
	if a.disabled != 0 {
		line.Disabled = &contract.CallKeyDisabled{Key: a.key, Status: int64(a.disabled)}
	}
	if a.problem != nil {
		line.KeyProblem = &contract.CallKeyProblem{Key: a.key, Status: int64(a.problemStatus), Message: *a.problem}
	}
	return line
}

// finish spools the call's line, then answers it: a call answered to its caller is already durable.
func (g *gateway) finish(w http.ResponseWriter, q *request, a *answer, transfer *contract.CallTransfer) {
	if a.disabled != 0 {
		g.onDisabled(a.key, a.disabled, a.attemptedAt)
	}
	line := g.line(q, a)
	line.Phase, line.Transfer = q.phase, transfer
	g.spool.append(line)
	replyCall(w, q, a)
}

func replyCall(w http.ResponseWriter, q *request, a *answer) {
	w.Header().Set("X-Fleet-Call-Id", q.id)
	w.Header().Set("X-Fleet-Outcome", a.outcome)
	if a.err != "" {
		w.Header().Set("X-Fleet-Error", a.err)
	}
	if a.retryAfter != "" {
		w.Header().Set("Retry-After", a.retryAfter)
	}
	writeJSON(w, a.status, a.body)
}

// limits are a connection's file limits with their defaults; a byte limit of 0 is no cap.
func limits(c connection) (fileBytes, replyBytes int64, stall, answer time.Duration) {
	stall, answer = defaultStall, defaultAnswer
	if l := c.Limits; l != nil {
		if l.FileBytes != nil {
			fileBytes = *l.FileBytes
		}
		if l.ReplyBytes != nil {
			replyBytes = *l.ReplyBytes
		}
		// More milliseconds than a Duration holds (about 292 years) saturate instead of wrapping negative.
		ms := func(v int64) time.Duration {
			return time.Duration(min(v, int64(math.MaxInt64/time.Millisecond))) * time.Millisecond
		}
		if l.StallMs != nil {
			stall = ms(*l.StallMs)
		}
		if l.AnswerMs != nil {
			answer = ms(*l.AnswerMs)
		}
	}
	return
}

// scanner finds any of its patterns in a stream, also one split across two writes: each write is searched together
// with the last len(longest)-1 bytes before it.
type scanner struct {
	patterns  [][]byte
	keep      int
	tail, buf []byte
	found     bool
}

func newScanner(patterns [][]byte) *scanner {
	s := &scanner{}
	for _, p := range patterns {
		if len(p) > 0 {
			s.patterns = append(s.patterns, p)
			s.keep = max(s.keep, len(p)-1)
		}
	}
	return s
}

func (s *scanner) write(chunk []byte) bool {
	if s.found || len(s.patterns) == 0 {
		return s.found
	}
	// A value split across the join lies within the tail and the chunk's first keep bytes; the rest is in the chunk.
	s.buf = append(append(s.buf[:0], s.tail...), chunk[:min(len(chunk), s.keep)]...)
	for _, p := range s.patterns {
		if bytes.Contains(chunk, p) || bytes.Contains(s.buf, p) {
			s.found = true
			return true
		}
	}
	if len(chunk) >= s.keep {
		s.tail = append(s.tail[:0], chunk[len(chunk)-s.keep:]...)
	} else if s.tail = append(s.tail, chunk...); len(s.tail) > s.keep {
		s.tail = append(s.tail[:0], s.tail[len(s.tail)-s.keep:]...)
	}
	return false
}

var mediaTypeText = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)

// bidiControl: a mark that reorders the text around it, so a shown name could hide its real extension.
func bidiControl(r rune) bool {
	return r == 0x061c || r == 0x200e || r == 0x200f || r >= 0x202a && r <= 0x202e || r >= 0x2066 && r <= 0x2069
}

// fileName: a name that stays inside the multipart header's quotes (no ", no \ escape) and names no path.
func fileName(name string) bool {
	if name == "" || name == "." || name == ".." || strings.TrimSpace(name) == "" || len(name) > 255 || !utf8.ValidString(name) {
		return false
	}
	for _, r := range name {
		if unicode.IsControl(r) || bidiControl(r) || r == '"' || r == '/' || r == '\\' {
			return false
		}
	}
	return true
}

// serveUpload: the request body is the file. It is staged under <state>/files while it is counted, hashed and scanned
// for every key value the gateway holds and the caller's token; only a whole, matching, clean file is sent, as one
// multipart part. Every refusal closes the connection, so an unread body is never taken as the next request.
func (g *gateway) serveUpload(w http.ResponseWriter, r *http.Request, q *request) {
	control, staging := http.NewResponseController(w), false
	refuse := func(a *answer, transfer *contract.CallTransfer) {
		if !staging && r.Header.Get("Expect") == "" {
			// A client that sends at once loses the reason to a reset when unread bytes meet the close: a small,
			// short read first, as for JSON refusals. With Expect: 100-continue the body is never sent.
			control.SetReadDeadline(time.Now().Add(refusedPatience))
			io.CopyN(io.Discard, r.Body, refusedBytes)
		}
		w.Header().Set("Connection", "close")
		g.finish(w, q, a, transfer)
	}
	if a := admit(q); a != nil {
		refuse(a, nil)
		return
	}
	g.transfers.Add(1)
	defer g.transfers.Add(-1)
	fileBytes, _, stall, _ := limits(q.c)
	contentType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.TrimSpace(strings.ToLower(contentType)) != "application/octet-stream" {
		refuse(refusal(400, "error", "file-expected", "an upload tool takes the file bytes as application/octet-stream with X-Fleet-Args"), nil)
		return
	}
	if r.ContentLength < 0 || r.ContentLength == 0 && r.Header.Get("Content-Length") == "" {
		refuse(refusal(411, "error", "length-required", "an upload needs Content-Length"), nil)
		return
	}
	if fileBytes > 0 && r.ContentLength > fileBytes {
		refuse(refusal(413, "error", "file-too-large", fmt.Sprintf("the file is larger than the connection's limit of %d bytes", fileBytes)), nil)
		return
	}
	meta, a := parseUploadMetadata(r, q.t)
	if a != nil {
		refuse(a, nil)
		return
	}

	if !g.reserveUploadSpace(r.ContentLength) {
		refuse(refusal(507, "error", "staging-failed", "insufficient space for this upload"), nil)
		return
	}
	reservation := r.ContentLength
	defer func() {
		if reservation > 0 {
			g.releaseUploadSpace(reservation)
		}
	}()
	part := filepath.Join(g.config.StateDir, "files", q.id+".part")
	file, err := os.OpenFile(part, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		refuse(refusal(507, "error", "staging-failed", "the file could not be staged"), nil)
		return
	}
	defer os.Remove(part)
	defer file.Close()
	// A stop cancels the staging read at once: nothing reached the provider, so the caller may simply retry.
	staging = true
	var drained atomic.Bool
	g.stagingLock.Lock()
	g.staging[q.id] = func() { drained.Store(true); control.SetReadDeadline(time.Now()) }
	if g.draining.Load() {
		drained.Store(true)
	}
	g.stagingLock.Unlock()
	unstage := func() bool {
		g.stagingLock.Lock()
		defer g.stagingLock.Unlock()
		delete(g.staging, q.id)
		return drained.Load() || g.draining.Load()
	}
	draining := refusal(503, "error", "draining", "the gateway is stopping; the file was not sent, try again")
	patterns := g.uploadSecrets(q)
	scan, hash := newScanner(patterns), sha256.New()
	buf := make([]byte, copyBytes)
	var staged int64
	var readErr, writeErr error
	for !drained.Load() {
		control.SetReadDeadline(time.Now().Add(stall))
		if drained.Load() {
			break // a cancel between the check and the deadline: its deadline was overwritten
		}
		n, err := r.Body.Read(buf)
		if n > 0 {
			if _, writeErr = file.Write(buf[:n]); writeErr != nil {
				break
			}
			hash.Write(buf[:n])
			scan.write(buf[:n])
			staged += int64(n)
		}
		if err != nil {
			if err != io.EOF {
				readErr = err
			}
			break
		}
	}
	control.SetReadDeadline(time.Time{})
	g.releaseUploadSpace(reservation)
	reservation = 0
	if unstage() {
		refuse(draining, nil)
		return
	}
	sum := hex.EncodeToString(hash.Sum(nil))
	transfer := &contract.CallTransfer{Direction: contract.CallTransferDirectionUpload, Bytes: staged, MediaType: meta.mediaType, Name: meta.name, Sha256: &sum}
	switch {
	case writeErr != nil:
		logf("staging %s: %v", q.id, writeErr)
		refuse(refusal(507, "error", "staging-failed", "the file could not be staged"), nil)
		return
	case errors.Is(readErr, os.ErrDeadlineExceeded):
		refuse(refusal(408, "error", "stalled", fmt.Sprintf("no file bytes arrived for %s; nothing was sent", stall)), nil)
		return
	case readErr != nil || staged != r.ContentLength:
		refuse(refusal(400, "error", "bad-request", "the file ended before Content-Length; nothing was sent"), nil)
		return
	case scan.found || g.uploadHasSecret(q, meta, file, patterns):
		// No transfer record: its name may be the secret.
		refuse(refusal(422, "error", "secret-found", "the file, its name or media type contains a key value or the caller's token; nothing was sent"), nil)
		return
	case sum != meta.sha:
		refuse(refusal(400, "error", "sha-mismatch", "the received bytes do not match X-Fleet-Sha256; nothing was sent"), transfer)
		return
	}
	g.postUpload(w, r, q, meta, file, transfer, nil)
}

type uploadMetadata struct {
	pathArgs             map[string]json.RawMessage
	name, mediaType, sha string
}

func parseUploadMetadata(r *http.Request, t tool) (uploadMetadata, *answer) {
	var args map[string]json.RawMessage
	encoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(r.Header.Get("X-Fleet-Args"), "="))
	if err != nil || json.Unmarshal(encoded, &args) != nil || args == nil {
		return uploadMetadata{}, refusal(400, "error", "bad-request", "X-Fleet-Args must be base64url JSON")
	}
	var name, mediaType string
	nameErr := json.Unmarshal(args["name"], &name)
	if len(args["mediaType"]) > 0 && json.Unmarshal(args["mediaType"], &mediaType) != nil {
		mediaType = "\n" // not a string: refused below
	}
	delete(args, "name")
	delete(args, "mediaType")
	pathArgs := maps.Clone(args)
	_, ok := fillPath(t, args)
	// fillPath took the path fields; any other argument has no place in a file call.
	ok = ok && len(args) == 0
	mediaType = strings.ToLower(mediaType)
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	want := r.Header.Get("X-Fleet-Sha256")
	switch {
	case !ok || !mediaTypeText.MatchString(mediaType) || !isHex(want, 64) || want != strings.ToLower(want):
		return uploadMetadata{}, refusal(400, "error", "bad-request", "X-Fleet-Args takes the path fields, name and mediaType; X-Fleet-Sha256 is the file's lowercase hex SHA-256")
	case nameErr != nil || !fileName(name):
		return uploadMetadata{}, refusal(400, "error", "bad-file-name", "the file name must be 1 to 255 bytes of UTF-8 without control characters, \", \\ or /")
	}

	return uploadMetadata{pathArgs, name, mediaType, want}, nil
}

func (g *gateway) uploadSecrets(q *request) [][]byte {
	patterns := [][]byte{[]byte(q.bearer)}
	g.values.Range(func(_, v any) bool { patterns = append(patterns, []byte(v.(string))); return true })
	return patterns
}

func (g *gateway) uploadHasSecret(q *request, meta uploadMetadata, file *os.File, scanned [][]byte) bool {
	current := g.uploadSecrets(q)
	return newScanner(current).write([]byte(meta.name+"\n"+meta.mediaType)) || g.newKeyIn(file, scanned, current)
}

// admitUpload reads the current snapshot and validates a staged file against its current limits and tool.
func (g *gateway) admitUpload(q *request, meta uploadMetadata, size int64) (string, *answer) {
	g.resolve(q)
	q.offered = q.offered && q.t.File != nil && *q.t.File == contract.GatewayFileModeUpload
	if a := admit(q); a != nil {
		return "", a
	}
	fileBytes, _, _, _ := limits(q.c)
	if fileBytes > 0 && size > fileBytes {
		return "", refusal(413, "error", "file-too-large", fmt.Sprintf("the file is larger than the connection's limit of %d bytes", fileBytes))
	}
	args := maps.Clone(meta.pathArgs)
	path, ok := fillPath(q.t, args)
	if !ok || len(args) > 0 {
		return "", refusal(400, "error", "bad-request", "the tool's path changed while the file arrived")
	}
	return path, nil
}

type uploadCommitHooks struct {
	ready  func(*request) (undo func() error, refusal *answer)
	reject func(http.ResponseWriter, *request, *answer, *contract.CallTransfer)
}

func (g *gateway) postUpload(w http.ResponseWriter, r *http.Request, q *request, meta uploadMetadata, file *os.File, transfer *contract.CallTransfer, commit *uploadCommitHooks) (keepStage bool) {
	reject := g.finish
	if commit != nil && commit.reject != nil {
		reject = commit.reject
	}
	again := *q
	path, a := g.admitUpload(&again, meta, transfer.Bytes)
	if a != nil {
		reject(w, q, a, transfer)
		return false
	}
	draining := refusal(503, "error", "draining", "the file was not sent")
	if g.draining.Load() || r.Context().Err() != nil {
		if commit == nil {
			g.finish(w, q, draining, transfer)
		} else {
			replyCall(w, q, draining)
		}
		return true
	}
	if commit != nil {
		undo, a := commit.ready(&again)
		if a != nil {
			reject(w, &again, a, transfer)
			return false
		}
		if g.draining.Load() || r.Context().Err() != nil {
			if err := undo(); err != nil {
				replyCall(w, &again, refusal(507, "unknown", "staging-failed", "the commit record could not be restored"))
				return false
			}
			replyCall(w, &again, draining)
			return true
		}
	}
	// Durable commit writes may have outlasted a token revocation, intake pause or size-cap change.
	if commit != nil {
		current := again
		path, a = g.admitUpload(&current, meta, transfer.Bytes)
		if a != nil {
			reject(w, &again, a, transfer)
			return false
		}
		again = current
	}
	_, _, stall, answerWait := limits(again.c)
	// One file part with the caller's name; the name rules keep it inside the quotes (RFC 7578 allows UTF-8).
	boundary := randomHex(16)
	head := "--" + boundary + "\r\nContent-Disposition: form-data; name=\"file\"; filename=\"" + meta.name + "\"\r\nContent-Type: " + meta.mediaType + "\r\n\r\n"
	tail := "\r\n--" + boundary + "--\r\n"
	a = g.call(r.Context(), q.connection, again.c, again.p, again.t, path, body{
		open: func() io.Reader {
			return io.MultiReader(strings.NewReader(head), io.NewSectionReader(file, 0, transfer.Bytes), strings.NewReader(tail))
		},
		length: int64(len(head)) + transfer.Bytes + int64(len(tail)), contentType: "multipart/form-data; boundary=" + boundary,
		stall: stall, answer: answerWait})
	g.finish(w, &again, a, transfer)
	return false
}

// newKeyIn: a key value loaded while the file was staging is in the staged file.
func (g *gateway) newKeyIn(file *os.File, before, now [][]byte) bool {
	seen, added := map[string]bool{}, [][]byte{}
	for _, p := range before {
		seen[string(p)] = true
	}
	for _, p := range now {
		if !seen[string(p)] {
			added = append(added, p)
		}
	}
	if len(added) == 0 {
		return false
	}
	scan, buf := newScanner(added), make([]byte, copyBytes)
	for offset := int64(0); ; {
		n, err := file.ReadAt(buf, offset)
		if n > 0 && scan.write(buf[:n]) {
			return true
		}
		offset += int64(n)
		if err != nil {
			return false
		}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

type downloadHeaderMeta struct {
	Bytes     int64  `json:"bytes"`
	MediaType string `json:"mediaType"`
	Name      string `json:"name"`
}

// serveDownload: a JSON lookup with the key names the file (fileFrom fields); the file itself is fetched from the
// provider's relative URL without the key and streamed to the caller unchanged. A started spool line is durable before
// the response headers leave, a completed line follows the stream. nil: the call was answered here; else the answer
// of a call refused or failed before any byte was sent.
func (g *gateway) serveDownload(w http.ResponseWriter, r *http.Request, q *request, path string, sent []byte) *answer {
	g.transfers.Add(1)
	defer g.transfers.Add(-1)
	_, replyBytes, stall, answerWait := limits(q.c)
	lookup := g.call(r.Context(), q.connection, q.c, q.p, q.t, path, body{data: sent})
	if lookup.outcome != "ok" || lookup.status < 200 || lookup.status > 299 {
		return lookup
	}
	fail := func(status int, outcome, code, reason string) *answer {
		a := refusal(status, outcome, code, reason)
		a.key, a.usage = lookup.key, lookup.usage
		return a
	}
	from := q.t.FileFrom
	rel, name, mediaType := stringAt(lookup.body, from.Url), stringAt(lookup.body, from.Name), stringAt(lookup.body, from.MediaType)
	size, sized := usageAt(lookup.body, from.Bytes)
	sized = sized && size < math.MaxInt64/2 // a size near the int64 limit is not a file
	base, err := url.Parse(q.p.BaseUrl)
	if err != nil {
		return fail(502, "error", "provider-body", "the provider base URL is invalid")
	}
	origin := base.Scheme + "://" + base.Host
	target, err := url.Parse(origin + rel)
	// Only a path on the provider's own host: an absolute or protocol-relative URL would send the caller elsewhere.
	if !sized || !strings.HasPrefix(rel, "/") || strings.HasPrefix(rel, "//") || strings.Contains(rel, "\\") || err != nil || target.Host != base.Host {
		return fail(502, "error", "provider-body", "the lookup reply has no relative file URL and size")
	}
	if replyBytes > 0 && size > replyBytes {
		return fail(413, "error", "reply-too-large", fmt.Sprintf("the file is larger than the connection's limit of %d bytes", replyBytes))
	}
	if mediaType == "" {
		mediaType = "application/octet-stream"
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	var stalled atomic.Bool
	timer := time.AfterFunc(answerWait, func() { stalled.Store(true); cancel() })
	defer timer.Stop()
	fetch, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return fail(502, "error", "provider-body", "the file URL is invalid")
	}
	// Without the key: a signed URL needs none, and identity keeps the bytes and their length as the provider stores them.
	fetch.Header.Set("Accept-Encoding", "identity")
	response, err := g.client.Do(fetch)
	switch {
	case err != nil && stalled.Load():
		return fail(504, "error", "provider-stalled", "the provider did not answer the file request in time")
	case err != nil && r.Context().Err() != nil:
		return fail(499, "error", "caller-cancelled", "the caller cancelled the call")
	case err != nil:
		return fail(502, "error", "provider-unreachable", "the file request got no answer")
	}
	defer response.Body.Close()
	if !timer.Stop() {
		// The answer wait ran out as the headers came: the fetch is cancelled, so no byte of it can be read.
		return fail(504, "error", "provider-stalled", "the provider did not answer the file request in time")
	}
	switch status := response.StatusCode; {
	case status >= 300 && status <= 399:
		return fail(502, "error", "provider-redirect", fmt.Sprintf("the file request answered HTTP %d; the redirect is not followed", status))
	case status < 200 || status > 299:
		return fail(502, "error", fmt.Sprintf("provider-%d", status), fmt.Sprintf("the file request answered HTTP %d", status))
	case response.ContentLength >= 0 && response.ContentLength != size:
		return fail(502, "error", "provider-body", "the file's length differs from the lookup size")
	}
	if size == 0 {
		// A 0-byte answer is whole at once: an empty file is checked before any header leaves.
		var one [1]byte
		timer.Reset(stall)
		k, err := response.Body.Read(one[:])
		for k == 0 && err == nil {
			k, err = response.Body.Read(one[:])
		}
		timer.Stop()
		switch {
		case k > 0:
			return fail(502, "error", "provider-body", "the file is longer than the lookup size")
		case err != io.EOF && stalled.Load():
			return fail(504, "error", "provider-stalled", "the provider stopped sending the file")
		case err != io.EOF:
			return fail(502, "error", "provider-body", "the file request was cut off")
		}
	}
	started := g.line(q, &answer{status: 200, outcome: "unknown", err: "download-unfinished", key: lookup.key, usage: lookup.usage})
	started.Phase = ptr(contract.CallPhaseStarted)
	started.Transfer = &contract.CallTransfer{Direction: contract.CallTransferDirectionDownload, Bytes: size, MediaType: mediaType, Name: name}
	if !g.spool.append(started) {
		// No byte leaves before the call is on record.
		return fail(503, "error", "spool-failed", "the call could not be recorded; the file was not sent")
	}
	meta, _ := json.Marshal(downloadHeaderMeta{Bytes: size, MediaType: mediaType, Name: name})
	writeRaw(w, size, map[string]string{"X-Fleet-Call-Id": q.id, "X-Fleet-Outcome": "ok", "X-Fleet-File": base64.RawURLEncoding.EncodeToString(meta)})
	control := http.NewResponseController(w)
	hash, buf := sha256.New(), make([]byte, copyBytes)
	var delivered int64
	code := ""
	defer control.SetWriteDeadline(time.Time{}) // net/http clears it too; the kept-alive connection never inherits it
	for code == "" && delivered < size {
		timer.Reset(stall)
		// One byte past the file's end shows a longer body; the last bytes leave only once the provider body ends with
		// them, since a caller that has Content-Length bytes takes the file as whole.
		n, err := response.Body.Read(buf[:min(int64(len(buf)), size-delivered+1)])
		longer, last := delivered+int64(n) > size, delivered+int64(n) == size
		if last && err == nil {
			var one [1]byte
			for k := 0; k == 0 && err == nil; {
				k, err = response.Body.Read(one[:])
				longer = k > 0
			}
		}
		timer.Stop() // a slow caller is not a stalled provider
		if longer {
			code = "provider-body"
			break
		}
		if last && err != nil && err != io.EOF {
			n = 0
		}
		if n > 0 {
			control.SetWriteDeadline(time.Now().Add(stall))
			written, werr := w.Write(buf[:n])
			hash.Write(buf[:written])
			delivered += int64(written)
			if werr != nil {
				code = "download-cut"
				if errors.Is(werr, os.ErrDeadlineExceeded) {
					code = "client-stalled"
				}
			}
		}
		switch {
		case code != "" || err == nil:
		case err == io.EOF && delivered == size: // whole, even if the stall timer fired just after the last read
		case stalled.Load():
			code = "provider-stalled"
		default:
			code = "download-cut" // a short provider body, or the caller left
		}
	}
	timer.Stop()
	if code == "" {
		control.SetWriteDeadline(time.Now().Add(stall))
		if err := control.Flush(); err != nil {
			code = "download-cut"
			if errors.Is(err, os.ErrDeadlineExceeded) {
				code = "client-stalled"
			}
		}
	}
	// No usage, disabled key or key problem: those rode on the started line and must not count twice.
	done := g.line(q, &answer{status: 200, outcome: "ok", key: lookup.key})
	done.Phase = ptr(contract.CallPhaseCompleted)
	done.Transfer = &contract.CallTransfer{Direction: contract.CallTransferDirectionDownload, Bytes: delivered, MediaType: mediaType, Name: name}
	if code == "" {
		sum := hex.EncodeToString(hash.Sum(nil))
		done.Transfer.Sha256 = &sum
	} else {
		done.Outcome, done.ErrorCode = contract.CallOutcomeError, &code
	}
	g.spool.append(done)
	if code != "" {
		// The caller sees a cut connection, never a short file that ends cleanly.
		panic(http.ErrAbortHandler)
	}
	return nil
}

// fillPath puts body fields into {field} in the catalogue path (Meshy task id); a used field leaves the body.
// A field after the path's "?" is query-escaped; a field before it is inserted as matched by its pattern.
// The caller never selects a key: api_key is dropped, and catalogue fields (Tavily include_usage) are added.
func fillPath(t tool, body map[string]json.RawMessage) (string, bool) {
	delete(body, "api_key")
	for k, v := range t.Inject {
		body[k] = v
	}
	// The template is read once from the left, so an inserted value is never read as a field, and whether a field is in
	// the query depends on the template's own "?", not on what earlier values contain.
	var out strings.Builder
	rest, query := t.Path, false
	for {
		start := strings.IndexByte(rest, '{')
		if start < 0 {
			out.WriteString(rest)
			return out.String(), true
		}
		query = query || strings.IndexByte(rest[:start], '?') >= 0
		length := strings.IndexByte(rest[start:], '}')
		if length < 0 {
			return "", false
		}
		field := rest[start+1 : start+length]
		var value string
		if json.Unmarshal(body[field], &value) != nil {
			// A whole number (a page offset sent as 50, not "50") is taken as its digits.
			var n json.Number
			if json.Unmarshal(body[field], &n) != nil {
				return "", false
			}
			value = n.String()
		}
		if value == "" {
			return "", false
		}
		if pattern, ok := t.PathPatterns[field]; ok {
			// Patterns compiled when the snapshot loaded; a "." or ".." path segment never reaches the provider.
			if !regexp.MustCompile(pattern).MatchString(value) || !query && dotSegment(value) {
				return "", false
			}
		} else if strings.IndexFunc(value, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 {
			return "", false
		}
		delete(body, field)
		if query {
			// A query value (a YouTrack search) is escaped, so it cannot add or end a parameter.
			value = url.QueryEscape(value)
		}
		out.WriteString(rest[:start])
		out.WriteString(value)
		rest = rest[start+length+1:]
	}
}

var (
	youtrackProjectPrefix = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,30}$`)
	youtrackUserID        = regexp.MustCompile(`^[0-9]+-[0-9]+$`)
)

// youtrackProjectPath validates the create request before moving its optional template to the query.
func youtrackProjectPath(path string, args map[string]json.RawMessage) (string, error) {
	var name, shortName string
	if json.Unmarshal(args["name"], &name) != nil || strings.TrimSpace(name) == "" {
		return "", errors.New("name must be a nonblank string")
	}
	if json.Unmarshal(args["shortName"], &shortName) != nil || !youtrackProjectPrefix.MatchString(shortName) {
		return "", errors.New("shortName must start with a letter and contain at most 31 letters, digits or underscores")
	}
	var leader map[string]string
	if json.Unmarshal(args["leader"], &leader) != nil || len(leader) != 1 || !youtrackUserID.MatchString(leader["id"]) {
		return "", errors.New("leader must contain only id, a YouTrack user database ID")
	}
	for field, raw := range args {
		switch field {
		case "name", "shortName", "leader":
		case "description":
			var description *string
			if json.Unmarshal(raw, &description) != nil || description == nil {
				return "", errors.New("description must be a string")
			}
		case "template":
			var template string
			if json.Unmarshal(raw, &template) != nil || (template != "scrum" && template != "kanban") {
				return "", errors.New("template must be scrum or kanban")
			}
			path += "&template=" + url.QueryEscape(template)
		default:
			return "", errors.New("project_create accepts only name, shortName, leader, description and template")
		}
	}
	delete(args, "template")
	return path, nil
}

// Anthropic's fixed native routes need no caller-selected URL or headers.
func anthropicPath(path string, body map[string]json.RawMessage) (string, bool) {
	if path == "/v1/models" {
		query := url.Values{}
		for field, raw := range body {
			switch field {
			case "limit":
				var n int64
				if json.Unmarshal(raw, &n) != nil || n < 1 || n > 1000 {
					return "", false
				}
				query.Set(field, strconv.FormatInt(n, 10))
			case "after_id", "before_id":
				var value string
				if json.Unmarshal(raw, &value) != nil || value == "" {
					return "", false
				}
				query.Set(field, value)
			default:
				return "", false
			}
		}
		if len(query) != 0 {
			path += "?" + query.Encode()
		}
		return path, true
	}
	if path != "/v1/messages" {
		return "", false
	}
	for field, raw := range body {
		switch field {
		case "model", "max_tokens", "messages", "system", "metadata", "stop_sequences", "tools", "tool_choice", "thinking", "output_config", "cache_control", "service_tier":
			// The provider validates native content and model-specific options.
		case "stream":
			if string(raw) != "false" {
				return "", false
			}
		default:
			return "", false
		}
	}
	var model string
	var maxTokens int64
	var messages []json.RawMessage
	if json.Unmarshal(body["model"], &model) != nil || model == "" ||
		json.Unmarshal(body["max_tokens"], &maxTokens) != nil || maxTokens < 1 ||
		json.Unmarshal(body["messages"], &messages) != nil || len(messages) == 0 {
		return "", false
	}
	return path, true
}

// tokensAt reads a model reply's token split and model by its usageFields: input without cache reads, output with
// reasoning, as an agent run's totals count them. Input, output and model are required; an optional count that is
// absent or null is none.
func tokensAt(data []byte, f *contract.GatewayUsageFields) (*contract.CallTokens, string, bool) {
	model := stringAt(data, f.Model)
	input, inOK := usageAt(data, f.Input)
	output, outOK := usageAt(data, f.Output)
	if model == "" || !inOK || !outOK {
		return nil, "", false
	}
	optional := func(field *string) (*int64, bool) {
		if field == nil {
			return nil, true
		}
		if raw, ok := jsonAt(data, *field); !ok || string(raw) == "null" {
			return nil, true
		}
		n, ok := usageAt(data, *field)
		return &n, ok
	}
	cacheRead, ok1 := optional(f.CacheRead)
	cacheWrite, ok2 := optional(f.CacheWrite)
	reasoning, ok3 := optional(f.Reasoning)
	extra, ok4 := optional(f.InputExtra)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return nil, "", false
	}
	if extra != nil {
		var ok bool
		if input, ok = tokenSum(input, *extra); !ok {
			return nil, "", false
		}
	}
	if f.InputHasCacheRead != nil && *f.InputHasCacheRead && cacheRead != nil {
		if *cacheRead > input {
			return nil, "", false
		}
		input -= *cacheRead
	}
	if reasoning != nil && (f.OutputHasReasoning == nil || !*f.OutputHasReasoning) {
		var ok bool
		if output, ok = tokenSum(output, *reasoning); !ok {
			return nil, "", false
		}
	}
	return &contract.CallTokens{Input: input, Output: output, CacheRead: cacheRead, CacheWrite: cacheWrite, Reasoning: reasoning}, model, true
}

// tokenSum adds non-negative counts; false when the sum does not fit.
func tokenSum(ns ...int64) (int64, bool) {
	var total int64
	for _, n := range ns {
		if n > math.MaxInt64-total {
			return 0, false
		}
		total += n
	}
	return total, true
}

func ptrValue(n *int64) int64 {
	if n == nil {
		return 0
	}
	return *n
}

func jsonAt(data []byte, field string) (json.RawMessage, bool) {
	raw := json.RawMessage(data)
	for _, f := range strings.Split(field, ".") {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return nil, false
		}
		var ok bool
		if raw, ok = m[f]; !ok {
			return nil, false
		}
	}
	return raw, true
}

func floatAt(data []byte, field string) (float64, bool) {
	raw, ok := jsonAt(data, field)
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return 0, false
	}
	var n float64
	if json.Unmarshal(raw, &n) != nil {
		return 0, false
	}
	return n, true
}

// usageAt reads a non-negative integer at a dotted field of a JSON reply.
func usageAt(data []byte, field string) (int64, bool) {
	n, ok := floatAt(data, field)
	if !ok || n < 0 || n != float64(int64(n)) {
		return 0, false
	}
	return int64(n), true
}

func dotSegment(value string) bool {
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return true
		}
	}
	return false
}

// stringAt reads a string at a dotted field of a JSON reply ("" when absent).
func stringAt(data []byte, field string) string {
	raw, ok := jsonAt(data, field)
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}
