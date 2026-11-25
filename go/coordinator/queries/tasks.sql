-- Tasks, their questions and acceptance (tasks.go), owner problems (problems.go) and execution trace origins
-- (execution_trace.go). tasks.go alone writes task.state (taskTransition); problems.go alone writes question.cause.

-- name: TaskGet :one
SELECT * FROM task WHERE id=$1;

-- name: TaskNotFoundDiag :one
-- Diagnostic visibility lookup for a task row after a 404 (called only with widened scope and fleet.deleted=on).
SELECT
  coalesce(project_id, '')::text AS project_id,
  (tombstoned_at IS NOT NULL)::boolean AS tombstoned,
  (project_id IS NOT NULL AND project_id = ANY (fleet_deleted_projects()))::boolean AS project_deleted
FROM task
WHERE id = $1;

-- name: TaskStateForShare :one
-- The task's state, held against a concurrent cancellation until the caller commits.
SELECT state FROM task WHERE id=$1 FOR SHARE;

-- name: TaskHasOpenBlockingQuestion :one
SELECT EXISTS (SELECT 1 FROM question WHERE task_id=$1 AND state='open' AND kind IN ('operator_decision','agent_question','secret'))::boolean;

-- name: TaskHasOpenDependency :one
SELECT EXISTS (SELECT 1 FROM task_dependency d JOIN task t ON t.id=d.depends_on_task_id WHERE d.task_id=$1 AND d.kind='blocks' AND t.state<>'completed')::boolean;

-- name: TaskHasStartedExecution :one
SELECT EXISTS (SELECT 1 FROM execution WHERE task_id=$1 AND state<>'preparing')::boolean;

-- name: TaskSetState :exec
UPDATE task SET state= @state,wait_reason=sqlc.narg(wait_reason),state_before_pause=sqlc.narg(state_before_pause),
 pause_reason=sqlc.narg(pause_reason),updated_at=now() WHERE id= @id;

-- name: TaskCancelLinkedExecutions :many
-- The open runs of a task (its own, those of its sessions, delegated children and pipeline steps of its runs), runs
-- first, locked.
WITH roots AS (
 SELECT r.id,r.kind FROM execution r
 WHERE r.task_id= @task_id OR (r.task_id IS NULL AND EXISTS (SELECT 1 FROM session s WHERE s.id=r.session_id AND s.task_id= @task_id))
)
SELECT e.id,e.kind FROM execution e
WHERE e.state IN ('preparing','running','waiting')
AND (e.id IN (SELECT id FROM roots) OR (e.task_id IS NULL AND (
 (e.delegation IS NOT NULL AND e.parent_execution_id IN (SELECT id FROM roots)) OR
 e.data->>'pipelineRunId' IN (SELECT id FROM roots WHERE kind='pipeline_run')
))) AND e.kind IN ('pipeline_run','agent_run','host_job')
ORDER BY (e.kind='pipeline_run') DESC,e.seq FOR UPDATE;

-- name: TaskCancelQueuedMessages :exec
UPDATE queued_message m SET state='cancelled',updated_at=now()
WHERE m.state IN ('queued','steering') AND m.tombstoned_at IS NULL
AND (m.task_id= @task_id OR (m.task_id IS NULL AND EXISTS (SELECT 1 FROM session s WHERE s.id=m.session_id AND s.task_id= @task_id)));

-- name: TaskDeleteTriggers :exec
DELETE FROM trigger t USING session s WHERE s.id=t.session_id AND s.task_id=$1;

-- name: TaskSetDirector :exec
UPDATE task SET directed_by= @directed_by,taken_from=sqlc.narg(taken_from),updated_at=now() WHERE id= @id;

-- name: TaskExecutionsSetDirector :exec
UPDATE execution SET directed_by= @directed_by WHERE task_id= @task_id AND state IN ('preparing','running','waiting');

-- name: TaskExecutionLink :one
-- The task, project and session of an execution.
SELECT task_id,project_id,session_id FROM execution WHERE id=$1;

