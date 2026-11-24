-- Dispatch, executions, artifact placement and message attachments (dispatch.go, executions.go, placement.go,
-- attachments.go; docs/designs/fleet-standard.md S2).

-- Placement (placement.go).

-- name: DispRuleConfirmed :one
SELECT EXISTS(SELECT 1 FROM artifact_rule_confirmation WHERE project_id= @project_id AND rule_hash= @rule_hash)::boolean;

-- name: DispArtifactCopies :many
SELECT store_id,state,last_error,attempts,next_attempt_at,(next_attempt_at IS NULL OR next_attempt_at<=now())::boolean AS due
FROM artifact_copy WHERE artifact_id=$1;

-- name: DispStoredArtifactCopies :many
SELECT c.artifact_id,c.store_id,c.state,c.last_error,c.attempts,c.next_attempt_at,(c.next_attempt_at IS NULL OR c.next_attempt_at<=now())::boolean AS due
FROM artifact_copy c JOIN artifact a ON a.id=c.artifact_id
WHERE a.state='stored';

-- name: DispCopyStart :exec
-- A copy queued to a store: copying under a six-hour lease, an earlier attempt's backoff and error cleared.
INSERT INTO artifact_copy(artifact_id,store_id,project_id,state,upload_id,staging_key,lease_expires_at)
VALUES(@artifact_id,@store_id,sqlc.narg(project_id),'copying',@upload_id,@staging_key,now()+interval '6 hours')
ON CONFLICT(artifact_id,store_id) DO UPDATE SET state='copying',next_attempt_at=NULL,upload_id=excluded.upload_id,
 staging_key=excluded.staging_key,lease_expires_at=excluded.lease_expires_at,last_error=NULL;

-- name: DispCopySharers :many
-- The projects of the other stored artifacts with the same content whose verified copy sits on the store.
SELECT a.project_id FROM artifact a JOIN artifact_copy c ON c.artifact_id=a.id
WHERE a.id<> @id AND a.project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text AND a.scope= @scope AND a.content_hash= @content_hash
 AND a.state='stored' AND c.store_id= @store_id AND c.state='verified';

-- name: DispCopyShared :one
SELECT EXISTS(SELECT 1 FROM artifact y JOIN artifact_copy z ON z.artifact_id=y.id
 WHERE y.id<> @id AND y.project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text AND y.scope= @scope AND y.content_hash= @content_hash
 AND y.state='stored' AND z.store_id= @store_id AND z.state='verified')::boolean;

-- name: DispArtifactHeld :one
-- A verified copy of the artifact on an unreachable store.
SELECT EXISTS(SELECT 1 FROM artifact_copy c JOIN store s ON s.id=c.store_id
 WHERE c.artifact_id=$1 AND c.state='verified' AND s.state='unreachable')::boolean;

-- name: DispVerifiedStores :many
SELECT store_id FROM artifact_copy WHERE artifact_id=$1 AND state='verified';

-- name: DispArtifactDelete :exec
UPDATE artifact SET state='deleted',deleted_at=now(),deleted_by= @deleted_by WHERE id= @id;

-- name: DispPurgesPending :many
SELECT c.artifact_id,c.store_id,a.project_id,a.scope,a.content_hash FROM artifact_copy c JOIN artifact a ON a.id=c.artifact_id
WHERE c.state='purge_pending';

-- name: DispArtifactWaits :many
-- The agent runs and host jobs waiting for their artifacts, with the names they declared; expired: waiting since before or host upload failed before reserve.
SELECT id,(data->'declaredArtifacts')::jsonb AS declared,
 ((coalesce((data->>'waitingSince')::bigint,0) < @before::bigint)
  OR EXISTS (SELECT 1 FROM event ev WHERE NOT ev.delta AND ev.execution_id=execution.id AND ev.type='outbox.failed'
   AND ev.payload->'data'->>'route'='/api/hosts/artifacts/failed'))::boolean AS expired
