-- Delivery (delivery.go, delivery_history.go): the status pass's state and samples, and the history's records (pull
-- requests, merge-queue runs, releases, baselines, the check), settings and pass state. An equal record is not written
-- again, so the change stream (kind delivery) carries only real changes.

-- name: DeliveryPrPut :exec
INSERT INTO delivery_pr(id, payload, project_id, number) VALUES (@id, @payload::jsonb, @project_id, @number)
ON CONFLICT (id) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now() WHERE delivery_pr.payload IS DISTINCT FROM EXCLUDED.payload;

-- name: DeliveryRunPut :exec
INSERT INTO delivery_run(id, payload, project_id, run_id) VALUES (@id, @payload::jsonb, @project_id, @run_id)
ON CONFLICT (id) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now() WHERE delivery_run.payload IS DISTINCT FROM EXCLUDED.payload;

-- name: DeliveryReleasePut :exec
INSERT INTO delivery_release(id, payload, project_id, tag) VALUES (@id, @payload::jsonb, @project_id, @tag)
ON CONFLICT (id) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now() WHERE delivery_release.payload IS DISTINCT FROM EXCLUDED.payload;

-- name: DeliveryBaselinePut :exec
INSERT INTO delivery_baseline(id, payload, project_id, name) VALUES (@id, @payload::jsonb, @project_id, @name)
ON CONFLICT (id) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now() WHERE delivery_baseline.payload IS DISTINCT FROM EXCLUDED.payload;

-- name: DeliveryCheckPut :exec
INSERT INTO delivery_check(id, payload, project_id) VALUES (@id, @payload::jsonb, @project_id)
ON CONFLICT (id) DO UPDATE SET payload = EXCLUDED.payload, updated_at = now() WHERE delivery_check.payload IS DISTINCT FROM EXCLUDED.payload;

-- name: DeliveryPrPayloads :many
SELECT payload FROM delivery_pr ORDER BY id;

-- name: DeliveryRunPayloads :many
SELECT payload FROM delivery_run ORDER BY id;

-- name: DeliveryReleasePayloads :many
SELECT payload FROM delivery_release ORDER BY id;

-- name: DeliveryBaselinePayloads :many
SELECT payload FROM delivery_baseline ORDER BY id;

-- name: DeliveryCheckPayload :one
SELECT payload FROM delivery_check LIMIT 1;

-- name: DeliveryUIReleasePayloads :many
-- The UI-only releases the history recorded (deliveryPass).
SELECT payload FROM delivery_release WHERE tag LIKE 'ui-%';

-- name: DeliveryHistoryState :one
SELECT coalesce(backfilled_from, '')::text AS backfilled_from, backfilled, coalesce(through, '')::text AS through,
  coalesce(pulls_updated, '')::text AS pulls_updated, coalesce(installs_from, '')::text AS installs_from,
  coalesce(refreshed_at, '')::text AS refreshed_at, coalesce(problem, '')::text AS problem,
  coalesce(checked_release, '')::text AS checked_release, coalesce(checked_at, '')::text AS checked_at
FROM delivery_history_state LIMIT 1;

-- name: DeliveryUISeen :many
SELECT version, seen_at FROM delivery_ui_seen;

-- name: DeliveryRunReads :many
SELECT payload, attempts, coalesce(last_error, '')::text AS last_error FROM delivery_run_read ORDER BY run_id;

-- name: DeliveryPrRetries :many
SELECT number, attempts, next_attempt_at, coalesce(last_error, '')::text AS last_error FROM delivery_pr
WHERE attempts > 0 OR next_attempt_at IS NOT NULL OR last_error IS NOT NULL;

-- name: DeliveryHistoryStateSave :exec
INSERT INTO delivery_history_state(id, backfilled_from, backfilled, through, pulls_updated, installs_from, refreshed_at, problem,
  checked_release, checked_at, project_id)
