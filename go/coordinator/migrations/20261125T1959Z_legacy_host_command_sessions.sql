-- one-time legacy cleanup (owner "да", 2026-11-26; fleet-conversations.md#migration-to-the-conversation-model
-- step 2): the hidden host command sessions (actor program-worker) are no conversation and are deleted with what
-- cascades from them (events, commands, messages, uploads, summaries). Their runs, costs and artifacts stay:
-- execution, artifact, question, runner_call_hour and session references are cleared first, so no link dangles. Sessions
-- with a run still open are kept. The file is safe to run again. The counts before the delete are one audit_log row (target legacy:20261125T1959Z_legacy_host_command_sessions).
SELECT set_config('fleet.project_ids', '*', true);
CREATE TEMP TABLE legacy_hs AS
 SELECT s.id FROM session s WHERE s.actor = 'program-worker'
  AND NOT EXISTS (SELECT 1 FROM execution e WHERE e.session_id = s.id AND e.state IN ('preparing', 'running', 'waiting'));
INSERT INTO audit_log (id, target, field, value, actor)
SELECT '20261125T1959Z_legacy_host_command_sessions', 'legacy:20261125T1959Z_legacy_host_command_sessions', 'deleted host command sessions', jsonb_build_object(
 'session', (SELECT count(*) FROM legacy_hs),
 'event', (SELECT count(*) FROM event WHERE session_id IN (SELECT id FROM legacy_hs)),
 'command', (SELECT count(*) FROM command WHERE session_id IN (SELECT id FROM legacy_hs)),
 'message', (SELECT count(*) FROM message WHERE session_id IN (SELECT id FROM legacy_hs)),
 'upload', (SELECT count(*) FROM upload WHERE session_id IN (SELECT id FROM legacy_hs)),
 'artifactKept', (SELECT count(*) FROM artifact WHERE session_id IN (SELECT id FROM legacy_hs)),
 'executionKept', (SELECT count(*) FROM execution WHERE session_id IN (SELECT id FROM legacy_hs))), 'migration'
ON CONFLICT (id) DO NOTHING;
UPDATE execution SET session_id = NULL WHERE session_id IN (SELECT id FROM legacy_hs);
UPDATE artifact SET session_id = NULL WHERE session_id IN (SELECT id FROM legacy_hs);
UPDATE question SET session_id = NULL WHERE session_id IN (SELECT id FROM legacy_hs);
UPDATE runner_call_hour SET session_id = NULL WHERE session_id IN (SELECT id FROM legacy_hs);
UPDATE session SET created_by_session_id = NULL WHERE created_by_session_id IN (SELECT id FROM legacy_hs);
UPDATE session SET source_session_id = NULL WHERE source_session_id IN (SELECT id FROM legacy_hs);
DELETE FROM session WHERE id IN (SELECT id FROM legacy_hs);
DROP TABLE legacy_hs;
