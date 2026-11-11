package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fleet/contract"
)

const (
	requestBytes    = 1 << 20
	refusedBytes    = 64 << 10
	refusedPatience = 2 * time.Second
	providerBytes   = 16 << 20
)

func nowMs() int64 { return time.Now().UnixMilli() }

func sha(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func isName(text string) bool {
	if text == "" || len(text) > 64 {
		return false
	}
	for _, c := range []byte(text) {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func isHex(text string, length int) bool {
	if len(text) != length {
		return false
	}
	_, err := hex.DecodeString(text)
	return err == nil
}

func writeAtomic(path string, data []byte) error {
	if err := os.WriteFile(path+".tmp", data, 0o600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// day is the UTC date of a millisecond time, for the spool file name.
func day(ms int64) string { return time.UnixMilli(ms).UTC().Format("2006-01-02") }

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, "fleet-gateway: "+format+"\n", args...) }

// ---------------------------------------------------------------- configuration

type config struct {
	Listen           string `json:"listen"`
	Snapshot         string `json:"snapshot"`
	KeysDir          string `json:"keys_dir"`
	StateDir         string `json:"state_dir"`
	RequestTimeoutMs uint64 `json:"request_timeout_ms"`
	DrainTimeoutMs   uint64 `json:"drain_timeout_ms"`
	requestTimeout   time.Duration
	drainTimeout     time.Duration
	refreshEvery     time.Duration // snapshot poll; tests shorten it
}

func loadConfig(path string) (*config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	c := &config{RequestTimeoutMs: 30_000, DrainTimeoutMs: 35_000}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	for name, value := range map[string]string{"snapshot": c.Snapshot, "keys_dir": c.KeysDir, "state_dir": c.StateDir} {
		if value == "" {
			return nil, fmt.Errorf("config %s is required", name)
		}
	}
	c.requestTimeout = time.Duration(c.RequestTimeoutMs) * time.Millisecond
	c.drainTimeout = time.Duration(c.DrainTimeoutMs) * time.Millisecond
	c.refreshEvery = time.Second
	return c, nil
}

// ---------------------------------------------------------------- snapshot

// The snapshot and call line types are the contract the coordinator writes (contracts/gateway.schema.json).
type (
	tool       = contract.GatewayTool
	provider   = contract.GatewayProvider
	connection = contract.GatewayConnection
)

type token struct {
	contract.GatewayRunToken
	// Budget scopes this run belongs to: person:<email>, project:<id>, experiment:<id>.
	scopes []string
}

type snapshot struct {
	contract.GatewaySnapshot
	revision string
	tokens   map[string]*token
}

func parseSnapshot(data []byte) (*snapshot, error) {
	s := &snapshot{}
	if err := json.Unmarshal(data, s); err != nil {
		return nil, fmt.Errorf("not JSON: %w", err)
	}
	switch {
	case s.Schema != 2:
		return nil, errors.New("unsupported schema")
	case s.GeneratedMs == 0:
		return nil, errors.New("generated_ms is missing")
	case s.Providers == nil:
		return nil, errors.New("providers is missing")
	case s.Connections == nil:
		return nil, errors.New("connections is missing")
	case s.Tokens == nil:
		return nil, errors.New("tokens is missing")
	}
	for name, p := range s.Providers {
		if p.Tools == nil {
			return nil, errors.New("provider tools are missing")
		}
		if p.BaseUrl == "" {
			return nil, errors.New("provider baseUrl is missing")
		}
		p.BaseUrl = strings.TrimRight(p.BaseUrl, "/")
		for toolName, t := range p.Tools {
			if t.Method != "GET" {
				t.Method = "POST"
			}
			if !strings.HasPrefix(t.Path, "/") {
				return nil, errors.New("tool path must start with /")
			}
			for field, pattern := range t.PathPatterns {
				if _, err := regexp.Compile(pattern); err != nil {
					return nil, fmt.Errorf("tool %s path pattern %s: %w", toolName, field, err)
				}
			}
			if t.Unit == "" {
				t.Unit = "calls"
			}
			p.Tools[toolName] = t
		}
		s.Providers[name] = p
	}
	for name, c := range s.Connections {
		if !isName(name) {
			return nil, errors.New("connection name is invalid")
		}
		if c.Keys == nil {
			return nil, errors.New("connection keys are missing")
		}
		for _, k := range c.Keys {
			if !isHex(k.Id, 12) {
				return nil, errors.New("key id is not a fingerprint")
			}
			if k.State == "" {
				return nil, errors.New("key state is missing")
			}
		}
		if _, ok := s.Providers[c.Provider]; !ok {
			return nil, fmt.Errorf("connection %s names an unknown provider", name)
		}
		if c.Intake == "" {
			c.Intake = "all"
			s.Connections[name] = c
		}
	}
	s.tokens = map[string]*token{}
	for _, rt := range s.Tokens {
		t := &token{GatewayRunToken: rt}
		if !isHex(t.Sha256, 64) {
			return nil, errors.New("token hash is not SHA-256")
		}
		if t.ExecutionId == "" {
			return nil, errors.New("token executionId is missing")
		}
		for scope, value := range map[string]string{"person": t.DirectedBy, "project": t.ProjectId, "experiment": t.ExperimentId} {
			if value != "" {
				t.scopes = append(t.scopes, scope+":"+value)
			}
		}
		s.tokens[t.Sha256] = t
	}
	s.revision = sha(string(data))[:12]
	return s, nil
}

func readSnapshot(path string) ([]byte, *snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	s, err := parseSnapshot(data)
	return data, s, err
}

// ---------------------------------------------------------------- spool

// spool keeps one JSON line per call in <state>/spool/<day>.jsonl.
// Durability contract: append returns only after the line is fsynced (and, for a new day file, its directory entry),
// so a call answered to its caller survives a machine crash. A line that cannot be made durable counts in the lost
// counter, persisted in <state>/spool-lost; lostRecorded is false while that counter on disk is behind the real one.
type spool struct {
	dir          string
	lost         atomic.Uint64
	lostRecorded atomic.Bool
	write        sync.Mutex
}

func openSpool(stateDir string) (*spool, error) {
	s := &spool{dir: filepath.Join(stateDir, "spool")}
	s.lostRecorded.Store(true)
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, fmt.Errorf("spool directory: %w", err)
	}
	if text, err := os.ReadFile(filepath.Join(stateDir, "spool-lost")); err == nil {
		lost, _ := strconv.ParseUint(strings.TrimSpace(string(text)), 10, 64)
		s.lost.Store(lost)
	}
	return s, nil
}

func (s *spool) append(line contract.CallLine) {
	s.write.Lock()
	defer s.write.Unlock()
	data, _ := json.Marshal(line)
	if err := s.appendDurably(append(data, '\n')); err != nil {
		lost := s.lost.Add(1)
		logf("spool write failed (%v); lost %d", err, lost)
		path := filepath.Join(filepath.Dir(s.dir), "spool-lost")
		if err := writeAtomic(path, []byte(strconv.FormatUint(lost, 10))); err != nil {
			s.lostRecorded.Store(false)
			logf("lost counter not persisted (%v); %s is behind by at least one line", err, path)
		} else {
			s.lostRecorded.Store(true)
		}
	}
}

func (s *spool) appendDurably(data []byte) error {
	path := filepath.Join(s.dir, day(nowMs())+".jsonl")
	_, statErr := os.Stat(path)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err == nil && statErr != nil {
		// A new file: its name is durable only once the directory is synced.
		var dir *os.File
		if dir, err = os.Open(s.dir); err == nil {
			err = dir.Sync()
			dir.Close()
		}
	}
	return err
}

// ---------------------------------------------------------------- service

// answer is the outcome of one call, ready for the caller and for the spool.
type answer struct {
	status     int
	body       []byte
	retryAfter string
	outcome    string
	err        string
	key        string
	usage      *int64
	disabled   int // HTTP status that disabled `key`, or 0
	// A provider 403 on this key: the provider's message. Recorded, never disabling (one bad body must not take out keys).
	problem *string
}

func refusal(status int, outcome, code, reason string) *answer {
	body, _ := json.Marshal(map[string]string{"error": code, "reason": reason})
	return &answer{status: status, body: body, outcome: outcome, err: code}
}

type disabledKey struct {
	status int
	at     int64
}

type gateway struct {
	config   *config
	client   *http.Client
	snapshot atomic.Pointer[snapshot]
	seenLock sync.Mutex
	seen     os.FileInfo // the snapshot file last read
	reread   sync.Mutex
	rejected atomic.Uint64
	// Key values by fingerprint, read from <keys_dir>/<connection>/<fingerprint> when a snapshot names them.
	values sync.Map
	// Keys the provider refused here, persisted in <state>/disabled-keys.
	disabledLock sync.Mutex
	disabled     map[string]disabledKey
	currentLock  sync.Mutex
	current      map[string]int
	spool        *spool
	draining     atomic.Bool
	started      time.Time
	calls        atomic.Uint64
}

func newGateway(c *config) (*gateway, error) {
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	g := &gateway{config: c, disabled: map[string]disabledKey{}, current: map[string]int{}, started: time.Now(),
		// Redirects are never followed: a 307/308 resends the method and body, which could charge a call twice.
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 64}}}
	if text, err := os.ReadFile(filepath.Join(c.StateDir, "disabled-keys")); err == nil {
		for _, line := range strings.Split(string(text), "\n") {
			var id string
			var d disabledKey
			if n, _ := fmt.Sscanf(line, "%s %d %d", &id, &d.status, &d.at); n == 3 {
				g.disabled[id] = d
			}
		}
	}
	// The coordinator's file when valid, else this gateway's own retained copy of the last valid one.
	good := filepath.Join(c.StateDir, "snapshot.good.json")
	data, s, err := readSnapshot(c.Snapshot)
	if err == nil {
		if err := writeAtomic(good, data); err != nil {
			return nil, fmt.Errorf("retain snapshot: %w", err)
		}
	} else {
		var retained error
		if _, s, retained = readSnapshot(good); retained != nil {
			return nil, fmt.Errorf("no usable snapshot: coordinator: %v; retained: %v", err, retained)
		}
		logf("snapshot refused (%v); serving the retained copy", err)
	}
	if info, err := os.Stat(c.Snapshot); err == nil {
		g.seen = info
	}
	if g.spool, err = openSpool(c.StateDir); err != nil {
		return nil, err
	}
	g.loadValues(s)
	g.snapshot.Store(s)
	return g, nil
}

