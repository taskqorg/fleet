-- Definitions (definitions.go, definitions_write.go, definitions_usage.go), project definitions and access
-- (project_definitions.go, project_access.go): docs/designs/fleet-definition-editors.md.

-- name: DefRows :many
-- Definitions with one revision each: the head, or revision when above 0; kind and id optional; live leaves out deleted
-- ones, current retired ones.
SELECT d.kind, d.id, d.path, d.head::bigint AS head, d.repo_sha, d.repo_content_sha, d.ever_shipped, d.retired_at,
 r.revision::bigint AS revision, r.parent, r.content, r.content_sha, r.repo_base, r.source, r.actor_kind,
 r.by_email, r.by_execution, r.directed_by, r.message, r.at
FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id
 AND r.revision = CASE WHEN sqlc.arg(revision)::int > 0 THEN sqlc.arg(revision)::int ELSE d.head END
WHERE (sqlc.narg(kind)::text IS NULL OR d.kind=sqlc.narg(kind)::text) AND (sqlc.narg(id)::text IS NULL OR d.id=sqlc.narg(id)::text)
 AND (NOT sqlc.arg(live)::boolean OR d.deleted_at IS NULL) AND (NOT sqlc.arg(current)::boolean OR d.retired_at IS NULL)
ORDER BY d.kind, d.id;

-- name: DefHistory :many
-- A definition's revisions below before, newest first.
SELECT d.kind, d.id, d.path, d.head::bigint AS head, d.repo_sha, d.repo_content_sha, d.ever_shipped, d.retired_at,
 r.revision::bigint AS revision, r.parent, r.content, r.content_sha, r.repo_base, r.source, r.actor_kind,
 r.by_email, r.by_execution, r.directed_by, r.message, r.at
FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id
WHERE d.kind=sqlc.arg(kind) AND d.id=sqlc.arg(id) AND r.revision < sqlc.arg(before)::int
ORDER BY r.revision DESC LIMIT sqlc.arg(lim)::int;

-- name: DefHeadTexts :many
-- Every current definition of a kind (id optional) with its head content.
SELECT d.id, r.content FROM definition d JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
WHERE d.kind=sqlc.arg(kind) AND (sqlc.narg(id)::text IS NULL OR d.id=sqlc.narg(id)::text) AND d.retired_at IS NULL ORDER BY d.id;

-- name: DefHeadNumber :one
SELECT head::bigint AS head FROM definition WHERE kind=$1 AND id=$2;

-- name: DefLock :exec
SELECT 1 FROM definition WHERE kind=$1 AND id=$2 FOR UPDATE;

-- name: DefRevisionBySha :one
-- The newest revision with this exact content.
SELECT revision::bigint AS revision FROM definition_revision WHERE kind=$1 AND id=$2 AND content_sha=$3 ORDER BY revision DESC LIMIT 1;

-- name: DefRepoState :one
-- Reconcile: the repository version the store recorded, the row locked.
SELECT repo_sha, retired_at FROM definition WHERE kind=$1 AND id=$2 FOR UPDATE;

-- name: DefInsertShipped :exec
-- Reconcile: a shipped file the store has not seen, at revision 1; a project is its own project.
INSERT INTO definition(kind,id,path,head,repo_sha,repo_content,repo_content_sha,ever_shipped,project_id)
VALUES(sqlc.arg(kind), sqlc.arg(id), sqlc.arg(path), 1, sqlc.arg(repo_sha), sqlc.arg(repo_content), sqlc.arg(repo_content_sha), true,
 CASE WHEN sqlc.arg(kind)::text='project' THEN sqlc.arg(id)::text END);

-- name: DefRecordRepo :exec
-- Reconcile: the repository moved or the file came back; a deleted agent or pipeline is the repository's again, a deleted
-- project stays deleted.
UPDATE definition SET repo_sha=sqlc.arg(repo_sha), repo_content=sqlc.arg(repo_content), repo_content_sha=sqlc.arg(repo_content_sha), ever_shipped=true,
 deleted_at=CASE WHEN kind='project' THEN deleted_at END,
 retired_at=CASE WHEN kind='project' AND deleted_at IS NOT NULL THEN retired_at END
WHERE kind=sqlc.arg(kind) AND id=sqlc.arg(id);

