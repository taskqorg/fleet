-- Records of the conversation kinds (generated once from the former convTables, stage 8e): each kind's row as its
-- record (the payload under the columns, a column absent when NULL), its rows whose record holds a filter's fields (a
-- column compared as a column, the other fields contained in payload) and its write: every column from its field when
-- the field has the column's type (absent or another type: NULL, the value kept in payload), payload the other fields.

-- name: RecSessionGet :one
SELECT (session.payload||jsonb_strip_nulls(jsonb_build_object('id',session.id,'projectId',session.project_id,'taskId',session.task_id,'hostId',session.host_id,'actor',session.actor,'directedBy',session.directed_by,'directorAccessRevokedAt',(extract(epoch FROM session.director_access_revoked_at)*1000)::bigint,'parentExecutionId',session.parent_execution_id,'createdByExecutionId',session.created_by_execution_id,'createdBySessionId',session.created_by_session_id,'sourceSessionId',session.source_session_id,'pipelineRunId',session.pipeline_run_id,'createdAt',(extract(epoch FROM session.created_at)*1000)::bigint,'archivedAt',(extract(epoch FROM session.archived_at)*1000)::bigint,'title',session.title,'titleBy',session.title_by,'titleCall',session.title_call,'titleAttemptAt',(extract(epoch FROM session.title_attempt_at)*1000)::bigint,'titleAttempts',nullif(session.title_attempts,0),'titleUnknownCall',session.title_unknown_call)))::jsonb FROM session WHERE id=$1;

-- name: RecSessionGetLocked :one
SELECT (session.payload||jsonb_strip_nulls(jsonb_build_object('id',session.id,'projectId',session.project_id,'taskId',session.task_id,'hostId',session.host_id,'actor',session.actor,'directedBy',session.directed_by,'directorAccessRevokedAt',(extract(epoch FROM session.director_access_revoked_at)*1000)::bigint,'parentExecutionId',session.parent_execution_id,'createdByExecutionId',session.created_by_execution_id,'createdBySessionId',session.created_by_session_id,'sourceSessionId',session.source_session_id,'pipelineRunId',session.pipeline_run_id,'createdAt',(extract(epoch FROM session.created_at)*1000)::bigint,'archivedAt',(extract(epoch FROM session.archived_at)*1000)::bigint,'title',session.title,'titleBy',session.title_by,'titleCall',session.title_call,'titleAttemptAt',(extract(epoch FROM session.title_attempt_at)*1000)::bigint,'titleAttempts',nullif(session.title_attempts,0),'titleUnknownCall',session.title_unknown_call)))::jsonb FROM session WHERE id=$1 FOR UPDATE;