func (g *gateway) loadValues(s *snapshot) {
	for name, c := range s.Connections {
		for _, k := range c.Keys {
			if _, ok := g.values.Load(k.Id); ok {
				continue
			}
			text, err := os.ReadFile(filepath.Join(g.config.KeysDir, name, k.Id))
			value := strings.TrimSpace(string(text))
			switch {
			case err != nil:
				logf("key %s/%s: %v", name, k.Id, err)
			// A file whose value does not match its fingerprint is never used under that name.
			case sha(value)[:12] != k.Id:
				logf("key file %s/%s does not match its fingerprint", name, k.Id)
			default:
				g.values.Store(k.Id, value)
			}
		}
	}
}

func (g *gateway) value(id string) (string, bool) {
	v, ok := g.values.Load(id)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// refresh adopts a newer valid snapshot; an invalid one is refused and the current one stays.
func (g *gateway) refresh(force bool) {
	info, err := os.Stat(g.config.Snapshot)
	if err != nil {
		return
	}
	g.seenLock.Lock()
	// The coordinator replaces the file by rename, so a new file is a new inode even within one mtime tick.
	if !force && g.seen != nil && os.SameFile(info, g.seen) && info.ModTime().Equal(g.seen.ModTime()) && info.Size() == g.seen.Size() {
		g.seenLock.Unlock()
		return
	}
	g.seen = info
	g.seenLock.Unlock()
	data, s, err := readSnapshot(g.config.Snapshot)
	if err != nil {
		g.rejected.Add(1)
		logf("snapshot refused, keeping the last good one: %v", err)
		return
	}
	if err := writeAtomic(filepath.Join(g.config.StateDir, "snapshot.good.json"), data); err != nil {
		logf("retain snapshot: %v", err)
	}
	g.loadValues(s)
	g.snapshot.Store(s)
}

// authenticate finds the token's run, else rereads the snapshot once (a token minted after the last adoption).
// Rereads are coalesced: a caller that finds one running waits for it instead of starting another.
func (g *gateway) authenticate(bearer string) *token {
	hash := sha(bearer)
	if t := g.snapshot.Load().tokens[hash]; t != nil {
		return t
	}
	if g.reread.TryLock() {
		g.refresh(true)
	} else {
		g.reread.Lock()
	}
	g.reread.Unlock()
	return g.snapshot.Load().tokens[hash]
}

func (g *gateway) usable(c connection) []string {
	g.disabledLock.Lock()
	defer g.disabledLock.Unlock()
	var ids []string
	for _, k := range c.Keys {
		if d, off := g.disabled[k.Id]; k.State == "active" && (!off || k.EnabledAt != nil && *k.EnabledAt > d.at) {
			ids = append(ids, k.Id)
		}
	}
	return ids
}

// send makes one provider request; the timeout covers the answer and, separately, reading its body.
// Cancelling caller ends the request too; it does not undo what the provider already did.
func (g *gateway) send(caller context.Context, method, url, keyHeader, value string, body []byte, headers map[string]string) (*http.Response, []byte, error) {
	ctx, cancel := context.WithCancel(caller)
	defer cancel()
	var timedOut atomic.Bool
	timer := time.AfterFunc(g.config.requestTimeout, func() { timedOut.Store(true); cancel() })
	defer timer.Stop()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, nil, err
	}
	if keyHeader != "" {
		request.Header.Set(keyHeader, value)
	} else {
		request.Header.Set("Authorization", "Bearer "+value)
	}
	request.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	response, err := g.client.Do(request)
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

func (g *gateway) call(ctx context.Context, name string, c connection, p provider, t tool, path string, body []byte) *answer {
	keys := g.usable(c)
	if len(keys) == 0 {
		return refusal(503, "error", "no-usable-key", "no active key; the holder or the owner must act")
	}
	g.currentLock.Lock()
	start := g.current[name]
	g.currentLock.Unlock()
	lastFailure := ""
	for step := range keys {
		index := (start + step) % len(keys)
		id := keys[index]
		value, ok := g.value(id)
		if !ok {
			lastFailure = "key " + id + " has no value file"
			continue
		}
		response, data, err := g.send(ctx, t.Method, p.BaseUrl+path, deref(p.KeyHeader), value, body, t.Headers)
		var cut errBody
		switch {
		case err != nil && ctx.Err() != nil:
			// The caller left: no failover, and a request already sent may still have been charged.
			a := refusal(499, "unknown", "caller-cancelled", "the caller cancelled the call")
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
			g.currentLock.Lock()
			g.current[name] = (index + 1) % len(keys)
			g.currentLock.Unlock()
			continue
		}
		status := response.StatusCode
		if status >= 300 && status <= 399 {
			// Not followed; whether the provider acted before redirecting is not known.
			a := refusal(502, "unknown", "provider-redirect", fmt.Sprintf("the provider answered HTTP %d; the redirect is not followed", status))
			a.key = id
			return a
		}
		if status == 401 || status == 402 {
			a := refusal(502, "error", fmt.Sprintf("key-disabled-%d", status), fmt.Sprintf("the provider refused key %s with HTTP %d; the key is disabled", id, status))
			a.key, a.disabled = id, status
			return a
		}
		// Success and every other provider reply, quota and 429 included, reach the caller unchanged, except that a key
		// value the provider echoed is replaced: the caller never sees a key.
		data = bytes.ReplaceAll(data, []byte(value), []byte("[key]"))
		a := &answer{status: status, body: data, retryAfter: response.Header.Get("Retry-After"), outcome: "ok", key: id}
		field := "usage." + t.Unit
		if t.UsageField != nil {
			field = *t.UsageField
		}
		if amount, ok := usageAt(data, field); ok && (t.UsageWhen == nil || stringAt(data, t.UsageWhen.Field) == t.UsageWhen.Equals) {
			a.usage = &amount
		}
		if status < 200 || status > 299 {
			a.outcome, a.err = "error", fmt.Sprintf("provider-%d", status)
		}
		if status == 403 {
			// Redact over the whole body first, so a key cut at the 500-character mark cannot leave a prefix behind.
			message := []rune(strings.ReplaceAll(strings.ToValidUTF8(string(data), "�"), value, "[key]"))
			text := string(message[:min(len(message), 500)])
			a.problem = &text
		}
		return a
	}
	return refusal(502, "error", "provider-unreachable", "no key reached the provider; last failure: "+lastFailure)
}

// usageReport is the provider's own per-key usage for every key with a value (reconcile, §2); unknown is reported, never guessed.
func (g *gateway) usageReport(ctx context.Context) map[string]any {
	s := g.snapshot.Load()
	rows := []map[string]any{}
	for name, c := range s.Connections {
		p := s.Providers[c.Provider]
		if p.Usage == nil || p.Usage.Path == "" || p.Usage.Field == "" {
			continue
		}
		for _, k := range c.Keys {
			value, ok := g.value(k.Id)
			if k.State == "retired" || !ok {
				continue
			}
			row := map[string]any{"connection": name, "key": k.Id, "usage": nil}
			response, data, err := g.send(ctx, "GET", p.BaseUrl+p.Usage.Path, deref(p.KeyHeader), value, nil, nil)
			switch {
			case errors.Is(err, context.DeadlineExceeded) && response == nil:
				row["unknown"] = "usage request timed out"
			case response == nil:
				row["unknown"] = fmt.Sprintf("usage request: %v", err)
			case response.StatusCode < 200 || response.StatusCode > 299:
				row["unknown"] = fmt.Sprintf("usage HTTP %d", response.StatusCode)
			case err != nil:
				row["unknown"] = "usage body cut off, too large or too slow"
			default:
				var v any
				json.Unmarshal(data, &v)
				for _, f := range strings.Split(p.Usage.Field, ".") {
					m, _ := v.(map[string]any)
					v = m[f]
				}
				if number, ok := v.(float64); ok {
					row["usage"] = number
				} else {
					row["unknown"] = "usage reply has no " + p.Usage.Field
				}
			}
			rows = append(rows, row)
		}
	}
	return map[string]any{"at": nowMs(), "keys": rows}
}

func (g *gateway) onDisabled(id string, status int) {
	g.disabledLock.Lock()
	g.disabled[id] = disabledKey{status, nowMs()}
	var text strings.Builder
	for k, d := range g.disabled {
		fmt.Fprintf(&text, "%s %d %d\n", k, d.status, d.at)
	}
	g.disabledLock.Unlock()
	if err := writeAtomic(filepath.Join(g.config.StateDir, "disabled-keys"), []byte(text.String())); err != nil {
		logf("disabled key %s not persisted: %v", id, err)
	}
	logf("key %s disabled after HTTP %d", id, status)
}

func (g *gateway) health() map[string]any {
	s := g.snapshot.Load()
	keys, usable := 0, 0
	for _, c := range s.Connections {
		keys += len(c.Keys)
		usable += len(g.usable(c))
	}
	status := "ok"
	if g.draining.Load() {
		status = "draining"
	}
	g.disabledLock.Lock()
	disabled := len(g.disabled)
	g.disabledLock.Unlock()
	return map[string]any{
		"status": status, "version": version, "uptime_s": int(time.Since(g.started).Seconds()),
		"snapshot": map[string]any{"revision": s.revision, "age_s": max(nowMs()-s.GeneratedMs, 0) / 1000,
			"tokens": len(s.tokens), "rejected": g.rejected.Load()},
		"keys":  map[string]any{"total": keys, "usable": usable, "disabled": disabled},
		"spool": map[string]any{"lost": g.spool.lost.Load(), "lostRecorded": g.spool.lostRecorded.Load()},
	}
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

func bearer(r *http.Request) (string, bool) {
	value, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return strings.TrimSpace(value), ok
}

func (g *gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == "GET" && r.URL.Path == "/healthz" {
		writeJSON(w, 200, g.health())
		return
	}
	// Loopback only: Caddy forwards /v1/call/* alone, so remote callers never reach this.
	if r.Method == "GET" && r.URL.Path == "/v1/usage" {
		t, ok := bearer(r)
		expected := g.snapshot.Load().UsageTokenSha256
		if !ok || expected == "" || sha(t) != expected {
			writeJSON(w, 401, map[string]string{"error": "unauthenticated", "reason": "the coordinator's usage token is required"})
			return
		}
		writeJSON(w, 200, g.usageReport(r.Context()))
		return
	}
	route, _ := strings.CutPrefix(r.URL.Path, "/v1/call/")
	connectionName, toolName, found := strings.Cut(route, "/")
	if r.Method != "POST" || !found || !strings.HasPrefix(r.URL.Path, "/v1/call/") || !isName(connectionName) || !isName(toolName) {
		writeJSON(w, 404, map[string]string{"error": "not-found", "reason": "unknown route"})
		return
	}
	startedMs, begun := nowMs(), time.Now()
	var run *token
	if t, ok := bearer(r); ok {
		run = g.authenticate(t)
	}
	// Read the body before any refusal: closing on unread bytes resets the connection and loses the caller's reason.
	// Unauthenticated callers get a small, short read, so a slow or large body cannot hold the gateway.
	limit, patience := int64(refusedBytes), refusedPatience
	if run != nil {
		limit, patience = requestBytes, g.config.requestTimeout
	}
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(patience))
	raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	http.NewResponseController(w).SetReadDeadline(time.Time{})
	s := g.snapshot.Load()
	c, known := s.Connections[connectionName]
	p := s.Providers[c.Provider]
	t, offered := p.Tools[toolName]
	offered = offered && known && contains(c.Tools, toolName)
	var a *answer
	usageKey := ""
	switch {
	case run == nil:
		a = refusal(401, "error", "unauthenticated", "a valid run token is required")
	case !contains(run.Connections, connectionName):
		a = refusal(403, "error", "not-granted", "this run is not granted the connection")
	case !offered:
		a = refusal(404, "error", "unknown-tool", "the connection has no such tool")
	case c.Intake != "all":
		a = refusal(403, "error", "intake-paused", "the connection's intake is paused")
	default:
		if scope := blockedScope(c, run); scope != "" {
			a = refusal(403, "error", "budget-reached", "a budget on "+scope+" is reached; ask the owner to raise it")
			break
		}
		var body map[string]json.RawMessage
		if readErr != nil || json.Unmarshal(raw, &body) != nil || body == nil {
			a = refusal(400, "error", "bad-request", "the body must be a JSON object of at most 1 MiB")
			break
		}
		if t.UsageOnce != nil {
			var v string
			json.Unmarshal(body[*t.UsageOnce], &v)
			usageKey = connectionName + ":" + v
		}
		path, ok := fillPath(t, body)
		if !ok {
			a = refusal(400, "error", "bad-request", "a path field is missing or not an id")
			break
		}
		var sent []byte
		if t.Method != "GET" {
			sent, _ = json.Marshal(body)
		}
		a = g.call(r.Context(), connectionName, c, p, t, path, sent)
	}
	if a.disabled != 0 {
		g.onDisabled(a.key, a.disabled)
	}
	callID := fmt.Sprintf("%x-%x-%x", startedMs, os.Getpid(), g.calls.Add(1)-1)
	line := contract.CallLine{CallId: callID, Connection: connectionName, Tool: toolName, Status: int64(a.status),
		Outcome: contract.CallOutcome(a.outcome), StartedAt: startedMs, Ms: time.Since(begun).Milliseconds()}
	if run != nil {
		line.ExecutionId, line.TokenSha256 = &run.ExecutionId, &run.Sha256
	}
	if a.key != "" {
		line.KeyFingerprint = &a.key
	}
	if a.err != "" {
		line.ErrorCode = &a.err
	}
	if a.usage != nil {
		line.Usage = &contract.CallUsage{Unit: t.Unit, Amount: *a.usage}
		if usageKey != "" {
			line.UsageKey = &usageKey
		}
	}
	if a.disabled != 0 {
		line.Disabled = &contract.CallKeyDisabled{Key: a.key, Status: int64(a.disabled)}
	}
	if a.problem != nil {
		line.KeyProblem = &contract.CallKeyProblem{Key: a.key, Status: 403, Message: *a.problem}
	}
	g.spool.append(line)
	w.Header().Set("X-Fleet-Call-Id", callID)
	w.Header().Set("X-Fleet-Outcome", a.outcome)
	if a.err != "" {
		w.Header().Set("X-Fleet-Error", a.err)
	}
	if a.retryAfter != "" {
		w.Header().Set("Retry-After", a.retryAfter)
	}
	writeJSON(w, a.status, a.body)
}

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}

