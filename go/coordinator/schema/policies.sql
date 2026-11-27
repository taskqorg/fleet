-- Current definitions (docs/designs/fleet-standard.md S1), applied after the migrations by applySchemaFiles
-- (store.go) whenever the file changes. Edit here, never in a migration; TestMigrationSchemaFiles keeps the file in
-- the database's deparsed form (FLEET_UPDATE_SCHEMA_FILES=1 go test ./coordinator -run TestMigrationSchemaFiles).

-- access_grant
ALTER TABLE access_grant ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON access_grant;
CREATE POLICY live ON access_grant AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((deleted_at IS NULL) AND (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[]))))));
DROP POLICY IF EXISTS project_scope ON access_grant;
CREATE POLICY project_scope ON access_grant FOR ALL TO public USING (fleet_visible(project_id));

-- account_catalogue
ALTER TABLE account_catalogue ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON account_catalogue;
CREATE POLICY live ON account_catalogue AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON account_catalogue;
CREATE POLICY project_scope ON account_catalogue FOR ALL TO public USING (fleet_visible(project_id));

-- account_check
ALTER TABLE account_check ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON account_check;
CREATE POLICY live ON account_check AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON account_check;
CREATE POLICY project_scope ON account_check FOR ALL TO public USING (fleet_visible(project_id));

-- account_inspection
ALTER TABLE account_inspection ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON account_inspection;
CREATE POLICY live ON account_inspection AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON account_inspection;
CREATE POLICY project_scope ON account_inspection FOR ALL TO public USING (fleet_visible(project_id));

-- activity
ALTER TABLE activity ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON activity;
CREATE POLICY project_scope ON activity FOR ALL TO public USING ((fleet_visible(project_id) OR ((project_id IS NULL) AND (session_id IS NOT NULL) AND (EXISTS ( SELECT 1
   FROM session s
  WHERE (s.id = activity.session_id)))) OR ((project_id IS NULL) AND (holder = current_setting('fleet.person'::text, true)))));

-- agent_gap
ALTER TABLE agent_gap ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON agent_gap;
CREATE POLICY live ON agent_gap AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON agent_gap;
CREATE POLICY project_scope ON agent_gap FOR ALL TO public USING (fleet_visible(project_id));

-- agent_gap_finding
ALTER TABLE agent_gap_finding ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON agent_gap_finding;
CREATE POLICY live ON agent_gap_finding AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON agent_gap_finding;
CREATE POLICY project_scope ON agent_gap_finding FOR ALL TO public USING (fleet_visible(project_id));

-- api_connection
ALTER TABLE api_connection ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON api_connection;
CREATE POLICY live ON api_connection AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON api_connection;
CREATE POLICY project_scope ON api_connection FOR ALL TO public USING ((fleet_visible(project_id) OR (holder = current_setting('fleet.person'::text, true))));

-- artifact
ALTER TABLE artifact ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON artifact;
CREATE POLICY live ON artifact AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON artifact;
CREATE POLICY project_scope ON artifact FOR ALL TO public USING (fleet_visible(project_id));

-- artifact_copy
ALTER TABLE artifact_copy ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON artifact_copy;
CREATE POLICY live ON artifact_copy AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON artifact_copy;
CREATE POLICY project_scope ON artifact_copy FOR ALL TO public USING (fleet_visible(project_id));

-- artifact_rule_confirmation
ALTER TABLE artifact_rule_confirmation ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON artifact_rule_confirmation;
CREATE POLICY live ON artifact_rule_confirmation AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON artifact_rule_confirmation;
CREATE POLICY project_scope ON artifact_rule_confirmation FOR ALL TO public USING (fleet_visible(project_id));

-- audit_log
ALTER TABLE audit_log ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON audit_log;
CREATE POLICY project_scope ON audit_log FOR ALL TO public USING ((fleet_visible(project_id) OR (holder = current_setting('fleet.person'::text, true))));

-- board_message
ALTER TABLE board_message ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON board_message;
CREATE POLICY live ON board_message AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON board_message;
CREATE POLICY project_scope ON board_message FOR ALL TO public USING (fleet_visible(project_id));

