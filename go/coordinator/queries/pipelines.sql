-- Pipeline runs on the work kit (pipeline_work.go, docs/designs/fleet-standard.md S6): one pipeline_work row per run
-- the kit still has to move; the retry columns are the kit's (work_kit.go).

-- name: PipelineWorkDue :many
SELECT run_id,attempts FROM pipeline_work WHERE next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz ORDER BY next_attempt_at,run_id LIMIT sqlc.arg(lim);

-- name: PipelineWorkEnsure :execrows
-- A running run has its row; nothing when it has one.
INSERT INTO pipeline_work(run_id) VALUES($1) ON CONFLICT DO NOTHING;

-- name: PipelineWorkDelete :exec
DELETE FROM pipeline_work WHERE run_id=$1;

-- name: PipelineRunLock :one
-- The run row, locked for this step's transaction.
SELECT id FROM execution WHERE id=$1 FOR UPDATE;

-- name: PipelineRunOfExecution :one
-- The pipeline run an agent or script step's own execution belongs to; '' for any other execution.
SELECT coalesce(data->>'pipelineRunId','')::text AS run_id FROM execution WHERE id=$1;

-- name: LabWorkDue :many
-- The due experiments, and a running experiment without its row (one set running outside the lab queue), first.
SELECT experiment_id,attempts FROM (
 SELECT e.id AS experiment_id,0 AS attempts,NULL::timestamptz AS due FROM experiment e WHERE e.state='running' AND NOT EXISTS(SELECT 1 FROM lab_work w WHERE w.experiment_id=e.id)
 UNION ALL SELECT experiment_id,attempts,next_attempt_at FROM lab_work WHERE next_attempt_at<=sqlc.arg(now)::pg_catalog.timestamptz) d
ORDER BY due NULLS FIRST,experiment_id LIMIT sqlc.arg(lim);

-- name: LabWorkEnsure :execrows
INSERT INTO lab_work(experiment_id) VALUES($1) ON CONFLICT DO NOTHING;

-- name: LabWorkDelete :exec
DELETE FROM lab_work WHERE experiment_id=$1;

-- name: LabExperimentState :one
SELECT state FROM experiment WHERE id=$1;

-- name: PipelineRevokeRunTokens :execrows
-- Every live call token of a run's steps (rows <run>:step:<i>), for a run that left the queue unread.
UPDATE run_token SET revoked_at=to_timestamp(sqlc.arg(at)::float8/1000), updated_at=now()
WHERE starts_with(execution_id, sqlc.arg(run_id)::text || ':step:') AND revoked_at IS NULL;

-- Pipeline authority (pipelineAuthorityStamp, pipeline_plan.go): the rows a dispatch is stamped with, read FOR SHARE
-- (the ...Share forms) inside a write transaction so they cannot change before the stamp is used.

-- name: PipelineProject :one
SELECT head,access_version FROM definition WHERE kind='project' AND id=$1 AND retired_at IS NULL;

-- name: PipelineProjectShare :one
SELECT head,access_version FROM definition WHERE kind='project' AND id=$1 AND retired_at IS NULL FOR SHARE;

-- name: PipelineProfileLive :one
SELECT id FROM definition WHERE kind='agent' AND id=$1 AND retired_at IS NULL;

-- name: PipelineProfileLiveShare :one
SELECT id FROM definition WHERE kind='agent' AND id=$1 AND retired_at IS NULL FOR SHARE;

