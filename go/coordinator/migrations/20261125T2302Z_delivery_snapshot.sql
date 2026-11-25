-- The delivery pass's last state (delivery.go deliveryLast: pull requests, stages, landing, samples) survives a restart:
-- a coordinator that starts answers publishing_status, delivery_status, Home and Changes from it until its first pass,
-- instead of "no pull requests" for the minute after every release (prod 2026-11-26: 10 of 12 reads empty while a
-- release rolled out). One row, written by every pass; read by the coordinator only.
CREATE TABLE IF NOT EXISTS delivery_snapshot(
 id text PRIMARY KEY,
 state jsonb NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
