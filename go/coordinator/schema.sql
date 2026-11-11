-- Fleet Postgres schema. Each "-- migration N" section runs once, in order, inside one transaction,
-- as the owner role fleet; fleet_app gets row access through the default privileges in deploy/infrastructure/fleet-postgres.sql.
-- schema_migration(version, applied_at) is created by store.migrate() before the first section.

-- migration 1: legacy records as documents (design Phase 1, item 10)
CREATE TABLE document(
 kind text NOT NULL,
 id text PRIMARY KEY,
 data jsonb NOT NULL,
 project_id text NULL,
 seq bigserial NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX document_kind_seq ON document(kind, seq);
CREATE INDEX document_project ON document(project_id) WHERE project_id IS NOT NULL;

-- migration 2: per-request reads filter documents by jsonb containment (store.all with a filter)
CREATE INDEX document_data ON document USING gin (data jsonb_path_ops);

-- migration 3: without the GIN pending list, SERIALIZABLE predicate locks stay on the index entries a query reads.
-- With fastupdate on, a non-empty pending list makes every containment read lock the whole index, so every write conflicts.
ALTER INDEX document_data SET (fastupdate = off);

-- migration 4: per-execution and per-session reads (store.all) lock only that execution's or session's index range.
CREATE INDEX document_execution ON document(kind, (data->>'executionId'), seq);
CREATE INDEX document_session ON document(kind, (data->>'sessionId'), seq);

-- migration 5: delegations still open (delegates.reconcile on every poll)
CREATE INDEX document_open_delegation ON document(seq) WHERE kind = 'delegation' AND coalesce(data->>'continuationExecutionId', '') = '' AND coalesce(data->>'state', '') <> 'suppressed';

-- migration 6: executions, tasks, dependencies and questions as typed tables (design Phase 2, items 16-18).
-- Each typed row keeps its typed columns and, in data, the record shape the coordinator API returns (the legacy document);
-- executions.ts and tasks.ts write both together. seq shares the document sequence, so insertion order holds across tables.
CREATE TYPE execution_kind AS ENUM ('agent_run', 'pipeline_run', 'pipeline_step', 'child_agent', 'tool_run');
CREATE TYPE execution_state AS ENUM ('preparing', 'running', 'waiting', 'succeeded', 'failed', 'cancelled');
CREATE TYPE observation AS ENUM ('confirmed', 'unconfirmed');
CREATE TYPE task_state AS ENUM ('queued', 'in_progress', 'waiting', 'paused', 'completed', 'failed', 'cancelled');
CREATE TYPE question_kind AS ENUM ('operator_decision', 'agent_question', 'acceptance');
CREATE TYPE question_state AS ENUM ('open', 'answered', 'withdrawn');
CREATE TABLE task(
 id text PRIMARY KEY,
 project_id text NULL,
 title text NOT NULL,
 expected_outcome text NULL,
 state task_state NOT NULL DEFAULT 'queued',
 state_before_pause task_state NULL,
 wait_reason text NULL,
 priority integer NOT NULL DEFAULT 0,
 completion_requirements jsonb NOT NULL DEFAULT '{}',
 result_destination text NULL,
 delivery_method jsonb NULL,
 accepted_by text NULL,
 accepted_at timestamptz NULL,
 accepted_artifact_ids text[] NULL,
 created_by text NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 legacy_kind text NULL,
 legacy_id text NULL,
 data jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
-- kind: blocks gates the queued -> in_progress and waiting -> in_progress transitions; revision_of (a delivery revising an
-- earlier one, legacy previousDeliveryId) is recorded without gating, as the legacy flow never waited for acceptance.
CREATE TABLE task_dependency(
 task_id text NOT NULL REFERENCES task(id),
 depends_on_task_id text NOT NULL REFERENCES task(id),
 kind text NOT NULL DEFAULT 'blocks' CHECK (kind IN ('blocks', 'revision_of')),
 project_id text NULL,
 PRIMARY KEY (task_id, depends_on_task_id)
);
CREATE TABLE execution(
 id text PRIMARY KEY,
 kind execution_kind NOT NULL,
 state execution_state NOT NULL,
 observation observation NOT NULL DEFAULT 'confirmed',
 last_seen_at timestamptz NULL,
 project_id text NULL,
 task_id text NULL REFERENCES task(id) DEFERRABLE INITIALLY DEFERRED,
 experiment_id text NULL,
 variant text NULL,
 parent_execution_id text NULL,
 session_id text NULL,
 account_id text NULL,
 host_id text NULL,
 environment_id text NULL,
 harness text NULL,
 harness_version text NULL,
 model text NULL,
 config_ref jsonb NULL,
 problem text NULL,
 started_at timestamptz NULL,
 ended_at timestamptz NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 delegation jsonb NULL,
 legacy_kind text NULL,
 legacy_id text NULL,
 data jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
CREATE INDEX execution_project_state ON execution(project_id, state, started_at);
CREATE INDEX execution_task ON execution(task_id);
CREATE INDEX execution_experiment ON execution(experiment_id);
CREATE INDEX execution_parent ON execution(parent_execution_id);
CREATE INDEX execution_account ON execution(account_id);
CREATE INDEX execution_session ON execution(session_id, seq);
CREATE INDEX execution_host ON execution(host_id, seq);
CREATE INDEX execution_legacy ON execution(legacy_kind, seq);
CREATE INDEX execution_open_delegation ON execution(seq) WHERE delegation IS NOT NULL AND coalesce(delegation->>'continuationExecutionId', '') = '' AND coalesce(delegation->>'state', '') <> 'suppressed';
CREATE INDEX execution_data ON execution USING gin (data jsonb_path_ops) WITH (fastupdate = off);
CREATE TABLE execution_totals(
 execution_id text PRIMARY KEY REFERENCES execution(id),
 project_id text NULL,
 tokens_input bigint NULL, tokens_input_basis text NOT NULL DEFAULT 'unavailable',
 tokens_output bigint NULL, tokens_output_basis text NOT NULL DEFAULT 'unavailable',
 tokens_cache_read bigint NULL, tokens_cache_read_basis text NOT NULL DEFAULT 'unavailable',
 tokens_cache_write bigint NULL, tokens_cache_write_basis text NOT NULL DEFAULT 'unavailable',
 rate_limit_last jsonb NULL, rate_limit_last_basis text NOT NULL DEFAULT 'unavailable',
 connection_cost jsonb NULL, connection_cost_basis text NOT NULL DEFAULT 'unavailable',
 wall_ms bigint NULL, wall_ms_basis text NOT NULL DEFAULT 'unavailable',
 active_ms bigint NULL, active_ms_basis text NOT NULL DEFAULT 'unavailable',
 cpu_ms bigint NULL, cpu_ms_basis text NOT NULL DEFAULT 'unavailable',
 peak_memory_bytes bigint NULL, peak_memory_bytes_basis text NOT NULL DEFAULT 'unavailable',
 history_incomplete integer NOT NULL DEFAULT 0,
 computed_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE question(
 id text PRIMARY KEY,
 kind question_kind NOT NULL,
 project_id text NULL,
 task_id text NULL REFERENCES task(id) DEFERRABLE INITIALLY DEFERRED,
 execution_id text NULL,
 session_id text NULL,
 text text NOT NULL,
 options jsonb NULL,
 asked_by text NULL,
 asked_at timestamptz NOT NULL DEFAULT now(),
 answer text NULL,
 answered_by text NULL,
 answered_at timestamptz NULL,
 state question_state NOT NULL DEFAULT 'open',
 updated_at timestamptz NOT NULL DEFAULT now(),
 legacy_kind text NULL,
 data jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
-- updated_at moves on every change; the rollback export (migrate-sqlite reverse) selects rows changed after the cut-over.
CREATE FUNCTION touch_updated_at() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN NEW.updated_at := now(); RETURN NEW; END $$;
CREATE TRIGGER execution_touch BEFORE UPDATE ON execution FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER question_touch BEFORE UPDATE ON question FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER task_touch BEFORE UPDATE ON task FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE INDEX question_task ON question(task_id);
CREATE INDEX question_execution ON question(execution_id);
CREATE INDEX question_session ON question(session_id, seq);
CREATE INDEX question_legacy ON question(legacy_kind, state, seq);
CREATE INDEX question_data ON question USING gin (data jsonb_path_ops) WITH (fastupdate = off);
CREATE INDEX task_project_state ON task(project_id, state);
CREATE INDEX task_legacy ON task(legacy_kind, seq);
CREATE INDEX task_data ON task USING gin (data jsonb_path_ops) WITH (fastupdate = off);
-- Derived per task: executions by state, totals summed over distinct executions, the latest result artifact, open questions.
-- Artifacts are documents until Phase 3 moves them to the artifact table.
CREATE VIEW task_view WITH (security_invoker = true) AS
 SELECT t.id AS task_id, t.project_id, t.state, t.wait_reason,
  coalesce((SELECT jsonb_object_agg(s.state, s.n) FROM (SELECT e.state, count(*) AS n FROM execution e WHERE e.task_id = t.id GROUP BY e.state) s), '{}') AS executions_by_state,
  (SELECT count(*) FROM execution e WHERE e.task_id = t.id) AS execution_count,
  (SELECT jsonb_build_object('tokens_input', sum(x.tokens_input), 'tokens_output', sum(x.tokens_output), 'tokens_cache_read', sum(x.tokens_cache_read), 'tokens_cache_write', sum(x.tokens_cache_write), 'wall_ms', sum(x.wall_ms), 'active_ms', sum(x.active_ms), 'cpu_ms', sum(x.cpu_ms), 'measured_wall', count(*) FILTER (WHERE x.wall_ms_basis = 'measured'), 'executions_with_totals', count(*))
   FROM execution_totals x WHERE x.execution_id IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)) AS totals,
  (SELECT d.data - 'contentBase64' FROM document d WHERE d.kind = 'artifact' AND d.data->>'executionId' IN (SELECT e.id FROM execution e WHERE e.task_id = t.id) ORDER BY d.seq DESC LIMIT 1) AS latest_result,
  (SELECT coalesce(jsonb_agg(jsonb_build_object('id', q.id, 'kind', q.kind, 'text', q.text, 'options', q.options) ORDER BY q.seq), '[]') FROM question q WHERE q.task_id = t.id AND q.state = 'open') AS open_questions
 FROM task t;
-- Row-level security on every table, applied to the owner too (FORCE). fleet.project_ids lists the visible projects;
-- '*' is the operator scope the coordinator sets for itself, and '' in the list makes rows without a project visible.
-- Unset (no scope) shows nothing.
ALTER TABLE document ENABLE ROW LEVEL SECURITY;
ALTER TABLE document FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON document USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE task ENABLE ROW LEVEL SECURITY;
ALTER TABLE task FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON task USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE task_dependency ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_dependency FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON task_dependency USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE execution ENABLE ROW LEVEL SECURITY;
ALTER TABLE execution FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON execution USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE execution_totals ENABLE ROW LEVEL SECURITY;
ALTER TABLE execution_totals FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON execution_totals USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE question ENABLE ROW LEVEL SECURITY;
ALTER TABLE question FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON question USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')))

-- migration 7: backfill after the first live Phase 2 run (idempotent; only fills NULL links and clears stale problems).
-- A succeeded execution carries no problem (design §1).
UPDATE execution SET problem = NULL WHERE state = 'succeeded' AND problem IS NOT NULL;
-- Executions of a session started by a host agent (session_start: startRequest and createdBySessionId) link to the creator
-- session's latest execution created at or before that session, the execution that called session_start.
UPDATE execution c SET parent_execution_id = p.id FROM document s, LATERAL (SELECT e.id FROM execution e WHERE e.session_id = s.data->>'createdBySessionId' AND e.legacy_kind = 'execution' AND coalesce((e.data->>'createdAt')::bigint, 0) <= coalesce((s.data->>'createdAt')::bigint, 0) ORDER BY e.seq DESC LIMIT 1) p WHERE c.parent_execution_id IS NULL AND c.legacy_kind = 'execution' AND s.kind = 'session' AND s.id = c.session_id AND s.data ? 'startRequest' AND s.data ? 'createdBySessionId';
-- Delegated children whose legacy record names the delegating execution.
UPDATE execution SET parent_execution_id = data->>'parentExecutionId' WHERE parent_execution_id IS NULL AND legacy_kind = 'execution' AND data ? 'parentExecutionId'

-- migration 8: change log for the operator app's live feed (docs/designs/fleet-operator-app.md §4, D5).
-- Triggers write one row per watched write, inside the writer's transaction, so a change is visible exactly when its data is.
-- Readers order by (xid, seq) and return only rows of transactions older than every transaction still running
-- (pg_snapshot_xmin), so a slow transaction that commits after a later one is never skipped. NOTIFY has a constant payload:
-- Postgres folds duplicates within one transaction, so each commit wakes listeners once.
CREATE TABLE change(
 seq bigserial PRIMARY KEY,
 xid xid8 NOT NULL DEFAULT pg_current_xact_id(),
 kind text NOT NULL,
 id text NOT NULL,
 task_id text NULL,
 session_id text NULL,
 host_id text NULL,
 project_id text NULL,
 op char(1) NOT NULL,
 at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX change_order ON change(xid, seq);
CREATE INDEX change_at ON change(at);
ALTER TABLE change ENABLE ROW LEVEL SECURITY;
ALTER TABLE change FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON change USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
-- Kind map (design §4): only kinds a view uses write rows; host polls that only move lastSeenAt are skipped. One line: the
-- migration runner splits statements at a semicolon that ends a line.
CREATE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1)); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;
CREATE TRIGGER document_change AFTER INSERT OR UPDATE OR DELETE ON document FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER task_change AFTER INSERT OR UPDATE OR DELETE ON task FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER task_dependency_change AFTER INSERT OR UPDATE OR DELETE ON task_dependency FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER execution_change AFTER INSERT OR UPDATE OR DELETE ON execution FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER execution_totals_change AFTER INSERT OR UPDATE OR DELETE ON execution_totals FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER question_change AFTER INSERT OR UPDATE OR DELETE ON question FOR EACH ROW EXECUTE FUNCTION fleet_change();
-- Priority is a queue class, not a number (owner decision 2026-11-10, fleet-data-foundation §2): urgent runs first, normal is
-- the default (first in, first out), background runs only when nothing else waits for the same resource. 0 and any other old
-- number become normal.
ALTER TABLE task ALTER COLUMN priority DROP DEFAULT;
ALTER TABLE task ALTER COLUMN priority TYPE text USING 'normal';
ALTER TABLE task ALTER COLUMN priority SET DEFAULT 'normal';
ALTER TABLE task ADD CONSTRAINT task_priority_class CHECK (priority IN ('urgent', 'normal', 'background'));
-- migration 9: artifact store (design §3, Phase 3 item 21). Bytes live in the per-host S3 stores; rows hold metadata,
-- copies and upload state. An upload or copy in progress is its artifact_copy row (staging key, upload id, lease).
CREATE TABLE store(
 id text PRIMARY KEY,
 kind text NOT NULL DEFAULT 's3',
 host_id text NOT NULL,
 zone text NULL,
 bucket text NOT NULL,
 endpoint text NOT NULL,
 local_endpoint text NULL,
 state text NOT NULL DEFAULT 'unreachable' CHECK (state IN ('reachable', 'unreachable', 'full')),
 capacity_bytes bigint NULL,
 used_bytes bigint NULL,
 requires_logon boolean NOT NULL DEFAULT false,
 -- store.free_space_warning (operator setting, §11): full when capacity left falls below it; NULL: never full.
 free_space_warning_bytes bigint NULL,
 observed_at timestamptz NULL,
 registered_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE artifact(
 id text PRIMARY KEY,
 project_id text NULL,
 scope text NOT NULL CHECK (scope IN ('lab', 'prod')),
 content_hash text NOT NULL,
 size bigint NOT NULL,
 media_type text NOT NULL,
 name text NOT NULL,
 execution_id text NULL,
 task_id text NULL REFERENCES task(id) DEFERRABLE INITIALLY DEFERRED,
 required boolean NOT NULL DEFAULT false,
 previous_artifact_id text NULL,
 derived_from_artifact_id text NULL,
 role text NOT NULL DEFAULT 'original' CHECK (role IN ('original', 'thumbnail', 'poster')),
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'stored', 'failed', 'deleted')),
 last_error text NULL,
 deleted_at timestamptz NULL,
 deleted_by text NULL,
 legacy_id text NULL UNIQUE,
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
CREATE INDEX artifact_hash ON artifact(project_id, scope, content_hash);
CREATE INDEX artifact_execution ON artifact(execution_id, seq);
CREATE INDEX artifact_task ON artifact(task_id);
CREATE INDEX artifact_state ON artifact(state);
CREATE TABLE artifact_copy(
 artifact_id text NOT NULL REFERENCES artifact(id),
 store_id text NOT NULL REFERENCES store(id),
 project_id text NULL,
 state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'copying', 'verified', 'failed', 'purge_pending', 'deleted')),
 upload_id text NULL,
 staging_key text NULL,
 lease_expires_at timestamptz NULL,
 verified_at timestamptz NULL,
 last_error text NULL,
 -- A failed copy is retried by placement after next_attempt_at (backoff doubling up to artifact_retry_window).
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (artifact_id, store_id)
);
CREATE INDEX artifact_copy_store ON artifact_copy(store_id, state);
CREATE INDEX artifact_copy_lease ON artifact_copy(state, lease_expires_at);
CREATE TABLE purged_hash(
 project_id text NOT NULL,
 scope text NOT NULL,
 content_hash text NOT NULL,
 reason text NOT NULL,
 purged_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (project_id, scope, content_hash)
);
CREATE TABLE artifact_rule_confirmation(
 project_id text NOT NULL,
 rule_hash text NOT NULL,
 confirmed_by text NOT NULL,
 confirmed_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (project_id, rule_hash)
);
CREATE TRIGGER artifact_touch BEFORE UPDATE ON artifact FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER artifact_copy_touch BEFORE UPDATE ON artifact_copy FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER store_touch BEFORE UPDATE ON store FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
-- The latest stored result of a task, in the record shape the API returned for artifact documents.
CREATE OR REPLACE VIEW task_view WITH (security_invoker = true) AS
 SELECT t.id AS task_id, t.project_id, t.state, t.wait_reason,
  coalesce((SELECT jsonb_object_agg(s.state, s.n) FROM (SELECT e.state, count(*) AS n FROM execution e WHERE e.task_id = t.id GROUP BY e.state) s), '{}') AS executions_by_state,
  (SELECT count(*) FROM execution e WHERE e.task_id = t.id) AS execution_count,
  (SELECT jsonb_build_object('tokens_input', sum(x.tokens_input), 'tokens_output', sum(x.tokens_output), 'tokens_cache_read', sum(x.tokens_cache_read), 'tokens_cache_write', sum(x.tokens_cache_write), 'wall_ms', sum(x.wall_ms), 'active_ms', sum(x.active_ms), 'cpu_ms', sum(x.cpu_ms), 'measured_wall', count(*) FILTER (WHERE x.wall_ms_basis = 'measured'), 'executions_with_totals', count(*))
   FROM execution_totals x WHERE x.execution_id IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)) AS totals,
  (SELECT r.v FROM (SELECT jsonb_build_object('id', coalesce(a.legacy_id, a.id), 'artifactId', a.id, 'executionId', a.execution_id, 'path', a.name, 'mediaType', a.media_type, 'size', a.size, 'sha256', a.content_hash) AS v, a.seq FROM artifact a WHERE a.state = 'stored' AND a.execution_id IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)
   UNION ALL SELECT d.data - 'contentBase64', d.seq FROM document d WHERE d.kind = 'artifact' AND d.data->>'executionId' IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)) r ORDER BY r.seq DESC LIMIT 1) AS latest_result,
  (SELECT coalesce(jsonb_agg(jsonb_build_object('id', q.id, 'kind', q.kind, 'text', q.text, 'options', q.options) ORDER BY q.seq), '[]') FROM question q WHERE q.task_id = t.id AND q.state = 'open') AS open_questions
 FROM task t;
