-- Conversation threads (threads.go, docs/designs/fleet-conversations.md): a thread is the agent runs of a task (or of a
-- session without a task) read as rows of messages, events, open streams, questions and execution starts by seq.
-- A message or event row is one JSON object (threadRow: id, executionId, type, text, blocks, deltaOf, closes, at, sentBy,
-- attachments); a question row is the question without its data.

-- name: ThreadMembersOfTask :many
-- A task's agent runs, plus the taskless runs of sessions whose first (or attached) task it is.
WITH attached AS (SELECT s.id AS session_id FROM session s WHERE s.task_id= @task_id::text),
 first_task AS (SELECT DISTINCT ON (x.session_id) x.session_id, x.task_id FROM execution x WHERE x.task_id IS NOT NULL
  AND x.session_id IN (SELECT y.session_id FROM execution y WHERE y.task_id= @task_id::text) ORDER BY x.session_id, x.seq)
SELECT e.id, e.session_id, e.harness, e.model, to_jsonb(e.config_ref->>'effort')::jsonb AS effort, e.state::text AS state, e.observation, e.project_id, e.started_at, e.ended_at, e.seq
FROM execution e WHERE e.kind='agent_run' AND (e.task_id= @task_id::text OR (e.task_id IS NULL AND (e.session_id IN (SELECT f.session_id FROM first_task f WHERE f.task_id= @task_id::text)
 OR e.session_id IN (SELECT a.session_id FROM attached a)))) ORDER BY e.seq;

-- name: ThreadMembersOfSession :many
SELECT e.id, e.session_id, e.harness, e.model, to_jsonb(e.config_ref->>'effort')::jsonb AS effort, e.state::text AS state, e.observation, e.project_id, e.started_at, e.ended_at, e.seq
FROM execution e WHERE e.kind='agent_run' AND e.session_id=$1 ORDER BY e.seq;

-- name: ThreadMembersOfSessionThread :many
-- A session's agent runs in one work-item thread ('' the main thread), over execution_thread (migration 109).
SELECT e.id, e.session_id, e.harness, e.model, to_jsonb(e.config_ref->>'effort')::jsonb AS effort, e.state::text AS state, e.observation, e.project_id, e.started_at, e.ended_at, e.seq
FROM execution e WHERE e.session_id= @session_id::text AND e.thread= @thread::text AND e.kind='agent_run' ORDER BY e.seq;
-- name: ThreadMemberOf :many
-- One execution as a thread member (its reply text), of any kind.
SELECT e.id, e.session_id, e.harness, e.model, to_jsonb(e.config_ref->>'effort')::jsonb AS effort, e.state::text AS state, e.observation, e.project_id, e.started_at, e.ended_at, e.seq
FROM execution e WHERE e.id=$1;

-- name: ThreadComposerSessions :many
-- Visible sessions still associated with the thread, including attached sessions without an execution.
SELECT s.id FROM session s
WHERE s.project_id IS NOT DISTINCT FROM sqlc.narg(project_id)::text
  AND s.archived_at IS NULL
  AND (s.task_id = @task_id::text OR (s.task_id IS NULL AND s.id = ANY(@session_ids::text[])))
ORDER BY s.created_at DESC NULLS LAST, s.id DESC;

-- name: ThreadSessionExists :one
SELECT EXISTS (SELECT 1 FROM session WHERE id=$1);

-- name: ThreadTaskExists :one
SELECT EXISTS (SELECT 1 FROM task WHERE id=$1);

-- name: ThreadSessionTask :many
-- The task a session is attached to (none when the session is missing or has none).
SELECT task_id FROM session WHERE id=$1 AND task_id IS NOT NULL;

-- name: ThreadSessionFirstTask :many
-- The task of a session's first run that has one.
SELECT task_id FROM execution WHERE session_id=$1 AND task_id IS NOT NULL ORDER BY seq LIMIT 1;

