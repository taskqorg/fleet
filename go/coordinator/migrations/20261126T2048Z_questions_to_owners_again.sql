-- 20261126T2003Z_questions_to_owners_only ran under 1.261126.116, which was rolled back to .113; .113 lacks
-- addressToManagingBot, so a question it asked in a bot's task (live: Q-FLT-505, "fail task" on T-FLT-79 of HAL0) reached
-- people. The same readdress again: an open question in a task a bot created or manages goes to that bot.
UPDATE question q SET addressed_to = 'bot:' || bt.bot_id
FROM (SELECT DISTINCT ON (task_id) task_id, bot_id FROM bot_task WHERE link_kind IN ('created', 'managed') AND tombstoned_at IS NULL ORDER BY task_id, created_at) bt
WHERE q.state = 'open' AND q.task_id = bt.task_id AND q.kind IN ('operator_decision', 'agent_question')
 AND coalesce(q.addressed_to, '') NOT LIKE 'bot:%'
 AND NOT EXISTS (SELECT 1 FROM execution e WHERE q.asked_by = 'execution:' || e.id AND e.bot_id = bt.bot_id);
