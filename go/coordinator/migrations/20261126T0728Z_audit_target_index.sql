-- The latest audited change of named targets (connections_drift, the Agent environment section of Changes) is read by
-- target; without an index every read scanned the whole audit log.
CREATE INDEX IF NOT EXISTS audit_log_target_seq ON audit_log (target, seq DESC);
