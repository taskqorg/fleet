-- The question state machine (question_states.go, docs/designs/fleet-standard.md S5).

-- name: QuestionLockState :one
SELECT state FROM question WHERE id=$1 FOR UPDATE;

-- name: QuestionAnswer :exec
UPDATE question SET state='answered',answer=sqlc.arg(answer),answered_by=sqlc.narg(answered_by),
 answered_at=CASE WHEN sqlc.arg(answered_now)::boolean THEN now() ELSE sqlc.narg(answered_at) END WHERE id=sqlc.arg(id);

-- name: QuestionWithdraw :exec
-- A withdrawal, with its record (reason, actor, at) when one is given.
UPDATE question SET state='withdrawn',data=CASE WHEN sqlc.narg(withdrawal)::jsonb IS NULL THEN data ELSE coalesce(data,'{}'::jsonb)||jsonb_build_object('withdrawal',sqlc.narg(withdrawal)::jsonb) END WHERE id=sqlc.arg(id);

-- name: QuestionsWithdrawOpen :many
-- Withdraws the open questions of ids; the ids withdrawn.
UPDATE question SET state='withdrawn' WHERE id=ANY(sqlc.arg(ids)::text[]) AND state='open' RETURNING id;

-- name: QuestionRowRecord :one
-- The question's row as a record keyed by column name (questionRecord: the answer checks and the answer replies).
SELECT to_jsonb(qu)::jsonb FROM question qu WHERE qu.id=$1;

-- name: QuestionExists :one
SELECT EXISTS (SELECT 1 FROM question WHERE id=$1);

-- name: QuestionRestPut :exec
-- An agent question record's fields without a column, and its project once known.
UPDATE question SET data= @data, project_id=coalesce(project_id, sqlc.narg(project_id)) WHERE id= @id;

-- name: AgentQuestionByID :one
SELECT * FROM question WHERE id=$1 AND kind='agent_question';

-- name: AgentQuestions :many
-- Agent question rows matching optional column filters.
SELECT * FROM question WHERE kind='agent_question'
 AND (sqlc.narg(execution_id)::text IS NULL OR execution_id=sqlc.narg(execution_id)::text)
 AND (sqlc.narg(session_id)::text IS NULL OR session_id=sqlc.narg(session_id)::text)
 AND (sqlc.narg(project_id)::text IS NULL OR project_id=sqlc.narg(project_id)::text)
 AND (sqlc.narg(request_id)::text IS NULL OR request_id=sqlc.narg(request_id)::text)
 AND (sqlc.narg(state)::text IS NULL OR state::text=sqlc.narg(state)::text)
 ORDER BY seq;
