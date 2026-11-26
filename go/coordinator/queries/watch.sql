-- The reads of a trigger's change check (watch.go, triggers.go).

-- name: TaskCancelled :one
SELECT EXISTS (SELECT 1 FROM task WHERE id=$1 AND state='cancelled');

-- name: SessionExecutionIDs :many
SELECT id FROM execution WHERE session_id = ANY(@session_ids::text[]);

-- name: ChildSessionIDs :many
SELECT id FROM session WHERE created_by_session_id = ANY(@session_ids::text[]);

-- name: TaskWatchState :one
-- A task's state and whether change row seq recorded a change of it: state_at is the time of the task's latest state,
-- pause or wait change, change.at the writing transaction's now(), so an edit that keeps the state (a title, a link, a
-- dependency) has state_at before its change and wakes nobody.
SELECT t.state::text AS state, t.pause_reason, (t.state_at >= c.at)::bool AS state_changed
FROM task t JOIN change c ON c.seq = sqlc.arg(seq)::bigint WHERE t.id = sqlc.arg(id)::text;

-- name: OpenQuestionTask :one
SELECT coalesce(task_id, '')::text AS task_id FROM question WHERE id=$1 AND state='open';

-- name: ExecutionWatchState :one
-- A failed or unconfirmed run, as the Needs you list counts it: not dismissed, not cancelled or abandoned, and not a
-- pipeline step whose run failed already (the run is the one entry). If the current signal has a recorded transition,
-- let that change carry it instead of attributing it to an older unmarked row.
SELECT signal.s::text AS s FROM execution e
CROSS JOIN LATERAL (SELECT fleet_execution_watch_signal(e.state, e.observation, e.problem) AS s) signal
WHERE e.id = @id AND e.kind <> 'tool_run' AND signal.s <> ''
 AND NOT EXISTS (SELECT 1 FROM problem_dismissal x WHERE x.execution_id = e.id)
 AND NOT (e.kind = 'pipeline_step' AND EXISTS (SELECT 1 FROM execution r WHERE r.kind = 'pipeline_run' AND r.problem IS NOT NULL
   AND r.id = coalesce(e.parent_execution_id, split_part(e.id, ':step:', 1))))
 AND (signal.s = sqlc.narg(watch_signal)::text OR (sqlc.narg(watch_signal)::text IS NULL AND NOT EXISTS (
   SELECT 1 FROM change c WHERE c.id = e.id AND c.execution_watch_signal <> '' AND c.execution_watch_signal = signal.s
 )));

-- name: LegacyExecutionWakeDelivered :one
-- Old change rows carry no transition identity. Recognize the generated change line only in a trigger message
-- delivered to this target, including messages from another trigger sharing this target.
SELECT EXISTS (
 SELECT 1 FROM trigger t
 JOIN session s ON s.id = t.session_id OR s.bot_id = t.bot_id
 JOIN message m ON m.session_id = s.id AND m.payload->>'triggerId' = t.id
 WHERE (t.session_id = sqlc.narg(session_id)::text OR t.bot_id = sqlc.narg(bot_id)::text)
   AND m.source = 'trigger'
   AND @signal_line::text = ANY(string_to_array(
     substring(split_part(m.text, E'\n', -1) from '^Fleet: [0-9]+ changes in (?:your focus|project [^:]+): (.*)$'), '; '))
);

-- name: WatchSession :one
-- The session fields a watch reads: its project, task, director and payload (deliveryId, focus).
SELECT project_id, task_id, directed_by, payload FROM session WHERE id=$1;
