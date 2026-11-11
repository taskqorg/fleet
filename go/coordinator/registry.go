package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Route registry: the one router. Domain files call these from init(); patterns are net/http ServeMux patterns
// ("GET /api/sessions/{id}", read with q.r.PathValue).
//   route        one transaction around the handler: GET a read snapshot, otherwise one GLOBAL write.
//   longRoute    no surrounding transaction (q.tx nil); the handler opens its own through q.app.store.Tx.
//   publicRoute  matched before the credential check; a plain http.HandlerFunc.
//   operation    POST /api/ops/{name}: read selects a read snapshot or one GLOBAL write.
//   onStart      called by main after the store opens; background tickers stop on ctx.
// A handler may return a raw value instead of JSON.

type raw func(http.ResponseWriter) error

// route kinds: per-route transaction (read snapshot for GET, one GLOBAL write otherwise); txNone runs without one.
const (
	txRead = iota
	txWrite
	txNone
)

type registeredRoute struct {
	pattern string
	h       handler
	tx      int // txRead, txWrite or txNone
	public  http.HandlerFunc
}

type registeredOp struct {
	read bool
	h    handler
}

var (
	routeTable []registeredRoute
	operations = map[string]registeredOp{}
	starters   []func(context.Context, *App)
)

func txOf(pattern string) int {
	if len(pattern) > 4 && pattern[:4] == "GET " {
		return txRead
	}
	return txWrite
}

func route(pattern string, h handler) {
	routeTable = append(routeTable, registeredRoute{pattern, h, txOf(pattern), nil})
}
func longRoute(pattern string, h handler) {
	routeTable = append(routeTable, registeredRoute{pattern, h, txNone, nil})
}
func publicRoute(pattern string, h http.HandlerFunc) {
	routeTable = append(routeTable, registeredRoute{pattern, nil, txNone, h})
}
func operation(name string, read bool, h handler) { operations[name] = registeredOp{read, h} }

// typedOp: an operation on its generated request and reply types (contracts/ops.schema.json). The body decodes into Req:
// fields the request type does not name are ignored, a named field of the wrong JSON type is 400.
func typedOp[Req, Rep any](h func(q *Request, r *Req) (Rep, error)) handler {
	return func(q *Request) (any, error) {
		var r Req
		if len(q.body) > 0 && string(q.body) != "null" {
			if err := exactKeys(q.body, reflect.TypeOf(r)); err != nil {
				return nil, err
			}
			if err := json.Unmarshal(q.body, &r); err != nil {
				var te *json.UnmarshalTypeError
				if errors.As(err, &te) && te.Field != "" {
					return nil, fail(400, "Invalid "+te.Field+": expected "+te.Type.String())
				}
				return nil, fail(400, "Invalid body")
			}
		}
		return h(q, &r)
	}
}

