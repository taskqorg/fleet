// Delete and purge (docs/designs/fleet-system-management.md, Archive, delete, purge). object_delete tombstones a project
// or a Lab experiment with every row that goes with it; system_purge removes tombstoned objects nothing live references.
// Both read one reference map: for each table, the columns that name an object and whether the row belongs to it.
// Migration 67 hides tombstoned rows and every row of a deleted project from every read (RLS); fleet.deleted=on shows
// them to host calls (a stop or teardown of deleted work still lands), to delete and to purge.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

func init() {
	op("object_delete", objectDelete)
	op("system_purge", systemPurge)
}

// refMode: what a row does with the object its column names.
type refMode int

const (
	refOwn         refMode = iota // the row belongs to the object: it goes with it (delete and purge)
	refKeep                       // the row names the object and keeps it while the row stays
	refWithProject                // derived state of the object that goes only with its whole project (resource tables)
	refNone                       // not an object of the map: other kinds, cursors, external or request ids, the change ring
)

// objectRef: table.column names an object of kind. expr is the SQL over the row when it is not the column itself (a
// JSON path, a polymorphic scope); array: the column is a text[] of ids.
type objectRef struct {
	table, column, kind string
	mode                refMode
	expr                string
	array               bool
}

// referenceMap: every column of schema.sql that names an object, and every *_id column (TestReferenceMapCoversSchema),
// except the foreign keys whose reference referenceKeys derives; an entry for a key column overrides its derivation.
var referenceMap = []objectRef{
	{table: "execution", column: "id", kind: "execution", mode: refOwn},
	{table: "execution", column: "project_id", kind: "project", mode: refOwn},
	{table: "execution", column: "task_id", kind: "task", mode: refOwn},
	{table: "execution", column: "experiment_id", kind: "experiment", mode: refOwn},
	{table: "execution", column: "parent_execution_id", kind: "execution", mode: refOwn}, // child runs, pipeline steps, delegations
	{table: "execution", column: "session_id", kind: "session", mode: refOwn},            // a session takes its executions
	{table: "execution", column: "host_id", kind: "host", mode: refKeep},
	{table: "execution", column: "account_id", mode: refNone},
	{table: "execution", column: "environment_id", mode: refNone},
	{table: "execution", column: "request_id", mode: refNone},
	{table: "execution", column: "profile_id", mode: refNone},
	{table: "execution", column: "pipeline_id", mode: refNone},
	{table: "execution", column: "step_id", mode: refNone},
	// Hosts, subscriptions, connections and tokens (migration 78). A run token goes with its execution and with the run
	// whose step it was minted for; an inspection with its execution; a watch with its session.
	{table: "host", column: "id", mode: refNone},
	{table: "host", column: "host_id", mode: refNone}, // the host's own id; system_purge deletes a removed host (host_remove.go)
	{table: "host", column: "project_id", kind: "project", mode: refOwn},
	{table: "host_observation", column: "id", mode: refNone},
	{table: "host_observation", column: "project_id", kind: "project", mode: refOwn},
	{table: "watch", column: "id", mode: refNone},
	{table: "watch", column: "project_id", kind: "project", mode: refOwn},
	{table: "managed_account", column: "id", mode: refNone},
	{table: "managed_account", column: "project_id", kind: "project", mode: refOwn},
	{table: "account_inspection", column: "id", mode: refNone},
	{table: "account_inspection", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "account_inspection", column: "host_id", kind: "host", mode: refKeep},
	{table: "account_inspection", column: "project_id", kind: "project", mode: refOwn},
	{table: "account_check", column: "id", mode: refNone},
	{table: "account_check", column: "host_id", kind: "host", mode: refKeep},
	{table: "account_check", column: "project_id", kind: "project", mode: refOwn},
	{table: "account_catalogue", column: "id", mode: refNone},
	{table: "account_catalogue", column: "host_id", kind: "host", mode: refKeep},
	{table: "account_catalogue", column: "project_id", kind: "project", mode: refOwn},
	{table: "api_connection", column: "id", mode: refNone},
	{table: "api_connection", column: "project_id", kind: "project", mode: refOwn},
	{table: "connection_key", column: "id", mode: refNone},
	{table: "connection_key", column: "project_id", kind: "project", mode: refOwn},
	{table: "run_token", column: "id", mode: refNone},
	{table: "run_token", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "run_token", column: "pipeline_run_id", kind: "execution", mode: refOwn}, // the tokens of a run's steps
	{table: "run_token", column: "project_id", kind: "project", mode: refOwn},
	{table: "person_setting", column: "id", mode: refNone},
	{table: "person_setting", column: "project_id", kind: "project", mode: refOwn},
	// Conversations (migration 77). A session working for a task outlives it (session and task lifecycles are separate):
	// the reference goes stale. A session's transcript (messages, queued messages, transitions, handoffs) belongs to the
	// session: a task's runs in a live session leave its turns in place; events and commands go with their execution too.
	{table: "session", column: "id", kind: "session", mode: refOwn},
	{table: "session", column: "project_id", kind: "project", mode: refOwn},
	{table: "session", column: "pipeline_run_id", kind: "execution", mode: refOwn}, // the sessions of a run's steps
	{table: "session", column: "task_id", kind: "task", mode: refNone},
	{table: "session", column: "host_id", kind: "host", mode: refKeep},
	{table: "session", column: "parent_execution_id", kind: "execution", mode: refNone},
	{table: "session", column: "created_by_execution_id", kind: "execution", mode: refKeep},
	{table: "session", column: "created_by_session_id", kind: "session", mode: refKeep},
	{table: "session", column: "source_session_id", kind: "session", mode: refKeep},
	{table: "message", column: "id", mode: refNone},
	{table: "message", column: "project_id", kind: "project", mode: refOwn},
	{table: "message", column: "execution_id", kind: "execution", mode: refNone},
	{table: "message", column: "request_id", mode: refNone},
	{table: "event", column: "id", mode: refNone},
	{table: "event", column: "project_id", kind: "project", mode: refOwn},
	{table: "queued_message", column: "id", mode: refNone},
	{table: "queued_message", column: "project_id", kind: "project", mode: refOwn},
	{table: "queued_message", column: "task_id", kind: "task", mode: refNone},
	{table: "queued_message", column: "execution_id", kind: "execution", mode: refNone},
	{table: "queued_message", column: "steer_command_id", mode: refNone},
	{table: "queued_message", column: "request_id", mode: refNone},
	{table: "command", column: "id", mode: refNone},
	{table: "command", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "command", column: "project_id", kind: "project", mode: refOwn},
	{table: "command", column: "host_id", kind: "host", mode: refKeep},
	{table: "handoff", column: "id", mode: refNone},
	{table: "handoff", column: "project_id", kind: "project", mode: refOwn},
	{table: "session_fork", column: "id", mode: refNone},
	{table: "session_fork", column: "source_session_id", kind: "session", mode: refKeep},
	{table: "session_fork", column: "project_id", kind: "project", mode: refOwn},
	{table: "session_fork", column: "request_id", mode: refNone},
	{table: "session_transition", column: "id", mode: refNone},
	{table: "session_transition", column: "project_id", kind: "project", mode: refOwn},
	{table: "worker_done", column: "id", mode: refNone},
	{table: "worker_done", column: "project_id", kind: "project", mode: refOwn},
	{table: "agent_tool_receipt", column: "id", mode: refNone},
	{table: "agent_tool_receipt", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "artifact", column: "id", kind: "artifact", mode: refOwn},
	{table: "artifact", column: "project_id", kind: "project", mode: refOwn},
	{table: "artifact", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "artifact", column: "task_id", kind: "task", mode: refOwn},
	{table: "artifact", column: "session_id", kind: "session", mode: refOwn}, // a conversation takes its uploaded attachments
	{table: "artifact", column: "previous_artifact_id", kind: "artifact", mode: refKeep},
	{table: "artifact", column: "derived_from_artifact_id", kind: "artifact", mode: refKeep},
	{table: "artifact", column: "legacy_id", mode: refNone},
	{table: "artifact_copy", column: "artifact_id", kind: "artifact", mode: refOwn},
	{table: "artifact_copy", column: "project_id", kind: "project", mode: refOwn},
	{table: "artifact_copy", column: "upload_id", mode: refNone},
	{table: "artifact_rule_confirmation", column: "project_id", kind: "project", mode: refOwn},
	{table: "budget", column: "id", mode: refNone},
	{table: "budget", column: "scope_id", kind: "project", mode: refOwn, expr: "(CASE WHEN scope='project' THEN scope_id END)"},
	{table: "budget", column: "scope_id", kind: "experiment", mode: refOwn, expr: "(CASE WHEN scope='experiment' THEN scope_id END)"},
	{table: "change", column: "id", mode: refNone}, // the change feed is a ring the stream prunes after 24 hours
	{table: "change", column: "task_id", mode: refNone},
	{table: "change", column: "session_id", mode: refNone},
	{table: "change", column: "host_id", mode: refNone},
	{table: "change", column: "project_id", mode: refNone},
	{table: "closed_delta_identity", column: "id", mode: refNone},
	{table: "definition", column: "id", mode: refNone}, // a purged project keeps its definition row: its id is never reused
	{table: "definition", column: "project_id", mode: refNone},
	{table: "definition_draft", column: "id", mode: refNone},
	{table: "entity_code", column: "entity_id", kind: "task", mode: refOwn},
	{table: "entity_code", column: "entity_id", kind: "question", mode: refOwn},
	{table: "entity_code", column: "entity_id", kind: "session", mode: refOwn},
	{table: "entity_code", column: "project_id", kind: "project", mode: refOwn},
	{table: "execution_totals", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "execution_totals", column: "project_id", kind: "project", mode: refOwn},
	{table: "experiment", column: "id", kind: "experiment", mode: refOwn},
	{table: "experiment", column: "project_id", kind: "project", mode: refOwn},
	{table: "experiment", column: "pipeline_id", mode: refNone},
	{table: "experiment_note", column: "id", mode: refNone},
	{table: "experiment_note", column: "experiment_id", kind: "experiment", mode: refOwn},
	{table: "experiment_note", column: "project_id", kind: "project", mode: refOwn},
	{table: "host_setting", column: "host_id", kind: "host", mode: refKeep},
	{table: "person_grant", column: "object_id", mode: refNone}, // the people file's projection, reloaded from the file
	{table: "price_entry", column: "id", mode: refNone},
	{table: "project_code", column: "project_id", mode: refNone}, // codes are never reused
	{table: "project_write_request", column: "request_id", mode: refNone},
	{table: "project_write_request", column: "project_id", kind: "project", mode: refOwn},
	{table: "purged_hash", column: "project_id", kind: "project", mode: refOwn},
	{table: "question", column: "id", kind: "question", mode: refOwn},
	{table: "question", column: "project_id", kind: "project", mode: refOwn},
	{table: "question", column: "task_id", kind: "task", mode: refOwn},
	{table: "question", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "question", column: "options", kind: "experiment", mode: refOwn, expr: "options->>'experiment_id'"}, // ratings
	{table: "question", column: "data", kind: "experiment", mode: refOwn, expr: "data->>'experimentId'"},        // budget questions
	{table: "question", column: "session_id", kind: "session", mode: refOwn},
	{table: "question", column: "request_id", mode: refNone},
	{table: "resource_build", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_build", column: "after_execution_id", mode: refNone}, // a cursor
	{table: "resource_contribution", column: "execution_id", kind: "execution", mode: refWithProject},
	{table: "resource_contribution", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_cost", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_extreme", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_metadata_repair", column: "repair_id", mode: refNone},
	{table: "resource_metadata_repair", column: "metadata_id", mode: refNone},
	{table: "resource_metadata_repair", column: "after_execution_id", mode: refNone},
	{table: "resource_metric", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_price_ref", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_price_ref", column: "price_id", mode: refNone},
	{table: "resource_project_summary", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_repair_scope", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_tool", column: "project_id", kind: "project", mode: refOwn},
	// An execution's removal is captured as resource work (migration 55); the processor retracts its contribution.
	{table: "resource_valuation", column: "execution_id", kind: "execution", mode: refWithProject},
	{table: "resource_work", column: "work_id", mode: refNone},
	{table: "resource_work", column: "execution_id", kind: "execution", mode: refWithProject},
	{table: "resource_work", column: "repair_id", mode: refNone},
	{table: "resource_work_scope", column: "project_id", kind: "project", mode: refOwn},
	{table: "route_decision", column: "id", mode: refNone},
	{table: "route_decision", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "route_decision", column: "project_id", kind: "project", mode: refOwn},
	{table: "route_decision", column: "host_id", kind: "host", mode: refKeep},
	{table: "score", column: "id", mode: refNone},
	{table: "score", column: "experiment_id", kind: "experiment", mode: refOwn},
	{table: "score", column: "project_id", kind: "project", mode: refOwn},
	{table: "score", column: "trial_ids", kind: "execution", mode: refKeep, array: true},
	{table: "store", column: "id", mode: refNone},
	{table: "store", column: "host_id", kind: "host", mode: refKeep},
	{table: "task", column: "id", kind: "task", mode: refOwn},
	{table: "task", column: "project_id", kind: "project", mode: refOwn},
	{table: "task", column: "accepted_artifact_ids", kind: "artifact", mode: refKeep, array: true},
	{table: "task", column: "created_by_session_id", kind: "session", mode: refKeep},
	{table: "task", column: "created_by_execution_id", kind: "execution", mode: refKeep},
	{table: "task_dependency", column: "task_id", kind: "task", mode: refOwn},
	{table: "task_dependency", column: "project_id", kind: "project", mode: refOwn},
	{table: "task_derived", column: "project_id", kind: "project", mode: refOwn},
	{table: "wake_outbox", column: "wake_id", mode: refNone},
	// Work kit rows (pipeline_work.go, delegation_work.go): they go with their run or experiment by foreign key; a
	// delegation's goes when its step finds the delegation gone.
	{table: "pipeline_work", column: "run_id", mode: refNone},
	{table: "lab_work", column: "experiment_id", mode: refNone},
	{table: "delegation_source_work", column: "delegation_id", mode: refNone},
	{table: "upload_finish_work", column: "upload_id", mode: refNone}, // goes with its upload (foreign key)
	// A pipeline run's plan (migration 81, pipelines.go): it goes with its run by foreign key; the ids an entry names
	// (its step, the executions, command, host and claim of its dispatch, its step's native execution, output artifact
	// and provider job) are evidence of the run, not references.
	{table: "pipeline_run", column: "run_id", mode: refNone},
	{table: "pipeline_step", column: "step_id", mode: refNone},
	{table: "pipeline_step", column: "ref_execution_id", mode: refNone},
	{table: "pipeline_step", column: "ref_source_run_id", mode: refNone},
	{table: "pipeline_step", column: "ref_source_step_id", mode: refNone},
	{table: "pipeline_step", column: "dispatch_claim_id", mode: refNone},
	{table: "pipeline_step", column: "dispatch_owner_boot_id", mode: refNone},
	{table: "pipeline_step", column: "dispatch_native_execution_id", mode: refNone},
	{table: "pipeline_step", column: "dispatch_command_id", mode: refNone},
	{table: "pipeline_step", column: "dispatch_host_id", mode: refNone},
	{table: "pipeline_step", column: "native_execution_id", mode: refNone},
	{table: "pipeline_step", column: "output_artifact_id", mode: refNone},
	{table: "pipeline_step", column: "job_id", mode: refNone},
	// Migration 81. The audit (audit_log, transport_audit) and purge tasks outlive what they name: no refOwn column.
	{table: "audit_log", column: "id", mode: refNone},
	{table: "audit_log", column: "project_id", kind: "project", mode: refNone},
	{table: "transport_audit", column: "id", mode: refNone},
	{table: "transport_audit", column: "execution_id", kind: "execution", mode: refNone},
	{table: "transport_audit", column: "conflict_id", mode: refNone},
	{table: "transport_audit", column: "request_id", mode: refNone},
	{table: "purge_task", column: "id", mode: refNone},
	{table: "purge_task", column: "store_id", mode: refNone},
	{table: "reconciliation", column: "id", mode: refNone},
	{table: "transport_conflict", column: "id", mode: refNone},
	{table: "transport_conflict", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "transport_conflict", column: "host_id", kind: "host", mode: refKeep},
	{table: "transport_conflict", column: "original_event_id", mode: refNone},
	{table: "transport_conflict", column: "collision_event_id", mode: refNone},
	{table: "problem_dismissal", column: "project_id", kind: "project", mode: refOwn},
	{table: "project_recommendation", column: "id", mode: refNone},
	{table: "project_recommendation", column: "project_id", kind: "project", mode: refOwn},
	{table: "project_recommendation", column: "method_id", mode: refNone}, // a catalogue method
	{table: "upload", column: "id", mode: refNone},
	{table: "upload", column: "project_id", kind: "project", mode: refOwn},
	{table: "upload", column: "artifact_id", kind: "artifact", mode: refNone}, // the artifact a finished upload stored
	{table: "delivery_state", column: "id", mode: refNone},
	{table: "delivery_state", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_sample", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_pr", column: "id", mode: refNone},
	{table: "delivery_pr", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_run", column: "id", mode: refNone},
	{table: "delivery_run", column: "run_id", mode: refNone}, // a GitHub run
	{table: "delivery_run", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_run_read", column: "run_id", mode: refNone},
	{table: "delivery_run_read", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_release", column: "id", mode: refNone},
	{table: "delivery_release", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_baseline", column: "id", mode: refNone},
	{table: "delivery_baseline", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_check", column: "id", mode: refNone},
	{table: "delivery_check", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_settings", column: "id", mode: refNone},
	{table: "delivery_settings", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_history_state", column: "id", mode: refNone},
	{table: "delivery_history_state", column: "project_id", kind: "project", mode: refOwn},
	{table: "delivery_ui_seen", column: "project_id", kind: "project", mode: refOwn},
}

// deliveryTables: the delivery tables (migration 82), each with a project and a tombstone.
var deliveryTables = []string{"delivery_state", "delivery_sample", "delivery_pr", "delivery_run", "delivery_run_read", "delivery_release",
	"delivery_baseline", "delivery_check", "delivery_settings", "delivery_history_state", "delivery_ui_seen"}

// objectTables: the tables whose rows are objects of a kind; the cascade of a root is closed over their refOwn columns.
var objectTables = []struct{ kind, table, filter string }{
	{"task", "task", ""}, {"execution", "execution", ""}, {"experiment", "experiment", ""},
	{"session", "session", ""}, {"question", "question", ""}, {"artifact", "artifact", ""},
}

// tombstoned: the tables with a tombstoned_at column (migration 67).
var tombstoned = append([]string{"execution", "task", "experiment", "question", "artifact", "score", "experiment_note",
	"session", "message", "event", "queued_message", "command", "handoff", "session_fork", "session_transition", "worker_done",
	"host", "host_observation", "watch", "managed_account", "account_inspection", "account_check", "account_catalogue", "api_connection",
	"connection_key", "run_token", "person_setting", "upload", "problem_dismissal", "project_recommendation", "transport_conflict"}, deliveryTables...)

// keptRows: rows of a table that neither go with an object nor keep one. None now: the audit records that outlive what
// they describe (the journal of M0) have their own tables without a refOwn column (migration 82).
var keptRows = map[string]string{}

// purgeOrder: the tables system_purge deletes from, referencing rows before the rows they reference (foreign keys).
var purgeOrder = append([]string{"score", "experiment_note", "artifact_copy", "artifact", "question", "execution_totals", "route_decision",
	"agent_tool_receipt", "closed_delta_identity", "resource_valuation", "resource_contribution", "problem_dismissal",
	"transport_conflict", "upload", "project_recommendation", "watch", "run_token", "account_inspection", "account_check", "account_catalogue",
	"connection_key", "api_connection", "managed_account", "host_observation", "host", "person_setting", "message", "event",
	"queued_message", "command", "handoff", "session_fork", "session_transition", "worker_done", "session", "execution",
	"resource_work", "resource_work_scope", "task_dependency", "task_derived", "task", "entity_code", "experiment", "budget",
	"project_write_request", "purged_hash", "artifact_rule_confirmation", "resource_project_summary", "resource_metric",
	"resource_extreme", "resource_tool", "resource_cost", "resource_price_ref", "resource_build", "resource_repair_scope"}, deliveryTables...)

// cascade: a root object (project or experiment) and the ids of every object that goes with it, by kind.
type cascade struct {
	kind, id string
	sets     map[string][]string
	refs     []objectRef // references(): referenceMap with the derived foreign keys
}

// referenceKeys: the references of the schema's foreign keys, read once. A key to an object table (objectTables) names
// that object: its row goes with it under ON DELETE CASCADE (refOwn) and keeps it otherwise (refKeep); a key to any
// other table names no object (refNone: the row follows its parent by the key). A referenceMap entry for the same
// column wins.
var referenceKeys struct {
	mu   sync.Mutex
	refs []objectRef
}

// references: referenceMap and the foreign keys it does not list.
func references(q *Request) ([]objectRef, error) {
	referenceKeys.mu.Lock()
	defer referenceKeys.mu.Unlock()
	if referenceKeys.refs != nil {
		return referenceKeys.refs, nil
	}
	keys, err := q.db().ReferenceKeys(q.ctx())
	if err != nil {
		return nil, err
	}
	refs := slices.Clone(referenceMap)
	for _, k := range keys {
		if slices.ContainsFunc(refs, func(r objectRef) bool { return r.table == k.Child && r.column == k.Col }) {
			continue
		}
		r := objectRef{table: k.Child, column: k.Col, mode: refNone}
		for _, o := range objectTables {
			if o.table == k.Parent {
				r.kind, r.mode = o.kind, refKeep
				if k.Cascades {
					r.mode = refOwn
				}
			}
		}
		refs = append(refs, r)
	}
	referenceKeys.refs = refs
	return refs, nil
}

// cascadeOf: the root and every object that goes with it, closed over the refOwn columns of objectTables. The caller's
// transaction decides what is visible: delete reads live rows, purge reads with fleet.deleted on.
func cascadeOf(q *Request, kind, id string) (*cascade, error) {
	refs, err := references(q)
	if err != nil {
		return nil, err
	}
	c := &cascade{kind: kind, id: id, sets: map[string][]string{kind: {id}}, refs: refs}
	for grew := true; grew; {
		grew = false
		for _, o := range objectTables {
			a := &args{c: c}
			where := c.goesWith(o.table, a)
			if where == "false" {
				continue
			}
			ids, err := cascadeIDs(q, sqlCascadeIDs(o.table, o.filter, where), a.vals)
			if err != nil {
				return nil, err
			}
			if len(ids) > len(c.sets[o.kind]) {
				c.sets[o.kind], grew = ids, true
			}
		}
	}
	return c, nil
}

// cascadeIDs: the ids an assembled cascade statement selects.
func cascadeIDs(q *Request, sql string, args []any) ([]string, error) {
	type idRow struct {
		ID string `db:"id"`
	}
	rs, err := typedRows[idRow](q, sql, args)
	ids := make([]string, len(rs))
	for i, r := range rs {
		ids[i] = r.ID
	}
	return ids, err
}

// counts: per kind what goes with the root (the root itself included), artifacts with their stored bytes.
func (c *cascade) counts(q *Request) ([]contract.KindCount, int64, error) {
	out := []contract.KindCount{}
	var bytes int64
	for _, k := range contract.ObjectKindValues {
		n := len(c.sets[string(k)])
		if n == 0 {
			continue
		}
		kc := contract.KindCount{Kind: k, Count: int64(n)}
		if k == contract.ObjectKindArtifact {
			var err error
			if bytes, err = q.db().CleanupStoredBytes(q.ctx(), c.sets["artifact"]); err != nil {
				return nil, 0, err
			}
			kc.Bytes = &bytes
		}
		out = append(out, kc)
	}
	return out, bytes, nil
}

// blockedBy: the first live row outside the cascade that keeps one of its objects, "" when none:
// "<table>.<column> by <document kind or table> <row id>" (a conversation table names the record field its column
// holds, convFieldOf). One scan per table: its refKeep columns are ORed.
func (c *cascade) blockedBy(q *Request) (string, error) {
	// An open session working for a task depends on it (an archived one outlives it; the reference goes stale).
	if len(c.sets["task"]) > 0 {
		sid, err := q.db().CleanupOpenSessionOfTasks(q.ctx(), db.CleanupOpenSessionOfTasksParams{Tasks: c.sets["task"], Sessions: nonNilStrs(c.sets["session"])})
		if err == nil {
			return "session.task_id by open session " + sid + " (archive it first)", nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	// Executions of the cascade that belong to live work outside it stay with that work: runs of a live task (delete
	// the task first), trials of a live experiment, children of an unfinished parent run that waits for them.
	if len(c.sets["execution"]) > 0 {
		reason, err := q.db().CleanupExecutionKept(q.ctx(), db.CleanupExecutionKeptParams{Executions: c.sets["execution"],
			Tasks: nonNilStrs(c.sets["task"]), Experiments: nonNilStrs(c.sets["experiment"])})
		if err == nil {
			return reason, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return "", err
		}
	}
	tables, refs := []string{}, map[string][]objectRef{}
	for _, r := range c.refs {
		if r.mode != refKeep || len(c.sets[r.kind]) == 0 {
			continue
		}
		if refs[r.table] == nil {
			tables = append(tables, r.table)
		}
		refs[r.table] = append(refs[r.table], r)
	}
	for _, t := range tables {
		a := &args{c: c}
		conds, label := []string{}, "CASE"
		for i, r := range refs[t] {
			m := r.match(a.of(r.kind))
			conds = append(conds, m)
			label += fmt.Sprintf(" WHEN %s THEN %d", m, i)
		}
		where := "(" + strings.Join(conds, " OR ") + ") AND NOT coalesce(" + c.goesWith(t, a) + ", false)"
		type keptRow struct {
			Ref int32  `db:"ref"`
			Rid string `db:"rid"`
		}
		rs, err := typedRows[keptRow](q, sqlKeptBy(t, label, where, keptRows[t]), a.vals)
		if err != nil {
			return "", err
		}
		if len(rs) > 0 {
			r := refs[t][rs[0].Ref]
			return strings.TrimSpace(fmt.Sprintf("%s.%s by %s %s", t, recordFieldOf(t, r.sql()), t, rs[0].Rid)), nil
		}
	}
	return "", nil
}

// showDeleted: this transaction sees tombstoned rows and the rows of deleted projects (migration 67).
func showDeleted(q *Request) error { return q.db().TxShowDeleted(q.ctx(), "on") }

func hideDeleted(q *Request) error { return q.db().TxShowDeleted(q.ctx(), "") }

// deleteRoot: the root row as the caller sees it; 404 when unknown, 409 when already deleted.
func deleteRoot(q *Request, kind contract.DeleteKind, id string) error {
	exists := q.db().DeleteRootExperiment
	switch kind {
	case contract.DeleteKindProject:
		exists = func(ctx context.Context, p db.DeleteRootExperimentParams) (bool, error) {
			return q.db().DeleteRootProject(ctx, db.DeleteRootProjectParams(p))
		}
	case contract.DeleteKindTask:
		exists = func(ctx context.Context, p db.DeleteRootExperimentParams) (bool, error) {
			return q.db().DeleteRootTask(ctx, db.DeleteRootTaskParams(p))
		}
	case contract.DeleteKindSession:
		exists = func(ctx context.Context, p db.DeleteRootExperimentParams) (bool, error) {
			return q.db().DeleteRootSession(ctx, db.DeleteRootSessionParams(p))
		}
	}
	if err := showDeleted(q); err != nil {
		return err
	}
	ok, err := exists(q.ctx(), db.DeleteRootExperimentParams{ID: id, Gone: true})
	if err == nil {
		err = hideDeleted(q)
	}
	if err != nil || ok {
		if err == nil {
			err = fail(409, "Already deleted")
		}
		return err
	}
	if ok, err := exists(q.ctx(), db.DeleteRootExperimentParams{ID: id}); err != nil || !ok {
		if err == nil {
			err = fail(404, "Unknown "+string(kind))
		}
		return err
	}
	return nil
}

// deletePreview: the reply both the preview and the confirm compute (NFR: preview equals effect).
func deletePreview(q *Request, b *contract.ObjectDeleteRequest) (*cascade, contract.ObjectDeleteReply, error) {
	r := contract.ObjectDeleteReply{Kind: b.Kind, Id: b.Id, Affected: []contract.KindCount{}}
	if err := deleteRoot(q, b.Kind, b.Id); err != nil {
		return nil, r, err
	}
	c, err := cascadeOf(q, string(b.Kind), b.Id)
	if err != nil {
		return nil, r, err
	}
	// Lab: a live row outside the object that keeps one of its objects (another experiment's score citing a trial)
	// refuses the delete, so nothing live ever points at a hidden row.
	if by, err := c.blockedBy(q); err != nil || by != "" {
		if err == nil {
			err = fail(409, "Kept by "+by)
		}
		return nil, r, err
	}
	if r.Affected, _, err = c.counts(q); err != nil {
		return nil, r, err
	}
	// Running: what the confirm stops (agent runs and host jobs) or cancels (running pipeline runs); their native
	// children (child_agent, tool_run) end with them.
	r.Running, err = q.db().CleanupRunningCount(q.ctx(), c.sets["execution"])
	return c, r, err
}

func objectDelete(q *Request, b *contract.ObjectDeleteRequest) (contract.ObjectDeleteReply, error) {
	var out contract.ObjectDeleteReply
	var c *cascade
	if b.Preview {
		err := runTx(q.ctx(), q.app, q, false, "", func(q *Request) error {
			var err error
			c, out, err = deletePreview(q, b)
			return err
		})
		return out, err
	}
	// Pipeline runs are cancelled first, each in its own transaction under its run lock (pipelineCancel), as a Lab
	// cell cancel does; the step a host holds is stopped like any execution. The experiments stop first, so Lab
	// launches no new trial meanwhile; the checks run in that same transaction, so a delete refused there stops nothing
	// (one refused by the last transaction, after a new citing score, leaves the experiment stopped; delete it again).
	// ponytail: a pipeline run a task starts between these transactions is tombstoned uncancelled; receipts of its
	// steps (host calls see deleted rows) could still advance it. Cancel it in the last transaction if that is seen.
	runs, running := []string{}, int64(0)
	if err := runTx(q.ctx(), q.app, q, true, "", func(q *Request) error {
		var err error
		if c, out, err = deletePreview(q, b); err != nil {
			return err
		}
		running = out.Running
		if err := experimentsStop(q, c.sets["experiment"], "deleted"); err != nil {
			return err
		}
		runs, err = q.db().CleanupRunningPipelineRuns(q.ctx(), c.sets["execution"])
		return err
	}); err != nil {
		return out, err
	}
	for _, id := range runs {
		own, err := pipelineCancel(q.ctx(), q.app, id)
		if err != nil {
			return out, fmt.Errorf("cancel %s: %w", id, err)
		}
		if own != "" {
			if err := stopExecution(q.ctx(), q.app, own); err != nil && !isNotFound(err) {
				return out, err
			}
		}
	}
	// DISPATCH too: a dispatch pass or a promotion cannot add an execution to the cascade while it is read and marked.
	err := runTxLocks(q.ctx(), q.app, q, true, []string{GLOBAL, DISPATCH}, func(q *Request) error {
		var err error
		if c, out, err = deletePreview(q, b); err != nil {
			return err
		}
		out.Running = running // what this confirm stops, counted before the pipeline cancels above
		if err := showDeleted(q); err != nil {
			return err
		}
		return tombstone(q, c, &out)
	})
	return out, err
}

// tombstone: stop, revoke, tear down and mark everything of the cascade; the caller's transaction shows deleted rows.
func tombstone(q *Request, c *cascade, out *contract.ObjectDeleteReply) error {
	ids := c.sets["execution"]
	live, err := q.db().CleanupLiveRuns(q.ctx(), ids)
	if err != nil {
		return err
	}
	for _, x := range live {
		if _, err := executionStop(q, x); err != nil {
			return fmt.Errorf("stop %v: %w", x, err)
		}
		if err := q.db().CleanupStopDetail(q.ctx(), x); err != nil {
			return err
		}
	}
	if n, err := q.db().CleanupRevokeRunTokens(q.ctx(), db.CleanupRevokeRunTokensParams{Ids: ids, At: ptr(time.UnixMilli(nowMs()))}); err != nil {
		return err
	} else if n > 0 {
		publish(q)
	}
	if err := experimentsStop(q, c.sets["experiment"], "deleted"); err != nil {
		return err
	}
	if err := questionsWithdraw(q, c.sets["question"]); err != nil {
		return err
	}
	// Messages still waiting for a turn never start one (host receipts see deleted rows and promote queued messages).
	if err := q.db().CleanupQueuedMessages(q.ctx(), c.sets["session"]); err != nil {
		return err
	}
	teardowns := int64(0)
	for _, sid := range c.sets["session"] {
		torn, err := sessionDeleteArchive(q, sid)
		if err != nil {
			return err
		}
		if torn {
			teardowns++
		}
	}
	out.Teardowns = teardowns
	// The objects get their tombstone; an experiment's own rows (scores, notes, the events and messages of its trials)
	// too, since only a deleted project's rows are hidden by project.
	for _, o := range objectTables {
		if len(c.sets[o.kind]) == 0 {
			continue
		}
		if _, err := q.tx.Exec(q.ctx(), sqlTombstoneObjects(o.table, o.filter), c.sets[o.kind]); err != nil {
			return err
		}
	}
	for _, t := range tombstoned {
		a := &args{c: c}
		where := c.goesWith(t, a)
		if where == "false" {
			continue
		}
		if c.kind == "project" && t != "transport_conflict" { // the project's own rows are hidden by project already (a transport conflict has none)
			where += " AND project_id IS DISTINCT FROM (" + a.of("project") + ")[1]"
		}
		if _, err := q.tx.Exec(q.ctx(), sqlTombstoneRows(t, where, keptRows[t]), a.vals...); err != nil {
			return err
		}
	}
	// Budgets are settings of the object and its experiments: they go now, with its secrets.
	if err := q.db().CleanupBudgets(q.ctx(), db.CleanupBudgetsParams{Scope: c.kind, ScopeID: c.id, Experiments: nonNilStrs(c.sets["experiment"])}); err != nil {
		return err
	}
	if c.kind == "project" {
		raw, err := q.db().CleanupProjectHead(q.ctx(), c.id)
		if err != nil {
			return err
		}
		conns, err := headConnections([]byte(raw))
		if err != nil {
			return err
		}
		if err := q.db().CleanupProjectDeleted(q.ctx(), c.id); err != nil {
			return err
		}
		if _, err := removeUnusedConnections(q, conns); err != nil {
			return err
		}
		invalidateEnvCacheOnCommit(q)
	}
	// Audit: no project, so it outlives the project's purge.
	if err := audit(q, c.kind+":"+c.id, "deletedAt", nil, nowMs(), auditExtra{Note: js(out.Affected)}); err != nil {
		return err
	}
	out.Deleted = true
	return nil
}

// purgeRoots: the tombstoned objects of kind, oldest first. An experiment of a deleted project goes with its project.
func purgeRoots(q *Request, kind contract.PurgeKind) ([]string, error) {
	read := q.db().PurgeRootProjects
	switch kind {
	case contract.PurgeKindExperiment:
		read = q.db().PurgeRootExperiments
	case contract.PurgeKindTask:
		read = q.db().PurgeRootTasks
	case contract.PurgeKindSession:
		read = q.db().PurgeRootSessions
	}
	ids, err := read(q.ctx())
	return nonNilStrs(ids), err
}

// purgeTask: a store object or a span deletion still to do after the rows went (a purge_task row: its store, and the
// rest as its payload).
type purgeTask struct {
	ID      string  `json:"-"`
	StoreID *string `json:"-"`
	purgeTaskPayload
}

type purgeTaskPayload struct {
	Objects      []purgeObject `json:"objects,omitempty"`
	ExecutionIDs []string      `json:"executionIds,omitempty"` // whose spans go
}

// purgeObject: an object on the task's store: a content object (projectId, scope, hash: it goes unless a copy maps to
// it again) or an upload's staging object (uploadId: its multipart upload is aborted first).
type purgeObject struct {
	Key       string  `json:"key"`
	UploadID  *string `json:"uploadId,omitempty"`
	ProjectID *string `json:"projectId,omitempty"`
	Scope     string  `json:"scope,omitempty"`
	Hash      *string `json:"hash,omitempty"`
}

// addPurgeTask: a store object or a span deletion still to do after the rows went.
func addPurgeTask(q *Request, t purgeTask) error {
	return q.db().PurgeTaskAdd(q.ctx(), db.PurgeTaskAddParams{ID: newID(), StoreID: t.StoreID, Payload: jsonBytes(t.purgeTaskPayload)})
}

// purgeCascade: delete every row of a cascade, in purgeOrder; the store objects no remaining artifact shares and the
// spans of its executions become purge tasks.
func purgeCascade(q *Request, c *cascade) error {
	objects, err := q.db().CleanupCascadeObjects(q.ctx(), nonNilStrs(c.sets["artifact"]))
	if err != nil {
		return err
	}
	for _, t := range purgeOrder {
		a := &args{c: c}
		where := c.goesWith(t, a)
		if where == "false" {
			continue
		}
		if _, err := q.tx.Exec(q.ctx(), sqlPurgeRows(t, where, keptRows[t]), a.vals...); err != nil {
			return fmt.Errorf("purge %s: %w", t, err)
		}
	}
	// An object is content-addressed (project/scope/hash) and may be shared: runPurgeTasks decides under the write lock.
	keys, seen := map[string][]purgeObject{}, map[string]bool{}
	for _, o := range objects {
		store := o.StoreID
		if k := deref(o.StagingKey); k != "" {
			// An upload in progress: its multipart parts are aborted, then its staging object deleted.
			keys[store] = append(keys[store], purgeObject{Key: k, UploadID: o.UploadID})
		}
		x := purgeObject{Key: keyOf(&Artifact{ProjectID: o.ProjectID, Scope: o.Scope, ContentHash: o.ContentHash}), ProjectID: o.ProjectID,
			Scope: o.Scope, Hash: ptr(o.ContentHash)}
		if k := store + "\x00" + x.Key; !seen[k] {
			seen[k] = true
			keys[store] = append(keys[store], x)
		}
	}
	for _, store := range slices.Sorted(maps.Keys(keys)) {
		if err := addPurgeTask(q, purgeTask{StoreID: ptr(store), purgeTaskPayload: purgeTaskPayload{Objects: keys[store]}}); err != nil {
			return err
		}
	}
	if len(c.sets["execution"]) > 0 {
		if err := addPurgeTask(q, purgeTask{purgeTaskPayload: purgeTaskPayload{ExecutionIDs: c.sets["execution"]}}); err != nil {
			return err
		}
	}
	if c.kind == "project" {
		return q.db().CleanupProjectPurged(q.ctx(), c.id)
	}
	return nil
}

// purgeRootTx: one tombstoned root in its own transaction: blocked (a live row keeps it) or purged.
func purgeRootTx(q *Request, kind contract.PurgeKind, id string, preview bool) (rows []contract.KindCount, bytes int64, blocked string, err error) {
	err = runTx(q.ctx(), q.app, q, !preview, "", func(q *Request) error {
		if err := showDeleted(q); err != nil {
			return err
		}
		if err := q.db().TxSetPurge(q.ctx()); err != nil {
			return err
		}
		c, err := cascadeOf(q, string(kind), id)
		if err != nil {
			return err
		}
		if blocked, err = c.blockedBy(q); err != nil || blocked != "" {
			return err
		}
		// A stop or teardown a live host has not finished keeps the object: purging it would leave the agent running and
		// the worktree in place. One for a removed (or purged) host is never fetched: it goes with the object.
		pending, err := q.db().CleanupPendingCommands(q.ctx(), db.CleanupPendingCommandsParams{Executions: nonNilStrs(c.sets["execution"]), DefaultHost: defaultHost()})
		if err != nil {
			return err
		}
		for _, p := range pending {
			if !p.Dead {
				blocked = fmt.Sprintf("pending %s command %s on host %s", p.Kind, p.ID, deref(p.Host))
				return nil
			}
		}
		if rows, bytes, err = c.counts(q); err != nil || preview {
			return err
		}
		if err := purgeCascade(q, c); err != nil {
			return err
		}
		return audit(q, string(kind)+":"+id, "purge", nil, rows, auditExtra{})
	})
	return
}

// purgeBudget: a system_purge call stops starting work after this long and says more; the site's proxy ends a request
// at 100 s (an empty reply there while the server went on).
var purgeBudget = 60 * time.Second

const deferred = "deferred: time budget; run system_purge again"

func systemPurge(q *Request, b *contract.SystemPurgeRequest) (contract.SystemPurgeReply, error) {
	out := contract.SystemPurgeReply{Removed: []contract.PurgeCount{}, Kept: []contract.PurgeCount{}, Rows: []contract.KindCount{}}
	start, more := time.Now(), false
	over := func() bool {
		if time.Since(start) > purgeBudget {
			more = true
			return true
		}
		return false
	}
	kinds := contract.PurgeKindValues
	if len(b.Kinds) > 0 {
		kinds = b.Kinds
	}
	// Hosts first, as the preview sees them: tombstoned rows count as references, so a host a purged project named goes
	// at the next purge.
	if slices.Contains(kinds, contract.PurgeKindHost) {
		var removed, kept contract.PurgeCount
		if err := runTx(q.ctx(), q.app, q, !b.Preview, "", func(q *Request) error {
			if err := showDeleted(q); err != nil {
				return err
			}
			var err error
			removed, kept, err = purgeHosts(q, b.Preview)
			return err
		}); err != nil {
			return out, err
		}
		out.Removed, out.Kept = append(out.Removed, removed), append(out.Kept, kept)
	}
	rows := map[contract.ObjectKind]*contract.KindCount{}
	// Store objects and spans of earlier purges first, with a third of the budget: roots that stay kept cannot starve them.
	if !b.Preview {
		if _, err := runPurgeTasks(q.ctx(), q.app, start.Add(purgeBudget/3)); err != nil {
			return out, err
		}
	}
	// Every root of every kind is checked before any goes, as the preview sees them: a root kept only by another
	// tombstoned root goes at the next purge, so the confirm removes what the preview listed. The roots of all kinds
	// go in one random order: a call that stops at its budget reaches others next time, so roots a live row keeps
	// cannot starve the rest. A preview cut by the budget (more) is partial: the confirm may reach other roots.
	type root struct {
		kind contract.PurgeKind
		id   string
	}
	order := []contract.PurgeKind{contract.PurgeKindExperiment, contract.PurgeKindTask, contract.PurgeKindSession, contract.PurgeKindProject}
	roots := []root{}
	for _, kind := range order {
		if !slices.Contains(kinds, kind) {
			continue
		}
		if err := runTx(q.ctx(), q.app, q, false, "", func(q *Request) error {
			if err := showDeleted(q); err != nil {
				return err
			}
			ids, err := purgeRoots(q, kind)
			for _, id := range ids {
				roots = append(roots, root{kind, id})
			}
			return err
		}); err != nil {
			return out, err
		}
	}
	rand.Shuffle(len(roots), func(i, j int) { roots[i], roots[j] = roots[j], roots[i] })
	kept := map[contract.PurgeKind][]contract.PurgeReason{}
	removed := map[contract.PurgeKind]*contract.PurgeCount{}
	for _, kind := range order {
		removed[kind] = &contract.PurgeCount{Kind: kind, Ids: []string{}, Bytes: new(int64)}
		kept[kind] = []contract.PurgeReason{}
	}
	if !b.Preview {
		free := []root{}
		for _, r := range roots {
			if time.Since(start) > purgeBudget/2 { // the checks leave the deletes half the budget
				more = true
				kept[r.kind] = append(kept[r.kind], contract.PurgeReason{Id: r.id, Reason: deferred})
				continue
			}
			_, _, blocked, err := purgeRootTx(q, r.kind, r.id, true)
			if err != nil {
				return out, err
			}
			if blocked == "" {
				free = append(free, r)
			} else {
				kept[r.kind] = append(kept[r.kind], contract.PurgeReason{Id: r.id, Reason: blocked})
			}
		}
		roots = free
	}
	for _, r := range roots {
		if over() {
			kept[r.kind] = append(kept[r.kind], contract.PurgeReason{Id: r.id, Reason: deferred})
			continue
		}
		rs, n, blocked, err := purgeRootTx(q, r.kind, r.id, b.Preview)
		if err != nil {
			return out, fmt.Errorf("purge %s %s: %w", r.kind, r.id, err)
		}
		if blocked != "" {
			kept[r.kind] = append(kept[r.kind], contract.PurgeReason{Id: r.id, Reason: blocked})
			continue
		}
		rm := removed[r.kind]
		rm.Ids, *rm.Bytes = append(rm.Ids, r.id), *rm.Bytes+n
		for _, x := range rs {
			if rows[x.Kind] == nil {
				rows[x.Kind] = &contract.KindCount{Kind: x.Kind}
			}
			rows[x.Kind].Count += x.Count
			if x.Bytes != nil {
				sum := *x.Bytes
				if rows[x.Kind].Bytes != nil {
					sum += *rows[x.Kind].Bytes
				}
				rows[x.Kind].Bytes = &sum
			}
		}
	}
	for _, kind := range order {
		if !slices.Contains(kinds, kind) {
			continue
		}
		rm := removed[kind]
		rm.Count = int64(len(rm.Ids))
		out.Removed, out.Kept = append(out.Removed, *rm), append(out.Kept, keptCount(kind, kept[kind]))
	}
	for _, k := range contract.ObjectKindValues {
		if rows[k] != nil {
			out.Rows = append(out.Rows, *rows[k])
		}
	}
	var left int64
	var err error
	if !b.Preview {
		left, err = runPurgeTasks(q.ctx(), q.app, start.Add(purgeBudget))
	} else {
		err = runTx(q.ctx(), q.app, q, false, "", func(q *Request) error {
			var err error
			left, err = q.db().PurgeTaskCount(q.ctx())
			return err
		})
	}
	out.Purged = !b.Preview
	out.RetryLater, out.More = &left, &more
	return out, err
}

func keptCount(kind contract.PurgeKind, kept []contract.PurgeReason) contract.PurgeCount {
	ids := make([]string, len(kept))
	for i, k := range kept {
		ids[i] = k.Id
	}
	return contract.PurgeCount{Kind: kind, Count: int64(len(kept)), Ids: ids, Reasons: kept}
}

// runPurgeTasks: delete the store objects and spans of earlier purges; what fails stays for the next purge, and a
// failed task does not stop the others. Returns the tasks left. Each store object is deleted in its own transaction
// holding its object lock (objectLock, which every copy insert of that content takes too), after checking that no
// copy on that store maps to it again: an upload or a write that deduplicated onto it since the purge keeps it. Span
// deletes run outside any transaction.
func runPurgeTasks(ctx context.Context, a *App, deadline time.Time) (int64, error) {
	var tasks []purgeTask
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		rs, err := q.db().PurgeTasks(q.ctx())
		tasks = nil // a retried transaction starts over
		for _, r := range rs {
			t := purgeTask{ID: r.ID, StoreID: r.StoreID}
			if json.Unmarshal(r.Payload, &t.purgeTaskPayload) == nil {
				tasks = append(tasks, t)
			}
		}
		return err
	}); err != nil {
		return 0, err
	}
	left := int64(0)
	// Spans of every task go in batched statements (one lightweight DELETE took ~125 ms on prod, one per purged root).
	var spanTasks, others []purgeTask
	for _, t := range tasks {
		if len(t.ExecutionIDs) > 0 && t.StoreID == nil {
			spanTasks = append(spanTasks, t)
		} else {
			others = append(others, t)
		}
	}
	// The ids of every span task in chunks of 500 (each a URL parameter of one DELETE), the deadline checked before
	// each; a failed chunk does not stop the next. A task goes once all its ids went; one partly done keeps the rest.
	all := []string{}
	for _, t := range spanTasks {
		all = append(all, t.ExecutionIDs...)
	}
	gone := map[string]bool{}
	for i := 0; i < len(all) && !time.Now().After(deadline); i += 500 {
		part := all[i:min(i+500, len(all))]
		if err := spanDeleteExecutions(ctx, part); err != nil {
			slog.Warn("purge: spans", "err", err)
			continue
		}
		for _, id := range part {
			gone[id] = true
		}
	}
	if len(spanTasks) > 0 {
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
			for _, t := range spanTasks {
				rest := []string{}
				for _, id := range t.ExecutionIDs {
					if !gone[id] {
						rest = append(rest, id)
					}
				}
				var err error
				switch {
				case len(rest) == 0:
					err = q.db().PurgeTaskDelete(q.ctx(), t.ID)
				case len(rest) < len(t.ExecutionIDs):
					next := t.purgeTaskPayload // a copy: a retried transaction reads the task as stored
					next.ExecutionIDs = rest
					err = q.db().PurgeTaskUpdate(q.ctx(), db.PurgeTaskUpdateParams{ID: t.ID, Payload: jsonBytes(next)})
				}
				if err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return left, err
		}
		for _, t := range spanTasks {
			for _, id := range t.ExecutionIDs {
				if !gone[id] {
					left++
					break
				}
			}
		}
	}
	for _, t := range others {
		if time.Now().After(deadline) { // the rest waits for the next purge
			left++
			continue
		}
		rest := []purgeObject{}
		if store := deref(t.StoreID); store != "" {
			s, err := storeGet(ctx, a, store)
			var s3 *S3
			if err == nil && s.State != "unreachable" {
				s3, err = tunnel(s)
			}
			if err != nil && !isNotFound(err) { // a store gone with its host has nothing left to delete
				slog.Warn("purge: store", "store", store, "err", err)
			}
			for _, o := range t.Objects {
				if isNotFound(err) {
					break
				}
				if s3 == nil || time.Now().After(deadline) { // the rest waits for the next purge
					rest = append(rest, o)
					continue
				}
				if e := runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
					if err := showDeleted(q); err != nil {
						return err
					}
					if err := objectLock(ctx, q.tx, store, o.Key); err != nil {
						return err
					}
					if o.Hash != nil {
						if used, err := q.db().CleanupObjectUsed(q.ctx(), db.CleanupObjectUsedParams{StoreID: store, ProjectID: o.ProjectID, Scope: o.Scope,
							ContentHash: *o.Hash}); err != nil || used {
							return err
						}
					}
					if u := deref(o.UploadID); u != "" {
						s3.AbortMultipartUpload(s.Bucket, o.Key, u) // an upload already finished or aborted answers an error
					}
					return s3.Delete(s.Bucket, o.Key)
				}); e != nil {
					slog.Warn("purge: object", "store", store, "key", o.Key, "err", e)
					rest = append(rest, o)
				}
			}
			t.Objects = rest
		}
		if err := runTx(ctx, a, nil, true, "", func(q *Request) error {
			if len(rest) == 0 {
				return q.db().PurgeTaskDelete(q.ctx(), t.ID)
			}
			// An update, never an insert: a task another purge finished meanwhile stays gone.
			return q.db().PurgeTaskUpdate(q.ctx(), db.PurgeTaskUpdateParams{ID: t.ID, Payload: jsonBytes(t.purgeTaskPayload)})
		}); err != nil {
			return left, err
		}
		if len(rest) > 0 {
			left++
		}
	}
	return left, nil
}

// objectLock: the transaction lock of one store object (content-addressed key): purge's check-and-delete and every
// copy insert of the same content serialize on it, outside the canonical lock order (a leaf lock, held until the
// transaction ends; its holder waits on no other lock). ponytail: purge holds it across the store DELETE, so a
// reserve of that same content, holding GLOBAL, waits for one store round trip; a pending-delete marker if that shows.
func objectLock(ctx context.Context, tx pgx.Tx, storeID, key string) error {
	return db.New(tx).AdvisoryXactLock(ctx, "object:"+storeID+":"+key)
}

// recordTables: the tables whose rows are records (queries/records.sql): a reason names a column by its record field.
var recordTables = []string{"session", "message", "event", "queued_message", "command", "handoff", "session_fork", "session_transition",
	"worker_done", "host", "host_observation", "managed_account", "account_inspection", "account_check", "account_catalogue", "api_connection",
	"connection_key", "run_token"}

// recordFieldOf: the record field a record table's column holds (project_id: projectId), else col.
func recordFieldOf(table, col string) string {
	if !slices.Contains(recordTables, table) {
		return col
	}
	switch {
	case table == "event" && col == "event_seq":
		return "seq"
	case table == "event" && col == "at":
		return "timestamp"
	}
	parts := strings.Split(col, "_")
	for i := 1; i < len(parts); i++ {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}