ALTER TABLE store ENABLE ROW LEVEL SECURITY;
ALTER TABLE store FORCE ROW LEVEL SECURITY;
CREATE POLICY operator_scope ON store USING (current_setting('fleet.project_ids', true) = '*');
ALTER TABLE artifact ENABLE ROW LEVEL SECURITY;
ALTER TABLE artifact FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON artifact USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE artifact_copy ENABLE ROW LEVEL SECURITY;
ALTER TABLE artifact_copy FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON artifact_copy USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE purged_hash ENABLE ROW LEVEL SECURITY;
ALTER TABLE purged_hash FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON purged_hash USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE artifact_rule_confirmation ENABLE ROW LEVEL SECURITY;
ALTER TABLE artifact_rule_confirmation FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON artifact_rule_confirmation USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
-- Change feed for the artifact tables (operator app §4): an artifact or a copy of it is kind 'artifact' with its task and
-- session (Results, Task detail); a store is kind 'host' of its host (Hosts). Store polls that only move observed_at or
-- updated_at, and copy lease renewals, write no row. One line: the migration runner splits at a semicolon that ends a line.
CREATE FUNCTION fleet_artifact_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; p text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'store' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'observed_at' - 'updated_at') = (to_jsonb(NEW) - 'observed_at' - 'updated_at') THEN RETURN NULL; END IF; k := 'host'; i := r.host_id; h := r.host_id; ELSE IF TG_TABLE_NAME = 'artifact_copy' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'lease_expires_at' - 'updated_at') = (to_jsonb(NEW) - 'lease_expires_at' - 'updated_at') THEN RETURN NULL; END IF; i := r.artifact_id; ELSE i := r.id; END IF; k := 'artifact'; p := r.project_id; SELECT coalesce(a.task_id, e.task_id), e.session_id INTO t, s FROM artifact a LEFT JOIN execution e ON e.id = a.execution_id WHERE a.id = i; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, p, left(TG_OP, 1)); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;
CREATE TRIGGER store_change AFTER INSERT OR UPDATE OR DELETE ON store FOR EACH ROW EXECUTE FUNCTION fleet_artifact_change();
CREATE TRIGGER artifact_change AFTER INSERT OR UPDATE OR DELETE ON artifact FOR EACH ROW EXECUTE FUNCTION fleet_artifact_change();
CREATE TRIGGER artifact_copy_change AFTER INSERT OR UPDATE OR DELETE ON artifact_copy FOR EACH ROW EXECUTE FUNCTION fleet_artifact_change()