-- bot
ALTER TABLE bot ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON bot;
CREATE POLICY live ON bot AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND (NOT (project_id = ANY (fleet_deleted_projects()))))));
DROP POLICY IF EXISTS project_scope ON bot;
CREATE POLICY project_scope ON bot FOR ALL TO public USING (fleet_visible(project_id));

-- bot_memory_revision
ALTER TABLE bot_memory_revision ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON bot_memory_revision;
CREATE POLICY project_scope ON bot_memory_revision FOR ALL TO public USING ((EXISTS ( SELECT 1
   FROM bot b
  WHERE (b.id = bot_memory_revision.bot_id))));

-- bot_revision
ALTER TABLE bot_revision ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON bot_revision;
CREATE POLICY project_scope ON bot_revision FOR ALL TO public USING ((EXISTS ( SELECT 1
   FROM bot b
  WHERE (b.id = bot_revision.bot_id))));

-- bot_task
ALTER TABLE bot_task ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON bot_task;
CREATE POLICY live ON bot_task AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND (NOT (project_id = ANY (fleet_deleted_projects()))))));
DROP POLICY IF EXISTS project_scope ON bot_task;
CREATE POLICY project_scope ON bot_task FOR ALL TO public USING (fleet_visible(project_id));

-- budget
ALTER TABLE budget ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON budget;
CREATE POLICY project_scope ON budget FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR ((scope = 'project'::text) AND (scope_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))) OR ((scope = 'person'::text) AND (scope_id = current_setting('fleet.person'::text, true)))));

-- change
ALTER TABLE change ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS access_server_change ON change;
CREATE POLICY access_server_change ON change FOR SELECT TO public USING ((kind = 'access-server'::text));
DROP POLICY IF EXISTS directed_change ON change;
CREATE POLICY directed_change ON change FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id)));
DROP POLICY IF EXISTS project_scope ON change;
CREATE POLICY project_scope ON change FOR ALL TO public USING ((fleet_visible(project_id) OR (holder = current_setting('fleet.person'::text, true))));

-- closed_delta_identity
ALTER TABLE closed_delta_identity ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS execution_scope ON closed_delta_identity;
CREATE POLICY execution_scope ON closed_delta_identity FOR ALL TO public USING ((EXISTS ( SELECT 1
   FROM execution
  WHERE (execution.id = closed_delta_identity.execution_id))));

-- command
ALTER TABLE command ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON command;
CREATE POLICY live ON command AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON command;
CREATE POLICY project_scope ON command FOR ALL TO public USING (fleet_visible(project_id));

-- connection_key
ALTER TABLE connection_key ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON connection_key;
CREATE POLICY live ON connection_key AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON connection_key;
CREATE POLICY project_scope ON connection_key FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_baseline
ALTER TABLE delivery_baseline ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_baseline;
CREATE POLICY live ON delivery_baseline AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_baseline;
CREATE POLICY project_scope ON delivery_baseline FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_check
ALTER TABLE delivery_check ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_check;
CREATE POLICY live ON delivery_check AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_check;
CREATE POLICY project_scope ON delivery_check FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_history_state
ALTER TABLE delivery_history_state ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_history_state;
CREATE POLICY live ON delivery_history_state AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_history_state;
CREATE POLICY project_scope ON delivery_history_state FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_pr
ALTER TABLE delivery_pr ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_pr;
CREATE POLICY live ON delivery_pr AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_pr;
CREATE POLICY project_scope ON delivery_pr FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_release
ALTER TABLE delivery_release ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_release;
CREATE POLICY live ON delivery_release AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_release;
CREATE POLICY project_scope ON delivery_release FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_run
ALTER TABLE delivery_run ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_run;
CREATE POLICY live ON delivery_run AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_run;
CREATE POLICY project_scope ON delivery_run FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_run_read
ALTER TABLE delivery_run_read ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_run_read;
CREATE POLICY live ON delivery_run_read AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_run_read;
CREATE POLICY project_scope ON delivery_run_read FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_sample
ALTER TABLE delivery_sample ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_sample;
CREATE POLICY live ON delivery_sample AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_sample;
CREATE POLICY project_scope ON delivery_sample FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_settings
ALTER TABLE delivery_settings ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_settings;
CREATE POLICY live ON delivery_settings AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_settings;
CREATE POLICY project_scope ON delivery_settings FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_state
ALTER TABLE delivery_state ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_state;
CREATE POLICY live ON delivery_state AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_state;
CREATE POLICY project_scope ON delivery_state FOR ALL TO public USING (fleet_visible(project_id));

