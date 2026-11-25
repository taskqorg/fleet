-- Current definitions (docs/designs/fleet-standard.md S1), applied after the migrations by applySchemaFiles
-- (store.go) whenever the file changes. Edit here, never in a migration; TestMigrationSchemaFiles keeps the file in
-- the database's deparsed form (FLEET_UPDATE_SCHEMA_FILES=1 go test ./coordinator -run TestMigrationSchemaFiles).

-- account_inspection
CREATE OR REPLACE TRIGGER account_inspection_tombstone BEFORE INSERT ON account_inspection FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');

-- api_connection
CREATE OR REPLACE TRIGGER api_connection_capture_metadata AFTER INSERT OR DELETE OR UPDATE ON api_connection FOR EACH ROW EXECUTE FUNCTION resource_capture_metadata();
CREATE OR REPLACE TRIGGER api_connection_change AFTER INSERT OR DELETE OR UPDATE ON api_connection FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER api_connection_spend AFTER INSERT OR DELETE OR UPDATE ON api_connection FOR EACH ROW EXECUTE FUNCTION execution_spend_on_conn();

-- artifact
CREATE OR REPLACE TRIGGER artifact_change AFTER INSERT OR DELETE OR UPDATE ON artifact FOR EACH ROW EXECUTE FUNCTION fleet_artifact_change();
CREATE OR REPLACE TRIGGER artifact_tombstone BEFORE INSERT ON artifact FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
CREATE OR REPLACE TRIGGER artifact_touch BEFORE UPDATE ON artifact FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE OR REPLACE TRIGGER artifact_zcode BEFORE INSERT OR UPDATE OF project_id, code ON artifact FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();

-- artifact_copy
CREATE OR REPLACE TRIGGER artifact_copy_change AFTER INSERT OR DELETE OR UPDATE ON artifact_copy FOR EACH ROW EXECUTE FUNCTION fleet_artifact_change();
CREATE OR REPLACE TRIGGER artifact_copy_touch BEFORE UPDATE ON artifact_copy FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- audit_log
CREATE OR REPLACE TRIGGER audit_log_change AFTER INSERT OR DELETE OR UPDATE ON audit_log FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- board_message
CREATE OR REPLACE TRIGGER board_message_change AFTER INSERT OR DELETE OR UPDATE ON board_message FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- bot
CREATE OR REPLACE TRIGGER bot_change AFTER INSERT OR DELETE OR UPDATE ON bot FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER bot_zcode BEFORE INSERT OR UPDATE OF project_id, code ON bot FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();

-- bot_automation
CREATE OR REPLACE TRIGGER bot_automation_change AFTER INSERT OR DELETE OR UPDATE OF revision ON bot_automation FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- bot_run
CREATE OR REPLACE TRIGGER bot_run_change AFTER INSERT OR DELETE OR UPDATE ON bot_run FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER bot_run_touch BEFORE UPDATE ON bot_run FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- bot_task
CREATE OR REPLACE TRIGGER bot_task_change AFTER INSERT OR DELETE OR UPDATE ON bot_task FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER bot_task_touch BEFORE UPDATE ON bot_task FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- command
CREATE OR REPLACE TRIGGER command_change AFTER INSERT OR DELETE OR UPDATE ON command FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER command_tombstone BEFORE INSERT ON command FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');