-- name: TaskQuestionInsert :exec
INSERT INTO question(id,kind,project_id,task_id,execution_id,session_id,text,options,asked_by,asked_at,request_id,data)
VALUES( @id, @kind,sqlc.narg(project_id),sqlc.narg(task_id),sqlc.narg(execution_id),sqlc.narg(session_id), @text,sqlc.narg(options),
 sqlc.narg(asked_by),coalesce(sqlc.narg(asked_at)::timestamptz,now()),sqlc.narg(request_id), @data);

-- name: TaskOpenAcceptances :many
SELECT id,execution_id,asked_by,data FROM question WHERE task_id=$1 AND kind='acceptance' AND state='open';

-- name: TaskHasPendingWork :one
SELECT (
 EXISTS (SELECT 1 FROM execution x WHERE x.task_id= @task_id AND x.kind NOT IN ('child_agent','tool_run')
 AND x.state IN ('preparing','running','waiting'))
 OR EXISTS (SELECT 1 FROM queued_message qm JOIN session ss ON ss.id=qm.session_id
 WHERE qm.state IN ('queued','steering') AND qm.tombstoned_at IS NULL AND coalesce(nullif(qm.task_id,''),ss.task_id)= @task_id)
)::boolean;

-- name: TaskIdle :one
-- An in-progress task with no open question or rejection of its latest primary run.
-- The caller has already checked for pending work.
SELECT t.id,t.title,t.project_id,coalesce(l.id,'')::text AS latest_id,coalesce(l.state::text,'')::text AS latest,l.session_id AS latest_session
FROM task t LEFT JOIN LATERAL (SELECT x.id, x.state, x.session_id FROM execution x
 WHERE x.task_id=t.id AND x.kind NOT IN ('child_agent','tool_run') ORDER BY x.seq DESC LIMIT 1) l ON true
WHERE t.id=$1 AND t.state='in_progress'
AND NOT EXISTS (SELECT 1 FROM question qq WHERE qq.task_id=t.id AND qq.state='open')
AND NOT EXISTS (SELECT 1 FROM question qq WHERE qq.task_id=t.id AND qq.kind='acceptance'
 AND qq.state='answered' AND qq.answer='rejected' AND qq.execution_id IS NOT DISTINCT FROM l.id);

-- name: TaskOpenIDs :many
SELECT id FROM task WHERE state IN ('in_progress','waiting') ORDER BY created_at;

-- name: TaskIdleDecisions :one
SELECT coalesce(array_agg(id),'{}')::text[] FROM question WHERE task_id=$1 AND state='open'
 AND kind='operator_decision' AND asked_by='fleet' AND data->>'idle'='true';

-- name: TaskAcceptanceExecution :one
-- The task's latest primary run; with earlier, its latest run with a confirmed success.
SELECT id,kind,state,observation,problem,coalesce(data->'report'->>'outcome','')::text AS outcome FROM execution
 WHERE task_id= @task_id AND kind NOT IN ('child_agent','tool_run')
 AND (NOT @earlier::boolean OR (state='succeeded' AND observation='confirmed'
 AND coalesce(data->'report'->>'outcome',CASE WHEN kind='agent_run' THEN '' ELSE 'succeeded' END)='succeeded'))
ORDER BY seq DESC LIMIT 1;

-- name: TaskAcceptanceArtifacts :many
-- The task's stored originals (of execution, when given) offered for acceptance.
SELECT coalesce(legacy_id,id)::text AS id FROM artifact
WHERE state='stored' AND role='original' AND name NOT LIKE 'mcp-lifecycle-%'
 AND ((sqlc.narg(execution_id)::text IS NULL AND task_id= @task_id)
  OR (sqlc.narg(execution_id)::text IS NOT NULL AND execution_id=sqlc.narg(execution_id) AND (task_id IS NULL OR task_id= @task_id)))
ORDER BY seq;

-- name: TaskAcceptanceAskAgain :exec
UPDATE question SET text= @text,asked_by= @asked_by,asked_at=now(),data=coalesce(data,'{}'::jsonb)||jsonb_build_object('artifactIds', @artifact_ids::jsonb) WHERE id= @id;

-- name: TaskQuestion :one
SELECT * FROM question WHERE id=$1;

-- name: TaskAccept :exec
UPDATE task SET accepted_by= @accepted_by,accepted_at=now(),accepted_artifact_ids= @artifact_ids::text[] WHERE id= @id;