-- name: ThreadRowsAfter :many
-- The rows of executions @ids from seq @from_seq up, nearest first, at most @lim per kind and execution and @lim in all.
SELECT x.seq, x.src, x.v FROM (
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT mm.seq, 'message'::text AS src, jsonb_build_object('id',mm.id,'executionId',mm.execution_id,'type',NULL,'text',to_jsonb(mm.text),'blocks',NULL,'deltaOf',NULL,'closes',NULL,'at',mm.updated_at,'sentBy',mm.sent_by,'attachments',mm.payload->'attachments') AS v
  FROM message mm WHERE mm.execution_id=m.id AND mm.seq >= @from_seq::bigint ORDER BY mm.seq ASC LIMIT @lim::int) d
 UNION ALL
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT ev.seq, 'event'::text AS src, jsonb_build_object('id',ev.id,'executionId',ev.execution_id,'type',ev.type,'text',ev.payload->'text','blocks',ev.payload->'data'->'fleetNormalized'->'blocks','deltaOf',ev.payload->'data'->'fleetNormalized'->'deltaOf','closes',ev.payload->'data'->'fleetNormalized'->'closes','at',ev.updated_at,'sentBy',ev.payload->>'sentBy','attachments',ev.payload->'attachments') AS v
  FROM event ev WHERE ev.execution_id=m.id AND ev.seq >= @from_seq::bigint AND NOT ev.delta
   AND (ev.type='execution.ended' OR jsonb_array_length(coalesce(ev.payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb))>0)
  ORDER BY ev.seq ASC LIMIT @lim::int) d
 UNION ALL
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT ev.seq, 'event-delta'::text AS src, jsonb_build_object('id',ev.id,'executionId',ev.execution_id,'type',ev.type,'text',ev.payload->'text','blocks',ev.payload->'data'->'fleetNormalized'->'blocks','deltaOf',ev.payload->'data'->'fleetNormalized'->'deltaOf','closes',ev.payload->'data'->'fleetNormalized'->'closes','at',ev.updated_at,'sentBy',ev.payload->>'sentBy','attachments',ev.payload->'attachments') AS v
  FROM event ev WHERE ev.execution_id=m.id AND ev.seq >= @from_seq::bigint AND ev.delta AND ev.payload->'data'->'fleetNormalized'->>'opens'='true'
  ORDER BY ev.seq ASC LIMIT @lim::int) d
 UNION ALL
 SELECT q.seq, 'question'::text, to_jsonb(q)-'data' FROM question q WHERE q.execution_id = ANY (@ids::text[]) AND q.seq >= @from_seq::bigint
 UNION ALL
 SELECT e.seq, 'execution'::text, jsonb_build_object('id',e.id) FROM execution e WHERE e.id = ANY (@ids::text[]) AND e.seq >= @from_seq::bigint
) x ORDER BY x.seq ASC LIMIT @lim::int;

