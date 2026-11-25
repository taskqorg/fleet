-- Work-item threads leave the model (fleet-conversations.md#migration-to-the-conversation-model, step 4): each line of
-- work is its own conversation with one native session. Since migration 117 a bot conversation's turns still carried
-- its subject as thread, so their native session sits under nativeSessions "<continuation>#<subject>" while
-- nativeSessionId kept the one from the split. That newest native session becomes the conversation's own. Then every
-- thread-keyed native session, thread visit and thread field goes, and the thread columns are dropped. Safe to run
-- again: the steps that read the columns run only while they exist.
SELECT set_config('fleet.project_ids', '*', true);
DO $$ BEGIN IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'execution' AND column_name = 'thread') THEN EXECUTE $q$ UPDATE session s SET payload = jsonb_set(s.payload || jsonb_build_object('nativeSessionId', x.value->>'nativeSessionId'), ARRAY['nativeSessions', x.base], x.value) FROM (SELECT DISTINCT ON (s2.id) s2.id, left(n.key, position('#' IN n.key) - 1) AS base, n.value FROM session s2 JOIN LATERAL (SELECT e.thread FROM execution e WHERE e.session_id = s2.id AND e.kind = 'agent_run' ORDER BY e.seq DESC LIMIT 1) last ON last.thread <> '' CROSS JOIN LATERAL jsonb_each(s2.payload->'nativeSessions') n WHERE jsonb_typeof(s2.payload->'nativeSessions') = 'object' AND right(n.key, length(last.thread) + 1) = '#' || last.thread AND coalesce(n.value->>'nativeSessionId', '') <> '' AND coalesce((n.value->>'capturedAt')::bigint, 0) >= coalesce((s2.payload->'nativeSessions'->left(n.key, position('#' IN n.key) - 1)->>'capturedAt')::bigint, 0) ORDER BY s2.id, (n.value->>'capturedAt')::bigint DESC NULLS LAST) x WHERE s.id = x.id $q$; END IF; END $$;
UPDATE session SET payload = jsonb_set(payload, '{nativeSessions}', coalesce((SELECT jsonb_object_agg(n.key, n.value)
 FROM jsonb_each(payload->'nativeSessions') n WHERE position('#' IN n.key) = 0), '{}'::jsonb))
WHERE jsonb_typeof(payload->'nativeSessions') = 'object' AND EXISTS (SELECT 1 FROM jsonb_object_keys(payload->'nativeSessions') k WHERE position('#' IN k) > 0);
UPDATE queued_message SET payload = payload - 'thread' WHERE payload ? 'thread';
UPDATE message SET payload = payload - 'thread' WHERE payload ? 'thread';
UPDATE execution SET data = data - 'thread' WHERE data ? 'thread';
DELETE FROM person_visit WHERE page LIKE 'thread:%';
ALTER TABLE execution DROP COLUMN IF EXISTS thread;
ALTER TABLE message DROP COLUMN IF EXISTS thread;
