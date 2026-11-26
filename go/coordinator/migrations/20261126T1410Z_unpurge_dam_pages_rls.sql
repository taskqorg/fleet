-- 20261126T1249Z_unpurge_dam_pages ran without the project scope, so the FORCE ROW LEVEL SECURITY of purged_hash hid every
-- row from its DELETE and the 6 purge marks of project dam stayed (owner query, 2026-11-26: `_` 7, `dam` 6). dam then
-- refused its own page images ("Content was purged") and group 3 hung at prompt. This removes exactly those dam marks;
-- the 7 project-less marks the owner asked for stay. Safe to run again.
SELECT set_config('fleet.project_ids', '*', true);
DELETE FROM purged_hash
WHERE project_id = 'dam'
  AND reason LIKE 'owner decision 2026-11-26: pipeline host-job files saved without a project between #1108 and #1169%';