-- name: TaskQuestions :many
-- Question rows (questions_list): each filter optional; projects keeps those projects (projects_null: and questions
-- outside projects), excluded leaves those out (exclude_null: and questions outside projects).
SELECT id,code,kind,project_id,task_id,execution_id,session_id,text,options,asked_by,asked_at,answer,answered_by,answered_at,state::text AS state,
 (data->'artifactIds')::jsonb AS artifact_ids,(data->'cause')::jsonb AS cause
FROM question
WHERE (sqlc.narg(id)::text IS NULL OR id = sqlc.narg(id))
 AND (sqlc.narg(task_id)::text IS NULL OR task_id = sqlc.narg(task_id))
 AND (sqlc.narg(bot_id)::text IS NULL
  OR session_id IN (SELECT s.id FROM session s WHERE s.bot_id = sqlc.narg(bot_id) AND s.tombstoned_at IS NULL)
  OR execution_id IN (SELECT e.id FROM execution e WHERE e.bot_id = sqlc.narg(bot_id))
  OR EXISTS (SELECT 1 FROM execution e WHERE e.bot_id = sqlc.narg(bot_id) AND question.asked_by = 'execution:' || e.id)
  OR task_id IN (SELECT bt.task_id FROM bot_task bt WHERE bt.bot_id = sqlc.narg(bot_id) AND bt.tombstoned_at IS NULL))
 AND (sqlc.narg(state)::text IS NULL OR state::text = sqlc.narg(state))
 AND (sqlc.narg(kind)::text IS NULL OR kind::text = sqlc.narg(kind))
 AND (sqlc.narg(projects)::text[] IS NULL OR project_id = ANY(sqlc.narg(projects)::text[]) OR (@projects_null::boolean AND project_id IS NULL))
 AND NOT coalesce(project_id = ANY(sqlc.narg(excluded)::text[]), false) AND (NOT @exclude_null::boolean OR project_id IS NOT NULL)
ORDER BY seq
LIMIT sqlc.narg(lim)::bigint;

-- name: TaskExecutionsPage :many
-- Execution rows (executions_list) in (seq, id) order after the cursor, each filter optional. system: include, only
-- or anything else (none); projects and excluded as in TaskQuestions. A pipeline step's end is its typed column or
-- the millisecond end older steps kept in data (never inferred from duration, updated_at or the clock; bounds keep the
-- timestamp representable, nested CASE protects casts of malformed legacy JSON).
SELECT id,kind,state,observation,last_seen_at,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,
 environment_id,harness,harness_version,model,config_ref,problem,started_at,
 (CASE WHEN kind<>'pipeline_step' THEN ended_at
 WHEN state NOT IN ('succeeded','failed','cancelled') THEN NULL
 WHEN ended_at IS NOT NULL THEN ended_at
 WHEN jsonb_typeof(data->'finishedAt')='number' THEN
  CASE WHEN (data->>'finishedAt')::numeric BETWEEN 0 AND 253402300799999
   AND trunc((data->>'finishedAt')::numeric)=(data->>'finishedAt')::numeric
  THEN to_timestamp(((data->>'finishedAt')::bigint/1000)::double precision)
   + ((data->>'finishedAt')::bigint%1000)*interval '1 millisecond' END END)::timestamptz AS end_at,
 definitions,(data->'report')::jsonb AS report,system_actor,seq
