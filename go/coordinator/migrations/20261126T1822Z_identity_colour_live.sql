-- 20261126T1801Z_identity_colour counted retired projects (prod had 20, 6 live), so live projects shared colours
-- while others were free (ABT and CBZ both rose). Live projects and bots, oldest first, take the palette in order
-- again; the colours shipped an hour earlier and no one chose them. fleet_identity_assign now counts live ones only.
WITH live AS (
 SELECT d.id, row_number() OVER (ORDER BY min(c.set_at) NULLS LAST, d.id) AS n
 FROM definition d LEFT JOIN project_code c ON c.project_id = d.id
 WHERE d.kind = 'project' AND d.retired_at IS NULL GROUP BY d.id)
UPDATE identity_colour i SET colour = (ARRAY['rose', 'violet', 'sky', 'teal', 'lime', 'sand', 'coral', 'slate'])[((live.n - 1) % 8) + 1]
FROM live WHERE i.kind = 'project' AND i.id = live.id;
WITH live AS (SELECT b.id, row_number() OVER (ORDER BY b.id) AS n FROM bot b WHERE b.tombstoned_at IS NULL)
UPDATE identity_colour i SET colour = (ARRAY['rose', 'violet', 'sky', 'teal', 'lime', 'sand', 'coral', 'slate'])[((live.n - 1) % 8) + 1]
FROM live WHERE i.kind = 'bot' AND i.id = live.id;
