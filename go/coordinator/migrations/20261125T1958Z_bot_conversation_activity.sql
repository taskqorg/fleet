-- migration 117 created each bot conversation with updated_at = the migration time, so the six moved
-- conversations listed as just active. Their time is their newest moved run, message or event; the list must not lie.
SELECT set_config('fleet.project_ids', '*', true);
UPDATE session s SET updated_at = x.at
FROM (SELECT d.id, greatest(
  (SELECT max(greatest(e.started_at, e.ended_at)) FROM execution e WHERE e.session_id = d.id),
  (SELECT max(m.updated_at) FROM message m WHERE m.session_id = d.id),
  (SELECT max(v.updated_at) FROM event v WHERE v.session_id = d.id AND NOT v.delta)) AS at
 FROM session d WHERE d.subject IS NOT NULL AND d.bot_id IS NOT NULL AND d.title_by = 'bot') x
WHERE s.id = x.id AND x.at IS NOT NULL;
-- The summary keeps the greater activity on an update, so it is counted again. A fresh database defines the function after
-- the migrations (schema/functions.sql) and has no such rows.
DO $$ BEGIN IF to_regprocedure('fleet_session_summary_recount(text)') IS NOT NULL THEN PERFORM fleet_session_summary_recount(d.id) FROM session d WHERE d.subject IS NOT NULL AND d.bot_id IS NOT NULL AND d.title_by = 'bot'; END IF; END $$;
