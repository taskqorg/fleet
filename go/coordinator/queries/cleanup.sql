-- Delete and purge (cleanup.go).

-- name: ReferenceKeys :many
-- Every foreign-key column of the schema, its parent table and whether its row goes with the parent (ON DELETE CASCADE).
SELECT c.conrelid::regclass::text AS child, a.attname::text AS col, c.confrelid::regclass::text AS parent,
  c.confdeltype = 'c' AS cascades
FROM pg_catalog.pg_constraint c JOIN pg_catalog.pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
WHERE c.contype = 'f' AND c.connamespace = 'public'::regnamespace
ORDER BY 1, 2;
