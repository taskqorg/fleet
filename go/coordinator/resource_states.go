// State owners of the resource projection (DESIGN_RULES.md, R1): one transition function per state machine, the only
// writer of that machine's state columns, applying the transition's effects in the caller's transaction. Retry state
// (attempts, next attempt, last error) belongs to the work kit (work_kit.go).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"
)

// buildTransitions: the phases (schema.sql, resource_build.phase) a build page may move each phase to. A rebuild starts
// clearing from any phase; ready is terminal for pages. resource_build.ready is generated from phase (migration 75).
var buildTransitions = map[string][]string{
	"clearing": {"clearing", "scanning"},
	"scanning": {"scanning", "draining"},
	"draining": {"draining", "ready"},
	"ready":    nil,
}

// repairTransitions: a metadata repair enumerates its executions page by page (done=false), is then done while its
// seeded work drains, and is finally deleted.
var repairTransitions = map[string][]string{
	"enumerating": {"enumerating", "done"},
	"done":        {"done", "deleted"},
	"deleted":     nil,
}

type buildEvent string

const (
	buildPage    buildEvent = "page"
	buildRebuild buildEvent = "rebuild"
)

// buildTransition is the only writer of a build's phase, scan_done, after_execution_id and generation and of its
// summary's ready and epoch. The caller holds the project lock. A page clears the disposable projection in bounded key
// pages (source work and frozen valuations survive), seeds one page of at most 100 source executions, or, with no
// scoped work or unresolved metadata coverage left, makes the build ready and publishes. A rebuild starts a new
// generation and epoch at clearing; the epoch stays preparing until clearing, scan and drain complete.
func buildTransition(q *Request, p string, event buildEvent) error {
	if err := resourceEnsureProject(q, p); err != nil {
		return err
	}
	var generation, phase string
	var after *string
	if err := q.tx.QueryRow(q.ctx(), `SELECT generation::text,phase,after_execution_id FROM resource_build WHERE project_id=$1 FOR UPDATE`, p).Scan(&generation, &phase, &after); err != nil {
		return err
	}
	if event == buildRebuild {
		if err := exec(q, `UPDATE resource_build SET generation=gen_random_uuid(),phase='clearing',scan_done=false,after_execution_id=NULL,updated_at=clock_timestamp() WHERE project_id=$1`, p); err != nil {
			return err
		}
		return exec(q, `UPDATE resource_project_summary SET epoch=gen_random_uuid(),ready=false,build_generation=(SELECT generation FROM resource_build WHERE project_id=$1) WHERE project_id=$1`, p)
	}
	to := func(next string, after *string) error {
		if !slices.Contains(buildTransitions[phase], next) {
			return fmt.Errorf("resource build of %s cannot move from %s to %s", p, phase, next)
		}
		return exec(q, `UPDATE resource_build SET phase=$2,scan_done=$3,after_execution_id=$4,updated_at=clock_timestamp() WHERE project_id=$1`, p, next, next == "draining" || next == "ready", after)
	}
	switch phase {
	case "ready":
		return nil
	case "clearing":
		var removed int64
		for _, table := range []struct{ name, order string }{{"resource_contribution", `execution_id COLLATE "C"`}, {"resource_metric", "metric,basis"}, {"resource_extreme", "metric,value"}, {"resource_tool", `name COLLATE "C",connection COLLATE "C"`}, {"resource_cost", "currency,basis"}, {"resource_price_ref", `currency,basis,price_id COLLATE "C",observed_at`}} {
			ct, err := q.tx.Exec(q.ctx(), `DELETE FROM `+table.name+` WHERE ctid IN (SELECT ctid FROM `+table.name+` WHERE project_id=$1 ORDER BY `+table.order+` LIMIT 100)`, p)
			if err != nil {
				return err
			}
			removed += ct.RowsAffected()
		}
		if removed > 0 {
			return to("clearing", after)
		}
		if err := exec(q, `UPDATE resource_project_summary SET executions=0,history_incomplete=0,other_calls=0,other_errors=0,tool_count=0,cost_count=0,price_count=0,header='{}',revision=0,published_at=NULL WHERE project_id=$1`, p); err != nil {
			return err
		}
		return to("scanning", nil)
	case "scanning":
		op, start := ">=", ""
		if after != nil {
			op, start = ">", *after
		}
		rs, err := q.tx.Query(q.ctx(), `SELECT id FROM execution WHERE project_id=$1 AND id COLLATE "C" `+op+` $2 COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100`, p, start)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rs, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if err = resourceSeedWork(q, ids, "build", &generation, nil); err != nil {
			return err
		}
		if len(ids) == 0 {
			return to("draining", after)
		}
		return to("scanning", &ids[len(ids)-1])
	}
	var pending bool
	if err := q.tx.QueryRow(q.ctx(), resourceProgressSQL, p).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return to("draining", after)
	}
	if err := to("ready", after); err != nil {
		return err
	}
	if err := exec(q, `UPDATE resource_project_summary SET ready=true WHERE project_id=$1`, p); err != nil {
		return err
	}
	return resourcePublish(q, p)
}