-- migration 10: derived task values for the operator list (docs/designs/fleet-view-urls-and-list-path.md §3).
-- task_derived holds what task_page used to compute per task with correlated subqueries. Triggers on execution, question
-- and task keep it current; execution and question writes never touch task rows. task_derived_values is the one definition
-- of the values; it and the functions below read all of a task's rows while fleet.project_ids is '*' (FORCE row-level
-- security applies to the owner too; how the setting is switched is explained above the functions). fleet_task_derived locks the task's task_derived row before reading, so
-- recomputes of one task run one after another. A row lock, not an advisory lock: an advisory lock per task exhausts the
-- shared lock table when one transaction writes thousands of tasks. Today only GLOBAL writers change the trigger columns
-- (data foundation §12), so the lock is a second guard.
CREATE TABLE task_derived(
 task_id text PRIMARY KEY REFERENCES task(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 agent text NULL,
 host text NULL,
 started_at timestamptz NULL,
 open_questions integer NOT NULL DEFAULT 0
);
ALTER TABLE task_derived ENABLE ROW LEVEL SECURITY;
ALTER TABLE task_derived FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON task_derived USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
-- execution_task_seq serves the recompute and every lookup by task_id, so it replaces execution_task.
CREATE INDEX execution_task_seq ON execution(task_id, seq DESC);
DROP INDEX execution_task;
CREATE INDEX task_priority_order ON task((CASE priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END), seq DESC, id);
-- One line each: the migration runner splits at a semicolon that ends a line.
-- Scope: these functions must read all of one task's rows whatever the writer's scope (FORCE row-level security applies to the
-- owner too). A function-level SET of fleet.project_ids is superuser-only for a custom setting on PostgreSQL 15+ (it failed
-- on PDS18 as the owner role fleet), so the two entry points save the setting, set '*' with set_config(..., true) and set the
-- saved value back. No exception block: if the call fails, its (sub)transaction rolls back and Postgres restores the
-- setting itself; an exception block would open one subtransaction per write.
-- A development database may hold functions of an earlier draft of this migration; drop every variant first.
DROP FUNCTION IF EXISTS task_derived_drift(text);
DROP FUNCTION IF EXISTS task_derived_drift();
DROP FUNCTION IF EXISTS fleet_task_derived(text);
DROP FUNCTION IF EXISTS task_derived_recompute(text);
DROP FUNCTION IF EXISTS task_derived_values(text);
-- task_derived_values sees every project while '*' is set, so only the definer functions below may call it.
CREATE FUNCTION task_derived_values(p_task text) RETURNS TABLE(project_id text, agent text, host text, started_at timestamptz, open_questions integer) LANGUAGE sql STABLE SET search_path = public AS $$ SELECT (SELECT t.project_id FROM task t WHERE t.id = p_task), (SELECT e.harness FROM execution e WHERE e.task_id = p_task AND e.state IN ('preparing', 'running') ORDER BY e.seq DESC LIMIT 1), (SELECT e.host_id FROM execution e WHERE e.task_id = p_task ORDER BY e.seq DESC LIMIT 1), (SELECT min(e.started_at) FROM execution e WHERE e.task_id = p_task), (SELECT count(*)::int FROM question q WHERE q.task_id = p_task AND q.state = 'open') $$;
REVOKE EXECUTE ON FUNCTION task_derived_values(text) FROM PUBLIC;
CREATE FUNCTION task_derived_recompute(p_task text) RETURNS void LANGUAGE plpgsql AS $$ DECLARE v record; BEGIN IF NOT EXISTS (SELECT 1 FROM task WHERE id = p_task) THEN RETURN; END IF; PERFORM 1 FROM task_derived WHERE task_id = p_task FOR UPDATE; IF NOT FOUND THEN INSERT INTO task_derived(task_id, project_id, agent, host, started_at, open_questions) SELECT p_task, x.* FROM task_derived_values(p_task) x ON CONFLICT (task_id) DO NOTHING; IF FOUND THEN RETURN; END IF; PERFORM 1 FROM task_derived WHERE task_id = p_task FOR UPDATE; END IF; SELECT * INTO v FROM task_derived_values(p_task); UPDATE task_derived d SET project_id = v.project_id, agent = v.agent, host = v.host, started_at = v.started_at, open_questions = v.open_questions WHERE d.task_id = p_task AND (d.project_id, d.agent, d.host, d.started_at, d.open_questions) IS DISTINCT FROM (v.project_id, v.agent, v.host, v.started_at, v.open_questions); END $$;
REVOKE EXECUTE ON FUNCTION task_derived_recompute(text) FROM PUBLIC;
CREATE FUNCTION fleet_task_derived(p_task text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$ DECLARE prev text := current_setting('fleet.project_ids', true); BEGIN IF p_task IS NULL THEN RETURN; END IF; PERFORM set_config('fleet.project_ids', '*', true); PERFORM task_derived_recompute(p_task); PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true); END $$;
-- Drift: tasks whose stored values differ from a fresh computation (or have no row). Values are computed with all projects
-- visible; the output is limited to the tasks the caller may see (the caller's fleet.project_ids, read before switching),
-- with the same rule as the project_scope policies.
CREATE FUNCTION task_derived_drift() RETURNS TABLE(task_id text, present boolean, stored jsonb, expected jsonb) LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$ DECLARE prev text := current_setting('fleet.project_ids', true); BEGIN PERFORM set_config('fleet.project_ids', '*', true); RETURN QUERY SELECT t.id, d.task_id IS NOT NULL, CASE WHEN d.task_id IS NULL THEN NULL ELSE jsonb_build_object('projectId', d.project_id, 'agent', d.agent, 'host', d.host, 'startedAt', d.started_at, 'openQuestions', d.open_questions) END, jsonb_build_object('projectId', v.project_id, 'agent', v.agent, 'host', v.host, 'startedAt', v.started_at, 'openQuestions', v.open_questions) FROM task t CROSS JOIN LATERAL task_derived_values(t.id) v LEFT JOIN task_derived d ON d.task_id = t.id WHERE (prev = '*' OR coalesce(t.project_id, '') = ANY (string_to_array(coalesce(prev, ''), ','))) AND (d.task_id IS NULL OR (d.project_id, d.agent, d.host, d.started_at, d.open_questions) IS DISTINCT FROM (v.project_id, v.agent, v.host, v.started_at, v.open_questions)) ORDER BY t.id; PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true); END $$;
CREATE FUNCTION fleet_task_derived_touch() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE a text; b text; BEGIN IF TG_TABLE_NAME = 'task' THEN PERFORM fleet_task_derived(NEW.id); RETURN NULL; END IF; IF TG_OP <> 'INSERT' THEN a := OLD.task_id; END IF; IF TG_OP <> 'DELETE' THEN b := NEW.task_id; END IF; IF a IS NOT NULL AND b IS NOT NULL AND a <> b THEN PERFORM fleet_task_derived(least(a, b)); PERFORM fleet_task_derived(greatest(a, b)); ELSE PERFORM fleet_task_derived(coalesce(b, a)); END IF; RETURN NULL; END $$;
-- seq is in the list because agent and host are "latest by seq" (migrate-sqlite rewrites seq when it keeps a record).
CREATE TRIGGER execution_derived AFTER INSERT OR DELETE OR UPDATE OF task_id, state, host_id, harness, started_at, seq ON execution FOR EACH ROW EXECUTE FUNCTION fleet_task_derived_touch();
CREATE TRIGGER question_derived AFTER INSERT OR DELETE OR UPDATE OF task_id, state ON question FOR EACH ROW EXECUTE FUNCTION fleet_task_derived_touch();
CREATE TRIGGER task_derived_project AFTER INSERT OR UPDATE OF project_id ON task FOR EACH ROW EXECUTE FUNCTION fleet_task_derived_touch();
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1)); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;
-- Backfill before the change trigger exists: existing tasks need no change row each (clients resync from their cursor).
-- The backfill reads every project itself instead of relying on the migration session's scope.
SELECT set_config('fleet.project_ids', '*', true);
INSERT INTO task_derived(task_id, project_id, agent, host, started_at, open_questions) SELECT t.id, v.* FROM task t CROSS JOIN LATERAL task_derived_values(t.id) v ON CONFLICT (task_id) DO NOTHING;
CREATE TRIGGER task_derived_change AFTER INSERT OR UPDATE OR DELETE ON task_derived FOR EACH ROW EXECUTE FUNCTION fleet_change();
-- migration 11: Lab groundwork (docs/designs/fleet-lab.md "Data contracts"). The new value 'rating' is not used in this migration
-- (Postgres refuses a new enum value in the transaction that added it). Old lab experiments are deleted (owner decision; not
-- restorable by design); the executions they ran stay as plain executions without the experiment link.
ALTER TYPE question_kind ADD VALUE 'rating';
ALTER TABLE execution ADD COLUMN request_id text;
CREATE UNIQUE INDEX execution_request ON execution(request_id) WHERE request_id IS NOT NULL;
CREATE TABLE experiment(
 id text PRIMARY KEY,
 project_id text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('single', 'ab', 'loop')),
 pipeline_id text NOT NULL,
 examples jsonb NOT NULL DEFAULT '[]',
 variants jsonb NOT NULL DEFAULT '[]',
 repeats int NOT NULL DEFAULT 1,
 evaluators jsonb NOT NULL DEFAULT '[]',
 primary_evaluator text NULL,
 strategy jsonb NULL,
 budgets jsonb NOT NULL DEFAULT '[]',
 state text NOT NULL DEFAULT 'draft' CHECK (state IN ('draft', 'running', 'paused', 'stopped', 'done')),
 stop_reason text NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
