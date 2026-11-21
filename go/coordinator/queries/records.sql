-- Records of the conversation, host and account kinds (generated once from the former convTables, stage 8e): each
-- kind's row as its record (the payload under the columns, a column absent when NULL), its rows whose record contains
-- a filter (an indexed text column compared first) and its write: every column from its field when the field has the
-- column's type (absent or another type: NULL, the value kept in payload), payload the other fields. A state machine's
-- column (host.state, connection_key.state) is not written here: a new row starts in its default state, which the
-- machine's transition moves (records.go recStateful).

-- name: RecSessionGet :one
SELECT (session.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'projectId',project_id,'taskId',task_id,'hostId',host_id,'actor',actor,'directedBy',directed_by,'directorAccessRevokedAt',(extract(epoch FROM director_access_revoked_at)*1000)::bigint,'parentExecutionId',parent_execution_id,'createdByExecutionId',created_by_execution_id,'createdBySessionId',created_by_session_id,'sourceSessionId',source_session_id,'pipelineRunId',pipeline_run_id,'createdAt',(extract(epoch FROM created_at)*1000)::bigint,'archivedAt',(extract(epoch FROM archived_at)*1000)::bigint,'title',title,'titleBy',title_by,'titleCall',title_call,'titleAttemptAt',(extract(epoch FROM title_attempt_at)*1000)::bigint,'titleAttempts',nullif(title_attempts,0),'titleUnknownCall',title_unknown_call)))::jsonb FROM session WHERE id=$1;

-- name: RecSessionGetLocked :one
SELECT (session.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'projectId',project_id,'taskId',task_id,'hostId',host_id,'actor',actor,'directedBy',directed_by,'directorAccessRevokedAt',(extract(epoch FROM director_access_revoked_at)*1000)::bigint,'parentExecutionId',parent_execution_id,'createdByExecutionId',created_by_execution_id,'createdBySessionId',created_by_session_id,'sourceSessionId',source_session_id,'pipelineRunId',pipeline_run_id,'createdAt',(extract(epoch FROM created_at)*1000)::bigint,'archivedAt',(extract(epoch FROM archived_at)*1000)::bigint,'title',title,'titleBy',title_by,'titleCall',title_call,'titleAttemptAt',(extract(epoch FROM title_attempt_at)*1000)::bigint,'titleAttempts',nullif(title_attempts,0),'titleUnknownCall',title_unknown_call)))::jsonb FROM session WHERE id=$1 FOR UPDATE;

-- name: RecSessionAll :many
SELECT (session.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'projectId',project_id,'taskId',task_id,'hostId',host_id,'actor',actor,'directedBy',directed_by,'directorAccessRevokedAt',(extract(epoch FROM director_access_revoked_at)*1000)::bigint,'parentExecutionId',parent_execution_id,'createdByExecutionId',created_by_execution_id,'createdBySessionId',created_by_session_id,'sourceSessionId',source_session_id,'pipelineRunId',pipeline_run_id,'createdAt',(extract(epoch FROM created_at)*1000)::bigint,'archivedAt',(extract(epoch FROM archived_at)*1000)::bigint,'title',title,'titleBy',title_by,'titleCall',title_call,'titleAttemptAt',(extract(epoch FROM title_attempt_at)*1000)::bigint,'titleAttempts',nullif(title_attempts,0),'titleUnknownCall',title_unknown_call)))::jsonb FROM session
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR session.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR session.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'taskId' IS NULL OR session.task_id=sqlc.arg(filter)::jsonb->>'taskId')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR session.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'actor' IS NULL OR session.actor=sqlc.arg(filter)::jsonb->>'actor')
  AND (sqlc.arg(filter)::jsonb->>'directedBy' IS NULL OR session.directed_by=sqlc.arg(filter)::jsonb->>'directedBy')
  AND (sqlc.arg(filter)::jsonb->>'parentExecutionId' IS NULL OR session.parent_execution_id=sqlc.arg(filter)::jsonb->>'parentExecutionId')
  AND (sqlc.arg(filter)::jsonb->>'createdByExecutionId' IS NULL OR session.created_by_execution_id=sqlc.arg(filter)::jsonb->>'createdByExecutionId')
  AND (sqlc.arg(filter)::jsonb->>'createdBySessionId' IS NULL OR session.created_by_session_id=sqlc.arg(filter)::jsonb->>'createdBySessionId')
  AND (sqlc.arg(filter)::jsonb->>'sourceSessionId' IS NULL OR session.source_session_id=sqlc.arg(filter)::jsonb->>'sourceSessionId')
  AND (sqlc.arg(filter)::jsonb->>'pipelineRunId' IS NULL OR session.pipeline_run_id=sqlc.arg(filter)::jsonb->>'pipelineRunId')
  AND (sqlc.arg(filter)::jsonb->>'title' IS NULL OR session.title=sqlc.arg(filter)::jsonb->>'title')
  AND (sqlc.arg(filter)::jsonb->>'titleBy' IS NULL OR session.title_by=sqlc.arg(filter)::jsonb->>'titleBy')
  AND (sqlc.arg(filter)::jsonb->>'titleCall' IS NULL OR session.title_call=sqlc.arg(filter)::jsonb->>'titleCall')
  AND (sqlc.arg(filter)::jsonb->>'titleUnknownCall' IS NULL OR session.title_unknown_call=sqlc.arg(filter)::jsonb->>'titleUnknownCall')
  AND (session.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'projectId',project_id,'taskId',task_id,'hostId',host_id,'actor',actor,'directedBy',directed_by,'directorAccessRevokedAt',(extract(epoch FROM director_access_revoked_at)*1000)::bigint,'parentExecutionId',parent_execution_id,'createdByExecutionId',created_by_execution_id,'createdBySessionId',created_by_session_id,'sourceSessionId',source_session_id,'pipelineRunId',pipeline_run_id,'createdAt',(extract(epoch FROM created_at)*1000)::bigint,'archivedAt',(extract(epoch FROM archived_at)*1000)::bigint,'title',title,'titleBy',title_by,'titleCall',title_call,'titleAttemptAt',(extract(epoch FROM title_attempt_at)*1000)::bigint,'titleAttempts',nullif(title_attempts,0),'titleUnknownCall',title_unknown_call)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY session.seq;