-- name: ThreadRowsBefore :many
-- The rows of executions @ids from seq @from_seq down, nearest first, at most @lim per kind and execution and @lim in all.
SELECT x.seq, x.src, x.v FROM (
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT mm.seq, 'message'::text AS src, jsonb_build_object('id',mm.id,'executionId',mm.execution_id,'type',NULL,'text',to_jsonb(mm.text),'blocks',NULL,'deltaOf',NULL,'closes',NULL,'at',mm.updated_at,'sentBy',mm.sent_by,'attachments',mm.payload->'attachments') AS v
  FROM message mm WHERE mm.execution_id=m.id AND mm.seq <= @from_seq::bigint ORDER BY mm.seq DESC LIMIT @lim::int) d
 UNION ALL
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT ev.seq, 'event'::text AS src, jsonb_build_object('id',ev.id,'executionId',ev.execution_id,'type',ev.type,'text',ev.payload->'text','blocks',ev.payload->'data'->'fleetNormalized'->'blocks','deltaOf',ev.payload->'data'->'fleetNormalized'->'deltaOf','closes',ev.payload->'data'->'fleetNormalized'->'closes','at',ev.updated_at,'sentBy',ev.payload->>'sentBy','attachments',ev.payload->'attachments') AS v
  FROM event ev WHERE ev.execution_id=m.id AND ev.seq <= @from_seq::bigint AND NOT ev.delta
   AND (ev.type='execution.ended' OR jsonb_array_length(coalesce(ev.payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb))>0)
  ORDER BY ev.seq DESC LIMIT @lim::int) d
 UNION ALL
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT ev.seq, 'event-delta'::text AS src, jsonb_build_object('id',ev.id,'executionId',ev.execution_id,'type',ev.type,'text',ev.payload->'text','blocks',ev.payload->'data'->'fleetNormalized'->'blocks','deltaOf',ev.payload->'data'->'fleetNormalized'->'deltaOf','closes',ev.payload->'data'->'fleetNormalized'->'closes','at',ev.updated_at,'sentBy',ev.payload->>'sentBy','attachments',ev.payload->'attachments') AS v
  FROM event ev WHERE ev.execution_id=m.id AND ev.seq <= @from_seq::bigint AND ev.delta AND ev.payload->'data'->'fleetNormalized'->>'opens'='true'
  ORDER BY ev.seq DESC LIMIT @lim::int) d
 UNION ALL
 SELECT q.seq, 'question'::text, to_jsonb(q)-'data' FROM question q WHERE q.execution_id = ANY (@ids::text[]) AND q.seq <= @from_seq::bigint
 UNION ALL
 SELECT e.seq, 'execution'::text, jsonb_build_object('id',e.id) FROM execution e WHERE e.id = ANY (@ids::text[]) AND e.seq <= @from_seq::bigint
) x ORDER BY x.seq DESC LIMIT @lim::int;

-- name: ThreadRowsSearch :many
-- ThreadRowsBefore with only the messages and events whose text matches the ILIKE pattern @pattern (thread_search): a
-- message by its text, an event by its blocks' text, names and tool input and output.
SELECT x.seq, x.src, x.v FROM (
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT mm.seq, 'message'::text AS src, jsonb_build_object('id',mm.id,'executionId',mm.execution_id,'type',NULL,'text',to_jsonb(mm.text),'blocks',NULL,'deltaOf',NULL,'closes',NULL,'at',mm.updated_at,'sentBy',mm.sent_by,'attachments',mm.payload->'attachments') AS v
  FROM message mm WHERE mm.execution_id=m.id AND mm.seq <= @from_seq::bigint AND mm.text ILIKE @pattern::text ORDER BY mm.seq DESC LIMIT @lim::int) d
 UNION ALL
 SELECT d.seq, d.src, d.v FROM unnest(@ids::text[]) AS m(id), LATERAL (
  SELECT ev.seq, 'event'::text AS src, jsonb_build_object('id',ev.id,'executionId',ev.execution_id,'type',ev.type,'text',ev.payload->'text','blocks',ev.payload->'data'->'fleetNormalized'->'blocks','deltaOf',ev.payload->'data'->'fleetNormalized'->'deltaOf','closes',ev.payload->'data'->'fleetNormalized'->'closes','at',ev.updated_at,'sentBy',ev.payload->>'sentBy','attachments',ev.payload->'attachments') AS v
  FROM event ev WHERE ev.execution_id=m.id AND ev.seq <= @from_seq::bigint AND NOT ev.delta
   AND (ev.type='execution.ended' OR jsonb_array_length(coalesce(ev.payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb))>0)
  AND (SELECT string_agg(concat_ws(' ',b->>'markdown',b->>'name',(b->'input')::text,(b->'output')::text),' ')
   FROM jsonb_array_elements(coalesce(ev.payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb)) b) ILIKE @pattern::text
  ORDER BY ev.seq DESC LIMIT @lim::int) d

) x ORDER BY x.seq DESC LIMIT @lim::int;

-- name: ThreadStreamClosed :one
-- An event of the execution closes stream @delta_of.
SELECT EXISTS (SELECT 1 FROM event WHERE NOT delta AND execution_id= @execution_id::text AND payload->'data'->'fleetNormalized'->'closes' ? @delta_of::text);