FROM execution
WHERE (sqlc.narg(id)::text IS NULL OR id = sqlc.narg(id))
 AND (sqlc.narg(task_id)::text IS NULL OR task_id = sqlc.narg(task_id))
 AND (sqlc.narg(project_id)::text IS NULL OR project_id = sqlc.narg(project_id))
 AND (sqlc.narg(parent_execution_id)::text IS NULL OR parent_execution_id = sqlc.narg(parent_execution_id))
 AND (sqlc.narg(session_id)::text IS NULL OR session_id = sqlc.narg(session_id))
 AND (sqlc.narg(bot_id)::text IS NULL OR bot_id = sqlc.narg(bot_id))
 AND (CASE @system::text WHEN 'include' THEN true WHEN 'only' THEN system_actor IS NOT NULL ELSE system_actor IS NULL END)
 AND (NOT @problem::boolean OR problem IS NOT NULL)
 -- Needs you: tool calls are per-call gateway records (a dead key asks its own question). A step shows through its run
 -- when the run carries a problem. A binding problem holds only while the execution is open.
 AND (NOT @needs_you::boolean OR (problem IS NOT NULL AND problem NOT IN ('unknown:abandoned','cancelled') AND kind<>'tool_run'
  AND NOT EXISTS (SELECT 1 FROM problem_dismissal x WHERE x.execution_id=execution.id)
  AND NOT (state IN ('succeeded','failed','cancelled') AND (problem LIKE 'unregistered host %' OR problem LIKE 'unregistered account %' OR problem LIKE 'no registered environment of project %'))
  AND NOT (kind='pipeline_step' AND EXISTS (SELECT 1 FROM execution r WHERE r.kind='pipeline_run' AND r.problem IS NOT NULL AND r.id=coalesce(execution.parent_execution_id,split_part(execution.id,':step:',1))))
  AND (project_id IS NULL OR EXISTS (SELECT 1 FROM definition d WHERE d.kind='project' AND d.id=execution.project_id))))
 AND (sqlc.narg(projects)::text[] IS NULL OR project_id = ANY(sqlc.narg(projects)::text[]) OR (@projects_null::boolean AND project_id IS NULL))
 AND NOT coalesce(project_id = ANY(sqlc.narg(excluded)::text[]), false) AND (NOT @exclude_null::boolean OR project_id IS NOT NULL)
 AND (sqlc.narg(after_seq)::bigint IS NULL OR (seq,id) > (sqlc.narg(after_seq)::bigint, sqlc.narg(after_id)::text))
ORDER BY seq,id
LIMIT sqlc.narg(lim)::int;

-- name: TaskView :one
SELECT execution_count,executions_by_state,latest_result,open_questions FROM task_view WHERE task_id=$1;

-- name: TaskDependencies :many
SELECT d.depends_on_task_id AS id,d.kind,t.code FROM task_dependency d LEFT JOIN task t ON t.id=d.depends_on_task_id WHERE d.task_id=$1;

-- name: TaskAcceptedResult :one
-- A completed task's first accepted result in selection order ($2, a JSON array of ids), among the stored artifacts of
-- its own and delegated runs (the lineage of analytics.go taskLineageCTEs).
WITH RECURSIVE
 candidate_sessions AS (
  SELECT DISTINCT x.session_id FROM execution x WHERE x.task_id = $1 AND x.session_id IS NOT NULL
 ),
 first_task AS (
  SELECT DISTINCT ON (e.session_id) e.session_id, e.task_id
  FROM execution e
  WHERE e.task_id IS NOT NULL AND e.session_id IN (SELECT session_id FROM candidate_sessions)
  ORDER BY e.session_id, e.seq
 ),
 direct_sessions AS (
  SELECT ss.id AS session_id FROM session ss
  WHERE (ss.task_id = $1 OR (coalesce(nullif(ss.task_id, ''), '') = '' AND ss.id IN (SELECT ft.session_id FROM first_task ft WHERE ft.task_id = $1)))
    AND ss.parent_execution_id IS NULL
 ),
 own_execs AS (
  SELECT e.id, e.session_id
  FROM execution e
  WHERE e.kind <> 'child_agent'
    AND e.delegation IS NULL
    AND (
      e.task_id = $1
      OR (e.task_id IS NULL AND e.session_id IN (SELECT session_id FROM direct_sessions))
      OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
         SELECT p.id FROM execution p
         WHERE p.kind <> 'child_agent'
           AND p.delegation IS NULL
           AND (p.task_id = $1 OR (p.task_id IS NULL AND p.session_id IN (SELECT session_id FROM direct_sessions)))
      ))
    )
 ),
 del_seed AS (
  SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
     OR (e.delegation IS NOT NULL AND (e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs) OR e.delegation->>'parentSessionId' IN (SELECT session_id FROM direct_sessions)))
  UNION
  SELECT e.id, e.session_id, true AS follow_session
  FROM execution e
  JOIN session s ON s.id = e.session_id
  WHERE s.parent_execution_id IN (SELECT id FROM own_execs)
     OR (s.created_by_execution_id IN (SELECT id FROM own_execs) AND coalesce(nullif(s.task_id, ''), '') = '')
     OR (s.created_by_session_id IN (SELECT session_id FROM direct_sessions) AND coalesce(nullif(s.task_id, ''), '') = '')
 ),
 del_tree AS (
  SELECT id, session_id, follow_session FROM del_seed
  UNION
  SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
  FROM execution e
  JOIN del_tree dt ON e.parent_execution_id = dt.id
     OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id NOT IN (SELECT session_id FROM direct_sessions) AND e.task_id IS NULL)
     OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
 ),
 accepted_results AS (
  SELECT selection.position,a.id AS row_id,jsonb_build_object('id',coalesce(a.legacy_id,a.id),'artifactId',a.id,'executionId',a.execution_id,'path',a.name,'mediaType',a.media_type,'sha256',a.content_hash,'size',a.size) AS result
  FROM jsonb_array_elements_text($2::jsonb) WITH ORDINALITY selection(id,position)
  JOIN artifact a ON a.id=selection.id OR a.legacy_id=selection.id
  WHERE a.state='stored' AND a.execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
 )
