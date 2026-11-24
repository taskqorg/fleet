// Fleet coordinator: the HTTP API, the SSE stream and the built app over Postgres. Environment: docs/coordinator-build.md.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fleet/coordinator/db"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/errtrack"
	"fleet/obs"
)

type Person struct {
	AccessVersion string   `json:"-"`
	Email         string   `json:"email"`
	Name          string   `json:"name"`
	Roles         []string `json:"roles"`
	Owner         bool     `json:"owner"`
	// A Cloudflare Access service token's common_name (the guard's X-Fleet-Person for it); empty for people.
	AccessCommonName string `json:"accessCommonName,omitempty"`
	Grants           []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"grants"`
}

type App struct {
	store         *Store
	people        atomic.Pointer[peopleSnapshot]
	peopleFile    string
	browserSecret string
	hostID        string
	stream        *Broadcast

	peopleCloudflare            *peopleCloudflare
	peopleCloudflareConfigError error

	runtimeOnce         sync.Once
	dispatchLoopStarted atomic.Bool
	dispatchMu          sync.Mutex
	dispatchCh          chan struct{}
	dispatchDoneCh      chan struct{}
	dispatchReqGen      uint64
	dispatchDoneGen     uint64
	lostCh              chan struct{}
	artifactWaitsCh     chan struct{}
	botWakeCh           chan struct{}

	envCacheGen  atomic.Uint64
	envMu        sync.RWMutex
	envCacheData map[string][]string
	envCacheOK   bool

	pollMu     sync.Mutex
	hostWake   map[string]*hostWakeEntry
	heartbeats map[string]*hostHeartbeat

	stopOnce sync.Once
	stopCh   chan struct{}
}

func (a *App) ensureRuntime() {
	a.runtimeOnce.Do(func() {
		if a.store != nil && a.store.app == nil {
			a.store.app = a
		}
		a.dispatchCh = make(chan struct{}, 1)
		a.dispatchDoneCh = make(chan struct{})
		a.lostCh = make(chan struct{}, 1)
		a.artifactWaitsCh = make(chan struct{}, 1)
		a.botWakeCh = make(chan struct{}, 1)
		a.hostWake = map[string]*hostWakeEntry{}
		a.heartbeats = map[string]*hostHeartbeat{}
		a.stopCh = make(chan struct{})
	})
}

func (a *App) stoppedCh() <-chan struct{} {
	a.ensureRuntime()
	return a.stopCh
}

func (a *App) closeLongPolls() {
	a.ensureRuntime()
	a.stopOnce.Do(func() { close(a.stopCh) })
}

type shutdownTimeouts struct {
	http       time.Duration
	background time.Duration
	store      time.Duration
}

var defaultShutdownTimeouts = shutdownTimeouts{
	http:       10 * time.Second,
	background: 5 * time.Second,
	store:      3 * time.Second,
}

func (a *App) shutdown(srv *http.Server, bgCancel context.CancelFunc, bg *bgTracker, reqCancel context.CancelFunc, timeouts shutdownTimeouts) {
	a.closeLongPolls()
	if a.stream != nil {
		a.stream.close()
	}
	if bgCancel != nil {
		bgCancel()
	}
	if srv != nil {
		c, cancel := context.WithTimeout(context.Background(), timeouts.http)
		if err := srv.Shutdown(c); err != nil {
			log.Printf("http shutdown: %v", err)
			if reqCancel != nil {
				reqCancel()
			}
			_ = srv.Close()
		}
		cancel()
	}
	if reqCancel != nil {
		reqCancel()
	}
	if bg != nil && !waitBackground(bg, timeouts.background) {
		log.Printf("shutdown: timed out waiting for background workers")
	}
	stopTelemetry() // the queued spans get their last flush
	if a.store != nil && !closeStoreWithTimeout(a.store, timeouts.store) {
		log.Printf("shutdown: timed out closing store")
	}
}

func waitBackground(bg *bgTracker, timeout time.Duration) bool {
	bg.close()
	done := make(chan struct{})
	go func() {
		bg.wait()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

func closeStoreWithTimeout(s *Store, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		s.Close()
		close(done)
	}()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-done:
		return true
	case <-t.C:
		return false
	}
}

func main() {
	if len(os.Args) == 2 && os.Args[1] == "selftest" {
		if err := selftest(); err != nil {
			log.Fatal("selftest: ", err)
		}
		fmt.Printf("fleet-coordinator %s: selftest ok\n", version)
		return
	}
	sigCtx, stopSignals := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stopSignals()
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Fatalf("%s required", k)
		}
		return v
	}
	peopleFile, database := need("FLEET_PEOPLE_FILE"), need("FLEET_DATABASE_URL")
	migration := os.Getenv("FLEET_MIGRATION_DATABASE_URL")
	if migration == "" {
		migration = database
	}
	if err := migrate(sigCtx, migration); err != nil {
		log.Fatal(err)
	}
	store, err := openStore(sigCtx, database)
	if err != nil {
		log.Fatal(err)
	}
	var storeClosed atomic.Bool
	defer func() {
		if !storeClosed.Load() {
			store.Close()
		}
	}()
	a := &App{store: store, browserSecret: os.Getenv("FLEET_BROWSER_SECRET"), hostID: os.Getenv("FLEET_HOST_ID")}
	a.peopleCloudflare, a.peopleCloudflareConfigError = cloudflarePeopleFromEnv()
	a.ensureRuntime()
	if a.hostID == "" {
		a.hostID = "owner-mac"
	}
	if err := a.loadPeople(sigCtx, peopleFile); err != nil {
		log.Fatal(err)
	}
	bgCtx, bgCancel := context.WithCancel(sigCtx)
	defer bgCancel()
	var bg bgTracker
	bgCtx = withBackgroundTracker(bgCtx, &bg)
	restoreCoalesced := setCoalescedScope(bgCtx, &bg)
	defer restoreCoalesced()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	a.stream = listen(bgCtx, database, store)
	a.stream.setOnWake(a.wakeBots)
	startTelemetry(bgCtx, a)
	startAll(bgCtx, a)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}
	srv := &http.Server{
		Addr:              "127.0.0.1:" + port,
		Handler:           obs.Handler(a.routes(), spanName),
		BaseContext:       func(net.Listener) context.Context { return reqCtx },
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-sigCtx.Done()
		stopSignals()
		storeClosed.Store(true)
		a.shutdown(srv, bgCancel, &bg, reqCancel, defaultShutdownTimeouts)
	}()
	ln, err := activatedListener(srv.Addr)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained // Serve returns when Shutdown starts; in-flight requests and background workers finish first
}

// loadPeople loads people.json at start (docs/designs/fleet-people-access.md); refreshPeople (browser_checks.go) re-reads it
// when it changes.
func (a *App) loadPeople(ctx context.Context, file string) error {
	a.peopleFile = file
	return a.refreshPeople(ctx)
}

var personEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+$`)