-- name: RecSessionPut :exec
INSERT INTO session(id, project_id, task_id, host_id, actor, directed_by, director_access_revoked_at, parent_execution_id, created_by_execution_id, created_by_session_id, source_session_id, pipeline_run_id, created_at, archived_at, title, title_by, title_call, title_attempt_at, title_attempts, title_unknown_call, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  CASE WHEN jsonb_typeof(r->'taskId')='string' THEN r->>'taskId' END,
  CASE WHEN jsonb_typeof(r->'hostId')='string' THEN r->>'hostId' END,
  CASE WHEN jsonb_typeof(r->'actor')='string' THEN r->>'actor' END,
  CASE WHEN jsonb_typeof(r->'directedBy')='string' THEN r->>'directedBy' END,
  CASE WHEN jsonb_typeof(r->'directorAccessRevokedAt')='number' THEN to_timestamp((r->>'directorAccessRevokedAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'parentExecutionId')='string' THEN r->>'parentExecutionId' END,
  CASE WHEN jsonb_typeof(r->'createdByExecutionId')='string' THEN r->>'createdByExecutionId' END,
  CASE WHEN jsonb_typeof(r->'createdBySessionId')='string' THEN r->>'createdBySessionId' END,
  CASE WHEN jsonb_typeof(r->'sourceSessionId')='string' THEN r->>'sourceSessionId' END,
  CASE WHEN jsonb_typeof(r->'pipelineRunId')='string' THEN r->>'pipelineRunId' END,
  CASE WHEN jsonb_typeof(r->'createdAt')='number' THEN to_timestamp((r->>'createdAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'archivedAt')='number' THEN to_timestamp((r->>'archivedAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'title')='string' THEN r->>'title' END,
  CASE WHEN jsonb_typeof(r->'titleBy')='string' THEN r->>'titleBy' END,
  CASE WHEN jsonb_typeof(r->'titleCall')='string' THEN r->>'titleCall' END,
  CASE WHEN jsonb_typeof(r->'titleAttemptAt')='number' THEN to_timestamp((r->>'titleAttemptAt')::float8/1000) END,
  coalesce(CASE WHEN jsonb_typeof(r->'titleAttempts')='number' AND (r->'titleAttempts')::numeric=trunc((r->'titleAttempts')::numeric) THEN (r->>'titleAttempts')::numeric::int END,0),
  CASE WHEN jsonb_typeof(r->'titleUnknownCall')='string' THEN r->>'titleUnknownCall' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'taskId')='null' OR jsonb_typeof(r->'taskId')='string' THEN 'taskId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'hostId')='null' OR jsonb_typeof(r->'hostId')='string' THEN 'hostId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'actor')='null' OR jsonb_typeof(r->'actor')='string' THEN 'actor' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'directedBy')='null' OR jsonb_typeof(r->'directedBy')='string' THEN 'directedBy' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'directorAccessRevokedAt')='null' OR jsonb_typeof(r->'directorAccessRevokedAt')='number' THEN 'directorAccessRevokedAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'parentExecutionId')='null' OR jsonb_typeof(r->'parentExecutionId')='string' THEN 'parentExecutionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'createdByExecutionId')='null' OR jsonb_typeof(r->'createdByExecutionId')='string' THEN 'createdByExecutionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'createdBySessionId')='null' OR jsonb_typeof(r->'createdBySessionId')='string' THEN 'createdBySessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'sourceSessionId')='null' OR jsonb_typeof(r->'sourceSessionId')='string' THEN 'sourceSessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'pipelineRunId')='null' OR jsonb_typeof(r->'pipelineRunId')='string' THEN 'pipelineRunId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'createdAt')='null' OR jsonb_typeof(r->'createdAt')='number' THEN 'createdAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'archivedAt')='null' OR jsonb_typeof(r->'archivedAt')='number' THEN 'archivedAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'title')='null' OR jsonb_typeof(r->'title')='string' THEN 'title' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'titleBy')='null' OR jsonb_typeof(r->'titleBy')='string' THEN 'titleBy' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'titleCall')='null' OR jsonb_typeof(r->'titleCall')='string' THEN 'titleCall' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'titleAttemptAt')='null' OR jsonb_typeof(r->'titleAttemptAt')='number' THEN 'titleAttemptAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'titleAttempts')='null' OR jsonb_typeof(r->'titleAttempts')='number' AND (r->'titleAttempts')::numeric=trunc((r->'titleAttempts')::numeric) THEN 'titleAttempts' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'titleUnknownCall')='null' OR jsonb_typeof(r->'titleUnknownCall')='string' THEN 'titleUnknownCall' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  project_id=excluded.project_id,
  task_id=excluded.task_id,
  host_id=excluded.host_id,
  actor=excluded.actor,
  directed_by=excluded.directed_by,
  director_access_revoked_at=excluded.director_access_revoked_at,
  parent_execution_id=excluded.parent_execution_id,
  created_by_execution_id=excluded.created_by_execution_id,
  created_by_session_id=excluded.created_by_session_id,
  source_session_id=excluded.source_session_id,
  pipeline_run_id=excluded.pipeline_run_id,
  created_at=excluded.created_at,
  archived_at=excluded.archived_at,
  title=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title ELSE NULL END,
  title_by=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_by ELSE NULL END,
  title_call=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_call ELSE NULL END,
  title_attempt_at=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_attempt_at ELSE NULL END,
  title_attempts=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_attempts ELSE 0 END,
  title_unknown_call=CASE WHEN session.project_id IS NOT DISTINCT FROM excluded.project_id THEN session.title_unknown_call ELSE NULL END;

