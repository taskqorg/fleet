-- Budgets (budgets.go, docs/designs/fleet-people-access.md §9).

-- name: BudgetUpsert :one
INSERT INTO budget(id,scope,scope_id,unit,"limit",period,action,set_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8)
ON CONFLICT(id) DO UPDATE SET scope=excluded.scope,scope_id=excluded.scope_id,unit=excluded.unit,"limit"=excluded."limit",
 period=excluded.period,action=excluded.action,set_by=excluded.set_by,set_at=now()
RETURNING *;

-- name: BudgetGet :one
SELECT * FROM budget WHERE id=$1;

-- name: BudgetsList :many
SELECT * FROM budget ORDER BY set_at;

-- name: BudgetsCovering :many
-- The budgets of a project and of a person.
SELECT * FROM budget WHERE (scope='project' AND scope_id=sqlc.arg(project_id)::text) OR (scope='person' AND scope_id=sqlc.arg(person)::text);

-- name: BudgetsConnectionStop :many
SELECT * FROM budget WHERE scope='connection' AND action='stop';

-- name: BudgetDelete :execrows
DELETE FROM budget WHERE id=$1;

-- name: BudgetRaise :exec
UPDATE budget SET "limit"=$2,set_by=$3,set_at=now() WHERE id=$1;

-- name: BudgetSpendConnectionCalls :one
-- Every call on a connection (Fleet's own included) since the start of the period; period NULL is all time.
SELECT count(*)::float8 AS spend FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id
WHERE e.kind='tool_run' AND e.data->>'connection'=sqlc.arg(connection)::text
 AND (sqlc.narg(period)::text IS NULL OR (e.data->>'startedAt')::bigint >= (extract(epoch from now() - ('1 '||sqlc.narg(period)::text)::interval)*1000)::bigint);

-- name: BudgetSpendConnectionAmount :one
-- The measured amount of a unit on a connection since the start of the period.
SELECT coalesce(sum((t.connection_cost->>'amount')::numeric) FILTER (WHERE t.connection_cost->>'unit'=sqlc.arg(unit)::text),0)::float8 AS spend
FROM execution e LEFT JOIN execution_totals t ON t.execution_id=e.id
WHERE e.kind='tool_run' AND e.data->>'connection'=sqlc.arg(connection)::text
 AND (sqlc.narg(period)::text IS NULL OR (e.data->>'startedAt')::bigint >= (extract(epoch from now() - ('1 '||sqlc.narg(period)::text)::interval)*1000)::bigint);

-- name: BudgetSpendPerson :one
-- A person's work (not Fleet's own calls) since the start of the period: wall time and tokens.
SELECT coalesce(sum(coalesce(t.wall_ms,0)),0)::float8 AS wall_ms, coalesce(sum(coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)),0)::float8 AS tokens
FROM execution e JOIN execution_totals t ON t.execution_id=e.id
WHERE e.directed_by=sqlc.arg(person)::text AND e.system_actor IS NULL
 AND (sqlc.narg(period)::text IS NULL OR e.started_at >= now() - ('1 '||sqlc.narg(period)::text)::interval);

-- name: BudgetSpendProject :one
-- A project's work (not Fleet's own calls) since the start of the period: wall time and tokens.
SELECT coalesce(sum(coalesce(t.wall_ms,0)),0)::float8 AS wall_ms, coalesce(sum(coalesce(t.tokens_input,0)+coalesce(t.tokens_output,0)),0)::float8 AS tokens
FROM execution e JOIN execution_totals t ON t.execution_id=e.id
WHERE e.project_id=sqlc.arg(project_id)::text AND e.system_actor IS NULL
 AND (sqlc.narg(period)::text IS NULL OR e.started_at >= now() - ('1 '||sqlc.narg(period)::text)::interval);

-- name: BudgetAskInsert :exec
-- An operator decision asking to raise a budget (questions table; budgetAsk in data names the budget).
INSERT INTO question(id,kind,project_id,text,options,asked_by,data) VALUES($1,'operator_decision',$2,$3,$4,$5,$6);