-- name: ThreadStreamBlocks :many
-- The blocks of each delta of stream @delta_of, in order.
SELECT (payload->'data'->'fleetNormalized'->'blocks')::jsonb AS blocks FROM event
WHERE delta AND execution_id= @execution_id::text AND payload->'data'->'fleetNormalized'->>'deltaOf'= @delta_of::text ORDER BY seq;

-- name: ThreadTotal :one
-- The blocks of executions @ids, without the execution starts and open streams: messages, event blocks, questions and
-- the result block of each ended execution with stored artifacts or a linked publishing observation.
SELECT ((SELECT count(*) FROM message WHERE execution_id = ANY (@ids::text[]))
 +(SELECT coalesce(sum(jsonb_array_length(coalesce(payload->'data'->'fleetNormalized'->'blocks','[]'::jsonb))),0) FROM event WHERE NOT delta AND execution_id = ANY (@ids::text[]))
 +(SELECT count(*) FROM question WHERE execution_id = ANY (@ids::text[]))
 +(SELECT count(*) FROM event v WHERE NOT v.delta AND v.type='execution.ended' AND v.execution_id = ANY (@ids::text[])
   AND (EXISTS (SELECT 1 FROM artifact a WHERE a.execution_id=v.execution_id AND a.state='stored')
     OR EXISTS (SELECT 1 FROM execution e JOIN audit_log p ON p.project_id=e.project_id
       WHERE e.id=v.execution_id AND split_part(p.target, ':', 1)='publishing'
         AND p.value->>'sourceExecutionId'=e.id
         AND jsonb_typeof(p.value->'number')='number' AND p.value->'number' > '0'::jsonb
         AND coalesce(p.value->>'error','')=''))))::bigint AS n;

-- name: ThreadOpenStreams :many
-- The streams executions @ids opened (their first delta).
SELECT execution_id::text AS execution_id, (payload->'data'->'fleetNormalized'->'deltaOf')::jsonb AS delta_of FROM event
WHERE delta AND execution_id = ANY (@ids::text[]) AND payload->'data'->'fleetNormalized'->>'opens'='true';