// personRow: one people.json entry checked as strictly as the old parsePeople: an e-mail (a bare name for a
// service-token person with accessCommonName), roles a non-empty list of
// operator/architect, owner true or false, grants a list of {kind, id} strings, no other keys.
func personRow(raw json.RawMessage) (*Person, error) {
	var p Person
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return nil, errors.New("a person is not {email, name, roles, owner, grants, accessCommonName}: " + err.Error())
	}
	if !personEmail.MatchString(p.Email) && (p.AccessCommonName == "" || p.Email == "" || strings.ContainsAny(p.Email, "@ \t\n")) {
		return nil, errors.New("each person needs an e-mail (a service-token person: a name such as site-agents)")
	}
	if len(p.Roles) == 0 {
		return nil, errors.New("roles of " + p.Email + " must be operator and/or architect")
	}
	for _, r := range p.Roles {
		if r != "operator" && r != "architect" {
			return nil, errors.New("roles of " + p.Email + " must be operator and/or architect")
		}
	}
	for _, g := range p.Grants {
		if !((g.Kind == "project" && g.ID != "" && !strings.ContainsAny(g.ID, ",\r\n\t ") && (g.ID != "*" || hasRole(&p, "architect"))) ||
			(g.Kind == "lab" && g.ID == "personal" && p.AccessCommonName == "") || g.Kind == "*") {
			return nil, errors.New("grants of " + p.Email + " must be [{kind, id}]")
		}
	}
	return &p, nil
}