FROM execution WHERE kind IN ('agent_run','host_job') AND state='waiting' AND data->>'waitReason'='artifacts'
ORDER BY seq;

-- name: DispRulePreview :many
SELECT id,name,size FROM artifact WHERE state='stored' AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text AND scope= @scope
 AND created_at <= now() - (@days::text||' days')::interval ORDER BY seq;

-- name: DispRuleConfirm :exec
INSERT INTO artifact_rule_confirmation(project_id,rule_hash,confirmed_by) VALUES(@project_id,@rule_hash,@confirmed_by)
ON CONFLICT(project_id,rule_hash) DO UPDATE SET confirmed_by=excluded.confirmed_by,confirmed_at=now();

-- name: DispCopyRetry :one
UPDATE artifact_copy SET next_attempt_at=now(),attempts=0 WHERE artifact_id=$1 AND store_id=$2 AND state='failed' RETURNING artifact_id;

-- Attachments (attachments.go).

-- name: DispAttachmentJoin :execrows
-- A person's upload joins the session it is sent in (unattached or already its own); an agent's only when already its own.
UPDATE artifact SET session_id= @session_id WHERE id= @id AND (session_id= @session_id OR (NOT @agent::boolean AND session_id IS NULL));

-- Executions (executions.go).

-- name: DispExecLab :one
SELECT experiment_id,variant FROM execution WHERE id = @id AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text;

-- name: DispMarkObservation :one
WITH prior AS MATERIALIZED (SELECT id,state,observation,task_id FROM execution WHERE id=$1 FOR UPDATE)
UPDATE execution e SET observation = $2::observation,
 last_seen_at=CASE WHEN $2::text='confirmed' THEN now() ELSE e.last_seen_at END
FROM prior WHERE e.id=$1 AND e.id=prior.id
RETURNING coalesce(CASE WHEN prior.state IN ('succeeded','failed','cancelled')
 AND prior.observation IS DISTINCT FROM $2::observation THEN (SELECT id FROM task WHERE id=prior.task_id) END,'')::text AS task_id;

-- name: DispHarnessVersion :exec
UPDATE execution SET harness_version = @version::text WHERE id = @id AND harness_version IS DISTINCT FROM @version::text;

-- name: DispExecParent :one
-- The fields a child agent row inherits from its parent execution.
SELECT project_id,task_id,bot_id,session_id,host_id,harness,directed_by,pipeline_id,pipeline_revision,step_id,profile_id,profile_revision
FROM execution WHERE id=$1;

-- name: DispExecEnded :one
SELECT EXISTS(SELECT 1 FROM execution WHERE id=$1 AND ended_at IS NOT NULL)::boolean;

-- name: DispDetectProblems :exec
-- Registration problems of open executions (an unknown host or account, no environment of the project on the host);
-- a retained runtime cause stays.
WITH found AS (SELECT e.id,CASE
    WHEN e.host_id IS NOT NULL AND NOT (e.host_id = ANY(@hosts::text[]) OR EXISTS (SELECT 1 FROM host_observation h WHERE h.id=e.host_id)) THEN 'unregistered host '||e.host_id
    WHEN e.account_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM managed_account a WHERE a.id=e.account_id) THEN 'unregistered account '||e.account_id
    WHEN e.project_id IS NOT NULL AND e.host_id IS NOT NULL AND jsonb_array_length(coalesce(@environments::jsonb->e.project_id,'[]'))>0 AND NOT (@environments::jsonb->e.project_id) ? e.host_id THEN 'no registered environment of project '||e.project_id||' on host '||e.host_id
   END AS problem FROM execution e WHERE e.state NOT IN ('succeeded','failed','cancelled') AND e.kind<>'child_agent')
UPDATE execution x SET problem=found.problem FROM found WHERE x.id=found.id
 AND (x.problem IS NULL OR x.problem LIKE 'unregistered host %' OR x.problem LIKE 'unregistered account %' OR x.problem LIKE 'no registered environment of project %')
 AND x.problem IS DISTINCT FROM found.problem;

