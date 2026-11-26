-- A bot's own identity colour and pattern (bot_identity_set, OR-72): '' means automatic (identity_colour) and solid.
ALTER TABLE bot ADD COLUMN IF NOT EXISTS identity_color text NOT NULL DEFAULT '' CHECK (identity_color IN ('', 'rose', 'violet', 'sky', 'teal', 'lime', 'sand', 'coral', 'slate'));
ALTER TABLE bot ADD COLUMN IF NOT EXISTS identity_pattern text NOT NULL DEFAULT '' CHECK (identity_pattern IN ('', 'solid', 'hatch'));