-- name: RecMessageGet :one
SELECT (message.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'executionId',execution_id,'projectId',project_id,'requestId',request_id,'text',text,'sentBy',sent_by,'source',source)))::jsonb FROM message WHERE id=$1;

-- name: RecMessageAll :many
SELECT (message.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'executionId',execution_id,'projectId',project_id,'requestId',request_id,'text',text,'sentBy',sent_by,'source',source)))::jsonb FROM message
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR message.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR message.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR message.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR message.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'requestId' IS NULL OR message.request_id=sqlc.arg(filter)::jsonb->>'requestId')
  AND (sqlc.arg(filter)::jsonb->>'text' IS NULL OR message.text=sqlc.arg(filter)::jsonb->>'text')
  AND (sqlc.arg(filter)::jsonb->>'sentBy' IS NULL OR message.sent_by=sqlc.arg(filter)::jsonb->>'sentBy')
  AND (sqlc.arg(filter)::jsonb->>'source' IS NULL OR message.source=sqlc.arg(filter)::jsonb->>'source')
  AND (message.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'executionId',execution_id,'projectId',project_id,'requestId',request_id,'text',text,'sentBy',sent_by,'source',source)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY message.seq;

-- name: RecMessagePut :exec
INSERT INTO message(id, session_id, execution_id, project_id, request_id, text, sent_by, source, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'sessionId')='string' THEN r->>'sessionId' END,
  CASE WHEN jsonb_typeof(r->'executionId')='string' THEN r->>'executionId' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  CASE WHEN jsonb_typeof(r->'requestId')='string' THEN r->>'requestId' END,
  CASE WHEN jsonb_typeof(r->'text')='string' THEN r->>'text' END,
  CASE WHEN jsonb_typeof(r->'sentBy')='string' THEN r->>'sentBy' END,
  CASE WHEN jsonb_typeof(r->'source')='string' THEN r->>'source' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'sessionId')='null' OR jsonb_typeof(r->'sessionId')='string' THEN 'sessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'executionId')='null' OR jsonb_typeof(r->'executionId')='string' THEN 'executionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'requestId')='null' OR jsonb_typeof(r->'requestId')='string' THEN 'requestId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'text')='null' OR jsonb_typeof(r->'text')='string' THEN 'text' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'sentBy')='null' OR jsonb_typeof(r->'sentBy')='string' THEN 'sentBy' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'source')='null' OR jsonb_typeof(r->'source')='string' THEN 'source' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  session_id=excluded.session_id,
  execution_id=excluded.execution_id,
  project_id=excluded.project_id,
  request_id=excluded.request_id,
  text=excluded.text,
  sent_by=excluded.sent_by,
  source=excluded.source;

-- name: RecEventGet :one
SELECT (event.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'seq',event_seq,'type',type,'timestamp',(extract(epoch FROM at)*1000)::bigint)))::jsonb FROM event WHERE id=$1 AND NOT event.delta;

-- name: RecEventAll :many
SELECT (event.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'seq',event_seq,'type',type,'timestamp',(extract(epoch FROM at)*1000)::bigint)))::jsonb FROM event
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR event.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR event.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR event.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR event.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'type' IS NULL OR event.type=sqlc.arg(filter)::jsonb->>'type') AND NOT event.delta
  AND (event.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'seq',event_seq,'type',type,'timestamp',(extract(epoch FROM at)*1000)::bigint)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY event.seq;

-- name: RecEventAnyAll :many
SELECT (event.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'seq',event_seq,'type',type,'timestamp',(extract(epoch FROM at)*1000)::bigint)))::jsonb FROM event
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR event.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR event.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR event.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR event.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'type' IS NULL OR event.type=sqlc.arg(filter)::jsonb->>'type')
  AND (event.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'projectId',project_id,'seq',event_seq,'type',type,'timestamp',(extract(epoch FROM at)*1000)::bigint)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY event.seq;

-- name: RecQueuedMessageGet :one
SELECT (queued_message.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id,'taskId',task_id,'requestId',request_id,'text',text,'state',state,'executionId',execution_id,'steerCommandId',steer_command_id,'sentBy',sent_by,'source',source,'createdAt',(extract(epoch FROM created_at)*1000)::bigint)))::jsonb FROM queued_message WHERE id=$1;

-- name: RecQueuedMessageAll :many
SELECT (queued_message.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id,'taskId',task_id,'requestId',request_id,'text',text,'state',state,'executionId',execution_id,'steerCommandId',steer_command_id,'sentBy',sent_by,'source',source,'createdAt',(extract(epoch FROM created_at)*1000)::bigint)))::jsonb FROM queued_message
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR queued_message.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR queued_message.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR queued_message.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'taskId' IS NULL OR queued_message.task_id=sqlc.arg(filter)::jsonb->>'taskId')
  AND (sqlc.arg(filter)::jsonb->>'requestId' IS NULL OR queued_message.request_id=sqlc.arg(filter)::jsonb->>'requestId')
  AND (sqlc.arg(filter)::jsonb->>'text' IS NULL OR queued_message.text=sqlc.arg(filter)::jsonb->>'text')
  AND (sqlc.arg(filter)::jsonb->>'state' IS NULL OR queued_message.state=sqlc.arg(filter)::jsonb->>'state')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR queued_message.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'steerCommandId' IS NULL OR queued_message.steer_command_id=sqlc.arg(filter)::jsonb->>'steerCommandId')
  AND (sqlc.arg(filter)::jsonb->>'sentBy' IS NULL OR queued_message.sent_by=sqlc.arg(filter)::jsonb->>'sentBy')
  AND (sqlc.arg(filter)::jsonb->>'source' IS NULL OR queued_message.source=sqlc.arg(filter)::jsonb->>'source')
  AND (queued_message.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id,'taskId',task_id,'requestId',request_id,'text',text,'state',state,'executionId',execution_id,'steerCommandId',steer_command_id,'sentBy',sent_by,'source',source,'createdAt',(extract(epoch FROM created_at)*1000)::bigint)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY queued_message.seq;

