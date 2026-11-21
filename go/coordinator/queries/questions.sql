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
