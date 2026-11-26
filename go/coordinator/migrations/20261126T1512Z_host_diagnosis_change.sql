-- host_diagnosis rows feed the change stream (schema/functions.sql fleet_change, kind host), so the host detail's
-- Diagnosis section rereads when a report lands; fleet_change reads project_id from every row it records.
ALTER TABLE host_diagnosis ADD COLUMN IF NOT EXISTS project_id text NULL;