-- name: DispOpenDelegations :many
SELECT delegation::jsonb FROM execution WHERE delegation IS NOT NULL AND coalesce(delegation->>'continuationExecutionId','')=''
 AND coalesce(delegation->>'state','')<>'suppressed' ORDER BY seq;

-- name: DispLockExecutionTask :exec
-- Execution writers lock the task before the execution.
SELECT 1 FROM task WHERE id=(SELECT coalesce(e.task_id,s.task_id) FROM execution e LEFT JOIN session s ON s.id=e.session_id WHERE e.id=$1)
FOR NO KEY UPDATE;

-- name: DispExecLock :exec
SELECT 1 FROM execution WHERE id=$1 FOR UPDATE;

-- name: DispCommandIDs :many
-- The commands of an execution, optionally of one kind and one status, in order.
SELECT id FROM command WHERE execution_id = @execution_id
 AND (sqlc.narg(kind)::text IS NULL OR kind = sqlc.narg(kind)::text)
 AND (sqlc.narg(status)::text IS NULL OR status::text = sqlc.narg(status)::text)
ORDER BY seq;

-- name: DispStartNeverHandedOut :one
SELECT EXISTS(SELECT 1 FROM command WHERE execution_id=$1 AND kind IN ('start','script') AND status='pending' AND handed_out=false)::boolean;

-- name: DispLatestReconciliation :one
SELECT jsonb_build_object('id', id, 'at', (extract(epoch FROM at) * 1000)::bigint, 'items', payload)::jsonb FROM reconciliation ORDER BY seq DESC LIMIT 1;

-- name: DispReconciliationPut :exec
-- One reconciliation pass: its items (agent.go reconcile).
INSERT INTO reconciliation(id, payload) VALUES (@id::text, @items::jsonb);

-- name: DispExecSessions :many
-- The sessions of the executions and the sessions that created them.
SELECT DISTINCT s::text FROM (SELECT e.session_id AS s FROM execution e WHERE e.id = ANY(@ids::text[])
 UNION SELECT s.created_by_session_id AS s FROM execution e JOIN session s ON s.id=e.session_id WHERE e.id = ANY(@ids::text[])) x
WHERE coalesce(s,'')<>'';

-- name: DispProblemDismiss :exec
-- An execution's problem taken off Needs you (executions_dismiss); the problem stays on the execution.
INSERT INTO problem_dismissal(execution_id, project_id, problem, reason, actor) VALUES (@execution_id::text, sqlc.narg(project_id)::text, @problem::text, @reason::text, @actor::text);

-- name: DispExecDismissRow :one
SELECT state::text AS state,project_id,problem,
 EXISTS (SELECT 1 FROM problem_dismissal d WHERE d.execution_id=execution.id)::boolean AS dismissed
FROM execution WHERE execution.id=$1 FOR UPDATE;