-- name: RecQueuedMessagePut :exec
INSERT INTO queued_message(id, session_id, project_id, task_id, request_id, text, state, execution_id, steer_command_id, sent_by, source, created_at, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'sessionId')='string' THEN r->>'sessionId' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  CASE WHEN jsonb_typeof(r->'taskId')='string' THEN r->>'taskId' END,
  CASE WHEN jsonb_typeof(r->'requestId')='string' THEN r->>'requestId' END,
  CASE WHEN jsonb_typeof(r->'text')='string' THEN r->>'text' END,
  CASE WHEN jsonb_typeof(r->'state')='string' THEN r->>'state' END,
  CASE WHEN jsonb_typeof(r->'executionId')='string' THEN r->>'executionId' END,
  CASE WHEN jsonb_typeof(r->'steerCommandId')='string' THEN r->>'steerCommandId' END,
  CASE WHEN jsonb_typeof(r->'sentBy')='string' THEN r->>'sentBy' END,
  CASE WHEN jsonb_typeof(r->'source')='string' THEN r->>'source' END,
  CASE WHEN jsonb_typeof(r->'createdAt')='number' THEN to_timestamp((r->>'createdAt')::float8/1000) END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'sessionId')='null' OR jsonb_typeof(r->'sessionId')='string' THEN 'sessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'taskId')='null' OR jsonb_typeof(r->'taskId')='string' THEN 'taskId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'requestId')='null' OR jsonb_typeof(r->'requestId')='string' THEN 'requestId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'text')='null' OR jsonb_typeof(r->'text')='string' THEN 'text' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'state')='null' OR jsonb_typeof(r->'state')='string' THEN 'state' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'executionId')='null' OR jsonb_typeof(r->'executionId')='string' THEN 'executionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'steerCommandId')='null' OR jsonb_typeof(r->'steerCommandId')='string' THEN 'steerCommandId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'sentBy')='null' OR jsonb_typeof(r->'sentBy')='string' THEN 'sentBy' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'source')='null' OR jsonb_typeof(r->'source')='string' THEN 'source' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'createdAt')='null' OR jsonb_typeof(r->'createdAt')='number' THEN 'createdAt' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  session_id=excluded.session_id,
  project_id=excluded.project_id,
  task_id=excluded.task_id,
  request_id=excluded.request_id,
  text=excluded.text,
  state=excluded.state,
  execution_id=excluded.execution_id,
  steer_command_id=excluded.steer_command_id,
  sent_by=excluded.sent_by,
  source=excluded.source,
  created_at=excluded.created_at;

-- name: RecCommandGet :one
SELECT (command.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'hostId',host_id,'projectId',project_id,'kind',kind,'status',status::text,'stopDetail',stop_detail,'handedOut',handed_out,'traceparent',traceparent)))::jsonb FROM command WHERE id=$1;

-- name: RecCommandAll :many
SELECT (command.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'hostId',host_id,'projectId',project_id,'kind',kind,'status',status::text,'stopDetail',stop_detail,'handedOut',handed_out,'traceparent',traceparent)))::jsonb FROM command
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR command.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR command.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR command.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR command.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR command.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'kind' IS NULL OR command.kind=sqlc.arg(filter)::jsonb->>'kind')
  AND (sqlc.arg(filter)::jsonb->>'stopDetail' IS NULL OR command.stop_detail=sqlc.arg(filter)::jsonb->>'stopDetail')
  AND (sqlc.arg(filter)::jsonb->>'traceparent' IS NULL OR command.traceparent=sqlc.arg(filter)::jsonb->>'traceparent')
  AND (command.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'sessionId',session_id,'hostId',host_id,'projectId',project_id,'kind',kind,'status',status::text,'stopDetail',stop_detail,'handedOut',handed_out,'traceparent',traceparent)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY command.seq;

-- name: RecHandoffGet :one
SELECT (handoff.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id,'state',state)))::jsonb FROM handoff WHERE id=$1;

-- name: RecHandoffAll :many
SELECT (handoff.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id,'state',state)))::jsonb FROM handoff
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR handoff.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR handoff.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR handoff.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'state' IS NULL OR handoff.state=sqlc.arg(filter)::jsonb->>'state')
  AND (handoff.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id,'state',state)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY handoff.seq;

-- name: RecHandoffPut :exec
INSERT INTO handoff(id, session_id, project_id, state, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'sessionId')='string' THEN r->>'sessionId' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  CASE WHEN jsonb_typeof(r->'state')='string' THEN r->>'state' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'sessionId')='null' OR jsonb_typeof(r->'sessionId')='string' THEN 'sessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'state')='null' OR jsonb_typeof(r->'state')='string' THEN 'state' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  session_id=excluded.session_id,
  project_id=excluded.project_id,
  state=excluded.state;

-- name: RecSessionForkGet :one
SELECT (session_fork.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'sourceSessionId',source_session_id,'projectId',project_id,'requestId',request_id)))::jsonb FROM session_fork WHERE id=$1;