-- name: ThreadStoredArtifacts :many
-- An execution's results: its stored files but previews and diagnostic files; preview_id is the newest stored preview
-- of each (its thumbnail, else a video's poster), the picture the thread shows inline.
SELECT coalesce(a.legacy_id,a.id)::text AS id, a.name, a.media_type, a.size,
 coalesce((SELECT p.id FROM artifact p WHERE p.execution_id=a.execution_id AND p.derived_from_artifact_id=a.id AND p.state='stored' AND p.role IN ('thumbnail','poster')
  ORDER BY p.role='thumbnail' DESC, p.seq DESC LIMIT 1),'')::text AS preview_id
FROM artifact a WHERE a.execution_id=$1 AND a.state='stored' AND a.role NOT IN ('thumbnail','poster','diagnostic') ORDER BY a.seq;

-- name: ThreadQuestion :many
-- Question @id when it belongs to one of executions @ids, as a thread row.
SELECT seq, (to_jsonb(q)-'data')::jsonb AS v FROM question q WHERE id= @id::text AND execution_id = ANY (@ids::text[]);

-- name: ThreadQuestionsSearch :many
-- The questions of executions @ids up to seq @from_seq whose text or answer matches @pattern, newest first.
SELECT seq, (to_jsonb(q)-'data')::jsonb AS v FROM question q WHERE execution_id = ANY (@ids::text[]) AND seq <= @from_seq::bigint
 AND (text ILIKE @pattern::text OR coalesce(answer,'') ILIKE @pattern::text) ORDER BY seq DESC LIMIT @lim::int;

-- name: ThreadArtifactExecution :many
SELECT execution_id FROM artifact WHERE id=$1 OR legacy_id=$1 LIMIT 1;

-- name: ThreadEventRows :many
-- Event @id (not a delta) as a thread row.
SELECT seq, CASE WHEN delta THEN 'event-delta' ELSE 'event' END::text AS src,
 jsonb_build_object('id',id,'executionId',execution_id,'type',type,'text',payload->'text','blocks',payload->'data'->'fleetNormalized'->'blocks','deltaOf',payload->'data'->'fleetNormalized'->'deltaOf','closes',payload->'data'->'fleetNormalized'->'closes','at',updated_at,'sentBy',payload->>'sentBy','attachments',payload->'attachments') AS v
FROM event WHERE id= @id::text AND NOT delta;

-- name: ThreadRowsByID :many
-- Event or message @id of executions @ids as thread rows.
SELECT seq, CASE WHEN delta THEN 'event-delta' ELSE 'event' END::text AS src,
 jsonb_build_object('id',id,'executionId',execution_id,'type',type,'text',payload->'text','blocks',payload->'data'->'fleetNormalized'->'blocks','deltaOf',payload->'data'->'fleetNormalized'->'deltaOf','closes',payload->'data'->'fleetNormalized'->'closes','at',updated_at,'sentBy',payload->>'sentBy','attachments',payload->'attachments') AS v
FROM event WHERE id= @id::text AND execution_id = ANY (@ids::text[])
UNION ALL
SELECT seq, 'message'::text AS src,
 jsonb_build_object('id',id,'executionId',execution_id,'type',NULL,'text',to_jsonb(text),'blocks',NULL,'deltaOf',NULL,'closes',NULL,'at',updated_at,'sentBy',sent_by,'attachments',payload->'attachments') AS v
FROM message WHERE id= @id::text AND execution_id = ANY (@ids::text[]);

-- name: ThreadStreamOpenRows :many
-- The delta that opened stream @delta_of of an execution, as a thread row.
SELECT seq, 'event-delta'::text AS src,
 jsonb_build_object('id',id,'executionId',execution_id,'type',type,'text',payload->'text','blocks',payload->'data'->'fleetNormalized'->'blocks','deltaOf',payload->'data'->'fleetNormalized'->'deltaOf','closes',payload->'data'->'fleetNormalized'->'closes','at',updated_at,'sentBy',payload->>'sentBy','attachments',payload->'attachments') AS v
FROM event WHERE delta AND execution_id= @execution_id::text AND payload->'data'->'fleetNormalized'->>'deltaOf'= @delta_of::text AND payload->'data'->'fleetNormalized'->>'opens'='true';

-- name: ThreadPublishingResults :many
-- The newest observed request linked to each execution, refreshed from observations of that exact request.
-- A live read restricts the result to requests affected by the supplied publishing audit rows.
WITH observations AS (
  SELECT p.id, p.seq, p.project_id, p.value,
    p.value->>'sourceExecutionId' AS source_id,
    jsonb_build_array(p.value->>'provider', lower(p.value->>'host'),
      lower(p.value->>'repo'), p.value->'number') AS request_key
  FROM audit_log p
  WHERE split_part(p.target, ':', 1)='publishing'
    AND jsonb_typeof(p.value->'number')='number' AND p.value->'number' > '0'::jsonb
    AND coalesce(p.value->>'error','')=''
    AND p.project_id IN (SELECT e.project_id FROM execution e WHERE e.id=ANY(@ids::text[]))
), linked AS (
  SELECT DISTINCT ON (e.id) e.id AS execution_id, p.project_id, p.request_key
  FROM execution e JOIN observations p ON p.project_id=e.project_id AND p.source_id=e.id
  WHERE e.id=ANY(@ids::text[])
  ORDER BY e.id, p.seq DESC
)
SELECT l.execution_id, p.value FROM linked l
CROSS JOIN LATERAL (
  SELECT x.value FROM observations x
  WHERE x.project_id=l.project_id AND x.request_key=l.request_key
  ORDER BY x.seq DESC LIMIT 1
) p
WHERE cardinality(@changed::text[])=0 OR EXISTS (
  SELECT 1 FROM observations c WHERE c.id=ANY(@changed::text[])
    AND c.project_id=l.project_id AND c.request_key=l.request_key
);
