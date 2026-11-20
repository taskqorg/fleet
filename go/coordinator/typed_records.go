// Hosts, subscriptions, connections and tokens as tables (migration 78, docs/designs/fleet-standard.md S1, stage 5):
// all/get/put (records.go) serve these kinds through convTables like the conversation kinds (sessions.go). A record
// field a query filters, joins or orders, a policy reads (holder) or a state machine owns is a column; payload holds the
// other fields. host.state and connection_key.state are state machines: a put creates a row with its state and moves
// an existing one only through hostTransition (host_states.go) or connectionKeyTransition (key_states.go).
package main

import (
	"context"

	"fleet/coordinator/db"
)

func init() {
	for kind, t := range map[string]*convTable{
		"host": {table: "host", cols: []convCol{{"hostId", "host_id", "text"}, {"holder", "holder", "text"}, {"state", "state", "hoststate"},
			{"intake", "intake", "text"}, {"secretHash", "secret_hash", "text"}, {"removedSecretHash", "removed_secret_hash", "text"},
			{"projectId", "project_id", "text"}}, state: &convState{col: "state", field: "state", of: hostState, move: hostTransition}},
		"host-observation": {table: "host_observation", cols: []convCol{{"holder", "holder", "text"}, {"lastSeenAt", "last_seen_at", "ms"},
			{"projectId", "project_id", "text"}}},
		"watch": {table: "watch", cols: []convCol{{"sessionId", "session_id", "text"}, {"projectId", "project_id", "text"}}},
		"managed-account": {table: "managed_account", cols: []convCol{{"name", "name", "text"}, {"holder", "holder", "text"}, {"intake", "intake", "text"},
			{"harness", "harness", "text"}, {"provider", "provider", "text"}, {"projectId", "project_id", "text"}}},
		"account-inspection": {table: "account_inspection", cols: []convCol{{"accountId", "account_id", "text"}, {"executionId", "execution_id", "text"},
			{"hostId", "host_id", "text"}, {"projectId", "project_id", "text"}}},
		"account-check": {table: "account_check", cols: []convCol{{"accountId", "account_id", "text"}, {"hostId", "host_id", "text"},
			{"binding", "binding", "text"}, {"projectId", "project_id", "text"}}},
		"account-catalogue": {table: "account_catalogue", cols: []convCol{{"hostId", "host_id", "text"}, {"harness", "harness", "text"},
			{"projectId", "project_id", "text"}}},
		"api-connection": {table: "api_connection", cols: []convCol{{"name", "name", "text"}, {"provider", "provider", "text"}, {"holder", "holder", "text"},
			{"projectId", "project_id", "text"}}},
		"connection-key": {table: "connection_key", cols: []convCol{{"connectionId", "connection_id", "text"}, {"fingerprint", "fingerprint", "text"},
			{"state", "state", "text"}, {"addedAt", "added_at", "ms"}, {"projectId", "project_id", "text"}},
			state: &convState{col: "state", field: "state", of: connectionKeyState, move: connectionKeyTransition}},
		"run-token": {table: "run_token", cols: []convCol{{"sha256", "sha256", "text"}, {"executionId", "execution_id", "text"},
			{"pipelineRunId", "pipeline_run_id", "text"}, {"personal", "personal", "text"}, {"step", "step", "bool"}, {"directedBy", "directed_by", "text"},
			{"createdAt", "created_at", "ms"}, {"revokedAt", "revoked_at", "ms"}, {"directorAccessRevokedAt", "director_access_revoked_at", "ms"},
			{"projectId", "project_id", "text"}}},
		"person-setting": {table: "person_setting", cols: []convCol{{"email", "email", "text"}, {"projectId", "project_id", "text"}}},
	} {
		convTables[kind] = t
	}
}

// convState: a state machine column of a table served through convTables. of reads the stored state (locked, "" when
// the row is new); move is the machine's only writer once the row exists.
type convState struct {
	col, field string
	of         func(q *Request, id string) (string, error)
	move       func(q *Request, id, to string) error
}

// hostStateActive: the host state a record shows by leaving state out.
const hostStateActive = "active"

// connDB: the typed queries on conn (the request's transaction when ctx carries one, else the pool).
func connDB(ctx context.Context, a *App) *db.Queries { return db.New(conn(ctx, a).(db.DBTX)) }
