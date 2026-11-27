-- The Runners detail list never includes tool calls or pipeline wrappers.
CREATE INDEX IF NOT EXISTS execution_runner_started ON execution(started_at)
 WHERE kind NOT IN ('pipeline_run','pipeline_step','tool_run');
CREATE INDEX IF NOT EXISTS execution_runner_ended ON execution(ended_at)
 WHERE kind NOT IN ('pipeline_run','pipeline_step','tool_run');