// setPeople parses and checks a people list, mirrors it into person and person_grant, then makes it current. Strict
// (people_set) refuses any malformed entry; otherwise (the file) a malformed entry is left out and named in skipped, so
// one bad row never locks out everyone else (site-agents included).
func (a *App) setPeople(ctx context.Context, raw []byte, strict bool, change func(*Request) error) (skipped []string, err error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, errors.New("people.json must be a list")
	}
	var list []*Person
	for i, r := range rows {
		p, err := personRow(r)
		if err != nil {
			err = fmt.Errorf("entry %d: %w", i+1, err)
		}
		if err != nil && strict {
			return nil, err
		}
		if err != nil {
			skipped = append(skipped, err.Error())
			continue
		}
		list = append(list, p)
	}
	people := map[string]*Person{}
	owners := 0
	for _, p := range list {
		p.Email = strings.ToLower(p.Email)
		if people[p.Email] != nil {
			return nil, errors.New("person " + p.Email + " is listed twice")
		}
		if p.Name == "" {
			p.Name = p.Email
		}
		if p.Owner {
			owners++
		}
		people[p.Email] = p
	}
	// A common name is a second key in the same X-Fleet-Person namespace: unique, never an e-mail key.
	names := map[string]bool{}
	for _, p := range list {
		cn := strings.ToLower(p.AccessCommonName)
		if cn != "" && (names[cn] || people[cn] != nil || strings.Contains(cn, "@")) {
			return nil, errors.New("accessCommonName " + p.Email + " is not unique or collides with an e-mail")
		}
		names[cn] = true
	}
	if owners != 1 {
		return nil, errors.New("exactly one person must have owner: true" + strings.Join(append([]string{""}, skipped...), "; left out: "))
	}
	err = a.store.Write(ctx, nil, []string{GLOBAL}, func(req *Request) error {
		emails := []string{}
		for _, p := range list {
			emails = append(emails, p.Email)
		}
		q := req.db()
		for _, revoke := range []func(context.Context, []string) error{q.PeopleRevokeInitialMembers, q.PeopleRevokeSessionDirectors,
			q.PeopleRevokeExecutionDirectors, q.PeopleRevokeRunTokens, q.PeopleMarkRemovedRequesters} {
			if err := revoke(ctx, emails); err != nil {
				return err
			}
		}
		if err := q.PeoplePruneRemoved(ctx, emails); err != nil {
			return err
		}
		// Upsert non-owners before the owner so transferring owner: true between existing people never violates person_one_owner.
		for _, isOwner := range []bool{false, true} {
			for _, p := range list {
				if p.Owner != isOwner {
					continue
				}
				if err := q.PersonUpsert(ctx, db.PersonUpsertParams{Email: p.Email, Name: p.Name, Roles: p.Roles, Owner: p.Owner}); err != nil {
					return err
				}
			}
		}
		if err := q.PersonGrantsClear(ctx); err != nil {
			return err
		}
		for _, p := range list {
			for _, g := range p.Grants {
				if err := q.PersonGrantInsert(ctx, db.PersonGrantInsertParams{Email: p.Email, ObjectKind: g.Kind, ObjectID: g.ID}); err != nil {
					return err
				}
			}
		}
		if change != nil {
			if err := change(req); err != nil {
				return err
			}
		}
		return q.PeopleChanged(ctx)
	})
	if err == nil {
		a.invalidateEnvCache()
		a.wakeDispatch()
		a.publishPeople(people)
		schedulePublish()
	}
	return skipped, err
}

// scopeOf: the owner works in '*' (nil); anyone else in the projects of their grants, plus null-project rows for architects.
func scopeOf(p *Person) *Scope {
	if p == nil || fullAccess(p) {
		return nil
	}
	if hasAllProjects(p) {
		return &Scope{ProjectIDs: "*", Person: p.Email}
	}
	ids := []string{}
	for _, g := range p.Grants {
		if g.Kind == "project" {
			ids = append(ids, g.ID)
		}
	}
	prefix := ""
	for _, r := range p.Roles {
		if r == "architect" {
			prefix = ","
		}
	}
	return &Scope{ProjectIDs: prefix + strings.Join(ids, ","), Person: p.Email}
}

func secretHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

// authHost: the host a host request's secret names (hostFromSecret): the fields of its record the handlers read.
type authHost struct {
	HostID     string `json:"hostId"`
	Holder     string `json:"holder"`
	AutoUpdate string `json:"autoUpdate"` // on, off or a hold until an RFC 3339 time (validAutoUpdate)
}

