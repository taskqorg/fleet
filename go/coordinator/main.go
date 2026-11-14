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
	"fmt"
	"log"
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

	runtimeOnce         sync.Once
	dispatchLoopStarted atomic.Bool
	dispatchMu          sync.Mutex
	dispatchCh          chan struct{}
	dispatchDoneCh      chan struct{}
	dispatchReqGen      uint64
	dispatchDoneGen     uint64

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
		a.dispatchCh = make(chan struct{}, 1)
		a.dispatchDoneCh = make(chan struct{})
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

func main() {
	if len(os.Args) == 2 && os.Args[1] == "selftest" {
		if err := selftest(); err != nil {
			log.Fatal("selftest: ", err)
		}
		fmt.Printf("fleet-coordinator %s: selftest ok\n", version)
		return
	}
	ctx := context.Background()
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
	if err := migrate(ctx, migration); err != nil {
		log.Fatal(err)
	}
	store, err := openStore(ctx, database)
	if err != nil {
		log.Fatal(err)
	}
	a := &App{store: store, browserSecret: os.Getenv("FLEET_BROWSER_SECRET"), hostID: os.Getenv("FLEET_HOST_ID")}
	a.ensureRuntime()
	if a.hostID == "" {
		a.hostID = "owner-mac"
	}
	if err := a.loadPeople(ctx, peopleFile); err != nil {
		log.Fatal(err)
	}
	a.stream = listen(ctx, database)
	startTelemetry(ctx, a)
	startAll(ctx, a)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: obs.Handler(a.routes(), spanName)}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
		<-ch
		a.closeLongPolls()
		a.stream.close()
		stopTelemetry() // the queued spans get their last flush
		// In-flight requests finish; with socket activation new ones wait in the held socket for the next process.
		c, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	ln, err := activatedListener(srv.Addr)
	if err != nil {
		log.Fatal(err)
	}
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-drained // Serve returns when Shutdown starts; in-flight requests finish first
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
		if (g.Kind != "project" && g.Kind != "*") || (g.Kind == "project" && g.ID == "") {
			return nil, errors.New("grants of " + p.Email + " must be [{kind, id}]")
		}
	}
	return &p, nil
}

// setPeople parses and checks a people list, mirrors it into person and person_grant, then makes it current. Strict
// (people_set) refuses any malformed entry; otherwise (the file) a malformed entry is left out and named in skipped, so
// one bad row never locks out everyone else (site-agents included).
// ponytail: the map swap is not synchronized with concurrent readers; a person edit is rare and owner-only.
func (a *App) setPeople(ctx context.Context, raw []byte, strict bool) (skipped []string, err error) {
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
	err = a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
		emails := []string{}
		for _, p := range list {
			emails = append(emails, p.Email)
		}
		if _, err := tx.Exec(ctx, `UPDATE definition SET initial_member_revoked_at=now(),access_version=access_version+1 WHERE kind='project' AND initial_member IS NOT NULL AND initial_member_revoked_at IS NULL AND NOT(initial_member=ANY($1))`, emails); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE document SET data=data||jsonb_build_object('directorAccessRevokedAt',extract(epoch from now())*1000) WHERE kind='session' AND project_id IS NULL AND data ? 'directedBy' AND NOT(data ? 'directorAccessRevokedAt') AND NOT(data->>'directedBy'=ANY($1))`, emails); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE execution SET data=data||jsonb_build_object('directorAccessRevokedAt',extract(epoch from now())*1000) WHERE project_id IS NULL AND session_id IS NULL AND directed_by IS NOT NULL AND NOT(data ? 'directorAccessRevokedAt') AND NOT(directed_by=ANY($1))`, emails); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE document SET data=data||jsonb_build_object('revokedAt',coalesce(data->'revokedAt',to_jsonb(extract(epoch from now())*1000)),'directorAccessRevokedAt',extract(epoch from now())*1000) WHERE kind='run-token' AND data ? 'directedBy' AND NOT(data ? 'directorAccessRevokedAt') AND NOT(data->>'directedBy'=ANY($1))`, emails); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE experiment SET requester_removed_at=now() WHERE requester_removed_at IS NULL AND requested_by IN (SELECT email FROM person WHERE NOT(email=ANY($1)))`, emails); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM person"); err != nil {
			return err
		}
		for _, p := range list {
			if _, err := tx.Exec(ctx, "INSERT INTO person(email,name,roles,owner) VALUES($1,$2,$3,$4)", p.Email, p.Name, p.Roles, p.Owner); err != nil {
				return err
			}
			for _, g := range p.Grants {
				if _, err := tx.Exec(ctx, "INSERT INTO person_grant(email,object_kind,object_id) VALUES($1,$2,$3) ON CONFLICT DO NOTHING", p.Email, g.Kind, g.ID); err != nil {
					return err
				}
			}
		}
		_, err := tx.Exec(ctx, "SELECT pg_notify('fleet_change','')")
		return err
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

// Request: the credential check's outcome and the route's transaction.
type Request struct {
	app        *App
	r          *http.Request
	person     *Person
	peopleView *peopleSnapshot
	host       map[string]any
	agent      string // execution:<id> when a host agent calls on behalf of person (its director)
	body       json.RawMessage
	tx         pgx.Tx
}

func (q *Request) ctx() context.Context { return q.r.Context() }

type handler func(*Request) (any, error)

func writeError(w http.ResponseWriter, err error) {
	var he *HTTPError
	code, msg := 500, err.Error()
	if errors.As(err, &he) {
		code = he.Code
		if code == 403 || he.Refused {
			obs.MarkRefused(w) // a missing role or grant, a removed host: a decision, not an error (fleet-telemetry.md §1)
		}
	} else if pgCode(err) == "42501" { // a write outside the person's scope (row-level security): 403, not 500
		code, msg = 403, "Not in your scope"
		obs.MarkRefused(w)
	} else {
		log.Printf("error: %v", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	body := map[string]any{"statusCode": code, "error": http.StatusText(code), "message": msg}
	if he != nil {
		for k, v := range he.Extra {
			body[k] = v
		}
	}
	json.NewEncoder(w).Encode(body)
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
	id := q.host["hostId"].(string)
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
	var n int
	err := a.store.pool.QueryRow(ctx, "SELECT count(*) FROM document WHERE kind='host' AND data @> jsonb_build_object('removedSecretHash', $1::text)", secretHash(secret)).Scan(&n)
	return n > 0, err
}

// hostFromSecret: the host document whose secretHash matches, if its holder is still a person.
func (a *App) hostFromSecret(ctx context.Context, secret string) (map[string]any, error) {
	if secret == "" {
		return nil, nil
	}
	var data []byte
	err := a.store.pool.QueryRow(ctx, "SELECT data FROM document WHERE kind='host' AND data->>'secretHash'=$1", secretHash(secret)).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var host map[string]any
	json.Unmarshal(data, &host)
	if holder, _ := host["holder"].(string); a.peopleSnapshot().person(holder) == nil {
		return nil, nil
	}
	return host, nil
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