-- name: RecSessionForkAll :many
SELECT (session_fork.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'sourceSessionId',source_session_id,'projectId',project_id,'requestId',request_id)))::jsonb FROM session_fork
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR session_fork.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR session_fork.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'sourceSessionId' IS NULL OR session_fork.source_session_id=sqlc.arg(filter)::jsonb->>'sourceSessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR session_fork.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'requestId' IS NULL OR session_fork.request_id=sqlc.arg(filter)::jsonb->>'requestId')
  AND (session_fork.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'sourceSessionId',source_session_id,'projectId',project_id,'requestId',request_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY session_fork.seq;

-- name: RecSessionForkPut :exec
INSERT INTO session_fork(id, session_id, source_session_id, project_id, request_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'sessionId')='string' THEN r->>'sessionId' END,
  CASE WHEN jsonb_typeof(r->'sourceSessionId')='string' THEN r->>'sourceSessionId' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  CASE WHEN jsonb_typeof(r->'requestId')='string' THEN r->>'requestId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'sessionId')='null' OR jsonb_typeof(r->'sessionId')='string' THEN 'sessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'sourceSessionId')='null' OR jsonb_typeof(r->'sourceSessionId')='string' THEN 'sourceSessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'requestId')='null' OR jsonb_typeof(r->'requestId')='string' THEN 'requestId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  session_id=excluded.session_id,
  source_session_id=excluded.source_session_id,
  project_id=excluded.project_id,
  request_id=excluded.request_id;

-- name: RecSessionTransitionGet :one
SELECT (session_transition.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id)))::jsonb FROM session_transition WHERE id=$1;

-- name: RecSessionTransitionAll :many
SELECT (session_transition.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id)))::jsonb FROM session_transition
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR session_transition.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR session_transition.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR session_transition.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (session_transition.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sessionId',session_id,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY session_transition.seq;

-- name: RecSessionTransitionPut :exec
INSERT INTO session_transition(id, session_id, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'sessionId')='string' THEN r->>'sessionId' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'sessionId')='null' OR jsonb_typeof(r->'sessionId')='string' THEN 'sessionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  session_id=excluded.session_id,
  project_id=excluded.project_id;

-- name: RecWorkerDoneGet :one
SELECT (worker_done.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'projectId',project_id)))::jsonb FROM worker_done WHERE id=$1;

-- name: RecWorkerDoneAll :many
SELECT (worker_done.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'projectId',project_id)))::jsonb FROM worker_done
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR worker_done.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR worker_done.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR worker_done.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (worker_done.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'executionId',execution_id,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY worker_done.seq;

-- name: RecWorkerDonePut :exec
INSERT INTO worker_done(id, execution_id, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'executionId')='string' THEN r->>'executionId' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'executionId')='null' OR jsonb_typeof(r->'executionId')='string' THEN 'executionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  execution_id=excluded.execution_id,
  project_id=excluded.project_id;

-- name: RecHostGet :one
SELECT (host.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'hostId',host_id,'holder',holder,'state',nullif(state,'active'),'intake',intake,'secretHash',secret_hash,'removedSecretHash',removed_secret_hash,'projectId',project_id)))::jsonb FROM host WHERE id=$1;

-- name: RecHostAll :many
SELECT (host.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'hostId',host_id,'holder',holder,'state',nullif(state,'active'),'intake',intake,'secretHash',secret_hash,'removedSecretHash',removed_secret_hash,'projectId',project_id)))::jsonb FROM host
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR host.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR host.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'holder' IS NULL OR host.holder=sqlc.arg(filter)::jsonb->>'holder')
  AND (sqlc.arg(filter)::jsonb->>'intake' IS NULL OR host.intake=sqlc.arg(filter)::jsonb->>'intake')
  AND (sqlc.arg(filter)::jsonb->>'secretHash' IS NULL OR host.secret_hash=sqlc.arg(filter)::jsonb->>'secretHash')
  AND (sqlc.arg(filter)::jsonb->>'removedSecretHash' IS NULL OR host.removed_secret_hash=sqlc.arg(filter)::jsonb->>'removedSecretHash')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR host.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (host.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'hostId',host_id,'holder',holder,'state',nullif(state,'active'),'intake',intake,'secretHash',secret_hash,'removedSecretHash',removed_secret_hash,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY host.seq;

-- name: RecHostPut :exec
INSERT INTO host(id, host_id, holder, intake, secret_hash, removed_secret_hash, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'hostId')='string' THEN r->>'hostId' END,
  CASE WHEN jsonb_typeof(r->'holder')='string' THEN r->>'holder' END,
  CASE WHEN jsonb_typeof(r->'intake')='string' THEN r->>'intake' END,
  CASE WHEN jsonb_typeof(r->'secretHash')='string' THEN r->>'secretHash' END,
  CASE WHEN jsonb_typeof(r->'removedSecretHash')='string' THEN r->>'removedSecretHash' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'hostId')='null' OR jsonb_typeof(r->'hostId')='string' THEN 'hostId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'holder')='null' OR jsonb_typeof(r->'holder')='string' THEN 'holder' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'state')='null' OR jsonb_typeof(r->'state')='string' THEN 'state' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'intake')='null' OR jsonb_typeof(r->'intake')='string' THEN 'intake' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'secretHash')='null' OR jsonb_typeof(r->'secretHash')='string' THEN 'secretHash' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'removedSecretHash')='null' OR jsonb_typeof(r->'removedSecretHash')='string' THEN 'removedSecretHash' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  host_id=excluded.host_id,
  holder=excluded.holder,
  intake=excluded.intake,
  secret_hash=excluded.secret_hash,
  removed_secret_hash=excluded.removed_secret_hash,
  project_id=excluded.project_id;

-- name: RecHostObservationGet :one
SELECT (host_observation.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'holder',holder,'lastSeenAt',(extract(epoch FROM last_seen_at)*1000)::bigint,'projectId',project_id)))::jsonb FROM host_observation WHERE id=$1;

