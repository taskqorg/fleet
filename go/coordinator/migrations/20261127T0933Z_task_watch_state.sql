-- NULL marks legacy task changes. Empty means the row did not record a task-state transition.
ALTER TABLE change ADD COLUMN IF NOT EXISTS task_watch_state text;