CREATE TABLE score(
 id text PRIMARY KEY,
 experiment_id text NOT NULL REFERENCES experiment(id) ON DELETE CASCADE,
 project_id text NOT NULL,
 trial_ids text[] NOT NULL,
 evaluator text NOT NULL,
 evaluator_version text NOT NULL,
 form text NOT NULL CHECK (form IN ('metric', 'pass_fail', 'pick_best', 'scale')),
 value jsonb NULL,
 source_execution_id text NULL REFERENCES execution(id),
 question_id text NULL REFERENCES question(id),
 rationale text NULL,
 replaces_score_id text NULL REFERENCES score(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
CREATE INDEX score_experiment ON score(experiment_id);
CREATE INDEX score_trials ON score USING gin(trial_ids);
ALTER TABLE experiment ENABLE ROW LEVEL SECURITY;
ALTER TABLE experiment FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON experiment USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
ALTER TABLE score ENABLE ROW LEVEL SECURITY;
ALTER TABLE score FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON score USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1)); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;
CREATE TRIGGER experiment_change AFTER INSERT OR UPDATE OR DELETE ON experiment FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER score_change AFTER INSERT OR UPDATE OR DELETE ON score FOR EACH ROW EXECUTE FUNCTION fleet_change();
DELETE FROM document WHERE kind = 'experiment';
UPDATE execution SET experiment_id = NULL, variant = NULL, data = data - 'experimentId' WHERE experiment_id IS NOT NULL OR data ? 'experimentId';
-- migration 12: Lab examples (docs/designs/fleet-lab.md "Use-case registry and examples"). An example is a lab-scope artifact with role 'example' (the
-- example JSON, written by the coordinator); approved_at is set when the owner approves it and stays null while proposed.
ALTER TABLE artifact DROP CONSTRAINT artifact_role_check;
ALTER TABLE artifact ADD CONSTRAINT artifact_role_check CHECK (role IN ('original', 'thumbnail', 'poster', 'example'));
ALTER TABLE artifact ADD COLUMN approved_at timestamptz NULL;
-- One live example artifact per project and name (name carries id and version); a purged one does not block a re-import.
CREATE UNIQUE INDEX artifact_example ON artifact((coalesce(project_id, '')), name) WHERE role = 'example' AND state <> 'deleted';