-- name: RecSessionAll :many
SELECT (session.payload||jsonb_strip_nulls(jsonb_build_object('id',session.id,'projectId',session.project_id,'taskId',session.task_id,'hostId',session.host_id,'actor',session.actor,'directedBy',session.directed_by,'directorAccessRevokedAt',(extract(epoch FROM session.director_access_revoked_at)*1000)::bigint,'parentExecutionId',session.parent_execution_id,'createdByExecutionId',session.created_by_execution_id,'createdBySessionId',session.created_by_session_id,'sourceSessionId',session.source_session_id,'pipelineRunId',session.pipeline_run_id,'createdAt',(extract(epoch FROM session.created_at)*1000)::bigint,'archivedAt',(extract(epoch FROM session.archived_at)*1000)::bigint,'title',session.title,'titleBy',session.title_by,'titleCall',session.title_call,'titleAttemptAt',(extract(epoch FROM session.title_attempt_at)*1000)::bigint,'titleAttempts',nullif(session.title_attempts,0),'titleUnknownCall',session.title_unknown_call)))::jsonb FROM session
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR session.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR session.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'taskId' IS NULL OR session.task_id=sqlc.arg(filter)::jsonb->>'taskId')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR session.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'actor' IS NULL OR session.actor=sqlc.arg(filter)::jsonb->>'actor')
  AND (sqlc.arg(filter)::jsonb->>'directedBy' IS NULL OR session.directed_by=sqlc.arg(filter)::jsonb->>'directedBy')
  AND (sqlc.arg(filter)::jsonb->'directorAccessRevokedAt' IS NULL OR to_jsonb((extract(epoch FROM session.director_access_revoked_at)*1000)::bigint)=sqlc.arg(filter)::jsonb->'directorAccessRevokedAt')
  AND (sqlc.arg(filter)::jsonb->>'parentExecutionId' IS NULL OR session.parent_execution_id=sqlc.arg(filter)::jsonb->>'parentExecutionId')
  AND (sqlc.arg(filter)::jsonb->>'createdByExecutionId' IS NULL OR session.created_by_execution_id=sqlc.arg(filter)::jsonb->>'createdByExecutionId')
  AND (sqlc.arg(filter)::jsonb->>'createdBySessionId' IS NULL OR session.created_by_session_id=sqlc.arg(filter)::jsonb->>'createdBySessionId')
  AND (sqlc.arg(filter)::jsonb->>'sourceSessionId' IS NULL OR session.source_session_id=sqlc.arg(filter)::jsonb->>'sourceSessionId')
  AND (sqlc.arg(filter)::jsonb->>'pipelineRunId' IS NULL OR session.pipeline_run_id=sqlc.arg(filter)::jsonb->>'pipelineRunId')
  AND (sqlc.arg(filter)::jsonb->'createdAt' IS NULL OR to_jsonb((extract(epoch FROM session.created_at)*1000)::bigint)=sqlc.arg(filter)::jsonb->'createdAt')
  AND (sqlc.arg(filter)::jsonb->'archivedAt' IS NULL OR to_jsonb((extract(epoch FROM session.archived_at)*1000)::bigint)=sqlc.arg(filter)::jsonb->'archivedAt')
  AND (sqlc.arg(filter)::jsonb->>'title' IS NULL OR session.title=sqlc.arg(filter)::jsonb->>'title')
  AND (sqlc.arg(filter)::jsonb->>'titleBy' IS NULL OR session.title_by=sqlc.arg(filter)::jsonb->>'titleBy')
  AND (sqlc.arg(filter)::jsonb->>'titleCall' IS NULL OR session.title_call=sqlc.arg(filter)::jsonb->>'titleCall')
  AND (sqlc.arg(filter)::jsonb->'titleAttemptAt' IS NULL OR to_jsonb((extract(epoch FROM session.title_attempt_at)*1000)::bigint)=sqlc.arg(filter)::jsonb->'titleAttemptAt')
  AND (sqlc.arg(filter)::jsonb->'titleAttempts' IS NULL OR to_jsonb(nullif(session.title_attempts,0))=sqlc.arg(filter)::jsonb->'titleAttempts')
  AND (sqlc.arg(filter)::jsonb->>'titleUnknownCall' IS NULL OR session.title_unknown_call=sqlc.arg(filter)::jsonb->>'titleUnknownCall')
  AND session.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'projectId' - 'taskId' - 'hostId' - 'actor' - 'directedBy' - 'directorAccessRevokedAt' - 'parentExecutionId' - 'createdByExecutionId' - 'createdBySessionId' - 'sourceSessionId' - 'pipelineRunId' - 'createdAt' - 'archivedAt' - 'title' - 'titleBy' - 'titleCall' - 'titleAttemptAt' - 'titleAttempts' - 'titleUnknownCall')
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
SELECT (message.payload||jsonb_strip_nulls(jsonb_build_object('id',message.id,'sessionId',message.session_id,'executionId',message.execution_id,'projectId',message.project_id,'requestId',message.request_id,'text',message.text,'sentBy',message.sent_by,'source',message.source)))::jsonb FROM message WHERE id=$1;

-- name: RecMessageAll :many
SELECT (message.payload||jsonb_strip_nulls(jsonb_build_object('id',message.id,'sessionId',message.session_id,'executionId',message.execution_id,'projectId',message.project_id,'requestId',message.request_id,'text',message.text,'sentBy',message.sent_by,'source',message.source)))::jsonb FROM message
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR message.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR message.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR message.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR message.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'requestId' IS NULL OR message.request_id=sqlc.arg(filter)::jsonb->>'requestId')
  AND (sqlc.arg(filter)::jsonb->>'text' IS NULL OR message.text=sqlc.arg(filter)::jsonb->>'text')
  AND (sqlc.arg(filter)::jsonb->>'sentBy' IS NULL OR message.sent_by=sqlc.arg(filter)::jsonb->>'sentBy')
  AND (sqlc.arg(filter)::jsonb->>'source' IS NULL OR message.source=sqlc.arg(filter)::jsonb->>'source')
  AND message.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'sessionId' - 'executionId' - 'projectId' - 'requestId' - 'text' - 'sentBy' - 'source')
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
SELECT (event.payload||jsonb_strip_nulls(jsonb_build_object('id',event.id,'executionId',event.execution_id,'sessionId',event.session_id,'projectId',event.project_id,'seq',event.event_seq,'type',event.type,'timestamp',(extract(epoch FROM event.at)*1000)::bigint)))::jsonb FROM event WHERE id=$1 AND NOT event.delta;

