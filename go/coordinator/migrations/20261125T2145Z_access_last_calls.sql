-- access_status reads each connection's latest call from execution_spend by its connection column (AccessLastCalls):
-- one backward walk of a connection's rows by start time. Under row level security the walk by data->>'connection' in
-- execution was no index condition and scanned every execution twice per connection (10 s on prod).
CREATE INDEX IF NOT EXISTS execution_spend_connection_last ON execution_spend (connection, started_at DESC, execution_id COLLATE "C" DESC)
 WHERE connection IS NOT NULL AND kind = 'tool_run';
