-- Entity codes, entity cards and search (entity_codes.go, entity_card.go, search.go): read paths over the entity tables
-- under the caller's row-level security. Search conditions mirror the lists' own (task_page, sessions_list,
-- artifact_list: tasks.go taskWhere, sessions.go sessionWhere, search.go artifactSearch), so a count opens a list of as
-- many rows. Free-text words come as LIKE patterns (likePattern), each to be found in one of the text columns; field terms
-- (agent, host, project: contains; state: exact) are optional.

-- name: EntResolveCodes :many
-- The entities codes name: a current code, or an alias superseded by a rename, with the entity's current code.
SELECT e.code, e.entity_id, e.type, coalesce(t.code,x.code,d.code,a.code,b.code)::text AS current FROM entity_code e
 LEFT JOIN task t ON e.type='T' AND t.id=e.entity_id LEFT JOIN question x ON e.type='Q' AND x.id=e.entity_id
 LEFT JOIN session d ON e.type='C' AND d.id=e.entity_id
 LEFT JOIN artifact a ON e.type='R' AND a.id=e.entity_id LEFT JOIN bot b ON e.type='B' AND b.id=e.entity_id
 WHERE e.code=ANY(@codes::text[]) AND coalesce(t.code,x.code,d.code,a.code,b.code) IS NOT NULL
 AND (e.code=coalesce(t.code,x.code,d.code,a.code,b.code) OR e.superseded_by IS NOT NULL);

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
 (SELECT state FROM fleet_conversation_state(d.id))::text AS state, coalesce(last.harness,d.payload->'agentConfig'->>'harness','')::text AS harness,
 coalesce((SELECT m.text FROM message m WHERE m.session_id=d.id ORDER BY m.seq LIMIT 1),'')::text AS first_message,
 greatest(d.updated_at,(SELECT max(a.updated_at) FROM message a WHERE a.session_id=d.id),(SELECT max(a.updated_at) FROM event a WHERE a.session_id=d.id AND NOT a.delta),ex.last_at)::timestamptz AS activity_at,
 ex.n::bigint AS runs, (SELECT count(*) FROM question x WHERE x.session_id=d.id AND x.state='open')::bigint AS open_questions
 FROM session d LEFT JOIN task t ON t.id=d.task_id
 LEFT JOIN LATERAL (SELECT e.harness FROM execution e WHERE e.session_id=d.id ORDER BY e.seq DESC LIMIT 1) last ON true
 LEFT JOIN LATERAL (SELECT count(*) AS n, max(greatest(e.started_at,e.ended_at)) AS last_at FROM execution e WHERE e.session_id=d.id) ex ON true
 WHERE d.id=$1;

-- name: EntCardExecution :one
SELECT e.id, e.kind::text AS kind, e.state::text AS state, e.project_id, e.task_id, e.session_id, e.experiment_id, e.host_id, e.harness, e.model, e.problem,
 e.started_at, e.ended_at, e.updated_at, t.code AS task_code, t.title AS task_title, s.code AS session_code
 FROM execution e LEFT JOIN task t ON t.id=e.task_id LEFT JOIN session s ON s.id=e.session_id WHERE e.id=$1;

-- name: EntCardResult :one
-- A result by its id or the id of the legacy record it replaced; preview_id is its newest stored preview (its thumbnail,
-- else a video's poster), the picture its card shows, as ThreadStoredArtifacts picks it.
SELECT a.id, a.code, a.name, a.state, a.project_id, a.media_type, a.size, a.content_hash, a.created_at, t.code AS task_code,
 coalesce((SELECT p.id FROM artifact p WHERE p.execution_id=a.execution_id AND p.derived_from_artifact_id=a.id AND p.state='stored' AND p.role IN ('thumbnail','poster')
  ORDER BY p.role='thumbnail' DESC, p.seq DESC LIMIT 1),'')::text AS preview_id
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
 (SELECT state FROM fleet_conversation_state(d.id))::text AS state,
 (SELECT e.harness FROM execution e WHERE e.session_id=d.id AND e.kind='agent_run' ORDER BY e.seq DESC LIMIT 1) AS harness,
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
 (SELECT state FROM fleet_conversation_state(d.id))::text AS state,
 (SELECT e.harness FROM execution e WHERE e.session_id=d.id AND e.kind='agent_run' ORDER BY e.seq DESC LIMIT 1) AS harness,
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