-- delivery_ui_seen
ALTER TABLE delivery_ui_seen ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON delivery_ui_seen;
CREATE POLICY live ON delivery_ui_seen AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON delivery_ui_seen;
CREATE POLICY project_scope ON delivery_ui_seen FOR ALL TO public USING (fleet_visible(project_id));

-- entity_code
ALTER TABLE entity_code ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON entity_code;
CREATE POLICY live ON entity_code AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON entity_code;
CREATE POLICY project_scope ON entity_code FOR ALL TO public USING ((fleet_visible(project_id) OR ((type = 'Q'::text) AND (EXISTS ( SELECT 1
   FROM question x
  WHERE ((x.id = entity_code.entity_id) AND (x.addressed_to = current_setting('fleet.person'::text, true)))))) OR ((type = 'C'::text) AND (project_id IS NULL) AND fleet_own_projectless_session(entity_id))));

-- event
ALTER TABLE event ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON event;
CREATE POLICY directed_session ON event FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id)));
DROP POLICY IF EXISTS live ON event;
CREATE POLICY live ON event AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON event;
CREATE POLICY project_scope ON event FOR ALL TO public USING (fleet_visible(project_id));

-- execution
ALTER TABLE execution ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_execution ON execution;
CREATE POLICY directed_execution ON execution FOR ALL TO public USING (((project_id IS NULL) AND (directed_by = current_setting('fleet.person'::text, true)) AND (fleet_own_projectless_session(session_id) OR ((session_id IS NULL) AND (NOT (data ? 'directorAccessRevokedAt'::text))))));
DROP POLICY IF EXISTS live ON execution;
CREATE POLICY live ON execution AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON execution;
CREATE POLICY project_scope ON execution FOR ALL TO public USING (fleet_visible(project_id));

-- execution_spend
ALTER TABLE execution_spend ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_spend ON execution_spend;
CREATE POLICY directed_spend ON execution_spend FOR ALL TO public USING (((project_id IS NULL) AND (fleet_own_projectless_execution(execution_id) OR (EXISTS ( SELECT 1
   FROM execution e
  WHERE ((e.id = execution_spend.execution_id) AND (e.project_id IS NULL) AND (e.session_id IS NULL) AND (e.directed_by = current_setting('fleet.person'::text, true)) AND (NOT (e.data ? 'directorAccessRevokedAt'::text))))))));
DROP POLICY IF EXISTS live ON execution_spend;
CREATE POLICY live ON execution_spend AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON execution_spend;
CREATE POLICY project_scope ON execution_spend FOR ALL TO public USING (fleet_visible(project_id));

-- execution_totals
ALTER TABLE execution_totals ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_totals ON execution_totals;
CREATE POLICY directed_totals ON execution_totals FOR ALL TO public USING (((project_id IS NULL) AND ((directed_by IS NULL) OR (directed_by = current_setting('fleet.person'::text, true))) AND (fleet_own_projectless_execution(execution_id) OR (EXISTS ( SELECT 1
   FROM execution e
  WHERE ((e.id = execution_totals.execution_id) AND (e.project_id IS NULL) AND (e.session_id IS NULL) AND (e.directed_by = current_setting('fleet.person'::text, true)) AND (NOT (e.data ? 'directorAccessRevokedAt'::text))))))));
DROP POLICY IF EXISTS live ON execution_totals;
CREATE POLICY live ON execution_totals AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON execution_totals;
CREATE POLICY project_scope ON execution_totals FOR ALL TO public USING (fleet_visible(project_id));