-- connection_key
CREATE OR REPLACE TRIGGER connection_key_change AFTER INSERT OR DELETE OR UPDATE ON connection_key FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- definition
CREATE OR REPLACE TRIGGER definition_change AFTER INSERT OR DELETE OR UPDATE ON definition FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- delivery_check
CREATE OR REPLACE TRIGGER delivery_check_change AFTER INSERT OR DELETE OR UPDATE ON delivery_check FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- delivery_pr
CREATE OR REPLACE TRIGGER delivery_pr_change AFTER INSERT OR DELETE OR UPDATE ON delivery_pr FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- delivery_release
CREATE OR REPLACE TRIGGER delivery_release_change AFTER INSERT OR DELETE OR UPDATE ON delivery_release FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- delivery_run
CREATE OR REPLACE TRIGGER delivery_run_change AFTER INSERT OR DELETE OR UPDATE ON delivery_run FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- delivery_state
CREATE OR REPLACE TRIGGER delivery_state_change AFTER INSERT OR DELETE OR UPDATE ON delivery_state FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- event
CREATE OR REPLACE TRIGGER event_change AFTER INSERT OR DELETE OR UPDATE ON event FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER event_tombstone BEFORE INSERT ON event FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE OR REPLACE TRIGGER session_summary_event_ins AFTER INSERT ON event REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION fleet_session_summary_event();
CREATE OR REPLACE TRIGGER session_summary_event_upd AFTER UPDATE ON event REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION fleet_session_summary_event();

-- execution
CREATE OR REPLACE TRIGGER execution_change AFTER INSERT OR DELETE OR UPDATE ON execution FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER execution_derived AFTER INSERT OR DELETE OR UPDATE OF task_id, state, host_id, harness, started_at, seq ON execution FOR EACH ROW EXECUTE FUNCTION fleet_task_derived_touch();
CREATE OR REPLACE TRIGGER execution_spend_exec_del AFTER DELETE ON execution REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION execution_spend_on_exec_del();
CREATE OR REPLACE TRIGGER execution_spend_exec_ins AFTER INSERT ON execution REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION execution_spend_on_exec_ins();
CREATE OR REPLACE TRIGGER execution_spend_exec_upd AFTER UPDATE ON execution REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION execution_spend_on_exec_upd();
CREATE OR REPLACE TRIGGER execution_tombstone BEFORE INSERT ON execution FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('parent_execution_id');
CREATE OR REPLACE TRIGGER execution_touch BEFORE UPDATE ON execution FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE OR REPLACE TRIGGER resource_align_totals_owner AFTER UPDATE OF project_id ON execution FOR EACH ROW EXECUTE FUNCTION resource_align_totals_owner();
CREATE OR REPLACE TRIGGER resource_capture_execution AFTER INSERT OR DELETE OR UPDATE ON execution FOR EACH ROW EXECUTE FUNCTION resource_capture_execution();
CREATE OR REPLACE TRIGGER session_summary_execution AFTER INSERT OR DELETE OR UPDATE OF state, started_at, ended_at, session_id, kind ON execution FOR EACH ROW EXECUTE FUNCTION fleet_session_summary_execution();

-- execution_totals
CREATE OR REPLACE TRIGGER execution_spend_totals_del AFTER DELETE ON execution_totals REFERENCING OLD TABLE AS old_rows FOR EACH STATEMENT EXECUTE FUNCTION execution_spend_on_totals_del();
CREATE OR REPLACE TRIGGER execution_spend_totals_ins AFTER INSERT ON execution_totals REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION execution_spend_on_totals_ins();
CREATE OR REPLACE TRIGGER execution_spend_totals_upd AFTER UPDATE ON execution_totals REFERENCING OLD TABLE AS old_rows NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION execution_spend_on_totals_upd();
CREATE OR REPLACE TRIGGER execution_totals_change AFTER INSERT OR DELETE OR UPDATE ON execution_totals FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER resource_capture_totals AFTER INSERT OR DELETE OR UPDATE ON execution_totals FOR EACH ROW EXECUTE FUNCTION resource_capture_totals();

-- experiment
CREATE OR REPLACE TRIGGER experiment_change AFTER INSERT OR DELETE OR UPDATE ON experiment FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER experiment_record BEFORE UPDATE ON experiment FOR EACH ROW EXECUTE FUNCTION fleet_experiment_record();

-- experiment_note
CREATE OR REPLACE TRIGGER experiment_note_append_only BEFORE DELETE OR UPDATE ON experiment_note FOR EACH ROW EXECUTE FUNCTION fleet_note_append_only();

