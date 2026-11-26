-- 2026-11-26: a purge of project-less pipeline files (artifact_purge with projectId null, which meant every project) also
-- marked the same page images purged in project dam, so dam would refuse them again ("Content was purged"). The owner
-- asked to delete only the project-less files; dam keeps the right to store this content. Safe to run again.
DELETE FROM purged_hash
WHERE project_id <> '_'
  AND reason LIKE 'owner decision 2026-11-26: pipeline host-job files saved without a project between #1108 and #1169%';
