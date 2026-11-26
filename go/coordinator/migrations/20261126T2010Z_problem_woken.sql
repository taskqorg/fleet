-- Idempotent bot wakes (docs/designs/fleet-system-management.md#problems-anomalies-with-a-bot-owner): when the owner
-- bot was last woken about a problem; a wake carries a problem only when it is new to the bot or changed since.
ALTER TABLE problem ADD COLUMN IF NOT EXISTS woken_at timestamptz NULL;