-- name: RecHostObservationAll :many
SELECT (host_observation.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'holder',holder,'lastSeenAt',(extract(epoch FROM last_seen_at)*1000)::bigint,'projectId',project_id)))::jsonb FROM host_observation
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR host_observation.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'holder' IS NULL OR host_observation.holder=sqlc.arg(filter)::jsonb->>'holder')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR host_observation.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (host_observation.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'holder',holder,'lastSeenAt',(extract(epoch FROM last_seen_at)*1000)::bigint,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY host_observation.seq;

-- name: RecHostObservationPut :exec
INSERT INTO host_observation(id, holder, last_seen_at, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'holder')='string' THEN r->>'holder' END,
  CASE WHEN jsonb_typeof(r->'lastSeenAt')='number' THEN to_timestamp((r->>'lastSeenAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'holder')='null' OR jsonb_typeof(r->'holder')='string' THEN 'holder' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'lastSeenAt')='null' OR jsonb_typeof(r->'lastSeenAt')='number' THEN 'lastSeenAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  holder=excluded.holder,
  last_seen_at=excluded.last_seen_at,
  project_id=excluded.project_id;

-- name: RecManagedAccountGet :one
SELECT (managed_account.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'name',name,'holder',holder,'intake',intake,'harness',harness,'provider',provider,'projectId',project_id)))::jsonb FROM managed_account WHERE id=$1;

-- name: RecManagedAccountAll :many
SELECT (managed_account.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'name',name,'holder',holder,'intake',intake,'harness',harness,'provider',provider,'projectId',project_id)))::jsonb FROM managed_account
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR managed_account.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'name' IS NULL OR managed_account.name=sqlc.arg(filter)::jsonb->>'name')
  AND (sqlc.arg(filter)::jsonb->>'holder' IS NULL OR managed_account.holder=sqlc.arg(filter)::jsonb->>'holder')
  AND (sqlc.arg(filter)::jsonb->>'intake' IS NULL OR managed_account.intake=sqlc.arg(filter)::jsonb->>'intake')
  AND (sqlc.arg(filter)::jsonb->>'harness' IS NULL OR managed_account.harness=sqlc.arg(filter)::jsonb->>'harness')
  AND (sqlc.arg(filter)::jsonb->>'provider' IS NULL OR managed_account.provider=sqlc.arg(filter)::jsonb->>'provider')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR managed_account.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (managed_account.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'name',name,'holder',holder,'intake',intake,'harness',harness,'provider',provider,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY managed_account.seq;

-- name: RecManagedAccountPut :exec
INSERT INTO managed_account(id, name, holder, intake, harness, provider, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'name')='string' THEN r->>'name' END,
  CASE WHEN jsonb_typeof(r->'holder')='string' THEN r->>'holder' END,
  CASE WHEN jsonb_typeof(r->'intake')='string' THEN r->>'intake' END,
  CASE WHEN jsonb_typeof(r->'harness')='string' THEN r->>'harness' END,
  CASE WHEN jsonb_typeof(r->'provider')='string' THEN r->>'provider' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'name')='null' OR jsonb_typeof(r->'name')='string' THEN 'name' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'holder')='null' OR jsonb_typeof(r->'holder')='string' THEN 'holder' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'intake')='null' OR jsonb_typeof(r->'intake')='string' THEN 'intake' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'harness')='null' OR jsonb_typeof(r->'harness')='string' THEN 'harness' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'provider')='null' OR jsonb_typeof(r->'provider')='string' THEN 'provider' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  name=excluded.name,
  holder=excluded.holder,
  intake=excluded.intake,
  harness=excluded.harness,
  provider=excluded.provider,
  project_id=excluded.project_id;

-- name: RecAccountInspectionGet :one
SELECT (account_inspection.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'accountId',account_id,'executionId',execution_id,'hostId',host_id,'projectId',project_id)))::jsonb FROM account_inspection WHERE id=$1;

-- name: RecAccountInspectionAll :many
SELECT (account_inspection.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'accountId',account_id,'executionId',execution_id,'hostId',host_id,'projectId',project_id)))::jsonb FROM account_inspection
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR account_inspection.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'accountId' IS NULL OR account_inspection.account_id=sqlc.arg(filter)::jsonb->>'accountId')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR account_inspection.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR account_inspection.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR account_inspection.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (account_inspection.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'accountId',account_id,'executionId',execution_id,'hostId',host_id,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY account_inspection.seq;

-- name: RecAccountInspectionPut :exec
INSERT INTO account_inspection(id, account_id, execution_id, host_id, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'accountId')='string' THEN r->>'accountId' END,
  CASE WHEN jsonb_typeof(r->'executionId')='string' THEN r->>'executionId' END,
  CASE WHEN jsonb_typeof(r->'hostId')='string' THEN r->>'hostId' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'accountId')='null' OR jsonb_typeof(r->'accountId')='string' THEN 'accountId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'executionId')='null' OR jsonb_typeof(r->'executionId')='string' THEN 'executionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'hostId')='null' OR jsonb_typeof(r->'hostId')='string' THEN 'hostId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  account_id=excluded.account_id,
  execution_id=excluded.execution_id,
  host_id=excluded.host_id,
  project_id=excluded.project_id;

-- name: RecAccountCheckGet :one
SELECT (account_check.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'accountId',account_id,'hostId',host_id,'binding',binding,'projectId',project_id)))::jsonb FROM account_check WHERE id=$1;

