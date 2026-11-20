-- The execution state machine (execution_states.go, docs/designs/fleet-standard.md S5): the one insert and the one
-- state move of an execution row.

-- name: ExecutionInsert :one
-- A new execution in its first state; nothing when its id or request_id exists.
INSERT INTO execution(id,kind,state,observation,project_id,task_id,experiment_id,variant,parent_execution_id,session_id,account_id,host_id,
 harness,model,config_ref,started_at,ended_at,data,directed_by,profile_id,profile_revision,pipeline_id,pipeline_revision,step_id,request_id,system_actor,definitions)
VALUES(sqlc.arg(id),sqlc.arg(kind),sqlc.arg(state),coalesce(sqlc.narg(observation)::observation,'confirmed'),sqlc.narg(project_id),sqlc.narg(task_id),
 sqlc.narg(experiment_id),sqlc.narg(variant),sqlc.narg(parent_execution_id),sqlc.narg(session_id),sqlc.narg(account_id),sqlc.narg(host_id),
 sqlc.narg(harness),sqlc.narg(model),sqlc.narg(config_ref),sqlc.narg(started_at),sqlc.narg(ended_at),coalesce(sqlc.narg(data)::jsonb,'{}'),
 sqlc.narg(directed_by),sqlc.narg(profile_id),sqlc.narg(profile_revision),sqlc.narg(pipeline_id),sqlc.narg(pipeline_revision),sqlc.narg(step_id),
 sqlc.narg(request_id),sqlc.narg(system_actor),coalesce(sqlc.narg(definitions)::jsonb,'[]'))
ON CONFLICT DO NOTHING RETURNING id;

-- name: ExecutionLockState :one
SELECT kind,state FROM execution WHERE id=$1 FOR UPDATE;

-- name: ExecutionMove :exec
-- One move: a success clears the problem; an end keeps a known end time, else now; a reopened step loses its end.
UPDATE execution SET state=sqlc.arg(state),
 observation=coalesce(sqlc.narg(observation)::observation,observation),
 problem=CASE WHEN sqlc.arg(state)::execution_state='succeeded' THEN NULL WHEN sqlc.arg(set_problem)::boolean THEN sqlc.narg(problem)::text ELSE problem END,
 ended_at=CASE WHEN sqlc.arg(ends)::boolean THEN coalesce(ended_at,now()) WHEN sqlc.arg(reopens)::boolean THEN NULL ELSE ended_at END
WHERE id=sqlc.arg(id);
