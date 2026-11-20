-- Price table (prices.go, docs/designs/fleet-analytics.md, Price table). Append-only.

-- name: PriceInsert :one
INSERT INTO price_entry(id,subject,unit,price,currency,source,source_ref,observed_at,effective_from,created_by)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
RETURNING *;

-- name: PricesList :many
SELECT * FROM price_entry WHERE sqlc.arg(subject)::text='' OR subject=sqlc.arg(subject)::text
ORDER BY subject,unit,effective_from DESC,seq DESC;

-- name: PriceEntries :many
-- The whole table, newest in force first, read once per estimate.
SELECT * FROM price_entry ORDER BY effective_from DESC,seq DESC;

-- name: PriceConnectionProvider :one
-- The provider of an API connection, absent when the connection is not visible.
SELECT coalesce(data->>'provider','')::text AS provider FROM document WHERE id=$1 AND kind='api-connection';
