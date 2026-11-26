-- Needs you holds only what a person alone can give (OR-71): an open question in a task a bot created or manages goes
-- to that bot (addressed_to bot:<id>), and an open notice of a setting an agent changed is withdrawn (the change stays
-- in the journal). Secret requests and acceptance stay with people.
UPDATE question q SET addressed_to = 'bot:' || bt.bot_id
FROM (SELECT DISTINCT ON (task_id) task_id, bot_id FROM bot_task WHERE link_kind IN ('created', 'managed') AND tombstoned_at IS NULL ORDER BY task_id, created_at) bt
WHERE q.state = 'open' AND q.task_id = bt.task_id AND q.kind IN ('operator_decision', 'agent_question')
 AND coalesce(q.addressed_to, '') NOT LIKE 'bot:%'
 AND NOT EXISTS (SELECT 1 FROM execution e WHERE q.asked_by = 'execution:' || e.id AND e.bot_id = bt.bot_id);
UPDATE question SET state = 'withdrawn'
WHERE state = 'open' AND data->>'notice' = 'owner-override' AND asked_by IN ('site-agents');