-- name: DispLostCandidates :many
-- Unfinished runs no host will finish (executions.go), silent for secs or finished/dropped by their host.
WITH flagged AS (
 SELECT execution.id, execution.kind::text AS kind, execution.state::text AS state, execution.seq,
  (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
   SELECT 1 FROM event ev WHERE NOT ev.delta AND ev.execution_id=execution.id AND ev.type='outbox.failed'
    AND ev.payload->'data'->>'route'='/api/hosts/receipts'
    AND (execution.state IN ('running','waiting') OR EXISTS (SELECT 1 FROM event pc WHERE NOT pc.delta AND pc.execution_id=execution.id AND pc.type='process.closed')
     OR ev.updated_at < now() - make_interval(secs => @closed_grace_secs::float8)))) AS is_outbox_failed,
  (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
   SELECT 1 FROM event ev WHERE NOT ev.delta AND ev.execution_id=execution.id AND ev.type='process.closed'
    AND ev.updated_at < now() - make_interval(secs => @closed_grace_secs::float8))) AS is_process_closed,
  (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
   SELECT 1 FROM command c WHERE c.execution_id=execution.id AND c.kind IN ('start','script') AND c.status IN ('completed','failed','stopped')
    AND c.updated_at < now() - make_interval(secs => @closed_grace_secs::float8))
   AND NOT EXISTS (
    SELECT 1 FROM command c2 WHERE c2.execution_id=execution.id AND c2.kind IN ('start','script') AND c2.status IN ('pending','accepted','started','unknown'))) AS is_terminal_cmd,
  (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
   SELECT 1 FROM event ev WHERE NOT ev.delta AND ev.execution_id=execution.id AND ev.type='teardown'
    AND ev.updated_at < now() - make_interval(secs => @closed_grace_secs::float8))) AS is_teardown,
  (kind IN ('agent_run','host_job') AND host_id IS NOT NULL AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
   SELECT 1 FROM host h WHERE h.host_id=execution.host_id AND h.state='removed'
    AND h.updated_at < now() - make_interval(secs => @closed_grace_secs::float8))) AS is_host_removed,
  (kind IN ('agent_run','host_job') AND host_id IS NOT NULL
   AND (state='running' OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')
    OR (state='preparing' AND EXISTS (SELECT 1 FROM command c WHERE c.execution_id=execution.id AND c.kind IN ('start','script') AND c.status IN ('accepted','started','unknown'))))
   AND EXISTS (
    SELECT 1 FROM host_observation h WHERE h.id=execution.host_id AND h.last_seen_at > now() - interval '300 seconds'
     AND h.payload ? 'activeExecutions' AND NOT (h.payload->'activeExecutions' ? execution.id)
     AND coalesce(execution.last_seen_at, execution.started_at, execution.updated_at) < h.last_seen_at - make_interval(secs => @closed_grace_secs::float8)
     AND coalesce(execution.last_seen_at, execution.started_at, execution.updated_at) < now() - make_interval(secs => @closed_grace_secs::float8))) AS is_missing_active,
  (kind='host_job' AND state='running' AND observation='confirmed' AND coalesce(last_seen_at, started_at, updated_at) < now() - make_interval(secs => @secs::float8)
   AND EXISTS (SELECT 1 FROM host_observation h WHERE h.id=execution.host_id AND h.last_seen_at > now() - interval '300 seconds')) AS is_silent_host_job,
  (kind IN ('agent_run','host_job') AND (observation='unconfirmed' OR (coalesce((data->>'stopRequested')::boolean,false) AND EXISTS (SELECT 1 FROM command c
   WHERE c.execution_id=execution.id AND c.kind='stop' AND c.status IN ('pending','unknown'))))
   AND coalesce(to_timestamp((data->>'updatedAt')::float8/1000), started_at, updated_at) < now() - make_interval(secs => @secs::float8)) AS is_unconfirmed,
  (kind='pipeline_step' AND observation='unconfirmed' AND coalesce(started_at, updated_at) < now() - make_interval(secs => @secs::float8)
   AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.id=coalesce(execution.parent_execution_id,split_part(execution.id,':step:',1)))) AS is_orphan_step
 FROM execution
 WHERE state IN ('preparing','running','waiting')
  AND (sqlc.narg(ids)::text[] IS NULL OR execution.id = ANY(sqlc.narg(ids)::text[]))
)
SELECT id, kind, state,
 CASE
  WHEN is_outbox_failed THEN 'outbox_failed_receipt'
  WHEN is_process_closed THEN 'process_closed_no_receipt'
  WHEN is_terminal_cmd THEN 'terminal_command_open_execution'
  WHEN is_teardown THEN 'workspace_teardown'
  WHEN is_host_removed THEN 'host_removed'
  WHEN is_missing_active THEN 'missing_from_host_active_executions'
  WHEN is_silent_host_job THEN 'silent_host_job'
  WHEN is_unconfirmed THEN 'unconfirmed_expired'
  WHEN is_orphan_step THEN 'orphan_pipeline_step'
 END::text AS cause
