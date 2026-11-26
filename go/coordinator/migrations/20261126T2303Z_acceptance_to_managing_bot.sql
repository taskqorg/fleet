-- A result's acceptance in a task a bot created or manages is that bot's (addressToManagingBot, 2026-11-27: Q-FLT-513
-- asked the owner to accept HAL0's fix for its own problem P-83). The open ones so far go to that bot.
UPDATE question q SET addressed_to = 'bot:' || bt.bot_id
FROM (SELECT DISTINCT ON (task_id) task_id, bot_id FROM bot_task WHERE link_kind IN ('created', 'managed') AND tombstoned_at IS NULL ORDER BY task_id, created_at) bt
WHERE q.state = 'open' AND q.task_id = bt.task_id AND q.kind = 'acceptance' AND coalesce(q.addressed_to, '') NOT LIKE 'bot:%';