VALUES ('delivery-history', nullif(@backfilled_from::text, ''), @backfilled, nullif(@through::text, ''), nullif(@pulls_updated::text, ''),
  nullif(@installs_from::text, ''), nullif(@refreshed_at::text, ''), nullif(@problem::text, ''), nullif(@checked_release::text, ''),
  nullif(@checked_at::text, ''), @project_id)
ON CONFLICT (id) DO UPDATE SET backfilled_from = EXCLUDED.backfilled_from, backfilled = EXCLUDED.backfilled, through = EXCLUDED.through,
  pulls_updated = EXCLUDED.pulls_updated, installs_from = EXCLUDED.installs_from, refreshed_at = EXCLUDED.refreshed_at,
  problem = EXCLUDED.problem, checked_release = EXCLUDED.checked_release, checked_at = EXCLUDED.checked_at, updated_at = now()
WHERE (delivery_history_state.backfilled_from, delivery_history_state.backfilled, delivery_history_state.through,
  delivery_history_state.pulls_updated, delivery_history_state.installs_from, delivery_history_state.refreshed_at,
  delivery_history_state.problem, delivery_history_state.checked_release, delivery_history_state.checked_at)
  IS DISTINCT FROM (EXCLUDED.backfilled_from, EXCLUDED.backfilled, EXCLUDED.through, EXCLUDED.pulls_updated, EXCLUDED.installs_from,
  EXCLUDED.refreshed_at, EXCLUDED.problem, EXCLUDED.checked_release, EXCLUDED.checked_at);

-- name: DeliverySiteObservations :many
SELECT first_seen, last_seen, covered_from, coordinator_version, ui_version, project_id
FROM delivery_site_observation ORDER BY first_seen;

-- name: DeliverySiteLatest :one
SELECT first_seen, last_seen, covered_from, coordinator_version, ui_version, project_id
FROM delivery_site_observation WHERE covered_from IS NOT NULL ORDER BY first_seen DESC LIMIT 1;

-- name: DeliverySiteExtend :exec
UPDATE delivery_site_observation SET last_seen = @last_seen WHERE first_seen = @first_seen;

-- name: DeliverySiteStart :exec
INSERT INTO delivery_site_observation(first_seen, last_seen, covered_from, coordinator_version, ui_version, project_id)
VALUES (@first_seen, @first_seen, @covered_from, @coordinator_version, @ui_version, @project_id);

-- name: DeliveryRunReadsKeep :exec
DELETE FROM delivery_run_read WHERE NOT run_id = ANY (@ids::bigint[]);

-- name: DeliveryRunReadsSave :exec
INSERT INTO delivery_run_read(run_id, payload, attempts, next_attempt_at, last_error, project_id)
SELECT DISTINCT ON (x.i) x.i, x.p, x.t, now(), nullif(x.e, ''), @project_id
FROM jsonb_to_recordset(@reads::jsonb) AS x(i bigint, p jsonb, t int, e text) ORDER BY x.i
ON CONFLICT (run_id) DO UPDATE SET payload = EXCLUDED.payload, attempts = EXCLUDED.attempts, next_attempt_at = EXCLUDED.next_attempt_at,
  last_error = EXCLUDED.last_error
WHERE (delivery_run_read.payload, delivery_run_read.attempts, delivery_run_read.last_error)
  IS DISTINCT FROM (EXCLUDED.payload, EXCLUDED.attempts, EXCLUDED.last_error);

-- name: DeliveryPrRetrySave :exec
-- The PR CI lookup state on the stored pull requests (prs: n, a, t as RFC 3339 or '' for none, e); a pull request left out is reset.
UPDATE delivery_pr p SET attempts = coalesce(x.a, 0), next_attempt_at = nullif(x.t, '')::timestamptz, last_error = nullif(x.e, '')
FROM delivery_pr o LEFT JOIN jsonb_to_recordset(@prs::jsonb) AS x(n bigint, a int, t text, e text) ON x.n = o.number
WHERE p.id = o.id AND (x.n IS NOT NULL OR o.attempts <> 0 OR o.next_attempt_at IS NOT NULL OR o.last_error IS NOT NULL)
  AND (p.attempts, p.next_attempt_at, p.last_error) IS DISTINCT FROM (coalesce(x.a, 0), nullif(x.t, '')::timestamptz, nullif(x.e, ''));

