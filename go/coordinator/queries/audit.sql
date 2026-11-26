-- The audit (audit.go, migration 82): one audit_log row per changed setting or object, written only by audit(). A
-- record is the row as the API shows it: payload (a reason, a note) under the columns, at in epoch ms.

-- name: AuditInsert :exec
INSERT INTO audit_log(id, target, field, previous, value, actor, on_behalf_of, holder, project_id, payload)
VALUES (@id::text, @target::text, @field::text, sqlc.narg(previous)::jsonb, sqlc.narg(value)::jsonb, @actor::text,
 sqlc.narg(on_behalf_of)::text, sqlc.narg(holder)::text, sqlc.narg(project_id)::text, @payload::jsonb);

-- name: AuditRecords :many
-- setting_changes: newest first, optionally of one target; the people list's own audit only for the owner.
SELECT seq, (payload || jsonb_strip_nulls(jsonb_build_object('id', id, 'target', target, 'field', field, 'actor', actor,
 'onBehalfOf', on_behalf_of, 'holder', holder, 'projectId', project_id, 'at', (extract(epoch FROM at) * 1000)::bigint))
 || jsonb_build_object('previous', previous, 'value', value))::jsonb AS data
FROM audit_log
WHERE (@target::text = '' OR target = @target::text)
 AND (@owner::boolean OR target <> 'people')
 AND (sqlc.narg(before)::bigint IS NULL OR seq < sqlc.narg(before)::bigint)
ORDER BY seq DESC LIMIT @lim::int;

-- name: AuditOfKind :many
-- The audit records of every target of a kind (account, connection: the target's part before ':'), oldest first.
SELECT target, (payload || jsonb_strip_nulls(jsonb_build_object('id', id, 'target', target, 'field', field, 'actor', actor,
 'onBehalfOf', on_behalf_of, 'holder', holder, 'projectId', project_id, 'at', (extract(epoch FROM at) * 1000)::bigint))
 || jsonb_build_object('previous', previous, 'value', value))::jsonb AS data
FROM audit_log WHERE split_part(target, ':', 1) = @kind::text ORDER BY seq;

-- name: PeopleCloudflareAudit :many
-- Retain the latest sync intent or result for each managed email after it leaves the roster.
SELECT DISTINCT ON (field) field, value
FROM audit_log
WHERE target = 'people' AND field LIKE 'cloudflare-access:%'
ORDER BY field, seq DESC;

-- name: AuditLatestOfTargets :many
-- The newest audit record of each named target (connections_drift: a connection's last settings change).
SELECT DISTINCT ON (target) target, (payload || jsonb_strip_nulls(jsonb_build_object('id', id, 'target', target, 'field', field, 'actor', actor,
 'onBehalfOf', on_behalf_of, 'holder', holder, 'projectId', project_id, 'at', (extract(epoch FROM at) * 1000)::bigint)))::jsonb AS data
FROM audit_log WHERE target = ANY(@targets::text[]) ORDER BY target, seq DESC;
