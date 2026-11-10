// Area C helpers (connections, pipelines, Lab). Small duplicates of other areas' helpers are expected; worker D merges them.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// cApp: the running coordinator, set by the area's onStart hook (background work and snapshot publishing need the store).
var cApp *App

func cConfigDir() string   { return envOr("FLEET_PROJECT_CONFIG_DIRECTORY", "config") }
func cProfilesDir() string { return envOr("FLEET_PROFILES_DIRECTORY", "config/agents") }
func cLabDir() string      { return envOr("FLEET_LAB_DIRECTORY", "config/lab") }

func cJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// cClone deep-copies a JSON value (maps and slices), so a stored record never aliases another.
func cClone(v any) any {
	var out any
	json.Unmarshal(cJSON(v), &out)
	return out
}

// cBody: the request body as a JSON object; absent or null is an empty object.
func cBody(q *Request) (doc, error) {
	b := doc{}
	if len(q.body) == 0 || string(q.body) == "null" {
		return b, nil
	}
	if json.Unmarshal(q.body, &b) != nil {
		return nil, fail(400, "Invalid body")
	}
	return b, nil
}

func cNum(v any) (float64, bool) {
	f, ok := v.(float64)
	if !ok {
		if i, ok2 := v.(int64); ok2 {
			return float64(i), true
		}
		if i, ok2 := v.(int); ok2 {
			return float64(i), true
		}
	}
	return f, ok
}

func cInt(v any) (int64, bool) {
	f, ok := cNum(v)
	if !ok || f != float64(int64(f)) || f > 1<<53 || f < -(1<<53) {
		return 0, false
	}
	return int64(f), true
}

func cList(v any) []any { l, _ := v.([]any); return l }

func cKeysOnly(b doc, allowed ...string) bool {
	for k := range b {
		ok := false
		for _, a := range allowed {
			if k == a {
				ok = true
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// cQuery: rows as maps (column name -> value); jsonb decodes to JSON values, timestamps to time.Time.
func cQuery(q *Request, sql string, args ...any) ([]doc, error) {
	rs, err := q.tx.Query(q.ctx(), sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rs, pgx.RowToMap)
	if out == nil {
		out = []map[string]any{}
	}
	return out, err
}

// cAll: every document of a kind, oldest first.
func cAll(q *Request, kind string) ([]doc, error) {
	return rows(q, "SELECT data FROM document WHERE kind=$1 ORDER BY seq", kind)
}

// cGet: one document, nil when absent.
func cGet(q *Request, id, kind string) (doc, error) {
	d, err := getDoc(q, id, kind)
	if errors.Is(err, errNotFound) {
		return nil, nil
	}
	return d, err
}

func cOwnerEmail(a *App) string {
	for _, p := range a.people {
		if p.Owner {
			return p.Email
		}
	}
	return "owner"
}

// director: the request person, else the owner (timers, hosts).
func cDirector(q *Request) string {
	if q.person != nil {
		return q.person.Email
	}
	return cOwnerEmail(q.app)
}

// cTx runs fn in its own write transaction under lock (GLOBAL when empty), as the coordinator.
func cTx(ctx context.Context, a *App, lock string, fn func(q *Request) error) error {
	return a.store.Tx(ctx, nil, true, lock, func(tx pgx.Tx) error {
		return fn(cReq(ctx, a, tx))
	})
}

func cRead(ctx context.Context, a *App, fn func(q *Request) error) error {
	return a.store.Tx(ctx, nil, false, "", func(tx pgx.Tx) error { return fn(cReq(ctx, a, tx)) })
}

// cReq: a request value for background work (no person: the coordinator's own scope).
func cReq(ctx context.Context, a *App, tx pgx.Tx) *Request {
	hr, _ := http.NewRequestWithContext(ctx, "POST", "/", nil)
	return &Request{r: hr, tx: tx, app: a}
}

// cAsk inserts one question (tasks.ts ask); returns its id.
func cAsk(q *Request, kind, text string, projectID any, options any, askedBy string, data doc) (string, error) {
	id := uuid()
	var opts any
	if options != nil {
		opts = cJSON(options)
	}
	if data == nil {
		data = doc{}
	}
	err := exec(q, "INSERT INTO question(id,kind,project_id,text,options,asked_by,data) VALUES($1,$2,$3,$4,$5,$6,$7)", id, kind, projectID, text, opts, askedBy, cJSON(data))
	return id, err
}

// cCreateExecution inserts a typed execution row unless its id or request_id exists (executions.ts createExecution).
func cCreateExecution(q *Request, v doc) (bool, error) {
	state := astr(v["state"])
	if state == "" {
		state = "preparing"
	}
	var cfg any
	if v["configRef"] != nil {
		cfg = cJSON(v["configRef"])
	}
	data := v["data"]
	if data == nil {
		data = doc{}
	}
	var id string
	err := q.tx.QueryRow(q.ctx(), `INSERT INTO execution(id,kind,state,project_id,parent_execution_id,config_ref,data,experiment_id,variant,request_id)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT DO NOTHING RETURNING id`,
		v["id"], v["kind"], state, v["projectId"], v["parentExecutionId"], cfg, cJSON(data), v["experimentId"], v["variant"], v["requestId"]).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// cWriteConnectionCost upserts the connection_cost total of one tool_run.
func cWriteConnectionCost(q *Request, id string, projectID any, cost any) error {
	basis := "measured"
	var c any
	if cost == nil {
		basis = "unavailable"
	} else {
		c = cJSON(cost)
	}
	return exec(q, `INSERT INTO execution_totals(execution_id,project_id,connection_cost,connection_cost_basis) VALUES($1,$2,$3,$4)
	 ON CONFLICT(execution_id) DO UPDATE SET connection_cost=excluded.connection_cost,connection_cost_basis=excluded.connection_cost_basis,computed_at=now()`, id, projectID, c, basis)
}

func cSortedKeys(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// cCanon: canonical JSON with sorted keys (equal values give equal hashes).
func cCanon(v any) string {
	switch x := v.(type) {
	case map[string]any:
		parts := []string{}
		for _, k := range cSortedKeys(x) {
			parts = append(parts, string(cJSON(k))+":"+cCanon(x[k]))
		}
		return "{" + strings.Join(parts, ",") + "}"
	case []any:
		parts := []string{}
		for _, e := range x {
			parts = append(parts, cCanon(e))
		}
		return "[" + strings.Join(parts, ",") + "]"
	default:
		return string(cJSON(x))
	}
}

func cMs(t any) any {
	if tt, ok := t.(time.Time); ok {
		return tt.UnixMilli()
	}
	return nil
}