-- migration 13: people (docs/designs/fleet-people-access.md §1, slice 1). Loaded from people.json by people.ts; the file is the
-- source of truth and these rows are its last valid load. A person reads their own rows; '*' scope reads all.
CREATE TABLE person(
 email text PRIMARY KEY,
 name text NOT NULL,
 roles text[] NOT NULL,
 owner boolean NOT NULL DEFAULT false,
 loaded_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE person_grant(
 email text NOT NULL REFERENCES person(email) ON DELETE CASCADE,
 object_kind text NOT NULL,
 object_id text NOT NULL,
 PRIMARY KEY (email, object_kind, object_id)
);
CREATE UNIQUE INDEX person_one_owner ON person(owner) WHERE owner;
ALTER TABLE person ENABLE ROW LEVEL SECURITY;
ALTER TABLE person FORCE ROW LEVEL SECURITY;
CREATE POLICY person_scope ON person USING (current_setting('fleet.project_ids', true) = '*' OR email = current_setting('fleet.person', true));
ALTER TABLE person_grant ENABLE ROW LEVEL SECURITY;
ALTER TABLE person_grant FORCE ROW LEVEL SECURITY;
CREATE POLICY person_scope ON person_grant USING (current_setting('fleet.project_ids', true) = '*' OR email = current_setting('fleet.person', true));

-- migration 14: host settings (docs/designs/fleet-data-foundation.md §11; people-access slice 2). A setting the executor
-- applies on its host (store capacity, service limits); applied_at and apply_error are the host's report.
CREATE TABLE host_setting(
 host_id text NOT NULL,
 key text NOT NULL,
 value jsonb NOT NULL,
 updated_by text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 applied_at timestamptz NULL,
 apply_error text NULL,
 PRIMARY KEY (host_id, key)
);
ALTER TABLE host_setting ENABLE ROW LEVEL SECURITY;
ALTER TABLE host_setting FORCE ROW LEVEL SECURITY;
CREATE POLICY operator_scope ON host_setting USING (current_setting('fleet.project_ids', true) = '*');
-- migration 15: Conversations Stage 1 (docs/designs/fleet-conversations.md). Stream deltas are documents of kind 'event-delta';
-- the change feed reports them as kind 'event', so the thread follows a stream live.
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1)); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;

