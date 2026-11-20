-- The transaction plumbing of store.go: advisory locks, the transaction's settings, and its small shared writes.

-- name: AdvisoryXactLock :exec
SELECT pg_advisory_xact_lock(hashtextextended(@lock::text, 0));

-- name: TryAdvisoryXactLock :one
SELECT pg_try_advisory_xact_lock(hashtextextended(@lock::text, 0));

-- name: TxSetActor :exec
SELECT set_config('fleet.actor', @actor::text, true);

-- name: TxSetScope :exec
SELECT set_config('fleet.project_ids', @project_ids::text, true), set_config('fleet.person', @person::text, true);

-- name: TxScope :one
SELECT coalesce(current_setting('fleet.project_ids', true), '')::text AS project_ids,
  coalesce(current_setting('fleet.person', true), '')::text AS person;

-- name: TxShowDeleted :exec
SELECT set_config('fleet.deleted', @shown::text, true);

-- name: QuestionAsk :exec
INSERT INTO question(id, kind, project_id, text, options, asked_by, data)
VALUES (@id, @kind::question_kind, sqlc.narg(project_id), @text, sqlc.narg(options), @asked_by, @data);

-- name: ConnectionCostWrite :exec
-- The connection_cost total and measured duration of one tool_run.
INSERT INTO execution_totals(execution_id, project_id, directed_by, billing_basis, connection_cost, connection_cost_basis,
  wall_ms, wall_ms_basis, active_ms, active_ms_basis, computed_at)
SELECT e.id, e.project_id, e.directed_by, 'metered', sqlc.narg(cost)::jsonb, @basis::text,
  CASE WHEN e.started_at IS NOT NULL AND e.ended_at IS NOT NULL THEN greatest(0, (extract(epoch FROM e.ended_at - e.started_at) * 1000)::bigint) END,
  CASE WHEN e.started_at IS NOT NULL AND e.ended_at IS NOT NULL THEN 'measured' ELSE 'unavailable' END,
  CASE WHEN e.started_at IS NOT NULL AND e.ended_at IS NOT NULL THEN greatest(0, (extract(epoch FROM e.ended_at - e.started_at) * 1000)::bigint) END,
  CASE WHEN e.started_at IS NOT NULL AND e.ended_at IS NOT NULL THEN 'measured' ELSE 'unavailable' END,
  now()
FROM execution e WHERE e.id = @id
ON CONFLICT (execution_id) DO UPDATE SET
  project_id = excluded.project_id,
  directed_by = coalesce(excluded.directed_by, execution_totals.directed_by),
  billing_basis = 'metered',
  connection_cost = excluded.connection_cost,
  connection_cost_basis = excluded.connection_cost_basis,
  wall_ms = coalesce(excluded.wall_ms, execution_totals.wall_ms),
  wall_ms_basis = CASE WHEN coalesce(excluded.wall_ms, execution_totals.wall_ms) IS NOT NULL THEN 'measured' ELSE execution_totals.wall_ms_basis END,
  active_ms = coalesce(excluded.active_ms, execution_totals.active_ms),
  active_ms_basis = CASE WHEN coalesce(excluded.active_ms, execution_totals.active_ms) IS NOT NULL THEN 'measured' ELSE execution_totals.active_ms_basis END,
  computed_at = now();