FROM flagged
WHERE is_outbox_failed OR is_process_closed OR is_terminal_cmd OR is_teardown OR is_host_removed OR is_missing_active OR is_silent_host_job OR is_unconfirmed OR is_orphan_step
ORDER BY seq;

-- name: DispLostStill :one
-- DispLostCandidates for one execution, re-checked under its row lock (a receipt that just arrived wins).
SELECT EXISTS(SELECT 1 FROM execution WHERE execution.id = @id AND state IN ('preparing','running','waiting') AND (
 (kind IN ('agent_run','host_job') AND (observation='unconfirmed' OR (coalesce((data->>'stopRequested')::boolean,false) AND EXISTS (SELECT 1 FROM command c
  WHERE c.execution_id=execution.id AND c.kind='stop' AND c.status IN ('pending','unknown'))))
  AND coalesce(to_timestamp((data->>'updatedAt')::float8/1000), started_at, updated_at) < now() - make_interval(secs => @secs::float8))
 OR (kind='host_job' AND state='running' AND observation='confirmed' AND coalesce(last_seen_at, started_at, updated_at) < now() - make_interval(secs => @secs::float8)
  AND EXISTS (SELECT 1 FROM host_observation h WHERE h.id=execution.host_id AND h.last_seen_at > now() - interval '300 seconds'))
 OR (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
  SELECT 1 FROM event ev WHERE NOT ev.delta AND ev.execution_id=execution.id AND ev.type='outbox.failed'
   AND ev.payload->'data'->>'route'='/api/hosts/receipts'
   AND (execution.state IN ('running','waiting') OR EXISTS (SELECT 1 FROM event pc WHERE NOT pc.delta AND pc.execution_id=execution.id AND pc.type='process.closed')
    OR ev.updated_at < now() - make_interval(secs => @closed_grace_secs::float8))))
 OR (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
  SELECT 1 FROM event ev WHERE NOT ev.delta AND ev.execution_id=execution.id AND ev.type='process.closed'
   AND ev.updated_at < now() - make_interval(secs => @closed_grace_secs::float8)))
 OR (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
  SELECT 1 FROM command c WHERE c.execution_id=execution.id AND c.kind IN ('start','script') AND c.status IN ('completed','failed','stopped')
   AND c.updated_at < now() - make_interval(secs => @closed_grace_secs::float8))
  AND NOT EXISTS (SELECT 1 FROM command c2 WHERE c2.execution_id=execution.id AND c2.kind IN ('start','script') AND c2.status IN ('pending','accepted','started','unknown')))
 OR (kind IN ('agent_run','host_job') AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
  SELECT 1 FROM event ev WHERE NOT ev.delta AND ev.execution_id=execution.id AND ev.type='teardown'
   AND ev.updated_at < now() - make_interval(secs => @closed_grace_secs::float8)))
 OR (kind IN ('agent_run','host_job') AND host_id IS NOT NULL AND (state IN ('preparing','running') OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')) AND EXISTS (
  SELECT 1 FROM host h WHERE h.host_id=execution.host_id AND h.state='removed'
   AND h.updated_at < now() - make_interval(secs => @closed_grace_secs::float8)))
 OR (kind IN ('agent_run','host_job') AND host_id IS NOT NULL
  AND (state='running' OR (state='waiting' AND coalesce(data->>'waitReason','')<>'artifacts')
   OR (state='preparing' AND EXISTS (SELECT 1 FROM command c WHERE c.execution_id=execution.id AND c.kind IN ('start','script') AND c.status IN ('accepted','started','unknown'))))
  AND EXISTS (
   SELECT 1 FROM host_observation h WHERE h.id=execution.host_id AND h.last_seen_at > now() - interval '300 seconds'
    AND h.payload ? 'activeExecutions' AND NOT (h.payload->'activeExecutions' ? execution.id)
    AND coalesce(execution.last_seen_at, execution.started_at, execution.updated_at) < h.last_seen_at - make_interval(secs => @closed_grace_secs::float8)
    AND coalesce(execution.last_seen_at, execution.started_at, execution.updated_at) < now() - make_interval(secs => @closed_grace_secs::float8)))
 OR (kind='pipeline_step' AND observation='unconfirmed' AND coalesce(started_at, updated_at) < now() - make_interval(secs => @secs::float8)
  AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.id=coalesce(execution.parent_execution_id,split_part(execution.id,':step:',1)))))
 FOR UPDATE SKIP LOCKED)::boolean;

