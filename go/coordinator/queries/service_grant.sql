-- name: ServiceGrantGet :one
SELECT * FROM service_grant WHERE name=$1 AND project_id=$2;

-- name: ServiceGrantLock :one
SELECT * FROM service_grant WHERE name=$1 FOR UPDATE;

-- name: ServiceGrantDue :many
SELECT name, attempts, check_sequence FROM service_grant
WHERE approved_at IS NOT NULL AND token_id IS NOT NULL AND next_attempt_at<=sqlc.arg(due)::timestamptz
ORDER BY next_attempt_at LIMIT sqlc.arg(lim)::int;

-- name: ServiceGrantAsk :execrows
INSERT INTO service_grant(name, project_id, request_id, question_id, asked_by)
VALUES($1,$2,$3,$4,$5) ON CONFLICT(name) DO NOTHING;

-- name: ServiceGrantDecide :exec
UPDATE service_grant SET approved_at=CASE WHEN sqlc.arg(approved)::boolean THEN now() END,
 declined_at=CASE WHEN NOT sqlc.arg(approved)::boolean THEN now() END
WHERE question_id=$1 AND approved_at IS NULL AND declined_at IS NULL;

-- name: ServiceGrantBootstrap :exec
INSERT INTO service_grant(name, project_id, request_id, asked_by, approved_at)
VALUES($1,$2,$3,$4,now()) ON CONFLICT(name) DO NOTHING;

-- name: ServiceGrantEnroll :exec
UPDATE service_grant SET account_id=$2, application_id=$3, token_id=$4, policy_id=$5, enrollment_reference=$6 WHERE name=$1;

-- name: ServiceGrantPause :exec
UPDATE service_grant SET paused=$2 WHERE name=$1;

-- name: ServiceGrantBeginCheck :exec
UPDATE service_grant SET check_sequence=check_sequence+1, check_started_at=$2 WHERE name=$1;

-- name: ServiceGrantObserve :exec
UPDATE service_grant SET checked_at=$2, provider_check=$3, binding_check=$4,
 client_id=coalesce(client_id, sqlc.narg(client_id)::text),
 expires_at=coalesce(sqlc.narg(expires_at)::timestamptz,expires_at),
 provider_last_seen_at=greatest(sqlc.narg(provider_last_seen_at)::timestamptz,provider_last_seen_at),
 adopted_at=CASE WHEN sqlc.arg(adopted)::boolean THEN coalesce(adopted_at,$2) ELSE adopted_at END
WHERE name=$1;

-- name: ServiceGrantFinishCheck :exec
UPDATE service_grant SET check_started_at=NULL WHERE name=$1;

-- name: ServiceGrantRequestRenewal :exec
INSERT INTO service_grant_renewal(grant_name,request_id,requested_by)
VALUES($1,$2,$3);

-- name: ServiceGrantGetRenewal :one
SELECT * FROM service_grant_renewal WHERE grant_name=$1 AND request_id=$2;

-- name: ServiceGrantLatestRenewal :one
SELECT * FROM service_grant_renewal WHERE grant_name=$1 ORDER BY requested_at DESC LIMIT 1;

-- name: ServiceGrantActiveRenewal :one
SELECT * FROM service_grant_renewal WHERE grant_name=$1 AND completed_at IS NULL;

-- name: ServiceGrantDispatchRenewal :exec
UPDATE service_grant_renewal SET baseline_expires_at=$3,baseline_secret_version=$4,dispatched_at=$5
WHERE grant_name=$1 AND request_id=$2 AND completed_at IS NULL AND dispatched_at IS NULL;

-- name: ServiceGrantAckRenewal :exec
UPDATE service_grant_renewal SET acknowledged_expiry=$3,detail=$4
WHERE grant_name=$1 AND request_id=$2 AND completed_at IS NULL;

-- name: ServiceGrantFinishRenewal :exec
UPDATE service_grant_renewal SET completed_at=now(),result=$3,detail=$4,verified_expiry=$5
WHERE grant_name=$1 AND request_id=$2 AND completed_at IS NULL;