-- migration 16: per-person visibility (docs/designs/fleet-people-access.md §2, slice 3). A person sees the projects of their
-- grants (fleet.project_ids, set per transaction) and every host, subscription or API key connection document, or store, they
-- hold (fleet.person). The coordinator's own scope '*' is unchanged.
DROP POLICY project_scope ON document;
CREATE POLICY project_scope ON document USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')) OR (kind IN ('host', 'managed-account', 'api-connection') AND data->>'holder' = current_setting('fleet.person', true)) OR (kind = 'host-observation' AND data->>'holder' = current_setting('fleet.person', true)));
ALTER TABLE store ADD COLUMN holder text NULL;
DROP POLICY operator_scope ON store;
CREATE POLICY operator_scope ON store USING (current_setting('fleet.project_ids', true) = '*' OR holder = current_setting('fleet.person', true));

-- migration 17: director and take (docs/designs/fleet-people-access.md §5, slice 4). directed_by is the person whose work a task
-- or execution is (the coordinator sets it; children inherit it); requested_by is the original director and never changes;
-- taken_from is the director before a take. Nullable: rows from before people-access have none (no backfill, owner guidance).
ALTER TABLE execution ADD COLUMN directed_by text NULL;
ALTER TABLE task ADD COLUMN directed_by text NULL;
ALTER TABLE task ADD COLUMN requested_by text NULL;
ALTER TABLE task ADD COLUMN taken_from text NULL;
-- Subscriptions get the slice 4 fields the new code reads: holder (the owner, the only person before people-access), intake from
-- enabled, an empty take rule. Needed to start; nothing else of the old records is kept.
UPDATE document SET data = (data - 'enabled') || jsonb_build_object('holder', (SELECT email FROM person WHERE owner), 'intake', CASE WHEN data->>'enabled' = 'false' THEN 'paused' ELSE 'all' END, 'kind', 'personal', 'take', '{"projects":[]}'::jsonb) WHERE kind = 'managed-account' AND EXISTS (SELECT 1 FROM person WHERE owner);
DELETE FROM document WHERE kind IN ('project-account', 'account-action');
-- A holder reads and writes the setting changes of their own host and subscriptions (rows carry the holder).
DROP POLICY project_scope ON document;
CREATE POLICY project_scope ON document USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')) OR (kind IN ('host', 'managed-account', 'api-connection', 'host-observation', 'setting-change') AND data->>'holder' = current_setting('fleet.person', true)));

