-- name: TaskWakeDirty :exec
INSERT INTO task_wake(id,kind,source_id,task_id,project_id)
SELECT 'task:'||t.id,'task',t.id,t.id,t.project_id FROM task t
WHERE t.id=$1 AND t.state IN ('queued','in_progress','waiting','paused')
ON CONFLICT (id) DO UPDATE SET revision=task_wake.revision+1,disposition=NULL;

-- name: TaskWakeAnswer :exec
INSERT INTO task_wake(id,kind,source_id,task_id,project_id)
SELECT 'reply:'||qq.id,'reply',qq.id,coalesce(qq.task_id,s.task_id),coalesce(qq.project_id,s.project_id,t.project_id)
FROM question qq LEFT JOIN session s ON s.id=qq.session_id
LEFT JOIN task t ON t.id=coalesce(qq.task_id,s.task_id)
WHERE qq.id=$1 AND qq.state='answered' AND qq.kind IN ('agent_question','operator_decision')
 AND qq.session_id IS NOT NULL AND qq.asked_by='execution:'||qq.execution_id
ON CONFLICT (id) DO NOTHING;

-- name: TaskWakeBlocker :exec
INSERT INTO task_wake(id,kind,source_id,project_id)
SELECT 'blocker:'||t.id,'blocker',t.id,t.project_id FROM task t WHERE t.id=$1
 AND EXISTS (SELECT 1 FROM task_dependency WHERE depends_on_task_id=t.id AND kind='blocks')
ON CONFLICT (id) DO UPDATE SET revision=task_wake.revision+1,disposition=NULL,cursor_id=NULL;

-- name: TaskWakeDue :many
SELECT id,revision,attempts FROM task_wake
WHERE disposition IS NULL AND next_attempt_at<=sqlc.arg(due_at)
ORDER BY next_attempt_at,id LIMIT $1;

-- name: TaskWakeGet :one
SELECT * FROM task_wake WHERE id=$1 FOR UPDATE;

-- name: TaskWakeFinish :exec
UPDATE task_wake SET disposition=sqlc.arg(disposition) WHERE id=$1;

-- name: TaskWakeReadAnswer :exec
UPDATE task_wake SET disposition='read by asking session' WHERE id=$1 AND kind='reply' AND disposition IS NULL;

-- name: TaskWakeDependents :many
SELECT d.task_id FROM task_dependency d
WHERE d.depends_on_task_id=sqlc.arg(blocker)::text AND d.kind='blocks'
 AND d.task_id>sqlc.arg(after_id)::text
ORDER BY d.task_id LIMIT $1;

-- name: TaskWakeAdvance :exec
UPDATE task_wake SET cursor_id=sqlc.arg(cursor_id) WHERE id=$1;

-- name: TaskWakePendingReply :one
SELECT EXISTS (
 SELECT 1 FROM task_wake w JOIN question qq ON qq.id=w.source_id
 WHERE w.kind='reply' AND w.disposition IS NULL
 AND (w.task_id=sqlc.arg(task_id) OR qq.session_id=sqlc.arg(session_id))
)::boolean;

-- name: TaskWakeAuditAnswers :many
SELECT qq.id FROM question qq JOIN session s ON s.id=qq.session_id
LEFT JOIN task t ON t.id=coalesce(qq.task_id,s.task_id)
WHERE qq.state='answered' AND qq.kind IN ('agent_question','operator_decision')
 AND qq.asked_by='execution:'||qq.execution_id
 AND (t.state IN ('queued','in_progress','waiting','paused')
  OR (qq.task_id IS NULL AND s.task_id IS NULL
   AND qq.execution_id=(SELECT e.id FROM execution e WHERE e.session_id=s.id AND e.kind='agent_run' ORDER BY e.seq DESC LIMIT 1)))
 AND NOT EXISTS (SELECT 1 FROM task_wake w WHERE w.id='reply:'||qq.id)
 AND NOT EXISTS (SELECT 1 FROM execution newer JOIN execution asker ON asker.id=qq.execution_id
  WHERE newer.session_id=qq.session_id AND newer.kind='agent_run' AND newer.seq>asker.seq)
