-- name: InventorySessions :many
-- The state of each session a host's inventory names (host_inventory.go, docs/designs/fleet-host-cleanup.md#11-inventory-reconcile),
-- in one query over the batch: whether it is known and live, its kind, and what keeps it from being settled.
SELECT d.id::text AS id,
  (s.id IS NOT NULL AND s.tombstoned_at IS NULL)::bool AS known,
  coalesce(s.project_id, '')::text AS project_id,
  (s.archived_at IS NOT NULL)::bool AS archived,
  (s.pipeline_run_id IS NOT NULL)::bool AS pipeline,
  (t.accepted_at IS NOT NULL)::bool AS accepted,
  coalesce((SELECT bool_or(e.state IN ('preparing', 'running', 'waiting') OR e.observation = 'unconfirmed') FROM execution e WHERE e.session_id = d.id), false)::bool AS live,
  EXISTS(SELECT 1 FROM command c WHERE c.session_id = d.id AND c.status IN ('pending', 'accepted', 'started'))::bool AS busy,
  EXISTS(SELECT 1 FROM question x WHERE x.session_id = d.id AND x.state = 'open')::bool AS asking,
  EXISTS(SELECT 1 FROM artifact a JOIN execution e ON e.id = a.execution_id WHERE e.session_id = d.id AND a.state = 'pending')::bool AS uploading,
  NOT EXISTS(SELECT 1 FROM artifact a JOIN execution e ON e.id = a.execution_id WHERE e.session_id = d.id AND a.state IN ('pending', 'failed'))::bool AS outputs_stored,
  EXISTS(SELECT 1 FROM command c WHERE c.session_id = d.id AND c.kind = 'teardown' AND c.status = 'completed'
    AND c.updated_at >= coalesce((SELECT max(coalesce(e.ended_at, e.updated_at)) FROM execution e WHERE e.session_id = d.id), '-infinity'))::bool AS torn
FROM unnest(@ids::text[]) AS d(id)
LEFT JOIN session s ON s.id = d.id
LEFT JOIN task t ON t.id = s.task_id;

-- name: IdleSessionsToArchive :many
-- Sessions the Idle archive takes (docs/designs/fleet-host-cleanup.md#10-worktree-lifecycle-for-every-run-kind): open,
-- not a pipeline step, no task, not a bot's thread (bots keep their own continuity), no live execution, no pending command, no open question, and no message, turn or
-- execution since @before.
SELECT s.id::text FROM session s
WHERE s.archived_at IS NULL AND s.tombstoned_at IS NULL AND s.pipeline_run_id IS NULL AND s.task_id IS NULL AND s.bot_id IS NULL
  AND s.created_at < @before
  AND NOT EXISTS(SELECT 1 FROM execution e WHERE e.session_id = s.id AND (e.state IN ('preparing', 'running', 'waiting') OR e.observation = 'unconfirmed' OR coalesce(e.ended_at, e.updated_at) >= @before))
  AND NOT EXISTS(SELECT 1 FROM message m WHERE m.session_id = s.id AND m.updated_at >= @before)
  AND NOT EXISTS(SELECT 1 FROM command c WHERE c.session_id = s.id AND c.status IN ('pending', 'accepted', 'started'))
  AND NOT EXISTS(SELECT 1 FROM question x WHERE x.session_id = s.id AND x.state = 'open')
ORDER BY s.created_at
LIMIT @lim;

-- name: HostInventorySet :exec
-- The host's latest inventory summary (hosts_list disk); the poll's HostObservationPut keeps it.
UPDATE host_observation SET payload = payload || jsonb_build_object('inventory', @inventory::jsonb) WHERE id = @id::text;