SELECT result::jsonb FROM accepted_results ORDER BY position,row_id LIMIT 1;

-- name: TaskList :many
-- task_list: every task with its view, optionally of one project and in one list state (held: paused and held).
SELECT sqlc.embed(t),v.execution_count,v.executions_by_state,v.open_questions FROM task t JOIN task_view v ON v.task_id=t.id
WHERE (sqlc.narg(project_id)::text IS NULL OR t.project_id = sqlc.narg(project_id))
 AND (sqlc.narg(state)::text IS NULL OR (CASE WHEN t.state='paused' AND t.pause_reason='held' THEN 'held' ELSE t.state::text END) = sqlc.narg(state))
ORDER BY t.seq;

-- name: TaskUpdateFields :exec
-- The given fields of a task (title, expected outcome, priority, result destination); the others stay.
UPDATE task SET title=coalesce(sqlc.narg(title),title),expected_outcome=coalesce(sqlc.narg(expected_outcome),expected_outcome),
 priority=coalesce(sqlc.narg(priority),priority),result_destination=coalesce(sqlc.narg(result_destination),result_destination),updated_at=now()
WHERE id = @id;

-- name: TaskQuestionLock :one
SELECT project_id,state FROM question WHERE id=$1 FOR UPDATE;

-- name: TaskAgentQuestionProject :one
-- An agent's question and the project of its question or session.
SELECT coalesce(q.project_id,s.project_id,'')::text AS project_id FROM question q LEFT JOIN session s ON s.id=q.session_id WHERE q.id=$1 AND q.kind='agent_question';

-- name: TaskCount :one
SELECT count(*)::bigint FROM task;

-- name: TaskInsert :exec
-- A new task, queued, or with held paused before its first queue (fleet-pm-agent.md §4).
INSERT INTO task(id,project_id,title,expected_outcome,completion_requirements,result_destination,created_by,created_by_execution_id,created_by_session_id,
 state,state_before_pause,pause_reason,directed_by,requested_by,priority)
VALUES(@id,sqlc.narg(project_id),@title,sqlc.narg(expected_outcome),@completion_requirements,sqlc.narg(result_destination),sqlc.narg(created_by),
 sqlc.narg(created_by_execution_id),sqlc.narg(created_by_session_id),
 CASE WHEN @held::boolean THEN 'paused' ELSE 'queued' END::task_state,CASE WHEN @held::boolean THEN 'queued' END::task_state,
 CASE WHEN @held::boolean THEN 'held' END,sqlc.narg(directed_by),sqlc.narg(directed_by),@priority);

-- name: TaskDependencyAdd :exec
INSERT INTO task_dependency(task_id,depends_on_task_id,kind,project_id) VALUES(@task_id,@depends_on,'blocks',sqlc.narg(project_id)) ON CONFLICT DO NOTHING;

-- name: TaskExecutionSession :one
SELECT session_id FROM execution WHERE id=$1;

-- Owner problems (problems.go).

-- name: TaskProblemSetCause :exec
UPDATE question SET cause= @cause,addressed_to= @addressed_to WHERE id= @id;

