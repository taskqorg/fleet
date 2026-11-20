-- Telemetry (telemetry.go): the executions a host's span batch names, and an execution's trace context.

-- name: TelemetryHostExecutions :many
-- The executions of ids that run on host_id; their work identity is what a span is attributed to.
SELECT id, project_id, task_id, experiment_id, account_id FROM execution WHERE id = ANY(@ids::text[]) AND host_id = @host_id::text;

-- name: TelemetryExecutionTraceparent :one
SELECT coalesce(data->>'traceparent', '')::text AS traceparent FROM execution WHERE id = $1;
