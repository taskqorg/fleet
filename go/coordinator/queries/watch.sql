-- The reads of a trigger's change check (watch.go, triggers.go).

-- name: TaskCancelled :one
SELECT EXISTS (SELECT 1 FROM task WHERE id=$1 AND state='cancelled');

-- name: SessionExecutionIDs :many
SELECT id FROM execution WHERE session_id = ANY(@session_ids::text[]);

-- name: ChildSessionIDs :many
SELECT id FROM session WHERE created_by_session_id = ANY(@session_ids::text[]);

-- name: TaskWatchState :one
-- NULL marks pre-upgrade rows. Only the first row for that task in its latest state transaction can serve as a
-- legacy transition. The old log did not distinguish a task write from derived or dependency writes.
SELECT coalesce(c.task_watch_state, CASE WHEN t.pause_reason = 'held' THEN 'held' ELSE t.state::text END)::text AS state,
       CASE WHEN c.task_watch_state IS NOT NULL THEN c.task_watch_state <> ''
            WHEN c.at = t.state_at AND c.op <> 'D' THEN NOT EXISTS (
              SELECT 1 FROM change earlier
              WHERE earlier.xid = c.xid AND earlier.seq < c.seq AND earlier.kind = 'task'
                AND earlier.id = c.id AND earlier.at = c.at)
            ELSE false END::bool AS state_changed,
       (c.task_watch_state IS NULL)::bool AS legacy, t.state_at
FROM task t JOIN change c ON c.seq = sqlc.arg(seq)::bigint AND c.kind = 'task' AND c.id = t.id
WHERE t.id = sqlc.arg(id)::text;

-- name: LegacyTaskWakeDelivered :one
-- A receipt reporting this state after entry covers a legacy row. A receipt for an earlier entry into the same
-- state does not. For promoted wakes use queue admission time. The generated change list is last for session triggers
-- and first for bot triggers.
SELECT EXISTS (
 SELECT 1 FROM trigger t
 JOIN session s ON (s.id = t.session_id OR s.bot_id = t.bot_id)
 CROSS JOIN LATERAL (
   SELECT m.text FROM message m
   WHERE m.session_id = s.id AND m.payload->>'triggerId' = t.id AND m.source = 'trigger'
     AND m.updated_at >= @state_at
     AND NOT EXISTS (SELECT 1 FROM queued_message qm WHERE qm.session_id = m.session_id AND qm.request_id = m.request_id)
   UNION ALL
   SELECT qm.text FROM queued_message qm
   WHERE qm.session_id = s.id AND qm.payload->>'triggerId' = t.id AND qm.source = 'trigger'
     AND qm.state IN ('queued', 'started') AND qm.created_at >= @state_at
 ) receipt
 WHERE (t.session_id = sqlc.narg(session_id)::text OR t.bot_id = sqlc.narg(bot_id)::text)
   AND @signal_line::text = ANY(string_to_array(
     substring(split_part(receipt.text, E'\n', CASE WHEN t.session_id IS NULL THEN 1 ELSE -1 END)
       from '^Fleet: [0-9]+ changes in (?:your focus|project [^:]+): (.*)$'), '; '))
);

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