ORDER BY qq.answered_at,qq.id LIMIT $1;

-- name: TaskWakeAuditTasks :many
SELECT t.id FROM task t
WHERE t.state IN ('waiting','in_progress')
 AND NOT EXISTS (SELECT 1 FROM task_wake w WHERE w.id='task:'||t.id AND w.disposition IS NULL)
 AND (
  (t.wait_reason='dependency' AND NOT EXISTS (
   SELECT 1 FROM task_dependency d LEFT JOIN task b ON b.id=d.depends_on_task_id
   WHERE d.task_id=t.id AND d.kind='blocks' AND b.state IN ('queued','in_progress','waiting','paused')
  ))
  OR (
   NOT EXISTS (SELECT 1 FROM question qq WHERE qq.task_id=t.id AND qq.state='open')
   AND (t.wait_reason='awaiting result' OR EXISTS (
    SELECT 1 FROM task_delivery_result dr WHERE dr.task_id=t.id
   ) OR (SELECT e.state='succeeded' AND e.kind='agent_run' AND e.observation='confirmed'
     FROM execution e WHERE e.task_id=t.id AND e.kind NOT IN ('child_agent','tool_run') ORDER BY e.seq DESC LIMIT 1))
   AND NOT EXISTS (SELECT 1 FROM execution e WHERE e.task_id=t.id AND e.kind NOT IN ('child_agent','tool_run')
     AND e.state IN ('preparing','running','waiting'))
   AND NOT EXISTS (SELECT 1 FROM queued_message m JOIN session s ON s.id=m.session_id
     WHERE coalesce(nullif(m.task_id,''),s.task_id)=t.id AND m.state IN ('queued','steering'))
  )
 )
ORDER BY t.updated_at,t.id LIMIT $1;

-- name: TaskUnresolvableDependency :one
SELECT EXISTS (
 SELECT 1 FROM task_dependency d LEFT JOIN task b ON b.id=d.depends_on_task_id
 WHERE d.task_id=$1 AND d.kind='blocks' AND (b.id IS NULL OR b.state IN ('failed','cancelled'))
)::boolean;

-- name: TaskDependencyWouldCycle :one
WITH RECURSIVE reachable(id) AS (
 SELECT unnest(sqlc.arg(prerequisites)::text[])
 UNION
 SELECT d.depends_on_task_id FROM task_dependency d JOIN reachable r ON d.task_id=r.id WHERE d.kind='blocks'
)
SELECT EXISTS (SELECT 1 FROM reachable WHERE id=sqlc.arg(dependent)::text)::boolean;

-- name: TaskDeliveryProofPut :execrows
INSERT INTO task_delivery_result(task_id,project_id,proof,verification_key)
VALUES ($1,$2,$3,$4)
ON CONFLICT (task_id) DO UPDATE SET proof=excluded.proof,verification_key=excluded.verification_key,
 stage_since=CASE WHEN task_delivery_result.verification_key=excluded.verification_key
   AND task_delivery_result.proof->>'delivered'=excluded.proof->>'delivered'
   THEN task_delivery_result.stage_since ELSE now() END
WHERE (task_delivery_result.proof IS DISTINCT FROM excluded.proof
 OR task_delivery_result.verification_key IS DISTINCT FROM excluded.verification_key)
 AND (task_delivery_result.proof->>'url' IS DISTINCT FROM excluded.proof->>'url'
  OR excluded.proof->>'stage'='ambiguous'
  OR coalesce(task_delivery_result.proof->>'mergedAt','')<=coalesce(excluded.proof->>'mergedAt',''));

-- name: TaskDeliveryProof :one
SELECT * FROM task_delivery_result WHERE task_id=$1;