// repairTransition is the only writer of a metadata repair's done and after_execution_id: one atomic union page over
// the normalized old/new connection names and full account IDs; the repair keeps its scopes until enumeration and all
// its seeded work drain.
func repairTransition(q *Request, repair string) error {
	var old, new []byte
	var after *string
	var done bool
	err := q.tx.QueryRow(q.ctx(), `SELECT old_class,new_class,after_execution_id,done FROM resource_metadata_repair WHERE repair_id=$1::uuid FOR UPDATE SKIP LOCKED`, repair).Scan(&old, &new, &after, &done)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if done { // done -> deleted once every seeded work UUID has drained
		return exec(q, `DELETE FROM resource_metadata_repair r WHERE repair_id=$1::uuid AND NOT EXISTS(SELECT 1 FROM resource_work w WHERE w.repair_id=r.repair_id)`, repair)
	}
	accounts, connections := []string{}, []string{}
	for _, raw := range [][]byte{old, new} {
		var c struct {
			AccountID      *string `json:"accountId"`
			ConnectionName *string `json:"connectionName"`
		}
		if err = json.Unmarshal(raw, &c); err != nil {
			return err
		}
		if c.AccountID != nil {
			accounts = append(accounts, *c.AccountID)
		}
		if c.ConnectionName != nil {
			connections = append(connections, *c.ConnectionName)
		}
	}
	op, start := ">=", ""
	if after != nil {
		op, start = ">", *after
	}
	rs, err := q.tx.Query(q.ctx(), `SELECT id FROM execution WHERE id IN (
   SELECT id FROM (
    SELECT x.id FROM unnest($1::text[]) a(account) CROSS JOIN LATERAL (SELECT id FROM execution WHERE account_id=a.account AND id COLLATE "C" `+op+` $3 COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100) x
    UNION SELECT x.id FROM unnest($2::text[]) c(connection) CROSS JOIN LATERAL (SELECT id FROM execution WHERE data->>'connection'=c.connection AND id COLLATE "C" `+op+` $3 COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100) x
   ) ids ORDER BY id COLLATE "C" LIMIT 100) ORDER BY id COLLATE "C"`, accounts, connections, start)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rs, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if err = resourceSeedWork(q, ids, "metadata", nil, &repair); err != nil {
		return err
	}
	// Include accounted old scopes from the seeded work too; a concurrent move cannot release coverage.
	if err = exec(q, `INSERT INTO resource_repair_scope(repair_id,project_id) SELECT DISTINCT w.repair_id,s.project_id FROM resource_work w JOIN resource_work_scope s USING(work_id) WHERE w.repair_id=$1::uuid ON CONFLICT DO NOTHING`, repair); err != nil {
		return err
	}
	if len(ids) > 0 {
		after = &ids[len(ids)-1]
	}
	return exec(q, `UPDATE resource_metadata_repair SET after_execution_id=$2,done=$3 WHERE repair_id=$1::uuid`, repair, after, len(ids) == 0)
}
