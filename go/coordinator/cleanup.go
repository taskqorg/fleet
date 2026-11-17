// Delete and purge (docs/designs/fleet-system-management.md, Archive, delete, purge). object_delete tombstones a project
// or a Lab experiment with every row that goes with it; system_purge removes tombstoned objects nothing live references.
// Both read one reference map: for each table, the columns that name an object and whether the row belongs to it.
// Migration 67 hides tombstoned rows and every row of a deleted project from every read (RLS); fleet.deleted=on shows
// them to host calls (a stop or teardown of deleted work still lands), to delete and to purge.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"strings"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

func init() {
	longOp("object_delete", ownerFirst("Only the owner deletes", typedOp(objectDelete)))
	longOp("system_purge", ownerFirst("Only the owner purges", typedOp(systemPurge)))
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

func (r objectRef) sql() string {
	if r.expr != "" {
		return r.expr
	}
	return r.column
}

func (r objectRef) match(param string) string {
	if r.array {
		return r.sql() + " && " + param
	}
	return r.sql() + " = ANY(" + param + ")"
}

// referenceMap: every column of schema.sql that names an object, and every *_id column (TestReferenceMapCoversSchema).
var referenceMap = []objectRef{
	{table: "execution", column: "id", kind: "execution", mode: refOwn},
	{table: "execution", column: "project_id", kind: "project", mode: refOwn},
	{table: "execution", column: "task_id", kind: "task", mode: refOwn},
	{table: "execution", column: "experiment_id", kind: "experiment", mode: refOwn},
	{table: "execution", column: "parent_execution_id", kind: "execution", mode: refOwn}, // child runs, pipeline steps, delegations
	{table: "execution", column: "session_id", kind: "session", mode: refKeep},
	{table: "execution", column: "host_id", kind: "host", mode: refKeep},
	{table: "execution", column: "account_id", mode: refNone},
	{table: "execution", column: "environment_id", mode: refNone},
	{table: "execution", column: "request_id", mode: refNone},
	{table: "execution", column: "profile_id", mode: refNone},
	{table: "execution", column: "pipeline_id", mode: refNone},
	{table: "execution", column: "step_id", mode: refNone},
	{table: "document", column: "id", kind: "session", mode: refOwn},
	{table: "document", column: "project_id", kind: "project", mode: refOwn},
	{table: "document", column: "data", kind: "execution", mode: refOwn, expr: "data->>'executionId'"},   // events, commands, run tokens, conflicts
	{table: "document", column: "data", kind: "session", mode: refOwn, expr: "data->>'sessionId'"},       // messages, transitions, queued messages
	{table: "document", column: "data", kind: "execution", mode: refOwn, expr: "data->>'pipelineRunId'"}, // the sessions of a run's steps
	{table: "document", column: "data", kind: "task", mode: refKeep, expr: "data->>'taskId'"},
	{table: "document", column: "data", kind: "host", mode: refKeep, expr: "data->>'hostId'"},
	{table: "document", column: "data", kind: "execution", mode: refKeep, expr: "data->>'createdByExecutionId'"},
	{table: "document", column: "data", kind: "session", mode: refKeep, expr: "data->>'createdBySessionId'"},
	{table: "document", column: "data", kind: "session", mode: refKeep, expr: "data->>'sourceSessionId'"},
	{table: "document", column: "data", kind: "execution", mode: refKeep, expr: "data->>'continuationExecutionId'"},
	{table: "agent_tool_receipt", column: "id", mode: refNone},
	{table: "agent_tool_receipt", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "artifact", column: "id", kind: "artifact", mode: refOwn},
	{table: "artifact", column: "project_id", kind: "project", mode: refOwn},
	{table: "artifact", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "artifact", column: "task_id", kind: "task", mode: refOwn},
	{table: "artifact", column: "previous_artifact_id", kind: "artifact", mode: refKeep},
	{table: "artifact", column: "derived_from_artifact_id", kind: "artifact", mode: refKeep},
	{table: "artifact", column: "legacy_id", mode: refNone},
	{table: "artifact_copy", column: "artifact_id", kind: "artifact", mode: refOwn},
	{table: "artifact_copy", column: "project_id", kind: "project", mode: refOwn},
	{table: "artifact_copy", column: "store_id", mode: refNone}, // a store goes with its removed host (host purge)
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
	{table: "closed_delta_identity", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "definition", column: "id", mode: refNone}, // a purged project keeps its definition row: its id is never reused
	{table: "definition", column: "project_id", mode: refNone},
	{table: "definition_draft", column: "id", mode: refNone},
	{table: "definition_revision", column: "id", mode: refNone},
	{table: "definition_revision", column: "kind", mode: refNone}, // with id, its definition
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
	{table: "person_grant", column: "email", mode: refNone},
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
	{table: "question", column: "session_id", kind: "session", mode: refKeep},
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
	{table: "resource_repair_scope", column: "repair_id", mode: refNone},
	{table: "resource_repair_scope", column: "project_id", kind: "project", mode: refOwn},
	{table: "resource_tool", column: "project_id", kind: "project", mode: refOwn},
	// An execution's removal is captured as resource work (migration 55); the processor retracts its contribution.
	{table: "resource_valuation", column: "execution_id", kind: "execution", mode: refWithProject},
	{table: "resource_work", column: "work_id", mode: refNone},
	{table: "resource_work", column: "execution_id", kind: "execution", mode: refWithProject},
	{table: "resource_work", column: "repair_id", mode: refNone},
	{table: "resource_work_scope", column: "work_id", mode: refNone},
	{table: "resource_work_scope", column: "project_id", kind: "project", mode: refOwn},
	{table: "route_decision", column: "id", mode: refNone},
	{table: "route_decision", column: "execution_id", kind: "execution", mode: refOwn},
	{table: "route_decision", column: "project_id", kind: "project", mode: refOwn},
	{table: "route_decision", column: "host_id", kind: "host", mode: refKeep},
	{table: "score", column: "id", mode: refNone},
	{table: "score", column: "experiment_id", kind: "experiment", mode: refOwn},
	{table: "score", column: "project_id", kind: "project", mode: refOwn},
	{table: "score", column: "trial_ids", kind: "execution", mode: refKeep, array: true},
	{table: "score", column: "source_execution_id", kind: "execution", mode: refKeep},
	{table: "score", column: "question_id", kind: "question", mode: refKeep},
	{table: "score", column: "replaces_score_id", mode: refNone}, // a score of the same experiment
	{table: "store", column: "id", mode: refNone},
	{table: "store", column: "host_id", kind: "host", mode: refKeep},
	{table: "task", column: "id", kind: "task", mode: refOwn},
	{table: "task", column: "project_id", kind: "project", mode: refOwn},
	{table: "task", column: "accepted_artifact_ids", kind: "artifact", mode: refKeep, array: true},
	{table: "task", column: "created_by_session_id", kind: "session", mode: refKeep},
	{table: "task", column: "created_by_execution_id", kind: "execution", mode: refKeep},
	{table: "task_dependency", column: "task_id", kind: "task", mode: refOwn},
	{table: "task_dependency", column: "depends_on_task_id", kind: "task", mode: refKeep},
	{table: "task_dependency", column: "project_id", kind: "project", mode: refOwn},
	{table: "task_derived", column: "task_id", kind: "task", mode: refOwn},
	{table: "task_derived", column: "project_id", kind: "project", mode: refOwn},
	{table: "wake_outbox", column: "wake_id", mode: refNone},
}

// objectTables: the tables whose rows are objects of a kind; the cascade of a root is closed over their refOwn columns.
var objectTables = []struct{ kind, table, filter string }{
	{"task", "task", ""}, {"execution", "execution", ""}, {"experiment", "experiment", ""},
	{"session", "document", "kind='session'"}, {"question", "question", ""}, {"artifact", "artifact", ""},
}

// tombstoned: the tables with a tombstoned_at column (migration 67).
var tombstoned = []string{"execution", "task", "experiment", "question", "artifact", "document", "score", "experiment_note"}

// keptRows: rows that neither go with an object nor keep one: audit records outlive what they describe (the journal of M0).
var keptRows = map[string]string{"document": "kind NOT IN ('setting-change','transport-audit','purge-task')"}

// purgeOrder: the tables system_purge deletes from, referencing rows before the rows they reference (foreign keys).
var purgeOrder = []string{"score", "experiment_note", "artifact_copy", "artifact", "question", "execution_totals", "route_decision",
	"agent_tool_receipt", "closed_delta_identity", "resource_valuation", "resource_contribution", "document", "execution",
	"resource_work", "resource_work_scope", "task_dependency", "task_derived", "task", "entity_code", "experiment", "budget",
	"project_write_request", "purged_hash", "artifact_rule_confirmation", "resource_project_summary", "resource_metric",
	"resource_extreme", "resource_tool", "resource_cost", "resource_price_ref", "resource_build", "resource_repair_scope"}

// cascade: a root object (project or experiment) and the ids of every object that goes with it, by kind.
type cascade struct {
	kind, id string
	sets     map[string][]string
}

// args builds one SQL predicate's parameters: one text[] per kind.
type args struct {
	c    *cascade
	vals []any
	at   map[string]int
}

func (a *args) of(kind string) string {
	if a.at == nil {
		a.at = map[string]int{}
	}
	n, ok := a.at[kind]
	if !ok {
		a.vals = append(a.vals, a.c.sets[kind])
		n = len(a.vals)
		a.at[kind] = n
	}
	return fmt.Sprintf("$%d::text[]", n)
}

// goesWith: the predicate of rows of table that belong to the cascade, "false" when none can.
func (c *cascade) goesWith(table string, a *args) string {
	conds := []string{}
	for _, r := range referenceMap {
		if r.table == table && (r.mode == refOwn || (r.mode == refWithProject && c.kind == "project")) && len(c.sets[r.kind]) > 0 {
			conds = append(conds, r.match(a.of(r.kind)))
		}
	}
	if len(conds) == 0 {
		return "false"
	}
	return "(" + strings.Join(conds, " OR ") + ")"
}

func andFilter(filter, where string) string {
	if filter == "" {
		return where
	}
	return filter + " AND " + where
}

// cascadeOf: the root and every object that goes with it, closed over the refOwn columns of objectTables. The caller's
// transaction decides what is visible: delete reads live rows, purge reads with fleet.deleted on.
func cascadeOf(q *Request, kind, id string) (*cascade, error) {
	c := &cascade{kind: kind, id: id, sets: map[string][]string{kind: {id}}}
	for grew := true; grew; {
		grew = false
		for _, o := range objectTables {
			a := &args{c: c}
			where := c.goesWith(o.table, a)
			if where == "false" {
				continue
			}
			rs, err := queryRows(q, "SELECT id FROM "+o.table+" WHERE "+andFilter(o.filter, where)+" ORDER BY id", a.vals...)
			if err != nil {
				return nil, err
			}
			if len(rs) > len(c.sets[o.kind]) {
				ids := make([]string, len(rs))
				for i, r := range rs {
					ids[i] = astr(r["id"])
				}
				c.sets[o.kind], grew = ids, true
			}
		}
	}
	return c, nil
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
			if err := q.tx.QueryRow(q.ctx(), "SELECT coalesce(sum(size),0)::bigint FROM artifact WHERE id=ANY($1) AND state='stored'", c.sets["artifact"]).Scan(&bytes); err != nil {
				return nil, 0, err
			}
			kc.Bytes = &bytes
		}
		out = append(out, kc)
	}
	return out, bytes, nil
}