-- handoff
CREATE OR REPLACE TRIGGER handoff_tombstone BEFORE INSERT ON handoff FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');

-- host_observation
CREATE OR REPLACE TRIGGER host_observation_change AFTER INSERT OR DELETE OR UPDATE ON host_observation FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- managed_account
CREATE OR REPLACE TRIGGER managed_account_capture_metadata AFTER INSERT OR DELETE OR UPDATE ON managed_account FOR EACH ROW EXECUTE FUNCTION resource_capture_metadata();
CREATE OR REPLACE TRIGGER managed_account_change AFTER INSERT OR DELETE OR UPDATE ON managed_account FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- message
CREATE OR REPLACE TRIGGER message_change AFTER INSERT OR DELETE OR UPDATE ON message FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER message_tombstone BEFORE INSERT ON message FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE OR REPLACE TRIGGER session_summary_message_ins AFTER INSERT ON message REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION fleet_session_summary_message();
CREATE OR REPLACE TRIGGER session_summary_message_upd AFTER UPDATE ON message REFERENCING NEW TABLE AS new_rows FOR EACH STATEMENT EXECUTE FUNCTION fleet_session_summary_message();

-- price_entry
CREATE OR REPLACE TRIGGER price_entry_append_only BEFORE DELETE OR UPDATE ON price_entry FOR EACH ROW EXECUTE FUNCTION fleet_append_only();
CREATE OR REPLACE TRIGGER price_entry_spend AFTER INSERT ON price_entry FOR EACH ROW EXECUTE FUNCTION execution_spend_on_price();

-- problem_dismissal
CREATE OR REPLACE TRIGGER problem_dismissal_tombstone BEFORE INSERT ON problem_dismissal FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');

-- project_recommendation
CREATE OR REPLACE TRIGGER project_recommendation_change AFTER INSERT OR DELETE OR UPDATE ON project_recommendation FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- question
CREATE OR REPLACE TRIGGER question_change AFTER INSERT OR DELETE OR UPDATE ON question FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER question_derived AFTER INSERT OR DELETE OR UPDATE OF task_id, state ON question FOR EACH ROW EXECUTE FUNCTION fleet_task_derived_touch();
CREATE OR REPLACE TRIGGER question_tombstone BEFORE INSERT ON question FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
CREATE OR REPLACE TRIGGER question_touch BEFORE UPDATE ON question FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE OR REPLACE TRIGGER question_zcode BEFORE INSERT OR UPDATE OF project_id, code ON question FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();
CREATE OR REPLACE TRIGGER session_summary_question AFTER INSERT OR DELETE OR UPDATE OF state, session_id ON question FOR EACH ROW EXECUTE FUNCTION fleet_session_summary_question();

-- queued_message
CREATE OR REPLACE TRIGGER queued_message_tombstone BEFORE INSERT ON queued_message FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');

-- resource_project_summary
CREATE OR REPLACE TRIGGER resource_summary_change AFTER UPDATE OF revision ON resource_project_summary FOR EACH ROW WHEN (old.revision IS DISTINCT FROM new.revision) EXECUTE FUNCTION fleet_resource_summary_change();

-- resource_work
CREATE OR REPLACE TRIGGER resource_work_pending_copy AFTER UPDATE OF first_pending_at ON resource_work FOR EACH ROW WHEN (new.first_pending_at IS DISTINCT FROM old.first_pending_at) EXECUTE FUNCTION resource_work_pending_copy();

-- resource_work_scope
CREATE OR REPLACE TRIGGER resource_work_scope_stamp BEFORE INSERT ON resource_work_scope FOR EACH ROW WHEN (new.first_pending_at IS NULL) EXECUTE FUNCTION resource_work_scope_stamp();

-- run_token
CREATE OR REPLACE TRIGGER run_token_tombstone BEFORE INSERT ON run_token FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');