-- name: TaskDeliveryVerificationAdmit :exec
INSERT INTO task_delivery_verification(verification_key,task_id,project_id,execution_id,deadline)
VALUES ($1,$2,$3,$4,$5);

-- name: TaskDeliveryVerificationGet :one
SELECT * FROM task_delivery_verification WHERE verification_key=$1;

-- name: TaskDeliveryOwnedChecks :many
SELECT v.execution_id, v.deadline, v.verification_key FROM task_delivery_verification v
JOIN execution e ON e.id=v.execution_id WHERE v.task_id=$1 AND e.state IN ('preparing','running','waiting');

-- name: TaskDeliveryVerified :one
SELECT EXISTS (
 SELECT 1 FROM execution e WHERE e.task_id=sqlc.arg(task_id) AND e.kind='agent_run'
 AND e.state='succeeded' AND e.observation='confirmed'
 AND e.data->'report'->>'outcome'='succeeded'
 AND e.data->'report'->>'liveCheck'=sqlc.arg(verification_key)::text
 AND e.ended_at>=sqlc.arg(delivered_at)::timestamptz
)::boolean;

-- name: TaskRecoveryStart :exec
INSERT INTO task_recovery(task_id,project_id,deadline) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING;

-- name: TaskRecoveryGet :one
SELECT * FROM task_recovery WHERE task_id=$1;

-- name: TaskRecoveryAdmit :exec
UPDATE task_recovery SET turns=turns+1,execution_id=$2,progress=coalesce(sqlc.arg(progress)::text[],'{}'::text[]) WHERE task_id=$1;

-- name: TaskRecoveryProgress :many
SELECT ('artifact:'||a.id)::text AS milestone FROM artifact a LEFT JOIN execution e ON e.id=a.execution_id
WHERE coalesce(a.task_id,e.task_id)=sqlc.arg(task_id) AND a.state='stored' AND a.role='original'
 AND a.name NOT LIKE 'mcp-lifecycle-%'
UNION
SELECT 'review:'||(value->>'headSha') FROM audit_log
WHERE field='code-review' AND value->>'taskId'=sqlc.arg(task_id) AND value->>'headSha'<>''
UNION
SELECT 'delivery:'||verification_key FROM task_delivery_result WHERE task_id=sqlc.arg(task_id)
UNION
SELECT 'answer:'||id FROM question WHERE task_id=sqlc.arg(task_id) AND state='answered'
 AND kind IN ('agent_question','operator_decision') AND asked_by<>'fleet'
UNION
SELECT 'dependency:'||b.id FROM task_dependency d JOIN task b ON b.id=d.depends_on_task_id
WHERE d.task_id=sqlc.arg(task_id) AND d.kind='blocks' AND b.state='completed';

-- name: TaskAutomaticStartAllowed :one
-- The task lock precedes the execution lock used by handout and stop. Absence means an ordinary (non-automatic) turn.
SELECT coalesce((a.deadline>clock_timestamp() AND t.state IN ('queued','in_progress','waiting')
 AND CASE WHEN a.verification_key IS NULL THEN dr.task_id IS NULL
          ELSE dr.verification_key=a.verification_key AND dr.proof->>'delivered'='true' END
 AND NOT EXISTS (SELECT 1 FROM question qq WHERE qq.task_id=t.id AND qq.state='open'
   AND qq.kind IN ('agent_question','operator_decision','secret'))
 AND NOT EXISTS (SELECT 1 FROM task_dependency d LEFT JOIN task b ON b.id=d.depends_on_task_id
   WHERE d.task_id=t.id AND d.kind='blocks' AND b.state IS DISTINCT FROM 'completed')
),false)::boolean AS allowed,a.deadline
FROM (
 SELECT r.task_id,r.deadline,NULL::text AS verification_key FROM task_recovery r WHERE r.execution_id=sqlc.arg(execution_id)::text
 UNION ALL
 SELECT v.task_id,v.deadline,v.verification_key FROM task_delivery_verification v WHERE v.execution_id=sqlc.arg(execution_id)::text
) a JOIN task t ON t.id=a.task_id
LEFT JOIN task_delivery_result dr ON dr.task_id=t.id
FOR SHARE OF t;

