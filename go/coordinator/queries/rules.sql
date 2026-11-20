-- Rules pass, Postgres half (rules.go, docs/designs/fleet-telemetry.md §6): the state its rules read.

-- name: RulesExecutionDirector :one
-- The director a project cause on an execution goes to.
SELECT directed_by FROM execution WHERE id=$1;

-- name: RulesStoresDown :many
-- Observed stores that are full or unreachable (store-fill, absence-store).
SELECT id,host_id,state,capacity_bytes,free_space_warning_bytes FROM store WHERE observed_at IS NOT NULL AND state IN ('full','unreachable');

-- name: RulesStuckExecutions :many
-- Confirmed running executions started over stuck_secs ago, with the seconds since their last non-delta event (else their start).
SELECT e.id,e.project_id,e.host_id,extract(epoch FROM now()-coalesce((SELECT max(d.updated_at) FROM event d WHERE NOT d.delta
 AND d.execution_id=e.id AND d.type <> 'resource.observation'), e.started_at))::float8 AS idle
FROM execution e WHERE e.state='running' AND e.observation='confirmed' AND NOT coalesce((e.data->>'stopRequested')::boolean,false)
 AND e.started_at < now() - make_interval(secs => sqlc.arg(stuck_secs)::float8);

-- name: RulesResourceFailing :many
-- Resource work whose attempts reached the failing level (work_kit.go): an execution's work, a project build, a metadata repair.
SELECT ('execution '||execution_id)::text AS item FROM (SELECT DISTINCT execution_id FROM resource_work WHERE last_error IS NOT NULL AND attempts>=sqlc.arg(attempts)::int) w
UNION ALL SELECT ('build of project '||project_id)::text FROM resource_build WHERE NOT ready AND attempts>=sqlc.arg(attempts)::int
UNION ALL SELECT ('metadata repair '||repair_id::text)::text FROM resource_metadata_repair WHERE attempts>=sqlc.arg(attempts)::int ORDER BY 1;