-- score
CREATE OR REPLACE TRIGGER score_change AFTER INSERT OR DELETE OR UPDATE ON score FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- session
CREATE OR REPLACE TRIGGER session_change AFTER INSERT OR DELETE OR UPDATE ON session FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER session_director_immutable BEFORE UPDATE ON session FOR EACH ROW EXECUTE FUNCTION fleet_session_director_immutable();
CREATE OR REPLACE TRIGGER session_summary_session AFTER INSERT OR UPDATE ON session FOR EACH ROW EXECUTE FUNCTION fleet_session_summary_session();
CREATE OR REPLACE TRIGGER session_zcode BEFORE INSERT OR UPDATE OF project_id, code ON session FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();

-- session_fork
CREATE OR REPLACE TRIGGER session_fork_tombstone BEFORE INSERT ON session_fork FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');

-- session_transition
CREATE OR REPLACE TRIGGER session_transition_tombstone BEFORE INSERT ON session_transition FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');

-- store
CREATE OR REPLACE TRIGGER store_change AFTER INSERT OR DELETE OR UPDATE ON store FOR EACH ROW EXECUTE FUNCTION fleet_artifact_change();
CREATE OR REPLACE TRIGGER store_touch BEFORE UPDATE ON store FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- task
CREATE OR REPLACE TRIGGER task_change AFTER INSERT OR DELETE OR UPDATE ON task FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER task_derived_project AFTER INSERT OR UPDATE OF project_id ON task FOR EACH ROW EXECUTE FUNCTION fleet_task_derived_touch();
CREATE OR REPLACE TRIGGER task_state_at BEFORE UPDATE OF state, pause_reason, wait_reason ON task FOR EACH ROW EXECUTE FUNCTION fleet_task_state_at();
CREATE OR REPLACE TRIGGER task_touch BEFORE UPDATE ON task FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE OR REPLACE TRIGGER task_zcode BEFORE INSERT OR UPDATE OF project_id, code ON task FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();

-- task_dependency
CREATE OR REPLACE TRIGGER task_dependency_change AFTER INSERT OR DELETE OR UPDATE ON task_dependency FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- task_derived
CREATE OR REPLACE TRIGGER task_derived_change AFTER INSERT OR DELETE OR UPDATE ON task_derived FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- transport_conflict
CREATE OR REPLACE TRIGGER transport_conflict_tombstone BEFORE INSERT ON transport_conflict FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');

-- trigger
CREATE OR REPLACE TRIGGER trigger_change AFTER INSERT OR DELETE ON trigger FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER trigger_update AFTER UPDATE ON trigger FOR EACH ROW WHEN (old.revision IS DISTINCT FROM new.revision OR old.fires IS DISTINCT FROM new.fires OR old.quiet_checks IS DISTINCT FROM new.quiet_checks OR old.last_error IS DISTINCT FROM new.last_error) EXECUTE FUNCTION fleet_change();

-- upload
CREATE OR REPLACE TRIGGER upload_tombstone BEFORE INSERT ON upload FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');

-- watch
CREATE OR REPLACE TRIGGER watch_change AFTER INSERT OR DELETE ON watch FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER watch_tombstone BEFORE INSERT ON watch FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE OR REPLACE TRIGGER watch_update AFTER UPDATE ON watch FOR EACH ROW WHEN ((old.payload - '{cursor,limitsSignaled}'::text[]) IS DISTINCT FROM (new.payload - '{cursor,limitsSignaled}'::text[])) EXECUTE FUNCTION fleet_change();

-- work_plan
CREATE OR REPLACE TRIGGER work_plan_change AFTER INSERT OR DELETE OR UPDATE ON work_plan FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE TRIGGER work_plan_touch BEFORE UPDATE ON work_plan FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- worker_done
CREATE OR REPLACE TRIGGER worker_done_tombstone BEFORE INSERT ON worker_done FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
