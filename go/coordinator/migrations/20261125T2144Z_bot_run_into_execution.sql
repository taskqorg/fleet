-- Conversation model, migration step 4 (docs/designs/fleet-conversations.md): a bot run is the run (execution) of a
-- turn in one of the bot's conversations, and a wake that never ran is a row of its trigger's log. bot_run goes:
-- its cause and quiet verdict move into the run's botContext (cause absent: a message), its task links keep their run
-- (bot_task.execution_id), and a skipped trigger wake becomes a trigger_log row. Its other rows that never ran (a
-- skipped manual run, a message cancelled in the queue) are deleted; the audit row legacy:<this file> counts them.
-- A bot has no Home either: its conversations are found by (bot_id, subject), so bot.home_session_id goes, and the
-- unused bot_automation and bot_wake_cursor tables (replaced by trigger, migration 106) go too, in a later migration.
SELECT set_config('fleet.project_ids', '*', true);
SELECT set_config('fleet.deleted', 'on', true);
-- A rerun finds the old columns dropped (and bot_run empty): empty stand-ins let the statements below run on nothing.
CREATE TABLE IF NOT EXISTS bot_run (id text PRIMARY KEY, bot_id text, project_id text, bot_revision integer, memory_revision integer,
 execution_id text, cause text, dedup_key text, state text, quiet boolean, reason text, created_at timestamptz, trigger_id text);
ALTER TABLE bot_task ADD COLUMN IF NOT EXISTS bot_run_id text;
ALTER TABLE bot ADD COLUMN IF NOT EXISTS home_session_id text;

CREATE TABLE IF NOT EXISTS trigger_log (
 trigger_id text NOT NULL REFERENCES trigger(id) ON DELETE CASCADE,
 request_id text NOT NULL,
 project_id text,
 reason text NOT NULL,
 at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (trigger_id, request_id)
);
CREATE INDEX IF NOT EXISTS trigger_log_at ON trigger_log(trigger_id, at DESC);
-- The runs of a bot, newest first (bot_runs, a bot's active and last run).
CREATE INDEX IF NOT EXISTS execution_bot_seq ON execution(bot_id, seq DESC) WHERE bot_id IS NOT NULL;
DROP INDEX IF EXISTS execution_bot;

UPDATE execution e SET bot_id = coalesce(e.bot_id, br.bot_id),
 data = jsonb_set(e.data, '{botContext}', (coalesce(e.data->'botContext', jsonb_build_object('botId', br.bot_id, 'projectId', br.project_id,
  'botRevision', br.bot_revision, 'memoryRevision', br.memory_revision)) - 'botRunId')
  || jsonb_strip_nulls(jsonb_build_object('cause', nullif(br.cause, 'message'), 'quiet', CASE WHEN br.quiet THEN true END)))
FROM bot_run br WHERE br.execution_id = e.id;
UPDATE bot_task bt SET execution_id = br.execution_id
FROM bot_run br WHERE br.id = bt.bot_run_id AND bt.execution_id IS NULL AND br.execution_id IS NOT NULL;
INSERT INTO trigger_log(trigger_id, request_id, project_id, reason, at)
SELECT br.trigger_id, coalesce(br.dedup_key, br.id), br.project_id, coalesce(nullif(br.reason, ''), br.state), br.created_at
FROM bot_run br JOIN trigger t ON t.id = br.trigger_id
WHERE br.execution_id IS NULL AND br.state IN ('skipped', 'failed')
ON CONFLICT DO NOTHING;
-- A Home conversation is an ordinary conversation of its bot.
UPDATE session s SET bot_id = b.id FROM bot b WHERE b.home_session_id = s.id AND s.bot_id IS NULL;

INSERT INTO audit_log(id, target, field, previous, value, actor, at, payload)
SELECT '20261125T2144Z_bot_run_into_execution', 'legacy:20261125T2144Z_bot_run_into_execution', 'deleted', NULL,
 jsonb_build_object('botRunsWithoutRun', (SELECT count(*) FROM bot_run br WHERE br.execution_id IS NULL),
  'intoTriggerLog', (SELECT count(*) FROM bot_run br JOIN trigger t ON t.id = br.trigger_id WHERE br.execution_id IS NULL AND br.state IN ('skipped', 'failed'))),
 'migration', now(), '{}'::jsonb
WHERE EXISTS (SELECT 1 FROM bot_run)
ON CONFLICT (id) DO NOTHING;

ALTER TABLE bot_task DROP COLUMN bot_run_id;
ALTER TABLE bot DROP COLUMN home_session_id;
-- The tables themselves stay, empty or unused, until no release in the rollback window (deploy/migrate-check.sh, the
-- last 5) creates their policies and triggers at start; a later migration drops bot_run, bot_automation and
-- bot_wake_cursor.
DELETE FROM bot_run;