-- name: PipelineConnectionGrant :one
-- What a connection grants (its record's holder, access, tools and intake, as connAllowed and the stamp read them).
SELECT coalesce(holder,payload->>'holder','')::text AS holder,coalesce(payload->'access','null')::jsonb AS access,
 coalesce(payload->'tools','null')::jsonb AS tools,coalesce(payload->>'intake','')::text AS intake FROM api_connection WHERE id=$1;

-- name: PipelineConnectionGrantShare :one
SELECT coalesce(holder,payload->>'holder','')::text AS holder,coalesce(payload->'access','null')::jsonb AS access,
 coalesce(payload->'tools','null')::jsonb AS tools,coalesce(payload->>'intake','')::text AS intake FROM api_connection WHERE id=$1 FOR SHARE;

-- name: PipelineHost :one
-- A host's record state (its column unless active, as the record shows it) and holder.
SELECT coalesce(CASE WHEN state<>'active' THEN state ELSE payload->>'state' END,'')::text AS state,coalesce(holder,payload->>'holder','')::text AS holder FROM host WHERE id=$1;

-- name: PipelineHostShare :one
SELECT coalesce(CASE WHEN state<>'active' THEN state ELSE payload->>'state' END,'')::text AS state,coalesce(holder,payload->>'holder','')::text AS holder FROM host WHERE id=$1 FOR SHARE;

-- name: PipelineStoredPlan :one
-- The stored plan of a run row, locked (guardPipelineWrite): whether it is a run record, its plan version, entry and
-- definition step counts, revision and definition hash.
SELECT (data ? 'pipelineId')::bool AS is_run,(CASE WHEN jsonb_typeof(data->'planVersion')='number' THEN data->>'planVersion' ELSE '' END)::text AS plan_version,
 (CASE WHEN jsonb_typeof(data->'plan'->'entries')='array' THEN jsonb_array_length(data->'plan'->'entries') ELSE 0 END)::int AS entries,
 (CASE WHEN jsonb_typeof(data->'definition'->'steps')='array' THEN jsonb_array_length(data->'definition'->'steps') ELSE 0 END)::int AS steps,
 (CASE WHEN jsonb_typeof(data->'plan'->'revision')='number' THEN (data->'plan'->>'revision')::numeric ELSE 0 END)::bigint AS revision,
 coalesce(data->'plan'->>'definitionSha256','')::text AS definition_sha256
FROM execution WHERE id=$1 FOR UPDATE;

-- name: PipelineExecutionExists :one
SELECT EXISTS(SELECT 1 FROM execution WHERE id=$1)::bool;

-- name: PipelineExecutionDefinitions :one
-- The definition pins of an execution in the run's project.
SELECT definitions FROM execution WHERE id=$1 AND project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text;

-- name: PipelineHostJob :one
-- A host job's record (as execJSON shows it) and its project.
SELECT (data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at,'projectId',project_id))::jsonb AS record
FROM execution WHERE id=$1 AND kind='host_job';

-- name: PipelineRunCancelled :one
SELECT EXISTS(SELECT 1 FROM execution WHERE id=$1 AND state='cancelled')::bool;

-- Pipeline runs and steps (pipelines.go): a run is one execution row of kind pipeline_run (its document in data), each
-- selected step one pipeline_step row <run>:step:<i>; their state moves only through executionTransition.

-- name: PipelineRunUpdate :exec
-- The run row's columns and document; the Lab's projectAssessments (lab_assessment.go) survive a write of the run.
UPDATE execution SET observation=sqlc.arg(observation)::observation,project_id=sqlc.narg(project_id),started_at=sqlc.narg(started_at),ended_at=sqlc.narg(ended_at),
 data=sqlc.arg(data)::jsonb || CASE WHEN data ? 'projectAssessments' THEN jsonb_build_object('projectAssessments',data->'projectAssessments') ELSE '{}'::jsonb END,
 config_ref=CASE WHEN variant IS NULL THEN sqlc.arg(config_ref)::jsonb ELSE config_ref END,experiment_id=coalesce(experiment_id,sqlc.narg(experiment_id)),variant=coalesce(variant,sqlc.narg(variant)),
 task_id=coalesce(task_id,sqlc.narg(task_id)),session_id=coalesce(session_id,sqlc.narg(session_id)),directed_by=coalesce(sqlc.narg(directed_by),directed_by),
 pipeline_id=coalesce(sqlc.narg(pipeline_id),pipeline_id),pipeline_revision=coalesce(sqlc.narg(pipeline_revision),pipeline_revision),
 parent_execution_id=coalesce(parent_execution_id,sqlc.narg(parent_execution_id))
WHERE id=sqlc.arg(id);

-- name: PipelineRunDefinitionsSet :exec
UPDATE execution SET definitions=sqlc.arg(definitions)::jsonb,pipeline_id=coalesce(sqlc.narg(pipeline_id),pipeline_id),pipeline_revision=coalesce(sqlc.narg(pipeline_revision),pipeline_revision)
WHERE id=sqlc.arg(id);

-- name: PipelineStepUpdate :exec
UPDATE execution SET observation=sqlc.arg(observation)::observation,project_id=sqlc.narg(project_id),started_at=sqlc.narg(started_at),ended_at=sqlc.narg(ended_at),data=sqlc.arg(data)::jsonb,
 task_id=coalesce(task_id,sqlc.narg(task_id)),session_id=coalesce(session_id,sqlc.narg(session_id)),directed_by=coalesce(sqlc.narg(directed_by),directed_by),
 experiment_id=sqlc.narg(experiment_id),variant=sqlc.narg(variant),
 pipeline_id=coalesce(sqlc.narg(pipeline_id),pipeline_id),pipeline_revision=coalesce(sqlc.narg(pipeline_revision),pipeline_revision),step_id=coalesce(sqlc.narg(step_id),step_id)
WHERE id=sqlc.arg(id);