-- name: DispExecStates :many
SELECT id, kind::text AS kind, state::text AS state, coalesce(data->>'waitReason','')::text AS wait_reason FROM execution WHERE id = ANY(@ids::text[]);

-- name: DispAbandonRow :one
SELECT state::text AS state,kind::text AS kind,project_id FROM execution WHERE id=$1 FOR UPDATE;

-- name: DispOrphanStep :one
-- A pipeline step whose run row is gone.
SELECT EXISTS(SELECT 1 FROM execution s WHERE s.id=$1 AND s.kind='pipeline_step'
 AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.id=coalesce(s.parent_execution_id,split_part(s.id,':step:',1))))::boolean;

-- name: DispAbandonData :exec
UPDATE execution SET data=data||jsonb_build_object('stopDetail','abandoned','error','abandoned: ' || @reason::text) WHERE id = @id;

-- name: DispAbandonProblem :exec
UPDATE execution SET problem='unknown:abandoned' WHERE id=$1;

-- name: DispTotalsWallUnknown :exec
-- An abandoned execution's run time is unknown, never start-to-abandon.
UPDATE execution_totals SET wall_ms=NULL,wall_ms_basis='unavailable' WHERE execution_id=$1;

-- name: DispOpenQuestions :many
-- Questions answered back into this execution lose their recipient on abandonment.
SELECT id FROM question WHERE execution_id=$1 AND state='open' AND kind='agent_question';

-- Dispatch (dispatch.go).

-- name: DispPendingSessions :many
-- The sessions with queued messages or unplaced agent runs.
SELECT DISTINCT sid::text FROM (
 SELECT session_id AS sid FROM queued_message WHERE state='queued'
 UNION ALL
 SELECT session_id AS sid FROM execution WHERE host_id IS NULL AND state='preparing' AND kind='agent_run' AND session_id IS NOT NULL
) x WHERE coalesce(sid,'')<>'';

-- name: DispNeedsSweep :one
-- Open delegations, open stall questions (causes matching the pattern) or unfinished executions.
SELECT (EXISTS(SELECT 1 FROM execution WHERE delegation IS NOT NULL AND coalesce(delegation->>'continuationExecutionId','')='' AND coalesce(delegation->>'state','')<>'suppressed')
 OR EXISTS(SELECT 1 FROM question WHERE state IN ('open','answered') AND cause ~ @pattern::text)
 OR EXISTS(SELECT 1 FROM execution WHERE state IN ('preparing','running','waiting')))::boolean;

-- name: DispPipelinePending :one
SELECT EXISTS(SELECT 1 FROM execution WHERE state IN ('preparing','waiting') AND kind='agent_run' AND host_id IS NULL AND data ? 'pipelineRunId')::boolean;

-- name: DispQueuedIDs :many
SELECT id FROM execution WHERE host_id IS NULL AND state='preparing' AND kind='agent_run' ORDER BY seq;

-- name: DispRouteDecision :exec
INSERT INTO route_decision(execution_id,project_id,host_id,candidates,winner)
VALUES(@execution_id,sqlc.narg(project_id),sqlc.narg(host_id),@candidates,sqlc.narg(winner));

-- name: DispWaitReason :exec
UPDATE execution SET wait_reason=sqlc.narg(wait_reason)::text WHERE id = @id;

-- name: DispExecTask :one
SELECT task_id FROM execution WHERE id=$1;

-- name: DispRunningByHost :many
SELECT host_id::text AS host_id, count(*)::int AS n FROM execution
WHERE host_id IS NOT NULL AND kind IN ('agent_run','host_job') AND state IN ('preparing','running')
GROUP BY 1;