-- experiment
ALTER TABLE experiment ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON experiment;
CREATE POLICY live ON experiment AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON experiment;
CREATE POLICY project_scope ON experiment FOR ALL TO public USING (fleet_visible(project_id));

-- experiment_note
ALTER TABLE experiment_note ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON experiment_note;
CREATE POLICY live ON experiment_note AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON experiment_note;
CREATE POLICY project_scope ON experiment_note FOR ALL TO public USING (fleet_visible(project_id));

-- handoff
ALTER TABLE handoff ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON handoff;
CREATE POLICY directed_session ON handoff FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id)));
DROP POLICY IF EXISTS live ON handoff;
CREATE POLICY live ON handoff AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON handoff;
CREATE POLICY project_scope ON handoff FOR ALL TO public USING (fleet_visible(project_id));

-- host
ALTER TABLE host ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON host;
CREATE POLICY live ON host AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON host;
CREATE POLICY project_scope ON host FOR ALL TO public USING ((fleet_visible(project_id) OR (holder = current_setting('fleet.person'::text, true))));

-- host_observation
ALTER TABLE host_observation ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON host_observation;
CREATE POLICY live ON host_observation AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON host_observation;
CREATE POLICY project_scope ON host_observation FOR ALL TO public USING ((fleet_visible(project_id) OR (holder = current_setting('fleet.person'::text, true))));

-- host_setting
ALTER TABLE host_setting ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS operator_scope ON host_setting;
CREATE POLICY operator_scope ON host_setting FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- host_update
ALTER TABLE host_update ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON host_update;
CREATE POLICY project_scope ON host_update FOR ALL TO public USING ((fleet_visible(project_id) OR (holder = current_setting('fleet.person'::text, true))));

-- lab_trial
ALTER TABLE lab_trial ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS visible ON lab_trial;
CREATE POLICY visible ON lab_trial FOR SELECT TO public USING ((EXISTS ( SELECT 1
   FROM execution e
  WHERE (e.id = lab_trial.execution_id))));

-- limit_reading_latest
ALTER TABLE limit_reading_latest ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON limit_reading_latest;
CREATE POLICY directed_session ON limit_reading_latest FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id)));
DROP POLICY IF EXISTS live ON limit_reading_latest;
CREATE POLICY live ON limit_reading_latest AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON limit_reading_latest;
CREATE POLICY project_scope ON limit_reading_latest FOR ALL TO public USING (fleet_visible(project_id));

-- managed_account
ALTER TABLE managed_account ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON managed_account;
CREATE POLICY live ON managed_account AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON managed_account;
CREATE POLICY project_scope ON managed_account FOR ALL TO public USING ((fleet_visible(project_id) OR (holder = current_setting('fleet.person'::text, true))));

-- message
ALTER TABLE message ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON message;
CREATE POLICY directed_session ON message FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id)));
DROP POLICY IF EXISTS live ON message;
CREATE POLICY live ON message AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON message;
CREATE POLICY project_scope ON message FOR ALL TO public USING (fleet_visible(project_id));

-- person
ALTER TABLE person ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS person_scope ON person;
CREATE POLICY person_scope ON person FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (email = current_setting('fleet.person'::text, true))));

-- person_grant
ALTER TABLE person_grant ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS person_scope ON person_grant;
CREATE POLICY person_scope ON person_grant FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (email = current_setting('fleet.person'::text, true))));

-- person_setting
ALTER TABLE person_setting ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON person_setting;
CREATE POLICY live ON person_setting AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON person_setting;
CREATE POLICY project_scope ON person_setting FOR ALL TO public USING (fleet_visible(project_id));