// blockedBy: the first live row outside the cascade that keeps one of its objects ("table.column"), "" when none.
func (c *cascade) blockedBy(q *Request) (string, error) {
	for _, r := range referenceMap {
		if r.mode != refKeep || len(c.sets[r.kind]) == 0 {
			continue
		}
		a := &args{c: c}
		ref := r.match(a.of(r.kind))
		where := ref + " AND NOT coalesce(" + c.goesWith(r.table, a) + ", false)"
		if ok, err := has(q, "SELECT 1 FROM "+r.table+" WHERE "+andFilter(keptRows[r.table], where), a.vals...); err != nil {
			return "", err
		} else if ok {
			return r.table + "." + r.sql(), nil
		}
	}
	return "", nil
}

// showDeleted: this transaction sees tombstoned rows and the rows of deleted projects (migration 67).
func showDeleted(q *Request) error {
	return exec(q, "SELECT set_config('fleet.deleted','on',true)")
}

func hideDeleted(q *Request) error {
	return exec(q, "SELECT set_config('fleet.deleted','',true)")
}

// deleteRoot: the root row as the caller sees it; 404 when unknown, 409 when already deleted.
func deleteRoot(q *Request, kind contract.DeleteKind, id string) error {
	live, gone := "SELECT 1 FROM experiment WHERE id=$1", "SELECT 1 FROM experiment WHERE id=$1 AND tombstoned_at IS NOT NULL"
	if kind == contract.DeleteKindProject {
		live, gone = "SELECT 1 FROM definition WHERE kind='project' AND id=$1 AND deleted_at IS NULL", "SELECT 1 FROM definition WHERE kind='project' AND id=$1 AND deleted_at IS NOT NULL"
	}
	if err := showDeleted(q); err != nil {
		return err
	}
	ok, err := has(q, gone, id)
	if err == nil {
		err = hideDeleted(q)
	}
	if err != nil || ok {
		if err == nil {
			err = fail(409, "Already deleted")
		}
		return err
	}
	if ok, err := has(q, live, id); err != nil || !ok {
		if err == nil {
			err = fail(404, "Unknown "+string(kind))
		}
		return err
	}
	return nil
}