-- name: TaskProblemHeld :one
-- The open or answered question holding cause.
SELECT id,state FROM question WHERE cause=$1 AND state IN ('open','answered') ORDER BY seq DESC LIMIT 1;

-- name: TaskProblemsHeld :many
-- The open or answered questions holding one of causes or a cause starting with prefix, locked.
SELECT id,state FROM question WHERE (cause=ANY(sqlc.narg(causes)::text[]) OR starts_with(cause,sqlc.narg(prefix)::text))
 AND state IN ('open','answered') ORDER BY id FOR UPDATE;

-- name: TaskProblemsLetGo :exec
UPDATE question SET cause=NULL WHERE id=ANY( @ids::text[]) AND cause IS NOT NULL;

-- name: TaskProblemsMatching :many
-- The open or answered questions whose cause matches pattern (a regular expression).
SELECT id,state,cause::text AS cause FROM question WHERE state IN ('open','answered') AND cause ~ @pattern::text ORDER BY id;

-- name: TaskProblemsOfSource :many
SELECT id,cause::text AS cause,coalesce(data->>'misses','0')::text AS misses FROM question
WHERE cause LIKE 'problem:%' AND state IN ('open','answered') AND data->>'source'= @source::text;

-- name: TaskProblemRefresh :exec
-- A present cause: its open question's text refreshed, its miss count cleared.
UPDATE question SET text=CASE WHEN state='open' THEN @text::text ELSE text END, data=data-'misses'
WHERE id= @id AND (data ? 'misses' OR (state='open' AND text IS DISTINCT FROM @text::text));

-- name: TaskProblemPresent :exec
UPDATE question SET data=data-'misses' WHERE id=$1 AND data ? 'misses';

-- name: TaskProblemMissed :exec
UPDATE question SET data=jsonb_set(data,'{misses}',to_jsonb( @misses::int)) WHERE id= @id;

-- Execution trace origins (execution_trace.go).

-- name: TaskTraceCommand :one
SELECT traceparent,(payload->'run')::jsonb AS run FROM command WHERE id=$1;

-- name: TaskTraceExecution :one
-- An agent run's or host job's trace origin and run attributes.
SELECT project_id,task_id,account_id,experiment_id,coalesce(data->>'traceparent','')::text AS traceparent,
 coalesce(nullif(data->>'traceActor',''),nullif(data->>'directedBy',''),directed_by,'')::text AS actor
FROM execution WHERE id=$1 AND kind IN ('agent_run','host_job');

-- name: TaskTraceOf :one
SELECT coalesce(data->>'traceparent','')::text AS traceparent FROM execution WHERE id=$1;

-- name: TaskDerivedDrift :one
-- The tasks whose stored derived row differs from the one their executions and questions give (task_derived_check).
SELECT coalesce(jsonb_agg(jsonb_build_object('taskId', d.task_id, 'stored', d.stored, 'expected', d.expected)), '[]')::jsonb AS drifted
FROM task_derived_drift() d;

-- name: TaskRowRecord :one
-- The task's row as a record keyed by column name (taskRow).
SELECT to_jsonb(t)::jsonb FROM task t WHERE t.id=$1;

-- name: TaskHasPrimaryExecution :one
SELECT EXISTS (SELECT 1 FROM execution WHERE task_id=$1 AND kind NOT IN ('child_agent','tool_run'))::boolean;

-- name: TaskDeliveryCandidates :many
-- Open tasks eligible for delivery reconciliation (queued or in_progress, unaccepted).
SELECT id,coalesce(code,'')::text AS code,coalesce(project_id,'')::text AS project_id,title,
 coalesce(expected_outcome,'')::text AS expected_outcome,state::text AS state,
 completion_requirements,coalesce(result_destination,'')::text AS result_destination,created_at
FROM task
WHERE state IN ('queued','in_progress') AND accepted_at IS NULL AND tombstoned_at IS NULL
ORDER BY created_at;

-- name: TaskDeliverySessions :many
-- Sessions bound to candidate tasks, with all distinct task IDs of their executions.
SELECT s.id AS session_id,coalesce(s.task_id,'')::text AS task_id,
 coalesce((SELECT array_agg(DISTINCT e.task_id) FROM execution e WHERE e.session_id=s.id AND e.task_id IS NOT NULL),'{}')::text[] AS exec_task_ids