-- name: DeliverySettingsGet :one
SELECT ratio, min_samples, min_delta_ms, baseline FROM delivery_settings LIMIT 1;

-- name: DeliverySettingsPut :exec
INSERT INTO delivery_settings(id, ratio, min_samples, min_delta_ms, baseline, project_id)
VALUES ('delivery-settings', @ratio, @min_samples, @min_delta_ms, @baseline, @project_id)
ON CONFLICT (id) DO UPDATE SET ratio = EXCLUDED.ratio, min_samples = EXCLUDED.min_samples, min_delta_ms = EXCLUDED.min_delta_ms,
  baseline = EXCLUDED.baseline, updated_at = now();

-- name: DeliveryBaselineDelete :exec
DELETE FROM delivery_baseline WHERE name = @name;

-- name: DeliveryBaselineExists :one
SELECT EXISTS (SELECT 1 FROM delivery_baseline WHERE name = @name);

-- name: DeliveryProblemRefresh :exec
-- An open regression problem shows the latest numbers and range, not those of the check that opened it.
UPDATE problem SET text = @text, data = data || @data::jsonb WHERE cause = @cause AND state = 'open' AND text <> @text;

-- name: DeliveryStatePut :exec
INSERT INTO delivery_state(id, project_id, refreshed_at) VALUES (@id, @project_id, @refreshed_at)
ON CONFLICT (id) DO UPDATE SET refreshed_at = EXCLUDED.refreshed_at, updated_at = now();

-- name: DeliverySampleAdd :exec
INSERT INTO delivery_sample(project_id, repo, at, payload) VALUES (@project_id, @repo, @at, @payload::jsonb);

-- name: DeliverySamplePrune :exec
DELETE FROM delivery_sample WHERE at < now() - interval '7 days';

-- name: DeliverySamples :many
SELECT project_id, repo, at, payload FROM delivery_sample WHERE at >= @since ORDER BY at, seq;

-- name: DeliveryHostsSeen :many
-- Every host's observation as the delivery reads it: last seen (ms, 0 when never), executor version and update.
SELECT id, coalesce((extract(epoch FROM last_seen_at) * 1000)::bigint, 0)::float8 AS last_seen_ms,
  (CASE WHEN jsonb_typeof(payload->'version') = 'string' THEN payload->>'version' ELSE '' END)::text AS version,
  (payload->'update')::jsonb AS update
FROM host_observation ORDER BY seq;

-- name: DeliverySessionTasks :many
-- The task of each named session that has one: a pull request from a session branch is that task's.
SELECT id, task_id::text AS task_id FROM session WHERE id = ANY(@ids::text[]) AND task_id IS NOT NULL;

-- name: DeliverySourceTasks :many
-- The session and task (its own, else its session's) of each named source execution of a publishing record.
SELECT e.id, coalesce(e.session_id, '')::text AS session_id, coalesce(e.task_id, s.task_id)::text AS task_id
FROM execution e LEFT JOIN session s ON s.id = e.session_id
WHERE e.id = ANY(@ids::text[]) AND coalesce(e.task_id, s.task_id) IS NOT NULL;

-- name: DeliverySnapshotPut :exec
-- The pass's last state (deliverySnapshot), kept across restarts.
INSERT INTO delivery_snapshot(id, state) VALUES ('last', @state) ON CONFLICT (id) DO UPDATE SET state = EXCLUDED.state, updated_at = now();

-- name: DeliverySnapshotGet :one
SELECT state FROM delivery_snapshot WHERE id = 'last';