-- name: RecEventAll :many
SELECT (event.payload||jsonb_strip_nulls(jsonb_build_object('id',event.id,'executionId',event.execution_id,'sessionId',event.session_id,'projectId',event.project_id,'seq',event.event_seq,'type',event.type,'timestamp',(extract(epoch FROM event.at)*1000)::bigint)))::jsonb FROM event
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR event.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR event.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR event.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR event.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->'seq' IS NULL OR to_jsonb(event.event_seq)=sqlc.arg(filter)::jsonb->'seq')
  AND (sqlc.arg(filter)::jsonb->>'type' IS NULL OR event.type=sqlc.arg(filter)::jsonb->>'type')
  AND (sqlc.arg(filter)::jsonb->'timestamp' IS NULL OR to_jsonb((extract(epoch FROM event.at)*1000)::bigint)=sqlc.arg(filter)::jsonb->'timestamp')
  AND event.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'executionId' - 'sessionId' - 'projectId' - 'seq' - 'type' - 'timestamp') AND NOT event.delta
ORDER BY event.seq;

-- name: RecEventAnyAll :many
SELECT (event.payload||jsonb_strip_nulls(jsonb_build_object('id',event.id,'executionId',event.execution_id,'sessionId',event.session_id,'projectId',event.project_id,'seq',event.event_seq,'type',event.type,'timestamp',(extract(epoch FROM event.at)*1000)::bigint)))::jsonb FROM event
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR event.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR event.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR event.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR event.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->'seq' IS NULL OR to_jsonb(event.event_seq)=sqlc.arg(filter)::jsonb->'seq')
  AND (sqlc.arg(filter)::jsonb->>'type' IS NULL OR event.type=sqlc.arg(filter)::jsonb->>'type')
  AND (sqlc.arg(filter)::jsonb->'timestamp' IS NULL OR to_jsonb((extract(epoch FROM event.at)*1000)::bigint)=sqlc.arg(filter)::jsonb->'timestamp')
  AND event.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'executionId' - 'sessionId' - 'projectId' - 'seq' - 'type' - 'timestamp')
ORDER BY event.seq;

-- name: RecQueuedMessageGet :one
SELECT (queued_message.payload||jsonb_strip_nulls(jsonb_build_object('id',queued_message.id,'sessionId',queued_message.session_id,'projectId',queued_message.project_id,'taskId',queued_message.task_id,'requestId',queued_message.request_id,'text',queued_message.text,'state',queued_message.state,'executionId',queued_message.execution_id,'steerCommandId',queued_message.steer_command_id,'sentBy',queued_message.sent_by,'source',queued_message.source,'createdAt',(extract(epoch FROM queued_message.created_at)*1000)::bigint)))::jsonb FROM queued_message WHERE id=$1;

-- name: RecQueuedMessageAll :many
SELECT (queued_message.payload||jsonb_strip_nulls(jsonb_build_object('id',queued_message.id,'sessionId',queued_message.session_id,'projectId',queued_message.project_id,'taskId',queued_message.task_id,'requestId',queued_message.request_id,'text',queued_message.text,'state',queued_message.state,'executionId',queued_message.execution_id,'steerCommandId',queued_message.steer_command_id,'sentBy',queued_message.sent_by,'source',queued_message.source,'createdAt',(extract(epoch FROM queued_message.created_at)*1000)::bigint)))::jsonb FROM queued_message
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
  AND (sqlc.arg(filter)::jsonb->'createdAt' IS NULL OR to_jsonb((extract(epoch FROM queued_message.created_at)*1000)::bigint)=sqlc.arg(filter)::jsonb->'createdAt')
  AND queued_message.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'sessionId' - 'projectId' - 'taskId' - 'requestId' - 'text' - 'state' - 'executionId' - 'steerCommandId' - 'sentBy' - 'source' - 'createdAt')
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
SELECT (command.payload||jsonb_strip_nulls(jsonb_build_object('id',command.id,'executionId',command.execution_id,'sessionId',command.session_id,'hostId',command.host_id,'projectId',command.project_id,'kind',command.kind,'status',command.status::text,'stopDetail',command.stop_detail,'handedOut',command.handed_out,'traceparent',command.traceparent)))::jsonb FROM command WHERE id=$1;

