-- Entity codes, entity cards and search (entity_codes.go, entity_card.go, search.go): read paths over the entity tables
-- under the caller's row-level security. Search conditions mirror the lists' own (task_page, sessions_list,
-- artifact_list: tasks.go taskWhere, sessions.go sessionWhere, search.go artifactSearch), so a count opens a list of as
-- many rows. Free-text words come as LIKE patterns (likePattern), each to be found in one of the text columns; field terms
-- (agent, host, project: contains; state: exact) are optional.

-- name: EntResolveCodes :many
-- The entities codes name: a current code, or an alias superseded by a rename, with the entity's current code.
SELECT e.code, e.entity_id, e.type, coalesce(t.code,x.code,d.code)::text AS current FROM entity_code e
 LEFT JOIN task t ON e.type='T' AND t.id=e.entity_id LEFT JOIN question x ON e.type='Q' AND x.id=e.entity_id
 LEFT JOIN session d ON e.type='C' AND d.id=e.entity_id
 WHERE e.code=ANY(@codes::text[]) AND coalesce(t.code,x.code,d.code) IS NOT NULL
 AND (e.code=coalesce(t.code,x.code,d.code) OR e.superseded_by IS NOT NULL);

-- name: EntProjectLive :one
SELECT EXISTS (SELECT 1 FROM definition WHERE kind='project' AND id=$1 AND retired_at IS NULL);

-- name: EntProjectCodeTaken :one
-- The code is or was another project's, or this project's former one (codes are never reused).
SELECT EXISTS (SELECT 1 FROM project_code WHERE code= @code AND NOT (project_id= @project_id AND current));

-- name: EntProjectCodeSet :exec
SELECT fleet_project_code_set(@project_id::text, @code::text);

-- name: EntCardTask :one
SELECT t.id, t.code, t.title, (CASE WHEN t.state='paused' AND t.pause_reason='held' THEN 'held' ELSE t.state::text END)::text AS state,
 t.project_id, t.updated_at, t.wait_reason, t.priority, coalesce(d.agent,d.last_agent,'')::text AS agent, d.host,
 coalesce(d.open_questions,0)::bigint AS open_questions, coalesce(d.executions,0)::bigint AS runs
 FROM task t LEFT JOIN task_derived d ON d.task_id=t.id WHERE t.id=$1;

-- name: EntCardQuestion :one
SELECT x.id, x.code, x.text, x.state::text AS state, x.project_id, x.task_id, x.session_id, x.asked_by, x.asked_at, x.answered_by, x.answer, t.code AS task_code
 FROM question x LEFT JOIN task t ON t.id=x.task_id WHERE x.id=$1;

-- name: EntCardConversation :one
-- One session with the sessions_list columns (sessions.go sessionBase), read for this session only.
SELECT d.id, d.code, d.project_id, coalesce(d.payload->'agentConfig'->>'id','')::text AS profile, t.code AS task_code,
 coalesce(last.state,'idle')::text AS state, coalesce(last.harness,d.payload->'agentConfig'->>'harness','')::text AS harness,
 coalesce((SELECT m.text FROM message m WHERE m.session_id=d.id ORDER BY m.seq LIMIT 1),'')::text AS first_message,
 greatest(d.updated_at,(SELECT max(a.updated_at) FROM message a WHERE a.session_id=d.id),(SELECT max(a.updated_at) FROM event a WHERE a.session_id=d.id AND NOT a.delta),ex.last_at)::timestamptz AS activity_at,
 ex.n::bigint AS runs, (SELECT count(*) FROM question x WHERE x.session_id=d.id AND x.state='open')::bigint AS open_questions
 FROM session d LEFT JOIN task t ON t.id=d.task_id
 LEFT JOIN LATERAL (SELECT e.state::text AS state, e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) last ON true
 LEFT JOIN LATERAL (SELECT count(*) AS n, max(greatest(e.started_at,e.ended_at)) AS last_at FROM execution e WHERE e.session_id=d.id) ex ON true
 WHERE d.id=$1;

