// Fleet coordinator, trial slice (docs/designs/fleet-stack.md#trial-slice-l1): the routes, tables and behaviours of
// SL1-SL11. Environment: docs/coordinator-build.md.
package main

//go:generate go run ../gen ../../contracts/host-protocol.schema.json main protocol_gen.go

import (
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
)

type Person struct {
	Email  string   `json:"email"`
	Name   string   `json:"name"`
	Roles  []string `json:"roles"`
	Owner  bool     `json:"owner"`
	Grants []struct {
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
	startAll(ctx, a)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: a.routes()}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
		<-ch
		a.stream.close()
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// loadPeople loads people.json at start (docs/designs/fleet-people-access.md); refreshPeople (b_request.go) re-reads it
// when it changes.
func (a *App) loadPeople(ctx context.Context, file string) error {
	a.peopleFile = file
	return a.refreshPeople(ctx)
}

// setPeople parses and checks a people list, mirrors it into person and person_grant, then makes it current.
// ponytail: the map swap is not synchronized with concurrent readers; a person edit is rare and owner-only.
func (a *App) setPeople(ctx context.Context, raw []byte) error {
	var list []*Person
	if err := json.Unmarshal(raw, &list); err != nil {
		return err
	}
	people := map[string]*Person{}
	owners := 0
	for _, p := range list {
		if p == nil || p.Email == "" {
			return errors.New("every person needs an email")
		}
		p.Email = strings.ToLower(p.Email)
		if people[p.Email] != nil {
			return errors.New("person " + p.Email + " is listed twice")
		}
		if p.Name == "" {
			p.Name = p.Email
		}
		if p.Owner {
			owners++
		}
		people[p.Email] = p
	}
	if owners != 1 {
		return errors.New("exactly one person must have owner: true")
	}
	err := a.store.Tx(ctx, nil, true, "", func(tx pgx.Tx) error {
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
	return err
}

// scopeOf: the owner works in '*' (nil); anyone else in the projects of their grants, plus null-project rows for architects.
func scopeOf(p *Person) *Scope {
	if p == nil || p.Owner {
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
	params []string
}

func (q *Request) ctx() context.Context { return q.r.Context() }

type handler func(*Request) (any, error)

// route kinds: per-route transaction (read snapshot for GET, one GLOBAL write otherwise, execution scope for
// host ingest); stream runs its own short reads.
const (
	txRead = iota
	txWrite
	txIngest
	txNone
)

type trialRoute struct {
	method string
	re     *regexp.Regexp
	kind   int
	h      handler
	raw    func(*Request, http.ResponseWriter) error
}

func (a *App) routes() http.Handler {
	r := func(method, pattern string, kind int, h handler) trialRoute {
		return trialRoute{method, regexp.MustCompile("^" + pattern + "$"), kind, h, nil}
	}
	seg := "([^/]+)"
	table := []trialRoute{
		r("POST", "/api/sessions", txWrite, a.sessionCreate),
		r("GET", "/api/sessions/"+seg, txRead, a.sessionGet),
		r("POST", "/api/sessions/"+seg+"/messages", txWrite, a.message),
		{method: "GET", re: regexp.MustCompile(`^/api/stream$`), kind: txNone, raw: a.serveStream},
	}
	mux := a.registered()
	return http.HandlerFunc(func(w http.ResponseWriter, hr *http.Request) {
		if _, p := mux.Handler(hr); p != "" { // registry.go routes win over the trial table
			mux.ServeHTTP(w, hr)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		q := &Request{r: hr, app: a}
		err := func() error {
			if err := a.authorize(q); err != nil {
				return err
			}
			for _, rt := range table {
				m := rt.re.FindStringSubmatch(hr.URL.Path)
				if m == nil || rt.method != hr.Method {
					continue
				}
				q.params = m[1:]
				if rt.raw != nil {
					return rt.raw(q, w)
				}
				if err := a.readBody(q); err != nil {
					return err
				}
				if err := a.guard(q); err != nil {
					return err
				}
				var out any
				lock := ""
				if rt.kind == txIngest {
					var b struct {
						ExecutionID *string `json:"executionId"`
					}
					if json.Unmarshal(q.body, &b) != nil || b.ExecutionID == nil {
						return fail(400, "Expected executionId")
					}
					lock = executionScope(*b.ExecutionID)
				}
				err := a.store.Tx(q.ctx(), scopeOf(q.person), rt.kind != txRead, lock, func(tx pgx.Tx) error {
					q.tx = tx
					var err error
					out, err = rt.h(q)
					return err
				})
				if err != nil {
					return err
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				return json.NewEncoder(w).Encode(out)
			}
			return fail(404, "Route "+hr.Method+":"+hr.URL.Path+" not found")
		}()
		if err != nil {
			writeError(w, err)
		}
	})
}

func writeError(w http.ResponseWriter, err error) {
	var he *HTTPError
	code, msg := 500, err.Error()
	if errors.As(err, &he) {
		code = he.Code
	} else if pgCode(err) == "42501" { // a write outside the person's scope (row-level security): 403, not 500
		code, msg = 403, "Not in your scope"
	} else {
		log.Printf("error: %v", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"statusCode": code, "error": http.StatusText(code), "message": msg})
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
	if p == nil {
		return fail(403, "Unknown person")
	}
	q.person = p
	return checkOrigin(q)
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