-- personal_lab_draft
ALTER TABLE personal_lab_draft ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS personal_lab_author ON personal_lab_draft;
CREATE POLICY personal_lab_author ON personal_lab_draft FOR ALL TO public USING ((author = current_setting('fleet.lab_author'::text, true)));
DROP POLICY IF EXISTS personal_lab_review_read ON personal_lab_draft;
CREATE POLICY personal_lab_review_read ON personal_lab_draft FOR SELECT TO public USING (((pipeline_revision IS NOT NULL) AND (current_setting('fleet.lab_review'::text, true) = 'true'::text) AND fleet_visible(project)));
DROP POLICY IF EXISTS personal_lab_review_write ON personal_lab_draft;
CREATE POLICY personal_lab_review_write ON personal_lab_draft FOR UPDATE TO public USING (((pipeline_revision IS NOT NULL) AND (current_setting('fleet.lab_review'::text, true) = 'true'::text) AND fleet_visible(project)));

-- pipeline_run
ALTER TABLE pipeline_run ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON pipeline_run;
CREATE POLICY project_scope ON pipeline_run FOR ALL TO public USING ((EXISTS ( SELECT 1
   FROM execution e
  WHERE (e.id = pipeline_run.run_id))));

-- pipeline_step
ALTER TABLE pipeline_step ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON pipeline_step;
CREATE POLICY project_scope ON pipeline_step FOR ALL TO public USING ((EXISTS ( SELECT 1
   FROM execution e
  WHERE (e.id = pipeline_step.run_id))));

-- problem
ALTER TABLE problem ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON problem;
CREATE POLICY live ON problem AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON problem;
CREATE POLICY project_scope ON problem FOR ALL TO public USING (fleet_visible(project_id));

-- problem_dismissal
ALTER TABLE problem_dismissal ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON problem_dismissal;
CREATE POLICY live ON problem_dismissal AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON problem_dismissal;
CREATE POLICY project_scope ON problem_dismissal FOR ALL TO public USING (fleet_visible(project_id));

-- project_app
ALTER TABLE project_app ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON project_app;
CREATE POLICY live ON project_app AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((deleted_at IS NULL) AND (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[]))))));
DROP POLICY IF EXISTS project_scope ON project_app;
CREATE POLICY project_scope ON project_app FOR ALL TO public USING (fleet_visible(project_id));

-- project_recommendation
ALTER TABLE project_recommendation ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON project_recommendation;
CREATE POLICY live ON project_recommendation AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON project_recommendation;
CREATE POLICY project_scope ON project_recommendation FOR ALL TO public USING (fleet_visible(project_id));

-- publishing_ci_recovery
ALTER TABLE publishing_ci_recovery ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON publishing_ci_recovery;
CREATE POLICY live ON publishing_ci_recovery AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON publishing_ci_recovery;
CREATE POLICY project_scope ON publishing_ci_recovery FOR ALL TO public USING (fleet_visible(project_id));

-- publishing_ci_request
ALTER TABLE publishing_ci_request ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON publishing_ci_request;
CREATE POLICY live ON publishing_ci_request AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON publishing_ci_request;
CREATE POLICY project_scope ON publishing_ci_request FOR ALL TO public USING (fleet_visible(project_id));

-- purge_task
ALTER TABLE purge_task ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON purge_task;
CREATE POLICY project_scope ON purge_task FOR ALL TO public USING (fleet_visible(NULL::text));

-- purged_hash
ALTER TABLE purged_hash ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON purged_hash;
CREATE POLICY live ON purged_hash AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON purged_hash;
CREATE POLICY project_scope ON purged_hash FOR ALL TO public USING (fleet_visible(project_id));

-- question
ALTER TABLE question ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON question;
CREATE POLICY live ON question AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON question;
CREATE POLICY project_scope ON question FOR ALL TO public USING ((fleet_visible(project_id) OR (addressed_to = current_setting('fleet.person'::text, true))));

-- queued_message
ALTER TABLE queued_message ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON queued_message;
CREATE POLICY directed_session ON queued_message FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id)));
DROP POLICY IF EXISTS live ON queued_message;
CREATE POLICY live ON queued_message AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON queued_message;
CREATE POLICY project_scope ON queued_message FOR ALL TO public USING (fleet_visible(project_id));