-- name: DefShippedHeads :many
-- Reconcile: every current shipped definition with its repository and head content shas.
SELECT d.kind, d.id, d.repo_content_sha, r.content_sha AS head_sha FROM definition d
 JOIN definition_revision r ON r.kind=d.kind AND r.id=d.id AND r.revision=d.head
WHERE d.ever_shipped AND d.retired_at IS NULL AND d.repo_sha IS NOT NULL;

-- name: DefRepoGone :exec
-- Reconcile: the shipped file is gone; an unedited definition retires.
UPDATE definition SET repo_sha=NULL, repo_content=NULL, repo_content_sha=NULL,
 retired_at=CASE WHEN sqlc.arg(retire)::boolean THEN now() END
WHERE kind=sqlc.arg(kind) AND id=sqlc.arg(id);

-- name: DefRevisionInsert :exec
INSERT INTO definition_revision(kind,id,revision,parent,content,content_sha,repo_base,source,actor_kind,by_email,by_execution,directed_by,message)
VALUES(sqlc.arg(kind), sqlc.arg(id), sqlc.arg(revision)::int, sqlc.narg(parent)::int, sqlc.arg(content), sqlc.arg(content_sha), sqlc.narg(repo_base),
 sqlc.arg(source), sqlc.arg(actor_kind), sqlc.narg(by_email), sqlc.narg(by_execution), sqlc.narg(directed_by), sqlc.arg(message));

-- name: DefMoveHead :exec
-- A save moves the head: a definition whose file is gone is the store's own from now, a retired one is back.
UPDATE definition SET head=sqlc.arg(head)::int, retired_at=CASE WHEN deleted_at IS NULL THEN NULL ELSE retired_at END,
 ever_shipped=ever_shipped AND repo_sha IS NOT NULL
WHERE kind=sqlc.arg(kind) AND id=sqlc.arg(id);

-- name: DefCreate :execrows
INSERT INTO definition(kind,id,path,head) VALUES($1,$2,$3,0) ON CONFLICT DO NOTHING;

-- name: DefRepoContent :one
SELECT repo_content FROM definition WHERE kind=$1 AND id=$2;

-- name: DefDraft :one
SELECT base::bigint AS base, content, updated_at FROM definition_draft WHERE kind=$1 AND id=$2 AND person=$3;

-- name: DefDraftPut :exec
INSERT INTO definition_draft(kind,id,person,base,content) VALUES(sqlc.arg(kind), sqlc.arg(id), sqlc.arg(person), sqlc.arg(base)::int, sqlc.arg(content))
ON CONFLICT(kind,id,person) DO UPDATE SET base=excluded.base, content=excluded.content, updated_at=now();

-- name: DefDraftDelete :execrows
-- One person's draft, or (person NULL) every draft of the definition.
DELETE FROM definition_draft WHERE kind=sqlc.arg(kind) AND id=sqlc.arg(id) AND (sqlc.narg(person)::text IS NULL OR person=sqlc.narg(person)::text);

-- name: DefDelete :exec
UPDATE definition SET deleted_at=now(), retired_at=coalesce(retired_at,now()) WHERE kind=$1 AND id=$2;

-- name: DefRunning :one
-- Unfinished executions that pin the definition; older runs name it only in data (pipelineId) or config_ref (profileId).
SELECT count(*)::bigint FROM execution WHERE state IN ('preparing','running','waiting')
 AND (definitions @> jsonb_build_array(jsonb_build_object('kind',sqlc.arg(kind)::text,'id',sqlc.arg(id)::text))
  OR (sqlc.arg(kind)::text='pipeline' AND (pipeline_id=sqlc.arg(id)::text OR data->>'pipelineId'=sqlc.arg(id)::text))
  OR (sqlc.arg(kind)::text='agent' AND (profile_id=sqlc.arg(id)::text OR config_ref->>'profileId'=sqlc.arg(id)::text)));

-- name: DefExperimentsUsing :many
-- Live experiments naming the id as their pipeline, or as a JSON string anywhere in their variants or evaluators.
SELECT id FROM experiment WHERE state IN ('draft','running','paused') AND tombstoned_at IS NULL AND (pipeline_id=sqlc.arg(id)::text
 OR jsonb_path_exists(variants, '$.** ? (@ == $id)', jsonb_build_object('id',sqlc.arg(id)::text))
 OR jsonb_path_exists(evaluators, '$.** ? (@ == $id)', jsonb_build_object('id',sqlc.arg(id)::text))) ORDER BY id;

