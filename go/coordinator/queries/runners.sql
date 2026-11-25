-- Runners screen (runners.go, docs/design-system/pages/runners.md). Each query repeats two expressions: when an execution
-- ran (end, else start, else the start a gateway call carries in data, else last seen), and its priced usages (a gateway
-- call's connection cost, an agent run's tokens per model, at the run's start; is_api_conn: a login that is an API connection).

-- name: RunnerRuns :many
-- Runs of the last 24 hours and every active run, tool calls apart; active first. ponytail: newest 500.
-- session_title: the session's stored or generated title, naming a run without a task.
SELECT e.id,e.kind::text AS kind,e.state::text AS state,e.host_id,e.harness,e.task_id,t.title,s.title AS session_title,e.session_id,e.parent_execution_id,e.problem,
 e.ended_at,e.last_seen_at,x.wall_ms,x.cpu_ms,x.peak_memory_bytes,
 (coalesce(e.data->>'connection','')||'/'||coalesce(e.data->>'tool',''))::text AS subject,x.connection_cost,
 x.billing_basis,coalesce(x.model,e.model) AS model,e.account_id,
 CASE WHEN coalesce(x.billing_basis,'') IN ('','unavailable') AND e.account_id IS NOT NULL THEN coalesce((SELECT m.is_api_conn FROM fleet_exec_conn_meta('',e.account_id) m),false) ELSE false END AS is_api_conn,
 x.tokens_input,x.tokens_output,x.tokens_cache_read,x.tokens_cache_write,coalesce(e.started_at,coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at)) AS at
FROM execution e LEFT JOIN execution_totals x ON x.execution_id=e.id LEFT JOIN task t ON t.id=e.task_id LEFT JOIN session s ON s.id=e.session_id
WHERE (e.state IN ('preparing','running','waiting') OR coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at) > now()-interval '24 hours')
 AND e.kind NOT IN ('pipeline_run','pipeline_step','tool_run')
ORDER BY (e.state IN ('preparing','running','waiting')) DESC,e.seq DESC LIMIT 500;

-- name: RunnerCalls :many
-- Tool calls of the same window with the caller run when it exists. ponytail: the newest 20000 calls of the day.
SELECT p.id AS caller_run,e.id,e.kind::text AS kind,e.state::text AS state,e.host_id,e.harness,e.task_id,t.title,e.session_id,e.parent_execution_id,e.problem,
 e.ended_at,e.last_seen_at,x.wall_ms,x.cpu_ms,x.peak_memory_bytes,
 (coalesce(e.data->>'connection','')||'/'||coalesce(e.data->>'tool',''))::text AS subject,x.connection_cost,
 x.billing_basis,coalesce(x.model,e.model) AS model,e.account_id,
 CASE WHEN coalesce(x.billing_basis,'') IN ('','unavailable') AND e.account_id IS NOT NULL THEN coalesce((SELECT m.is_api_conn FROM fleet_exec_conn_meta('',e.account_id) m),false) ELSE false END AS is_api_conn,
 x.tokens_input,x.tokens_output,x.tokens_cache_read,x.tokens_cache_write,coalesce(e.started_at,coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at)) AS at
FROM execution e LEFT JOIN execution_totals x ON x.execution_id=e.id LEFT JOIN task t ON t.id=e.task_id LEFT JOIN execution p ON p.id=e.parent_execution_id
WHERE (e.state IN ('preparing','running','waiting') OR coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at) > now()-interval '24 hours')
 AND e.kind='tool_run' ORDER BY e.seq DESC LIMIT 20000;

-- name: RunnerSpendRows :many
-- Every execution this UTC month by when it ran; today: it ran this UTC day; measured: it has recorded totals.
SELECT e.host_id,e.kind::text AS kind,(coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at) >= date_trunc('day',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC')::boolean AS today,
 (x.execution_id IS NOT NULL)::boolean AS measured,x.wall_ms,x.cpu_ms,(coalesce(e.data->>'connection','')||'/'||coalesce(e.data->>'tool',''))::text AS subject,x.connection_cost,
 x.billing_basis,coalesce(x.model,e.model) AS model,e.account_id,
 CASE WHEN coalesce(x.billing_basis,'') IN ('','unavailable') AND e.account_id IS NOT NULL THEN coalesce((SELECT m.is_api_conn FROM fleet_exec_conn_meta('',e.account_id) m),false) ELSE false END AS is_api_conn,
 x.tokens_input,x.tokens_output,x.tokens_cache_read,x.tokens_cache_write,coalesce(e.started_at,coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at)) AS at
FROM execution e LEFT JOIN execution_totals x ON x.execution_id=e.id
WHERE e.kind NOT IN ('pipeline_run','pipeline_step') AND coalesce(e.ended_at,e.started_at,CASE WHEN e.data->>'startedAt' ~ '^[0-9]{1,15}$' THEN to_timestamp((e.data->>'startedAt')::bigint/1000.0) END,e.last_seen_at) >= date_trunc('month',now() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC';