-- name: EntCardExecution :one
SELECT e.id, e.kind::text AS kind, e.state::text AS state, e.project_id, e.task_id, e.session_id, e.experiment_id, e.host_id, e.harness, e.model, e.problem,
 e.started_at, e.ended_at, e.updated_at, t.code AS task_code, t.title AS task_title, s.code AS session_code
 FROM execution e LEFT JOIN task t ON t.id=e.task_id LEFT JOIN session s ON s.id=e.session_id WHERE e.id=$1;

-- name: EntCardResult :one
-- A result by its id or the id of the legacy record it replaced.
SELECT a.id, a.name, a.state, a.project_id, a.media_type, a.size, a.content_hash, a.created_at, t.code AS task_code
 FROM artifact a LEFT JOIN task t ON t.id=coalesce(a.task_id,(SELECT e.task_id FROM execution e WHERE e.id=a.execution_id)) WHERE a.id= @id OR a.legacy_id= @id
 LIMIT 1;

-- name: EntCardExperiment :one
SELECT x.id, x.state, x.project_id, x.pipeline_id, x.goal, x.decision, x.created_at, coalesce(x.launched_at,x.created_at)::timestamptz AS at,
 jsonb_array_length(x.variants)::bigint AS variants, (SELECT count(*) FROM execution e WHERE e.experiment_id=x.id AND e.kind='pipeline_run')::bigint AS trials
 FROM experiment x WHERE x.id=$1;

-- name: EntCardProjectCounts :one
SELECT coalesce((SELECT code FROM project_code WHERE project_id= @id::text AND current),'')::text AS code,
 (SELECT count(*) FROM task WHERE project_id= @id::text AND state IN ('queued','in_progress','waiting','paused'))::bigint AS open,
 (SELECT count(*) FROM execution WHERE project_id= @id::text AND state IN ('preparing','running'))::bigint AS running,
 (SELECT count(*) FROM question WHERE project_id= @id::text AND state='open')::bigint AS questions;

-- name: EntDeliveryPr :one
-- A delivered pull request (delivery_history.go).
SELECT payload FROM delivery_pr WHERE number=$1;

-- name: EntDeliveryRelease :one
-- A release of the delivery history (delivery_history.go).
SELECT payload FROM delivery_release WHERE tag=$1;

-- name: EntSearchTasks :many
-- Task hits: ids (a code jump), else the search; ignored projects left out ('-' as ignore_none: work outside projects).
-- The focus first, then an id or code that starts with prefix, then recently changed.
SELECT t.id, t.code, t.title, (CASE WHEN t.state='paused' AND t.pause_reason='held' THEN 'held' ELSE t.state::text END)::text AS state,
 t.project_id, d.last_agent AS agent, d.host, t.updated_at AS at
 FROM task t LEFT JOIN task_derived d ON d.task_id=t.id
 WHERE (sqlc.narg(ids)::text[] IS NULL OR t.id=ANY(sqlc.narg(ids)::text[]))
 AND NOT EXISTS (SELECT 1 FROM unnest(@words::text[]) AS w(p) WHERE NOT (lower(t.title) LIKE w.p OR lower(t.id) LIKE w.p OR lower(coalesce(t.code,'')) LIKE w.p))
 AND (sqlc.narg(agent)::text IS NULL OR lower(coalesce(d.last_agent,'')) LIKE sqlc.narg(agent)::text)
 AND (sqlc.narg(host)::text IS NULL OR lower(coalesce(d.host,'')) LIKE sqlc.narg(host)::text)
 AND (sqlc.narg(project)::text IS NULL OR lower(coalesce(t.project_id,'')) LIKE sqlc.narg(project)::text)
 AND (sqlc.narg(state)::text IS NULL OR (CASE WHEN t.state='paused' AND t.pause_reason='held' THEN 'held' ELSE t.state::text END)=sqlc.narg(state)::text)
 AND NOT coalesce(t.project_id=ANY(@ignored::text[]),false) AND (NOT @ignore_none::boolean OR t.project_id IS NOT NULL)
 ORDER BY coalesce(t.project_id,'-')=ANY(@focus::text[]) DESC,
 (sqlc.narg(prefix)::text IS NOT NULL AND (lower(t.id) LIKE sqlc.narg(prefix)::text OR lower(coalesce(t.code,'')) LIKE sqlc.narg(prefix)::text)) DESC,
 t.updated_at DESC, t.id
 LIMIT @lim;

