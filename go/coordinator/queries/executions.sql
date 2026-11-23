-- The execution state machine (execution_states.go, docs/designs/fleet-standard.md S5): the one insert and the one
-- state move of an execution row.

-- name: ExecutionInsert :one
-- A new execution in its first state; nothing when its id or request_id exists.
INSERT INTO execution(id,kind,state,observation,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,
 harness,model,config_ref,started_at,ended_at,data,directed_by,profile_id,profile_revision,pipeline_id,pipeline_revision,step_id,request_id,system_actor,definitions)
VALUES(sqlc.arg(id),sqlc.arg(kind),sqlc.arg(state),coalesce(sqlc.narg(observation)::observation,'confirmed'),sqlc.narg(project_id),sqlc.narg(task_id),
 sqlc.narg(experiment_id),sqlc.narg(variant),sqlc.narg(parent_execution_id),sqlc.narg(session_id),sqlc.narg(account_id),sqlc.narg(host_id),
 sqlc.narg(harness),sqlc.narg(model),sqlc.narg(config_ref),sqlc.narg(started_at),sqlc.narg(ended_at),coalesce(sqlc.narg(data)::jsonb,'{}'),
 sqlc.narg(directed_by),sqlc.narg(profile_id),sqlc.narg(profile_revision),sqlc.narg(pipeline_id),sqlc.narg(pipeline_revision),sqlc.narg(step_id),
 sqlc.narg(request_id),sqlc.narg(system_actor),coalesce(sqlc.narg(definitions)::jsonb,'[]'))
ON CONFLICT DO NOTHING RETURNING id;

-- name: ExecutionLockState :one
SELECT kind,state FROM execution WHERE id=$1 FOR UPDATE;

-- name: ExecutionMove :exec
-- One move: a success clears the problem; an end keeps a known end time, else now; a reopened step loses its end.
UPDATE execution SET state=sqlc.arg(state),
 observation=coalesce(sqlc.narg(observation)::observation,observation),
 problem=CASE WHEN sqlc.arg(state)::execution_state='succeeded' THEN NULL WHEN sqlc.arg(set_problem)::boolean THEN sqlc.narg(problem)::text ELSE problem END,
 ended_at=CASE WHEN sqlc.arg(ends)::boolean THEN coalesce(ended_at,now()) WHEN sqlc.arg(reopens)::boolean THEN NULL ELSE ended_at END
WHERE id=sqlc.arg(id);

-- Execution record reads, updates and leaf totals (executions.go).

-- name: ExecutionByID :one
SELECT * FROM execution WHERE id=$1 AND kind IN ('agent_run','host_job');

-- name: Executions :many
SELECT * FROM execution
WHERE kind IN ('agent_run','host_job')
 AND (sqlc.narg(session_id)::text IS NULL OR session_id=sqlc.narg(session_id)::text)
ORDER BY seq;

-- name: ExecutionStateOf :one
-- An execution's state and observation, both empty when there is none.
SELECT coalesce(e.state::text,'')::text AS state, coalesce(e.observation::text,'')::text AS observation
FROM (VALUES (1)) AS single(n) LEFT JOIN execution e ON e.id=$1;

-- name: ExecutionSessionHasOtherRun :one
-- Another agent run or host job of the session than id.
SELECT EXISTS (SELECT 1 FROM execution WHERE session_id= @session_id AND id<> @id AND kind IN ('agent_run','host_job'));

-- name: ExecutionPipelineRef :one
SELECT pipeline_id, pipeline_revision, step_id FROM execution WHERE id=$1;

-- name: ExecutionUpdate :exec
-- executionPutRaw's write of an existing run: its columns and the open rest in data, keeping the trace of its first write.
UPDATE execution SET observation= @observation, project_id= sqlc.narg(project_id), task_id=coalesce(task_id, sqlc.narg(task_id)), experiment_id= sqlc.narg(experiment_id),
 variant= sqlc.narg(variant), parent_execution_id= sqlc.narg(parent_execution_id), session_id= sqlc.narg(session_id), account_id= sqlc.narg(account_id),
 host_id= sqlc.narg(host_id), harness= sqlc.narg(harness), model= sqlc.narg(model), config_ref= @config_ref, started_at= sqlc.narg(started_at), ended_at= sqlc.narg(ended_at),
 data= @data::jsonb || jsonb_strip_nulls(jsonb_build_object('traceparent',data->'traceparent','traceActor',data->'traceActor')),
 directed_by=coalesce(sqlc.narg(directed_by),directed_by), profile_id=coalesce(sqlc.narg(profile_id),profile_id), profile_revision=coalesce(sqlc.narg(profile_revision),profile_revision),
 pipeline_id=coalesce(sqlc.narg(pipeline_id),pipeline_id), pipeline_revision=coalesce(sqlc.narg(pipeline_revision),pipeline_revision), step_id=coalesce(sqlc.narg(step_id),step_id)
 WHERE id= @id;

