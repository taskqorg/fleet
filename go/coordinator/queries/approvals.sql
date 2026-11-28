-- Approval cards (approvals.go; docs/designs/fleet-telegram.md, Approvals): agent questions whose data.approval holds
-- the scope of a radical action.

-- name: ApprovalByRequest :one
-- A session's newest card for one requestId of approval_ask.
SELECT id, state::text AS state, coalesce(answer, '')::text AS answer, coalesce(answered_by, '')::text AS answered_by, (data->'approval')::jsonb AS approval
FROM question WHERE session_id = @session_id AND data->'approval'->>'requestId' = @request_id::text
ORDER BY asked_at DESC LIMIT 1;

-- name: ApprovalOf :one
-- The scope of an approval card and its project (none: the question is no approval card).
SELECT (data->'approval')::jsonb AS approval, coalesce(q.project_id, s.project_id, '')::text AS project_id
FROM question q LEFT JOIN session s ON s.id = q.session_id WHERE q.id = $1 AND q.data ? 'approval';

-- name: ApprovalOpenForSession :many
-- Open approval cards of a conversation: free-text approval counts only when there is exactly one.
SELECT id FROM question WHERE session_id = $1 AND state = 'open' AND data ? 'approval' ORDER BY asked_at;

-- name: LatestDesignOffer :one
-- The newest sealed revision owns admission even after rework or withdrawal.
SELECT * FROM question WHERE task_id=sqlc.arg(task_id) AND data ? 'designOffer'
ORDER BY (data->'designOffer'->>'revision')::bigint DESC LIMIT 1;

-- name: DesignOfferRequest :one
SELECT * FROM question WHERE task_id=sqlc.arg(task_id) AND execution_id=sqlc.arg(execution_id)
AND data->'designOffer'->>'requestId'=sqlc.arg(request_key)::text ORDER BY (data->'designOffer'->>'revision')::bigint DESC LIMIT 1;

-- name: DesignOfferArtifact :one
-- Share-lock the retained original while sealing or deciding. Legacy IDs are not packet identities.
SELECT * FROM artifact WHERE id=$1 AND tombstoned_at IS NULL AND state='stored' FOR SHARE;

-- name: AssignDesignWriter :exec
-- Mutable build ownership is outside the sealed designOffer. The task lock serializes retries and replacements.
UPDATE question SET data=jsonb_set(data,'{designBuildExecution}',to_jsonb(sqlc.arg(execution_id)::text))
WHERE id=sqlc.arg(question_id) AND state='answered' AND data ? 'designOffer';