func blockedScope(c connection, run *token) string {
	for _, b := range c.Blocked {
		if b == "connection" || contains(run.scopes, b) {
			return b
		}
	}
	return ""
}

// fillPath puts body fields into {field} in the catalogue path (Meshy task id); a used field leaves the body.
// The caller never selects a key: api_key is dropped, and catalogue fields (Tavily include_usage) are added.
func fillPath(t tool, body map[string]json.RawMessage) (string, bool) {
	delete(body, "api_key")
	for k, v := range t.Inject {
		body[k] = v
	}
	path := t.Path
	for {
		start := strings.IndexByte(path, '{')
		if start < 0 {
			return path, true
		}
		length := strings.IndexByte(path[start:], '}')
		if length < 0 {
			return "", false
		}
		field := path[start+1 : start+length]
		var value string
		if json.Unmarshal(body[field], &value) != nil || value == "" {
			return "", false
		}
		if pattern, ok := t.PathPatterns[field]; ok {
			// Patterns compiled when the snapshot loaded; a "." or ".." segment never reaches the provider.
			if !regexp.MustCompile(pattern).MatchString(value) || dotSegment(value) {
				return "", false
			}
		} else if strings.IndexFunc(value, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 {
			return "", false
		}
		delete(body, field)
		path = path[:start] + value + path[start+length+1:]
	}
}

// usageAt reads a non-negative integer at a dotted field of a JSON reply.
func usageAt(data []byte, field string) (int64, bool) {
	var v any
	if json.Unmarshal(data, &v) != nil {
		return 0, false
	}
	for _, f := range strings.Split(field, ".") {
		m, ok := v.(map[string]any)
		if !ok {
			return 0, false
		}
		v = m[f]
	}
	n, ok := v.(float64)
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
	var v any
	if json.Unmarshal(data, &v) != nil {
		return ""
	}
	for _, f := range strings.Split(field, ".") {
		m, _ := v.(map[string]any)
		v = m[f]
	}
	s, _ := v.(string)
	return s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// serve answers on listener until stop closes, then drains for drain_timeout.
func serve(c *config, listener net.Listener, stop <-chan struct{}) error {
	g, err := newGateway(c)
	if err != nil {
		listener.Close()
		return err
	}
	done := make(chan struct{})
	defer close(done)
	go func() {
		tick := time.NewTicker(c.refreshEvery)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				g.refresh(false)
			case <-done:
				return
			}
		}
	}()
	server := &http.Server{Handler: g, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	fmt.Printf("fleet-gateway: serving on %s\n", listener.Addr())
	select {
	case err := <-served:
		return err
	case <-stop:
	}
	g.draining.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), c.drainTimeout)
	defer cancel()
	if server.Shutdown(ctx) != nil {
		logf("drain timed out; remaining calls are cut")
		server.Close()
	}
	fmt.Printf("fleet-gateway: drained, lost spool lines: %d\n", g.spool.lost.Load())
	return nil
}
