package main

import (
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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"fleet/contract"
	"fleet/errtrack"
	"fleet/obs"
)

const (
	requestBytes    = 1 << 20
	refusedBytes    = 64 << 10
	refusedPatience = 2 * time.Second
	providerBytes   = 16 << 20
	// File tools (fleet-connections.md §9): defaults for a connection's limits that leave stallMs or answerMs out.
	defaultStall  = 60 * time.Second
	defaultAnswer = 120 * time.Second
	copyBytes     = 32 << 10
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

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, "fleet-gateway: "+format+"\n", args...) }

func ptr[T any](v T) *T { return &v }

func contains(list []string, item string) bool {
	for _, x := range list {
		if x == item {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

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

// ---------------------------------------------------------------- service

type disabledKey struct {
	status int
	at     int64
}

type cooldownKey struct {
	until int64
	setAt int64
}

type gateway struct {
	config   *config
	access   accessLinks // project access logins (access.go)
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
	// Keys temporarily cooling down after a configured failover status.
	cooldownLock sync.Mutex
	cooldown     map[string]cooldownKey
	// In-memory per-key selection count for least_used balancing.
	usedLock    sync.Mutex
	used        map[string]int64
	currentLock sync.Mutex
	current     map[string]int
	spool       *spool
	draining    atomic.Bool
	started     time.Time
	calls       atomic.Uint64
	// File calls in flight (/healthz transfers), and the cancel of each upload still staging, by call id: a stop
	// refuses those with draining, since nothing reached the provider yet.
	transfers       atomic.Int64
	handling        atomic.Int64 // /v1/call requests in their handler
	stagingLock     sync.Mutex
	staging         map[string]func()
	uploadLocks     sync.Map
	uploadSpaceLock sync.Mutex
	uploadReserved  uint64
}

func newGateway(c *config) (*gateway, error) {
	if err := os.MkdirAll(c.StateDir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	// Staged uploads live under <state>/files; a crash leaves *.part files, removed here.
	files := filepath.Join(c.StateDir, "files")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return nil, fmt.Errorf("files directory: %w", err)
	}
	if left, _ := filepath.Glob(filepath.Join(files, "*.part")); len(left) > 0 {
		for _, part := range left {
			os.Remove(part)
		}
		logf("removed %d staged upload(s) left by an earlier process", len(left))
	}
	g := &gateway{config: c, disabled: map[string]disabledKey{}, cooldown: map[string]cooldownKey{}, used: map[string]int64{}, current: map[string]int{}, started: time.Now(), staging: map[string]func(){},
		// Redirects are never followed: a 307/308 resends the method and body, which could charge a call twice.
		client: &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, IdleConnTimeout: 90 * time.Second, MaxIdleConnsPerHost: 64}}}
	if err := os.MkdirAll(g.uploadDir(), 0o700); err != nil {
		return nil, fmt.Errorf("resumable directory: %w", err)
	}
	if err := syncDir(c.StateDir); err != nil {
		return nil, err
	}
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
	g.maintainUploads(time.Now())
	g.loadValues(s)
	g.snapshot.Store(s)
	return g, nil
}

type usageReportReply struct {
	At   int64            `json:"at"`
	Keys []usageReportRow `json:"keys"`
}

type usageReportRow struct {
	Connection string   `json:"connection"`
	Key        string   `json:"key"`
	Usage      *float64 `json:"usage"`
	Unknown    string   `json:"unknown,omitempty"`
}

// usageReport is the provider's own per-key usage for every key with a value (reconcile, §2); unknown is reported, never guessed.
func (g *gateway) usageReport(ctx context.Context) usageReportReply {
	s := g.snapshot.Load()
	rows := []usageReportRow{}
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
			row := usageReportRow{Connection: name, Key: k.Id}
			response, data, err := g.send(ctx, "GET", providerURL(p, value, p.Usage.Path), deref(p.KeyHeader), keyInHeader(p, value), body{}, nil)
			switch {
			case errors.Is(err, context.DeadlineExceeded) && response == nil:
				row.Unknown = "usage request timed out"
			case response == nil:
				row.Unknown = fmt.Sprintf("usage request: %v", err)
			case response.StatusCode < 200 || response.StatusCode > 299:
				row.Unknown = fmt.Sprintf("usage HTTP %d", response.StatusCode)
			case err != nil:
				row.Unknown = "usage body cut off, too large or too slow"
			default:
				if number, ok := floatAt(data, p.Usage.Field); ok {
					row.Usage = &number
				} else {
					row.Unknown = "usage reply has no " + p.Usage.Field
				}
			}
			rows = append(rows, row)
		}
	}
	return usageReportReply{At: nowMs(), Keys: rows}
}

