-- Subscriptions (accounts.go, limits.go; migration 78).

-- name: AccountNameTaken :one
SELECT EXISTS (SELECT 1 FROM managed_account WHERE name=$1);

-- name: AccountInspectionExecution :one
-- The execution of an inspection: retained ones predate host_job and may still be tool_run.
SELECT (e.data || jsonb_build_object('state', e.state, 'observation', e.observation))::jsonb AS record
 FROM execution e JOIN account_inspection i ON i.id=$1 AND e.id=i.execution_id
 WHERE e.kind IN ('host_job','tool_run') AND e.data->>'actor'='program-worker'
 AND e.data->>'accountId'=i.account_id AND e.project_id IS NOT DISTINCT FROM i.project_id;

-- name: LimitReadingsSince :many
-- Every limit_reading since a time, per subscription, oldest first.
SELECT x.account_id::text AS account_id, r.value::jsonb AS reading, d.updated_at
 FROM event d JOIN execution x ON x.id = d.execution_id CROSS JOIN LATERAL jsonb_array_elements(coalesce(d.payload->'data'->'readings','[]'::jsonb)) r(value)
 WHERE NOT d.delta AND d.type='limit_reading' AND x.account_id IS NOT NULL AND d.updated_at >= $1
 ORDER BY d.updated_at, d.seq;

-- name: InflightLimitRead :one
-- A limits read already on its way for a subscription.
SELECT host_id, execution_id, payload FROM command WHERE kind= @kind::text AND payload->'payload'->>'mode'='limits'
 AND payload->'payload'->>'accountId'= @account_id::text AND status IN ('pending','accepted','started') LIMIT 1;

-- name: AccountTakesWork :one
-- A subscription, by id or name, that takes new work (intake all).
SELECT EXISTS (SELECT 1 FROM managed_account WHERE intake='all' AND (id= @account::text OR name= @account::text));

-- name: AccountRows :many
-- Every subscription, in insertion order.
SELECT * FROM managed_account ORDER BY seq;

-- name: AccountHostSeen :many
-- When each host was last seen, by its observation.
SELECT id, last_seen_at FROM host_observation ORDER BY seq;

-- name: AccountHostHarnessAllowed :one
-- Whether a host that is not removed allows a harness (host setting harnesses.<harness>.allowed; only false refuses).
SELECT coalesce(payload->'harnesses'->(sqlc.arg(harness)::text)->'allowed', 'true'::jsonb) <> 'false'::jsonb AS allowed
 FROM host WHERE host_id= @host_id::text AND state<>'removed' ORDER BY seq LIMIT 1;

-- name: AccountRowOf :one
SELECT * FROM managed_account WHERE id=$1;

-- name: AccountPut :exec
INSERT INTO managed_account(id, name, holder, intake, harness, provider, project_id, payload)
VALUES (@id, sqlc.narg(name), sqlc.narg(holder), sqlc.narg(intake), sqlc.narg(harness), sqlc.narg(provider), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 name=excluded.name, holder=excluded.holder, intake=excluded.intake, harness=excluded.harness, provider=excluded.provider, project_id=excluded.project_id;

-- name: AccountInspections :many
SELECT * FROM account_inspection
WHERE (sqlc.narg(account_id)::text IS NULL OR account_id = sqlc.narg(account_id)) ORDER BY seq;

-- name: AccountInspectionPut :exec
INSERT INTO account_inspection(id, account_id, execution_id, host_id, project_id, payload)
VALUES (@id, sqlc.narg(account_id), sqlc.narg(execution_id), sqlc.narg(host_id), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 account_id=excluded.account_id, execution_id=excluded.execution_id, host_id=excluded.host_id, project_id=excluded.project_id;

-- name: AccountChecks :many
SELECT * FROM account_check
WHERE account_id = @account_id::text
 AND (sqlc.narg(host_id)::text IS NULL OR host_id = sqlc.narg(host_id))
 AND (sqlc.narg(binding)::text IS NULL OR binding = sqlc.narg(binding))
ORDER BY seq;

-- name: AccountCheckPut :exec
INSERT INTO account_check(id, account_id, host_id, binding, project_id, payload)
VALUES (@id, sqlc.narg(account_id), sqlc.narg(host_id), sqlc.narg(binding), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 account_id=excluded.account_id, host_id=excluded.host_id, binding=excluded.binding, project_id=excluded.project_id;

-- name: AccountCatalogues :many
SELECT * FROM account_catalogue
WHERE (sqlc.narg(host_id)::text IS NULL OR host_id = sqlc.narg(host_id))
 AND (sqlc.narg(harness)::text IS NULL OR harness = sqlc.narg(harness))
ORDER BY seq;

-- name: AccountCataloguePut :exec
INSERT INTO account_catalogue(id, host_id, harness, project_id, payload)
VALUES (@id, sqlc.narg(host_id), sqlc.narg(harness), sqlc.narg(project_id), @payload)
ON CONFLICT(id) DO UPDATE SET payload=excluded.payload, updated_at=now(),
 host_id=excluded.host_id, harness=excluded.harness, project_id=excluded.project_id;

-- name: AccountBindingRejectModel :execrows
-- J5: marks model rejected on the account's binding on a host, in one statement (no read-modify-write of the account).
UPDATE managed_account SET updated_at = now(), payload = jsonb_set(payload, '{bindings}', (
  SELECT jsonb_agg(CASE WHEN b->>'hostId' = sqlc.arg(host_id)::text
    THEN jsonb_set(b, '{rejectedModels}', CASE WHEN jsonb_typeof(b->'rejectedModels') = 'object' THEN b->'rejectedModels' ELSE '{}'::jsonb END || jsonb_build_object(sqlc.arg(model)::text, sqlc.arg(at_ms)::bigint))
    ELSE b END ORDER BY ord)
  FROM jsonb_array_elements(payload->'bindings') WITH ORDINALITY AS x(b, ord)))
WHERE id = sqlc.arg(id) AND jsonb_typeof(payload->'bindings') = 'array'
  AND EXISTS (SELECT 1 FROM jsonb_array_elements(payload->'bindings') b
    WHERE b->>'hostId' = sqlc.arg(host_id)::text AND NOT coalesce(b->'rejectedModels' ? sqlc.arg(model)::text, false));
