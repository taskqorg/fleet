// Fleet coordinator front door, slice 1 (docs/designs/fleet-stack.md#coordinator-migration-order). It serves the routes in
// owned() itself and passes every other request, unchanged, to the TypeScript coordinator at FLEET_BACKEND_URL. Both share
// one Postgres; the TypeScript coordinator applies schema.sql and mirrors people.json into person rows.
//
// Environment, as packages/coordinator/server.ts: PORT, FLEET_DATABASE_URL (role fleet_app), FLEET_PEOPLE_FILE,
// FLEET_BROWSER_SECRET, FLEET_BROWSER_ORIGIN, FLEET_BROWSER_PROXY_AUTH; plus FLEET_BACKEND_URL.
package main

//go:generate go run ../gen ../../contracts/host-protocol.schema.json main protocol_gen.go

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
)

type App struct {
	store         *Store
	people        *People
	browserSecret string
	browserOrigin string
	proxyAuth     bool
	stream        *Broadcast
	backend       *httputil.ReverseProxy
}

func main() {
	ctx := context.Background()
	need := func(k string) string {
		v := os.Getenv(k)
		if v == "" {
			log.Fatalf("%s required", k)
		}
		return v
	}
	peopleFile, database, backendURL := need("FLEET_PEOPLE_FILE"), need("FLEET_DATABASE_URL"), need("FLEET_BACKEND_URL")
	target, err := url.Parse(backendURL)
	if err != nil {
		log.Fatal(err)
	}
	store, err := openStore(ctx, database)
	if err != nil {
		log.Fatal(err)
	}
	a := &App{store: store, people: &People{file: peopleFile, store: store}, browserSecret: os.Getenv("FLEET_BROWSER_SECRET"),
		browserOrigin: os.Getenv("FLEET_BROWSER_ORIGIN"), proxyAuth: os.Getenv("FLEET_BROWSER_PROXY_AUTH") == "true"}
	if a.proxyAuth && a.browserOrigin == "" {
		log.Fatal("Proxy browser auth requires configured browser origin")
	}
	// The Host header passes unchanged: the TypeScript origin check compares Origin with it.
	a.backend = &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) { pr.SetURL(target); pr.Out.Host = pr.In.Host }}
	a.stream = listen(ctx, database)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8787"
	}
	srv := &http.Server{Addr: "127.0.0.1:" + port, Handler: a.routes()}
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, os.Interrupt)
		<-ch
		a.stream.close() // streams end with a close frame before the server waits for them
		c, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		srv.Shutdown(c)
	}()
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// Request: the credential check's outcome and the route's transaction.
type Request struct {
	r      *http.Request
	person *Person
	body   map[string]any
	tx     pgx.Tx
	params []string
}

func (q *Request) ctx() context.Context { return q.r.Context() }

type handler func(*Request) (any, error)

type route struct {
	method string
	re     *regexp.Regexp
	write  bool
	h      handler
	raw    func(*Request, http.ResponseWriter) error
}

// Path parameters as Fastify matches them: one non-empty segment of at most 100 characters (maxParamLength).
const seg = "([^/]{1,100})"

func (a *App) table() []route {
	r := func(method, pattern string, write bool, h handler) route {
		return route{method, regexp.MustCompile("^" + pattern + "$"), write, h, nil}
	}
	return []route{
		r("POST", "/api/sessions", true, a.sessionCreate),
		r("GET", "/api/sessions/"+seg, false, a.sessionGet),
		r("GET", "/api/executions/"+seg+"/events", false, a.eventsGet),
		{method: "GET", re: regexp.MustCompile(`^/api/stream$`), raw: a.serveStream},
	}
}

// simpleBody: a request body Go reads exactly as Fastify would. Anything else (another media type, an empty JSON body,
// invalid JSON, a prototype key, over 1 MiB) goes to TypeScript, which answers it with its own error.
func simpleBody(hr *http.Request, raw []byte) (map[string]any, bool) {
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(hr.Header.Get("Content-Type"), ";", 2)[0]))
	if len(raw) == 0 && ct == "" {
		return nil, true
	}
	if ct != "application/json" || len(raw) == 0 || len(raw) > 1<<20 || bytes.Contains(raw, []byte("__proto__")) || bytes.Contains(raw, []byte("constructor")) {
		return nil, false
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil, false
	}
	m, _ := v.(map[string]any)
	return m, true
}

