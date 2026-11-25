-- A forum topic's room keeps its group's title (chat_title) beside its own (the topic name): Conversations labels a
-- topic "<group> › <topic>" and the general chat "<group>", so one group's rooms read as that group's. Every message
-- carries its chat's title; the backfill takes it from the group's general room where Fleet saw one.
SELECT set_config('fleet.project_ids', '*', true);
ALTER TABLE telegram_room ADD COLUMN IF NOT EXISTS chat_title text NOT NULL DEFAULT '';
UPDATE telegram_room SET chat_title = title WHERE thread_id = 0 AND kind <> 'private' AND chat_title = '' AND title <> '';
UPDATE telegram_room t SET chat_title = g.title FROM telegram_room g
WHERE g.chat_id = t.chat_id AND g.thread_id = 0 AND t.thread_id <> 0 AND t.chat_title = '' AND g.title <> '';
-- Before topics kept their names (89241b7b), every topic message set its room's title to the group's, so topic rooms
-- read as the general chat ("PilotQ TEST" several times). Such a title is unknown until the topic's name is seen.
UPDATE telegram_room SET title = '' WHERE thread_id <> 0 AND title <> '' AND title = chat_title;