-- name: EntCountTasks :one
-- task_page's count for the search in the focus (focused: '-' in focus is work outside projects), ignored left out.
SELECT count(*) FROM task t LEFT JOIN task_derived d ON d.task_id=t.id
 WHERE NOT EXISTS (SELECT 1 FROM unnest(@words::text[]) AS w(p) WHERE NOT (lower(t.title) LIKE w.p OR lower(t.id) LIKE w.p OR lower(coalesce(t.code,'')) LIKE w.p))
 AND (sqlc.narg(agent)::text IS NULL OR lower(coalesce(d.last_agent,'')) LIKE sqlc.narg(agent)::text)
 AND (sqlc.narg(host)::text IS NULL OR lower(coalesce(d.host,'')) LIKE sqlc.narg(host)::text)
 AND (sqlc.narg(project)::text IS NULL OR lower(coalesce(t.project_id,'')) LIKE sqlc.narg(project)::text)
 AND (sqlc.narg(state)::text IS NULL OR (CASE WHEN t.state='paused' AND t.pause_reason='held' THEN 'held' ELSE t.state::text END)=sqlc.narg(state)::text)
 AND (NOT @focused::boolean OR t.project_id=ANY(@focus::text[]) OR ('-'=ANY(@focus::text[]) AND t.project_id IS NULL))
 AND NOT coalesce(t.project_id=ANY(@ignored::text[]),false) AND (NOT @ignore_none::boolean OR t.project_id IS NOT NULL);

-- name: EntSearchQuestions :many
-- Question hits: ids, else the search by text or code; the focus first, open ones first, then recently asked.
SELECT x.id, x.code, x.text, x.state::text AS state, x.project_id, x.task_id, x.session_id, x.asked_at AS at FROM question x
 WHERE (sqlc.narg(ids)::text[] IS NULL OR x.id=ANY(sqlc.narg(ids)::text[]))
 AND NOT EXISTS (SELECT 1 FROM unnest(@words::text[]) AS w(p) WHERE NOT (lower(x.text) LIKE w.p OR lower(coalesce(x.code,'')) LIKE w.p))
 AND (sqlc.narg(project)::text IS NULL OR lower(coalesce(x.project_id,'')) LIKE sqlc.narg(project)::text)
 AND (sqlc.narg(state)::text IS NULL OR x.state::text=sqlc.narg(state)::text)
 AND NOT coalesce(x.project_id=ANY(@ignored::text[]),false) AND (NOT @ignore_none::boolean OR x.project_id IS NOT NULL)
 ORDER BY coalesce(x.project_id,'-')=ANY(@focus::text[]) DESC, x.state='open' DESC, x.seq DESC
 LIMIT @lim;