FROM session s
WHERE s.tombstoned_at IS NULL
 AND (s.task_id = ANY( @task_ids::text[])
  OR EXISTS (SELECT 1 FROM execution e WHERE e.session_id=s.id AND e.task_id = ANY( @task_ids::text[])));

-- name: TaskDeliveryForgeRefs :many
-- Forge code-review audit entries recording PR repo/branch/headSha for candidate tasks.
SELECT (value->>'taskId')::text AS task_id,coalesce(value->>'repo','')::text AS repo,
 coalesce(value->>'branch','')::text AS branch,coalesce(value->>'headSha','')::text AS head_sha
FROM audit_log
WHERE split_part(target,':',1)='forge' AND field='code-review' AND (value->>'taskId') = ANY( @task_ids::text[])
ORDER BY seq DESC;

-- name: TaskDeliveryEvidence :many
-- Confirmed worker reports and done board posts referencing PRs or post-delivery verification.
SELECT 'execution'::text AS source,e.task_id::text AS task_id,
 coalesce(e.data->'report'->>'outcome','')::text AS outcome,
 coalesce(e.data->'report'->>'summary','')::text AS text,
 coalesce(e.ended_at,e.started_at,e.updated_at)::timestamptz AS at
FROM execution e
WHERE e.task_id = ANY( @task_ids::text[]) AND e.kind NOT IN ('child_agent','tool_run')
 AND e.state='succeeded' AND e.observation='confirmed' AND coalesce(e.data->'report'->>'outcome','') <> 'failed'
 AND (coalesce(e.data->'report'->>'outcome','')='succeeded' OR coalesce(e.data->'report'->>'summary','') <> '')
UNION ALL
SELECT 'board'::text AS source,b.scope_id::text AS task_id,''::text AS outcome,b.text,b.created_at AS at
FROM board_message b
WHERE b.scope_kind='task' AND b.scope_id = ANY( @task_ids::text[]) AND b.kind='done' AND b.tombstoned_at IS NULL;

-- name: TaskDeliveryRejections :many
SELECT coalesce(execution_id,'')::text AS execution_id,data
FROM question
WHERE task_id=$1 AND kind='acceptance' AND state='answered' AND answer='rejected';

-- name: TaskStoredArtifactNames :many
SELECT coalesce(legacy_id,id)::text AS id,name
FROM artifact
WHERE task_id=$1 AND state='stored' AND role='original' AND name NOT LIKE 'mcp-lifecycle-%' AND tombstoned_at IS NULL
ORDER BY seq;

-- name: TaskHistory :many
-- Task history (fleet-conversations.md#entities): the conversations that created the task, run on it, or are a bot's
-- conversation for subject task:<id>, oldest first; host command sessions are not conversations.
SELECT s.id, coalesce(s.code, '')::text AS code,
 coalesce(nullif(s.title, ''), nullif(left(split_part(btrim((SELECT m.text FROM message m WHERE m.session_id = s.id ORDER BY m.seq LIMIT 1)), E'\n', 1), 120), ''), 'Conversation')::text AS title,
 coalesce(nullif(s.payload->>'role', ''), s.payload->'agentConfig'->>'id', '')::text AS role,
 coalesce(s.bot_id, '')::text AS bot_id,
 coalesce((SELECT r.name FROM bot_revision r WHERE r.bot_id = s.bot_id ORDER BY r.revision DESC LIMIT 1), '')::text AS bot_name,
 coalesce(ss.state, 'idle')::text AS state, coalesce(ss.executions, 0)::bigint AS turns, ss.activity_at,
 coalesce(s.id = t.created_by_session_id, false)::boolean AS created_task
FROM task t
JOIN session s ON s.id = t.created_by_session_id OR s.task_id = t.id OR (s.bot_id IS NOT NULL AND s.subject = 'task:' || t.id)
 OR s.id IN (SELECT e.session_id FROM execution e WHERE e.task_id = t.id AND e.session_id IS NOT NULL)
LEFT JOIN session_summary ss ON ss.session_id = s.id
WHERE t.id = $1 AND s.tombstoned_at IS NULL AND coalesce(s.actor, '') <> 'program-worker'
ORDER BY s.seq;