// errorBody: the JSON body of an error answer; an HTTPError's Extra fields join it (writeError).
type errorBody struct {
	StatusCode int    `json:"statusCode"`
	Error      string `json:"error"`
	Message    string `json:"message"`
}

// Request: the credential check's outcome and the route's transaction.
type Request struct {
	app        *App
	r          *http.Request
	person     *Person
	peopleView *peopleSnapshot
	host       *authHost
	agent      string // execution:<id> when a host agent calls on behalf of person (its director)
	body       json.RawMessage
	tx         pgx.Tx
	accessTx   pgx.Tx // tx on which refreshRequestAccess already ran
}

func (q *Request) baseCtx() context.Context {
	if q != nil && q.r != nil {
		return q.r.Context()
	}
	return context.Background()
}

// ctx returns the request context, carrying q.tx via withTx whenever a transaction is active so helpers using
// conn/txCtx stay on the same connection instead of taking a second pool connection.
func (q *Request) ctx() context.Context {
	if q != nil && q.tx != nil {
		return withTx(q)
	}
	return q.baseCtx()
}

type handler func(*Request) (any, error)

// errorAnswer: the status and message an error answers with (writeError, and the host poll's failure record).
func errorAnswer(err error) (code int, msg string, he *HTTPError) {
	if errors.As(err, &he) {
		return he.Code, err.Error(), he
	}
	if pgCode(err) == "42501" { // a write outside the person's scope (row-level security): 403, not 500
		return 403, "Not in your scope", nil
	}
	return 500, err.Error(), nil
}

func writeError(w http.ResponseWriter, err error) {
	code, msg, he := errorAnswer(err)
	if he != nil {
		if code == 403 || he.Refused {
			obs.MarkRefused(w) // a missing role or grant, a removed host: a decision, not an error (fleet-telemetry.md §1)
		}
	} else if code == 403 {
		obs.MarkRefused(w)
	} else {
		log.Printf("error: %v", err)
	}
	if code == 500 {
		errtrack.Capture(err, map[string]string{"status": "500"}) // an unexpected server error (fleet-error-tracking.md)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	body := jsonBytes(errorBody{StatusCode: code, Error: http.StatusText(code), Message: msg})
	if he != nil && he.Extra != nil {
		// Extra is a struct: its fields join the body's object, over a field of the same name.
		var fields, extra rawObject
		if json.Unmarshal(body, &fields) == nil && json.Unmarshal(jsonBytes(he.Extra), &extra) == nil {
			maps.Copy(fields, extra)
			body = jsonBytes(fields)
		}
	}
	w.Write(append(body, '\n'))
}

// readBody keeps the JSON body and applies the host preHandler: a body hostId of another host is 403, a missing one is the
// host the secret names.
func (a *App) readBody(q *Request) error {
	if q.r.Method == "GET" || q.r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(http.MaxBytesReader(nil, q.r.Body, 1<<20))
	var raw json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		if err.Error() == "EOF" {
			return nil
		}
		return fail(400, "Invalid JSON body")
	}
	if t := strings.TrimSpace(string(raw)); t != "null" && !strings.HasPrefix(t, "{") { // every route takes an object
		return fail(400, "Invalid body")
	}
	q.body = raw
	if q.host == nil {
		return nil
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil
	}
	id := q.host.HostID
	if h, ok := obj["hostId"]; ok {
		var s string
		if json.Unmarshal(h, &s) != nil || s != id {
			return fail(403, "Host rejected")
		}
	}
	obj["hostId"], _ = json.Marshal(id)
	q.body, _ = json.Marshal(obj)
	return nil
}

