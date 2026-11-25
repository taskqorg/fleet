-- The latest limit reading per subscription, period and visibility scope (docs/designs/fleet-people-access.md §8), kept
-- by the trigger fleet_limit_reading_latest (schema/triggers.sql) in the same transaction as the limit_reading event
-- that brings it. latestReadings (artifact_routes.go: subscription_limits, access_status, dispatch) read every
-- reading ever written and sorted them (1.2-7 s on prod, 2026-11-26); this table holds one row per account, period
-- and scope at any age, so a 30-day window or a reading without a reset time stays. scope is the reading's project,
-- or session:<id> for a projectless run, so row-level security shows a person the same latest visible reading the
-- events did.
CREATE TABLE IF NOT EXISTS limit_reading_latest (
 account_id text NOT NULL,
 period text NOT NULL,
 scope text NOT NULL,
 project_id text NULL,
 session_id text NULL,
 host_id text NULL,
 event_seq bigint NOT NULL,
 updated_at timestamptz NOT NULL,
 payload jsonb NOT NULL,
 PRIMARY KEY (account_id, period, scope)
);
SELECT set_config('fleet.project_ids', '*', true);
INSERT INTO limit_reading_latest (account_id, period, scope, project_id, session_id, host_id, event_seq, updated_at, payload)
SELECT DISTINCT ON (x.account_id, coalesce(r.value->>'period', ''), coalesce(d.project_id, 'session:' || coalesce(d.session_id, '')))
 x.account_id, coalesce(r.value->>'period', ''), coalesce(d.project_id, 'session:' || coalesce(d.session_id, '')),
 d.project_id, d.session_id, x.host_id, d.seq, d.updated_at, r.value
FROM event d JOIN execution x ON x.id = d.execution_id
 CROSS JOIN LATERAL jsonb_array_elements(coalesce(d.payload->'data'->'readings', '[]'::jsonb)) AS r(value)
WHERE NOT d.delta AND d.type = 'limit_reading' AND x.account_id IS NOT NULL
ORDER BY x.account_id, coalesce(r.value->>'period', ''), coalesce(d.project_id, 'session:' || coalesce(d.session_id, '')), d.seq DESC
ON CONFLICT (account_id, period, scope) DO NOTHING;
