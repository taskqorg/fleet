-- name: YoutrackBindingGet :one
SELECT * FROM youtrack_binding WHERE id = $1;

-- name: YoutrackBindingsForSelector :many
SELECT b.id, b.issue_ids FROM youtrack_binding b
WHERE b.instance = $1 AND b.remote_project_id = $2
AND (NOT b.disconnected OR EXISTS (SELECT 1 FROM youtrack_task l WHERE l.binding_id=b.id));

-- name: YoutrackBindingsActive :many
SELECT sqlc.embed(b) FROM youtrack_binding b
JOIN trigger t ON t.id = b.id AND t.bot_id = b.bot_id
JOIN bot ON bot.id = b.bot_id
JOIN definition p ON p.kind='project' AND p.id=b.project_id AND p.retired_at IS NULL AND p.deleted_at IS NULL
WHERE t.enabled AND t.preset_id = 'youtrack-project' AND NOT bot.paused AND NOT b.disconnected
ORDER BY b.id;

-- name: YoutrackBindingInsert :exec
INSERT INTO youtrack_binding(id, project_id, bot_id, instance, remote_project_id, remote_project_key, issue_ids,
    bot_user_id, reviewer_user_id, reviewer_person, token_sha256, started_ms, cursor_ms)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$12);

-- name: YoutrackBindingAdvance :exec
UPDATE youtrack_binding SET cursor_ms = greatest(cursor_ms, @cursor_ms)
WHERE id = @id AND cursor_ms = @previous_ms AND revision = @revision AND NOT disconnected;

-- name: YoutrackTaskGet :one
SELECT * FROM youtrack_task WHERE instance = $1 AND issue_id = $2;

-- name: YoutrackTaskByID :one
SELECT * FROM youtrack_task WHERE task_id = $1;

-- name: YoutrackTaskReserve :execrows
INSERT INTO youtrack_task(instance,issue_id,binding_id,project_id,task_id,decision_ms,assignment_ms,assignment_authorized,issue_updated_ms)
VALUES ($1,$2,$3,$4,NULL,$5,$6,true,$7) ON CONFLICT DO NOTHING;

-- name: YoutrackTaskAdvance :exec
UPDATE youtrack_task SET decision_ms = @decision_ms WHERE instance = @instance AND issue_id = @issue_id;

-- name: YoutrackTaskObserve :exec
UPDATE youtrack_task SET assignment_ms = @assignment_ms, assignment_authorized = @assignment_authorized,
  issue_updated_ms = @issue_updated_ms
WHERE instance = @instance AND issue_id = @issue_id;

-- name: YoutrackReceiptGet :one
SELECT woke FROM youtrack_receipt WHERE binding_id = $1 AND issue_id = $2 AND remote_event = $3;

-- name: YoutrackReceiptInsert :exec
INSERT INTO youtrack_receipt(binding_id,issue_id,remote_event,project_id,observed_ms,woke)
VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING;

-- name: YoutrackCommentInsert :execrows
INSERT INTO youtrack_comment_work(id,binding_id,issue_id,project_id,milestone,kind)
VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING;

-- name: YoutrackCommentGet :one
SELECT * FROM youtrack_comment_work WHERE id = $1;

-- name: YoutrackCommentsDue :many
SELECT w.id, w.attempts FROM youtrack_comment_work w
JOIN youtrack_task l ON l.binding_id = w.binding_id AND l.issue_id = w.issue_id
JOIN youtrack_binding b ON b.id = w.binding_id
LEFT JOIN task t ON t.id = l.task_id
WHERE w.confirmed_at IS NULL AND w.skip_reason IS NULL AND w.next_attempt_at <= @at
  AND (w.sent_at IS NOT NULL OR NOT b.disconnected AND t.state <> 'cancelled')
ORDER BY w.next_attempt_at, w.id LIMIT @lim;

-- name: YoutrackCommentStart :execrows
UPDATE youtrack_comment_work SET sent_at = @at WHERE id = @id AND sent_at IS NULL;

-- name: YoutrackCommentConfirm :exec
UPDATE youtrack_comment_work SET confirmed_at = @at, comment_id = @comment_id, provider_created_ms = @provider_created_ms
WHERE id = @id;

