-- Rules pass, Postgres half (rules.go, docs/designs/fleet-telemetry.md §6): the state its rules read.

-- name: RulesExecutionDirector :one
-- The director a project cause on an execution goes to.
SELECT directed_by FROM execution WHERE id=$1;

-- name: RulesStoresDown :many
-- Observed stores that are full or unreachable (store-fill, absence-store).
SELECT id,host_id,state,capacity_bytes,free_space_warning_bytes FROM store WHERE observed_at IS NOT NULL AND state IN ('full','unreachable');

-- name: RulesStuckExecutions :many
-- Confirmed running executions started over stuck_secs ago, with idle time since their last event or fresh dispatch handoff.
-- A selected step and its run already have an actionable dispatch finding while the reserved child is waiting for placement.
SELECT e.id,e.project_id,e.host_id,extract(epoch FROM now()-greatest(
 coalesce((SELECT max(d.updated_at) FROM event d WHERE NOT d.delta
  AND d.execution_id=e.id AND d.type <> 'resource.observation'), e.started_at),
 CASE WHEN e.kind IN ('pipeline_run','pipeline_step') THEN (
  SELECT to_timestamp(max(p.dispatch_handed_off_at_ms) / 1000.0) FROM pipeline_step p
  WHERE p.run_id=CASE WHEN e.kind='pipeline_run' THEN e.id ELSE e.parent_execution_id END
   AND p.ref_mode='fresh' AND (e.kind='pipeline_run' OR p.ref_execution_id=e.id)
 ) END))::float8 AS idle
FROM execution e WHERE e.state='running' AND e.observation='confirmed' AND NOT coalesce((e.data->>'stopRequested')::boolean,false)
 AND e.started_at < now() - make_interval(secs => sqlc.arg(stuck_secs)::float8)
 AND (e.kind NOT IN ('pipeline_run','pipeline_step') OR NOT EXISTS (
  SELECT 1 FROM pipeline_step p
  JOIN execution step ON step.id=p.ref_execution_id AND step.state='running'
  JOIN execution native ON native.id=p.native_execution_id AND native.parent_execution_id=step.id
  WHERE p.run_id=CASE WHEN e.kind='pipeline_run' THEN e.id ELSE e.parent_execution_id END
   AND (e.kind='pipeline_run' OR step.id=e.id)
   AND p.ref_mode='fresh' AND p.dispatch_phase='reserved' AND native.kind='agent_run'
   AND native.state='preparing' AND native.host_id IS NULL AND native.wait_reason IS NOT NULL AND coalesce(native.data->>'waitDetail','')<>''
 ));

-- name: RulesResourceFailing :many
-- Resource work whose attempts reached the failing level (work_kit.go): an execution's work, a project build, a metadata repair.
SELECT ('execution '||execution_id)::text AS item FROM (SELECT DISTINCT execution_id FROM resource_work WHERE last_error IS NOT NULL AND attempts>=sqlc.arg(attempts)::int) w
UNION ALL SELECT ('build of project '||project_id)::text FROM resource_build WHERE NOT ready AND attempts>=sqlc.arg(attempts)::int
UNION ALL SELECT ('metadata repair '||repair_id::text)::text FROM resource_metadata_repair WHERE attempts>=sqlc.arg(attempts)::int ORDER BY 1;
