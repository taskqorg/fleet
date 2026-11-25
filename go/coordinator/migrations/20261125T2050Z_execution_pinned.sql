-- The runs that pinned definitions (DefUsageRecent: Library's tree, system_changes): about 2 300 of 250 000 runs a
-- month. DefUsageRecent read every run of the window and dropped those without definitions (326 ms under RLS on prod,
-- db_explain 2026-11-26); this partial index holds the pinned runs by start, so the window reads only them.
CREATE INDEX IF NOT EXISTS execution_pinned ON execution (started_at) WHERE jsonb_typeof(definitions) = 'array' AND definitions <> '[]'::jsonb;
