// Hosts, subscriptions, connections and tokens as tables (migration 78, docs/designs/fleet-standard.md S1, stage 5):
// their records are read and written through their typed queries (queries/records.sql, records.go) like the
// conversation kinds. A record field a query filters, joins or orders, a policy reads (holder) or a state machine owns is
// a column; payload holds the other fields. host.state and connection_key.state are state machines: a put creates a row
// with its state and moves an existing one only through hostTransition (host_states.go) or connectionKeyTransition
// (key_states.go).
package main

import (
	"context"

	"fleet/coordinator/db"
)

// hostStateActive: the host state a record shows by leaving state out.
const hostStateActive = "active"

// connDB: the typed queries on conn (the request's transaction when ctx carries one, else the pool).
func connDB(ctx context.Context, a *App) *db.Queries { return db.New(conn(ctx, a).(db.DBTX)) }