// authorize is the onRequest credential check (SL1, SL2): a host request is the host its own secret names and carries no
// person; a browser request carries exactly `Bearer <browser secret>` and a known person.
func (a *App) authorize(q *Request) error {
	h := q.r.Header
	supplied := strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
	_, named := h["X-Fleet-Person"]
	if err := a.refreshPeople(q.ctx()); err != nil {
		return err
	}
	if strings.HasPrefix(q.r.URL.Path, "/api/hosts/") {
		host, err := a.hostFromSecret(q.ctx(), supplied)
		if err != nil {
			return err
		}
		if host == nil {
			if removed, err := a.removedHostSecret(q.ctx(), supplied); err != nil {
				return err
			} else if removed {
				return &HTTPError{Code: 410, Msg: "host-removed", Refused: true} // the executor stops polling and exits 0
			}
			return fail(401, "Unauthorized")
		}
		if named {
			return fail(403, "Host requests carry no person")
		}
		q.host = host
		if deletedHostRoutes[q.r.URL.Path] {
			q.r = q.r.WithContext(context.WithValue(q.ctx(), showDeletedKey{}, true))
		}
		return nil
	}
	if a.browserSecret == "" || subtle.ConstantTimeCompare([]byte(supplied), []byte(a.browserSecret)) != 1 {
		return fail(401, "Unauthorized")
	}
	email := h.Get("X-Fleet-Person")
	if email == "" {
		return fail(401, "Person required")
	}
	p := q.peopleSnapshot().person(strings.ToLower(email))
	for _, c := range q.peopleSnapshot().list() { // ponytail: linear scan, fewer than 10 people
		if p == nil && c.AccessCommonName != "" && strings.EqualFold(c.AccessCommonName, email) {
			p = c
			break
		}
	}
	if p == nil {
		return fail(403, "Unknown person")
	}
	var err error
	q.person, err = a.resolveAccess(q.ctx(), p.Email)
	if err != nil {
		return err
	}
	return checkOrigin(q)
}

// removedHostSecret: the secret belongs to a removed host.
func (a *App) removedHostSecret(ctx context.Context, secret string) (bool, error) {
	if secret == "" {
		return false, nil
	}
	h := secretHash(secret)
	n, err := db.New(a.store.readPool).HostRemovedSecretCount(ctx, &h)
	return n > 0, err
}

// hostFromSecret: the host credentials whose secretHash matches, if its holder is still a person.
func (a *App) hostFromSecret(ctx context.Context, secret string) (*authHost, error) {
	if secret == "" {
		return nil, nil
	}
	s := a.store
	key := secretHash(secret)
	s.authMu.Lock()
	generation := s.authGeneration
	var cached *authHost
	if s.authWrites == 0 {
		if h, ok := s.authHosts[key]; ok {
			cached = &h
		}
	}
	s.authMu.Unlock()
	if cached == nil {
		row, err := db.New(s.readPool).HostBySecret(ctx, &key)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		var payload struct {
			AutoUpdate json.RawMessage `json:"autoUpdate"`
		}
		_ = json.Unmarshal(row.Payload, &payload)
		var autoUpdate string
		_ = json.Unmarshal(payload.AutoUpdate, &autoUpdate)
		h := authHost{HostID: row.HostID, Holder: deref(row.Holder), AutoUpdate: autoUpdate}
		s.authMu.Lock()
		if s.authWrites == 0 && s.authGeneration == generation {
			if len(s.authHosts) >= 1024 {
				s.authHosts = nil
			}
			if s.authHosts == nil {
				s.authHosts = map[string]authHost{}
			}
			s.authHosts[key] = h
		}
		s.authMu.Unlock()
		cached = &h
	}
	if a.peopleSnapshot().person(cached.Holder) == nil {
		return nil, nil
	}
	return cached, nil
}

func pgCode(err error) string {
	var pe interface{ SQLState() string }
	if errors.As(err, &pe) {
		return pe.SQLState()
	}
	return ""
}

var _ = fmt.Sprint

// activatedListener takes the systemd socket (fleet.socket, held open across restarts so a release is no refused
// connection) or binds addr for tests and manual runs.
func activatedListener(addr string) (net.Listener, error) {
	if os.Getenv("LISTEN_PID") == strconv.Itoa(os.Getpid()) && os.Getenv("LISTEN_FDS") == "1" {
		ln, err := net.FileListener(os.NewFile(3, "systemd-socket"))
		if err != nil {
			return nil, err
		}
		if tcp, ok := ln.Addr().(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
			ln.Close()
			return nil, fmt.Errorf("the coordinator binds only to loopback, not %s", ln.Addr())
		}
		log.Printf("serving on activated socket %s", ln.Addr())
		return ln, nil
	}
	return net.Listen("tcp", addr)
}

// hostOf: the authenticated host's id.
func hostOf(q *Request) string { return q.host.HostID }
