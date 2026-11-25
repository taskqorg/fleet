-- Conversation model step 5 (docs/designs/fleet-conversations.md#migration-to-the-conversation-model): a private chat
-- is a room with one person, and its current conversation is the room's open binding (telegram_work), as in a group
-- room. Each person's home conversation becomes the open work item of their private room (<user id>:0), then
-- telegram_person.home_session_id goes. A rerun finds the column gone and moves nothing.
SELECT set_config('fleet.project_ids', '*', true);
DO $$ BEGIN IF EXISTS (SELECT 1 FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = 'telegram_person' AND column_name = 'home_session_id') THEN
  EXECUTE $q$INSERT INTO telegram_room (id, chat_id, thread_id, kind) SELECT p.telegram_user_id || ':0', p.telegram_user_id, 0, 'private' FROM telegram_person p WHERE p.home_session_id IS NOT NULL ON CONFLICT DO NOTHING$q$; EXECUTE $q$INSERT INTO telegram_work (id, room_id, session_id, title, created_by, created_at, last_at) SELECT 'work:home:' || p.email, r.id, p.home_session_id, coalesce(s.title, ''), p.email, coalesce(s.created_at, p.linked_at), clock_timestamp() FROM telegram_person p JOIN telegram_room r ON r.chat_id = p.telegram_user_id AND r.thread_id = 0 JOIN session s ON s.id = p.home_session_id WHERE NOT EXISTS (SELECT 1 FROM telegram_work w WHERE w.room_id = r.id AND w.session_id = p.home_session_id AND w.closed_at IS NULL) ON CONFLICT DO NOTHING$q$; END IF; END $$;
ALTER TABLE telegram_person DROP COLUMN IF EXISTS home_session_id;