-- migration 18: dispatch (docs/designs/fleet-people-access.md §6-7 and §9, slice 6). A queued agent execution has no host until
-- dispatch claims it on a host poll; wait_reason says why it has not started; route_decision records each claim or new wait
-- reason; a stall question has a cause key, one open question per cause; addressed_to lets the person a question is for read it.
ALTER TABLE execution ADD COLUMN wait_reason text NULL;
CREATE INDEX execution_undispatched ON execution(seq) WHERE host_id IS NULL AND state = 'preparing';
ALTER TABLE question ADD COLUMN cause text NULL;
ALTER TABLE question ADD COLUMN addressed_to text NULL;
CREATE UNIQUE INDEX question_open_cause ON question(cause) WHERE state = 'open' AND cause IS NOT NULL;
DROP POLICY project_scope ON question;
CREATE POLICY project_scope ON question USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')) OR addressed_to = current_setting('fleet.person', true));
CREATE TABLE route_decision(
 id bigserial PRIMARY KEY,
 execution_id text NOT NULL,
 project_id text NULL,
 at timestamptz NOT NULL DEFAULT now(),
 host_id text NULL,
 candidates jsonb NOT NULL,
 winner jsonb NULL
);
CREATE INDEX route_decision_execution ON route_decision(execution_id, at);
ALTER TABLE route_decision ENABLE ROW LEVEL SECURITY;
ALTER TABLE route_decision FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON route_decision USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
CREATE TABLE budget(
 id text PRIMARY KEY,
 scope text NOT NULL CHECK (scope IN ('person', 'project', 'experiment', 'connection')),
 scope_id text NOT NULL,
 unit text NOT NULL,
 "limit" numeric NOT NULL,
 period text NULL CHECK (period IN ('day', 'week', 'month')),
 action text NOT NULL CHECK (action IN ('warn', 'pause', 'stop')),
 set_by text NOT NULL,
 set_at timestamptz NOT NULL DEFAULT now()
);
ALTER TABLE budget ENABLE ROW LEVEL SECURITY;
ALTER TABLE budget FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON budget USING (current_setting('fleet.project_ids', true) = '*' OR (scope = 'project' AND scope_id = ANY (string_to_array(current_setting('fleet.project_ids', true), ','))) OR (scope = 'person' AND scope_id = current_setting('fleet.person', true)));

-- migration 19: limit readings (docs/designs/fleet-people-access.md §8, slice 7) are read from limit_reading events; this keeps
-- that read off a scan of every event.
CREATE INDEX document_limit_reading ON document(seq) WHERE kind = 'event' AND data->>'type' = 'limit_reading';

-- migration 20: Held tasks (docs/designs/fleet-pm-agent.md §4). A Held task is paused with pause_reason 'held': an agent's task
-- waits for release before its first queue. NULL is an ordinary pause.
ALTER TABLE task ADD COLUMN pause_reason text NULL;

-- migration 21: the actor of a change (docs/designs/fleet-pm-agent.md §5): the agent execution whose call wrote it
-- (fleet.actor), NULL for people and the coordinator. A watching agent skips its own changes by it.
ALTER TABLE change ADD COLUMN actor text NULL;
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), '')); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;

-- migration 22: host jobs (copy, verify, account inspection, integration, script steps) are their own execution kind.
ALTER TYPE execution_kind ADD VALUE 'host_job';

-- migration 23: one execution record (docs/designs/fleet-data-foundation.md §7): its state and observation live in their
-- columns only, in the typed vocabulary, with the real kind and harness; data keeps the open remainder.
UPDATE execution SET kind = 'host_job', harness = NULL WHERE legacy_kind = 'execution' AND (data->>'actor' = 'program-worker' OR data ? 'pipelineStepId');
UPDATE execution SET harness = coalesce(data->'agentConfig'->>'harness', 'codex') WHERE legacy_kind = 'execution' AND kind = 'agent_run';
UPDATE execution SET data = data - 'state' - 'observation', legacy_kind = NULL, legacy_id = NULL WHERE legacy_kind = 'execution';

-- migration 24: pipeline runs and steps keep their state only in the typed column too; an unknown outcome is failed and
-- unconfirmed. No execution row has a legacy kind or id any more.
UPDATE execution SET state = 'failed', observation = 'unconfirmed' WHERE legacy_kind IN ('pipeline-run', 'pipeline-step') AND data->>'state' = 'unknown';
UPDATE execution SET data = data || jsonb_build_object('pipelineId', coalesce(data->'definition'->>'id', id)) WHERE legacy_kind = 'pipeline-run' AND NOT data ? 'pipelineId';
UPDATE execution SET data = data - 'state' - 'observation' WHERE legacy_kind IN ('pipeline-run', 'pipeline-step');
CREATE INDEX execution_kind ON execution(kind, seq);
ALTER TABLE execution DROP COLUMN legacy_kind, DROP COLUMN legacy_id;

-- migration 25: holder-scoped documents the app follows (Keys, subscription limits) write changes of their own kind (C14).
-- holder: the document's holder, who sees the change like the document itself (the document policy's holder clause).
ALTER TABLE change ADD COLUMN holder text NULL;
DROP POLICY project_scope ON change;
CREATE POLICY project_scope ON change USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')) OR holder = current_setting('fleet.person', true));
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change') THEN k := r.kind; w := r.data->>'holder'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;

-- migration 26: the experiment record (docs/designs/fleet-lab.md, Experiment record). goal and hypothesis are required at
-- launch; the record fields are frozen from launch on, and a later change is an append-only experiment_note amendment.
-- decision is written at close (pending until a person or the decision rule settles it). Scores outlive their experiment.
ALTER TABLE experiment ADD COLUMN goal text NULL, ADD COLUMN hypothesis text NULL, ADD COLUMN decision_rule text NULL,
 ADD COLUMN lab_group text NULL, ADD COLUMN decision text NULL, ADD COLUMN decision_reason text NULL,
 ADD COLUMN decided_by text NULL, ADD COLUMN decided_at timestamptz NULL, ADD COLUMN launched_at timestamptz NULL;
