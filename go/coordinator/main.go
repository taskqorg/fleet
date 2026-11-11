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
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/obs"
)

type Person struct {
	Email string   `json:"email"`
	Name  string   `json:"name"`
	Roles []string `json:"roles"`
	Owner bool     `json:"owner"`
	// A Cloudflare Access service token's common_name (the guard's X-Fleet-Person for it); empty for people.
	AccessCommonName string `json:"accessCommonName,omitempty"`
	Grants           []struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"grants"`
}

type App struct {
	store         *Store
	people        map[string]*Person
	peopleFile    string
	browserSecret string
	hostID        string
	stream        *Broadcast
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
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
		<-ch
		a.stream.close()
		stopTelemetry() // the queued spans get their last flush
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
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
		return nil
	})
	if err == nil {
		a.people = people
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
	app    *App
	r      *http.Request
	person *Person
	host   map[string]any
	agent  string // execution:<id> when a host agent calls on behalf of person (its director)
	body   json.RawMessage
	tx     pgx.Tx
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
	p := a.people[strings.ToLower(email)]
	for _, c := range a.people { // ponytail: linear scan, fewer than 10 people
		if p == nil && c.AccessCommonName != "" && strings.EqualFold(c.AccessCommonName, email) {
			p = c
			break
		}
	}
	if p == nil {
		return fail(403, "Unknown person")
	}
	q.person = p
	return checkOrigin(q)
}

// removedHostSecret: the secret belongs to a removed host.
func (a *App) removedHostSecret(ctx context.Context, secret string) (bool, error) {
	if secret == "" {
		return false, nil
	}
	var n int
	err := a.store.pool.QueryRow(ctx, "SELECT count(*) FROM document WHERE kind='host' AND data->>'removedSecretHash'=$1", secretHash(secret)).Scan(&n)
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
	if holder, _ := host["holder"].(string); a.people[holder] == nil {
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
