-- Access servers and grants write the change stream (kinds access-grant and access-server, fleet_change in
-- schema/functions.sql), so the Access screens update live: fleet_change records every row's project_id, and a server
-- belongs to no project.
ALTER TABLE access_server ADD COLUMN IF NOT EXISTS project_id text NULL CHECK (project_id IS NULL);