-- reconciliation
ALTER TABLE reconciliation ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON reconciliation;
CREATE POLICY project_scope ON reconciliation FOR ALL TO public USING (fleet_visible(NULL::text));

-- resource_build
ALTER TABLE resource_build ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_build;
CREATE POLICY project_scope ON resource_build FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_contribution
ALTER TABLE resource_contribution ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_contribution;
CREATE POLICY project_scope ON resource_contribution FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_cost
ALTER TABLE resource_cost ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_cost;
CREATE POLICY project_scope ON resource_cost FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_extreme
ALTER TABLE resource_extreme ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_extreme;
CREATE POLICY project_scope ON resource_extreme FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_metadata_repair
ALTER TABLE resource_metadata_repair ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance ON resource_metadata_repair;
CREATE POLICY maintenance ON resource_metadata_repair FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- resource_metric
ALTER TABLE resource_metric ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_metric;
CREATE POLICY project_scope ON resource_metric FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_price_ref
ALTER TABLE resource_price_ref ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_price_ref;
CREATE POLICY project_scope ON resource_price_ref FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_project_summary
ALTER TABLE resource_project_summary ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_project_summary;
CREATE POLICY project_scope ON resource_project_summary FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_repair_scope
ALTER TABLE resource_repair_scope ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance ON resource_repair_scope;
CREATE POLICY maintenance ON resource_repair_scope FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- resource_tool
ALTER TABLE resource_tool ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON resource_tool;
CREATE POLICY project_scope ON resource_tool FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (project_id = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- resource_valuation
ALTER TABLE resource_valuation ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance ON resource_valuation;
CREATE POLICY maintenance ON resource_valuation FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- resource_work
ALTER TABLE resource_work ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance ON resource_work;
CREATE POLICY maintenance ON resource_work FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- resource_work_scope
ALTER TABLE resource_work_scope ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance ON resource_work_scope;
CREATE POLICY maintenance ON resource_work_scope FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- route_decision
ALTER TABLE route_decision ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON route_decision;
CREATE POLICY live ON route_decision AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON route_decision;
CREATE POLICY project_scope ON route_decision FOR ALL TO public USING (fleet_visible(project_id));

-- run_token
ALTER TABLE run_token ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON run_token;
CREATE POLICY live ON run_token AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON run_token;
CREATE POLICY project_scope ON run_token FOR ALL TO public USING (fleet_visible(project_id));

-- runner_call_hour
ALTER TABLE runner_call_hour ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed ON runner_call_hour;
CREATE POLICY directed ON runner_call_hour FOR ALL TO public USING (((project_id IS NULL) AND (directed_by = current_setting('fleet.person'::text, true))));
DROP POLICY IF EXISTS project_scope ON runner_call_hour;
CREATE POLICY project_scope ON runner_call_hour FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (COALESCE(project_id, ''::text) = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))));

-- score
ALTER TABLE score ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON score;
CREATE POLICY live ON score AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON score;
CREATE POLICY project_scope ON score FOR ALL TO public USING (fleet_visible(project_id));

-- session
ALTER TABLE session ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON session;
CREATE POLICY directed_session ON session FOR ALL TO public USING (((project_id IS NULL) AND (directed_by = current_setting('fleet.person'::text, true)) AND (director_access_revoked_at IS NULL)));
DROP POLICY IF EXISTS live ON session;
CREATE POLICY live ON session AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON session;
CREATE POLICY project_scope ON session FOR ALL TO public USING (fleet_visible(project_id));

-- session_fork
ALTER TABLE session_fork ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON session_fork;
CREATE POLICY directed_session ON session_fork FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id) AND fleet_own_projectless_session(source_session_id)));
DROP POLICY IF EXISTS live ON session_fork;
CREATE POLICY live ON session_fork AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON session_fork;
CREATE POLICY project_scope ON session_fork FOR ALL TO public USING (fleet_visible(project_id));

-- session_summary
ALTER TABLE session_summary ENABLE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS visible ON session_summary;
CREATE POLICY visible ON session_summary FOR SELECT TO public USING ((EXISTS ( SELECT 1
   FROM session s
  WHERE (s.id = session_summary.session_id))));