-- name: ExecutionDefinitions :exec
UPDATE execution SET definitions= @definitions, profile_id=coalesce(sqlc.narg(profile_id),profile_id), profile_revision=coalesce(sqlc.narg(profile_revision),profile_revision),
 pipeline_id=coalesce(sqlc.narg(pipeline_id),pipeline_id), pipeline_revision=coalesce(sqlc.narg(pipeline_revision),pipeline_revision) WHERE id= @id;

-- name: ExecutionDirectedBy :exec
UPDATE execution SET directed_by= @directed_by::text WHERE id= @id AND directed_by IS DISTINCT FROM @directed_by::text;

-- name: ExecutionTaskOf :one
-- The task of an execution, when the task exists.
SELECT e.task_id FROM execution e JOIN task k ON k.id=e.task_id WHERE e.id=$1;

-- name: ExecutionLockKind :one
-- Locks an execution before its totals (parent before totals, the order every totals writer takes).
SELECT kind::text FROM execution WHERE id=$1 FOR UPDATE;

-- name: TotalsConfirmedUsage :exec
-- The totals row of accepted native usage, without completion or process resources.
INSERT INTO execution_totals(execution_id,project_id,directed_by,billing_basis,computed_at)
 SELECT id,project_id,directed_by,CASE
 WHEN account_id IS NOT NULL AND NOT coalesce((SELECT m.is_api_conn FROM fleet_exec_conn_meta('',account_id) m),false) THEN 'subscription'
 WHEN kind IN ('agent_run','child_agent','tool_run') THEN 'metered' ELSE 'unavailable' END,now()
 FROM execution WHERE id=$1
 ON CONFLICT(execution_id) DO UPDATE SET project_id=excluded.project_id,directed_by=excluded.directed_by,
 computed_at=now();

-- name: TotalsWrite :exec
-- The totals row of a leaf execution, merged so a receipt and process.closed converge in any order; the wall time is
-- measured when the run has both ends, unless it was abandoned (it ended when given up).
INSERT INTO execution_totals(execution_id,project_id,directed_by,billing_basis,wall_ms,wall_ms_basis,computed_at)
 SELECT id,project_id,directed_by,
  CASE
   WHEN account_id IS NOT NULL AND NOT coalesce((SELECT m.is_api_conn FROM fleet_exec_conn_meta('', account_id) m), false) THEN 'subscription'
   WHEN kind IN ('agent_run','child_agent','tool_run') THEN 'metered'
   ELSE 'unavailable'
  END,
  CASE WHEN started_at IS NOT NULL AND ended_at IS NOT NULL AND problem IS DISTINCT FROM 'unknown:abandoned' THEN greatest(0,(extract(epoch FROM ended_at-started_at)*1000)::bigint) END,
  CASE WHEN started_at IS NOT NULL AND ended_at IS NOT NULL AND problem IS DISTINCT FROM 'unknown:abandoned' THEN 'measured' ELSE 'unavailable' END,
  now()
 FROM execution WHERE id= @id::text
 ON CONFLICT(execution_id) DO UPDATE SET
  project_id=excluded.project_id,
  directed_by=coalesce(excluded.directed_by,execution_totals.directed_by),
  billing_basis=CASE WHEN execution_totals.billing_basis='metered' AND execution_totals.connection_cost IS NOT NULL THEN 'metered' ELSE excluded.billing_basis END,
  wall_ms=CASE WHEN (SELECT problem FROM execution WHERE id= @id::text)='unknown:abandoned' THEN NULL ELSE coalesce(excluded.wall_ms,execution_totals.wall_ms) END,
  wall_ms_basis=CASE WHEN (SELECT problem FROM execution WHERE id= @id::text)='unknown:abandoned' THEN 'unavailable' WHEN coalesce(excluded.wall_ms,execution_totals.wall_ms) IS NOT NULL THEN 'measured' ELSE 'unavailable' END,
  computed_at=now();

-- name: TotalsModel :exec
-- The model the harness reports (migration 45).
SELECT fleet_write_model(@id::text);

-- name: TotalsTokens :exec
-- Tokens from the harness's usage report (migrations 40, 44, 50).
SELECT fleet_write_tokens(@id::text);

