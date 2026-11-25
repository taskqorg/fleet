-- History (docs/designs/fleet-history.md): a coordinator problem question (one with a cause) is system upkeep and
-- groups by its rule, as fleet_activity() now writes it. Entries written before take that section and key, so a sweep
-- of per-run questions folds into one entry and leaves Live's Just now (section work). Nothing is deleted.
SELECT set_config('fleet.project_ids', '*', true);
SELECT set_config('fleet.deleted', 'on', true);
UPDATE activity a SET section = 'system',
 group_key = 'cause:' || coalesce(q.data->>'rule', regexp_replace(coalesce(q.cause, q.data->>'cause'), ':[^:]*[0-9][^:]*', ':*', 'g'))
FROM question q
WHERE a.kind = 'question' AND a.subject = 'question:' || q.id AND coalesce(q.cause, q.data->>'cause') IS NOT NULL;