-- name: RecAccountCheckAll :many
SELECT (account_check.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'accountId',account_id,'hostId',host_id,'binding',binding,'projectId',project_id)))::jsonb FROM account_check
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR account_check.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'accountId' IS NULL OR account_check.account_id=sqlc.arg(filter)::jsonb->>'accountId')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR account_check.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'binding' IS NULL OR account_check.binding=sqlc.arg(filter)::jsonb->>'binding')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR account_check.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (account_check.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'accountId',account_id,'hostId',host_id,'binding',binding,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY account_check.seq;

-- name: RecAccountCheckPut :exec
INSERT INTO account_check(id, account_id, host_id, binding, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'accountId')='string' THEN r->>'accountId' END,
  CASE WHEN jsonb_typeof(r->'hostId')='string' THEN r->>'hostId' END,
  CASE WHEN jsonb_typeof(r->'binding')='string' THEN r->>'binding' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'accountId')='null' OR jsonb_typeof(r->'accountId')='string' THEN 'accountId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'hostId')='null' OR jsonb_typeof(r->'hostId')='string' THEN 'hostId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'binding')='null' OR jsonb_typeof(r->'binding')='string' THEN 'binding' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  account_id=excluded.account_id,
  host_id=excluded.host_id,
  binding=excluded.binding,
  project_id=excluded.project_id;

-- name: RecAccountCatalogueGet :one
SELECT (account_catalogue.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'hostId',host_id,'harness',harness,'projectId',project_id)))::jsonb FROM account_catalogue WHERE id=$1;

-- name: RecAccountCatalogueAll :many
SELECT (account_catalogue.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'hostId',host_id,'harness',harness,'projectId',project_id)))::jsonb FROM account_catalogue
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR account_catalogue.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR account_catalogue.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'harness' IS NULL OR account_catalogue.harness=sqlc.arg(filter)::jsonb->>'harness')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR account_catalogue.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (account_catalogue.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'hostId',host_id,'harness',harness,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY account_catalogue.seq;

-- name: RecAccountCataloguePut :exec
INSERT INTO account_catalogue(id, host_id, harness, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'hostId')='string' THEN r->>'hostId' END,
  CASE WHEN jsonb_typeof(r->'harness')='string' THEN r->>'harness' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'hostId')='null' OR jsonb_typeof(r->'hostId')='string' THEN 'hostId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'harness')='null' OR jsonb_typeof(r->'harness')='string' THEN 'harness' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  host_id=excluded.host_id,
  harness=excluded.harness,
  project_id=excluded.project_id;

-- name: RecApiConnectionGet :one
SELECT (api_connection.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'name',name,'provider',provider,'holder',holder,'projectId',project_id)))::jsonb FROM api_connection WHERE id=$1;

-- name: RecApiConnectionGetLocked :one
SELECT (api_connection.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'name',name,'provider',provider,'holder',holder,'projectId',project_id)))::jsonb FROM api_connection WHERE id=$1 FOR UPDATE;

-- name: RecApiConnectionAll :many
SELECT (api_connection.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'name',name,'provider',provider,'holder',holder,'projectId',project_id)))::jsonb FROM api_connection
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR api_connection.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'name' IS NULL OR api_connection.name=sqlc.arg(filter)::jsonb->>'name')
  AND (sqlc.arg(filter)::jsonb->>'provider' IS NULL OR api_connection.provider=sqlc.arg(filter)::jsonb->>'provider')
  AND (sqlc.arg(filter)::jsonb->>'holder' IS NULL OR api_connection.holder=sqlc.arg(filter)::jsonb->>'holder')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR api_connection.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (api_connection.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'name',name,'provider',provider,'holder',holder,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY api_connection.seq;

-- name: RecApiConnectionPut :exec
INSERT INTO api_connection(id, name, provider, holder, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'name')='string' THEN r->>'name' END,
  CASE WHEN jsonb_typeof(r->'provider')='string' THEN r->>'provider' END,
  CASE WHEN jsonb_typeof(r->'holder')='string' THEN r->>'holder' END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'name')='null' OR jsonb_typeof(r->'name')='string' THEN 'name' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'provider')='null' OR jsonb_typeof(r->'provider')='string' THEN 'provider' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'holder')='null' OR jsonb_typeof(r->'holder')='string' THEN 'holder' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  name=excluded.name,
  provider=excluded.provider,
  holder=excluded.holder,
  project_id=excluded.project_id;

-- name: RecConnectionKeyGet :one
SELECT (connection_key.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'connectionId',connection_id,'fingerprint',fingerprint,'state',state,'addedAt',(extract(epoch FROM added_at)*1000)::bigint,'projectId',project_id)))::jsonb FROM connection_key WHERE id=$1;

-- name: RecConnectionKeyAll :many
SELECT (connection_key.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'connectionId',connection_id,'fingerprint',fingerprint,'state',state,'addedAt',(extract(epoch FROM added_at)*1000)::bigint,'projectId',project_id)))::jsonb FROM connection_key
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR connection_key.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'connectionId' IS NULL OR connection_key.connection_id=sqlc.arg(filter)::jsonb->>'connectionId')
  AND (sqlc.arg(filter)::jsonb->>'fingerprint' IS NULL OR connection_key.fingerprint=sqlc.arg(filter)::jsonb->>'fingerprint')
  AND (sqlc.arg(filter)::jsonb->>'state' IS NULL OR connection_key.state=sqlc.arg(filter)::jsonb->>'state')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR connection_key.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (connection_key.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'connectionId',connection_id,'fingerprint',fingerprint,'state',state,'addedAt',(extract(epoch FROM added_at)*1000)::bigint,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY connection_key.seq;

-- name: RecConnectionKeyPut :exec
INSERT INTO connection_key(id, connection_id, fingerprint, added_at, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'connectionId')='string' THEN r->>'connectionId' END,
  CASE WHEN jsonb_typeof(r->'fingerprint')='string' THEN r->>'fingerprint' END,
  CASE WHEN jsonb_typeof(r->'addedAt')='number' THEN to_timestamp((r->>'addedAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'connectionId')='null' OR jsonb_typeof(r->'connectionId')='string' THEN 'connectionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'fingerprint')='null' OR jsonb_typeof(r->'fingerprint')='string' THEN 'fingerprint' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'state')='null' OR jsonb_typeof(r->'state')='string' THEN 'state' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'addedAt')='null' OR jsonb_typeof(r->'addedAt')='number' THEN 'addedAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  connection_id=excluded.connection_id,
  fingerprint=excluded.fingerprint,
  added_at=excluded.added_at,
  project_id=excluded.project_id;