-- name: PipelineSteps :many
-- The step rows of selected plan entries: owner run, project and record (as execJSON shows it).
SELECT id,parent_execution_id,project_id,(data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at))::jsonb AS record
FROM execution WHERE kind='pipeline_step' AND id=ANY(sqlc.arg(ids)::text[]);

-- name: PipelineStepCalls :many
-- The tool calls of steps, with their connection cost.
SELECT e.parent_execution_id::text AS step,e.id,coalesce(e.data->>'tool','')::text AS tool,coalesce(e.data->>'connection','')::text AS connection,t.connection_cost AS cost,
 coalesce(t.connection_cost_basis,'unavailable')::text AS basis
FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id
WHERE e.kind='tool_run' AND e.parent_execution_id=ANY(sqlc.arg(ids)::text[]) ORDER BY e.seq;

-- name: PipelineRun :one
-- A run record (as execJSON shows it); none for a Lab trial row whose run has not started.
SELECT (data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at))::jsonb AS record
FROM execution WHERE id=$1 AND kind='pipeline_run' AND data ? 'pipelineId';

-- name: PipelineCaller :one
-- The execution a pipeline is started from, with the task of its session.
SELECT e.project_id,coalesce(e.task_id,s.task_id) AS task_id,e.session_id,e.directed_by
FROM execution e LEFT JOIN session s ON s.id=e.session_id WHERE e.id=$1;

-- name: PipelineTaskProject :one
SELECT project_id FROM task WHERE id=$1;

-- name: PipelineProblemSet :exec
UPDATE execution SET problem=sqlc.arg(problem)::text WHERE id=sqlc.arg(id);

-- name: PipelineStoredArtifacts :many
-- The stored artifacts of an execution, in upload order.
SELECT id,name AS path,content_hash AS sha256 FROM artifact WHERE execution_id=$1 AND state='stored' ORDER BY seq;

-- name: PipelinePendingArtifacts :one
SELECT count(*)::int FROM artifact WHERE execution_id=$1 AND state='pending';

-- name: PipelineStepLineage :one
-- The lineage columns of a step row its job's tool_run copies.
SELECT task_id,session_id,directed_by,pipeline_id,pipeline_revision,step_id FROM execution WHERE id=$1;

-- name: PipelineStepCredits :one
SELECT count(*)::int AS calls,coalesce(sum((t.connection_cost->>'credits')::numeric),0)::float8 AS credits
FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id WHERE e.parent_execution_id=$1 AND e.kind='tool_run';

-- name: PipelineOpenJobs :many
-- The open jobs under a step, locked.
SELECT id FROM execution WHERE parent_execution_id=$1 AND state IN ('preparing','running','waiting') ORDER BY id FOR UPDATE;

-- name: PipelineScriptCommand :one
-- A script step's command: its id and host (the column, else the record's).
SELECT id,coalesce(host_id,payload->>'hostId','')::text AS host_id FROM command WHERE execution_id=$1 AND kind='script' ORDER BY seq LIMIT 1;

-- name: PipelineOwnedJobIDs :many
-- Every job id a step owns.
SELECT coalesce(data->>'jobId','')::text AS id FROM execution WHERE data ? 'jobId';

-- name: PipelineStepJobSet :exec
UPDATE execution SET data=data||jsonb_build_object('jobId',sqlc.arg(job_id)::text) WHERE id=sqlc.arg(id);

-- name: PipelineQuestion :one
-- What an answered question's after-effects read (labAnswerAfter, budgetAnswerAfter).
SELECT id,kind::text AS kind,answer,answered_by,options,data FROM question WHERE id=$1;

-- name: PipelineExecutionPins :one
SELECT definitions FROM execution WHERE id=$1;

-- name: PipelineRunsByRequest :many
-- The runs of one request of a pipeline file (a repeated create answers its run), in order.
SELECT (data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at))::jsonb AS record
FROM execution WHERE kind='pipeline_run' AND data ? 'pipelineId' AND data->>'requestId'=sqlc.arg(request_id)::text AND data->>'pipeline'=sqlc.arg(pipeline)::text ORDER BY seq;

-- name: PipelineRunsByProjectRequest :many
SELECT (data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at))::jsonb AS record
FROM execution WHERE kind='pipeline_run' AND data ? 'pipelineId' AND data->>'projectId'=sqlc.arg(project_id)::text AND data->>'requestId'=sqlc.arg(request_id)::text ORDER BY seq;

-- name: PipelineRunsByRerun :many
SELECT (data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at))::jsonb AS record
FROM execution WHERE kind='pipeline_run' AND data ? 'pipelineId' AND data->>'requestId'=sqlc.arg(request_id)::text AND data->>'rerunOf'=sqlc.arg(rerun_of)::text ORDER BY seq;