func (a *App) routes() http.Handler {
	table := a.table()
	return http.HandlerFunc(func(w http.ResponseWriter, hr *http.Request) {
		var rt *route
		var params []string
		if hr.URL.RawPath == "" { // a percent-encoded path is matched by Fastify's own decoder
			for i := range table {
				if m := table[i].re.FindStringSubmatch(hr.URL.Path); m != nil && table[i].method == hr.Method {
					rt, params = &table[i], m[1:]
					break
				}
			}
		}
		if rt == nil {
			a.backend.ServeHTTP(w, hr)
			return
		}
		raw, err := io.ReadAll(io.LimitReader(hr.Body, 1<<20+1))
		if err != nil {
			return
		}
		body, simple := simpleBody(hr, raw)
		if hr.Method == "GET" && len(raw) > 0 {
			simple = false
		}
		if !simple {
			hr.Body = io.NopCloser(io.MultiReader(bytes.NewReader(raw), hr.Body))
			a.backend.ServeHTTP(w, hr)
			return
		}
		q := &Request{r: hr, body: body, params: params}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		err = func() error {
			if err := a.authorize(q); err != nil {
				return err
			}
			if rt.raw != nil {
				return rt.raw(q, w)
			}
			var out any
			if err := a.store.Tx(q.ctx(), scopeOf(q.person), rt.write, "", func(tx pgx.Tx) error {
				q.tx = tx
				var err error
				out, err = rt.h(q)
				return err
			}); err != nil {
				return err
			}
			b, err := marshal(out)
			if err != nil {
				return err
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			_, err = w.Write(b)
			return err
		}()
		if err != nil {
			writeError(w, err)
		}
	})
}

// writeError answers as Fastify's default error handler: {statusCode, error, message}; 42501 (row-level security) is 403.
func writeError(w http.ResponseWriter, err error) {
	var he *HTTPError
	code, msg := 500, err.Error()
	if errors.As(err, &he) {
		code = he.Code
	} else if pgCode(err) == "42501" {
		code, msg = 403, "Not in your scope"
	} else {
		log.Printf("error: %v", err)
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]any{"statusCode": code, "error": http.StatusText(code), "message": msg})
}

// authorize is the browser part of app.ts onRequest and the project-grant preHandler (Go owns no host route): exactly
// `Bearer <browser secret>`, a known person, the origin rules, and a named project the person may see.
func (a *App) authorize(q *Request) error {
	h := q.r.Header
	supplied := strings.TrimPrefix(h.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(supplied), []byte(a.browserSecret)) != 1 {
		return fail(401, "Unauthorized")
	}
	email := h.Get("X-Fleet-Person")
	if email == "" {
		return fail(401, "Person required")
	}
	p := a.people.personFor(q.ctx(), email)
	if p == nil {
		return fail(403, "Unknown person")
	}
	origin, hasOrigin := h["Origin"]
	m := q.r.Method
	if a.proxyAuth && m != "GET" && m != "HEAD" && m != "OPTIONS" && (!hasOrigin || origin[0] != a.browserOrigin) {
		return fail(403, "Origin required")
	}
	if hasOrigin && origin[0] != "" {
		want := a.browserOrigin
		if want == "" {
			want = "http://" + q.r.Host
		}
		if origin[0] != want {
			return fail(403, "Origin rejected")
		}
	}
	q.person = p
	if !p.Owner {
		// args?.projectId ?? req.query?.projectId: a body value of any type other than null wins over the query.
		var project string
		var ok bool
		if v, present := q.body["projectId"]; present && v != nil {
			project, ok = v.(string)
		} else {
			project, ok = firstQuery(q.r, "projectId")
		}
		if ok && !p.mayAccessProject(project) {
			return fail(404, "Unknown project")
		}
	}
	return nil
}

func firstQuery(r *http.Request, k string) (string, bool) {
	v, ok := r.URL.Query()[k]
	if !ok || len(v) != 1 { // Fastify gives an array for a repeated key: not a string, so no check
		return "", false
	}
	return v[0], true
}

func pgCode(err error) string {
	var pe interface{ SQLState() string }
	if errors.As(err, &pe) {
		return pe.SQLState()
	}
	return ""
}