-- name: EntSearchSessions :many
-- Conversation hits with the sessions_list rows (sessions.go sessionBase): ids (archived ones too), else the search
-- (archived ones left out); host command sessions (program-worker) are no conversations. The focus first, then activity.
WITH x AS (SELECT d.id, d.code, d.project_id, d.seq, nullif(d.title,'') AS title, d.archived_at, d.actor, d.host_id,
 d.payload->'agentConfig'->>'harness' AS agent_harness,
 coalesce((SELECT e.state::text FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1),'idle') AS state,
 (SELECT e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) AS harness,
 (SELECT m.text FROM message m WHERE m.session_id=d.id ORDER BY m.seq LIMIT 1) AS first_message,
 greatest(d.updated_at,ex.last_at,(SELECT max(m.updated_at) FROM message m WHERE m.session_id=d.id),(SELECT max(v.updated_at) FROM event v WHERE v.session_id=d.id AND NOT v.delta)) AS activity_at
 FROM session d
 LEFT JOIN LATERAL (SELECT max(greatest(e.started_at,e.ended_at)) AS last_at FROM execution e WHERE e.session_id=d.id) ex ON true)
SELECT x.id, x.code, coalesce(x.title,'')::text AS title, coalesce(x.first_message,'')::text AS first_message, x.state::text AS state, x.project_id,
 coalesce(x.harness,x.agent_harness,'')::text AS agent, x.activity_at::timestamptz AS at FROM x
 WHERE (sqlc.narg(ids)::text[] IS NULL OR x.id=ANY(sqlc.narg(ids)::text[])) AND (sqlc.narg(ids)::text[] IS NOT NULL OR x.archived_at IS NULL)
 AND coalesce(x.actor,'')<>'program-worker'
 AND NOT EXISTS (SELECT 1 FROM unnest(@words::text[]) AS w(p) WHERE NOT (lower(coalesce(x.title,'')) LIKE w.p OR lower(coalesce(x.first_message,'')) LIKE w.p
  OR lower(x.id) LIKE w.p OR lower(coalesce(x.code,'')) LIKE w.p))
 AND (sqlc.narg(agent)::text IS NULL OR lower(coalesce(x.harness,x.agent_harness,'')) LIKE sqlc.narg(agent)::text)
 AND (sqlc.narg(host)::text IS NULL OR lower(coalesce((SELECT e.host_id FROM execution e WHERE e.session_id=x.id ORDER BY e.seq DESC LIMIT 1),x.host_id,'')) LIKE sqlc.narg(host)::text)
 AND (sqlc.narg(project)::text IS NULL OR lower(coalesce(x.project_id,'')) LIKE sqlc.narg(project)::text)
 AND (sqlc.narg(state)::text IS NULL OR x.state=sqlc.narg(state)::text)
 AND NOT coalesce(x.project_id=ANY(@ignored::text[]),false) AND (NOT @ignore_none::boolean OR x.project_id IS NOT NULL)
 ORDER BY coalesce(x.project_id,'-')=ANY(@focus::text[]) DESC, x.activity_at DESC, x.id
 LIMIT @lim;

-- name: EntCountSessions :one
-- sessions_list's count for the search in the focus, ignored left out.
WITH x AS (SELECT d.id, d.code, d.project_id, nullif(d.title,'') AS title, d.archived_at, d.actor, d.host_id,
 d.payload->'agentConfig'->>'harness' AS agent_harness,
 coalesce((SELECT e.state::text FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1),'idle') AS state,
 (SELECT e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) AS harness,
 (SELECT m.text FROM message m WHERE m.session_id=d.id ORDER BY m.seq LIMIT 1) AS first_message
 FROM session d)
