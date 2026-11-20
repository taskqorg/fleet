-- Dispatch, executions, artifact placement and message attachments (dispatch.go, executions.go, placement.go,
-- attachments.go; docs/designs/fleet-standard.md S2).

-- Placement (placement.go).

-- name: DispRuleConfirmed :one
SELECT EXISTS(SELECT 1 FROM artifact_rule_confirmation WHERE project_id= @project_id AND rule_hash= @rule_hash)::boolean;

-- name: DispArtifactCopies :many
SELECT store_id,state,last_error,attempts,next_attempt_at,(next_attempt_at IS NULL OR next_attempt_at<=now())::boolean AS due
FROM artifact_copy WHERE artifact_id=$1;

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

-- name: DispCopyDeleted :exec
UPDATE artifact_copy SET state='deleted' WHERE artifact_id=$1 AND store_id=$2;

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
-- The agent runs and host jobs waiting for their artifacts, with the names they declared; expired: waiting since before.
SELECT id,(data->'declaredArtifacts')::jsonb AS declared,(coalesce((data->>'waitingSince')::bigint,0) < @before::bigint)::boolean AS expired
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

-- name: DispMarkObservation :exec
UPDATE execution SET observation = @observation::observation,
 last_seen_at=CASE WHEN @observation::text='confirmed' THEN now() ELSE last_seen_at END WHERE id = @id;

-- name: DispHarnessVersion :exec
UPDATE execution SET harness_version = @version::text WHERE id = @id AND harness_version IS DISTINCT FROM @version::text;

-- name: DispExecParent :one
-- The fields a child agent row inherits from its parent execution.
SELECT project_id,task_id,session_id,host_id,harness,directed_by,pipeline_id,pipeline_revision,step_id,profile_id,profile_revision
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
SELECT data FROM document WHERE kind='reconciliation' ORDER BY seq DESC LIMIT 1;

-- name: DispExecSessions :many
-- The sessions of the executions and the sessions that created them.
SELECT DISTINCT s::text FROM (SELECT e.session_id AS s FROM execution e WHERE e.id = ANY(@ids::text[])
 UNION SELECT s.created_by_session_id AS s FROM execution e JOIN session s ON s.id=e.session_id WHERE e.id = ANY(@ids::text[])) x
WHERE coalesce(s,'')<>'';

-- name: DispExecDismissRow :one
SELECT state::text AS state,project_id,problem,
 EXISTS (SELECT 1 FROM document d WHERE d.kind='problem-dismissal' AND d.id=execution.id)::boolean AS dismissed
FROM execution WHERE execution.id=$1 FOR UPDATE;

-- name: DispLostIDs :many
-- Unfinished runs no host will finish (lostCond in executions.go), silent for secs.
SELECT execution.id FROM execution WHERE state IN ('preparing','running','waiting') AND (
 (kind IN ('agent_run','host_job') AND (observation='unconfirmed' OR (coalesce((data->>'stopRequested')::boolean,false) AND EXISTS (SELECT 1 FROM command c
  WHERE c.execution_id=execution.id AND c.kind='stop' AND c.status IN ('pending','unknown'))))
  AND coalesce(to_timestamp((data->>'updatedAt')::float8/1000), started_at, updated_at) < now() - make_interval(secs => @secs::float8))
 OR (kind='host_job' AND state='running' AND observation='confirmed' AND coalesce(last_seen_at, started_at, updated_at) < now() - make_interval(secs => @secs::float8)
  AND EXISTS (SELECT 1 FROM host_observation h WHERE h.id=execution.host_id AND h.last_seen_at > now() - interval '300 seconds'))
 OR (kind='pipeline_step' AND observation='unconfirmed' AND coalesce(started_at, updated_at) < now() - make_interval(secs => @secs::float8)
  AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.id=coalesce(execution.parent_execution_id,split_part(execution.id,':step:',1)))));

-- name: DispLostStill :one
-- DispLostIDs for one execution, re-checked under its row lock (a receipt that just arrived wins).
SELECT EXISTS(SELECT 1 FROM execution WHERE execution.id = @id AND state IN ('preparing','running','waiting') AND (
 (kind IN ('agent_run','host_job') AND (observation='unconfirmed' OR (coalesce((data->>'stopRequested')::boolean,false) AND EXISTS (SELECT 1 FROM command c
  WHERE c.execution_id=execution.id AND c.kind='stop' AND c.status IN ('pending','unknown'))))
  AND coalesce(to_timestamp((data->>'updatedAt')::float8/1000), started_at, updated_at) < now() - make_interval(secs => @secs::float8))
 OR (kind='host_job' AND state='running' AND observation='confirmed' AND coalesce(last_seen_at, started_at, updated_at) < now() - make_interval(secs => @secs::float8)
  AND EXISTS (SELECT 1 FROM host_observation h WHERE h.id=execution.host_id AND h.last_seen_at > now() - interval '300 seconds'))
 OR (kind='pipeline_step' AND observation='unconfirmed' AND coalesce(started_at, updated_at) < now() - make_interval(secs => @secs::float8)
  AND NOT EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.id=coalesce(execution.parent_execution_id,split_part(execution.id,':step:',1)))))
 FOR UPDATE SKIP LOCKED)::boolean;

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
SELECT id FROM question WHERE execution_id=$1 AND state='open';

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