-- name: DefSessionsUsing :one
-- Open sessions whose profile it is.
SELECT count(*)::bigint FROM session WHERE payload->'agentConfig'->>'id'=sqlc.arg(id)::text AND archived_at IS NULL AND tombstoned_at IS NULL;

-- name: DefUsage :many
-- Runs, successes, median wall time and tokens of the executions that pinned a saved revision of kind/id (id '' for every
-- definition of the kind) in the last days, per definition (total=1) and per revision; -1 stands for no revision (a total)
-- and no median.
SELECT (d->>'id')::text AS id,
 coalesce(CASE WHEN (d->>'revision') ~ '^[0-9]+$' THEN (d->>'revision')::bigint END, -1)::bigint AS rev,
 GROUPING(CASE WHEN (d->>'revision') ~ '^[0-9]+$' THEN (d->>'revision')::bigint END)::int AS total,
 count(*)::bigint AS runs, (count(*) FILTER (WHERE e.state='succeeded'))::bigint AS succeeded,
 coalesce((percentile_disc(0.5) WITHIN GROUP (ORDER BY coalesce(t.wall_ms, CASE WHEN e.started_at IS NOT NULL AND e.ended_at IS NOT NULL THEN greatest(0, (extract(epoch FROM e.ended_at - e.started_at)*1000)::bigint) END)))::bigint, -1)::bigint AS median,
 coalesce(sum(coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)),0)::bigint AS tokens
FROM execution e CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d LEFT JOIN execution_totals t ON t.execution_id=e.id
WHERE e.definitions @> sqlc.arg(pin)::jsonb AND d->>'kind'=sqlc.arg(kind)::text AND (sqlc.arg(id)::text='' OR d->>'id'=sqlc.arg(id)::text)
 AND coalesce((d->>'draft')::boolean,false)=false AND coalesce(d->>'revision','') ~ '^[0-9]+$'
 AND coalesce(e.started_at, e.updated_at) >= now() - make_interval(days => sqlc.arg(days)::int)
GROUP BY d->>'id', ROLLUP (CASE WHEN (d->>'revision') ~ '^[0-9]+$' THEN (d->>'revision')::bigint END);

-- name: DefPersonRoles :one
SELECT roles, owner FROM person WHERE email=$1;

-- name: DefWriteRequest :one
-- A recorded project write of this person, operation and request id (idempotent replay).
SELECT payload_sha, result FROM project_write_request WHERE directing_email=$1 AND operation=$2 AND request_id=$3;

-- name: DefWriteRequestInsert :exec
INSERT INTO project_write_request(directing_email,operation,request_id,payload_sha,project_id,result) VALUES($1,$2,$3,$4,$5,$6);

-- name: DefSetProjectScope :exec
SELECT set_config('fleet.project_ids', sqlc.arg(ids)::text, true);

-- name: DefSetScope :exec
SELECT set_config('fleet.project_ids', sqlc.arg(ids)::text, true), set_config('fleet.person', sqlc.arg(person)::text, true);

-- name: DefProjectCreate :exec
-- A new project's identity, reserved with its creator as initial member.
INSERT INTO definition(kind,id,path,head,project_id,created_by,initial_member) VALUES('project', sqlc.arg(id), sqlc.arg(path), 0, sqlc.arg(id), sqlc.arg(created_by), sqlc.arg(created_by));

-- name: DefProjectAccessLock :one
SELECT access_version, (initial_member_revoked_at IS NOT NULL)::boolean AS revoked FROM definition WHERE kind='project' AND id=$1 FOR UPDATE;

-- name: DefProjectAccess :one
SELECT initial_member, (initial_member_revoked_at IS NOT NULL)::boolean AS revoked, access_version FROM definition WHERE kind='project' AND id=$1;

-- name: DefProjectRevoke :exec
UPDATE definition SET initial_member_revoked_at=now(), access_version=access_version+1 WHERE kind='project' AND id=$1;

-- name: DefProjectOpenWork :many
-- A project's open tasks (states given), open sessions and unfinished executions (not pipeline steps), per kind.
SELECT kind::text AS kind, count(*)::bigint AS n, min(id)::text AS example FROM (
 SELECT 'task' AS kind, id FROM task WHERE project_id=sqlc.arg(project_id)::text AND state::text = ANY(sqlc.arg(task_states)::text[])
 UNION ALL SELECT 'session', id FROM session WHERE project_id=sqlc.arg(project_id)::text AND archived_at IS NULL
 UNION ALL SELECT 'execution', id FROM execution WHERE project_id=sqlc.arg(project_id)::text AND kind::text<>'pipeline_step' AND state IN ('preparing','running','waiting')
) o GROUP BY kind ORDER BY kind;