type healthReply struct {
	Status    string                             `json:"status"`
	Version   string                             `json:"version"`
	UptimeS   int                                `json:"uptime_s"`
	Snapshot  healthSnapshot                     `json:"snapshot"`
	Keys      healthKeys                         `json:"keys"`
	Spool     healthSpool                        `json:"spool"`
	Limits    map[string]*contract.GatewayLimits `json:"limits"`
	Transfers int64                              `json:"transfers"`
}

type healthSnapshot struct {
	Revision string `json:"revision"`
	AgeS     int64  `json:"age_s"`
	Tokens   int    `json:"tokens"`
	Rejected uint64 `json:"rejected"`
}

type healthKeys struct {
	Total    int `json:"total"`
	Usable   int `json:"usable"`
	Disabled int `json:"disabled"`
}

type healthSpool struct {
	Lost         uint64 `json:"lost"`
	LostRecorded bool   `json:"lostRecorded"`
}

func (g *gateway) health() healthReply {
	s := g.snapshot.Load()
	keys, usable := 0, 0
	limits := map[string]*contract.GatewayLimits{}
	for name, c := range s.Connections {
		keys += len(c.Keys)
		usable += len(g.usable(c))
		if c.Limits != nil {
			limits[name] = c.Limits
		}
	}
	status := "ok"
	if g.draining.Load() {
		status = "draining"
	}
	g.disabledLock.Lock()
	disabled := len(g.disabled)
	g.disabledLock.Unlock()
	return healthReply{
		Status:  status,
		Version: version,
		UptimeS: int(time.Since(g.started).Seconds()),
		Snapshot: healthSnapshot{
			Revision: s.revision,
			AgeS:     max(nowMs()-s.GeneratedMs, 0) / 1000,
			Tokens:   len(s.tokens),
			Rejected: g.rejected.Load(),
		},
		Keys: healthKeys{
			Total:    keys,
			Usable:   usable,
			Disabled: disabled,
		},
		Spool: healthSpool{
			Lost:         g.spool.lost.Load(),
			LostRecorded: g.spool.lostRecorded.Load(),
		},
		Limits:    limits,
		Transfers: g.transfers.Load(),
	}
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
	if r.Method == "POST" && r.URL.Path == tokenSpecsPath {
		g.serveTokenSpecs(w, r)
		return
	}
	route, _ := strings.CutPrefix(r.URL.Path, "/v1/call/")
	connectionName, toolName, found := strings.Cut(route, "/")
	if r.Method != "POST" || !found || !strings.HasPrefix(r.URL.Path, "/v1/call/") || !isName(connectionName) || !isName(toolName) {
		writeJSON(w, 404, map[string]string{"error": "not-found", "reason": "unknown route"})
		return
	}
	g.handling.Add(1)
	defer g.handling.Add(-1)
	startedMs := nowMs()
	q := &request{id: fmt.Sprintf("%x-%x-%x", startedMs, os.Getpid(), g.calls.Add(1)-1), connection: connectionName, tool: toolName,
		startedMs: startedMs, begun: time.Now()}
	if t, ok := bearer(r); ok {
		q.run, q.bearer = g.authenticate(t), t
	}
	// The tool is resolved before the body is read: an upload's body is the file, read only once the call is admitted.
	q.fileClient = r.Header.Get("X-Fleet-File-Client") == "1"
	g.resolve(q)
	if q.offered && q.t.File != nil && *q.t.File == contract.GatewayFileModeUpload {
		if r.Header.Get(contract.UploadModeHeader) != "" {
			g.serveResumableUpload(w, r, q)
		} else {
			g.serveUpload(w, r, q)
		}
		return
	}
	// Read the body before any refusal: closing on unread bytes resets the connection and loses the caller's reason.
	// Unauthenticated callers get a small, short read, so a slow or large body cannot hold the gateway.
	limit, patience := int64(refusedBytes), refusedPatience
	if q.run != nil {
		limit, patience = requestBytes, g.config.requestTimeout
	}
	http.NewResponseController(w).SetReadDeadline(time.Now().Add(patience))
	raw, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	http.NewResponseController(w).SetReadDeadline(time.Time{})
	// A pause, budget block or tool change published while the body arrived applies to this call.
	g.resolve(q)
	a := admit(q)
	if a == nil && q.t.File != nil && *q.t.File == contract.GatewayFileModeUpload {
		// The tool became an upload tool while this JSON body arrived.
		a = refusal(400, "error", "file-expected", "an upload tool takes the file bytes as application/octet-stream with X-Fleet-Args")
	}
	if a == nil {
		if q.connection == "telegram" && q.tool == "get_updates" {
			w.Header().Set(contract.GatewayAdmissionHeader, strconv.FormatInt(nowMs(), 10))
		}
		if a = g.serveJSON(w, r, q, raw, readErr); a == nil {
			return // a download streamed, with its started and completed spool lines
		}
	}
	g.finish(w, q, a, nil)
}