SELECT count(*) FROM x
 WHERE x.archived_at IS NULL AND coalesce(x.actor,'')<>'program-worker'
 AND NOT EXISTS (SELECT 1 FROM unnest(@words::text[]) AS w(p) WHERE NOT (lower(coalesce(x.title,'')) LIKE w.p OR lower(coalesce(x.first_message,'')) LIKE w.p
  OR lower(x.id) LIKE w.p OR lower(coalesce(x.code,'')) LIKE w.p))
 AND (sqlc.narg(agent)::text IS NULL OR lower(coalesce(x.harness,x.agent_harness,'')) LIKE sqlc.narg(agent)::text)
 AND (sqlc.narg(host)::text IS NULL OR lower(coalesce((SELECT e.host_id FROM execution e WHERE e.session_id=x.id ORDER BY e.seq DESC LIMIT 1),x.host_id,'')) LIKE sqlc.narg(host)::text)
 AND (sqlc.narg(project)::text IS NULL OR lower(coalesce(x.project_id,'')) LIKE sqlc.narg(project)::text)
 AND (sqlc.narg(state)::text IS NULL OR x.state=sqlc.narg(state)::text)
 AND (NOT @focused::boolean OR x.project_id=ANY(@focus::text[]) OR ('-'=ANY(@focus::text[]) AND x.project_id IS NULL))
 AND NOT coalesce(x.project_id=ANY(@ignored::text[]),false) AND (NOT @ignore_none::boolean OR x.project_id IS NOT NULL);

-- name: EntSearchResults :many
-- Result hits by name as artifact_list searches (deleted ones left out); the focus first, then newest.
SELECT id, name, state, project_id, task_id, created_at AS at FROM artifact
 WHERE tombstoned_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM unnest(@words::text[]) AS w(p) WHERE NOT (lower(name) LIKE w.p))
 AND (sqlc.narg(project)::text IS NULL OR lower(coalesce(project_id,'')) LIKE sqlc.narg(project)::text)
 AND (sqlc.narg(state)::text IS NULL OR state=sqlc.narg(state)::text)
 AND NOT coalesce(project_id=ANY(@ignored::text[]),false) AND (NOT @ignore_none::boolean OR project_id IS NOT NULL)
 ORDER BY coalesce(project_id,'-')=ANY(@focus::text[]) DESC, seq DESC
 LIMIT @lim;

-- name: EntCountResults :one
-- artifact_list's count for the search in the focus (the bare project_id column, so its index serves), ignored left out.
SELECT count(*) FROM artifact
 WHERE tombstoned_at IS NULL
 AND NOT EXISTS (SELECT 1 FROM unnest(@words::text[]) AS w(p) WHERE NOT (lower(name) LIKE w.p))
 AND (sqlc.narg(project)::text IS NULL OR lower(coalesce(project_id,'')) LIKE sqlc.narg(project)::text)
 AND (sqlc.narg(state)::text IS NULL OR state=sqlc.narg(state)::text)
 AND (NOT @focused::boolean OR project_id=ANY(@focus::text[]) OR ('-'=ANY(@focus::text[]) AND project_id IS NULL))
 AND NOT coalesce(project_id=ANY(@ignored::text[]),false) AND (NOT @ignore_none::boolean OR project_id IS NOT NULL);

-- name: EntSearchHosts :many
-- Configured hosts (removed ones left out) with the name their record gives.
SELECT host_id, (CASE WHEN jsonb_typeof(payload->'name')='string' THEN payload->>'name' ELSE '' END)::text AS name
 FROM host WHERE state<>'removed' ORDER BY seq;


-- Record writers of records.go (putExecution, putQuestion, the execution totals, the end event, owned).

-- name: EntExecutionStateOf :one
-- An execution's state, '' when there is none.
SELECT coalesce((SELECT state::text FROM execution WHERE id=$1),'')::text;

-- name: EntSessionHasOtherRun :one
-- Another agent run or host job of the session than id.
SELECT EXISTS (SELECT 1 FROM execution WHERE session_id= @session_id AND id<> @id AND kind IN ('agent_run','host_job'));

-- name: EntExecutionPipelineRef :one
SELECT pipeline_id, pipeline_revision, step_id FROM execution WHERE id=$1;