-- name: TaskLifecycleLock :exec
SELECT 1 FROM task WHERE id=$1 FOR UPDATE;

-- name: TaskRecoveryDecision :one
SELECT id FROM question WHERE task_id=sqlc.arg(task_id) AND kind='operator_decision'
 AND state='open' AND data->>'taskRecovery'=sqlc.arg(cause)::text LIMIT 1;

-- name: TaskFailureDecisions :one
SELECT coalesce(array_agg(id),'{}')::text[] FROM question
WHERE task_id=$1 AND state='open' AND kind='operator_decision' AND asked_by='fleet'
 AND data->>'idle'='true' AND NOT (data ? 'taskRecovery');

-- name: TaskAskerQuestion :one
SELECT * FROM question WHERE id=$1 AND kind IN ('agent_question','operator_decision')
 AND asked_by='execution:'||execution_id AND session_id IS NOT NULL;

-- name: TaskWakeQuestionTask :one
SELECT task_id FROM question WHERE id=$1;

-- name: TaskWakeForAction :one
SELECT * FROM task_wake WHERE task_id=$1 AND disposition IS NULL
ORDER BY (kind='reply') DESC,next_attempt_at,id LIMIT 1;

-- name: TaskDependencyPending :one
SELECT d.depends_on_task_id FROM task_dependency d LEFT JOIN task b ON b.id=d.depends_on_task_id
WHERE d.task_id=$1 AND d.kind='blocks' AND b.state IS DISTINCT FROM 'completed' ORDER BY d.depends_on_task_id LIMIT 1;

-- name: TaskDependencyWaive :execrows
DELETE FROM task_dependency WHERE task_id=sqlc.arg(task_id) AND depends_on_task_id=sqlc.arg(blocker) AND kind='blocks';

-- name: TaskFirstPendingMessage :one
SELECT m.id FROM queued_message m JOIN session s ON s.id=m.session_id
WHERE coalesce(nullif(m.task_id,''),s.task_id)=$1 AND m.state IN ('queued','steering') ORDER BY m.seq LIMIT 1;

-- name: TaskWakeHealth :one
SELECT (count(*) FILTER (WHERE disposition IS NULL))::bigint AS pending,
 (count(*) FILTER (WHERE disposition IS NULL AND attempts>0))::bigint AS failing,
 min(next_attempt_at) FILTER (WHERE disposition IS NULL AND next_attempt_at<=now())::timestamptz AS oldest_due_at
FROM task_wake;

-- name: TaskRecoveryReauthorize :exec
UPDATE task_recovery SET generation=generation+1,turns=0,execution_id=NULL,progress='{}',deadline=$2 WHERE task_id=$1;

-- name: TaskGeneratedRecoveryDecisions :many
SELECT id,coalesce(data->>'taskRecovery','')::text AS cause FROM question
WHERE task_id=$1 AND state='open' AND kind='operator_decision' AND asked_by='fleet' AND data ? 'taskRecovery'
ORDER BY seq;

-- name: TaskAutomaticOverdue :many
SELECT e.id FROM (
 SELECT execution_id,deadline FROM task_recovery WHERE execution_id IS NOT NULL AND deadline<=now()
 UNION ALL
 SELECT execution_id,deadline FROM task_delivery_verification WHERE deadline<=now()
) automatic JOIN execution e ON e.id=automatic.execution_id
WHERE e.state IN ('preparing','running','waiting') AND e.data->>'stopRequested' IS DISTINCT FROM 'true'
ORDER BY automatic.deadline,e.id LIMIT $1;