-- session_transition
ALTER TABLE session_transition ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS directed_session ON session_transition;
CREATE POLICY directed_session ON session_transition FOR ALL TO public USING (((project_id IS NULL) AND fleet_own_projectless_session(session_id)));
DROP POLICY IF EXISTS live ON session_transition;
CREATE POLICY live ON session_transition AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON session_transition;
CREATE POLICY project_scope ON session_transition FOR ALL TO public USING (fleet_visible(project_id));

-- store
ALTER TABLE store ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS operator_scope ON store;
CREATE POLICY operator_scope ON store FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (holder = current_setting('fleet.person'::text, true))));

-- task
ALTER TABLE task ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON task;
CREATE POLICY live ON task AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON task;
CREATE POLICY project_scope ON task FOR ALL TO public USING (fleet_visible(project_id));

-- task_dependency
ALTER TABLE task_dependency ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON task_dependency;
CREATE POLICY live ON task_dependency AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON task_dependency;
CREATE POLICY project_scope ON task_dependency FOR ALL TO public USING (fleet_visible(project_id));

-- task_derived
ALTER TABLE task_derived ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON task_derived;
CREATE POLICY live ON task_derived AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))));
DROP POLICY IF EXISTS project_scope ON task_derived;
CREATE POLICY project_scope ON task_derived FOR ALL TO public USING (fleet_visible(project_id));

-- telegram_history
ALTER TABLE telegram_history ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS room_scope ON telegram_history;
CREATE POLICY room_scope ON telegram_history FOR ALL TO public USING ((EXISTS ( SELECT 1
   FROM telegram_room r
  WHERE (r.id = telegram_history.room_id))));

-- telegram_link
ALTER TABLE telegram_link ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS person_scope ON telegram_link;
CREATE POLICY person_scope ON telegram_link FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (email = current_setting('fleet.person'::text, true))));

-- telegram_outbox
ALTER TABLE telegram_outbox ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS session_scope ON telegram_outbox;
CREATE POLICY session_scope ON telegram_outbox FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR ((session_id IS NOT NULL) AND (EXISTS ( SELECT 1
   FROM session s
  WHERE (s.id = telegram_outbox.session_id))))));

-- telegram_person
ALTER TABLE telegram_person ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS person_scope ON telegram_person;
CREATE POLICY person_scope ON telegram_person FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (email = current_setting('fleet.person'::text, true))));

-- telegram_room
ALTER TABLE telegram_room ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON telegram_room;
CREATE POLICY project_scope ON telegram_room FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR ((project_hint <> ''::text) AND fleet_visible(project_hint)) OR (EXISTS ( SELECT 1
   FROM telegram_work w
  WHERE (w.room_id = telegram_room.id)))));

-- telegram_turn
ALTER TABLE telegram_turn ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS session_scope ON telegram_turn;
CREATE POLICY session_scope ON telegram_turn FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (EXISTS ( SELECT 1
   FROM session s
  WHERE (s.id = telegram_turn.session_id)))));

-- telegram_update
ALTER TABLE telegram_update ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS operator_scope ON telegram_update;
CREATE POLICY operator_scope ON telegram_update FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- telegram_work
ALTER TABLE telegram_work ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS session_scope ON telegram_work;
CREATE POLICY session_scope ON telegram_work FOR ALL TO public USING (((current_setting('fleet.project_ids'::text, true) = '*'::text) OR (EXISTS ( SELECT 1
   FROM session s
  WHERE (s.id = telegram_work.session_id)))));

-- transport_audit
ALTER TABLE transport_audit ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON transport_audit;
CREATE POLICY project_scope ON transport_audit FOR ALL TO public USING (fleet_visible(NULL::text));

-- transport_conflict
ALTER TABLE transport_conflict ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON transport_conflict;
CREATE POLICY live ON transport_conflict AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR (tombstoned_at IS NULL)));
DROP POLICY IF EXISTS project_scope ON transport_conflict;
CREATE POLICY project_scope ON transport_conflict FOR ALL TO public USING (fleet_visible(NULL::text));