-- name: EntExecutionUpdate :exec
-- putExecution's write of an existing run: its columns and the open rest in data, keeping the trace of its first write.
UPDATE execution SET observation= @observation, project_id= sqlc.narg(project_id), task_id=coalesce(task_id, sqlc.narg(task_id)), experiment_id= sqlc.narg(experiment_id),
 variant= sqlc.narg(variant), parent_execution_id= sqlc.narg(parent_execution_id), session_id= sqlc.narg(session_id), account_id= sqlc.narg(account_id),
 host_id= sqlc.narg(host_id), harness= sqlc.narg(harness), model= sqlc.narg(model), config_ref= @config_ref, started_at= sqlc.narg(started_at), ended_at= sqlc.narg(ended_at),
 data= @data::jsonb || jsonb_strip_nulls(jsonb_build_object('traceparent',data->'traceparent','traceActor',data->'traceActor')),
 directed_by=coalesce(sqlc.narg(directed_by),directed_by), profile_id=coalesce(sqlc.narg(profile_id),profile_id), profile_revision=coalesce(sqlc.narg(profile_revision),profile_revision),
 pipeline_id=coalesce(sqlc.narg(pipeline_id),pipeline_id), pipeline_revision=coalesce(sqlc.narg(pipeline_revision),pipeline_revision), step_id=coalesce(sqlc.narg(step_id),step_id)
 WHERE id= @id;

-- name: EntExecutionDefinitions :exec
UPDATE execution SET definitions= @definitions, profile_id=coalesce(sqlc.narg(profile_id),profile_id), profile_revision=coalesce(sqlc.narg(profile_revision),profile_revision),
 pipeline_id=coalesce(sqlc.narg(pipeline_id),pipeline_id), pipeline_revision=coalesce(sqlc.narg(pipeline_revision),pipeline_revision) WHERE id= @id;

-- name: EntExecutionDirectedBy :exec
UPDATE execution SET directed_by= @directed_by::text WHERE id= @id AND directed_by IS DISTINCT FROM @directed_by::text;

-- name: EntExecutionTaskOf :one
-- The task of an execution, when the task exists.
SELECT e.task_id FROM execution e JOIN task k ON k.id=e.task_id WHERE e.id=$1;

-- name: EntExecutionLockKind :one
-- Locks an execution before its totals (parent before totals, the order every totals writer takes).
SELECT kind::text FROM execution WHERE id=$1 FOR UPDATE;

-- name: EntTotalsConfirmedUsage :exec
-- The totals row of accepted native usage, without completion or process resources.
INSERT INTO execution_totals(execution_id,project_id,directed_by,billing_basis,computed_at)
 SELECT id,project_id,directed_by,CASE
 WHEN account_id IS NOT NULL AND NOT coalesce((SELECT m.is_api_conn FROM fleet_exec_conn_meta('',account_id) m),false) THEN 'subscription'
 WHEN kind IN ('agent_run','child_agent','tool_run') THEN 'metered' ELSE 'unavailable' END,now()
 FROM execution WHERE id=$1
 ON CONFLICT(execution_id) DO UPDATE SET project_id=excluded.project_id,directed_by=excluded.directed_by,
 computed_at=now();

-- name: EntTotalsWrite :exec
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

-- name: EntTotalsModel :exec
-- The model the harness reports (migration 45).
SELECT fleet_write_model(@id::text);

-- name: EntTotalsTokens :exec
-- Tokens from the harness's usage report (migrations 40, 44, 50).
SELECT fleet_write_tokens(@id::text);

-- name: EntTotalsNativeObservations :exec
SELECT fleet_write_native_observations(@id::text);

-- name: EntTotalsRateLimit :exec
-- The last rate-limit reading of the execution.
UPDATE execution_totals SET rate_limit_last=r.payload->'data',rate_limit_last_basis='measured'
 FROM (SELECT payload FROM event WHERE NOT delta AND execution_id= @id::text AND type='limit_reading' ORDER BY seq DESC LIMIT 1) r WHERE execution_id= @id::text;

-- name: EntTotalsProcessClosed :exec
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

