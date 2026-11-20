-- The resource state machines (resource_states.go, docs/designs/fleet-analytics.md): builds and metadata repairs.

-- name: ResourceBuildLock :one
SELECT generation::text AS generation,phase,after_execution_id FROM resource_build WHERE project_id=$1 FOR UPDATE;

-- name: ResourceBuildRestart :exec
-- A rebuild: a new generation starts clearing.
UPDATE resource_build SET generation=gen_random_uuid(),phase='clearing',scan_done=false,after_execution_id=NULL,updated_at=clock_timestamp() WHERE project_id=$1;

-- name: ResourceSummaryNewEpoch :exec
UPDATE resource_project_summary SET epoch=gen_random_uuid(),ready=false,build_generation=(SELECT generation FROM resource_build b WHERE b.project_id=$1) WHERE project_id=$1;

-- name: ResourceBuildMove :exec
UPDATE resource_build SET phase=$2,scan_done=$3,after_execution_id=$4,updated_at=clock_timestamp() WHERE project_id=$1;

-- The clearing pages: at most 100 key-ordered rows of one disposable table of the project.

-- name: ResourceClearContributions :execrows
DELETE FROM resource_contribution WHERE ctid IN (SELECT ctid FROM resource_contribution c WHERE c.project_id=sqlc.arg(project_id)::text ORDER BY c.execution_id COLLATE "C" LIMIT 100);

-- name: ResourceClearMetrics :execrows
DELETE FROM resource_metric WHERE ctid IN (SELECT ctid FROM resource_metric m WHERE m.project_id=$1 ORDER BY m.metric,m.basis LIMIT 100);

-- name: ResourceClearExtremes :execrows
DELETE FROM resource_extreme WHERE ctid IN (SELECT ctid FROM resource_extreme x WHERE x.project_id=$1 ORDER BY x.metric,x.value LIMIT 100);

-- name: ResourceClearTools :execrows
DELETE FROM resource_tool WHERE ctid IN (SELECT ctid FROM resource_tool t WHERE t.project_id=$1 ORDER BY t.name COLLATE "C",t.connection COLLATE "C" LIMIT 100);

-- name: ResourceClearCosts :execrows
DELETE FROM resource_cost WHERE ctid IN (SELECT ctid FROM resource_cost c WHERE c.project_id=$1 ORDER BY c.currency,c.basis LIMIT 100);

-- name: ResourceClearPriceRefs :execrows
DELETE FROM resource_price_ref WHERE ctid IN (SELECT ctid FROM resource_price_ref r WHERE r.project_id=$1 ORDER BY r.currency,r.basis,r.price_id COLLATE "C",r.observed_at LIMIT 100);

-- name: ResourceSummaryClear :exec
UPDATE resource_project_summary SET executions=0,history_incomplete=0,other_calls=0,other_errors=0,tool_count=0,cost_count=0,price_count=0,header='{}',revision=0,published_at=NULL WHERE project_id=$1;

-- name: ResourceScanPage :many
-- One page of a build's scan: at most 100 of the project's executions in key order after the key after ('' for the
-- first page: ids are never empty).
SELECT id FROM execution WHERE project_id=sqlc.arg(project_id)::text AND id COLLATE "C" > sqlc.arg(after)::text COLLATE "C" ORDER BY id COLLATE "C" LIMIT 100;

-- name: ResourceProgress :one
-- Whether a project still has scoped work. Unenumerated metadata repairs conservatively keep every scope pending.
SELECT EXISTS(SELECT 1 FROM resource_work_scope s WHERE s.project_id=$1)
 OR EXISTS(SELECT 1 FROM resource_repair_scope r WHERE r.project_id=$1)
 OR EXISTS(SELECT 1 FROM resource_metadata_repair m WHERE NOT m.done) AS pending;

-- name: ResourceSummaryReady :exec
UPDATE resource_project_summary SET ready=true WHERE project_id=$1;

-- name: ResourceRepairLock :one
SELECT old_class,new_class,after_execution_id,done FROM resource_metadata_repair WHERE repair_id=sqlc.arg(repair_id)::text::uuid FOR UPDATE SKIP LOCKED;

-- name: ResourceRepairDelete :exec
-- A done repair goes once every work row it seeded has drained.
DELETE FROM resource_metadata_repair r WHERE r.repair_id=sqlc.arg(repair_id)::text::uuid AND NOT EXISTS(SELECT 1 FROM resource_work w WHERE w.repair_id=r.repair_id);

-- name: ResourceRepairPage :many
-- One atomic union page over the normalized old/new connection names and full account IDs, after the key after (''
-- for the first page: ids are never empty).
SELECT id FROM execution WHERE id IN (
 SELECT ids.id FROM (
  SELECT x.id FROM unnest(sqlc.arg(accounts)::text[]) AS a(account) CROSS JOIN LATERAL (SELECT e.id FROM execution e WHERE e.account_id=a.account AND e.id COLLATE "C" > sqlc.arg(after)::text COLLATE "C" ORDER BY e.id COLLATE "C" LIMIT 100) x
  UNION SELECT x.id FROM unnest(sqlc.arg(connections)::text[]) AS c(connection) CROSS JOIN LATERAL (SELECT e.id FROM execution e WHERE e.data->>'connection'=c.connection AND e.id COLLATE "C" > sqlc.arg(after)::text COLLATE "C" ORDER BY e.id COLLATE "C" LIMIT 100) x
 ) ids ORDER BY ids.id COLLATE "C" LIMIT 100) ORDER BY id COLLATE "C";

-- name: ResourceRepairScopes :exec
-- The accounted old scopes of the seeded work too; a concurrent move cannot release coverage.
INSERT INTO resource_repair_scope(repair_id,project_id) SELECT DISTINCT w.repair_id,s.project_id FROM resource_work w JOIN resource_work_scope s USING(work_id) WHERE w.repair_id=sqlc.arg(repair_id)::text::uuid ON CONFLICT DO NOTHING;

-- name: ResourceRepairMove :exec
UPDATE resource_metadata_repair SET after_execution_id=sqlc.narg(after)::text,done=sqlc.arg(done)::boolean WHERE repair_id=sqlc.arg(repair_id)::text::uuid;