-- name: RecRunTokenGet :one
SELECT (run_token.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sha256',sha256,'executionId',execution_id,'pipelineRunId',pipeline_run_id,'personal',personal,'step',step,'directedBy',directed_by,'createdAt',(extract(epoch FROM created_at)*1000)::bigint,'revokedAt',(extract(epoch FROM revoked_at)*1000)::bigint,'directorAccessRevokedAt',(extract(epoch FROM director_access_revoked_at)*1000)::bigint,'projectId',project_id)))::jsonb FROM run_token WHERE id=$1;

-- name: RecRunTokenAll :many
SELECT (run_token.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sha256',sha256,'executionId',execution_id,'pipelineRunId',pipeline_run_id,'personal',personal,'step',step,'directedBy',directed_by,'createdAt',(extract(epoch FROM created_at)*1000)::bigint,'revokedAt',(extract(epoch FROM revoked_at)*1000)::bigint,'directorAccessRevokedAt',(extract(epoch FROM director_access_revoked_at)*1000)::bigint,'projectId',project_id)))::jsonb FROM run_token
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR run_token.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sha256' IS NULL OR run_token.sha256=sqlc.arg(filter)::jsonb->>'sha256')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR run_token.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'pipelineRunId' IS NULL OR run_token.pipeline_run_id=sqlc.arg(filter)::jsonb->>'pipelineRunId')
  AND (sqlc.arg(filter)::jsonb->>'personal' IS NULL OR run_token.personal=sqlc.arg(filter)::jsonb->>'personal')
  AND (sqlc.arg(filter)::jsonb->>'directedBy' IS NULL OR run_token.directed_by=sqlc.arg(filter)::jsonb->>'directedBy')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR run_token.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (run_token.payload||jsonb_strip_nulls(jsonb_build_object('id',id,'sha256',sha256,'executionId',execution_id,'pipelineRunId',pipeline_run_id,'personal',personal,'step',step,'directedBy',directed_by,'createdAt',(extract(epoch FROM created_at)*1000)::bigint,'revokedAt',(extract(epoch FROM revoked_at)*1000)::bigint,'directorAccessRevokedAt',(extract(epoch FROM director_access_revoked_at)*1000)::bigint,'projectId',project_id)))::jsonb @> sqlc.arg(filter)::jsonb
ORDER BY run_token.seq;

-- name: RecRunTokenPut :exec
INSERT INTO run_token(id, sha256, execution_id, pipeline_run_id, personal, step, directed_by, created_at, revoked_at, director_access_revoked_at, project_id, payload)
SELECT r->>'id',
  CASE WHEN jsonb_typeof(r->'sha256')='string' THEN r->>'sha256' END,
  CASE WHEN jsonb_typeof(r->'executionId')='string' THEN r->>'executionId' END,
  CASE WHEN jsonb_typeof(r->'pipelineRunId')='string' THEN r->>'pipelineRunId' END,
  CASE WHEN jsonb_typeof(r->'personal')='string' THEN r->>'personal' END,
  CASE WHEN jsonb_typeof(r->'step')='boolean' THEN (r->>'step')::boolean END,
  CASE WHEN jsonb_typeof(r->'directedBy')='string' THEN r->>'directedBy' END,
  CASE WHEN jsonb_typeof(r->'createdAt')='number' THEN to_timestamp((r->>'createdAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'revokedAt')='number' THEN to_timestamp((r->>'revokedAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'directorAccessRevokedAt')='number' THEN to_timestamp((r->>'directorAccessRevokedAt')::float8/1000) END,
  CASE WHEN jsonb_typeof(r->'projectId')='string' THEN r->>'projectId' END,
  r - 'id' - (CASE WHEN jsonb_typeof(r->'sha256')='null' OR jsonb_typeof(r->'sha256')='string' THEN 'sha256' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'executionId')='null' OR jsonb_typeof(r->'executionId')='string' THEN 'executionId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'pipelineRunId')='null' OR jsonb_typeof(r->'pipelineRunId')='string' THEN 'pipelineRunId' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'personal')='null' OR jsonb_typeof(r->'personal')='string' THEN 'personal' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'step')='null' OR jsonb_typeof(r->'step')='boolean' THEN 'step' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'directedBy')='null' OR jsonb_typeof(r->'directedBy')='string' THEN 'directedBy' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'createdAt')='null' OR jsonb_typeof(r->'createdAt')='number' THEN 'createdAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'revokedAt')='null' OR jsonb_typeof(r->'revokedAt')='number' THEN 'revokedAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'directorAccessRevokedAt')='null' OR jsonb_typeof(r->'directorAccessRevokedAt')='number' THEN 'directorAccessRevokedAt' ELSE '' END) - (CASE WHEN jsonb_typeof(r->'projectId')='null' OR jsonb_typeof(r->'projectId')='string' THEN 'projectId' ELSE '' END)
FROM (SELECT sqlc.arg(record)::jsonb r) x
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload,
  updated_at=now(),
  sha256=excluded.sha256,
  execution_id=excluded.execution_id,
  pipeline_run_id=excluded.pipeline_run_id,
  personal=excluded.personal,
  step=excluded.step,
  directed_by=excluded.directed_by,
  created_at=excluded.created_at,
  revoked_at=excluded.revoked_at,
  director_access_revoked_at=excluded.director_access_revoked_at,
  project_id=excluded.project_id;