// exactKeys refuses a missing required field, and a body key that names a request field only case-insensitively. encoding/json matches field names
// case-insensitively and the last key wins, so {"executionid": x} would override an executionId the agent tool path
// bound (bound in agent.go) and act on another execution.
func exactKeys(body json.RawMessage, t reflect.Type) error {
	if t.Kind() != reflect.Struct {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(body, &m) != nil {
		return nil // not an object: decode reports it
	}
	for i := 0; i < t.NumField(); i++ {
		tag, opts, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		for k := range m {
			if k != tag && strings.EqualFold(k, tag) {
				return fail(400, "Unknown field "+k+": use "+tag)
			}
		}
		// A field the schema requires has no omitempty: a missing one is 400, not its zero value (a limit of 0).
		if _, ok := m[tag]; !ok && !strings.Contains(opts, "omitempty") {
			return fail(400, "Expected "+tag)
		}
	}
	return nil
}

// checkFirst: a role check before the body is read, so a caller without the role learns nothing from a 400.
func checkFirst(check func(*Request) error, h handler) handler {
	return func(q *Request) (any, error) {
		if err := check(q); err != nil {
			return nil, err
		}
		return h(q)
	}
}

func operatorFirst(h handler) handler { return checkFirst(operatorOnly, h) }

func ownerFirst(msg string, h handler) handler {
	return checkFirst(func(q *Request) error { return ownerOnly(q, msg) }, h)
}

// asT: rows and records read as maps, as their contract type. For values whose map keys are fixed by the query or the
// builder (a SELECT column list); a reply built field by field names its struct directly.
func asT[T any](v any) (T, error) {
	var t T
	err := json.Unmarshal(jsonBytes(v), &t)
	return t, err
}

// recOf: an open record as the contract's map; never nil, so a required record is written as {}.
func recOf(v any) map[string]json.RawMessage {
	if m := rawMap(v); m != nil {
		return m
	}
	return map[string]json.RawMessage{}
}

// timeText: a SELECTed timestamp as its JSON text (RFC 3339), as a map reply wrote it.
func timeText(v any) string {
	switch t := v.(type) {
	case time.Time:
		return t.Format(time.RFC3339Nano)
	case string:
		return t
	}
	return fmt.Sprint(v)
}

func recsOf(vs []doc) []map[string]json.RawMessage {
	out := make([]map[string]json.RawMessage, 0, len(vs))
	for _, v := range vs {
		out = append(out, recOf(v))
	}
	return out
}
func onStart(fn func(ctx context.Context, a *App)) { starters = append(starters, fn) }

func startAll(ctx context.Context, a *App) {
	for _, fn := range starters {
		fn(ctx, a)
	}
}

// registered builds the mux of every registered route, with the /api/ops dispatcher when any operation exists.
func (a *App) registered() *http.ServeMux {
	mux := http.NewServeMux()
	for _, rt := range routeTable {
		if rt.public != nil {
			mux.HandleFunc(rt.pattern, rt.public)
			continue
		}
		rt := rt
		mux.HandleFunc(rt.pattern, func(w http.ResponseWriter, hr *http.Request) { a.serve(w, hr, rt.tx, rt.h) })
	}
	if len(operations) > 0 {
		mux.HandleFunc("POST /api/ops/{name}", func(w http.ResponseWriter, hr *http.Request) {
			op, ok := operations[hr.PathValue("name")]
			if !ok {
				a.serve(w, hr, txNone, func(*Request) (any, error) { return nil, fail(404, "Unknown operation") })
				return
			}
			kind := txWrite
			if op.read {
				kind = txRead
			}
			a.serve(w, hr, kind, op.h)
		})
	}
	return mux
}

// routes is the server handler: the registered mux; GET /api/stream after the credential check; any other unmatched
// path is a JSON 404 after the credential check.
func (a *App) routes() http.Handler {
	mux := a.registered()
	return http.HandlerFunc(func(w http.ResponseWriter, hr *http.Request) {
		if _, p := mux.Handler(hr); p != "" {
			mux.ServeHTTP(w, hr)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		q := &Request{r: hr, app: a}
		err := a.authorize(q)
		if err == nil && hr.Method == "GET" && hr.URL.Path == "/api/stream" {
			// The stream runs its own short reads: no body, no person gate, no surrounding transaction (stream.go).
			err = a.serveStream(q, w)
		} else if err == nil {
			err = fail(404, "Route "+hr.Method+":"+hr.URL.Path+" not found")
		}
		if err != nil {
			writeError(w, err)
		}
	})
}

// serve: credential check, body, the route's transaction, then JSON (or the raw writer) or the error.
func (a *App) serve(w http.ResponseWriter, hr *http.Request, kind int, h handler) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	q := &Request{r: hr, app: a}
	var out any
	err := a.authorize(q)
	if err == nil {
		err = a.readBody(q)
	}
	if err == nil {
		err = a.guard(q)
	}
	if err == nil && kind == txNone {
		out, err = h(q)
	} else if err == nil {
		err = a.store.Tx(q.ctx(), scopeOf(q.person), kind == txWrite, "", func(tx pgx.Tx) error {
			q.tx = tx
			var err error
			out, err = h(q)
			return err
		})
	}
	if err == nil {
		if r, ok := out.(raw); ok {
			err = r(w)
		} else {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			err = json.NewEncoder(w).Encode(out)
		}
	}
	if err != nil {
		writeError(w, err)
	}
}

// guard is the app.ts preHandler person gate on every route and operation, whatever registered it: architect operations and
// routes need the architect role (or the held subscription), a named project must be granted. gatedObjOp, gatedRoute and gatedOp apply the
// same gate again; it is idempotent.
func (a *App) guard(q *Request) error {
	if q.person == nil || fullAccess(q.person) {
		return nil
	}
	if name, ok := strings.CutPrefix(q.r.URL.Path, "/api/ops/"); ok {
		return a.guardOp(q, name, q.body)
	}
	return a.guardRoute(q)
}
