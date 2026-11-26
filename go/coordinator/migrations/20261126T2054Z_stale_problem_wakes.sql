-- Before the batch conversation (problemsSubject), a bot woke one conversation per problem (subject problem:<id>). On
-- 2026-11-26 HAL0 had 419 Fleet wakes queued in 31 such conversations (18:27-20:02 UTC); promoting them cost seconds
-- each in the dispatch pass. The Fleet wakes queued there are stale: they are cancelled, and every open problem a bot owns
-- counts as unwoken again (woken_at NULL), so its next batch carries each problem once with its current state. A
-- person's message in a problem conversation (problem_talk) stays queued.
UPDATE queued_message m SET state = 'cancelled', updated_at = now()
FROM session s
WHERE m.session_id = s.id AND m.state = 'queued' AND s.bot_id IS NOT NULL AND starts_with(s.subject, 'problem:')
 AND m.sent_by = 'Fleet' AND m.source IN ('trigger', 'wake', 'watch');
UPDATE problem SET woken_at = NULL WHERE state = 'open' AND owner_bot_id IS NOT NULL;
