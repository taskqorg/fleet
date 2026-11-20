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
