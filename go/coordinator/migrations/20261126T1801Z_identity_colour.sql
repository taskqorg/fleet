-- The automatic identity colour of each project and bot (OR-72, docs/design-system/controls/project-tile.md): stored
-- once, the least-used colour of its kind at the time, so two projects do not share a colour while one is free. A
-- project's settings color overrides it. New rows get theirs from the trigger fleet_identity_assign (schema/).
-- The colours are contracts/vocabulary.json identity.colors, in that order.
CREATE TABLE IF NOT EXISTS identity_colour(
 kind text NOT NULL CHECK (kind IN ('project', 'bot')),
 id text NOT NULL,
 colour text NOT NULL CHECK (colour IN ('rose', 'violet', 'sky', 'teal', 'lime', 'sand', 'coral', 'slate')),
 assigned_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (kind, id)
);
-- Existing projects and bots, oldest first, take the colours in palette order: the least-used colour each time.
INSERT INTO identity_colour(kind, id, colour)
SELECT 'project', d.id, (ARRAY['rose', 'violet', 'sky', 'teal', 'lime', 'sand', 'coral', 'slate'])[((row_number() OVER (ORDER BY min(c.set_at) NULLS LAST, d.id) - 1) % 8) + 1]
FROM definition d LEFT JOIN project_code c ON c.project_id = d.id
WHERE d.kind = 'project' GROUP BY d.id
ON CONFLICT DO NOTHING;
INSERT INTO identity_colour(kind, id, colour)
SELECT 'bot', b.id, (ARRAY['rose', 'violet', 'sky', 'teal', 'lime', 'sand', 'coral', 'slate'])[((row_number() OVER (ORDER BY b.id) - 1) % 8) + 1]
FROM bot b
ON CONFLICT DO NOTHING;