// spanRoute names a request's span by its method and first two path segments (/v1/call), never by connection or tool.
func spanRoute(r *http.Request) string {
	seg := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 3)
	if len(seg) > 2 {
		seg = seg[:2]
	}
	return r.Method + " /" + strings.Join(seg, "/")
}

// serve answers on listener until stop closes, then drains for drain_timeout.
func serve(c *config, listener net.Listener, stop <-chan struct{}) error {
	g, err := newGateway(c)
	if err != nil {
		listener.Close()
		return err
	}
	// serve returns only after the refresh loop has stopped: a refresh still writing state_dir would outlive the call.
	done, refreshed := make(chan struct{}), make(chan struct{})
	defer func() { close(done); <-refreshed }()
	errtrack.Go("gateway.refresh", func() {
		defer close(refreshed)
		tick := time.NewTicker(c.refreshEvery)
		expire := time.NewTicker(time.Hour)
		defer tick.Stop()
		defer expire.Stop()
		for {
			select {
			case <-tick.C:
				g.refresh(false)
			case now := <-expire.C:
				g.maintainUploads(now)
			case <-done:
				return
			}
		}
	})
	// obs.Handler, as in the coordinator: a handler's panic reaches Bugsink through the errtrack hook.
	server := &http.Server{Handler: obs.Handler(g, spanRoute), ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	errtrack.Go("gateway.serve", func() { served <- server.Serve(listener) })
	fmt.Printf("fleet-gateway: serving on %s\n", listener.Addr())
	select {
	case err := <-served:
		return err
	case <-stop:
	}
	// Uploads still staging answer draining at once (nothing reached the provider); posts in flight finish within the drain.
	g.stagingLock.Lock()
	g.draining.Store(true)
	for _, cancel := range g.staging {
		cancel()
	}
	g.stagingLock.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), c.drainTimeout)
	defer cancel()
	if server.Shutdown(ctx) != nil {
		logf("drain timed out; remaining calls are cut")
		server.Close()
		// A cut call still writes its spool line (a post cut at the drain end may have been stored).
		for end := time.Now().Add(5 * time.Second); g.handling.Load() > 0 && time.Now().Before(end); {
			time.Sleep(10 * time.Millisecond)
		}
	}
	fmt.Printf("fleet-gateway: drained, lost spool lines: %d\n", g.spool.lost.Load())
	return nil
}