-- name: RecCommandAll :many
SELECT (command.payload||jsonb_strip_nulls(jsonb_build_object('id',command.id,'executionId',command.execution_id,'sessionId',command.session_id,'hostId',command.host_id,'projectId',command.project_id,'kind',command.kind,'status',command.status::text,'stopDetail',command.stop_detail,'handedOut',command.handed_out,'traceparent',command.traceparent)))::jsonb FROM command
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR command.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR command.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR command.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'hostId' IS NULL OR command.host_id=sqlc.arg(filter)::jsonb->>'hostId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR command.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'kind' IS NULL OR command.kind=sqlc.arg(filter)::jsonb->>'kind')
  AND (sqlc.arg(filter)::jsonb->>'status' IS NULL OR command.status=(sqlc.arg(filter)::jsonb->>'status')::command_status)
  AND (sqlc.arg(filter)::jsonb->>'stopDetail' IS NULL OR command.stop_detail=sqlc.arg(filter)::jsonb->>'stopDetail')
  AND (sqlc.arg(filter)::jsonb->'handedOut' IS NULL OR to_jsonb(command.handed_out)=sqlc.arg(filter)::jsonb->'handedOut')
  AND (sqlc.arg(filter)::jsonb->>'traceparent' IS NULL OR command.traceparent=sqlc.arg(filter)::jsonb->>'traceparent')
  AND command.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'executionId' - 'sessionId' - 'hostId' - 'projectId' - 'kind' - 'status' - 'stopDetail' - 'handedOut' - 'traceparent')
ORDER BY command.seq;

-- name: RecHandoffGet :one
SELECT (handoff.payload||jsonb_strip_nulls(jsonb_build_object('id',handoff.id,'sessionId',handoff.session_id,'projectId',handoff.project_id,'state',handoff.state)))::jsonb FROM handoff WHERE id=$1;

-- name: RecHandoffAll :many
SELECT (handoff.payload||jsonb_strip_nulls(jsonb_build_object('id',handoff.id,'sessionId',handoff.session_id,'projectId',handoff.project_id,'state',handoff.state)))::jsonb FROM handoff
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR handoff.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR handoff.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR handoff.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'state' IS NULL OR handoff.state=sqlc.arg(filter)::jsonb->>'state')
  AND handoff.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'sessionId' - 'projectId' - 'state')
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
SELECT (session_fork.payload||jsonb_strip_nulls(jsonb_build_object('id',session_fork.id,'sessionId',session_fork.session_id,'sourceSessionId',session_fork.source_session_id,'projectId',session_fork.project_id,'requestId',session_fork.request_id)))::jsonb FROM session_fork WHERE id=$1;

-- name: RecSessionForkAll :many
SELECT (session_fork.payload||jsonb_strip_nulls(jsonb_build_object('id',session_fork.id,'sessionId',session_fork.session_id,'sourceSessionId',session_fork.source_session_id,'projectId',session_fork.project_id,'requestId',session_fork.request_id)))::jsonb FROM session_fork
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR session_fork.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR session_fork.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'sourceSessionId' IS NULL OR session_fork.source_session_id=sqlc.arg(filter)::jsonb->>'sourceSessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR session_fork.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND (sqlc.arg(filter)::jsonb->>'requestId' IS NULL OR session_fork.request_id=sqlc.arg(filter)::jsonb->>'requestId')
  AND session_fork.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'sessionId' - 'sourceSessionId' - 'projectId' - 'requestId')
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
SELECT (session_transition.payload||jsonb_strip_nulls(jsonb_build_object('id',session_transition.id,'sessionId',session_transition.session_id,'projectId',session_transition.project_id)))::jsonb FROM session_transition WHERE id=$1;

-- name: RecSessionTransitionAll :many
SELECT (session_transition.payload||jsonb_strip_nulls(jsonb_build_object('id',session_transition.id,'sessionId',session_transition.session_id,'projectId',session_transition.project_id)))::jsonb FROM session_transition
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR session_transition.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'sessionId' IS NULL OR session_transition.session_id=sqlc.arg(filter)::jsonb->>'sessionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR session_transition.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND session_transition.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'sessionId' - 'projectId')
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
SELECT (worker_done.payload||jsonb_strip_nulls(jsonb_build_object('id',worker_done.id,'executionId',worker_done.execution_id,'projectId',worker_done.project_id)))::jsonb FROM worker_done WHERE id=$1;

-- name: RecWorkerDoneAll :many
SELECT (worker_done.payload||jsonb_strip_nulls(jsonb_build_object('id',worker_done.id,'executionId',worker_done.execution_id,'projectId',worker_done.project_id)))::jsonb FROM worker_done
WHERE (sqlc.arg(filter)::jsonb->>'id' IS NULL OR worker_done.id=sqlc.arg(filter)::jsonb->>'id')
  AND (sqlc.arg(filter)::jsonb->>'executionId' IS NULL OR worker_done.execution_id=sqlc.arg(filter)::jsonb->>'executionId')
  AND (sqlc.arg(filter)::jsonb->>'projectId' IS NULL OR worker_done.project_id=sqlc.arg(filter)::jsonb->>'projectId')
  AND worker_done.payload @> (sqlc.arg(filter)::jsonb - 'id' - 'executionId' - 'projectId')
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