-- name: TotalsNativeObservations :exec
SELECT fleet_write_native_observations(@id::text);

-- name: TotalsRateLimit :exec
-- The last rate-limit reading of the execution.
UPDATE execution_totals SET rate_limit_last=r.payload->'data',rate_limit_last_basis='measured'
 FROM (SELECT payload FROM event WHERE NOT delta AND execution_id= @id::text AND type='limit_reading' ORDER BY seq DESC LIMIT 1) r WHERE execution_id= @id::text;

-- name: TotalsProcessClosed :exec
-- The process resources of the last process.closed event; tool calls for an agent run (kind).
WITH pc AS (
 SELECT payload->'data'->'resources' AS r
 FROM event
 WHERE NOT delta AND execution_id= @id::text AND type='process.closed'
 ORDER BY seq DESC LIMIT 1
), parsed AS (
 SELECT
  jsonb_typeof(r)='object' AS has_obj,
  fleet_token_count(r->'cpuMs') AS cpu_ms,
  fleet_token_count(r->'peakMemoryBytes') AS peak_mem,
  fleet_token_count(r->'activeMs') AS active_ms,
  fleet_token_count(r->'turnCount') AS turn_count,
  CASE WHEN jsonb_typeof(r->'cpuMissingReason')='string' THEN r->>'cpuMissingReason' END AS cpu_reason,
  CASE WHEN jsonb_typeof(r->'memoryMissingReason')='string' THEN r->>'memoryMissingReason' END AS mem_reason,
  CASE WHEN @kind::text='agent_run' AND jsonb_typeof(r)='object' THEN
   jsonb_strip_nulls(jsonb_build_object(
    'byTool', CASE WHEN jsonb_typeof(r->'toolCalls')='object' THEN r->'toolCalls' ELSE '{}'::jsonb END,
    'other', CASE WHEN jsonb_typeof(r->'toolCallsOther')='object' THEN r->'toolCallsOther' END
   ))
  END AS tool_calls
 FROM pc
)
UPDATE execution_totals t SET
 cpu_ms=coalesce(p.cpu_ms,t.cpu_ms),
 cpu_ms_basis=CASE WHEN coalesce(p.cpu_ms,t.cpu_ms) IS NOT NULL THEN 'measured' ELSE t.cpu_ms_basis END,
 cpu_missing_reason=CASE
  WHEN coalesce(p.cpu_ms,t.cpu_ms) IS NOT NULL THEN NULL
  WHEN p.has_obj THEN coalesce(p.cpu_reason,t.cpu_missing_reason)
  ELSE coalesce(t.cpu_missing_reason,'executor_version')
 END,
 peak_memory_bytes=coalesce(p.peak_mem,t.peak_memory_bytes),
 peak_memory_bytes_basis=CASE WHEN coalesce(p.peak_mem,t.peak_memory_bytes) IS NOT NULL THEN 'measured' ELSE t.peak_memory_bytes_basis END,
 memory_missing_reason=CASE
  WHEN coalesce(p.peak_mem,t.peak_memory_bytes) IS NOT NULL THEN NULL
  WHEN p.has_obj THEN coalesce(p.mem_reason,t.memory_missing_reason)
  ELSE coalesce(t.memory_missing_reason,'executor_version')
 END,
 active_ms=coalesce(p.active_ms,t.active_ms),
 active_ms_basis=CASE WHEN coalesce(p.active_ms,t.active_ms) IS NOT NULL THEN 'measured' ELSE t.active_ms_basis END,
 turn_count=coalesce(p.turn_count,t.turn_count),
 turn_count_basis=CASE WHEN coalesce(p.turn_count,t.turn_count) IS NOT NULL THEN 'measured' ELSE t.turn_count_basis END,
 tool_calls=coalesce(p.tool_calls,t.tool_calls),
 tool_calls_basis=CASE WHEN coalesce(p.tool_calls,t.tool_calls) IS NOT NULL THEN 'measured' ELSE t.tool_calls_basis END,
 computed_at=now()
FROM parsed p
WHERE t.execution_id= @id::text;

-- name: ExecutionEnded :exec
-- The end of an agent run in its thread, one event with a fixed id.
INSERT INTO event(id,execution_id,session_id,project_id,type,at,payload) SELECT 'ended:'||e.id,e.id,e.session_id,e.project_id,'execution.ended',
 coalesce(e.ended_at,now()),jsonb_build_object('data',jsonb_build_object('fleetNormalized',jsonb_build_object('blocks','[]'::jsonb)))
 FROM execution e WHERE e.id=$1 AND e.kind='agent_run' ON CONFLICT(id) DO NOTHING;
