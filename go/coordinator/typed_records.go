// Hosts, subscriptions, connections and tokens as typed tables (migration 78, docs/designs/fleet-standard.md S1):
// read and written through their typed queries (queries/hosts.sql, queries/accounts.sql, queries/connections.sql). A
// record field a query filters, joins or orders, a policy reads (holder) or a state machine owns is a column; payload
// holds the other fields. host.state and connection_key.state are state machines: a put leaves the state alone (a new
// row starts active) and moves it only through hostTransition (host_states.go) or connectionKeyTransition (key_states.go).
package main

import (
	"context"
	"encoding/json"
	"math"
	"time"

	"fleet/coordinator/db"
)

// hostStateActive: the host state a record shows by leaving state out.
const hostStateActive = "active"

// connDB: the typed queries on conn (the request's transaction when ctx carries one, else the pool).
func connDB(ctx context.Context, a *App) *db.Queries { return db.New(conn(ctx, a).(db.DBTX)) }

// timeMs converts a nullable timestamp to Unix milliseconds; nil gives 0.
func timeMs(t *time.Time) float64 {
	if t == nil {
		return 0
	}
	return math.Round(float64(t.UnixMicro()) / 1000)
}

// timeMsPtr converts a nullable timestamp to nullable Unix milliseconds.
func timeMsPtr(t *time.Time) *float64 {
	if t == nil {
		return nil
	}
	v := timeMs(t)
	return &v
}

// msRaw encodes a nullable timestamp as JSON Unix milliseconds (nil when absent).
func msRaw(t *time.Time) json.RawMessage {
	if t == nil {
		return nil
	}
	return jsonBytes(int64(timeMs(t)))
}

// rawMs decodes a JSON Unix millisecond timestamp into *time.Time (nil when absent or non-numeric).
func rawMs(raw json.RawMessage) *time.Time {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var ms float64
	if json.Unmarshal(raw, &ms) != nil {
		return nil
	}
	t := time.UnixMilli(int64(math.Round(ms))).UTC()
	return &t
}

// floatMsPtr converts non-zero Unix milliseconds to *time.Time (0 gives nil).
func floatMsPtr(ms float64) *time.Time {
	if ms == 0 {
		return nil
	}
	t := time.UnixMilli(int64(math.Round(ms))).UTC()
	return &t
}