// liveRuns: the executions the confirm stops (agent runs and host jobs) or cancels (running pipeline runs); their
// native children (child_agent, tool_run) end with them.
const liveRuns = "(" + execKinds + " AND state IN ('preparing','running','waiting')) OR (kind='pipeline_run' AND state='running')"

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
	err = q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM execution WHERE id=ANY($1) AND ("+liveRuns+")", c.sets["execution"]).Scan(&r.Running)
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
	runs := []string{}
	if err := runTx(q.ctx(), q.app, q, true, "", func(q *Request) error {
		var err error
		if c, out, err = deletePreview(q, b); err != nil {
			return err
		}
		if err := exec(q, "UPDATE experiment SET state='stopped',stop_reason='deleted' WHERE id=ANY($1) AND state IN ('running','paused')", c.sets["experiment"]); err != nil {
			return err
		}
		rs, err := queryRows(q, "SELECT id FROM execution WHERE id=ANY($1) AND kind='pipeline_run' AND state='running' ORDER BY seq", c.sets["execution"])
		for _, r := range rs {
			runs = append(runs, astr(r["id"]))
		}
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
	err := runTx(q.ctx(), q.app, q, true, "", func(q *Request) error {
		var err error
		if c, out, err = deletePreview(q, b); err != nil {
			return err
		}
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
	live, err := queryRows(q, "SELECT id FROM execution WHERE id=ANY($1) AND "+execKinds+" AND state IN ('preparing','running','waiting') ORDER BY seq", ids)
	if err != nil {
		return err
	}
	for _, x := range live {
		if _, err := executionStop(q, astr(x["id"])); err != nil {
			return fmt.Errorf("stop %v: %w", x["id"], err)
		}
		if err := exec(q, "UPDATE execution SET data=data||'{\"stopDetail\":\"deleted\"}' WHERE id=$1", x["id"]); err != nil {
			return err
		}
	}
	if tag, err := q.tx.Exec(q.ctx(), "UPDATE document SET data=data||jsonb_build_object('revokedAt',$2::bigint) WHERE kind='run-token' AND data->>'executionId'=ANY($1) AND NOT data ? 'revokedAt'", ids, nowMs()); err != nil {
		return err
	} else if tag.RowsAffected() > 0 {
		publish(q)
	}
	if err := exec(q, "UPDATE experiment SET state='stopped',stop_reason='deleted' WHERE id=ANY($1) AND state IN ('running','paused')", c.sets["experiment"]); err != nil {
		return err
	}
	if err := exec(q, "UPDATE question SET state='withdrawn' WHERE id=ANY($1) AND state='open'", c.sets["question"]); err != nil {
		return err
	}
	teardowns := int64(0)
	for _, sid := range c.sets["session"] {
		s, err := get(q, sid, "session")
		if err != nil {
			return err
		}
		if s["archivedAt"] != nil {
			continue
		}
		if pending, err := all(q, "command", doc{"sessionId": sid, "kind": "teardown", "status": "pending"}); err != nil || len(pending) > 0 {
			if err != nil {
				return err
			}
			continue
		}
		es, err := sessionExecutions(q, sid)
		if err != nil {
			return err
		}
		var last doc
		for _, e := range es {
			if astr(e["hostId"]) != "" {
				last = e
			}
		}
		s["archivedAt"] = nowMs()
		if err := put(q, "session", s); err != nil {
			return err
		}
		if last == nil {
			continue
		}
		// Queued after the stop: the executor keeps a worktree with uncommitted or unpushed work (cleanup.keepWhen).
		if t, err := teardownCommand(q, s, last, false); err != nil {
			return err
		} else if t["kind"] == "teardown" {
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
		if err := exec(q, "UPDATE "+o.table+" SET tombstoned_at=now() WHERE "+andFilter(o.filter, "id=ANY($1) AND tombstoned_at IS NULL"), c.sets[o.kind]); err != nil {
			return err
		}
	}
	for _, t := range tombstoned {
		a := &args{c: c}
		where := c.goesWith(t, a)
		if where == "false" {
			continue
		}
		if c.kind == "project" { // the project's own rows are hidden by project already
			where += " AND project_id IS DISTINCT FROM (" + a.of("project") + ")[1]"
		}
		if err := exec(q, "UPDATE "+t+" SET tombstoned_at=now() WHERE "+andFilter(keptRows[t], where+" AND tombstoned_at IS NULL"), a.vals...); err != nil {
			return err
		}
	}
	// Budgets are settings of the object and its experiments: they go now, with its secrets.
	if err := exec(q, "DELETE FROM budget WHERE (scope=$1 AND scope_id=$2) OR (scope='experiment' AND scope_id=ANY($3))", c.kind, c.id, c.sets["experiment"]); err != nil {
		return err
	}
	if c.kind == "project" {
		var raw []byte
		if err := q.tx.QueryRow(q.ctx(), `SELECT r.content FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
		  WHERE d.kind='project' AND d.id=$1`, c.id).Scan(&raw); err != nil {
			return err
		}
		conns, err := headConnections(raw)
		if err != nil {
			return err
		}
		if err := exec(q, "UPDATE definition SET deleted_at=now(),retired_at=coalesce(retired_at,now()) WHERE kind='project' AND id=$1", c.id); err != nil {
			return err
		}
		if _, err := removeUnusedConnections(q, conns); err != nil {
			return err
		}
		invalidateEnvCacheOnCommit(q)
	}
	// Audit: a setting-change row until the journal (M0); no projectId, so it outlives the project's purge.
	if err := put(q, "setting-change", doc{"id": newID(), "target": c.kind + ":" + c.id, "field": "deletedAt", "previous": nil, "value": nowMs(),
		"actor": actor(q, "operator"), "at": nowMs(), "note": js(out.Affected)}); err != nil {
		return err
	}
	out.Deleted = true
	return nil
}

// purgeRoots: the tombstoned objects of kind, oldest first. An experiment of a deleted project goes with its project.
func purgeRoots(q *Request, kind contract.PurgeKind) ([]string, error) {
	sql := "SELECT id FROM definition WHERE kind='project' AND deleted_at IS NOT NULL AND purged_at IS NULL ORDER BY deleted_at,id"
	if kind == contract.PurgeKindExperiment {
		sql = "SELECT id FROM experiment WHERE tombstoned_at IS NOT NULL AND NOT project_id=ANY(fleet_deleted_projects()) ORDER BY tombstoned_at,id"
	}
	rs, err := queryRows(q, sql)
	out := []string{}
	for _, r := range rs {
		out = append(out, astr(r["id"]))
	}
	return out, err
}

// addPurgeTask: a store object or a span deletion still to do after the rows went (document kind purge-task).
func addPurgeTask(q *Request, t doc) error {
	t["id"], t["createdAt"] = newID(), nowMs()
	return put(q, "purge-task", t)
}

// purgeCascade: delete every row of a cascade, in purgeOrder; the store objects no remaining artifact shares and the
// spans of its executions become purge tasks.
func purgeCascade(q *Request, c *cascade) error {
	objects, err := queryRows(q, `SELECT DISTINCT c.store_id,a.project_id,a.scope,a.content_hash,c.staging_key FROM artifact_copy c JOIN artifact a ON a.id=c.artifact_id
	  WHERE a.id=ANY($1) AND c.state<>'deleted'`, c.sets["artifact"])
	if err != nil {
		return err
	}
	for _, t := range purgeOrder {
		a := &args{c: c}
		where := c.goesWith(t, a)
		if where == "false" {
			continue
		}
		if err := exec(q, "DELETE FROM "+t+" WHERE "+andFilter(keptRows[t], where), a.vals...); err != nil {
			return fmt.Errorf("purge %s: %w", t, err)
		}
	}
	// An object is content-addressed (project/scope/hash) and may be shared: runPurgeTasks decides under the write lock.
	keys := map[string][]any{}
	for _, o := range objects {
		store := astr(o["store_id"])
		if k := astr(o["staging_key"]); k != "" {
			keys[store] = append(keys[store], doc{"key": k})
		}
		x := doc{"key": keyOf(&Artifact{ProjectID: strp(o, "project_id"), Scope: astr(o["scope"]), ContentHash: astr(o["content_hash"])}),
			"projectId": o["project_id"], "scope": o["scope"], "hash": o["content_hash"]}
		if !slices.ContainsFunc(keys[store], func(y any) bool { return mapOf(y)["key"] == x["key"] }) {
			keys[store] = append(keys[store], x)
		}
	}
	for _, store := range slices.Sorted(maps.Keys(keys)) {
		if err := addPurgeTask(q, doc{"storeId": store, "objects": keys[store]}); err != nil {
			return err
		}
	}
	if len(c.sets["execution"]) > 0 {
		if err := addPurgeTask(q, doc{"executionIds": c.sets["execution"]}); err != nil {
			return err
		}
	}
	if c.kind == "project" {
		return exec(q, "UPDATE definition SET purged_at=now() WHERE kind='project' AND id=$1", c.id)
	}
	return nil
}

// purgeRootTx: one tombstoned root in its own transaction: blocked (a live row keeps it) or purged.
func purgeRootTx(q *Request, kind contract.PurgeKind, id string, preview bool) (rows []contract.KindCount, bytes int64, blocked string, err error) {
	err = runTx(q.ctx(), q.app, q, !preview, "", func(q *Request) error {
		if err := showDeleted(q); err != nil {
			return err
		}
		if err := exec(q, "SELECT set_config('fleet.purge','on',true)"); err != nil {
			return err
		}
		c, err := cascadeOf(q, string(kind), id)
		if err != nil {
			return err
		}
		if blocked, err = c.blockedBy(q); err != nil || blocked != "" {
			return err
		}
		// A stop or teardown a host has not fetched yet keeps the object: purging it would leave the agent running and
		// the worktree in place. hosts_remove settles the commands of a removed host.
		if ok, err := has(q, "SELECT 1 FROM document WHERE kind='command' AND data->>'status'='pending' AND data->>'executionId'=ANY($1)", c.sets["execution"]); err != nil || ok {
			if ok {
				blocked = "pending command"
			}
			return err
		}
		if rows, bytes, err = c.counts(q); err != nil || preview {
			return err
		}
		if err := purgeCascade(q, c); err != nil {
			return err
		}
		return put(q, "setting-change", doc{"id": newID(), "target": string(kind) + ":" + id, "field": "purge", "value": rows, "actor": actor(q, "operator"), "at": nowMs()})
	})
	return
}

func systemPurge(q *Request, b *contract.SystemPurgeRequest) (contract.SystemPurgeReply, error) {
	out := contract.SystemPurgeReply{Removed: []contract.PurgeCount{}, Kept: []contract.PurgeCount{}, Rows: []contract.KindCount{}}
	kinds := contract.PurgeKindValues
	if len(b.Kinds) > 0 {
		kinds = b.Kinds
	}
	// Hosts first, as the preview sees them: tombstoned rows count as references, so a host a purged project named goes
	// at the next purge.
	if slices.Contains(kinds, contract.PurgeKindHost) {
		if err := runTx(q.ctx(), q.app, q, !b.Preview, "", func(q *Request) error {
			if err := showDeleted(q); err != nil {
				return err
			}
			removed, kept, err := purgeHosts(q, b.Preview)
			out.Removed, out.Kept = append(out.Removed, removed), append(out.Kept, kept)
			return err
		}); err != nil {
			return out, err
		}
	}
	rows := map[contract.ObjectKind]*contract.KindCount{}
	for _, kind := range []contract.PurgeKind{contract.PurgeKindExperiment, contract.PurgeKindProject} {
		if !slices.Contains(kinds, kind) {
			continue
		}
		var roots []string
		if err := runTx(q.ctx(), q.app, q, false, "", func(q *Request) error {
			if err := showDeleted(q); err != nil {
				return err
			}
			var err error
			roots, err = purgeRoots(q, kind)
			return err
		}); err != nil {
			return out, err
		}
		removed, kept := contract.PurgeCount{Kind: kind, Ids: []string{}}, contract.PurgeCount{Kind: kind, Ids: []string{}}
		var bytes int64
		// The roots unblocked before any goes, as the preview sees them: a root kept only by another tombstoned root
		// goes at the next purge.
		if !b.Preview {
			free := []string{}
			for _, id := range roots {
				_, _, blocked, err := purgeRootTx(q, kind, id, true)
				if err != nil {
					return out, err
				}
				if blocked == "" {
					free = append(free, id)
				} else {
					kept.Ids = append(kept.Ids, id)
				}
			}
			roots = free
		}
		{
			left := []string{}
			for _, id := range roots {
				rs, n, blocked, err := purgeRootTx(q, kind, id, b.Preview)
				if err != nil {
					return out, fmt.Errorf("purge %s %s: %w", kind, id, err)
				}
				if blocked != "" {
					left = append(left, id)
					continue
				}
				removed.Ids, bytes = append(removed.Ids, id), bytes+n
				for _, r := range rs {
					if rows[r.Kind] == nil {
						rows[r.Kind] = &contract.KindCount{Kind: r.Kind}
					}
					rows[r.Kind].Count += r.Count
					if r.Bytes != nil {
						sum := *r.Bytes
						if rows[r.Kind].Bytes != nil {
							sum += *rows[r.Kind].Bytes
						}
						rows[r.Kind].Bytes = &sum
					}
				}
			}
			roots = left
		}
		removed.Count, removed.Bytes = int64(len(removed.Ids)), &bytes
		kept.Ids = append(kept.Ids, roots...)
		kept.Count = int64(len(kept.Ids))
		out.Removed, out.Kept = append(out.Removed, removed), append(out.Kept, kept)
	}
	for _, k := range contract.ObjectKindValues {
		if rows[k] != nil {
			out.Rows = append(out.Rows, *rows[k])
		}
	}
	var left int64
	var err error
	if !b.Preview {
		left, err = runPurgeTasks(q.ctx(), q.app)
		out.Purged = true
	} else {
		err = runTx(q.ctx(), q.app, q, false, "", func(q *Request) error {
			return q.tx.QueryRow(q.ctx(), "SELECT count(*) FROM document WHERE kind='purge-task'").Scan(&left)
		})
	}
	out.RetryLater = &left
	return out, err
}

// runPurgeTasks: delete the store objects and spans of earlier purges; a task that fails stays for the next purge.
// Returns the tasks left. A store object is deleted holding its object lock (objectLock, which artifact_reserve's
// copy inserts take too) after checking that no copy on that store maps to it again: an upload that deduplicated onto
// it, or started a new copy of the same content, since the purge keeps it. Span deletes run outside any transaction.
func runPurgeTasks(ctx context.Context, a *App) (int64, error) {
	var tasks []doc
	if err := runTx(ctx, a, nil, false, "", func(q *Request) error {
		var err error
		tasks, err = rows(q, "SELECT data FROM document WHERE kind='purge-task' ORDER BY seq")
		return err
	}); err != nil {
		return 0, err
	}
	left := int64(0)
	for _, t := range tasks {
		rest := []any{}
		var err error
		if store := astr(t["storeId"]); store != "" {
			err = runTx(ctx, a, nil, true, NOLOCK, func(q *Request) error {
				rest = []any{}
				if err := showDeleted(q); err != nil {
					return err
				}
				s, err := storeGet(ctx, a, store)
				if isNotFound(err) { // the store went with its host: nothing to delete
					return nil
				}
				var s3 *S3
				if err == nil && s.State != "unreachable" {
					s3, err = tunnel(s)
				}
				if err != nil {
					slog.Warn("purge: store", "store", store, "err", err)
				}
				for _, x := range anys(t["objects"]) {
					o := mapOf(x)
					if err := objectLock(ctx, q.tx, store, astr(o["key"])); err != nil {
						return err
					}
					if o["hash"] != nil {
						used, err := has(q, `SELECT 1 FROM artifact_copy c JOIN artifact a ON a.id=c.artifact_id WHERE c.store_id=$1 AND c.state<>'deleted'
						  AND a.project_id IS NOT DISTINCT FROM $2 AND a.scope=$3 AND a.content_hash=$4`, store, o["projectId"], o["scope"], o["hash"])
						if err != nil {
							return err
						}
						if used {
							continue
						}
					}
					if s3 == nil || s3.Delete(s.Bucket, astr(o["key"])) != nil {
						rest = append(rest, o)
					}
				}
				return nil
			})
			t["objects"] = rest
		} else if ids := strs(t["executionIds"]); len(ids) > 0 && spanStore != nil {
			for i := 0; i < len(ids); i += 200 {
				part := ids[i:min(i+200, len(ids))]
				if err := spanStore.Exec(ctx, "DELETE FROM span WHERE execution_id IN {ids:Array(String)}", map[string]string{"ids": clickhouseStrings(part)}); err != nil {
					slog.Warn("purge: spans", "err", err)
					for _, id := range ids[i:] {
						rest = append(rest, id)
					}
					break
				}
			}
			t["executionIds"] = rest
		}
		if err == nil {
			err = runTx(ctx, a, nil, true, "", func(q *Request) error {
				if len(rest) == 0 {
					return exec(q, "DELETE FROM document WHERE kind='purge-task' AND id=$1", t["id"])
				}
				return put(q, "purge-task", t)
			})
		}
		if err != nil {
			return left, err
		}
		if len(rest) > 0 {
			left++
		}
	}
	return left, nil
}

// objectLock: the transaction lock of one store object (content-addressed key): purge's check-and-delete and
// artifact_reserve's copy inserts of the same content serialize on it, outside the canonical lock order (a leaf lock,
// held until the transaction ends, never followed by another wait).
func objectLock(ctx context.Context, tx pgx.Tx, storeID, key string) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "object:"+storeID+":"+key)
	return err
}

// clickhouseStrings: an Array(String) parameter value (ClickHouse literal syntax).
func clickhouseStrings(ids []string) string {
	q := make([]string, len(ids))
	for i, s := range ids {
		q[i] = "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
	}
	return "[" + strings.Join(q, ",") + "]"
}