-- name: EntExecutionEnded :exec
-- The end of an agent run in its thread, one event with a fixed id.
INSERT INTO event(id,execution_id,session_id,project_id,type,at,payload) SELECT 'ended:'||e.id,e.id,e.session_id,e.project_id,'execution.ended',
 coalesce(e.ended_at,now()),jsonb_build_object('data',jsonb_build_object('fleetNormalized',jsonb_build_object('blocks','[]'::jsonb)))
 FROM execution e WHERE e.id=$1 AND e.kind='agent_run' ON CONFLICT(id) DO NOTHING;

-- name: EntQuestionExists :one
SELECT EXISTS (SELECT 1 FROM question WHERE id=$1);

-- name: EntQuestionRest :exec
-- An agent question record's fields without a column, and its project once known.
UPDATE question SET data= @data, project_id=coalesce(project_id, sqlc.narg(project_id)) WHERE id= @id;

-- name: EntExecutionHost :one
-- The host an agent run or host job record names (its hostId), '' when none.
SELECT (CASE WHEN jsonb_typeof(data->'hostId')='string' THEN data->>'hostId' ELSE '' END)::text FROM execution WHERE id=$1 AND kind IN ('agent_run','host_job');

-- Records by kind (records.go all/get/put): an execution record is execution.data with its typed state, observation
-- and last contact (records.go execJSON); an agent question record is its columns over the open rest in data
-- (agentQuestionJSON); a delegation record is execution.delegation; no kind is a document any more.

-- name: EntExecutionRecords :many
SELECT (data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at))::jsonb FROM execution
 WHERE kind IN ('agent_run','host_job') AND data @> @filter::jsonb ORDER BY seq;

-- name: EntExecutionRecord :one
SELECT (data || jsonb_build_object('state',state,'observation',observation,'lastSeenAt',last_seen_at))::jsonb FROM execution
 WHERE id=$1 AND kind IN ('agent_run','host_job');

-- name: EntDelegationRecords :many
SELECT delegation::jsonb FROM execution WHERE delegation IS NOT NULL AND delegation @> @filter::jsonb ORDER BY seq;

-- name: EntDelegationRecord :one
SELECT delegation::jsonb FROM execution WHERE delegation->>'id'= @id::text LIMIT 1;

-- name: EntDelegationPut :execrows
-- A delegation record on its child execution; the child's parent once known.
UPDATE execution SET delegation= @delegation, parent_execution_id=coalesce(parent_execution_id, sqlc.narg(parent_execution_id)) WHERE id= @id;

-- name: EntAgentQuestions :many
-- Agent question records whose columns equal the given values.
SELECT (data || jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'taskId',task_id,'requestId',request_id,
 'question',text,'state',state,'answer',answer,'answeredBy',answered_by,'answeredAt',(extract(epoch FROM answered_at)*1000)::bigint,'createdAt',(extract(epoch FROM asked_at)*1000)::bigint)))::jsonb
 FROM question WHERE kind='agent_question'
 AND (sqlc.narg(execution_id)::text IS NULL OR execution_id=sqlc.narg(execution_id)::text)
 AND (sqlc.narg(session_id)::text IS NULL OR session_id=sqlc.narg(session_id)::text)
 AND (sqlc.narg(project_id)::text IS NULL OR project_id=sqlc.narg(project_id)::text)
 AND (sqlc.narg(request_id)::text IS NULL OR request_id=sqlc.narg(request_id)::text)
 AND (sqlc.narg(state)::text IS NULL OR state::text=sqlc.narg(state)::text)
 ORDER BY seq;

-- name: EntAgentQuestion :one
SELECT (data || jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'taskId',task_id,'requestId',request_id,
 'question',text,'state',state,'answer',answer,'answeredBy',answered_by,'answeredAt',(extract(epoch FROM answered_at)*1000)::bigint,'createdAt',(extract(epoch FROM asked_at)*1000)::bigint)))::jsonb
 FROM question WHERE id=$1 AND kind='agent_question';