ALTER TABLE experiment ADD CONSTRAINT experiment_decision CHECK (decision IS NULL OR decision IN ('keep baseline', 'inconclusive', 'pending') OR decision LIKE 'adopt %');
ALTER TABLE score DROP CONSTRAINT score_experiment_id_fkey;
CREATE TABLE experiment_note(
 id text PRIMARY KEY,
 experiment_id text NOT NULL REFERENCES experiment(id),
 project_id text NOT NULL,
 kind text NOT NULL CHECK (kind IN ('amendment', 'note')),
 field text NULL,
 old jsonb NULL,
 new jsonb NULL,
 text text NOT NULL,
 author text NOT NULL,
 at timestamptz NOT NULL DEFAULT now(),
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
CREATE INDEX experiment_note_experiment ON experiment_note(experiment_id, seq);
ALTER TABLE experiment_note ENABLE ROW LEVEL SECURITY;
ALTER TABLE experiment_note FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON experiment_note USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')));
-- The one place for the record's state rules: a stop or completion leaves the decision pending; launch stamps launched_at.
CREATE OR REPLACE FUNCTION fleet_experiment_record() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.state IN ('stopped', 'done') AND NEW.decision IS NULL THEN NEW.decision := 'pending'; END IF; IF OLD.state = 'draft' AND NEW.state <> 'draft' AND NEW.launched_at IS NULL THEN NEW.launched_at := now(); END IF; RETURN NEW; END $$;
CREATE TRIGGER experiment_record BEFORE UPDATE ON experiment FOR EACH ROW EXECUTE FUNCTION fleet_experiment_record();
CREATE OR REPLACE FUNCTION fleet_note_append_only() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'experiment notes are append-only'; END $$;
CREATE TRIGGER experiment_note_append_only BEFORE UPDATE OR DELETE ON experiment_note FOR EACH ROW EXECUTE FUNCTION fleet_note_append_only();

-- migration 27: the price table (docs/designs/fleet-analytics.md, Price table). Append-only: a price change is a new entry.
-- A figure uses the latest entry of its subject and unit with effective_from <= the work's start; none gives unavailable.
CREATE TABLE price_entry(
 id text PRIMARY KEY,
 subject text NOT NULL,
 unit text NOT NULL,
 price numeric NOT NULL CHECK (price >= 0),
 currency text NOT NULL,
 source text NOT NULL CHECK (source IN ('vendor', 'snapshot', 'owner')),
 source_ref text NULL,
 observed_at timestamptz NOT NULL,
 effective_from timestamptz NOT NULL,
 created_by text NULL,
 seq bigint NOT NULL DEFAULT nextval(pg_get_serial_sequence('document', 'seq'))
);
CREATE INDEX price_entry_subject ON price_entry(subject, unit, effective_from);
-- A released migration is never edited (migration 26 shipped in 1.261111.37 with fleet_note_append_only); this one brings
-- its own function.
CREATE OR REPLACE FUNCTION fleet_append_only() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION '% rows are append-only', TG_TABLE_NAME; END $$;
CREATE TRIGGER price_entry_append_only BEFORE UPDATE OR DELETE ON price_entry FOR EACH ROW EXECUTE FUNCTION fleet_append_only();

-- migration 28 (27 is the price table of #249): definitions in the store (docs/designs/fleet-definition-editors.md, Data model). Content is the canonical
-- text (keys sorted, two-space indent, trailing newline), kept as text so diff, sha and export see the exact bytes.
-- Revisions are never updated or deleted. project_id stays null: definitions are shared, the change feed needs the column.
CREATE TABLE definition(
 kind text NOT NULL CHECK (kind IN ('agent', 'pipeline', 'catalogue')),
 id text NOT NULL,
 path text NOT NULL,
 head int NOT NULL,
 repo_sha text NULL,
 repo_content text NULL,
 repo_content_sha text NULL,
 ever_shipped boolean NOT NULL DEFAULT false,
 retired_at timestamptz NULL,
 project_id text NULL,
 PRIMARY KEY (kind, id)
);
CREATE TABLE definition_revision(
 kind text NOT NULL,
 id text NOT NULL,
 revision int NOT NULL,
 parent int NULL,
 content text NOT NULL,
 content_sha text NOT NULL,
 repo_base text NULL,
 source text NOT NULL CHECK (source IN ('repo', 'editor', 'lab', 'agent')),
 actor_kind text NOT NULL CHECK (actor_kind IN ('person', 'agent', 'system')),
 by_email text NULL,
 by_execution text NULL,
 directed_by text NULL,
 message text NOT NULL,
 at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (kind, id, revision),
 FOREIGN KEY (kind, id) REFERENCES definition
);
CREATE TABLE definition_draft(
 kind text NOT NULL CHECK (kind IN ('agent', 'pipeline', 'catalogue')),
 id text NOT NULL,
 person text NOT NULL,
 base int NOT NULL,
 content text NOT NULL,
 updated_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY (kind, id, person)
);
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change') THEN k := r.kind; w := r.data->>'holder'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;
CREATE TRIGGER definition_change AFTER INSERT OR UPDATE OR DELETE ON definition FOR EACH ROW EXECUTE FUNCTION fleet_change();

-- migration 29: typed-decision scores (docs/designs/fleet-lab.md, Statistics): a score of form 'typed' holds the trial's
-- answer through its variant's bands, the example's label and tag, and p.
ALTER TABLE score DROP CONSTRAINT IF EXISTS score_form_check;
ALTER TABLE score ADD CONSTRAINT score_form_check CHECK (form IN ('metric', 'pass_fail', 'pick_best', 'scale', 'typed'));

-- migration 30: executions pin the definitions they resolve (fleet-definition-editors.md, Resolve): a list of
-- DefinitionRef {kind, id, revision, contentSha, draft?}; draft_content holds a draft run's content by <kind>/<id>.
ALTER TABLE execution ADD COLUMN definitions jsonb NOT NULL DEFAULT '[]', ADD COLUMN draft_content jsonb NULL, ADD COLUMN rerun_of text NULL, ADD COLUMN reused_steps jsonb NULL;
CREATE INDEX execution_definitions ON execution USING gin (definitions jsonb_path_ops);