-- name: YoutrackTaskWait :one
SELECT q.kind, q.addressed_to, w.sent_at, w.provider_created_ms
FROM question q LEFT JOIN youtrack_task t ON t.task_id = q.task_id
LEFT JOIN youtrack_comment_work w ON w.binding_id = t.binding_id AND w.issue_id = t.issue_id AND w.milestone = q.id
WHERE q.task_id = $1 AND q.state = 'open' AND q.kind IN ('secret', 'operator_decision', 'agent_question', 'acceptance')
ORDER BY (q.kind = 'secret') DESC, q.asked_at DESC, q.id DESC LIMIT 1;

-- name: YoutrackCountWake :exec
UPDATE trigger SET fires = fires + 1, last_fire_at = now() WHERE id = $1;

-- name: YoutrackClearTriggerError :exec
UPDATE trigger SET last_error = NULL WHERE id = $1 AND last_error IS NOT NULL;

-- name: YoutrackCommentRestart :exec
UPDATE youtrack_comment_work SET sent_at = NULL WHERE id = @id AND sent_at = @at AND confirmed_at IS NULL;

-- name: YoutrackSessionSubject :one
SELECT subject FROM session WHERE id = $1;

-- name: YoutrackInboxInsert :execrows
INSERT INTO youtrack_inbox(id,binding_id,project_id,issue_id,signal,revision_ms,event_ref,binding_revision)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT DO NOTHING;

-- name: YoutrackInboxGet :one
SELECT * FROM youtrack_inbox WHERE id = $1;

-- name: YoutrackInboxDue :many
SELECT i.id, i.attempts FROM youtrack_inbox i
JOIN trigger t ON t.id = i.binding_id AND t.enabled AND t.preset_id = 'youtrack-project'
JOIN bot b ON b.id = t.bot_id AND NOT b.paused
JOIN youtrack_binding binding ON binding.id = i.binding_id
LEFT JOIN youtrack_task l ON l.binding_id = i.binding_id AND l.issue_id = i.issue_id
LEFT JOIN task work ON work.id = l.task_id
WHERE i.disposition IS NULL AND i.next_attempt_at <= @at
  AND (binding.disconnected OR i.binding_revision <> binding.revision OR work.state IS DISTINCT FROM 'paused' OR work.pause_reason = 'held')
ORDER BY i.next_attempt_at, i.id LIMIT @lim;

-- name: YoutrackInboxFinish :exec
UPDATE youtrack_inbox SET disposition = @disposition
WHERE id = @id AND binding_revision = @binding_revision AND disposition IS NULL;

-- name: YoutrackManagedTask :one
SELECT t.id FROM task t JOIN bot_task bt ON bt.task_id = t.id
WHERE (t.id = @task_ref OR t.code = @task_ref) AND t.project_id = @project_id
  AND bt.bot_id = @bot_id AND bt.link_kind IN ('created','managed');

-- name: YoutrackTaskAdopt :execrows
INSERT INTO youtrack_task(instance,issue_id,binding_id,project_id,task_id,decision_ms,assignment_ms,assignment_authorized,issue_updated_ms)
VALUES ($1,$2,$3,$4,$5,0,0,false,0) ON CONFLICT DO NOTHING;

-- name: YoutrackBindingConfigure :one
UPDATE youtrack_binding SET token_sha256 = @token_sha256, reviewer_user_id = @reviewer_user_id, reviewer_person = @reviewer_person,
  remote_project_key = @remote_project_key, issue_ids = @issue_ids, started_ms = @started_ms, cursor_ms = @cursor_ms,
  disconnected=false, revision=revision+1
WHERE id = @id AND revision = @revision RETURNING revision;

-- name: YoutrackBindingDisconnect :one
UPDATE youtrack_binding SET disconnected=true, revision=revision+1
WHERE id = @id AND revision = @revision RETURNING revision;

-- name: YoutrackCommentSkip :exec
UPDATE youtrack_comment_work SET skip_reason = @skip_reason WHERE id = @id AND confirmed_at IS NULL;

-- name: YoutrackTaskAttach :exec
UPDATE youtrack_task SET task_id = @task_id WHERE instance = @instance AND issue_id = @issue_id;

-- name: YoutrackInboxCarry :exec
UPDATE youtrack_inbox SET binding_revision = @next_revision
WHERE binding_id = @binding_id AND binding_revision = @previous_revision AND disposition IS NULL
  AND (cardinality(@issue_ids::text[]) = 0 OR issue_id = ANY(@issue_ids::text[]));
