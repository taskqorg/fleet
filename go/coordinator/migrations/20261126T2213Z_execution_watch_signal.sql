-- NULL identifies changes written before execution watch signals were recorded.
ALTER TABLE change ADD COLUMN IF NOT EXISTS execution_watch_signal text;
CREATE INDEX IF NOT EXISTS change_execution_watch_signal ON change(id, execution_watch_signal) WHERE execution_watch_signal <> '';