-- trigger
ALTER TABLE trigger ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON trigger;
CREATE POLICY project_scope ON trigger FOR ALL TO public USING (fleet_visible(project_id));

-- trigger_log
ALTER TABLE trigger_log ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS project_scope ON trigger_log;
CREATE POLICY project_scope ON trigger_log FOR ALL TO public USING (fleet_visible(project_id));

-- upload
ALTER TABLE upload ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON upload;
CREATE POLICY live ON upload AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON upload;
CREATE POLICY project_scope ON upload FOR ALL TO public USING (fleet_visible(project_id));

-- wake_outbox
ALTER TABLE wake_outbox ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS maintenance ON wake_outbox;
CREATE POLICY maintenance ON wake_outbox FOR ALL TO public USING ((current_setting('fleet.project_ids'::text, true) = '*'::text));

-- watch
ALTER TABLE watch ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON watch;
CREATE POLICY live ON watch AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON watch;
CREATE POLICY project_scope ON watch FOR ALL TO public USING (fleet_visible(project_id));

-- work_plan
ALTER TABLE work_plan ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON work_plan;
CREATE POLICY live ON work_plan AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON work_plan;
CREATE POLICY project_scope ON work_plan FOR ALL TO public USING (fleet_visible(project_id));

-- worker_done
ALTER TABLE worker_done ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON worker_done;
CREATE POLICY live ON worker_done AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND ((project_id IS NULL) OR (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[])))))));
DROP POLICY IF EXISTS project_scope ON worker_done;
CREATE POLICY project_scope ON worker_done FOR ALL TO public USING (fleet_visible(project_id));

-- youtrack_binding
ALTER TABLE youtrack_binding ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON youtrack_binding;
CREATE POLICY live ON youtrack_binding AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[]))))));
DROP POLICY IF EXISTS project_scope ON youtrack_binding;
CREATE POLICY project_scope ON youtrack_binding FOR ALL TO public USING (fleet_visible(project_id)) WITH CHECK (fleet_visible(project_id));

-- youtrack_comment_work
ALTER TABLE youtrack_comment_work ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON youtrack_comment_work;
CREATE POLICY live ON youtrack_comment_work AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[]))))));
DROP POLICY IF EXISTS project_scope ON youtrack_comment_work;
CREATE POLICY project_scope ON youtrack_comment_work FOR ALL TO public USING (fleet_visible(project_id)) WITH CHECK (fleet_visible(project_id));

-- youtrack_inbox
ALTER TABLE youtrack_inbox ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON youtrack_inbox;
CREATE POLICY live ON youtrack_inbox AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[]))))));
DROP POLICY IF EXISTS project_scope ON youtrack_inbox;
CREATE POLICY project_scope ON youtrack_inbox FOR ALL TO public USING (fleet_visible(project_id)) WITH CHECK (fleet_visible(project_id));

-- youtrack_receipt
ALTER TABLE youtrack_receipt ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON youtrack_receipt;
CREATE POLICY live ON youtrack_receipt AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[]))))));
DROP POLICY IF EXISTS project_scope ON youtrack_receipt;
CREATE POLICY project_scope ON youtrack_receipt FOR ALL TO public USING (fleet_visible(project_id)) WITH CHECK (fleet_visible(project_id));

-- youtrack_task
ALTER TABLE youtrack_task ENABLE ROW LEVEL SECURITY, FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS live ON youtrack_task;
CREATE POLICY live ON youtrack_task AS RESTRICTIVE FOR SELECT TO public USING ((fleet_show_deleted() OR ((tombstoned_at IS NULL) AND (NOT (project_id = ANY (( SELECT fleet_deleted_projects() AS fleet_deleted_projects)::text[]))))));
DROP POLICY IF EXISTS project_scope ON youtrack_task;
CREATE POLICY project_scope ON youtrack_task FOR ALL TO public USING (fleet_visible(project_id)) WITH CHECK (fleet_visible(project_id));
