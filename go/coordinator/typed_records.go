// Typed tables (docs/designs/fleet-standard.md S1): read and written through their typed queries
// (queries/hosts.sql, queries/accounts.sql, queries/connections.sql, queries/sessions.sql, queries/commands.sql,
// queries/agent.sql, queries/events.sql). A record field a query filters, joins or orders, a policy reads (holder)
// or a state machine owns is a column; payload holds the other fields. host.state, connection_key.state and
// command.status are state machines: a put leaves the state alone and moves it only through hostTransition
// (host_states.go), connectionKeyTransition (key_states.go) or commandTransition (commands.go).
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

// int64Ms converts a nullable timestamp to Unix milliseconds; nil gives 0.
func int64Ms(t *time.Time) int64 { return int64(timeMs(t)) }

// int64MsPtr converts a nullable timestamp to nullable Unix milliseconds.
func int64MsPtr(t *time.Time) *int64 {
	if t == nil {
		return nil
	}
	v := int64Ms(t)
	return &v
}

// intMsPtr converts non-zero Unix milliseconds to *time.Time (0 gives nil).
func intMsPtr(ms int64) *time.Time {
	if ms == 0 {
		return nil
	}
	t := time.UnixMilli(ms).UTC()
	return &t
}

// optMsPtr converts nullable Unix milliseconds to *time.Time (nil gives nil).
func optMsPtr(ms *int64) *time.Time {
	if ms == nil {
		return nil
	}
	t := time.UnixMilli(*ms).UTC()
	return &t
}

// recordRaw unmarshals a JSON payload object into a non-nil rawObject.
func recordRaw(payload []byte) (rawObject, error) {
	out := rawObject{}
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &out); err != nil {
			return nil, err
		}
		if out == nil {
			out = rawObject{}
		}
	}
	return out, nil
}

// recordMap unmarshals a JSON payload object and normalizes each value with sortedJSON.
func recordMap(payload []byte) (map[string]json.RawMessage, error) {
	var r map[string]json.RawMessage
	if len(payload) > 0 {
		if err := json.Unmarshal(payload, &r); err != nil {
			return nil, err
		}
	}
	if r == nil {
		r = map[string]json.RawMessage{}
	}
	for k, v := range r {
		var err error
		if r[k], err = sortedJSON(v); err != nil {
			return nil, err
		}
	}
	return r, nil
}
