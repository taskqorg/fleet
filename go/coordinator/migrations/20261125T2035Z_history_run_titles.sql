-- History (docs/designs/fleet-history.md): entries written before fleet_activity() named untitled runs and keyed
-- experiment trials by experiment. A run entry with no title takes the name of what ran it (Host job, <pipeline> run,
-- <harness> run); a trial without a task folds by its experiment and takes its title. Nothing is deleted.
SELECT set_config('fleet.project_ids', '*', true);
SELECT set_config('fleet.deleted', 'on', true);
UPDATE activity a SET group_key = 'experiment:' || x.id, title = coalesce(x.lab_group, x.goal, x.kind::text) || ' trial'
FROM experiment x
WHERE a.kind = 'run' AND a.experiment_id = x.id AND a.task_id IS NULL;
UPDATE activity a SET title = CASE e.kind WHEN 'host_job' THEN 'Host job' WHEN 'pipeline_run' THEN coalesce(e.pipeline_id, 'Pipeline') || ' run' ELSE coalesce(e.harness, 'Agent') || ' run' END
FROM execution e
WHERE a.kind = 'run' AND a.title = '' AND a.subject = 'execution:' || e.id;
