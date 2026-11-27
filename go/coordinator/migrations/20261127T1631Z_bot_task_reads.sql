-- Bot task pages aggregate only visible, live agent attempts for the selected task.
CREATE INDEX IF NOT EXISTS execution_task_live_agent_state ON execution(task_id, state)
 WHERE task_id IS NOT NULL AND kind = 'agent_run' AND tombstoned_at IS NULL;
