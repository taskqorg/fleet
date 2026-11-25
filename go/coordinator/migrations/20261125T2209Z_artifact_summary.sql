-- A result's one-line summary (projects Overview and Results, docs/design-system/pages/results.md): the `summary` text a
-- pipeline's output.json carries (scripts/lab/doc_recognize.py finish writes "KATARZYNA NOWAK · 5 files · 1 unreadable"),
-- read once when the coordinator stores the output, so a list names what was produced instead of "output.json".
ALTER TABLE artifact ADD COLUMN IF NOT EXISTS summary text;