-- name: DefProjectRetire :exec
UPDATE definition SET retired_at=coalesce(retired_at,now()) WHERE kind='project' AND id=$1;

-- name: DefProjectRetired :one
SELECT EXISTS(SELECT 1 FROM definition WHERE kind='project' AND id=$1 AND retired_at IS NOT NULL);

-- name: DefRemovalPending :one
-- A pending connection-remove of this connection on the host.
SELECT EXISTS(SELECT 1 FROM command WHERE host_id=sqlc.arg(host_id)::text AND kind='connection-remove' AND status='pending'
 AND payload->'payload'->>'connection'=sqlc.arg(connection)::text);

-- name: DefRemovalsPending :many
-- The pending connection-remove commands of these connections.
SELECT id FROM command WHERE kind='connection-remove' AND status='pending' AND payload->'payload'->>'connection' = ANY(sqlc.arg(connections)::text[]) ORDER BY seq;

-- name: DefPerson :one
SELECT email, name, roles, owner, loaded_at::text AS loaded_at FROM person WHERE email=$1;

-- name: DefPersonShare :one
SELECT email, name, roles, owner, loaded_at::text AS loaded_at FROM person WHERE email=$1 FOR SHARE;

-- name: DefMembershipVersion :one
-- The person's initial memberships with their access versions, as one text.
SELECT coalesce(string_agg(id||':'||access_version::text, ',' ORDER BY id), '')::text FROM definition WHERE kind='project' AND initial_member=$1;

-- name: DefGrants :many
SELECT object_kind, object_id FROM person_grant WHERE email=$1;

-- name: DefGrantsShare :many
SELECT object_kind, object_id FROM person_grant WHERE email=$1 FOR SHARE;

-- name: DefInitialMemberships :many
SELECT id FROM definition WHERE kind='project' AND initial_member=$1 AND initial_member_revoked_at IS NULL;

-- name: DefUsageRecent :many
-- DefUsage's totals for every definition of the kinds in one pass (Library's tree, system_changes): the last days'
-- executions are chosen by plain timestamp comparisons on execution_started_at, which RLS lets an index serve (a
-- jsonb containment is not leakproof, so DefUsage's pin cannot start from execution_definitions under RLS); a run that
-- has not started counts by its last update, as in DefUsage.
WITH recent AS (
 SELECT x.id, x.state, x.started_at, x.ended_at, x.definitions FROM execution x
 WHERE x.started_at >= now() - make_interval(days => sqlc.arg(days)::int) AND jsonb_typeof(x.definitions) = 'array' AND x.definitions <> '[]'::jsonb
 UNION ALL
 SELECT x.id, x.state, x.started_at, x.ended_at, x.definitions FROM execution x
 WHERE x.started_at IS NULL AND x.updated_at >= now() - make_interval(days => sqlc.arg(days)::int) AND jsonb_typeof(x.definitions) = 'array' AND x.definitions <> '[]'::jsonb
)
SELECT (d->>'kind')::text AS kind, (d->>'id')::text AS id,
 count(*)::bigint AS runs, (count(*) FILTER (WHERE e.state='succeeded'))::bigint AS succeeded,
 coalesce((percentile_disc(0.5) WITHIN GROUP (ORDER BY coalesce(t.wall_ms, CASE WHEN e.started_at IS NOT NULL AND e.ended_at IS NOT NULL THEN greatest(0, (extract(epoch FROM e.ended_at - e.started_at)*1000)::bigint) END)))::bigint, -1)::bigint AS median,
 coalesce(sum(coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)),0)::bigint AS tokens
FROM recent e CROSS JOIN LATERAL jsonb_array_elements(e.definitions) d
LEFT JOIN execution_totals t ON t.execution_id=e.id
WHERE d->>'kind' = ANY(sqlc.arg(kinds)::text[])
 AND coalesce((d->>'draft')::boolean,false)=false AND coalesce(d->>'revision','') ~ '^[0-9]+$'
GROUP BY d->>'kind', d->>'id';
