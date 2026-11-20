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
-- DefinitionRef {kind, id, revision, contentSha, draft?}. The draft and rerun columns come with the operations that use them.
ALTER TABLE execution ADD COLUMN definitions jsonb NOT NULL DEFAULT '[]';
-- migration 31: binary Lab example files (docs/designs/fleet-asset-pipelines.md §4): stored with role 'example-file',
-- found by content hash.
ALTER TABLE artifact DROP CONSTRAINT artifact_role_check;
ALTER TABLE artifact ADD CONSTRAINT artifact_role_check CHECK (role IN ('original', 'thumbnail', 'poster', 'example', 'example-file'));
-- migration 32: questions are read from their columns (W5): the asker's request id is a column; the agent question record
-- and the project question are built from the columns, data keeps only fields without one. legacy_kind goes from question
-- and task, legacy_id from task.
ALTER TABLE question ADD COLUMN request_id text NULL;
UPDATE question SET request_id = data->>'requestId' WHERE legacy_kind IN ('question', 'project-question');
-- A column the record names but the row lacks takes the record's value first.
UPDATE question SET answer = coalesce(answer, data->>'answer'), answered_by = coalesce(answered_by, data->>'answeredBy'),
 answered_at = coalesce(answered_at, to_timestamp((data->>'answeredAt')::float8 / 1000)) WHERE legacy_kind IN ('question', 'project-question') AND data ? 'answer';
UPDATE question SET data = '{}' WHERE legacy_kind = 'project-question';
UPDATE question SET data = data - 'id' - 'executionId' - 'sessionId' - 'projectId' - 'requestId' - 'question' - 'state' - 'answer' - 'answeredBy' - 'answeredAt' - 'createdAt' WHERE legacy_kind = 'question';
DROP INDEX question_legacy;
DROP INDEX task_legacy;
ALTER TABLE question DROP COLUMN legacy_kind;
ALTER TABLE task DROP COLUMN legacy_kind, DROP COLUMN legacy_id;
CREATE INDEX question_request ON question(execution_id, request_id) WHERE request_id IS NOT NULL;
CREATE INDEX question_project_request ON question(project_id, request_id) WHERE request_id IS NOT NULL;
CREATE INDEX question_kind_state ON question(kind, state, seq);

-- migration 33: definition_usage reads executions by the definitions they pinned (containment).
CREATE INDEX execution_definitions ON execution USING gin (definitions jsonb_path_ops);


-- migration 34: Catalogue recommendations notify scoped live readers.
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change', 'project-recommendation') THEN k := r.kind; w := r.data->>'holder'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN k := 'execution'; i := r.execution_id; SELECT e.task_id, e.session_id, e.host_id INTO t, s, h FROM execution e WHERE e.id = r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;

-- migration 35: projects share the versioned definition owner; initial access is revocable and survives people reload.
ALTER TABLE definition DROP CONSTRAINT definition_kind_check;
ALTER TABLE definition ADD CONSTRAINT definition_kind_check CHECK (kind IN ('agent','pipeline','catalogue','project'));
ALTER TABLE definition ADD COLUMN created_by text, ADD COLUMN initial_member text,
 ADD COLUMN initial_member_revoked_at timestamptz, ADD COLUMN access_version bigint NOT NULL DEFAULT 1;
CREATE TABLE project_write_request(
 directing_email text NOT NULL, operation text NOT NULL, request_id text NOT NULL,
 payload_sha text NOT NULL, project_id text NOT NULL, result jsonb NOT NULL,
 PRIMARY KEY(directing_email,operation,request_id)
);

-- Own projectless conversations are directed work, not access to all rows without a project.
CREATE FUNCTION fleet_own_projectless_session(sid text) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE prev text := current_setting('fleet.project_ids',true); who text := current_setting('fleet.person',true); allowed boolean;
BEGIN
 IF coalesce(who,'')='' OR sid IS NULL THEN RETURN false; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 SELECT EXISTS(SELECT 1 FROM document WHERE kind='session' AND id=sid AND project_id IS NULL AND data->>'directedBy'=who AND NOT(data ? 'directorAccessRevokedAt')) INTO allowed;
 PERFORM set_config('fleet.project_ids',coalesce(prev,''),true); RETURN allowed;
END $$;
CREATE FUNCTION fleet_own_projectless_execution(eid text) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE prev text := current_setting('fleet.project_ids',true); who text := current_setting('fleet.person',true); sid text; director text;
BEGIN
 IF coalesce(who,'')='' OR eid IS NULL THEN RETURN false; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 SELECT session_id,directed_by INTO sid,director FROM execution WHERE id=eid AND project_id IS NULL;
 PERFORM set_config('fleet.project_ids',coalesce(prev,''),true);
 RETURN coalesce(director=who AND fleet_own_projectless_session(sid),false);
END $$;
CREATE POLICY directed_session ON document USING (project_id IS NULL AND (
 (kind='session' AND data->>'directedBy'=current_setting('fleet.person',true) AND NOT(data ? 'directorAccessRevokedAt')) OR
 (kind IN ('message','event','event-delta','session-transition','queued-message','handoff') AND fleet_own_projectless_session(data->>'sessionId')) OR
 (kind='session-fork' AND fleet_own_projectless_session(data->>'sessionId') AND fleet_own_projectless_session(data->>'sourceSessionId'))));
CREATE POLICY directed_execution ON execution USING (project_id IS NULL AND directed_by=current_setting('fleet.person',true) AND fleet_own_projectless_session(session_id));
CREATE POLICY directed_totals ON execution_totals USING (project_id IS NULL AND fleet_own_projectless_execution(execution_id));
CREATE POLICY directed_change ON change USING (project_id IS NULL AND fleet_own_projectless_session(session_id));
CREATE FUNCTION fleet_session_director_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.kind='session' AND OLD.data ? 'directedBy' AND OLD.data->>'directedBy' IS DISTINCT FROM NEW.data->>'directedBy' THEN RAISE EXCEPTION 'session director is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER session_director_immutable BEFORE UPDATE ON document FOR EACH ROW EXECUTE FUNCTION fleet_session_director_immutable();

-- migration 36: a host MCP mutation's acknowledgment commits with its side effect, before an HTTP retry.
-- Only the proved host-agent route reads these receipts, after current director and operation authorization.
CREATE TABLE agent_tool_receipt(
 id text PRIMARY KEY, execution_id text NOT NULL, directing_email text NOT NULL,
 operation text NOT NULL, payload_sha text NOT NULL, result jsonb NOT NULL, authority jsonb NOT NULL
);

-- migration 37: Lab retains its authenticated requester; legacy provenance remains unknown.
ALTER TABLE experiment ADD COLUMN requested_by text, ADD COLUMN requester_removed_at timestamptz;

-- migration 38: an agent's key request is a secret question; the owner answers it only through connections_key_add.
ALTER TYPE question_kind ADD VALUE 'secret';

-- migration 39: human entity codes (D17; docs/designs/fleet-data-foundation.md §2, Human codes). The UUID stays the key.
-- project_code: every code a project ever had (never reused); one current. A project without one gets a derived code
-- on its first coded row. code_counter issues numbers per (scope, type, section) by UPDATE ... RETURNING in the insert's
-- transaction. entity_code keeps every code issued; a moved entity or a renamed project gets a new code and the old one
-- stays as an alias (superseded_by). Codes are issued by triggers, so every writer of task and question is covered.
CREATE TABLE project_code(
 code text PRIMARY KEY CHECK (code ~ '^[A-Z][A-Z0-9]{1,4}$' AND code !~ '^[A-F0-9]+$' AND code !~ '[IOSZB]' AND code <> 'ALL'),
 project_id text NOT NULL, current boolean NOT NULL DEFAULT true, set_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX project_code_current ON project_code(project_id) WHERE current;
CREATE TABLE code_counter(scope text NOT NULL, type text NOT NULL, section text NOT NULL DEFAULT '', next bigint NOT NULL, PRIMARY KEY (scope, type, section));
CREATE TABLE entity_code(
 code text PRIMARY KEY, entity_id text NOT NULL, type text NOT NULL, project_id text NULL,
 issued_at timestamptz NOT NULL DEFAULT now(), superseded_by text NULL
);
CREATE INDEX entity_code_entity ON entity_code(entity_id);
ALTER TABLE entity_code ENABLE ROW LEVEL SECURITY;
ALTER TABLE entity_code FORCE ROW LEVEL SECURITY;
CREATE POLICY project_scope ON entity_code USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ','))
 OR (type = 'Q' AND EXISTS(SELECT 1 FROM question x WHERE x.id = entity_id AND x.addressed_to = current_setting('fleet.person', true))));
ALTER TABLE task ADD COLUMN code text UNIQUE;
ALTER TABLE question ADD COLUMN code text UNIQUE;

-- fleet_project_code: the project's current code; derives one when it has none: the first letter, then the letters and
-- digits that are not vowels or I, O, S, Z, B, three to five of them, then a digit while taken (FLEET -> FLT).
CREATE FUNCTION fleet_project_code(p_project text) RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE c text; base text; cand text; n int;
BEGIN
 IF p_project IS NULL OR p_project = '' THEN RETURN 'ALL'; END IF;
 SELECT code INTO c FROM project_code WHERE project_id=p_project AND current;
 IF c IS NOT NULL THEN RETURN c; END IF;
 -- ponytail: one lock for every derivation (rare: a project's first coded row), per-project locks if that ever contends
 PERFORM pg_advisory_xact_lock(hashtext('fleet_project_code_derive'));
 SELECT code INTO c FROM project_code WHERE project_id=p_project AND current;
 IF c IS NOT NULL THEN RETURN c; END IF;
 base := regexp_replace(upper(p_project), '[^A-Z0-9]', '', 'g');
 base := regexp_replace(coalesce(substr(regexp_replace(base, '^[^A-Z]+', ''), 1, 1), ''), '[IOSZB]', 'X') || regexp_replace(substr(regexp_replace(base, '^[^A-Z]+', ''), 2), '[AEIOUSZB]', '', 'g');
 IF length(base) < 1 THEN base := 'P'; END IF;
 base := rpad(base, 3, 'X');
 FOR n IN 3..5 LOOP
  cand := substr(base, 1, n);
  IF length(cand) = n AND cand !~ '^[A-F0-9]+$' AND cand <> 'ALL' AND NOT EXISTS(SELECT 1 FROM project_code WHERE code=cand) THEN
   INSERT INTO project_code(code, project_id) VALUES(cand, p_project); RETURN cand;
  END IF;
 END LOOP;
 FOR n IN 2..99 LOOP
  cand := substr(base, 1, 5 - length(n::text)) || n;
  IF cand !~ '^[A-F0-9]+$' AND NOT EXISTS(SELECT 1 FROM project_code WHERE code=cand) THEN
   INSERT INTO project_code(code, project_id) VALUES(cand, p_project); RETURN cand;
  END IF;
 END LOOP;
 FOR n IN 2..9999 LOOP -- a hex-like id (a1b2c3, added): P is not a hex digit
  cand := 'P' || n;
  IF NOT EXISTS(SELECT 1 FROM project_code WHERE code=cand) THEN
   INSERT INTO project_code(code, project_id) VALUES(cand, p_project); RETURN cand;
  END IF;
 END LOOP;
 RAISE EXCEPTION 'no free project code for %', p_project;
END $$;

-- fleet_issue_code: the next code of a type in the project's scope, recorded in entity_code.
CREATE FUNCTION fleet_issue_code(p_type text, p_project text, p_entity text) RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE s text; n bigint; c text;
BEGIN
 -- shared: issues run together; a project code change (fleet_project_code_set) waits for them and they for it
 PERFORM pg_advisory_xact_lock_shared(hashtext('fleet_project_code'));
 s := fleet_project_code(p_project);
 INSERT INTO code_counter(scope, type, next) VALUES(s, p_type, 2) ON CONFLICT(scope, type, section) DO UPDATE SET next=code_counter.next+1 RETURNING next-1 INTO n;
 c := p_type || '-' || s || '-' || n;
 INSERT INTO entity_code(code, entity_id, type, project_id) VALUES(c, p_entity, p_type, nullif(p_project, ''));
 RETURN c;
END $$;

-- A new row, or a row whose project changed, gets a code; the old code becomes an alias of the new one.
CREATE FUNCTION fleet_entity_code() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE t text := CASE TG_TABLE_NAME WHEN 'task' THEN 'T' ELSE 'Q' END; prev text;
BEGIN
 IF TG_OP = 'INSERT' THEN
  NEW.code := fleet_issue_code(t, NEW.project_id, NEW.id);
 ELSIF NEW.code IS NOT NULL AND NEW.code IS DISTINCT FROM OLD.code THEN
  RAISE EXCEPTION 'codes are issued, never written (set code to NULL to reissue)';
 ELSIF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.code IS NULL THEN
  IF NEW.project_id IS NOT DISTINCT FROM OLD.project_id THEN NEW.updated_at := OLD.updated_at; END IF; -- a reissue is not a change of the row
  NEW.code := fleet_issue_code(t, NEW.project_id, NEW.id);
  prev := current_setting('fleet.project_ids', true);
  PERFORM set_config('fleet.project_ids', '*', true);
  UPDATE entity_code SET superseded_by=NEW.code, project_id=nullif(NEW.project_id, '') WHERE entity_id=NEW.id AND type=t AND code<>NEW.code;
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 END IF;
 RETURN NEW;
END $$;

-- fleet_project_code_set: a new current code for a project; every task and question of the project gets a new code and
-- its old codes resolve to it.
CREATE FUNCTION fleet_project_code_set(p_project text, p_code text) RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE r record; prev text := current_setting('fleet.project_ids', true);
BEGIN
 PERFORM pg_advisory_xact_lock(hashtext('fleet_project_code'));
 IF EXISTS(SELECT 1 FROM project_code WHERE code=p_code AND project_id=p_project AND current) THEN RETURN p_code; END IF;
 IF EXISTS(SELECT 1 FROM project_code WHERE code=p_code) THEN RAISE EXCEPTION 'project code % was used', p_code USING ERRCODE='23505'; END IF;
 UPDATE project_code SET current=false WHERE project_id=p_project AND current;
 INSERT INTO project_code(code, project_id) VALUES(p_code, p_project);
 PERFORM set_config('fleet.project_ids', '*', true); -- every row of the project, whatever the caller's scope (the op checked the project)
 FOR r IN SELECT id FROM task WHERE project_id=p_project ORDER BY created_at, id LOOP UPDATE task SET code=NULL WHERE id=r.id; END LOOP;
 FOR r IN SELECT id FROM question WHERE project_id=p_project ORDER BY asked_at, id LOOP UPDATE question SET code=NULL WHERE id=r.id; END LOOP;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN p_code;
END $$;

-- Named to run after task_touch and question_touch (BEFORE triggers run by name), so a reissue keeps updated_at.
CREATE TRIGGER task_zcode BEFORE INSERT OR UPDATE OF project_id, code ON task FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();
CREATE TRIGGER question_zcode BEFORE INSERT OR UPDATE OF project_id, code ON question FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();
-- Fleet's own project keeps the code the D17 examples use.
INSERT INTO project_code(code, project_id) VALUES('FLT', 'fleet');
-- Backfill in created_at order, filling only empty values. Generated delivery titles carried a session UUID; it goes.
-- No change row per backfilled row (as migration 10): clients load codes with the rows.
ALTER TABLE task DISABLE TRIGGER task_change;
ALTER TABLE question DISABLE TRIGGER question_change;
DO $$ DECLARE r record; BEGIN
 PERFORM set_config('fleet.project_ids', '*', true);
 FOR r IN SELECT id FROM task WHERE code IS NULL ORDER BY created_at, seq LOOP UPDATE task SET code=NULL WHERE id=r.id; END LOOP;
 FOR r IN SELECT id FROM question WHERE code IS NULL ORDER BY asked_at, seq LOOP UPDATE question SET code=NULL WHERE id=r.id; END LOOP;
END $$;
ALTER TABLE task ENABLE TRIGGER task_change;
ALTER TABLE question ENABLE TRIGGER question_change;
UPDATE task SET title='Deliver session' WHERE title ~* '^Deliver session [0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$';

-- migration 40: agent-run tokens from the harness's own usage report (the last result or turn.completed event with
-- nativeUsage; the scope is the run, so the last report is taken, never a sum). Claude reports input, output,
-- cache_read_input_tokens and cache_creation_input_tokens; Codex reports input_tokens including cached_input_tokens
-- (input is stored without them) and, from the app server, {total, last}: total spans the whole thread (earlier runs
-- too), so last is taken; a run of several turns then counts its last turn only (ponytail: diff total against the
-- previous run of the thread when steered runs matter). Backfills every
-- finished run; writeTotals calls it on each end.
-- A count only from a whole JSON number; anything else a host sent is unknown, never an error that blocks the run's end.
CREATE FUNCTION fleet_token_count(v jsonb) RETURNS bigint LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN jsonb_typeof(v)='number' AND v::text ~ '^[0-9]{1,18}$' THEN v::text::bigint END
$$;
CREATE FUNCTION fleet_write_tokens(eid text) RETURNS void LANGUAGE sql AS $$
 WITH ev AS (SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM (
  SELECT data->'data'->'fleetNormalized'->'nativeUsage' AS u FROM document
  WHERE kind='event' AND data->>'executionId'=eid AND data->'data'->'fleetNormalized'->>'lifecycle' IN ('result','turn.completed')
   AND jsonb_typeof(data->'data'->'fleetNormalized'->'nativeUsage')='object' ORDER BY seq DESC LIMIT 1) r),
 n AS (SELECT
  fleet_token_count(coalesce(u->'input_tokens',u->'inputTokens')) AS input,
  fleet_token_count(coalesce(u->'output_tokens',u->'outputTokens')) AS output,
  fleet_token_count(coalesce(u->'cache_read_input_tokens',u->'cached_input_tokens',u->'cachedInputTokens')) AS cache_read,
  fleet_token_count(u->'cache_creation_input_tokens') AS cache_write,
  (u ? 'cached_input_tokens' OR u ? 'cachedInputTokens') AS input_has_cache FROM ev)
 UPDATE execution_totals SET
  tokens_input=CASE WHEN n.input_has_cache THEN n.input-coalesce(n.cache_read,0) ELSE n.input END,
  tokens_input_basis=CASE WHEN n.input IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_output=n.output, tokens_output_basis=CASE WHEN n.output IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_cache_read=n.cache_read, tokens_cache_read_basis=CASE WHEN n.cache_read IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_cache_write=n.cache_write, tokens_cache_write_basis=CASE WHEN n.cache_write IS NULL THEN 'unavailable' ELSE 'measured' END
 FROM n WHERE execution_id=eid
$$;
SELECT fleet_write_tokens(execution_id) FROM execution_totals t JOIN execution e ON e.id=t.execution_id WHERE e.kind='agent_run';

-- migration 41: migration 40's backfill ran under FORCE ROW LEVEL SECURITY without a project scope, so it saw no rows;
-- run it again with every project in scope (this transaction only).
SELECT set_config('fleet.project_ids', '*', true);
SELECT fleet_write_tokens(execution_id) FROM execution_totals t JOIN execution e ON e.id=t.execution_id WHERE e.kind='agent_run';

-- migration 42: conversations carry C- codes like tasks and questions (D17); project codes come from the name's words
-- (fleet-hello-world -> FHW, desiign -> DSG) and avoid only I and O (owner review 2026-11-13).
ALTER TABLE project_code DROP CONSTRAINT project_code_code_check;
ALTER TABLE project_code ADD CONSTRAINT project_code_code_check CHECK (code ~ '^[A-Z][A-Z0-9]{1,4}$' AND code !~ '^[A-F0-9]+$' AND code !~ '[IO]' AND code <> 'ALL');
ALTER TABLE document ADD COLUMN code text UNIQUE;
-- A person reads the code of their own conversation without a project, as they read the conversation.
DROP POLICY project_scope ON entity_code;
CREATE POLICY project_scope ON entity_code USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ','))
 OR (type = 'Q' AND EXISTS(SELECT 1 FROM question x WHERE x.id = entity_id AND x.addressed_to = current_setting('fleet.person', true)))
 OR (type = 'C' AND project_id IS NULL AND fleet_own_projectless_session(entity_id)));

-- fleet_project_code: the project's current code; derives one when it has none. Several words: their initials
-- (hello-world-alt -> HWA), then letters of the last word; one word: its first letter and the consonants after it,
-- three of them (fleet -> FLT). I and O are left out; then a digit, then P and a number, while taken.
CREATE OR REPLACE FUNCTION fleet_project_code(p_project text) RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE c text; w text[]; x text; base text := ''; more text; cands text[] := '{}'; cand text; n int;
BEGIN
 IF p_project IS NULL OR p_project = '' THEN RETURN 'ALL'; END IF;
 SELECT code INTO c FROM project_code WHERE project_id=p_project AND current;
 IF c IS NOT NULL THEN RETURN c; END IF;
 PERFORM pg_advisory_xact_lock(hashtext('fleet_project_code_derive'));
 SELECT code INTO c FROM project_code WHERE project_id=p_project AND current;
 IF c IS NOT NULL THEN RETURN c; END IF;
 w := array_remove(regexp_split_to_array(translate(upper(p_project), 'IO', ''), '[^A-Z0-9]+'), '');
 IF coalesce(array_length(w, 1), 0) = 0 THEN w := ARRAY['P']; END IF;
 IF array_length(w, 1) >= 2 THEN
  FOREACH x IN ARRAY w LOOP base := base || left(x, 1); END LOOP;
  more := regexp_replace(substr(w[array_length(w, 1)], 2), '[AEUY]', '', 'g');
  cands := ARRAY[left(base, 5), left(base || more, 4), left(base || more, 5)];
 ELSE
  base := left(w[1], 1) || regexp_replace(substr(w[1], 2), '[AEUY]', '', 'g');
  cands := ARRAY[left(base, 3), left(base, 4), left(base, 5), left(base, 2)];
 END IF;
 IF cands[1] ~ '^[0-9]' THEN cands := ARRAY['P' || left(cands[1], 4)]; END IF;
 FOR n IN 2..9 LOOP cands := cands || (left(cands[1], 4) || n); END LOOP;
 FOREACH cand IN ARRAY cands LOOP
  IF cand ~ '^[A-Z][A-Z0-9]{1,4}$' AND cand !~ '^[A-F0-9]+$' AND cand <> 'ALL' AND NOT EXISTS(SELECT 1 FROM project_code WHERE code=cand) THEN
   INSERT INTO project_code(code, project_id) VALUES(cand, p_project); RETURN cand;
  END IF;
 END LOOP;
 FOR n IN 2..9999 LOOP -- a hex-like or crowded name: P and a number
  IF NOT EXISTS(SELECT 1 FROM project_code WHERE code='P' || n) THEN
   INSERT INTO project_code(code, project_id) VALUES('P' || n, p_project); RETURN 'P' || n;
  END IF;
 END LOOP;
 RAISE EXCEPTION 'no free project code for %', p_project;
END $$;

CREATE OR REPLACE FUNCTION fleet_entity_code() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE t text := CASE TG_TABLE_NAME WHEN 'task' THEN 'T' WHEN 'document' THEN 'C' ELSE 'Q' END; prev text;
BEGIN
 IF TG_OP = 'INSERT' THEN
  prev := current_setting('fleet.project_ids', true);
  PERFORM set_config('fleet.project_ids', '*', true);
  IF t = 'C' AND EXISTS(SELECT 1 FROM document WHERE id=NEW.id) THEN NEW.code := NULL; ELSE NEW.code := fleet_issue_code(t, NEW.project_id, NEW.id); END IF;
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 ELSIF NEW.code IS NOT NULL AND NEW.code IS DISTINCT FROM OLD.code THEN
  RAISE EXCEPTION 'codes are issued, never written (set code to NULL to reissue)';
 ELSIF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.code IS NULL THEN
  IF NEW.project_id IS NOT DISTINCT FROM OLD.project_id THEN NEW.updated_at := OLD.updated_at; END IF; -- a reissue is not a change of the row
  NEW.code := fleet_issue_code(t, NEW.project_id, NEW.id);
  prev := current_setting('fleet.project_ids', true);
  PERFORM set_config('fleet.project_ids', '*', true);
  UPDATE entity_code SET superseded_by=NEW.code, project_id=nullif(NEW.project_id, '') WHERE entity_id=NEW.id AND type=t AND code<>NEW.code;
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 END IF;
 RETURN NEW;
END $$;
-- fleet_issue_code records the code under the operator scope: the caller's scope may not list rows without a project
-- (a member's projectless conversation), and the row itself was already checked by its own policy.
CREATE OR REPLACE FUNCTION fleet_issue_code(p_type text, p_project text, p_entity text) RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE s text; n bigint; c text; prev text := current_setting('fleet.project_ids', true);
BEGIN
 PERFORM pg_advisory_xact_lock_shared(hashtext('fleet_project_code'));
 s := fleet_project_code(p_project);
 INSERT INTO code_counter(scope, type, next) VALUES(s, p_type, 2) ON CONFLICT(scope, type, section) DO UPDATE SET next=code_counter.next+1 RETURNING next-1 INTO n;
 c := p_type || '-' || s || '-' || n;
 PERFORM set_config('fleet.project_ids', '*', true);
 INSERT INTO entity_code(code, entity_id, type, project_id) VALUES(c, p_entity, p_type, nullif(p_project, ''));
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN c;
END $$;
-- A session document is written by upsert: a conflicting insert of an existing row issues nothing (it becomes an update).
-- ponytail: two concurrent first upserts of one new session both issue; the loser's code is an orphan entity_code row
-- (superseded_by null, not the current code) that resolve ignores; lock the id if that ever matters.
CREATE TRIGGER document_zcode BEFORE INSERT OR UPDATE OF project_id, code ON document FOR EACH ROW WHEN (NEW.kind = 'session') EXECUTE FUNCTION fleet_entity_code();

CREATE OR REPLACE FUNCTION fleet_project_code_set(p_project text, p_code text) RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE r record; prev text := current_setting('fleet.project_ids', true);
BEGIN
 PERFORM pg_advisory_xact_lock(hashtext('fleet_project_code'));
 IF EXISTS(SELECT 1 FROM project_code WHERE code=p_code AND project_id=p_project AND current) THEN RETURN p_code; END IF;
 IF EXISTS(SELECT 1 FROM project_code WHERE code=p_code) THEN RAISE EXCEPTION 'project code % was used', p_code USING ERRCODE='23505'; END IF;
 UPDATE project_code SET current=false WHERE project_id=p_project AND current;
 INSERT INTO project_code(code, project_id) VALUES(p_code, p_project);
 PERFORM set_config('fleet.project_ids', '*', true); -- every row of the project, whatever the caller's scope (the op checked the project)
 FOR r IN SELECT id FROM task WHERE project_id=p_project ORDER BY created_at, id LOOP UPDATE task SET code=NULL WHERE id=r.id; END LOOP;
 FOR r IN SELECT id FROM question WHERE project_id=p_project ORDER BY asked_at, id LOOP UPDATE question SET code=NULL WHERE id=r.id; END LOOP;
 FOR r IN SELECT id FROM document WHERE kind='session' AND project_id=p_project ORDER BY seq LOOP UPDATE document SET code=NULL WHERE id=r.id; END LOOP;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN p_code;
END $$;
-- Backfill sessions in creation order, without a change row each (as migration 39).
ALTER TABLE document DISABLE TRIGGER document_change;
DO $$ DECLARE r record; BEGIN
 PERFORM set_config('fleet.project_ids', '*', true);
 FOR r IN SELECT id FROM document WHERE kind='session' AND code IS NULL ORDER BY seq LOOP UPDATE document SET code=NULL WHERE id=r.id; END LOOP;
END $$;
ALTER TABLE document ENABLE TRIGGER document_change;

-- migration 43: an abandoned run's wall time is unknown (it ended when given up, often days after it stopped).
SELECT set_config('fleet.project_ids', '*', true);
UPDATE execution_totals t SET wall_ms=NULL, wall_ms_basis='unavailable' FROM execution e WHERE e.id=t.execution_id AND e.problem='unknown:abandoned';

-- migration 44: the Codex app server reports usage per model call (usage.observation with {last, total}, total spanning
-- the whole thread) and its turn.completed carries none, so migration 40 found nothing. A run's tokens are now the sum
-- of its calls' last when it has such reports, else its last result or turn.completed report as before.
CREATE OR REPLACE FUNCTION fleet_write_tokens(eid text) RETURNS void LANGUAGE sql AS $$
 WITH evs AS (SELECT data->'data'->'fleetNormalized'->>'lifecycle' AS lc, data->'data'->'fleetNormalized'->'nativeUsage' AS u, seq FROM document
  WHERE kind='event' AND data->>'executionId'=eid AND jsonb_typeof(data->'data'->'fleetNormalized'->'nativeUsage')='object'),
 calls AS (SELECT u->'last' AS u FROM evs WHERE lc='usage.observation' AND jsonb_typeof(u->'last')='object'),
 rep AS (SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE lc IN ('result','turn.completed') AND NOT EXISTS (SELECT 1 FROM calls) ORDER BY seq DESC LIMIT 1),
 rows AS (SELECT
  fleet_token_count(coalesce(u->'input_tokens',u->'inputTokens')) AS input,
  fleet_token_count(coalesce(u->'output_tokens',u->'outputTokens')) AS output,
  fleet_token_count(coalesce(u->'cache_read_input_tokens',u->'cached_input_tokens',u->'cachedInputTokens')) AS cache_read,
  fleet_token_count(coalesce(u->'cache_creation_input_tokens',u->'cacheWriteInputTokens')) AS cache_write,
  (u ? 'cached_input_tokens' OR u ? 'cachedInputTokens') AS input_has_cache
  FROM (SELECT u FROM calls UNION ALL SELECT u FROM rep) x),
 n AS (SELECT sum(CASE WHEN input_has_cache THEN input-coalesce(cache_read,0) ELSE input END) AS input, sum(output) AS output,
  sum(cache_read) AS cache_read, sum(cache_write) AS cache_write FROM rows)
 UPDATE execution_totals SET
  tokens_input=n.input, tokens_input_basis=CASE WHEN n.input IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_output=n.output, tokens_output_basis=CASE WHEN n.output IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_cache_read=n.cache_read, tokens_cache_read_basis=CASE WHEN n.cache_read IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_cache_write=n.cache_write, tokens_cache_write_basis=CASE WHEN n.cache_write IS NULL THEN 'unavailable' ELSE 'measured' END
 FROM n WHERE execution_id=eid
$$;
SELECT set_config('fleet.project_ids', '*', true);
SELECT fleet_write_tokens(execution_id) FROM execution_totals t JOIN execution e ON e.id=t.execution_id WHERE e.kind='agent_run';

-- migration 45: the model a run actually used, from its harness's own events (the last event whose data names a
-- model: Claude's system event says claude-opus-5-5 where the execution row says opus). Prices are per resolved model.
ALTER TABLE execution_totals ADD COLUMN model text NULL;
CREATE FUNCTION fleet_write_model(eid text) RETURNS void LANGUAGE sql AS $$
 UPDATE execution_totals SET model=(SELECT data->'data'->>'model' FROM document WHERE kind='event' AND data->>'executionId'=eid
  AND jsonb_typeof(data->'data'->'model')='string' ORDER BY seq DESC LIMIT 1) WHERE execution_id=eid
$$;
SELECT set_config('fleet.project_ids', '*', true);
SELECT fleet_write_model(execution_id) FROM execution_totals t JOIN execution e ON e.id=t.execution_id WHERE e.kind='agent_run';

-- migration 46: task row facts (owner review P9): when the task entered its current state. A trigger sets it on every state
-- change, whatever the writer; existing tasks start from their last update. The backfill runs with the table's user triggers
-- off, so task_touch keeps updated_at (the default sort) and no change row is written per task, and with every project
-- visible (FORCE row-level security applies to the migration role too).
ALTER TABLE task ADD COLUMN state_at timestamptz;
ALTER TABLE task DISABLE TRIGGER USER;
SELECT set_config('fleet.project_ids', '*', true);
UPDATE task SET state_at = updated_at;
ALTER TABLE task ENABLE TRIGGER USER;
ALTER TABLE task ALTER COLUMN state_at SET DEFAULT now(), ALTER COLUMN state_at SET NOT NULL;
-- Held and paused, and each wait reason, show as their own state in the list, so a change of reason restarts the time too.
CREATE FUNCTION fleet_task_state_at() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF (NEW.state, NEW.pause_reason, NEW.wait_reason) IS DISTINCT FROM (OLD.state, OLD.pause_reason, OLD.wait_reason) THEN NEW.state_at := now(); END IF; RETURN NEW; END $$;
CREATE TRIGGER task_state_at BEFORE UPDATE OF state, pause_reason, wait_reason ON task FOR EACH ROW EXECUTE FUNCTION fleet_task_state_at();

-- migration 47: task row facts live in task_derived (migration 10), not in per-row subqueries: executions counts every
-- execution of the task, last_agent is the harness of its latest one (the row's agent mark and the agent: search term).
-- A new execution changes the count, so it writes one task change, as the row shows the new count.
-- The functions are recreated with the new columns; the backfill writes no change row per task. Sort keys of task_page
-- (updated_at as epoch ms, seq) get indexes for the default grouping (priority) and for no grouping, both directions.
ALTER TABLE task_derived ADD COLUMN executions integer NOT NULL DEFAULT 0, ADD COLUMN last_agent text NULL;
DROP FUNCTION task_derived_drift();
DROP FUNCTION fleet_task_derived(text);
DROP FUNCTION task_derived_recompute(text);
DROP FUNCTION task_derived_values(text);
CREATE FUNCTION task_derived_values(p_task text) RETURNS TABLE(project_id text, agent text, host text, started_at timestamptz, open_questions integer, executions integer, last_agent text) LANGUAGE sql STABLE SET search_path = public AS $$ SELECT (SELECT t.project_id FROM task t WHERE t.id = p_task), (SELECT e.harness FROM execution e WHERE e.task_id = p_task AND e.state IN ('preparing', 'running') ORDER BY e.seq DESC LIMIT 1), (SELECT e.host_id FROM execution e WHERE e.task_id = p_task ORDER BY e.seq DESC LIMIT 1), (SELECT min(e.started_at) FROM execution e WHERE e.task_id = p_task), (SELECT count(*)::int FROM question q WHERE q.task_id = p_task AND q.state = 'open'), (SELECT count(*)::int FROM execution e WHERE e.task_id = p_task), (SELECT e.harness FROM execution e WHERE e.task_id = p_task ORDER BY e.seq DESC LIMIT 1) $$;
REVOKE EXECUTE ON FUNCTION task_derived_values(text) FROM PUBLIC;
CREATE FUNCTION task_derived_recompute(p_task text) RETURNS void LANGUAGE plpgsql AS $$ DECLARE v record; BEGIN IF NOT EXISTS (SELECT 1 FROM task WHERE id = p_task) THEN RETURN; END IF; PERFORM 1 FROM task_derived WHERE task_id = p_task FOR UPDATE; IF NOT FOUND THEN INSERT INTO task_derived(task_id, project_id, agent, host, started_at, open_questions, executions, last_agent) SELECT p_task, x.* FROM task_derived_values(p_task) x ON CONFLICT (task_id) DO NOTHING; IF FOUND THEN RETURN; END IF; PERFORM 1 FROM task_derived WHERE task_id = p_task FOR UPDATE; END IF; SELECT * INTO v FROM task_derived_values(p_task); UPDATE task_derived d SET project_id = v.project_id, agent = v.agent, host = v.host, started_at = v.started_at, open_questions = v.open_questions, executions = v.executions, last_agent = v.last_agent WHERE d.task_id = p_task AND (d.project_id, d.agent, d.host, d.started_at, d.open_questions, d.executions, d.last_agent) IS DISTINCT FROM (v.project_id, v.agent, v.host, v.started_at, v.open_questions, v.executions, v.last_agent); END $$;
REVOKE EXECUTE ON FUNCTION task_derived_recompute(text) FROM PUBLIC;
CREATE FUNCTION fleet_task_derived(p_task text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$ DECLARE prev text := current_setting('fleet.project_ids', true); BEGIN IF p_task IS NULL THEN RETURN; END IF; PERFORM set_config('fleet.project_ids', '*', true); PERFORM task_derived_recompute(p_task); PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true); END $$;
CREATE FUNCTION task_derived_drift() RETURNS TABLE(task_id text, present boolean, stored jsonb, expected jsonb) LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$ DECLARE prev text := current_setting('fleet.project_ids', true); BEGIN PERFORM set_config('fleet.project_ids', '*', true); RETURN QUERY SELECT t.id, d.task_id IS NOT NULL, CASE WHEN d.task_id IS NULL THEN NULL ELSE jsonb_build_object('projectId', d.project_id, 'agent', d.agent, 'host', d.host, 'startedAt', d.started_at, 'openQuestions', d.open_questions, 'executions', d.executions, 'lastAgent', d.last_agent) END, jsonb_build_object('projectId', v.project_id, 'agent', v.agent, 'host', v.host, 'startedAt', v.started_at, 'openQuestions', v.open_questions, 'executions', v.executions, 'lastAgent', v.last_agent) FROM task t CROSS JOIN LATERAL task_derived_values(t.id) v LEFT JOIN task_derived d ON d.task_id = t.id WHERE (prev = '*' OR coalesce(t.project_id, '') = ANY (string_to_array(coalesce(prev, ''), ','))) AND (d.task_id IS NULL OR (d.project_id, d.agent, d.host, d.started_at, d.open_questions, d.executions, d.last_agent) IS DISTINCT FROM (v.project_id, v.agent, v.host, v.started_at, v.open_questions, v.executions, v.last_agent)) ORDER BY t.id; PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true); END $$;
SELECT set_config('fleet.project_ids', '*', true);
ALTER TABLE task_derived DISABLE TRIGGER task_derived_change;
UPDATE task_derived d SET executions = x.n, last_agent = x.h FROM (SELECT e.task_id, count(*)::int AS n, (array_agg(e.harness ORDER BY e.seq DESC))[1] AS h FROM execution e WHERE e.task_id IS NOT NULL GROUP BY e.task_id) x WHERE x.task_id = d.task_id;
ALTER TABLE task_derived ENABLE TRIGGER task_derived_change;
-- The changed sort reads updated_at as UTC epoch ms, an immutable expression, so it is indexed directly. Every task write
-- moves updated_at, so these indexes cost HOT updates; the list's default order needs them. task_priority_order (seq DESC)
-- has no reader left: the created sort is (-(seq::float8)).
DROP INDEX task_priority_order;
CREATE INDEX task_sort_priority_changed_desc ON task((CASE priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END), (-((extract(epoch FROM updated_at AT TIME ZONE 'UTC')*1000)::float8)), id);
CREATE INDEX task_sort_priority_changed ON task((CASE priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END), ((extract(epoch FROM updated_at AT TIME ZONE 'UTC')*1000)::float8), id);
CREATE INDEX task_sort_priority_created_desc ON task((CASE priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END), (-(seq::float8)), id);
CREATE INDEX task_sort_priority_created ON task((CASE priority WHEN 'urgent' THEN '1' WHEN 'normal' THEN '2' ELSE '3' END), (seq::float8), id);
CREATE INDEX task_sort_changed_desc ON task((-((extract(epoch FROM updated_at AT TIME ZONE 'UTC')*1000)::float8)), id);
CREATE INDEX task_sort_changed ON task(((extract(epoch FROM updated_at AT TIME ZONE 'UTC')*1000)::float8), id);
CREATE INDEX task_sort_created_desc ON task((-(seq::float8)), id);
CREATE INDEX task_sort_created ON task((seq::float8), id);

-- migration 48: requested artifacts precede incidental MCP lifecycle diagnostics in the task result.
CREATE OR REPLACE VIEW task_view WITH (security_invoker = true) AS
 SELECT t.id AS task_id, t.project_id, t.state, t.wait_reason,
  coalesce((SELECT jsonb_object_agg(s.state, s.n) FROM (SELECT e.state, count(*) AS n FROM execution e WHERE e.task_id = t.id GROUP BY e.state) s), '{}') AS executions_by_state,
  (SELECT count(*) FROM execution e WHERE e.task_id = t.id) AS execution_count,
  (SELECT jsonb_build_object('tokens_input', sum(x.tokens_input), 'tokens_output', sum(x.tokens_output), 'tokens_cache_read', sum(x.tokens_cache_read), 'tokens_cache_write', sum(x.tokens_cache_write), 'wall_ms', sum(x.wall_ms), 'active_ms', sum(x.active_ms), 'cpu_ms', sum(x.cpu_ms), 'measured_wall', count(*) FILTER (WHERE x.wall_ms_basis = 'measured'), 'executions_with_totals', count(*))
   FROM execution_totals x WHERE x.execution_id IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)) AS totals,
  (SELECT r.v FROM (SELECT jsonb_build_object('id', coalesce(a.legacy_id, a.id), 'artifactId', a.id, 'executionId', a.execution_id, 'path', a.name, 'mediaType', a.media_type, 'size', a.size, 'sha256', a.content_hash) AS v, a.seq, (a.name NOT LIKE 'mcp-lifecycle-%') AS deliverable, (SELECT e.seq FROM execution e WHERE e.id=a.execution_id) AS execution_seq, a.required FROM artifact a WHERE a.state = 'stored' AND a.execution_id IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)
   UNION ALL SELECT d.data - 'contentBase64', d.seq, (coalesce(d.data->>'path','') NOT LIKE 'mcp-lifecycle-%'), (SELECT e.seq FROM execution e WHERE e.id=d.data->>'executionId'), coalesce((d.data->>'required')::boolean,false) FROM document d WHERE d.kind = 'artifact' AND d.data->>'executionId' IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)) r ORDER BY r.deliverable DESC, r.execution_seq DESC, r.required DESC, r.seq DESC LIMIT 1) AS latest_result,
  (SELECT coalesce(jsonb_agg(jsonb_build_object('id', q.id, 'kind', q.kind, 'text', q.text, 'options', q.options) ORDER BY q.seq), '[]') FROM question q WHERE q.task_id = t.id AND q.state = 'open') AS open_questions
 FROM task t;

-- migration 49: result versions follow first content publication; repeated collection does not advance them.
CREATE OR REPLACE VIEW task_view WITH (security_invoker = true) AS
 SELECT t.id AS task_id, t.project_id, t.state, t.wait_reason,
  coalesce((SELECT jsonb_object_agg(s.state, s.n) FROM (SELECT e.state, count(*) AS n FROM execution e WHERE e.task_id = t.id GROUP BY e.state) s), '{}') AS executions_by_state,
  (SELECT count(*) FROM execution e WHERE e.task_id = t.id) AS execution_count,
  (SELECT jsonb_build_object('tokens_input', sum(x.tokens_input), 'tokens_output', sum(x.tokens_output), 'tokens_cache_read', sum(x.tokens_cache_read), 'tokens_cache_write', sum(x.tokens_cache_write), 'wall_ms', sum(x.wall_ms), 'active_ms', sum(x.active_ms), 'cpu_ms', sum(x.cpu_ms), 'measured_wall', count(*) FILTER (WHERE x.wall_ms_basis = 'measured'), 'executions_with_totals', count(*))
   FROM execution_totals x WHERE x.execution_id IN (SELECT e.id FROM execution e WHERE e.task_id = t.id)) AS totals,
  (WITH publications AS (
    SELECT 'artifact:'||a.id AS row_id, a.id, a.legacy_id, a.project_id, a.scope, a.content_hash, a.previous_artifact_id,
      a.seq, e.seq AS execution_seq, a.required, a.state='stored' AS stored, a.name NOT LIKE 'mcp-lifecycle-%' AS deliverable,
      jsonb_build_object('id', coalesce(a.legacy_id,a.id), 'artifactId',a.id, 'executionId',a.execution_id, 'path',a.name, 'mediaType',a.media_type, 'size',a.size, 'sha256',a.content_hash) AS v
    FROM artifact a JOIN execution e ON e.id=a.execution_id WHERE a.state IN ('stored','deleted') AND e.task_id=t.id
    UNION ALL
    SELECT 'document:'||d.id, d.id, NULL, coalesce(d.project_id,d.data->>'projectId'), d.data->>'scope', d.data->>'sha256', d.data->>'previousArtifactId',
      d.seq, e.seq, coalesce((d.data->>'required')::boolean,false), true, coalesce(d.data->>'path','') NOT LIKE 'mcp-lifecycle-%', d.data-'contentBase64'
    FROM document d JOIN execution e ON e.id=d.data->>'executionId' WHERE d.kind='artifact' AND e.task_id=t.id
   ), predecessors AS (
    SELECT a.id,a.legacy_id,a.project_id,a.scope,a.seq FROM artifact a JOIN execution e ON e.id=a.execution_id WHERE e.task_id=t.id
    UNION ALL
    SELECT d.id,NULL,coalesce(d.project_id,d.data->>'projectId'),d.data->>'scope',d.seq
    FROM document d JOIN execution e ON e.id=d.data->>'executionId' WHERE d.kind='artifact' AND e.task_id=t.id
   ), identities AS (
    SELECT c.*, CASE WHEN nullif(project_id,'') IS NOT NULL AND scope IN ('prod','lab') AND content_hash ~ '^[0-9a-f]{64}$' THEN NULL ELSE row_id END AS unknown_identity,
      CASE WHEN nullif(c.previous_artifact_id,'') IS NULL THEN false ELSE EXISTS (SELECT 1 FROM predecessors p WHERE (p.id=c.previous_artifact_id OR p.legacy_id=c.previous_artifact_id)
        AND p.seq<c.seq AND p.project_id=c.project_id AND p.scope=c.scope) END AS explicit_version
    FROM publications c
   ), anchors AS (
    SELECT i.*, max(seq) FILTER (WHERE explicit_version) OVER (
      PARTITION BY project_id,scope,content_hash,unknown_identity ORDER BY seq ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS anchor
    FROM identities i
   ), versions AS (
    SELECT a.*, CASE WHEN anchor IS NULL THEN min(execution_seq) OVER w
      ELSE max(execution_seq) FILTER (WHERE seq=anchor) OVER w END AS version_execution_seq
    FROM anchors a WINDOW w AS (PARTITION BY project_id,scope,content_hash,unknown_identity,anchor)
   ), positions AS (
    SELECT v.*, coalesce(anchor,min(seq) FILTER (WHERE execution_seq=version_execution_seq) OVER (
      PARTITION BY project_id,scope,content_hash,unknown_identity,anchor)) AS version_seq
    FROM versions v
   ) SELECT v FROM positions WHERE stored
   ORDER BY deliverable DESC,version_execution_seq DESC,version_seq DESC,required DESC,seq DESC LIMIT 1) AS latest_result,
  (SELECT coalesce(jsonb_agg(jsonb_build_object('id', q.id, 'kind', q.kind, 'text', q.text, 'options', q.options) ORDER BY q.seq), '[]') FROM question q WHERE q.task_id = t.id AND q.state = 'open') AS open_questions
 FROM task t;

-- migration 50: leaf execution resource summaries, scalar lineage columns, 3-tier token precedence, tool_run change suppression, and directed_totals RLS.
ALTER TABLE execution_totals
 ADD COLUMN turn_count bigint NULL,
 ADD COLUMN turn_count_basis text NOT NULL DEFAULT 'unavailable',
 ADD COLUMN tool_calls jsonb NULL,
 ADD COLUMN tool_calls_basis text NOT NULL DEFAULT 'unavailable',
 ADD COLUMN cpu_missing_reason text NULL,
 ADD COLUMN memory_missing_reason text NULL,
 ADD COLUMN billing_basis text NOT NULL DEFAULT 'unavailable',
 ADD COLUMN directed_by text NULL;
ALTER TABLE execution
 ADD COLUMN profile_id text NULL,
 ADD COLUMN profile_revision bigint NULL,
 ADD COLUMN pipeline_id text NULL,
 ADD COLUMN pipeline_revision bigint NULL,
 ADD COLUMN step_id text NULL;
ALTER TABLE task
 ADD COLUMN created_by_session_id text NULL,
 ADD COLUMN created_by_execution_id text NULL;

DROP POLICY directed_execution ON execution;
CREATE POLICY directed_execution ON execution USING (
 project_id IS NULL AND directed_by = current_setting('fleet.person', true)
 AND (fleet_own_projectless_session(session_id) OR (session_id IS NULL AND NOT (data ? 'directorAccessRevokedAt')))
);
DROP POLICY directed_totals ON execution_totals;
CREATE POLICY directed_totals ON execution_totals USING (
 project_id IS NULL AND (directed_by IS NULL OR directed_by = current_setting('fleet.person', true))
 AND (fleet_own_projectless_execution(execution_id) OR EXISTS (
  SELECT 1 FROM execution e WHERE e.id = execution_id AND e.project_id IS NULL AND e.session_id IS NULL
   AND e.directed_by = current_setting('fleet.person', true) AND NOT (e.data ? 'directorAccessRevokedAt')
 ))
);

CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; ek text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change', 'project-recommendation') THEN k := r.kind; w := r.data->>'holder'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN IF r.kind = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN SELECT e.kind::text, e.task_id, e.session_id, e.host_id INTO ek, t, s, h FROM execution e WHERE e.id = r.execution_id; IF ek = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;

CREATE OR REPLACE FUNCTION fleet_write_tokens(eid text) RETURNS void LANGUAGE sql AS $$
 WITH doc_evs AS MATERIALIZED (
  SELECT data, seq
  FROM document
  WHERE kind='event' AND data->>'executionId'=eid
 ),
 evs AS (
  SELECT data->'data'->'fleetNormalized'->>'lifecycle' AS lc, data->'data'->'fleetNormalized'->'nativeUsage' AS u, seq
  FROM doc_evs WHERE jsonb_typeof(data->'data'->'fleetNormalized'->'nativeUsage')='object'
 ),
 calls AS (SELECT u->'last' AS u FROM evs WHERE lc='usage.observation' AND jsonb_typeof(u->'last')='object'),
 rep AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE lc IN ('result','turn.completed') AND NOT EXISTS (SELECT 1 FROM calls) ORDER BY seq DESC LIMIT 1
 ),
 inc_native AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE coalesce(lc,'') NOT IN ('usage.observation','result','turn.completed')
   AND NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
 ),
 inc_assistant AS (
  SELECT DISTINCT ON (mid) mu AS u FROM (
   SELECT
    coalesce(d.data->'data'->'fleetNormalized'->'message'->>'id', d.data->'data'->'message'->>'id', d.seq::text) AS mid,
    coalesce(d.data->'data'->'fleetNormalized'->'message'->'usage', d.data->'data'->'message'->'usage') AS mu,
    d.seq
   FROM doc_evs d
   WHERE NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
    AND coalesce(d.data->'data'->'fleetNormalized'->>'lifecycle', d.data->'data'->>'type')='assistant'
    AND jsonb_typeof(d.data->'data'->'fleetNormalized'->'nativeUsage') IS DISTINCT FROM 'object'
  ) m WHERE jsonb_typeof(mu)='object' ORDER BY mid, seq DESC
 ),
 rows AS (
  SELECT
   fleet_token_count(coalesce(u->'input_tokens',u->'inputTokens')) AS input,
   fleet_token_count(coalesce(u->'output_tokens',u->'outputTokens')) AS output,
   fleet_token_count(coalesce(u->'cache_read_input_tokens',u->'cached_input_tokens',u->'cachedInputTokens',u->'cachedReadTokens',u->'cache_read_tokens')) AS cache_read,
   fleet_token_count(coalesce(u->'cache_creation_input_tokens',u->'cacheWriteInputTokens',u->'cachedWriteTokens',u->'cache_write_tokens')) AS cache_write,
   (u ? 'cached_input_tokens' OR u ? 'cachedInputTokens') AS input_has_cache
  FROM (SELECT u FROM calls UNION ALL SELECT u FROM rep UNION ALL SELECT u FROM inc_native UNION ALL SELECT u FROM inc_assistant) x
 ),
 n AS (
  SELECT
   sum(CASE WHEN input_has_cache AND input IS NOT NULL THEN greatest(0, input - coalesce(cache_read,0)) ELSE input END) AS input,
   sum(output) AS output, sum(cache_read) AS cache_read, sum(cache_write) AS cache_write
  FROM rows
 )
 UPDATE execution_totals SET
  tokens_input=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.input ELSE execution_totals.tokens_input END,
  tokens_input_basis=CASE WHEN (CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.input ELSE execution_totals.tokens_input END) IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_output=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.output ELSE execution_totals.tokens_output END,
  tokens_output_basis=CASE WHEN (CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.output ELSE execution_totals.tokens_output END) IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_cache_read=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_read ELSE execution_totals.tokens_cache_read END,
  tokens_cache_read_basis=CASE WHEN (CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_read ELSE execution_totals.tokens_cache_read END) IS NULL THEN 'unavailable' ELSE 'measured' END,
  tokens_cache_write=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_write ELSE execution_totals.tokens_cache_write END,
  tokens_cache_write_basis=CASE WHEN (CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_write ELSE execution_totals.tokens_cache_write END) IS NULL THEN 'unavailable' ELSE 'measured' END
 FROM n WHERE execution_id=eid
$$;

CREATE OR REPLACE VIEW task_view WITH (security_invoker = true) AS
 SELECT t.id AS task_id, t.project_id, t.state, t.wait_reason,
  coalesce((SELECT jsonb_object_agg(s.state, s.n) FROM (SELECT e.state, count(*) AS n FROM execution e WHERE e.task_id = t.id GROUP BY e.state) s), '{}') AS executions_by_state,
  (SELECT count(*) FROM execution e WHERE e.task_id = t.id) AS execution_count,
  (SELECT jsonb_build_object('tokens_input', sum(x.tokens_input), 'tokens_output', sum(x.tokens_output), 'tokens_cache_read', sum(x.tokens_cache_read), 'tokens_cache_write', sum(x.tokens_cache_write), 'wall_ms', sum(x.wall_ms), 'active_ms', sum(x.active_ms), 'cpu_ms', sum(x.cpu_ms), 'turn_count', sum(x.turn_count), 'peak_memory_bytes', max(x.peak_memory_bytes), 'measured_wall', count(*) FILTER (WHERE x.wall_ms_basis = 'measured'), 'measured_cpu', count(*) FILTER (WHERE x.cpu_ms_basis = 'measured'), 'executions_with_totals', count(*))
   FROM execution_totals x JOIN execution e ON e.id = x.execution_id WHERE e.task_id = t.id AND e.kind NOT IN ('pipeline_run', 'pipeline_step', 'tool_run')) AS totals,
  (WITH publications AS (
    SELECT 'artifact:'||a.id AS row_id, a.id, a.legacy_id, a.project_id, a.scope, a.content_hash, a.previous_artifact_id,
      a.seq, e.seq AS execution_seq, a.required, a.state='stored' AS stored, a.name NOT LIKE 'mcp-lifecycle-%' AS deliverable,
      jsonb_build_object('id', coalesce(a.legacy_id,a.id), 'artifactId',a.id, 'executionId',a.execution_id, 'path',a.name, 'mediaType',a.media_type, 'size',a.size, 'sha256',a.content_hash) AS v
    FROM artifact a JOIN execution e ON e.id=a.execution_id WHERE a.state IN ('stored','deleted') AND e.task_id=t.id
    UNION ALL
    SELECT 'document:'||d.id, d.id, NULL, coalesce(d.project_id,d.data->>'projectId'), d.data->>'scope', d.data->>'sha256', d.data->>'previousArtifactId',
      d.seq, e.seq, coalesce((d.data->>'required')::boolean,false), true, coalesce(d.data->>'path','') NOT LIKE 'mcp-lifecycle-%', d.data-'contentBase64'
    FROM document d JOIN execution e ON e.id=d.data->>'executionId' WHERE d.kind='artifact' AND e.task_id=t.id
   ), predecessors AS (
    SELECT a.id,a.legacy_id,a.project_id,a.scope,a.seq FROM artifact a JOIN execution e ON e.id=a.execution_id WHERE e.task_id=t.id
    UNION ALL
    SELECT d.id,NULL,coalesce(d.project_id,d.data->>'projectId'),d.data->>'scope',d.seq
    FROM document d JOIN execution e ON e.id=d.data->>'executionId' WHERE d.kind='artifact' AND e.task_id=t.id
   ), identities AS (
    SELECT c.*, CASE WHEN nullif(project_id,'') IS NOT NULL AND scope IN ('prod','lab') AND content_hash ~ '^[0-9a-f]{64}$' THEN NULL ELSE row_id END AS unknown_identity,
      CASE WHEN nullif(c.previous_artifact_id,'') IS NULL THEN false ELSE EXISTS (SELECT 1 FROM predecessors p WHERE (p.id=c.previous_artifact_id OR p.legacy_id=c.previous_artifact_id)
        AND p.seq<c.seq AND p.project_id=c.project_id AND p.scope=c.scope) END AS explicit_version
    FROM publications c
   ), anchors AS (
    SELECT i.*, max(seq) FILTER (WHERE explicit_version) OVER (
      PARTITION BY project_id,scope,content_hash,unknown_identity ORDER BY seq ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS anchor
    FROM identities i
   ), versions AS (
    SELECT a.*, CASE WHEN anchor IS NULL THEN min(execution_seq) OVER w
      ELSE max(execution_seq) FILTER (WHERE seq=anchor) OVER w END AS version_execution_seq
    FROM anchors a WINDOW w AS (PARTITION BY project_id,scope,content_hash,unknown_identity,anchor)
   ), positions AS (
    SELECT v.*, coalesce(anchor,min(seq) FILTER (WHERE execution_seq=version_execution_seq) OVER (
      PARTITION BY project_id,scope,content_hash,unknown_identity,anchor)) AS version_seq
    FROM versions v
   ) SELECT v FROM positions WHERE stored
   ORDER BY deliverable DESC,version_execution_seq DESC,version_seq DESC,required DESC,seq DESC LIMIT 1) AS latest_result,
  (SELECT coalesce(jsonb_agg(jsonb_build_object('id', q.id, 'kind', q.kind, 'text', q.text, 'options', q.options) ORDER BY q.seq), '[]') FROM question q WHERE q.task_id = t.id AND q.state = 'open') AS open_questions
 FROM task t;

SET CONSTRAINTS ALL IMMEDIATE;
SELECT set_config('fleet.project_ids', '*', true);
ALTER TABLE document NO FORCE ROW LEVEL SECURITY;
ALTER TABLE execution_totals NO FORCE ROW LEVEL SECURITY;
ALTER TABLE execution NO FORCE ROW LEVEL SECURITY;
ALTER TABLE task NO FORCE ROW LEVEL SECURITY;
SET LOCAL row_security = off;
ALTER TABLE execution_totals DISABLE TRIGGER USER;
ALTER TABLE execution DISABLE TRIGGER USER;
ALTER TABLE task DISABLE TRIGGER USER;
DROP INDEX execution_data;
DROP INDEX execution_definitions;
DELETE FROM execution_totals x USING execution e WHERE e.id = x.execution_id AND e.kind IN ('pipeline_run', 'pipeline_step');
UPDATE execution_totals x SET
 directed_by = v.directed_by,
 billing_basis = v.basis
FROM (
 SELECT xt.execution_id,
  coalesce(xt.directed_by, e.directed_by) AS directed_by,
  CASE
   WHEN e.account_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM document a WHERE a.kind = 'api-connection' AND a.id = e.account_id) THEN 'subscription'
   WHEN e.kind IN ('agent_run', 'child_agent', 'tool_run') OR xt.connection_cost IS NOT NULL THEN 'metered'
   ELSE 'unavailable'
  END AS basis
 FROM execution_totals xt
 JOIN execution e ON e.id = xt.execution_id
) v
WHERE x.execution_id = v.execution_id
 AND (x.directed_by IS DISTINCT FROM v.directed_by OR x.billing_basis IS DISTINCT FROM v.basis);
WITH RECURSIVE base AS MATERIALIZED (
 SELECT e.id, e.parent_execution_id, nullif(e.data->>'pipelineStepId', '') AS psid,
  coalesce((SELECT d->>'id' FROM jsonb_array_elements(CASE WHEN jsonb_typeof(e.definitions) = 'array' THEN e.definitions ELSE '[]'::jsonb END) d WHERE d->>'kind' = 'agent' LIMIT 1), nullif(e.config_ref->>'profileId', '')) AS profile_id,
  (SELECT CASE WHEN (d->>'revision') ~ '^[0-9]+$' THEN (d->>'revision')::bigint END FROM jsonb_array_elements(CASE WHEN jsonb_typeof(e.definitions) = 'array' THEN e.definitions ELSE '[]'::jsonb END) d WHERE d->>'kind' = 'agent' LIMIT 1) AS profile_revision,
  coalesce((SELECT d->>'id' FROM jsonb_array_elements(CASE WHEN jsonb_typeof(e.definitions) = 'array' THEN e.definitions ELSE '[]'::jsonb END) d WHERE d->>'kind' = 'pipeline' LIMIT 1), nullif(e.data->>'pipelineId', '')) AS pipeline_id,
  (SELECT CASE WHEN (d->>'revision') ~ '^[0-9]+$' THEN (d->>'revision')::bigint END FROM jsonb_array_elements(CASE WHEN jsonb_typeof(e.definitions) = 'array' THEN e.definitions ELSE '[]'::jsonb END) d WHERE d->>'kind' = 'pipeline' LIMIT 1) AS pipeline_revision,
  CASE WHEN e.kind = 'pipeline_step' THEN nullif(e.data->>'id', '') ELSE e.step_id END AS step_id
 FROM execution e
),
lineage AS (
 SELECT id, pipeline_id, pipeline_revision, step_id, profile_id, profile_revision, ARRAY[id] AS seen
 FROM base
 WHERE pipeline_id IS NOT NULL OR step_id IS NOT NULL OR profile_id IS NOT NULL
 UNION ALL
 SELECT c.id,
  CASE WHEN c.pipeline_id IS NOT NULL THEN c.pipeline_id ELSE p.pipeline_id END,
  CASE WHEN c.pipeline_id IS NOT NULL THEN c.pipeline_revision ELSE p.pipeline_revision END,
  coalesce(c.step_id, p.step_id),
  CASE WHEN c.profile_id IS NOT NULL THEN c.profile_id ELSE p.profile_id END,
  CASE WHEN c.profile_id IS NOT NULL THEN c.profile_revision ELSE p.profile_revision END,
  p.seen || c.id
 FROM base c
 JOIN lineage p ON p.id = coalesce(c.psid, c.parent_execution_id)
 WHERE NOT (c.id = ANY(p.seen)) AND array_length(p.seen, 1) < 32
   AND ((c.pipeline_id IS NULL AND p.pipeline_id IS NOT NULL)
     OR (c.step_id IS NULL AND p.step_id IS NOT NULL)
     OR (c.profile_id IS NULL AND p.profile_id IS NOT NULL))
)
UPDATE execution c SET
 pipeline_id = l.pipeline_id,
 pipeline_revision = l.pipeline_revision,
 step_id = l.step_id,
 profile_id = l.profile_id,
 profile_revision = l.profile_revision
FROM (
 SELECT DISTINCT ON (id) id, pipeline_id, pipeline_revision, step_id, profile_id, profile_revision
 FROM lineage
 ORDER BY id, array_length(seen, 1) DESC
) l
WHERE c.id = l.id
 AND (c.pipeline_id, c.pipeline_revision, c.step_id, c.profile_id, c.profile_revision)
  IS DISTINCT FROM (l.pipeline_id, l.pipeline_revision, l.step_id, l.profile_id, l.profile_revision);
UPDATE task t SET
 created_by_execution_id = substr(t.created_by, 11),
 created_by_session_id = (SELECT e.session_id FROM execution e WHERE e.id = substr(t.created_by, 11))
WHERE t.created_by LIKE 'execution:%' AND t.created_by_execution_id IS NULL;
SELECT count(fleet_write_tokens(t.execution_id))
FROM execution_totals t JOIN execution e ON e.id = t.execution_id
WHERE e.kind = 'agent_run' AND coalesce(t.tokens_input, t.tokens_output, t.tokens_cache_read, t.tokens_cache_write) IS NULL;
RESET row_security;
ALTER TABLE task FORCE ROW LEVEL SECURITY;
ALTER TABLE execution FORCE ROW LEVEL SECURITY;
ALTER TABLE execution_totals FORCE ROW LEVEL SECURITY;
ALTER TABLE document FORCE ROW LEVEL SECURITY;
ALTER TABLE task ENABLE TRIGGER USER;
ALTER TABLE execution ENABLE TRIGGER USER;
ALTER TABLE execution_totals ENABLE TRIGGER USER;
SET CONSTRAINTS ALL DEFERRED;
CREATE INDEX execution_data ON execution USING gin (data jsonb_path_ops) WITH (fastupdate = off);
CREATE INDEX execution_definitions ON execution USING gin (definitions jsonb_path_ops);
CREATE INDEX execution_project_started ON execution(project_id, started_at);
CREATE INDEX execution_pipeline_rev ON execution(pipeline_id, pipeline_revision) WHERE pipeline_id IS NOT NULL;
CREATE INDEX execution_profile_rev ON execution(profile_id, profile_revision) WHERE profile_id IS NOT NULL;
CREATE INDEX task_created_by_session ON task(created_by_session_id) WHERE created_by_session_id IS NOT NULL;
CREATE INDEX task_created_by_execution ON task(created_by_execution_id) WHERE created_by_execution_id IS NOT NULL;
CREATE INDEX execution_totals_directed ON execution_totals(directed_by) WHERE project_id IS NULL AND directed_by IS NOT NULL;

-- migration 51: analytics RLS-safe metadata/hidden helpers and fleet_write_tokens basis preservation.
CREATE FUNCTION fleet_exec_conn_meta(conn_name text, aid text)
RETURNS TABLE(provider text, is_api_conn boolean)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 v_provider text;
 v_is_api boolean := false;
BEGIN
 IF coalesce(conn_name, '') = '' AND coalesce(aid, '') = '' THEN
  provider := NULL;
  is_api_conn := false;
  RETURN NEXT;
  RETURN;
 END IF;
 PERFORM set_config('fleet.project_ids', '*', true);
 BEGIN
  SELECT coalesce(
   CASE WHEN coalesce(conn_name, '') <> '' THEN (SELECT d.data->>'provider' FROM document d WHERE d.kind = 'api-connection' AND d.id = 'connection:' || conn_name LIMIT 1) END,
   CASE WHEN coalesce(aid, '') <> '' THEN (SELECT d.data->>'provider' FROM document d WHERE d.kind = 'api-connection' AND d.id = aid LIMIT 1) END
  ) INTO v_provider;
  IF coalesce(aid, '') <> '' THEN
   SELECT EXISTS(SELECT 1 FROM document a WHERE a.kind = 'api-connection' AND a.id = aid) INTO v_is_api;
  END IF;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 provider := v_provider;
 is_api_conn := coalesce(v_is_api, false);
 RETURN NEXT;
END $$;

CREATE FUNCTION fleet_window_leaf_count(win_from timestamptz, win_to timestamptz)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 total_n bigint := 0;
BEGIN
 IF win_from IS NULL OR win_to IS NULL OR NOT (win_from < win_to) THEN
  RETURN 0;
 END IF;
 PERFORM set_config('fleet.project_ids', '*', true);
 BEGIN
  SELECT count(*)::bigint INTO total_n
  FROM execution e
  LEFT JOIN execution_totals t ON t.execution_id = e.id
  WHERE e.kind NOT IN ('pipeline_run', 'pipeline_step')
    AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL)
    AND e.started_at >= win_from
    AND e.started_at < win_to;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN coalesce(total_n, 0);
END $$;

CREATE FUNCTION fleet_task_hidden_lineage_count(tid text)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 vis_n bigint := 0;
 all_n bigint := 0;
 pass_n int;
 cur_n bigint;
BEGIN
 IF coalesce(tid, '') = '' OR NOT EXISTS (SELECT 1 FROM task WHERE id = tid) THEN
  RETURN 0;
 END IF;
 BEGIN
  FOR pass_n IN 0..1 LOOP
   IF pass_n = 1 THEN
    PERFORM set_config('fleet.project_ids', '*', true);
   END IF;
   WITH RECURSIVE
    candidate_sessions AS (
     SELECT DISTINCT session_id FROM execution WHERE task_id = tid AND session_id IS NOT NULL
    ),
    first_task AS (
     SELECT DISTINCT ON (e.session_id) e.session_id, e.task_id
     FROM execution e
     WHERE e.task_id IS NOT NULL AND e.session_id IN (SELECT session_id FROM candidate_sessions)
     ORDER BY e.session_id, e.seq
    ),
    direct_sessions AS (
     SELECT id AS session_id FROM document
     WHERE kind = 'session'
       AND (data->>'taskId' = tid OR (coalesce(nullif(data->>'taskId', ''), '') = '' AND id IN (SELECT session_id FROM first_task WHERE task_id = tid)))
       AND NOT (data ? 'parentExecutionId')
    ),
    own_execs AS (
     SELECT e.id, e.session_id
     FROM execution e
     WHERE e.kind <> 'child_agent'
       AND e.delegation IS NULL
       AND (
         e.task_id = tid
         OR (e.task_id IS NULL AND e.session_id IN (SELECT session_id FROM direct_sessions))
         OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
            SELECT p.id FROM execution p
            WHERE p.kind <> 'child_agent'
              AND p.delegation IS NULL
              AND (p.task_id = tid OR (p.task_id IS NULL AND p.session_id IN (SELECT session_id FROM direct_sessions)))
         ))
       )
    ),
    del_seed AS (
     SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
        OR (e.delegation IS NOT NULL AND (e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs) OR e.delegation->>'parentSessionId' IN (SELECT session_id FROM direct_sessions)))
     UNION
     SELECT e.id, e.session_id, true AS follow_session
     FROM execution e
     JOIN document s ON s.kind = 'session' AND s.id = e.session_id
     WHERE s.data->>'parentExecutionId' IN (SELECT id FROM own_execs)
        OR (s.data->>'createdByExecutionId' IN (SELECT id FROM own_execs) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
        OR (s.data->>'createdBySessionId' IN (SELECT session_id FROM direct_sessions) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
    ),
    del_tree AS (
     SELECT id, session_id, follow_session FROM del_seed
     UNION
     SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     JOIN del_tree dt ON e.parent_execution_id = dt.id
        OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id NOT IN (SELECT session_id FROM direct_sessions) AND e.task_id IS NULL)
        OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
    ),
    spawned_tasks AS (
     SELECT t.id
     FROM task t
     WHERE t.id <> tid
       AND (
         t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
         OR t.created_by_session_id IN (
          SELECT session_id FROM direct_sessions
          UNION SELECT session_id FROM own_execs WHERE session_id IS NOT NULL
          UNION SELECT session_id FROM del_tree WHERE session_id IS NOT NULL
         )
       )
    ),
    spawned_execs AS (
     SELECT e.id AS exec_id
     FROM spawned_tasks st
     JOIN execution e ON (
      e.task_id = st.id
      OR (e.task_id IS NULL AND e.session_id IN (
        SELECT d.id FROM document d WHERE d.kind = 'session' AND d.data->>'taskId' = st.id
      ))
      OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
        SELECT p.id FROM execution p
        WHERE p.task_id = st.id
           OR (p.task_id IS NULL AND p.session_id IN (
             SELECT d.id FROM document d WHERE d.kind = 'session' AND d.data->>'taskId' = st.id
           ))
      ))
     )
    )
   SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks)
        + (SELECT count(DISTINCT d.id)::bigint
           FROM del_tree d
           JOIN execution e ON e.id = d.id
           LEFT JOIN execution_totals t ON t.execution_id = e.id
           WHERE d.id NOT IN (SELECT exec_id FROM spawned_execs)
             AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
             AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL))
   INTO cur_n;
   IF pass_n = 0 THEN
    vis_n := coalesce(cur_n, 0);
   ELSE
    all_n := coalesce(cur_n, 0);
   END IF;
  END LOOP;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN greatest(0::bigint, all_n - vis_n);
END $$;

CREATE FUNCTION fleet_session_hidden_lineage_count(sid text)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 vis_n bigint := 0;
 all_n bigint := 0;
 pass_n int;
 cur_n bigint;
BEGIN
 IF coalesce(sid, '') = '' OR NOT EXISTS (SELECT 1 FROM document WHERE kind = 'session' AND id = sid) THEN
  RETURN 0;
 END IF;
 BEGIN
  FOR pass_n IN 0..1 LOOP
   IF pass_n = 1 THEN
    PERFORM set_config('fleet.project_ids', '*', true);
   END IF;
   WITH RECURSIVE
    own_execs AS (
     SELECT e.id, e.session_id
     FROM execution e
     WHERE e.kind <> 'child_agent'
       AND e.delegation IS NULL
       AND (e.session_id = sid OR (e.kind = 'tool_run' AND e.parent_execution_id IN (SELECT id FROM execution WHERE session_id = sid AND kind <> 'child_agent' AND delegation IS NULL)))
    ),
    del_seed AS (
     SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
        OR (e.delegation IS NOT NULL AND (e.delegation->>'parentSessionId' = sid OR e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs)))
     UNION
     SELECT e.id, e.session_id, true AS follow_session
     FROM execution e
     JOIN document s ON s.kind = 'session' AND s.id = e.session_id
     WHERE e.session_id <> sid
       AND (
         s.data->>'parentExecutionId' IN (SELECT id FROM own_execs)
         OR (s.data->>'createdByExecutionId' IN (SELECT id FROM own_execs) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
         OR (s.data->>'createdBySessionId' = sid AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
       )
    ),
    del_tree AS (
     SELECT id, session_id, follow_session FROM del_seed
     UNION
     SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     JOIN del_tree dt ON e.parent_execution_id = dt.id
        OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id <> sid AND e.task_id IS NULL)
        OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
    ),
    primary_task AS (
     SELECT nullif(data->>'taskId', '') AS task_id FROM document WHERE kind = 'session' AND id = sid
    ),
    spawned_tasks AS (
     SELECT t.id
     FROM task t
     WHERE t.id IS DISTINCT FROM (SELECT task_id FROM primary_task)
       AND (
         t.created_by_session_id = sid
         OR t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
         OR t.created_by_session_id IN (SELECT session_id FROM del_tree WHERE session_id IS NOT NULL)
       )
    ),
    spawned_execs AS (
     SELECT e.id AS exec_id
     FROM spawned_tasks st
     JOIN execution e ON (
      e.task_id = st.id
      OR (e.task_id IS NULL AND e.session_id IN (
        SELECT d.id FROM document d WHERE d.kind = 'session' AND d.data->>'taskId' = st.id
      ))
      OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
        SELECT p.id FROM execution p
        WHERE p.task_id = st.id
           OR (p.task_id IS NULL AND p.session_id IN (
             SELECT d.id FROM document d WHERE d.kind = 'session' AND d.data->>'taskId' = st.id
           ))
      ))
     )
    )
   SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks)
        + (SELECT count(DISTINCT d.id)::bigint
           FROM del_tree d
           JOIN execution e ON e.id = d.id
           LEFT JOIN execution_totals t ON t.execution_id = e.id
           WHERE d.id NOT IN (SELECT exec_id FROM spawned_execs)
             AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
             AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL))
   INTO cur_n;
   IF pass_n = 0 THEN
    vis_n := coalesce(cur_n, 0);
   ELSE
    all_n := coalesce(cur_n, 0);
   END IF;
  END LOOP;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN greatest(0::bigint, all_n - vis_n);
END $$;

CREATE OR REPLACE FUNCTION fleet_write_tokens(eid text) RETURNS void LANGUAGE sql AS $$
 WITH doc_evs AS MATERIALIZED (
  SELECT data, seq
  FROM document
  WHERE kind='event' AND data->>'executionId'=eid
 ),
 evs AS (
  SELECT data->'data'->'fleetNormalized'->>'lifecycle' AS lc, data->'data'->'fleetNormalized'->'nativeUsage' AS u, seq
  FROM doc_evs WHERE jsonb_typeof(data->'data'->'fleetNormalized'->'nativeUsage')='object'
 ),
 calls AS (SELECT u->'last' AS u FROM evs WHERE lc='usage.observation' AND jsonb_typeof(u->'last')='object'),
 rep AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE lc IN ('result','turn.completed') AND NOT EXISTS (SELECT 1 FROM calls) ORDER BY seq DESC LIMIT 1
 ),
 inc_native AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE coalesce(lc,'') NOT IN ('usage.observation','result','turn.completed')
   AND NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
 ),
 inc_assistant AS (
  SELECT DISTINCT ON (mid) mu AS u FROM (
   SELECT
    coalesce(d.data->'data'->'fleetNormalized'->'message'->>'id', d.data->'data'->'message'->>'id', d.seq::text) AS mid,
    coalesce(d.data->'data'->'fleetNormalized'->'message'->'usage', d.data->'data'->'message'->'usage') AS mu,
    d.seq
   FROM doc_evs d
   WHERE NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
    AND coalesce(d.data->'data'->'fleetNormalized'->>'lifecycle', d.data->'data'->>'type')='assistant'
    AND jsonb_typeof(d.data->'data'->'fleetNormalized'->'nativeUsage') IS DISTINCT FROM 'object'
  ) m WHERE jsonb_typeof(mu)='object' ORDER BY mid, seq DESC
 ),
 rows AS (
  SELECT
   fleet_token_count(coalesce(u->'input_tokens',u->'inputTokens')) AS input,
   fleet_token_count(coalesce(u->'output_tokens',u->'outputTokens')) AS output,
   fleet_token_count(coalesce(u->'cache_read_input_tokens',u->'cached_input_tokens',u->'cachedInputTokens',u->'cachedReadTokens',u->'cache_read_tokens')) AS cache_read,
   fleet_token_count(coalesce(u->'cache_creation_input_tokens',u->'cacheWriteInputTokens',u->'cachedWriteTokens',u->'cache_write_tokens')) AS cache_write,
   (u ? 'cached_input_tokens' OR u ? 'cachedInputTokens') AS input_has_cache
  FROM (SELECT u FROM calls UNION ALL SELECT u FROM rep UNION ALL SELECT u FROM inc_native UNION ALL SELECT u FROM inc_assistant) x
 ),
 n AS (
  SELECT
   sum(CASE WHEN input_has_cache AND input IS NOT NULL THEN greatest(0, input - coalesce(cache_read,0)) ELSE input END) AS input,
   sum(output) AS output, sum(cache_read) AS cache_read, sum(cache_write) AS cache_write
  FROM rows
 )
 UPDATE execution_totals SET
  tokens_input=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.input ELSE execution_totals.tokens_input END,
  tokens_input_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.input IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_input_basis END,
  tokens_output=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.output ELSE execution_totals.tokens_output END,
  tokens_output_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.output IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_output_basis END,
  tokens_cache_read=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_read ELSE execution_totals.tokens_cache_read END,
  tokens_cache_read_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.cache_read IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_cache_read_basis END,
  tokens_cache_write=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_write ELSE execution_totals.tokens_cache_write END,
  tokens_cache_write_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.cache_write IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_cache_write_basis END
 FROM n WHERE execution_id=eid
$$;

-- migration 52: suppress no-op store updates in fleet_artifact_change() so metadata/observed_at-only touches emit no change rows.
CREATE OR REPLACE FUNCTION fleet_artifact_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; p text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'store' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'observed_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'observed_at' - 'updated_at') THEN RETURN NULL; END IF; k := 'host'; i := r.host_id; h := r.host_id; ELSE IF TG_TABLE_NAME = 'artifact_copy' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'lease_expires_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'lease_expires_at' - 'updated_at') THEN RETURN NULL; END IF; i := r.artifact_id; ELSE i := r.id; END IF; k := 'artifact'; p := r.project_id; SELECT coalesce(a.task_id, e.task_id), e.session_id INTO t, s FROM artifact a LEFT JOIN execution e ON e.id = a.execution_id WHERE a.id = i; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, p, left(TG_OP, 1)); PERFORM pg_notify('fleet_change', ''); RETURN NULL; END $$;

-- migration 53: indexed transport validation and compact identities for closed ephemeral chunks.
ALTER INDEX document_data SET (fastupdate = on);
CREATE INDEX document_event_sequence ON document ((data->>'executionId'), ((data->>'seq')::bigint)) WHERE kind IN ('event','event-delta');
CREATE INDEX document_delta_block ON document ((data->>'executionId'), (data->'data'->'fleetNormalized'->>'deltaOf'), seq) WHERE kind='event-delta';
CREATE TABLE closed_delta_identity (
 id text PRIMARY KEY,
 execution_id text NOT NULL REFERENCES execution(id) ON DELETE CASCADE,
 event_seq bigint NOT NULL,
 digest text NOT NULL,
 event_type text NOT NULL,
 UNIQUE(execution_id,event_seq)
);
ALTER TABLE closed_delta_identity ENABLE ROW LEVEL SECURITY;
ALTER TABLE closed_delta_identity FORCE ROW LEVEL SECURITY;
CREATE POLICY execution_scope ON closed_delta_identity USING (EXISTS(SELECT 1 FROM execution WHERE execution.id=execution_id));


-- migration 54: trial attribution and reported native reasoning/API observations, including retained runs.
ALTER TABLE execution_totals
 ADD COLUMN tokens_reasoning bigint NULL,
 ADD COLUMN tokens_reasoning_basis text NOT NULL DEFAULT 'unavailable',
 ADD COLUMN api_ms bigint NULL,
 ADD COLUMN api_ms_basis text NOT NULL DEFAULT 'unavailable';

CREATE OR REPLACE FUNCTION fleet_write_native_observations(eid text) RETURNS void LANGUAGE sql AS $$
 WITH doc_evs AS MATERIALIZED (
  SELECT data, seq
  FROM document
  WHERE kind='event' AND data->>'executionId'=eid
 ),
 evs AS (
  SELECT data->'data'->'fleetNormalized'->>'lifecycle' AS lc, data->'data'->'fleetNormalized'->'nativeUsage' AS u, seq
  FROM doc_evs WHERE jsonb_typeof(data->'data'->'fleetNormalized'->'nativeUsage')='object'
 ),
 calls AS (SELECT u->'last' AS u FROM evs WHERE lc='usage.observation' AND jsonb_typeof(u->'last')='object'),
 rep AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE lc IN ('result','turn.completed') AND NOT EXISTS (SELECT 1 FROM calls) ORDER BY seq DESC LIMIT 1
 ),
 inc_native AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE coalesce(lc,'') NOT IN ('usage.observation','result','turn.completed')
   AND NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
 ),
 inc_assistant AS (
  SELECT DISTINCT ON (mid) mu AS u FROM (
   SELECT
    coalesce(d.data->'data'->'fleetNormalized'->'message'->>'id', d.data->'data'->'message'->>'id', d.seq::text) AS mid,
    coalesce(d.data->'data'->'fleetNormalized'->'message'->'usage', d.data->'data'->'message'->'usage') AS mu,
    d.seq
   FROM doc_evs d
   WHERE NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
    AND coalesce(d.data->'data'->'fleetNormalized'->>'lifecycle', d.data->'data'->>'type')='assistant'
    AND jsonb_typeof(d.data->'data'->'fleetNormalized'->'nativeUsage') IS DISTINCT FROM 'object'
  ) m WHERE jsonb_typeof(mu)='object' ORDER BY mid, seq DESC
 ),
 rows AS (
  SELECT fleet_token_count(coalesce(u->'reasoning_output_tokens',u->'reasoningOutputTokens',u->'thought_tokens',u->'thoughtTokens',u->'output_tokens_details'->'reasoning_tokens')) AS reasoning
  FROM (SELECT u FROM calls UNION ALL SELECT u FROM rep UNION ALL SELECT u FROM inc_native UNION ALL SELECT u FROM inc_assistant) x
 ), n AS (
  SELECT CASE WHEN count(*)=count(reasoning) THEN sum(reasoning) END AS reasoning FROM rows
 )
 UPDATE execution_totals SET tokens_reasoning=n.reasoning,
  tokens_reasoning_basis=CASE WHEN n.reasoning IS NULL THEN 'unavailable' ELSE 'measured' END
 FROM n WHERE execution_id=eid;
 WITH report AS (
  SELECT fleet_token_count(coalesce(data#>'{data,fleetNormalized,native,duration_api_ms}',data#>'{data,duration_api_ms}',data#>'{data,native,duration_api_ms}')) AS ms
  FROM document WHERE kind='event' AND data->>'executionId'=eid
   AND coalesce(data#>>'{data,fleetNormalized,lifecycle}',data->>'type')='result'
  ORDER BY seq DESC LIMIT 1
 ) UPDATE execution_totals SET api_ms=report.ms,
  api_ms_basis=CASE WHEN report.ms IS NULL THEN 'unavailable' ELSE 'measured' END
 FROM report WHERE execution_id=eid
$$;

-- Owner-only upgrade includes projectless retained executions. Suppress live derived-state triggers
-- for this attribution/observation repair; no work state or source event is changed.
ALTER TABLE execution DISABLE TRIGGER USER;
ALTER TABLE execution_totals DISABLE TRIGGER USER;
ALTER TABLE execution NO FORCE ROW LEVEL SECURITY;
ALTER TABLE execution_totals NO FORCE ROW LEVEL SECURITY;
ALTER TABLE document NO FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS event NO FORCE ROW LEVEL SECURITY; -- the events' table from migration 77, for a later replay of this pass
SET LOCAL row_security=off;

-- The trial's typed parent lineage is authoritative; profile names were incorrectly stored as variant labels.
WITH RECURSIVE lab_lineage AS (
 SELECT id,project_id,experiment_id,variant FROM execution WHERE kind='pipeline_run' AND experiment_id IS NOT NULL
 UNION
 SELECT e.id,e.project_id,p.experiment_id,p.variant FROM execution e JOIN lab_lineage p ON e.parent_execution_id=p.id
 WHERE e.project_id IS NOT DISTINCT FROM p.project_id AND NOT (e.kind='pipeline_run' AND e.experiment_id IS NOT NULL)
)
UPDATE execution e SET experiment_id=l.experiment_id,variant=l.variant FROM lab_lineage l
WHERE e.id=l.id AND (e.experiment_id IS DISTINCT FROM l.experiment_id OR e.variant IS DISTINCT FROM l.variant);
SELECT count(fleet_write_native_observations(t.execution_id)) FROM execution_totals t JOIN execution e ON e.id=t.execution_id
WHERE e.kind IN ('agent_run','child_agent');
ALTER TABLE execution FORCE ROW LEVEL SECURITY;
ALTER TABLE execution_totals FORCE ROW LEVEL SECURITY;
ALTER TABLE document FORCE ROW LEVEL SECURITY;
ALTER TABLE IF EXISTS event FORCE ROW LEVEL SECURITY;
RESET row_security;
ALTER TABLE execution ENABLE TRIGGER USER;
ALTER TABLE execution_totals ENABLE TRIGGER USER;

-- migration 55: transactional source capture for prepared project resource summaries.
-- No history scan or prepared read exposure; capture remains on when processing is stopped.
CREATE TABLE resource_work (
 work_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),
 execution_id text NOT NULL,
 origin text NOT NULL DEFAULT 'source' CHECK (origin IN ('source','build','metadata')),
 build_generation uuid NULL,
 repair_id uuid NULL,
 first_pending_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 last_error_class text NULL,
 CONSTRAINT resource_work_source_key UNIQUE NULLS NOT DISTINCT (source_xid,execution_id,origin,build_generation,repair_id)
);
CREATE INDEX resource_work_eligible ON resource_work(next_attempt_at,first_pending_at,work_id);
CREATE INDEX resource_work_execution ON resource_work(execution_id);
CREATE INDEX resource_work_repair ON resource_work(repair_id) WHERE repair_id IS NOT NULL;
CREATE TABLE resource_work_scope (
 work_id uuid NOT NULL REFERENCES resource_work ON DELETE CASCADE,
 project_id text NOT NULL,
 PRIMARY KEY(work_id,project_id)
);
CREATE INDEX resource_work_scope_project ON resource_work_scope(project_id,work_id);
CREATE TABLE resource_metadata_repair (
 repair_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),
 metadata_id text NOT NULL,
 old_class jsonb NOT NULL,
 new_class jsonb NOT NULL,
 after_execution_id text NULL,
 done boolean NOT NULL DEFAULT false,
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_error text NULL,
 UNIQUE(source_xid,metadata_id)
);
CREATE INDEX resource_metadata_repair_eligible ON resource_metadata_repair(next_attempt_at,created_at);
CREATE TABLE resource_repair_scope (
 repair_id uuid NOT NULL REFERENCES resource_metadata_repair ON DELETE CASCADE,
 project_id text NOT NULL,
 PRIMARY KEY(repair_id,project_id)
);
CREATE INDEX resource_repair_scope_project ON resource_repair_scope(project_id,repair_id);
-- Pending work is maintenance state. It has no public operation and is never a source of private facts.
ALTER TABLE resource_work ENABLE ROW LEVEL SECURITY;
ALTER TABLE resource_work FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON resource_work USING (current_setting('fleet.project_ids',true)='*');
ALTER TABLE resource_work_scope ENABLE ROW LEVEL SECURITY;
ALTER TABLE resource_work_scope FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON resource_work_scope USING (current_setting('fleet.project_ids',true)='*');
ALTER TABLE resource_metadata_repair ENABLE ROW LEVEL SECURITY;
ALTER TABLE resource_metadata_repair FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON resource_metadata_repair USING (current_setting('fleet.project_ids',true)='*');
ALTER TABLE resource_repair_scope ENABLE ROW LEVEL SECURITY;
ALTER TABLE resource_repair_scope FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON resource_repair_scope USING (current_setting('fleet.project_ids',true)='*');

CREATE FUNCTION resource_enqueue_source(eid text, old_project text, new_project text) RETURNS void
LANGUAGE plpgsql SET search_path=public AS $$
DECLARE wid uuid;
BEGIN
 INSERT INTO resource_work(execution_id) VALUES(eid)
 ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id
 RETURNING work_id INTO wid;
 INSERT INTO resource_work_scope(work_id,project_id)
 SELECT wid,p FROM (VALUES(old_project),(new_project)) v(p) WHERE p IS NOT NULL
 ON CONFLICT DO NOTHING;
 PERFORM pg_notify('fleet_resource_work','');
END $$;
REVOKE ALL ON FUNCTION resource_enqueue_source(text,text,text) FROM PUBLIC;

-- Stable allowlist: touches, arbitrary JSON, model-call payloads and computed_at do not enqueue work.
CREATE FUNCTION resource_execution_facts(r execution) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT jsonb_build_array(r.kind,r.project_id,r.state,r.account_id,r.harness,r.model,r.started_at,r.ended_at,
 CASE WHEN r.started_at IS NULL AND NOT (coalesce(r.data->>'startedAt','') ~ '^[0-9]{1,15}$') AND r.ended_at IS NULL THEN r.last_seen_at END,
 r.data->'provider',r.data->'connection',r.data->'tool',r.data->'outcome',r.data->'startedAt')
$$;
CREATE FUNCTION resource_capture_execution() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true); eid text; old_project text; new_project text;
BEGIN
 IF TG_TABLE_NAME <> 'execution' OR TG_WHEN <> 'AFTER' THEN RAISE EXCEPTION 'invalid resource capture context'; END IF;
 IF TG_OP='UPDATE' AND resource_execution_facts(OLD) IS NOT DISTINCT FROM resource_execution_facts(NEW) THEN RETURN NULL; END IF;
 IF TG_OP <> 'INSERT' THEN eid:=OLD.id; old_project:=OLD.project_id; END IF;
 IF TG_OP <> 'DELETE' THEN eid:=NEW.id; new_project:=NEW.project_id; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 PERFORM resource_enqueue_source(eid,old_project,new_project);
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
 RETURN NULL;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;
REVOKE ALL ON FUNCTION resource_capture_execution() FROM PUBLIC;
CREATE TRIGGER resource_capture_execution AFTER INSERT OR UPDATE OR DELETE ON execution
 FOR EACH ROW EXECUTE FUNCTION resource_capture_execution();

CREATE FUNCTION resource_capture_totals() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true); eid text; old_project text; new_project text;
BEGIN
 IF TG_TABLE_NAME <> 'execution_totals' OR TG_WHEN <> 'AFTER' THEN RAISE EXCEPTION 'invalid resource capture context'; END IF;
 IF TG_OP='UPDATE' AND (to_jsonb(OLD)-ARRAY['execution_id','computed_at','rate_limit_last','rate_limit_last_basis'])
   IS NOT DISTINCT FROM (to_jsonb(NEW)-ARRAY['execution_id','computed_at','rate_limit_last','rate_limit_last_basis']) THEN RETURN NULL; END IF;
 IF TG_OP <> 'INSERT' THEN eid:=OLD.execution_id; old_project:=OLD.project_id; END IF;
 IF TG_OP <> 'DELETE' THEN eid:=NEW.execution_id; new_project:=NEW.project_id; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 PERFORM resource_enqueue_source(eid,old_project,new_project);
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
 RETURN NULL;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;
REVOKE ALL ON FUNCTION resource_capture_totals() FROM PUBLIC;
CREATE TRIGGER resource_capture_totals AFTER INSERT OR UPDATE OR DELETE ON execution_totals
 FOR EACH ROW EXECUTE FUNCTION resource_capture_totals();

CREATE FUNCTION resource_align_totals_owner() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true);
BEGIN
 IF TG_TABLE_NAME <> 'execution' OR TG_WHEN <> 'AFTER' OR TG_OP <> 'UPDATE' THEN RAISE EXCEPTION 'invalid resource alignment context'; END IF;
 IF OLD.project_id IS NOT DISTINCT FROM NEW.project_id THEN RETURN NULL; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 UPDATE execution_totals SET project_id=NEW.project_id WHERE execution_id=NEW.id AND project_id IS DISTINCT FROM NEW.project_id;
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
 RETURN NULL;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;
REVOKE ALL ON FUNCTION resource_align_totals_owner() FROM PUBLIC;
-- Alphabetical AFTER-trigger ordering aligns totals before execution capture.
CREATE TRIGGER resource_align_totals_owner AFTER UPDATE OF project_id ON execution
 FOR EACH ROW EXECUTE FUNCTION resource_align_totals_owner();

CREATE FUNCTION resource_metadata_class(r document) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT CASE WHEN r.kind IN ('api-connection','managed-account') THEN jsonb_build_object(
 'accountId',r.id,'connectionName',CASE WHEN r.kind='api-connection' THEN substr(r.id,12) END,
 'kind',r.kind,'provider',r.data->'provider') ELSE '{}'::jsonb END
$$;
CREATE FUNCTION resource_capture_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true); old_class jsonb:='{}'; new_class jsonb:='{}'; mid text;
BEGIN
 IF TG_TABLE_NAME <> 'document' OR TG_WHEN <> 'AFTER' THEN RAISE EXCEPTION 'invalid metadata capture context'; END IF;
 IF TG_OP <> 'INSERT' THEN old_class:=resource_metadata_class(OLD); mid:=OLD.id; END IF;
 IF TG_OP <> 'DELETE' THEN new_class:=resource_metadata_class(NEW); mid:=NEW.id; END IF;
 IF old_class IS NOT DISTINCT FROM new_class THEN RETURN NULL; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 INSERT INTO resource_metadata_repair(metadata_id,old_class,new_class) VALUES(mid,old_class,new_class)
 ON CONFLICT(source_xid,metadata_id) DO UPDATE SET new_class=excluded.new_class;
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
 PERFORM pg_notify('fleet_resource_work','');
 RETURN NULL;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;
REVOKE ALL ON FUNCTION resource_capture_metadata() FROM PUBLIC;
CREATE TRIGGER resource_capture_metadata_insert AFTER INSERT ON document
 FOR EACH ROW WHEN (NEW.kind IN ('api-connection','managed-account')) EXECUTE FUNCTION resource_capture_metadata();
CREATE TRIGGER resource_capture_metadata_update AFTER UPDATE ON document
 FOR EACH ROW WHEN (OLD.kind IN ('api-connection','managed-account') OR NEW.kind IN ('api-connection','managed-account')) EXECUTE FUNCTION resource_capture_metadata();
CREATE TRIGGER resource_capture_metadata_delete AFTER DELETE ON document
 FOR EACH ROW WHEN (OLD.kind IN ('api-connection','managed-account')) EXECUTE FUNCTION resource_capture_metadata();

-- migration 56: replacement-safe prepared resources; capture remains always enabled.
CREATE INDEX resource_work_oldest ON resource_work(first_pending_at);
CREATE TABLE resource_contribution (
 execution_id text PRIMARY KEY, project_id text NULL, source_sha text NOT NULL,
 included boolean NOT NULL, facts jsonb NOT NULL, method jsonb NOT NULL,
 method_sha text NOT NULL, updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX resource_contribution_project ON resource_contribution(project_id,execution_id COLLATE "C");
CREATE TABLE resource_valuation (
 execution_id text PRIMARY KEY, method_sha text NOT NULL, method jsonb NOT NULL,
 chosen_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE resource_project_summary (
 project_id text PRIMARY KEY, epoch uuid NOT NULL DEFAULT gen_random_uuid(), revision bigint NOT NULL DEFAULT 0 CHECK(revision>=0),
 build_generation uuid NOT NULL, ready boolean NOT NULL DEFAULT false, header jsonb NOT NULL DEFAULT '{}', published_at timestamptz NULL,
 executions bigint NOT NULL DEFAULT 0 CHECK(executions>=0), history_incomplete bigint NOT NULL DEFAULT 0 CHECK(history_incomplete>=0),
 other_calls bigint NOT NULL DEFAULT 0 CHECK(other_calls>=0), other_errors bigint NOT NULL DEFAULT 0 CHECK(other_errors>=0),
 tool_count bigint NOT NULL DEFAULT 0 CHECK(tool_count>=0), cost_count bigint NOT NULL DEFAULT 0 CHECK(cost_count>=0), price_count bigint NOT NULL DEFAULT 0 CHECK(price_count>=0)
);
CREATE TYPE resource_metric_name AS ENUM ('input','output','cacheRead','cacheWrite','reasoning','wallMs','activeMs','apiMs','cpuMs','peakMemoryBytes','credits','turns','toolCalls','toolErrors','tokenActors','processActors','toolActors');
CREATE TABLE resource_metric (
 project_id text NOT NULL, metric resource_metric_name NOT NULL, basis text NOT NULL CHECK(basis IN ('measured','estimated','subscription','unavailable')),
 present_count bigint NOT NULL CHECK(present_count>=0), expected_count bigint NOT NULL CHECK(expected_count>=0),
 sum_value numeric NOT NULL, reasons jsonb NOT NULL DEFAULT '{}', PRIMARY KEY(project_id,metric,basis)
);
CREATE TABLE resource_extreme (
 project_id text NOT NULL, metric text NOT NULL CHECK(metric IN ('start','end','peakMemory')),
 value bigint NOT NULL, multiplicity bigint NOT NULL CHECK(multiplicity>0), PRIMARY KEY(project_id,metric,value)
);
CREATE INDEX resource_extreme_last ON resource_extreme(project_id,metric,value DESC);
CREATE TABLE resource_tool (
 project_id text NOT NULL, connection text NOT NULL, name text NOT NULL,
 calls bigint NOT NULL CHECK(calls>=0), errors bigint NOT NULL CHECK(errors>=0), references_count bigint NOT NULL CHECK(references_count>0),
 PRIMARY KEY(project_id,connection,name)
);
CREATE INDEX resource_tool_page ON resource_tool(project_id,name COLLATE "C",connection COLLATE "C");
CREATE TABLE resource_cost (
 project_id text NOT NULL, currency text NOT NULL, basis text NOT NULL CHECK(basis IN ('measured','estimated','subscription','unavailable')),
 value numeric NULL, usages bigint NOT NULL CHECK(usages>=0), references_count bigint NOT NULL CHECK(references_count>0), PRIMARY KEY(project_id,currency,basis)
);
CREATE TABLE resource_price_ref (
 project_id text NOT NULL, currency text NOT NULL, basis text NOT NULL, price_id text NOT NULL,
 observed_at bigint NOT NULL, references_count bigint NOT NULL CHECK(references_count>0), PRIMARY KEY(project_id,currency,basis,price_id,observed_at)
);
CREATE INDEX resource_price_ref_page ON resource_price_ref(project_id,currency,basis,price_id COLLATE "C",observed_at);
CREATE TABLE resource_build (
 project_id text PRIMARY KEY, generation uuid NOT NULL DEFAULT gen_random_uuid(), after_execution_id text NULL,
 scan_done boolean NOT NULL DEFAULT false, ready boolean NOT NULL DEFAULT false,
 phase text NOT NULL DEFAULT 'scanning' CHECK(phase IN ('clearing','scanning','draining','ready')),
 last_error text NULL, updated_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
-- Bounded project scans and metadata union scans use these keyset indexes.
CREATE INDEX resource_execution_project_page ON execution(project_id,id COLLATE "C");
CREATE INDEX resource_execution_account_page ON execution(account_id,id COLLATE "C");
CREATE INDEX resource_execution_connection_page ON execution((data->>'connection'),id COLLATE "C");
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['resource_contribution','resource_project_summary','resource_metric','resource_extreme','resource_tool','resource_cost','resource_price_ref','resource_build'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY',t);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY',t);
  EXECUTE format('CREATE POLICY project_scope ON %I USING (current_setting(''fleet.project_ids'',true)=''*'' OR project_id=ANY(string_to_array(current_setting(''fleet.project_ids'',true),'','')))',t);
 END LOOP;
END $$;
ALTER TABLE resource_valuation ENABLE ROW LEVEL SECURITY;
ALTER TABLE resource_valuation FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON resource_valuation USING(current_setting('fleet.project_ids',true)='*');

-- migration 57
-- Prepared pages use the same C-order tuple as their cursors, including cost groups.
CREATE INDEX resource_cost_page ON resource_cost(project_id,currency COLLATE "C",basis COLLATE "C");
-- A committed header/revision and its public change are one transaction. Existing change
-- RLS and XID barriers serve both SSE and reconnect; NOTIFY alone is never the record.
CREATE FUNCTION fleet_resource_summary_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE k text; BEGIN
 k := 'resource_summary';
 INSERT INTO change(kind,id,project_id,op) VALUES(k,NEW.project_id,NEW.project_id,'U');
 PERFORM pg_notify('fleet_change','');
 RETURN NULL;
END $$;
CREATE TRIGGER resource_summary_change AFTER UPDATE OF revision ON resource_project_summary
 FOR EACH ROW WHEN (OLD.revision IS DISTINCT FROM NEW.revision) EXECUTE FUNCTION fleet_resource_summary_change();


-- migration 58: durable notification intents keep NOTIFY's commit mutex out of source transactions.
CREATE TABLE wake_outbox (
 wake_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 source_xid xid8 NOT NULL DEFAULT pg_current_xact_id(),
 channel text NOT NULL CHECK (channel IN ('fleet_change','fleet_resource_work','fleet_resource_summary')),
 payload text NOT NULL CHECK (octet_length(payload)<8000),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp()
);
CREATE INDEX wake_outbox_pending ON wake_outbox(created_at,wake_id);
ALTER TABLE wake_outbox ENABLE ROW LEVEL SECURITY;
ALTER TABLE wake_outbox FORCE ROW LEVEL SECURITY;
CREATE POLICY maintenance ON wake_outbox USING (current_setting('fleet.project_ids',true)='*');

-- Only an append enters maintenance scope; callers cannot supply a source XID or modify old intents.
CREATE FUNCTION fleet_enqueue_wake(ch text, body text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true);
BEGIN
 IF ch IS NULL OR body IS NULL OR ch NOT IN ('fleet_change','fleet_resource_work','fleet_resource_summary')
  OR octet_length(body)>=8000 OR (ch<>'fleet_resource_summary' AND body<>'') THEN
  RAISE EXCEPTION 'invalid wake channel or payload';
 END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 INSERT INTO wake_outbox(channel,payload) VALUES(ch,body);
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;
REVOKE ALL ON FUNCTION fleet_enqueue_wake(text,text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION fleet_enqueue_wake(text,text) TO fleet_app;

CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; ek text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change', 'project-recommendation') THEN k := r.kind; w := r.data->>'holder'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN IF r.kind = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN SELECT e.kind::text, e.task_id, e.session_id, e.host_id INTO ek, t, s, h FROM execution e WHERE e.id = r.execution_id; IF ek = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $$;

CREATE OR REPLACE FUNCTION fleet_artifact_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; p text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'store' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'observed_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'observed_at' - 'updated_at') THEN RETURN NULL; END IF; k := 'host'; i := r.host_id; h := r.host_id; ELSE IF TG_TABLE_NAME = 'artifact_copy' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'lease_expires_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'lease_expires_at' - 'updated_at') THEN RETURN NULL; END IF; i := r.artifact_id; ELSE i := r.id; END IF; k := 'artifact'; p := r.project_id; SELECT coalesce(a.task_id, e.task_id), e.session_id INTO t, s FROM artifact a LEFT JOIN execution e ON e.id = a.execution_id WHERE a.id = i; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, p, left(TG_OP, 1)); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $$;

CREATE OR REPLACE FUNCTION resource_enqueue_source(eid text, old_project text, new_project text) RETURNS void
LANGUAGE plpgsql SET search_path=public AS $$
DECLARE wid uuid;
BEGIN
 INSERT INTO resource_work(execution_id) VALUES(eid)
 ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id
 RETURNING work_id INTO wid;
 INSERT INTO resource_work_scope(work_id,project_id)
 SELECT wid,p FROM (VALUES(old_project),(new_project)) v(p) WHERE p IS NOT NULL
 ON CONFLICT DO NOTHING;
 PERFORM fleet_enqueue_wake('fleet_resource_work','');
END $$;

CREATE OR REPLACE FUNCTION resource_capture_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true); old_class jsonb:='{}'; new_class jsonb:='{}'; mid text;
BEGIN
 IF TG_TABLE_NAME <> 'document' OR TG_WHEN <> 'AFTER' THEN RAISE EXCEPTION 'invalid metadata capture context'; END IF;
 IF TG_OP <> 'INSERT' THEN old_class:=resource_metadata_class(OLD); mid:=OLD.id; END IF;
 IF TG_OP <> 'DELETE' THEN new_class:=resource_metadata_class(NEW); mid:=NEW.id; END IF;
 IF old_class IS NOT DISTINCT FROM new_class THEN RETURN NULL; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 INSERT INTO resource_metadata_repair(metadata_id,old_class,new_class) VALUES(mid,old_class,new_class)
 ON CONFLICT(source_xid,metadata_id) DO UPDATE SET new_class=excluded.new_class;
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
 PERFORM fleet_enqueue_wake('fleet_resource_work','');
 RETURN NULL;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;

CREATE OR REPLACE FUNCTION fleet_resource_summary_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE k text; BEGIN
 k := 'resource_summary';
 INSERT INTO change(kind,id,project_id,op) VALUES(k,NEW.project_id,NEW.project_id,'U');
 PERFORM fleet_enqueue_wake('fleet_change','');
 RETURN NULL;
END $$;

-- migration 59: delivery passes reach watchers through the change stream (kind delivery): delivery-state is written per
-- project when its pull requests change; the history's records (pull requests, runs, releases) and its regression check
-- are written only when they change (dhPut skips equal data).
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; ek text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change', 'project-recommendation') THEN k := r.kind; w := r.data->>'holder'; ELSIF r.kind IN ('delivery-state', 'delivery-pr', 'delivery-run', 'delivery-release', 'delivery-check') THEN k := 'delivery'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN IF r.kind = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN SELECT e.kind::text, e.task_id, e.session_id, e.host_id INTO ek, t, s, h FROM execution e WHERE e.id = r.execution_id; IF ek = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $$;


-- migration 60: capture authoritative audit targets with their committed change; legacy rows remain unknown.
ALTER TABLE change ADD COLUMN setting_target text NULL;
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; ek text; st text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'setting-change' THEN st := nullif(r.data->>'target', ''); END IF; IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'session' THEN k := 'session'; s := r.id; ELSIF r.kind IN ('event', 'event-delta', 'message') THEN k := 'event'; s := r.data->>'sessionId'; ELSIF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'command' THEN k := 'command'; h := r.data->>'hostId'; s := r.data->>'sessionId'; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change', 'project-recommendation') THEN k := r.kind; w := r.data->>'holder'; ELSIF r.kind IN ('delivery-state', 'delivery-pr', 'delivery-run', 'delivery-release', 'delivery-check') THEN k := 'delivery'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN IF r.kind = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN SELECT e.kind::text, e.task_id, e.session_id, e.host_id INTO ek, t, s, h FROM execution e WHERE e.id = r.execution_id; IF ek = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder, setting_target) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w, st); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $$;

-- migration 61: Tasks group by project by default (owner, 2026-11-16), so the default page order (project, then changed
-- newest first) is indexed like the priority grouping in migration 47.
CREATE INDEX task_sort_project_changed_desc ON task((coalesce(project_id,'~')), (-((extract(epoch FROM updated_at AT TIME ZONE 'UTC')*1000)::float8)), id);
CREATE INDEX task_sort_project_changed ON task((coalesce(project_id,'~')), ((extract(epoch FROM updated_at AT TIME ZONE 'UTC')*1000)::float8), id);
CREATE INDEX task_sort_project_created_desc ON task((coalesce(project_id,'~')), (-(seq::float8)), id);
CREATE INDEX task_sort_project_created ON task((coalesce(project_id,'~')), (seq::float8), id);

-- migration 62: browser errors of the operator app (client_error_report, docs/designs/fleet-telemetry.md section 9):
-- one group per fingerprint (name, message, normalized top frame) and its latest reports (at most 20 per group);
-- first_person and first_full bound the new groups one person, and people without full access together, open.
-- System data: only the unrestricted scope sees it; the report operation writes it in the coordinator's own scope.
CREATE TABLE client_error(
  fingerprint text PRIMARY KEY,
  name text NOT NULL,
  message text NOT NULL,
  top_frame text NOT NULL,
  source text NOT NULL,
  first_seen timestamptz NOT NULL DEFAULT now(),
  last_seen timestamptz NOT NULL DEFAULT now(),
  count bigint NOT NULL,
  last_version text NOT NULL,
  last_path text NOT NULL,
  last_user_agent text NOT NULL,
  people text[] NOT NULL DEFAULT '{}',
  versions text[] NOT NULL DEFAULT '{}',
  first_person text NOT NULL DEFAULT '',
  first_full boolean NOT NULL DEFAULT false
);
CREATE INDEX client_error_last_seen ON client_error(last_seen DESC, fingerprint);
CREATE INDEX client_error_first_person ON client_error(first_person, first_seen);
CREATE TABLE client_error_report(
  seq bigserial PRIMARY KEY,
  fingerprint text NOT NULL REFERENCES client_error(fingerprint) ON DELETE CASCADE,
  at timestamptz NOT NULL DEFAULT now(),
  client_at timestamptz NOT NULL,
  person text NOT NULL,
  version text NOT NULL,
  path text NOT NULL,
  user_agent text NOT NULL,
  count bigint NOT NULL,
  source text NOT NULL,
  stack text NOT NULL
);
CREATE INDEX client_error_report_fingerprint ON client_error_report(fingerprint, seq DESC);
ALTER TABLE client_error ENABLE ROW LEVEL SECURITY;
ALTER TABLE client_error FORCE ROW LEVEL SECURITY;
CREATE POLICY operator_scope ON client_error USING (current_setting('fleet.project_ids', true) = '*');
ALTER TABLE client_error_report ENABLE ROW LEVEL SECURITY;
ALTER TABLE client_error_report FORCE ROW LEVEL SECURITY;
CREATE POLICY operator_scope ON client_error_report USING (current_setting('fleet.project_ids', true) = '*');
-- client_errors_list names the problem question holding each group's cause, open or answered (dismissed).
CREATE INDEX question_cause ON question(cause, seq) WHERE cause IS NOT NULL;

-- migration 63: every prepared read reports its project's oldest pending work. Joining all of a project's pending scopes
-- to resource_work made that read cost grow with the backlog, and 50 readers' reads then starved the processor that
-- drains it. first_pending_at does not change in normal operation, so each scope row carries a copy (kept equal by a
-- trigger), indexed per project. A scope row whose work row is not yet visible to its
-- insert (an older coordinator's single-statement seeding) keeps NULL and is read through the old join over its own
-- partial index; failed work (normally none) has its own partial index and drives the error lookup.
SELECT set_config('fleet.project_ids','*',true);
ALTER TABLE resource_work_scope ADD COLUMN first_pending_at timestamptz;
UPDATE resource_work_scope ws SET first_pending_at=w.first_pending_at FROM resource_work w WHERE w.work_id=ws.work_id;
CREATE INDEX resource_work_scope_oldest ON resource_work_scope(project_id,first_pending_at) WHERE first_pending_at IS NOT NULL;
CREATE INDEX resource_work_scope_unstamped ON resource_work_scope(project_id) WHERE first_pending_at IS NULL;
CREATE INDEX resource_work_failed ON resource_work(first_pending_at,work_id) WHERE last_error_class IS NOT NULL;
CREATE FUNCTION resource_work_pending_copy() RETURNS trigger LANGUAGE plpgsql SET search_path=public AS $$
BEGIN UPDATE resource_work_scope SET first_pending_at=NEW.first_pending_at WHERE work_id=NEW.work_id; RETURN NULL; END $$;
CREATE TRIGGER resource_work_pending_copy AFTER UPDATE OF first_pending_at ON resource_work FOR EACH ROW
 WHEN (NEW.first_pending_at IS DISTINCT FROM OLD.first_pending_at) EXECUTE FUNCTION resource_work_pending_copy();
-- Writers pass the copy; an insert without it (an older coordinator) takes it from an already visible work row.
CREATE FUNCTION resource_work_scope_stamp() RETURNS trigger LANGUAGE plpgsql SET search_path=public AS $$
BEGIN
 IF NEW.first_pending_at IS NULL THEN SELECT first_pending_at INTO NEW.first_pending_at FROM resource_work WHERE work_id=NEW.work_id; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER resource_work_scope_stamp BEFORE INSERT ON resource_work_scope FOR EACH ROW
 WHEN (NEW.first_pending_at IS NULL) EXECUTE FUNCTION resource_work_scope_stamp();
CREATE OR REPLACE FUNCTION resource_enqueue_source(eid text, old_project text, new_project text) RETURNS void
LANGUAGE plpgsql SET search_path=public AS $$
DECLARE wid uuid; pending timestamptz;
BEGIN
 INSERT INTO resource_work(execution_id) VALUES(eid)
 ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id
 RETURNING work_id,first_pending_at INTO wid,pending;
 INSERT INTO resource_work_scope(work_id,project_id,first_pending_at)
 SELECT wid,p,pending FROM (VALUES(old_project),(new_project)) v(p) WHERE p IS NOT NULL
 ON CONFLICT DO NOTHING;
 PERFORM fleet_enqueue_wake('fleet_resource_work','');
END $$;

-- migration 64: a replacement transaction writes its net reason-count changes per metric row at once. Merging keeps a
-- reason while its count is positive, drops it at zero and rejects a negative count as a corrupt ledger.
CREATE FUNCTION resource_reasons_merge(old jsonb, delta jsonb) RETURNS jsonb LANGUAGE plpgsql IMMUTABLE SET search_path=public AS $$
DECLARE merged jsonb;
BEGIN
 SELECT coalesce(jsonb_object_agg(k,v),'{}'::jsonb) INTO merged FROM (
  SELECT k,sum(v) v FROM (SELECT key k,value::bigint v FROM jsonb_each_text(old) UNION ALL SELECT key,value::bigint FROM jsonb_each_text(delta)) x
  GROUP BY k HAVING sum(v)<>0) y;
 IF EXISTS(SELECT 1 FROM jsonb_each_text(merged) WHERE value::bigint<0) THEN RAISE EXCEPTION 'negative resource reason count'; END IF;
 RETURN merged;
END $$;

-- migration 65: resource_work, its scopes and wake_outbox are queues whose rows are inserted and deleted within seconds.
-- Autovacuum's truncation of their empty tail pages takes ACCESS EXCLUSIVE on the table; in the combined qualification
-- (m20r2) it coincided with a 10 s processor stall during a burst and a late source window, because every capture
-- trigger and claim waits for that lock. The space is reused by the next inserts anyway, so the tail is kept.
ALTER TABLE resource_work SET (vacuum_truncate=false);
ALTER TABLE resource_work_scope SET (vacuum_truncate=false);
ALTER TABLE wake_outbox SET (vacuum_truncate=false);

-- migration 66: browser errors moved to Bugsink, Fleet's error tracker (docs/designs/fleet-error-tracking.md). The
-- client_error store of migration 62 is dropped; its open problems are withdrawn and answered ones let go of their cause.
-- question_cause stays: every cause lookup uses it.
SELECT set_config('fleet.project_ids','*',true);
UPDATE question SET state=CASE WHEN state='open' THEN 'withdrawn'::question_state ELSE state END, cause=NULL
 WHERE cause LIKE 'problem:client-error:%' AND state IN ('open','answered');
DROP TABLE client_error_report;
DROP TABLE client_error;

-- migration 67: delete and purge (docs/designs/fleet-system-management.md, Archive, delete, purge). object_delete
-- tombstones an object with every row that goes with it (cleanup.go, reference map): a deleted project keeps its
-- definition row (deleted_at and retired_at; purged_at once system_purge removed its rows), so its id is never reused;
-- every other row of the cascade gets tombstoned_at (artifact.deleted_at already means "bytes removed by retention").
-- A restrictive SELECT policy on every project-owned table hides tombstoned rows and every row of a deleted project
-- from every read, the owner's and the coordinator's own passes included. fleet.deleted=on shows them: host calls, so
-- the stop and teardown of deleted work still land, and delete and purge themselves. The change feed (pruned after a
-- day) and the resource tables (read only after a project check) keep their own lifecycle.
ALTER TABLE definition ADD COLUMN deleted_at timestamptz NULL, ADD COLUMN purged_at timestamptz NULL;
ALTER TABLE execution ADD COLUMN tombstoned_at timestamptz NULL;
ALTER TABLE task ADD COLUMN tombstoned_at timestamptz NULL;
ALTER TABLE experiment ADD COLUMN tombstoned_at timestamptz NULL;
ALTER TABLE question ADD COLUMN tombstoned_at timestamptz NULL;
ALTER TABLE artifact ADD COLUMN tombstoned_at timestamptz NULL;
ALTER TABLE document ADD COLUMN tombstoned_at timestamptz NULL;
ALTER TABLE score ADD COLUMN tombstoned_at timestamptz NULL;
ALTER TABLE experiment_note ADD COLUMN tombstoned_at timestamptz NULL;
CREATE FUNCTION fleet_show_deleted() RETURNS boolean LANGUAGE sql STABLE AS $$ SELECT coalesce(current_setting('fleet.deleted', true), '') = 'on' $$;
CREATE FUNCTION fleet_deleted_projects() RETURNS text[] LANGUAGE sql STABLE AS
 $$ SELECT coalesce(array_agg(id), '{}') FROM definition WHERE kind = 'project' AND deleted_at IS NOT NULL $$;
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['execution','task','experiment','question','artifact','document','score','experiment_note'] LOOP
  EXECUTE format('CREATE POLICY live ON %I AS RESTRICTIVE FOR SELECT USING (fleet_show_deleted() OR (tombstoned_at IS NULL'
   ' AND (project_id IS NULL OR NOT project_id = ANY ((SELECT fleet_deleted_projects())::text[])%s)))', t,
   CASE WHEN t = 'document' THEN ' OR kind IN (''setting-change'', ''transport-audit'')' ELSE '' END); -- audit outlives the project
 END LOOP;
 FOREACH t IN ARRAY ARRAY['artifact_copy','artifact_rule_confirmation','entity_code','execution_totals','purged_hash',
  'route_decision','task_dependency','task_derived'] LOOP
  EXECUTE format('CREATE POLICY live ON %I AS RESTRICTIVE FOR SELECT USING (fleet_show_deleted()'
   ' OR project_id IS NULL OR NOT project_id = ANY ((SELECT fleet_deleted_projects())::text[]))', t);
 END LOOP;
END $$;
-- A row a host's own call writes later for a tombstoned execution (an event, a receipt, a stop landing after the
-- delete) is born tombstoned.
CREATE FUNCTION fleet_inherit_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE e text; BEGIN
 -- Only a writer that sees deleted rows: for any other one a tombstoned row would fail the policy's check on an upsert.
 IF NEW.tombstoned_at IS NOT NULL OR NOT fleet_show_deleted() THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME = 'document' THEN
  IF NEW.kind IN ('setting-change', 'transport-audit', 'purge-task') THEN RETURN NEW; END IF; -- audit outlives the object
  e := NEW.data->>'executionId';
  IF e IS NULL AND NEW.data->>'sessionId' IS NOT NULL AND EXISTS (SELECT 1 FROM document WHERE kind = 'session' AND id = NEW.data->>'sessionId' AND tombstoned_at IS NOT NULL) THEN
   NEW.tombstoned_at := now(); RETURN NEW;
  END IF;
 ELSE e := to_jsonb(NEW)->>TG_ARGV[0]; END IF;
 IF e IS NOT NULL AND EXISTS (SELECT 1 FROM execution WHERE id = e AND tombstoned_at IS NOT NULL) THEN NEW.tombstoned_at := now(); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER document_tombstone BEFORE INSERT ON document FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone();
CREATE TRIGGER artifact_tombstone BEFORE INSERT ON artifact FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
CREATE TRIGGER question_tombstone BEFORE INSERT ON question FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
CREATE TRIGGER execution_tombstone BEFORE INSERT ON execution FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('parent_execution_id');
-- Notes stay append-only for every writer but delete, which tombstones a deleted experiment's notes, and system_purge,
-- which removes them.
CREATE OR REPLACE FUNCTION fleet_note_append_only() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF TG_OP = 'DELETE' AND coalesce(current_setting('fleet.purge', true), '') = 'on' THEN RETURN OLD; END IF;
 IF TG_OP = 'UPDATE' AND to_jsonb(NEW) - 'tombstoned_at' = to_jsonb(OLD) - 'tombstoned_at' THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'experiment notes are append-only';
END $$;

-- migration 68: "no project" is NULL. A read token minted for a projectless pass (bugsink-resolved) carried project ''
-- into its tool_run executions, so every resource scope query returned '': the replacement's lock "project:" is not a
-- lock scope, and the processor retried that work every pass and failed every call. The rows get NULL and their
-- totals the execution's project, the '' scopes those updates enqueue go (repair scopes copy work scopes),
-- and the check keeps '' out; every totals, contribution and scope writer copies the project from the execution.
SELECT set_config('fleet.project_ids','*',true), set_config('fleet.deleted','on',true);
UPDATE execution SET project_id=NULL WHERE project_id='';
UPDATE execution_totals t SET project_id=e.project_id FROM execution e WHERE e.id=t.execution_id AND t.project_id='';
DELETE FROM resource_work_scope WHERE project_id='';
DELETE FROM resource_repair_scope WHERE project_id='';
ALTER TABLE execution ADD CONSTRAINT execution_project_id_not_empty CHECK (project_id<>'');
-- migration 69: Fleet's own background calls (docs/designs/fleet-analytics.md, System activity). The coordinator's
-- GitHub and Bugsink reads (a run token "<connection>-read:<uuid>" minted for the project whose grant allows the read)
-- became tool_runs of that project and filled its analytics and execution lists. system_actor names the Fleet component
-- that made a call on its own ('coordinator'), written at insert; analytics_query, executions_list, project resources
-- and person/project budgets leave those rows out unless asked for; connection budgets and Runners keep them. Every
-- read token gets the mark (the first ones, 2026-11-12, a personal token named github-read, too), so calls ingested
-- after this migration carry it. The earlier rows of these reads get the mark and legacySystemRead in data;
-- systemReadsCleanup (system_activity.go) deletes them once no spool line names them. The mark is no resource fact
-- (resource_execution_facts), so the marked rows are queued for resource replacement here. No span names these calls
-- (a gateway call runs under no run context).
ALTER TABLE execution ADD COLUMN IF NOT EXISTS system_actor text NULL;
CREATE INDEX IF NOT EXISTS execution_system ON execution(seq) WHERE system_actor IS NOT NULL;
SELECT set_config('fleet.project_ids', '*', true), set_config('fleet.deleted', 'on', true);
UPDATE document SET data = data || '{"systemActor": "coordinator"}'
 WHERE kind = 'run-token' AND data->>'executionId' ~ '^[a-z0-9-]+-read:[0-9a-f-]{36}$';
-- The 2026-11-12 calls (personal token github-read) have no parent and tokenName in data, minted for a project and no
-- person; a person's own token of that name has a director and no project, and keeps its calls.
CREATE TEMP TABLE system_read ON COMMIT DROP AS
 SELECT id, project_id FROM execution WHERE kind = 'tool_run' AND system_actor IS NULL
  AND (parent_execution_id ~ '^[a-z0-9-]+-read:[0-9a-f-]{36}$'
   OR (parent_execution_id IS NULL AND data->>'tokenName' = 'github-read' AND project_id IS NOT NULL AND directed_by IS NULL));
UPDATE execution SET system_actor = 'coordinator', data = data || '{"legacySystemRead": true}' WHERE id IN (SELECT id FROM system_read);
SELECT resource_enqueue_source(id, project_id, project_id) FROM system_read;

-- migration 70: the references of a document as indexed columns (cleanup.go, reference map). Under row-level security a
-- JSON path is never an index condition (jsonb_object_field_text is not leakproof), so each delete and purge check of
-- data->>'executionId' or data->>'sessionId' read and detoasted every document (2.3 s per check on 39k documents in
-- production). A stored generated column compares with texteq, which is leakproof, so its index serves those reads.
ALTER TABLE document ADD COLUMN execution_ref text GENERATED ALWAYS AS (data->>'executionId') STORED,
 ADD COLUMN session_ref text GENERATED ALWAYS AS (data->>'sessionId') STORED;
CREATE INDEX document_execution_ref ON document(execution_ref) WHERE execution_ref IS NOT NULL;
CREATE INDEX document_session_ref ON document(session_ref) WHERE session_ref IS NOT NULL;
-- migration 71: message attachments (docs/designs/fleet-conversations.md, Attachments). An operator upload is an artifact
-- with role 'attachment' and no execution; session_id names the conversation it was attached to (set by the upload or by
-- the first message_send/session_start that carries it). The change feed names that session for its rows.
ALTER TABLE artifact ADD COLUMN IF NOT EXISTS session_id text NULL;
CREATE INDEX IF NOT EXISTS artifact_session ON artifact(session_id, seq) WHERE session_id IS NOT NULL;
ALTER TABLE artifact DROP CONSTRAINT artifact_role_check;
ALTER TABLE artifact ADD CONSTRAINT artifact_role_check CHECK (role IN ('original', 'thumbnail', 'poster', 'example', 'example-file', 'attachment'));
CREATE OR REPLACE FUNCTION fleet_artifact_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; p text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'store' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'observed_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'observed_at' - 'updated_at') THEN RETURN NULL; END IF; k := 'host'; i := r.host_id; h := r.host_id; ELSE IF TG_TABLE_NAME = 'artifact_copy' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'lease_expires_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'lease_expires_at' - 'updated_at') THEN RETURN NULL; END IF; i := r.artifact_id; ELSE i := r.id; END IF; k := 'artifact'; p := r.project_id; SELECT coalesce(a.task_id, e.task_id), coalesce(a.session_id, e.session_id) INTO t, s FROM artifact a LEFT JOIN execution e ON e.id = a.execution_id WHERE a.id = i; IF TG_TABLE_NAME = 'artifact' THEN s := coalesce(s, r.session_id); END IF; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, p, left(TG_OP, 1)); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $$;
-- migration 72: the hidden-lineage counts of task_get and session_get (migration 51) reach the spawned tasks' executions
-- from those tasks (usually none), as analytics.go spawnedExecsCTEs does: one join of every execution with every task
-- under an OR condition was planned as a nested loop over both tables once statistics were current (27-53 s per call on
-- a 20000-task store, under the global write lock when task_update returns the task).
CREATE OR REPLACE FUNCTION fleet_task_hidden_lineage_count(tid text)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 vis_n bigint := 0;
 all_n bigint := 0;
 pass_n int;
 cur_n bigint;
BEGIN
 IF coalesce(tid, '') = '' OR NOT EXISTS (SELECT 1 FROM task WHERE id = tid) THEN
  RETURN 0;
 END IF;
 BEGIN
  FOR pass_n IN 0..1 LOOP
   IF pass_n = 1 THEN
    PERFORM set_config('fleet.project_ids', '*', true);
   END IF;
   WITH RECURSIVE
    candidate_sessions AS (
     SELECT DISTINCT session_id FROM execution WHERE task_id = tid AND session_id IS NOT NULL
    ),
    first_task AS (
     SELECT DISTINCT ON (e.session_id) e.session_id, e.task_id
     FROM execution e
     WHERE e.task_id IS NOT NULL AND e.session_id IN (SELECT session_id FROM candidate_sessions)
     ORDER BY e.session_id, e.seq
    ),
    direct_sessions AS (
     SELECT id AS session_id FROM document
     WHERE kind = 'session'
       AND (data->>'taskId' = tid OR (coalesce(nullif(data->>'taskId', ''), '') = '' AND id IN (SELECT session_id FROM first_task WHERE task_id = tid)))
       AND NOT (data ? 'parentExecutionId')
    ),
    own_execs AS (
     SELECT e.id, e.session_id
     FROM execution e
     WHERE e.kind <> 'child_agent'
       AND e.delegation IS NULL
       AND (
         e.task_id = tid
         OR (e.task_id IS NULL AND e.session_id IN (SELECT session_id FROM direct_sessions))
         OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
            SELECT p.id FROM execution p
            WHERE p.kind <> 'child_agent'
              AND p.delegation IS NULL
              AND (p.task_id = tid OR (p.task_id IS NULL AND p.session_id IN (SELECT session_id FROM direct_sessions)))
         ))
       )
    ),
    del_seed AS (
     SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
        OR (e.delegation IS NOT NULL AND (e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs) OR e.delegation->>'parentSessionId' IN (SELECT session_id FROM direct_sessions)))
     UNION
     SELECT e.id, e.session_id, true AS follow_session
     FROM execution e
     JOIN document s ON s.kind = 'session' AND s.id = e.session_id
     WHERE s.data->>'parentExecutionId' IN (SELECT id FROM own_execs)
        OR (s.data->>'createdByExecutionId' IN (SELECT id FROM own_execs) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
        OR (s.data->>'createdBySessionId' IN (SELECT session_id FROM direct_sessions) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
    ),
    del_tree AS (
     SELECT id, session_id, follow_session FROM del_seed
     UNION
     SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     JOIN del_tree dt ON e.parent_execution_id = dt.id
        OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id NOT IN (SELECT session_id FROM direct_sessions) AND e.task_id IS NULL)
        OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
    ),
    spawned_tasks AS (
     SELECT t.id
     FROM task t
     WHERE t.id <> tid
       AND (
         t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
         OR t.created_by_session_id IN (
          SELECT session_id FROM direct_sessions
          UNION SELECT session_id FROM own_execs WHERE session_id IS NOT NULL
          UNION SELECT session_id FROM del_tree WHERE session_id IS NOT NULL
         )
       )
    ),
    spawned_task_ids AS MATERIALIZED (SELECT id FROM spawned_tasks),
    spawned_sessions AS MATERIALIZED (
     SELECT d.id AS session_id
     FROM spawned_task_ids st
     JOIN document d ON d.kind = 'session' AND d.data->>'taskId' = st.id
    ),
    spawned_runs AS MATERIALIZED (
     SELECT e.id FROM spawned_task_ids st JOIN execution e ON e.task_id = st.id
     UNION
     SELECT e.id FROM spawned_sessions ss JOIN execution e ON e.task_id IS NULL AND e.session_id = ss.session_id
    ),
    spawned_execs AS (
     SELECT id AS exec_id FROM spawned_runs
     UNION
     SELECT e.id AS exec_id FROM spawned_runs sr JOIN execution e ON e.kind = 'tool_run' AND e.parent_execution_id = sr.id
    )
   SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks)
        + (SELECT count(DISTINCT d.id)::bigint
           FROM del_tree d
           JOIN execution e ON e.id = d.id
           LEFT JOIN execution_totals t ON t.execution_id = e.id
           WHERE d.id NOT IN (SELECT exec_id FROM spawned_execs)
             AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
             AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL))
   INTO cur_n;
   IF pass_n = 0 THEN
    vis_n := coalesce(cur_n, 0);
   ELSE
    all_n := coalesce(cur_n, 0);
   END IF;
  END LOOP;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN greatest(0::bigint, all_n - vis_n);
END $$;

CREATE OR REPLACE FUNCTION fleet_session_hidden_lineage_count(sid text)
RETURNS bigint
LANGUAGE plpgsql SECURITY DEFINER SET search_path = public AS $$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 vis_n bigint := 0;
 all_n bigint := 0;
 pass_n int;
 cur_n bigint;
BEGIN
 IF coalesce(sid, '') = '' OR NOT EXISTS (SELECT 1 FROM document WHERE kind = 'session' AND id = sid) THEN
  RETURN 0;
 END IF;
 BEGIN
  FOR pass_n IN 0..1 LOOP
   IF pass_n = 1 THEN
    PERFORM set_config('fleet.project_ids', '*', true);
   END IF;
   WITH RECURSIVE
    own_execs AS (
     SELECT e.id, e.session_id
     FROM execution e
     WHERE e.kind <> 'child_agent'
       AND e.delegation IS NULL
       AND (e.session_id = sid OR (e.kind = 'tool_run' AND e.parent_execution_id IN (SELECT id FROM execution WHERE session_id = sid AND kind <> 'child_agent' AND delegation IS NULL)))
    ),
    del_seed AS (
     SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
        OR (e.delegation IS NOT NULL AND (e.delegation->>'parentSessionId' = sid OR e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs)))
     UNION
     SELECT e.id, e.session_id, true AS follow_session
     FROM execution e
     JOIN document s ON s.kind = 'session' AND s.id = e.session_id
     WHERE e.session_id <> sid
       AND (
         s.data->>'parentExecutionId' IN (SELECT id FROM own_execs)
         OR (s.data->>'createdByExecutionId' IN (SELECT id FROM own_execs) AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
         OR (s.data->>'createdBySessionId' = sid AND coalesce(nullif(s.data->>'taskId', ''), '') = '')
       )
    ),
    del_tree AS (
     SELECT id, session_id, follow_session FROM del_seed
     UNION
     SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     JOIN del_tree dt ON e.parent_execution_id = dt.id
        OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id <> sid AND e.task_id IS NULL)
        OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
    ),
    primary_task AS (
     SELECT nullif(data->>'taskId', '') AS task_id FROM document WHERE kind = 'session' AND id = sid
    ),
    spawned_tasks AS (
     SELECT t.id
     FROM task t
     WHERE t.id IS DISTINCT FROM (SELECT task_id FROM primary_task)
       AND (
         t.created_by_session_id = sid
         OR t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
         OR t.created_by_session_id IN (SELECT session_id FROM del_tree WHERE session_id IS NOT NULL)
       )
    ),
    spawned_task_ids AS MATERIALIZED (SELECT id FROM spawned_tasks),
    spawned_sessions AS MATERIALIZED (
     SELECT d.id AS session_id
     FROM spawned_task_ids st
     JOIN document d ON d.kind = 'session' AND d.data->>'taskId' = st.id
    ),
    spawned_runs AS MATERIALIZED (
     SELECT e.id FROM spawned_task_ids st JOIN execution e ON e.task_id = st.id
     UNION
     SELECT e.id FROM spawned_sessions ss JOIN execution e ON e.task_id IS NULL AND e.session_id = ss.session_id
    ),
    spawned_execs AS (
     SELECT id AS exec_id FROM spawned_runs
     UNION
     SELECT e.id AS exec_id FROM spawned_runs sr JOIN execution e ON e.kind = 'tool_run' AND e.parent_execution_id = sr.id
    )
   SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks)
        + (SELECT count(DISTINCT d.id)::bigint
           FROM del_tree d
           JOIN execution e ON e.id = d.id
           LEFT JOIN execution_totals t ON t.execution_id = e.id
           WHERE d.id NOT IN (SELECT exec_id FROM spawned_execs)
             AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
             AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL))
   INTO cur_n;
   IF pass_n = 0 THEN
    vis_n := coalesce(cur_n, 0);
   ELSE
    all_n := coalesce(cur_n, 0);
   END IF;
  END LOOP;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN greatest(0::bigint, all_n - vis_n);
END $$;
-- migration 73: a pipeline script step ran in a session of its own with no actor, so the Conversations list showed each
-- one as an empty "New conversation" (project cs: 140 of them on 2026-11-16). startScript now marks the session
-- program-worker like every other host command session, and the list leaves those out. The earlier ones get the mark:
-- a pipeline session with no message, at least one host job and no other execution.
SELECT set_config('fleet.project_ids', '*', true), set_config('fleet.deleted', 'on', true);
UPDATE document d SET data = d.data || '{"actor": "program-worker"}'
 WHERE d.kind = 'session' AND d.data ? 'pipelineRunId' AND NOT d.data ? 'actor'
  AND NOT EXISTS (SELECT 1 FROM document m WHERE m.session_ref = d.id AND m.kind = 'message')
  AND EXISTS (SELECT 1 FROM execution e WHERE e.session_id = d.id AND e.data->>'actor' = 'program-worker')
  AND NOT EXISTS (SELECT 1 FROM execution e WHERE e.session_id = d.id AND coalesce(e.data->>'actor', '') <> 'program-worker');

-- migration 74: failing resource work pauses (resource_projection.go, resourcePause). On 2026-11-17 nine work rows
-- failed before their claim, so no attempt was recorded and every 100 ms pass failed them again (about 30k failed
-- resource calls per 5 minutes for 8 hours). Work rows already had attempts; a project build and a metadata repair now
-- have them too, and a build its own next attempt, so each failing item pauses exponentially up to the maximum while
-- the others keep their pace; resource-failing (rules.go) asks once about an item whose pause reached the maximum.
ALTER TABLE resource_build ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 ADD COLUMN next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp();
ALTER TABLE resource_metadata_repair ADD COLUMN attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0);
-- Work attempts counted under the old 1 s cap start the new ramp, so no row starts at the maximum pause or asks at once.
SELECT set_config('fleet.project_ids', '*', true);
UPDATE resource_work SET attempts = 0, next_attempt_at = least(next_attempt_at, clock_timestamp()) WHERE attempts > 0;

-- migration 75: one visibility function (docs/designs/fleet-standard.md S1). fleet_visible(project_id) is the project
-- scope test every project_scope policy repeated: the coordinator's '*' scope, or a project id in fleet.project_ids (a
-- row with no project is visible to the scope that lists ''). A plain STABLE SQL function, so the planner inlines it and
-- each policy plans as before. Every policy holding the copied expression is rewritten to call it, other terms kept;
-- the rewrite fails the migration when any copy is left.
CREATE OR REPLACE FUNCTION fleet_visible(p_project text) RETURNS boolean LANGUAGE sql STABLE PARALLEL SAFE AS $$
 SELECT current_setting('fleet.project_ids', true) = '*' OR coalesce(p_project, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ','))
$$;
DO $$
DECLARE
 copy constant text := $c$(current_setting('fleet.project_ids'::text, true) = '*'::text) OR (COALESCE(project_id, ''::text) = ANY (string_to_array(current_setting('fleet.project_ids'::text, true), ','::text)))$c$;
 p record;
BEGIN
 FOR p IN SELECT tablename, policyname, qual, with_check FROM pg_policies WHERE schemaname = 'public' AND (strpos(qual, copy) > 0 OR strpos(with_check, copy) > 0) LOOP
  IF p.qual IS NOT NULL THEN
   EXECUTE format('ALTER POLICY %I ON %I USING (%s)', p.policyname, p.tablename, replace(p.qual, copy, 'fleet_visible(project_id)'));
  END IF;
  IF p.with_check IS NOT NULL THEN
   EXECUTE format('ALTER POLICY %I ON %I WITH CHECK (%s)', p.policyname, p.tablename, replace(p.with_check, copy, 'fleet_visible(project_id)'));
  END IF;
 END LOOP;
 IF EXISTS (SELECT 1 FROM pg_policies WHERE schemaname = 'public' AND (qual ILIKE '%coalesce(project_id%' OR with_check ILIKE '%coalesce(project_id%')) THEN
  RAISE EXCEPTION 'migration 75: a policy still copies the project scope expression';
 END IF;
END $$;

-- migration 76: one owner per state (go/coordinator/DESIGN_RULES.md, S5). A build's ready was stored beside its phase and
-- written by every transition; it is now derived from the phase, which buildTransition (resource_states.go) alone writes.
ALTER TABLE resource_build DROP COLUMN ready;
ALTER TABLE resource_build ADD COLUMN ready boolean GENERATED ALWAYS AS (phase = 'ready') STORED;
-- The work kit's retry columns are attempts, next_attempt_at and last_error on every queue's
-- table (docs/designs/fleet-standard.md S6); its one wake channel fleet_work carries the name of the queue with new
-- work (work_kit.go). Pending intents of the old channel move over.
ALTER TABLE resource_work RENAME COLUMN last_error_class TO last_error;
ALTER TABLE wake_outbox DROP CONSTRAINT wake_outbox_channel_check;
SELECT set_config('fleet.project_ids', '*', true);
UPDATE wake_outbox SET channel = 'fleet_work', payload = 'resource-work' WHERE channel = 'fleet_resource_work';
ALTER TABLE wake_outbox ADD CONSTRAINT wake_outbox_channel_check CHECK (channel IN ('fleet_change','fleet_work','fleet_resource_summary'));
CREATE OR REPLACE FUNCTION fleet_enqueue_wake(ch text, body text) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true);
BEGIN
 IF ch IS NULL OR body IS NULL OR ch NOT IN ('fleet_change','fleet_work','fleet_resource_summary')
  OR octet_length(body)>=8000 OR (ch='fleet_change' AND body<>'') OR (ch='fleet_work' AND body !~ '^[a-z][a-z0-9-]*$') THEN
  RAISE EXCEPTION 'invalid wake channel or payload';
 END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 INSERT INTO wake_outbox(channel,payload) VALUES(ch,body);
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;
CREATE OR REPLACE FUNCTION resource_enqueue_source(eid text, old_project text, new_project text) RETURNS void
LANGUAGE plpgsql SET search_path=public AS $$
DECLARE wid uuid; pending timestamptz;
BEGIN
 INSERT INTO resource_work(execution_id) VALUES(eid)
 ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id
 RETURNING work_id,first_pending_at INTO wid,pending;
 INSERT INTO resource_work_scope(work_id,project_id,first_pending_at)
 SELECT wid,p,pending FROM (VALUES(old_project),(new_project)) v(p) WHERE p IS NOT NULL
 ON CONFLICT DO NOTHING;
 PERFORM fleet_enqueue_wake('fleet_work','resource-work');
END $$;
CREATE OR REPLACE FUNCTION resource_capture_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true); old_class jsonb:='{}'; new_class jsonb:='{}'; mid text;
BEGIN
 IF TG_TABLE_NAME <> 'document' OR TG_WHEN <> 'AFTER' THEN RAISE EXCEPTION 'invalid metadata capture context'; END IF;
 IF TG_OP <> 'INSERT' THEN old_class:=resource_metadata_class(OLD); mid:=OLD.id; END IF;
 IF TG_OP <> 'DELETE' THEN new_class:=resource_metadata_class(NEW); mid:=NEW.id; END IF;
 IF old_class IS NOT DISTINCT FROM new_class THEN RETURN NULL; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 INSERT INTO resource_metadata_repair(metadata_id,old_class,new_class) VALUES(mid,old_class,new_class)
 ON CONFLICT(source_xid,metadata_id) DO UPDATE SET new_class=excluded.new_class;
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
 PERFORM fleet_enqueue_wake('fleet_work','resource-repair');
 RETURN NULL;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;

-- migration 77: conversations out of the document table (docs/designs/fleet-standard.md S1, stage 4). Sessions,
-- messages, events (event-delta is an event with delta), queued messages, host commands, handoffs, session forks,
-- session transitions and worker-done reports become tables. Each record field a query filters, joins, orders or a state
-- machine owns is a column; payload holds the record's other fields (an event's data, a command's payload). seq keeps
-- the document sequence, so a thread's messages and events keep their order. The change feed keeps its kinds (session,
-- event, command); visibility is fleet_visible(project_id) with the same live and projectless-director policies.
SELECT set_config('fleet.project_ids', '*', true), set_config('fleet.deleted', 'on', true);
CREATE TYPE command_status AS ENUM ('pending', 'accepted', 'started', 'completed', 'failed', 'stopped', 'unknown');
CREATE TABLE session(
 id text PRIMARY KEY,
 code text UNIQUE,
 project_id text NULL,
 task_id text NULL,
 host_id text NULL,
 actor text NULL,
 directed_by text NULL,
 director_access_revoked_at timestamptz NULL,
 parent_execution_id text NULL,
 created_by_execution_id text NULL,
 created_by_session_id text NULL,
 source_session_id text NULL,
 pipeline_run_id text NULL,
 created_at timestamptz NULL,
 archived_at timestamptz NULL,
 title text NULL,
 title_by text NULL,
 title_call text NULL,
 title_attempt_at timestamptz NULL,
 title_attempts integer NOT NULL DEFAULT 0 CHECK (title_attempts >= 0),
 title_unknown_call text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX session_seq ON session(seq);
CREATE INDEX session_project ON session(project_id, seq);
CREATE INDEX session_task ON session(task_id) WHERE task_id IS NOT NULL;
CREATE INDEX session_created_by_session ON session(created_by_session_id) WHERE created_by_session_id IS NOT NULL;
CREATE INDEX session_parent_execution ON session(parent_execution_id) WHERE parent_execution_id IS NOT NULL;
CREATE INDEX session_created_by_execution ON session(created_by_execution_id) WHERE created_by_execution_id IS NOT NULL;
CREATE INDEX session_pipeline_run ON session(pipeline_run_id) WHERE pipeline_run_id IS NOT NULL;
CREATE INDEX session_host ON session(host_id) WHERE host_id IS NOT NULL;
CREATE INDEX session_untitled ON session(seq) WHERE title IS NULL AND archived_at IS NULL;
CREATE TABLE message(
 id text PRIMARY KEY,
 session_id text NOT NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 execution_id text NULL,
 project_id text NULL,
 request_id text NULL,
 text text NULL,
 sent_by text NULL,
 source text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX message_session ON message(session_id, seq);
CREATE INDEX message_execution ON message(execution_id, seq) WHERE execution_id IS NOT NULL;
CREATE INDEX message_activity ON message(session_id, updated_at);
CREATE INDEX message_request ON message(session_id, request_id);
CREATE TABLE event(
 id text PRIMARY KEY,
 delta boolean NOT NULL DEFAULT false,
 execution_id text NULL REFERENCES execution(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 session_id text NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 event_seq bigint NULL,
 type text NULL,
 at timestamptz NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX event_execution ON event(execution_id, seq);
CREATE INDEX event_sequence ON event(execution_id, event_seq);
CREATE INDEX event_session ON event(session_id, seq) WHERE session_id IS NOT NULL;
CREATE INDEX event_activity ON event(session_id, updated_at) WHERE NOT delta;
CREATE INDEX event_type ON event(execution_id, type) WHERE NOT delta;
CREATE INDEX event_limit_reading ON event(seq) WHERE type = 'limit_reading' AND NOT delta;
CREATE INDEX event_delta_block ON event(execution_id, (payload->'data'->'fleetNormalized'->>'deltaOf'), seq) WHERE delta;
CREATE TABLE queued_message(
 id text PRIMARY KEY,
 session_id text NOT NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 task_id text NULL,
 request_id text NULL,
 text text NULL,
 state text NULL,
 execution_id text NULL,
 steer_command_id text NULL,
 sent_by text NULL,
 source text NULL,
 created_at timestamptz NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX queued_message_session ON queued_message(session_id, seq);
CREATE INDEX queued_message_open ON queued_message(state, seq) WHERE state IN ('queued', 'steering');
CREATE INDEX queued_message_steer ON queued_message(steer_command_id) WHERE steer_command_id IS NOT NULL;
CREATE TABLE command(
 id text PRIMARY KEY,
 -- No foreign key to execution: a command may outlive its execution, and the poll stops it with that refusal.
 execution_id text NULL,
 session_id text NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 host_id text NULL,
 project_id text NULL,
 kind text NOT NULL,
 status command_status NOT NULL DEFAULT 'pending',
 stop_detail text NULL,
 handed_out boolean NULL,
 traceparent text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX command_pending ON command(host_id, seq) WHERE status = 'pending';
CREATE INDEX command_execution ON command(execution_id, kind) WHERE execution_id IS NOT NULL;
CREATE INDEX command_session ON command(session_id, kind) WHERE session_id IS NOT NULL;
CREATE INDEX command_host ON command(host_id) WHERE host_id IS NOT NULL;
CREATE TABLE handoff(
 id text PRIMARY KEY,
 session_id text NOT NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 state text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX handoff_session ON handoff(session_id, seq);
CREATE INDEX handoff_sent ON handoff(seq) WHERE state = 'sent';
CREATE TABLE session_fork(
 id text PRIMARY KEY,
 session_id text NOT NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 source_session_id text NOT NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 request_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX session_fork_source ON session_fork(source_session_id, request_id);
CREATE INDEX session_fork_session ON session_fork(session_id);
CREATE TABLE session_transition(
 id text PRIMARY KEY,
 session_id text NOT NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX session_transition_session ON session_transition(session_id, seq);
CREATE TABLE worker_done(
 id text PRIMARY KEY,
 execution_id text NULL REFERENCES execution(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX worker_done_execution ON worker_done(execution_id) WHERE execution_id IS NOT NULL;
CREATE FUNCTION pg_temp.str(v jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN jsonb_typeof(v) = 'string' THEN v #>> '{}' END $$;
CREATE FUNCTION pg_temp.ms(v jsonb) RETURNS timestamptz LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN jsonb_typeof(v) = 'number' THEN to_timestamp((v #>> '{}')::float8 / 1000) END $$;
-- rest: the fields of d without a column: a field named in cols moves to its column when its JSON type is the column's
-- ('' any type, null always), otherwise it stays.
CREATE FUNCTION pg_temp.rest(d jsonb, cols jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT coalesce(jsonb_object_agg(k, v), '{}') FROM jsonb_each(d) e(k, v)
 WHERE NOT (cols ? k AND (cols->>k = '' OR jsonb_typeof(v) IN (cols->>k, 'null')))
$$;
CREATE FUNCTION pg_temp.num(v jsonb) RETURNS bigint LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN jsonb_typeof(v) = 'number' THEN round((v #>> '{}')::numeric)::bigint END $$;
INSERT INTO session(id, code, project_id, task_id, host_id, actor, directed_by, director_access_revoked_at, parent_execution_id,
 created_by_execution_id, created_by_session_id, source_session_id, pipeline_run_id, created_at, archived_at, title, title_by,
 title_call, title_attempt_at, title_attempts, title_unknown_call, payload, seq, updated_at, tombstoned_at)
SELECT d.id, d.code, d.project_id, pg_temp.str(d.data->'taskId'), pg_temp.str(d.data->'hostId'), pg_temp.str(d.data->'actor'),
 pg_temp.str(d.data->'directedBy'), pg_temp.ms(d.data->'directorAccessRevokedAt'), pg_temp.str(d.data->'parentExecutionId'),
 pg_temp.str(d.data->'createdByExecutionId'), pg_temp.str(d.data->'createdBySessionId'), pg_temp.str(d.data->'sourceSessionId'),
 pg_temp.str(d.data->'pipelineRunId'), pg_temp.ms(d.data->'createdAt'), pg_temp.ms(d.data->'archivedAt'), pg_temp.str(d.data->'title'),
 pg_temp.str(d.data->'titleBy'), pg_temp.str(d.data->'titleCall'), pg_temp.ms(d.data->'titleAttemptAt'),
 greatest(coalesce(pg_temp.num(d.data->'titleAttempts'), 0), 0), pg_temp.str(d.data->'titleUnknownCall'),
 pg_temp.rest(d.data, '{"id":"","projectId":"","taskId":"string","hostId":"string","actor":"string","directedBy":"string","directorAccessRevokedAt":"number","parentExecutionId":"string","createdByExecutionId":"string","createdBySessionId":"string","sourceSessionId":"string","pipelineRunId":"string","createdAt":"number","archivedAt":"number","title":"string","titleBy":"string","titleCall":"string","titleAttemptAt":"number","titleAttempts":"number","titleUnknownCall":"string"}'),
 d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'session';
INSERT INTO message(id, session_id, execution_id, project_id, request_id, text, sent_by, source, payload, seq, updated_at, tombstoned_at)
SELECT d.id, d.session_ref, pg_temp.str(d.data->'executionId'), d.project_id, pg_temp.str(d.data->'requestId'), pg_temp.str(d.data->'text'),
 pg_temp.str(d.data->'sentBy'), pg_temp.str(d.data->'source'),
 pg_temp.rest(d.data, '{"id":"","sessionId":"","executionId":"string","projectId":"","requestId":"string","text":"string","sentBy":"string","source":"string"}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'message' AND EXISTS (SELECT 1 FROM session s WHERE s.id = d.session_ref);
INSERT INTO event(id, delta, execution_id, session_id, project_id, event_seq, type, at, payload, seq, updated_at, tombstoned_at)
SELECT d.id, d.kind = 'event-delta', (SELECT e.id FROM execution e WHERE e.id = d.execution_ref), (SELECT s.id FROM session s WHERE s.id = d.session_ref),
 d.project_id, pg_temp.num(d.data->'seq'), pg_temp.str(d.data->'type'), pg_temp.ms(d.data->'timestamp'),
 pg_temp.rest(d.data, '{"id":"","executionId":"","sessionId":"","projectId":"","seq":"number","type":"string","timestamp":"number"}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind IN ('event', 'event-delta');
INSERT INTO queued_message(id, session_id, project_id, task_id, request_id, text, state, execution_id, steer_command_id, sent_by, source, created_at, payload, seq, updated_at, tombstoned_at)
SELECT d.id, d.session_ref, d.project_id, pg_temp.str(d.data->'taskId'), pg_temp.str(d.data->'requestId'), pg_temp.str(d.data->'text'),
 pg_temp.str(d.data->'state'), pg_temp.str(d.data->'executionId'), pg_temp.str(d.data->'steerCommandId'), pg_temp.str(d.data->'sentBy'),
 pg_temp.str(d.data->'source'), pg_temp.ms(d.data->'createdAt'),
 pg_temp.rest(d.data, '{"id":"","sessionId":"","projectId":"","taskId":"string","requestId":"string","text":"string","state":"string","executionId":"string","steerCommandId":"string","sentBy":"string","source":"string","createdAt":"number"}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'queued-message' AND EXISTS (SELECT 1 FROM session s WHERE s.id = d.session_ref);
INSERT INTO command(id, execution_id, session_id, host_id, project_id, kind, status, stop_detail, handed_out, traceparent, payload, seq, updated_at, tombstoned_at)
SELECT d.id, (SELECT e.id FROM execution e WHERE e.id = d.execution_ref), (SELECT s.id FROM session s WHERE s.id = d.session_ref),
 pg_temp.str(d.data->'hostId'), d.project_id, coalesce(pg_temp.str(d.data->'kind'), ''),
 CASE WHEN d.data->>'status' IN ('pending', 'accepted', 'started', 'completed', 'failed', 'stopped', 'unknown') THEN (d.data->>'status')::command_status ELSE 'stopped' END,
 pg_temp.str(d.data->'stopDetail'), CASE WHEN jsonb_typeof(d.data->'handedOut') = 'boolean' THEN (d.data->>'handedOut')::boolean END,
 pg_temp.str(d.data->'traceparent'),
 pg_temp.rest(d.data, '{"id":"","executionId":"","sessionId":"","hostId":"string","projectId":"","kind":"string","status":"","stopDetail":"string","handedOut":"boolean","traceparent":"string"}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'command';
INSERT INTO handoff(id, session_id, project_id, state, payload, seq, updated_at, tombstoned_at)
SELECT d.id, d.session_ref, d.project_id, pg_temp.str(d.data->'state'), d.data - '{id,sessionId,projectId,state}'::text[], d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'handoff' AND EXISTS (SELECT 1 FROM session s WHERE s.id = d.session_ref);
INSERT INTO session_fork(id, session_id, source_session_id, project_id, request_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, d.session_ref, d.data->>'sourceSessionId', d.project_id, pg_temp.str(d.data->'requestId'),
 d.data - '{id,sessionId,sourceSessionId,projectId,requestId}'::text[], d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'session-fork' AND EXISTS (SELECT 1 FROM session s WHERE s.id = d.session_ref)
 AND EXISTS (SELECT 1 FROM session s WHERE s.id = d.data->>'sourceSessionId');
INSERT INTO session_transition(id, session_id, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, d.session_ref, d.project_id, d.data - '{id,sessionId,projectId}'::text[], d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'session-transition' AND EXISTS (SELECT 1 FROM session s WHERE s.id = d.session_ref);
INSERT INTO worker_done(id, execution_id, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, (SELECT e.id FROM execution e WHERE e.id = d.execution_ref), d.project_id, d.data - '{id,executionId,projectId}'::text[], d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'worker-done';
-- The copies' deferred foreign keys are checked now: a pending check would block the ALTER TABLEs below.
SET CONSTRAINTS ALL IMMEDIATE;
ALTER TABLE document DISABLE TRIGGER document_change;
DELETE FROM document WHERE kind IN ('session', 'message', 'event', 'event-delta', 'queued-message', 'command', 'handoff', 'session-fork', 'session-transition', 'worker-done');
ALTER TABLE document ENABLE TRIGGER document_change;
DROP INDEX document_limit_reading, document_event_sequence, document_delta_block;
DROP TRIGGER session_director_immutable ON document;
DROP TRIGGER document_zcode ON document;
ALTER TABLE document DROP COLUMN code;
ALTER TABLE document ADD CONSTRAINT document_moved_kind CHECK (kind NOT IN ('session', 'message', 'event', 'event-delta', 'queued-message', 'command', 'handoff', 'session-fork', 'session-transition', 'worker-done'));
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY ARRAY['session','message','event','queued_message','command','handoff','session_fork','session_transition','worker_done'] LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
  EXECUTE format('CREATE POLICY project_scope ON %I USING (fleet_visible(project_id))', t);
  EXECUTE format('CREATE POLICY live ON %I AS RESTRICTIVE FOR SELECT USING (fleet_show_deleted() OR (tombstoned_at IS NULL'
   ' AND (project_id IS NULL OR NOT project_id = ANY ((SELECT fleet_deleted_projects())::text[]))))', t);
 END LOOP;
END $$;
CREATE OR REPLACE FUNCTION fleet_own_projectless_session(sid text) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE prev text := current_setting('fleet.project_ids',true); who text := current_setting('fleet.person',true); allowed boolean;
BEGIN
 IF coalesce(who,'')='' OR sid IS NULL THEN RETURN false; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 SELECT EXISTS(SELECT 1 FROM session WHERE id=sid AND project_id IS NULL AND directed_by=who AND director_access_revoked_at IS NULL) INTO allowed;
 PERFORM set_config('fleet.project_ids',coalesce(prev,''),true); RETURN allowed;
END $$;
CREATE POLICY directed_session ON session USING (project_id IS NULL AND directed_by=current_setting('fleet.person',true) AND director_access_revoked_at IS NULL);
CREATE POLICY directed_session ON message USING (project_id IS NULL AND fleet_own_projectless_session(session_id));
CREATE POLICY directed_session ON event USING (project_id IS NULL AND fleet_own_projectless_session(session_id));
CREATE POLICY directed_session ON queued_message USING (project_id IS NULL AND fleet_own_projectless_session(session_id));
CREATE POLICY directed_session ON handoff USING (project_id IS NULL AND fleet_own_projectless_session(session_id));
CREATE POLICY directed_session ON session_transition USING (project_id IS NULL AND fleet_own_projectless_session(session_id));
CREATE POLICY directed_session ON session_fork USING (project_id IS NULL AND fleet_own_projectless_session(session_id) AND fleet_own_projectless_session(source_session_id));
DROP POLICY directed_session ON document;
CREATE OR REPLACE FUNCTION fleet_session_director_immutable() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF OLD.directed_by IS NOT NULL AND OLD.directed_by IS DISTINCT FROM NEW.directed_by THEN RAISE EXCEPTION 'session director is immutable'; END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER session_director_immutable BEFORE UPDATE ON session FOR EACH ROW EXECUTE FUNCTION fleet_session_director_immutable();
CREATE OR REPLACE FUNCTION fleet_entity_code() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE t text := CASE TG_TABLE_NAME WHEN 'task' THEN 'T' WHEN 'session' THEN 'C' ELSE 'Q' END; prev text;
BEGIN
 IF TG_OP = 'INSERT' THEN
  prev := current_setting('fleet.project_ids', true);
  PERFORM set_config('fleet.project_ids', '*', true);
  IF t = 'C' AND EXISTS(SELECT 1 FROM session WHERE id=NEW.id) THEN NEW.code := NULL; ELSE NEW.code := fleet_issue_code(t, NEW.project_id, NEW.id); END IF;
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 ELSIF NEW.code IS NOT NULL AND NEW.code IS DISTINCT FROM OLD.code THEN
  RAISE EXCEPTION 'codes are issued, never written (set code to NULL to reissue)';
 ELSIF NEW.project_id IS DISTINCT FROM OLD.project_id OR NEW.code IS NULL THEN
  IF NEW.project_id IS NOT DISTINCT FROM OLD.project_id THEN NEW.updated_at := OLD.updated_at; END IF; -- a reissue is not a change of the row
  NEW.code := fleet_issue_code(t, NEW.project_id, NEW.id);
  prev := current_setting('fleet.project_ids', true);
  PERFORM set_config('fleet.project_ids', '*', true);
  UPDATE entity_code SET superseded_by=NEW.code, project_id=nullif(NEW.project_id, '') WHERE entity_id=NEW.id AND type=t AND code<>NEW.code;
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 END IF;
 RETURN NEW;
END $$;
-- A session is written by upsert: a conflicting insert of an existing row issues nothing (it becomes an update).
CREATE TRIGGER session_zcode BEFORE INSERT OR UPDATE OF project_id, code ON session FOR EACH ROW EXECUTE FUNCTION fleet_entity_code();
CREATE OR REPLACE FUNCTION fleet_project_code_set(p_project text, p_code text) RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE r record; prev text := current_setting('fleet.project_ids', true);
BEGIN
 PERFORM pg_advisory_xact_lock(hashtext('fleet_project_code'));
 IF EXISTS(SELECT 1 FROM project_code WHERE code=p_code AND project_id=p_project AND current) THEN RETURN p_code; END IF;
 IF EXISTS(SELECT 1 FROM project_code WHERE code=p_code) THEN RAISE EXCEPTION 'project code % was used', p_code USING ERRCODE='23505'; END IF;
 UPDATE project_code SET current=false WHERE project_id=p_project AND current;
 INSERT INTO project_code(code, project_id) VALUES(p_code, p_project);
 PERFORM set_config('fleet.project_ids', '*', true); -- every row of the project, whatever the caller's scope (the op checked the project)
 FOR r IN SELECT id FROM task WHERE project_id=p_project ORDER BY created_at, id LOOP UPDATE task SET code=NULL WHERE id=r.id; END LOOP;
 FOR r IN SELECT id FROM question WHERE project_id=p_project ORDER BY asked_at, id LOOP UPDATE question SET code=NULL WHERE id=r.id; END LOOP;
 FOR r IN SELECT id FROM session WHERE project_id=p_project ORDER BY seq LOOP UPDATE session SET code=NULL WHERE id=r.id; END LOOP;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN p_code;
END $$;
-- A row a host's own call writes later for a tombstoned execution or session is born tombstoned (migration 67): the
-- first argument names the execution column, the second (when given) the session column.
CREATE OR REPLACE FUNCTION fleet_inherit_tombstone() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE e text; s text; BEGIN
 IF NEW.tombstoned_at IS NOT NULL OR NOT fleet_show_deleted() THEN RETURN NEW; END IF;
 IF TG_TABLE_NAME = 'document' THEN
  IF NEW.kind IN ('setting-change', 'transport-audit', 'purge-task') THEN RETURN NEW; END IF; -- audit outlives the object
  e := NEW.data->>'executionId'; s := NEW.data->>'sessionId';
 ELSE
  e := to_jsonb(NEW)->>TG_ARGV[0];
  IF TG_NARGS > 1 THEN s := to_jsonb(NEW)->>TG_ARGV[1]; END IF;
 END IF;
 IF e IS NULL AND s IS NOT NULL AND EXISTS (SELECT 1 FROM session WHERE id = s AND tombstoned_at IS NOT NULL) THEN
  NEW.tombstoned_at := now(); RETURN NEW;
 END IF;
 IF e IS NOT NULL AND EXISTS (SELECT 1 FROM execution WHERE id = e AND tombstoned_at IS NOT NULL) THEN NEW.tombstoned_at := now(); END IF;
 RETURN NEW;
END $$;
CREATE TRIGGER message_tombstone BEFORE INSERT ON message FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE TRIGGER event_tombstone BEFORE INSERT ON event FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE TRIGGER queued_message_tombstone BEFORE INSERT ON queued_message FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE TRIGGER command_tombstone BEFORE INSERT ON command FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE TRIGGER handoff_tombstone BEFORE INSERT ON handoff FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE TRIGGER session_fork_tombstone BEFORE INSERT ON session_fork FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE TRIGGER session_transition_tombstone BEFORE INSERT ON session_transition FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
CREATE TRIGGER worker_done_tombstone BEFORE INSERT ON worker_done FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
-- The change feed: the same kinds as from the document rows (a session; its messages and events as 'event'; a command
-- with its host and session).
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; ek text; st text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'setting-change' THEN st := nullif(r.data->>'target', ''); END IF; IF r.kind = 'host-observation' AND TG_OP = 'UPDATE' AND (OLD.data - 'lastSeenAt') = (NEW.data - 'lastSeenAt') THEN RETURN NULL; END IF; IF r.kind = 'host-observation' THEN k := 'host'; h := r.id; ELSIF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('managed-account', 'api-connection', 'connection-key', 'setting-change', 'project-recommendation') THEN k := r.kind; w := r.data->>'holder'; ELSIF r.kind IN ('delivery-state', 'delivery-pr', 'delivery-run', 'delivery-release', 'delivery-check') THEN k := 'delivery'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'session' THEN k := 'session'; i := r.id; s := r.id; ELSIF TG_TABLE_NAME IN ('message', 'event') THEN k := 'event'; i := r.id; s := r.session_id; ELSIF TG_TABLE_NAME = 'command' THEN k := 'command'; i := r.id; h := r.host_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN IF r.kind = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN SELECT e.kind::text, e.task_id, e.session_id, e.host_id INTO ek, t, s, h FROM execution e WHERE e.id = r.execution_id; IF ek = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder, setting_target) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w, st); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $$;
CREATE TRIGGER session_change AFTER INSERT OR UPDATE OR DELETE ON session FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER message_change AFTER INSERT OR UPDATE OR DELETE ON message FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER event_change AFTER INSERT OR UPDATE OR DELETE ON event FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER command_change AFTER INSERT OR UPDATE OR DELETE ON command FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE OR REPLACE FUNCTION fleet_session_hidden_lineage_count(sid text)
 RETURNS bigint
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 vis_n bigint := 0;
 all_n bigint := 0;
 pass_n int;
 cur_n bigint;
BEGIN
 IF coalesce(sid, '') = '' OR NOT EXISTS (SELECT 1 FROM session WHERE id = sid) THEN
  RETURN 0;
 END IF;
 BEGIN
  FOR pass_n IN 0..1 LOOP
   IF pass_n = 1 THEN
    PERFORM set_config('fleet.project_ids', '*', true);
   END IF;
   WITH RECURSIVE
    own_execs AS (
     SELECT e.id, e.session_id
     FROM execution e
     WHERE e.kind <> 'child_agent'
       AND e.delegation IS NULL
       AND (e.session_id = sid OR (e.kind = 'tool_run' AND e.parent_execution_id IN (SELECT id FROM execution WHERE session_id = sid AND kind <> 'child_agent' AND delegation IS NULL)))
    ),
    del_seed AS (
     SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
        OR (e.delegation IS NOT NULL AND (e.delegation->>'parentSessionId' = sid OR e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs)))
     UNION
     SELECT e.id, e.session_id, true AS follow_session
     FROM execution e
     JOIN session s ON s.id = e.session_id
     WHERE e.session_id <> sid
       AND (
         s.parent_execution_id IN (SELECT id FROM own_execs)
         OR (s.created_by_execution_id IN (SELECT id FROM own_execs) AND coalesce(nullif(s.task_id, ''), '') = '')
         OR (s.created_by_session_id = sid AND coalesce(nullif(s.task_id, ''), '') = '')
       )
    ),
    del_tree AS (
     SELECT id, session_id, follow_session FROM del_seed
     UNION
     SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     JOIN del_tree dt ON e.parent_execution_id = dt.id
        OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id <> sid AND e.task_id IS NULL)
        OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
    ),
    primary_task AS (
     SELECT nullif(task_id, '') AS task_id FROM session WHERE id = sid
    ),
    spawned_tasks AS (
     SELECT t.id
     FROM task t
     WHERE t.id IS DISTINCT FROM (SELECT task_id FROM primary_task)
       AND (
         t.created_by_session_id = sid
         OR t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
         OR t.created_by_session_id IN (SELECT session_id FROM del_tree WHERE session_id IS NOT NULL)
       )
    ),
    spawned_task_ids AS MATERIALIZED (SELECT id FROM spawned_tasks),
    spawned_sessions AS MATERIALIZED (
     SELECT d.id AS session_id
     FROM spawned_task_ids st
     JOIN session d ON d.task_id = st.id
    ),
    spawned_runs AS MATERIALIZED (
     SELECT e.id FROM spawned_task_ids st JOIN execution e ON e.task_id = st.id
     UNION
     SELECT e.id FROM spawned_sessions ss JOIN execution e ON e.task_id IS NULL AND e.session_id = ss.session_id
    ),
    spawned_execs AS (
     SELECT id AS exec_id FROM spawned_runs
     UNION
     SELECT e.id AS exec_id FROM spawned_runs sr JOIN execution e ON e.kind = 'tool_run' AND e.parent_execution_id = sr.id
    )
   SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks)
        + (SELECT count(DISTINCT d.id)::bigint
           FROM del_tree d
           JOIN execution e ON e.id = d.id
           LEFT JOIN execution_totals t ON t.execution_id = e.id
           WHERE d.id NOT IN (SELECT exec_id FROM spawned_execs)
             AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
             AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL))
   INTO cur_n;
   IF pass_n = 0 THEN
    vis_n := coalesce(cur_n, 0);
   ELSE
    all_n := coalesce(cur_n, 0);
   END IF;
  END LOOP;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN greatest(0::bigint, all_n - vis_n);
END $function$
;
CREATE OR REPLACE FUNCTION fleet_task_hidden_lineage_count(tid text)
 RETURNS bigint
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
DECLARE
 prev text := current_setting('fleet.project_ids', true);
 vis_n bigint := 0;
 all_n bigint := 0;
 pass_n int;
 cur_n bigint;
BEGIN
 IF coalesce(tid, '') = '' OR NOT EXISTS (SELECT 1 FROM task WHERE id = tid) THEN
  RETURN 0;
 END IF;
 BEGIN
  FOR pass_n IN 0..1 LOOP
   IF pass_n = 1 THEN
    PERFORM set_config('fleet.project_ids', '*', true);
   END IF;
   WITH RECURSIVE
    candidate_sessions AS (
     SELECT DISTINCT session_id FROM execution WHERE task_id = tid AND session_id IS NOT NULL
    ),
    first_task AS (
     SELECT DISTINCT ON (e.session_id) e.session_id, e.task_id
     FROM execution e
     WHERE e.task_id IS NOT NULL AND e.session_id IN (SELECT session_id FROM candidate_sessions)
     ORDER BY e.session_id, e.seq
    ),
    direct_sessions AS (
     SELECT id AS session_id FROM session
     WHERE (task_id = tid OR (coalesce(nullif(task_id, ''), '') = '' AND id IN (SELECT session_id FROM first_task WHERE task_id = tid)))
       AND parent_execution_id IS NULL
    ),
    own_execs AS (
     SELECT e.id, e.session_id
     FROM execution e
     WHERE e.kind <> 'child_agent'
       AND e.delegation IS NULL
       AND (
         e.task_id = tid
         OR (e.task_id IS NULL AND e.session_id IN (SELECT session_id FROM direct_sessions))
         OR (e.kind = 'tool_run' AND e.parent_execution_id IN (
            SELECT p.id FROM execution p
            WHERE p.kind <> 'child_agent'
              AND p.delegation IS NULL
              AND (p.task_id = tid OR (p.task_id IS NULL AND p.session_id IN (SELECT session_id FROM direct_sessions)))
         ))
       )
    ),
    del_seed AS (
     SELECT e.id, e.session_id, (e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     WHERE (e.parent_execution_id IN (SELECT id FROM own_execs) AND (e.kind = 'child_agent' OR e.delegation IS NOT NULL))
        OR (e.delegation IS NOT NULL AND (e.delegation->>'parentExecutionId' IN (SELECT id FROM own_execs) OR e.delegation->>'parentSessionId' IN (SELECT session_id FROM direct_sessions)))
     UNION
     SELECT e.id, e.session_id, true AS follow_session
     FROM execution e
     JOIN session s ON s.id = e.session_id
     WHERE s.parent_execution_id IN (SELECT id FROM own_execs)
        OR (s.created_by_execution_id IN (SELECT id FROM own_execs) AND coalesce(nullif(s.task_id, ''), '') = '')
        OR (s.created_by_session_id IN (SELECT session_id FROM direct_sessions) AND coalesce(nullif(s.task_id, ''), '') = '')
    ),
    del_tree AS (
     SELECT id, session_id, follow_session FROM del_seed
     UNION
     SELECT e.id, e.session_id, (dt.follow_session OR e.delegation IS NOT NULL) AS follow_session
     FROM execution e
     JOIN del_tree dt ON e.parent_execution_id = dt.id
        OR (dt.follow_session AND e.session_id IS NOT NULL AND dt.session_id IS NOT NULL AND e.session_id = dt.session_id AND e.session_id NOT IN (SELECT session_id FROM direct_sessions) AND e.task_id IS NULL)
        OR (e.delegation IS NOT NULL AND e.delegation->>'parentExecutionId' = dt.id)
    ),
    spawned_tasks AS (
     SELECT t.id
     FROM task t
     WHERE t.id <> tid
       AND (
         t.created_by_execution_id IN (SELECT id FROM own_execs UNION SELECT id FROM del_tree)
         OR t.created_by_session_id IN (
          SELECT session_id FROM direct_sessions
          UNION SELECT session_id FROM own_execs WHERE session_id IS NOT NULL
          UNION SELECT session_id FROM del_tree WHERE session_id IS NOT NULL
         )
       )
    ),
    spawned_task_ids AS MATERIALIZED (SELECT id FROM spawned_tasks),
    spawned_sessions AS MATERIALIZED (
     SELECT d.id AS session_id
     FROM spawned_task_ids st
     JOIN session d ON d.task_id = st.id
    ),
    spawned_runs AS MATERIALIZED (
     SELECT e.id FROM spawned_task_ids st JOIN execution e ON e.task_id = st.id
     UNION
     SELECT e.id FROM spawned_sessions ss JOIN execution e ON e.task_id IS NULL AND e.session_id = ss.session_id
    ),
    spawned_execs AS (
     SELECT id AS exec_id FROM spawned_runs
     UNION
     SELECT e.id AS exec_id FROM spawned_runs sr JOIN execution e ON e.kind = 'tool_run' AND e.parent_execution_id = sr.id
    )
   SELECT (SELECT count(DISTINCT id)::bigint FROM spawned_tasks)
        + (SELECT count(DISTINCT d.id)::bigint
           FROM del_tree d
           JOIN execution e ON e.id = d.id
           LEFT JOIN execution_totals t ON t.execution_id = e.id
           WHERE d.id NOT IN (SELECT exec_id FROM spawned_execs)
             AND e.kind NOT IN ('pipeline_run', 'pipeline_step')
             AND NOT (e.kind = 'child_agent' AND t.execution_id IS NULL))
   INTO cur_n;
   IF pass_n = 0 THEN
    vis_n := coalesce(cur_n, 0);
   ELSE
    all_n := coalesce(cur_n, 0);
   END IF;
  END LOOP;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
  RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 RETURN greatest(0::bigint, all_n - vis_n);
END $function$
;
CREATE OR REPLACE FUNCTION fleet_write_model(eid text)
 RETURNS void
 LANGUAGE sql
AS $function$
 UPDATE execution_totals SET model=(SELECT payload->'data'->>'model' FROM event WHERE NOT delta AND execution_id=eid
  AND jsonb_typeof(payload->'data'->'model')='string' ORDER BY seq DESC LIMIT 1) WHERE execution_id=eid
$function$
;
CREATE OR REPLACE FUNCTION fleet_write_native_observations(eid text)
 RETURNS void
 LANGUAGE sql
AS $function$
 WITH doc_evs AS MATERIALIZED (
  SELECT payload AS data, seq
  FROM event
  WHERE NOT delta AND execution_id=eid
 ),
 evs AS (
  SELECT data->'data'->'fleetNormalized'->>'lifecycle' AS lc, data->'data'->'fleetNormalized'->'nativeUsage' AS u, seq
  FROM doc_evs WHERE jsonb_typeof(data->'data'->'fleetNormalized'->'nativeUsage')='object'
 ),
 calls AS (SELECT u->'last' AS u FROM evs WHERE lc='usage.observation' AND jsonb_typeof(u->'last')='object'),
 rep AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE lc IN ('result','turn.completed') AND NOT EXISTS (SELECT 1 FROM calls) ORDER BY seq DESC LIMIT 1
 ),
 inc_native AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE coalesce(lc,'') NOT IN ('usage.observation','result','turn.completed')
   AND NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
 ),
 inc_assistant AS (
  SELECT DISTINCT ON (mid) mu AS u FROM (
   SELECT
    coalesce(d.data->'data'->'fleetNormalized'->'message'->>'id', d.data->'data'->'message'->>'id', d.seq::text) AS mid,
    coalesce(d.data->'data'->'fleetNormalized'->'message'->'usage', d.data->'data'->'message'->'usage') AS mu,
    d.seq
   FROM doc_evs d
   WHERE NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
    AND coalesce(d.data->'data'->'fleetNormalized'->>'lifecycle', d.data->'data'->>'type')='assistant'
    AND jsonb_typeof(d.data->'data'->'fleetNormalized'->'nativeUsage') IS DISTINCT FROM 'object'
  ) m WHERE jsonb_typeof(mu)='object' ORDER BY mid, seq DESC
 ),
 rows AS (
  SELECT fleet_token_count(coalesce(u->'reasoning_output_tokens',u->'reasoningOutputTokens',u->'thought_tokens',u->'thoughtTokens',u->'output_tokens_details'->'reasoning_tokens')) AS reasoning
  FROM (SELECT u FROM calls UNION ALL SELECT u FROM rep UNION ALL SELECT u FROM inc_native UNION ALL SELECT u FROM inc_assistant) x
 ), n AS (
  SELECT CASE WHEN count(*)=count(reasoning) THEN sum(reasoning) END AS reasoning FROM rows
 )
 UPDATE execution_totals SET tokens_reasoning=n.reasoning,
  tokens_reasoning_basis=CASE WHEN n.reasoning IS NULL THEN 'unavailable' ELSE 'measured' END
 FROM n WHERE execution_id=eid;
 WITH report AS (
  SELECT fleet_token_count(coalesce(payload#>'{data,fleetNormalized,native,duration_api_ms}',payload#>'{data,duration_api_ms}',payload#>'{data,native,duration_api_ms}')) AS ms
  FROM event WHERE NOT delta AND execution_id=eid
   AND coalesce(payload#>>'{data,fleetNormalized,lifecycle}',type)='result'
  ORDER BY seq DESC LIMIT 1
 ) UPDATE execution_totals SET api_ms=report.ms,
  api_ms_basis=CASE WHEN report.ms IS NULL THEN 'unavailable' ELSE 'measured' END
 FROM report WHERE execution_id=eid
$function$
;
CREATE OR REPLACE FUNCTION fleet_write_tokens(eid text)
 RETURNS void
 LANGUAGE sql
AS $function$
 WITH doc_evs AS MATERIALIZED (
  SELECT payload AS data, seq
  FROM event
  WHERE NOT delta AND execution_id=eid
 ),
 evs AS (
  SELECT data->'data'->'fleetNormalized'->>'lifecycle' AS lc, data->'data'->'fleetNormalized'->'nativeUsage' AS u, seq
  FROM doc_evs WHERE jsonb_typeof(data->'data'->'fleetNormalized'->'nativeUsage')='object'
 ),
 calls AS (SELECT u->'last' AS u FROM evs WHERE lc='usage.observation' AND jsonb_typeof(u->'last')='object'),
 rep AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE lc IN ('result','turn.completed') AND NOT EXISTS (SELECT 1 FROM calls) ORDER BY seq DESC LIMIT 1
 ),
 inc_native AS (
  SELECT CASE WHEN jsonb_typeof(u->'last')='object' THEN u->'last' ELSE u END AS u FROM evs
  WHERE coalesce(lc,'') NOT IN ('usage.observation','result','turn.completed')
   AND NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
 ),
 inc_assistant AS (
  SELECT DISTINCT ON (mid) mu AS u FROM (
   SELECT
    coalesce(d.data->'data'->'fleetNormalized'->'message'->>'id', d.data->'data'->'message'->>'id', d.seq::text) AS mid,
    coalesce(d.data->'data'->'fleetNormalized'->'message'->'usage', d.data->'data'->'message'->'usage') AS mu,
    d.seq
   FROM doc_evs d
   WHERE NOT EXISTS (SELECT 1 FROM calls) AND NOT EXISTS (SELECT 1 FROM rep)
    AND coalesce(d.data->'data'->'fleetNormalized'->>'lifecycle', d.data->'data'->>'type')='assistant'
    AND jsonb_typeof(d.data->'data'->'fleetNormalized'->'nativeUsage') IS DISTINCT FROM 'object'
  ) m WHERE jsonb_typeof(mu)='object' ORDER BY mid, seq DESC
 ),
 rows AS (
  SELECT
   fleet_token_count(coalesce(u->'input_tokens',u->'inputTokens')) AS input,
   fleet_token_count(coalesce(u->'output_tokens',u->'outputTokens')) AS output,
   fleet_token_count(coalesce(u->'cache_read_input_tokens',u->'cached_input_tokens',u->'cachedInputTokens',u->'cachedReadTokens',u->'cache_read_tokens')) AS cache_read,
   fleet_token_count(coalesce(u->'cache_creation_input_tokens',u->'cacheWriteInputTokens',u->'cachedWriteTokens',u->'cache_write_tokens')) AS cache_write,
   (u ? 'cached_input_tokens' OR u ? 'cachedInputTokens') AS input_has_cache
  FROM (SELECT u FROM calls UNION ALL SELECT u FROM rep UNION ALL SELECT u FROM inc_native UNION ALL SELECT u FROM inc_assistant) x
 ),
 n AS (
  SELECT
   sum(CASE WHEN input_has_cache AND input IS NOT NULL THEN greatest(0, input - coalesce(cache_read,0)) ELSE input END) AS input,
   sum(output) AS output, sum(cache_read) AS cache_read, sum(cache_write) AS cache_write
  FROM rows
 )
 UPDATE execution_totals SET
  tokens_input=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.input ELSE execution_totals.tokens_input END,
  tokens_input_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.input IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_input_basis END,
  tokens_output=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.output ELSE execution_totals.tokens_output END,
  tokens_output_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.output IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_output_basis END,
  tokens_cache_read=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_read ELSE execution_totals.tokens_cache_read END,
  tokens_cache_read_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.cache_read IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_cache_read_basis END,
  tokens_cache_write=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN n.cache_write ELSE execution_totals.tokens_cache_write END,
  tokens_cache_write_basis=CASE WHEN coalesce(n.input,n.output,n.cache_read,n.cache_write) IS NOT NULL THEN CASE WHEN n.cache_write IS NULL THEN 'unavailable' ELSE 'measured' END ELSE execution_totals.tokens_cache_write_basis END
 FROM n WHERE execution_id=eid
$function$
;

-- migration 78: hosts, accounts, connections and tokens out of the document table (docs/designs/fleet-standard.md S1,
-- stage 5). Hosts, host observations, watches, subscriptions (managed-account) with their inspections, checks and
-- catalogues, API connections, connection keys, run tokens and person settings become tables. A record field a query
-- filters, joins or orders, a policy reads (holder) or a state machine owns (host.state, connection_key.state) is a
-- column; payload holds the record's other fields; seq keeps the document sequence. The change feed keeps its kinds
-- (host, managed-account, api-connection, connection-key); the holder rule of the document policy moves to the tables
-- that had it. secret and account-action documents are no longer written; any left are deleted with the moved kinds.
SELECT set_config('fleet.project_ids', '*', true), set_config('fleet.deleted', 'on', true);
CREATE TABLE host(
 id text PRIMARY KEY,
 host_id text NOT NULL UNIQUE,
 holder text NULL,
 state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'removed')),
 intake text NULL,
 secret_hash text NULL,
 removed_secret_hash text NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE host_observation(
 id text PRIMARY KEY,
 holder text NULL,
 last_seen_at timestamptz NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE watch(
 id text PRIMARY KEY,
 session_id text NOT NULL REFERENCES session(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE managed_account(
 id text PRIMARY KEY,
 name text NULL,
 holder text NULL,
 intake text NULL,
 harness text NULL,
 provider text NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE account_inspection(
 id text PRIMARY KEY,
 account_id text NULL REFERENCES managed_account(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 execution_id text NULL,
 host_id text NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE account_check(
 id text PRIMARY KEY,
 account_id text NULL REFERENCES managed_account(id) ON DELETE CASCADE DEFERRABLE INITIALLY DEFERRED,
 host_id text NULL,
 binding text NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE account_catalogue(
 id text PRIMARY KEY,
 host_id text NULL,
 harness text NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE api_connection(
 id text PRIMARY KEY,
 name text NOT NULL UNIQUE,
 provider text NULL,
 holder text NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE connection_key(
 id text PRIMARY KEY,
 connection_id text NOT NULL REFERENCES api_connection(name) ON DELETE CASCADE ON UPDATE CASCADE DEFERRABLE INITIALLY DEFERRED,
 fingerprint text NULL,
 state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'disabled', 'retired')),
 added_at timestamptz NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE run_token(
 id text PRIMARY KEY,
 sha256 text NULL,
 execution_id text NULL,
 pipeline_run_id text NULL,
 personal text NULL,
 step boolean NULL,
 directed_by text NULL,
 created_at timestamptz NULL,
 revoked_at timestamptz NULL,
 director_access_revoked_at timestamptz NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE TABLE person_setting(
 id text PRIMARY KEY,
 email text NULL,
 project_id text NULL,
 payload jsonb NOT NULL DEFAULT '{}',
 seq bigint NOT NULL DEFAULT nextval('document_seq_seq'),
 updated_at timestamptz NOT NULL DEFAULT now(),
 tombstoned_at timestamptz NULL
);
CREATE INDEX host_secret ON host(secret_hash) WHERE secret_hash IS NOT NULL;
CREATE INDEX host_removed_secret ON host(removed_secret_hash) WHERE removed_secret_hash IS NOT NULL;
CREATE INDEX watch_session ON watch(session_id);
CREATE INDEX managed_account_name ON managed_account(name);
CREATE INDEX account_inspection_account ON account_inspection(account_id, seq);
CREATE INDEX account_inspection_execution ON account_inspection(execution_id) WHERE execution_id IS NOT NULL;
CREATE INDEX account_check_account ON account_check(account_id, host_id, binding);
CREATE INDEX account_catalogue_host ON account_catalogue(host_id, harness);
CREATE INDEX connection_key_connection ON connection_key(connection_id, seq);
CREATE INDEX run_token_execution ON run_token(execution_id) WHERE execution_id IS NOT NULL;
CREATE INDEX run_token_pipeline_run ON run_token(pipeline_run_id) WHERE pipeline_run_id IS NOT NULL;
CREATE INDEX run_token_open ON run_token(seq) WHERE revoked_at IS NULL;
CREATE INDEX run_token_personal ON run_token(personal) WHERE personal IS NOT NULL;
CREATE OR REPLACE FUNCTION pg_temp.str(v jsonb) RETURNS text LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN jsonb_typeof(v) = 'string' THEN v #>> '{}' END $$;
CREATE OR REPLACE FUNCTION pg_temp.ms(v jsonb) RETURNS timestamptz LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN jsonb_typeof(v) = 'number' THEN to_timestamp((v #>> '{}')::float8 / 1000) END $$;
CREATE OR REPLACE FUNCTION pg_temp.bool(v jsonb) RETURNS boolean LANGUAGE sql IMMUTABLE AS $$ SELECT CASE WHEN jsonb_typeof(v) = 'boolean' THEN (v #>> '{}')::boolean END $$;
CREATE OR REPLACE FUNCTION pg_temp.rest(d jsonb, cols jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT coalesce(jsonb_object_agg(k, v), '{}') FROM jsonb_each(d) e(k, v)
 WHERE NOT (cols ? k AND (cols->>k = '' OR jsonb_typeof(v) IN (cols->>k, 'null')))
$$;
INSERT INTO host(id, host_id, holder, state, intake, secret_hash, removed_secret_hash, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'hostId'), pg_temp.str(d.data->'holder'), CASE WHEN d.data->>'state' = 'removed' THEN 'removed' ELSE 'active' END, pg_temp.str(d.data->'intake'), pg_temp.str(d.data->'secretHash'), pg_temp.str(d.data->'removedSecretHash'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","hostId":"string","holder":"string","state":"string","intake":"string","secretHash":"string","removedSecretHash":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'host' AND jsonb_typeof(d.data->'hostId') = 'string';
INSERT INTO host_observation(id, holder, last_seen_at, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'holder'), pg_temp.ms(d.data->'lastSeenAt'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","holder":"string","lastSeenAt":"number","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'host-observation';
INSERT INTO watch(id, session_id, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'sessionId'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","sessionId":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'watch' AND EXISTS (SELECT 1 FROM session s WHERE s.id = d.data->>'sessionId');
INSERT INTO managed_account(id, name, holder, intake, harness, provider, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'name'), pg_temp.str(d.data->'holder'), pg_temp.str(d.data->'intake'), pg_temp.str(d.data->'harness'), pg_temp.str(d.data->'provider'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","name":"string","holder":"string","intake":"string","harness":"string","provider":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'managed-account';
INSERT INTO account_inspection(id, account_id, execution_id, host_id, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'accountId'), pg_temp.str(d.data->'executionId'), pg_temp.str(d.data->'hostId'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","accountId":"string","executionId":"string","hostId":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'account-inspection' AND (d.data->>'accountId' IS NULL OR EXISTS (SELECT 1 FROM document a WHERE a.kind = 'managed-account' AND a.id = d.data->>'accountId'));
INSERT INTO account_check(id, account_id, host_id, binding, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'accountId'), pg_temp.str(d.data->'hostId'), pg_temp.str(d.data->'binding'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","accountId":"string","hostId":"string","binding":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'account-check' AND (d.data->>'accountId' IS NULL OR EXISTS (SELECT 1 FROM document a WHERE a.kind = 'managed-account' AND a.id = d.data->>'accountId'));
INSERT INTO account_catalogue(id, host_id, harness, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'hostId'), pg_temp.str(d.data->'harness'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","hostId":"string","harness":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'account-catalogue';
INSERT INTO api_connection(id, name, provider, holder, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'name'), pg_temp.str(d.data->'provider'), pg_temp.str(d.data->'holder'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","name":"string","provider":"string","holder":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'api-connection' AND jsonb_typeof(d.data->'name') = 'string';
INSERT INTO connection_key(id, connection_id, fingerprint, state, added_at, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'connectionId'), pg_temp.str(d.data->'fingerprint'), CASE WHEN d.data->>'state' IN ('active', 'disabled', 'retired') THEN d.data->>'state' ELSE 'disabled' END, pg_temp.ms(d.data->'addedAt'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","connectionId":"string","fingerprint":"string","state":"string","addedAt":"number","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'connection-key' AND EXISTS (SELECT 1 FROM document a WHERE a.kind = 'api-connection' AND a.data->>'name' = d.data->>'connectionId');
INSERT INTO run_token(id, sha256, execution_id, pipeline_run_id, personal, step, directed_by, created_at, revoked_at, director_access_revoked_at, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'sha256'), pg_temp.str(d.data->'executionId'), pg_temp.str(d.data->'pipelineRunId'), pg_temp.str(d.data->'personal'), pg_temp.bool(d.data->'step'), pg_temp.str(d.data->'directedBy'), pg_temp.ms(d.data->'createdAt'), pg_temp.ms(d.data->'revokedAt'), pg_temp.ms(d.data->'directorAccessRevokedAt'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","sha256":"string","executionId":"string","pipelineRunId":"string","personal":"string","step":"boolean","directedBy":"string","createdAt":"number","revokedAt":"number","directorAccessRevokedAt":"number","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'run-token';
INSERT INTO person_setting(id, email, project_id, payload, seq, updated_at, tombstoned_at)
SELECT d.id, pg_temp.str(d.data->'email'), d.project_id,
 pg_temp.rest(d.data, '{"id":"","email":"string","projectId":""}'), d.seq, d.updated_at, d.tombstoned_at
FROM document d WHERE d.kind = 'person-setting';
-- The copies' deferred foreign keys are checked now: a pending check would block the ALTER TABLEs below.
SET CONSTRAINTS ALL IMMEDIATE;
DROP TRIGGER resource_capture_metadata_insert ON document;
DROP TRIGGER resource_capture_metadata_update ON document;
DROP TRIGGER resource_capture_metadata_delete ON document;
ALTER TABLE document DISABLE TRIGGER document_change;
DELETE FROM document WHERE kind IN ('host', 'host-observation', 'watch', 'managed-account', 'account-inspection', 'account-check', 'account-catalogue', 'account-action', 'api-connection', 'connection-key', 'run-token', 'secret', 'person-setting');
ALTER TABLE document ENABLE TRIGGER document_change;
ALTER TABLE document DROP CONSTRAINT document_moved_kind;
ALTER TABLE document ADD CONSTRAINT document_moved_kind CHECK (kind NOT IN ('session', 'message', 'event', 'event-delta', 'queued-message', 'command', 'handoff', 'session-fork', 'session-transition', 'worker-done', 'host', 'host-observation', 'watch', 'managed-account', 'account-inspection', 'account-check', 'account-catalogue', 'account-action', 'api-connection', 'connection-key', 'run-token', 'secret', 'person-setting'));
ALTER POLICY project_scope ON document USING (fleet_visible(project_id) OR (kind = 'setting-change' AND data->>'holder' = current_setting('fleet.person', true)));
DO $$ DECLARE t text; BEGIN
 FOREACH t IN ARRAY string_to_array('host,host_observation,watch,managed_account,account_inspection,account_check,account_catalogue,api_connection,connection_key,run_token,person_setting', ',') LOOP
  EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
  EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
  IF t IN ('host', 'host_observation', 'managed_account', 'api_connection') THEN
   EXECUTE format('CREATE POLICY project_scope ON %I USING (fleet_visible(project_id) OR holder = current_setting(''fleet.person'', true))', t);
  ELSE
   EXECUTE format('CREATE POLICY project_scope ON %I USING (fleet_visible(project_id))', t);
  END IF;
  EXECUTE format('CREATE POLICY live ON %I AS RESTRICTIVE FOR SELECT USING (fleet_show_deleted() OR (tombstoned_at IS NULL'
   ' AND (project_id IS NULL OR NOT project_id = ANY ((SELECT fleet_deleted_projects())::text[]))))', t);
 END LOOP;
END $$;
-- A row a host's own call writes later for a tombstoned execution or session is born tombstoned (migration 67).
CREATE TRIGGER run_token_tombstone BEFORE INSERT ON run_token FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
CREATE TRIGGER account_inspection_tombstone BEFORE INSERT ON account_inspection FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id');
CREATE TRIGGER watch_tombstone BEFORE INSERT ON watch FOR EACH ROW EXECUTE FUNCTION fleet_inherit_tombstone('execution_id', 'session_id');
-- The change feed: the same kinds as from the document rows (a host observation as 'host', but not when only its last
-- contact moved; a subscription and an API connection with their holder; a connection key).
CREATE OR REPLACE FUNCTION fleet_change() RETURNS trigger LANGUAGE plpgsql AS $$ DECLARE r record; k text; i text; t text; s text; h text; w text; ek text; st text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'document' THEN IF r.kind = 'setting-change' THEN st := nullif(r.data->>'target', ''); END IF; IF r.kind = 'artifact' THEN k := 'artifact'; s := r.data->>'sessionId'; SELECT e.task_id INTO t FROM execution e WHERE e.id = r.data->>'executionId'; ELSIF r.kind IN ('setting-change', 'project-recommendation') THEN k := r.kind; w := r.data->>'holder'; ELSIF r.kind IN ('delivery-state', 'delivery-pr', 'delivery-run', 'delivery-release', 'delivery-check') THEN k := 'delivery'; ELSE RETURN NULL; END IF; i := r.id; ELSIF TG_TABLE_NAME = 'host_observation' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - '{last_seen_at,updated_at}'::text[]) = (to_jsonb(NEW) - '{last_seen_at,updated_at}'::text[]) THEN RETURN NULL; END IF; k := 'host'; i := r.id; h := r.id; ELSIF TG_TABLE_NAME IN ('managed_account', 'api_connection') THEN k := replace(TG_TABLE_NAME, '_', '-'); i := r.id; w := r.holder; ELSIF TG_TABLE_NAME = 'connection_key' THEN k := 'connection-key'; i := r.id; ELSIF TG_TABLE_NAME = 'session' THEN k := 'session'; i := r.id; s := r.id; ELSIF TG_TABLE_NAME IN ('message', 'event') THEN k := 'event'; i := r.id; s := r.session_id; ELSIF TG_TABLE_NAME = 'command' THEN k := 'command'; i := r.id; h := r.host_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN IF r.kind = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN SELECT e.kind::text, e.task_id, e.session_id, e.host_id INTO ek, t, s, h FROM execution e WHERE e.id = r.execution_id; IF ek = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder, setting_target) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w, st); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $$;
CREATE TRIGGER host_observation_change AFTER INSERT OR UPDATE OR DELETE ON host_observation FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER managed_account_change AFTER INSERT OR UPDATE OR DELETE ON managed_account FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER api_connection_change AFTER INSERT OR UPDATE OR DELETE ON api_connection FOR EACH ROW EXECUTE FUNCTION fleet_change();
CREATE TRIGGER connection_key_change AFTER INSERT OR UPDATE OR DELETE ON connection_key FOR EACH ROW EXECUTE FUNCTION fleet_change();
-- Resource metadata (migration 55): a subscription's or an API connection's class, captured from its own table.
DROP FUNCTION resource_metadata_class(document);
CREATE FUNCTION resource_metadata_class(p_kind text, p_id text, p_provider jsonb) RETURNS jsonb LANGUAGE sql IMMUTABLE AS $$
 SELECT jsonb_build_object('accountId', p_id, 'connectionName', CASE WHEN p_kind = 'api-connection' THEN substr(p_id, 12) END,
  'kind', p_kind, 'provider', p_provider)
$$;
CREATE OR REPLACE FUNCTION resource_capture_metadata() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER SET search_path=public AS $$
DECLARE old_scope text := current_setting('fleet.project_ids',true); old_class jsonb:='{}'; new_class jsonb:='{}'; mid text;
 k text := replace(TG_TABLE_NAME, '_', '-');
BEGIN
 IF TG_TABLE_NAME NOT IN ('api_connection', 'managed_account') OR TG_WHEN <> 'AFTER' THEN RAISE EXCEPTION 'invalid metadata capture context'; END IF;
 IF TG_OP <> 'INSERT' THEN old_class:=resource_metadata_class(k, OLD.id, coalesce(to_jsonb(OLD.provider), OLD.payload->'provider')); mid:=OLD.id; END IF;
 IF TG_OP <> 'DELETE' THEN new_class:=resource_metadata_class(k, NEW.id, coalesce(to_jsonb(NEW.provider), NEW.payload->'provider')); mid:=NEW.id; END IF;
 IF old_class IS NOT DISTINCT FROM new_class THEN RETURN NULL; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 INSERT INTO resource_metadata_repair(metadata_id,old_class,new_class) VALUES(mid,old_class,new_class)
 ON CONFLICT(source_xid,metadata_id) DO UPDATE SET new_class=excluded.new_class;
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true);
 PERFORM fleet_enqueue_wake('fleet_work','resource-repair');
 RETURN NULL;
EXCEPTION WHEN OTHERS THEN
 PERFORM set_config('fleet.project_ids',coalesce(old_scope,''),true); RAISE;
END $$;
CREATE TRIGGER api_connection_capture_metadata AFTER INSERT OR UPDATE OR DELETE ON api_connection FOR EACH ROW EXECUTE FUNCTION resource_capture_metadata();
CREATE TRIGGER managed_account_capture_metadata AFTER INSERT OR UPDATE OR DELETE ON managed_account FOR EACH ROW EXECUTE FUNCTION resource_capture_metadata();
CREATE OR REPLACE FUNCTION fleet_exec_conn_meta(conn_name text, aid text) RETURNS TABLE(provider text, is_api_conn boolean)
 LANGUAGE plpgsql SECURITY DEFINER SET search_path TO 'public' AS $function$
DECLARE prev text := current_setting('fleet.project_ids', true); v_provider text; v_is_api boolean := false;
BEGIN
 IF coalesce(conn_name, '') = '' AND coalesce(aid, '') = '' THEN
  provider := NULL; is_api_conn := false; RETURN NEXT; RETURN;
 END IF;
 PERFORM set_config('fleet.project_ids', '*', true);
 BEGIN
  SELECT coalesce(
   CASE WHEN coalesce(conn_name, '') <> '' THEN (SELECT coalesce(c.provider, c.payload->>'provider') FROM api_connection c WHERE c.id = 'connection:' || conn_name) END,
   CASE WHEN coalesce(aid, '') <> '' THEN (SELECT coalesce(c.provider, c.payload->>'provider') FROM api_connection c WHERE c.id = aid) END
  ) INTO v_provider;
  IF coalesce(aid, '') <> '' THEN
   SELECT EXISTS(SELECT 1 FROM api_connection c WHERE c.id = aid) INTO v_is_api;
  END IF;
 EXCEPTION WHEN OTHERS THEN
  PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true); RAISE;
 END;
 PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true);
 provider := v_provider; is_api_conn := coalesce(v_is_api, false); RETURN NEXT;
END $function$;
-- migration 79: pipelines, Lab and delegation sources on the work kit (docs/designs/fleet-standard.md S6, stage 6). A
-- running pipeline run, an experiment to look at and a delegation whose website sources are read each have one row
-- with the kit's retry columns; producers insert or wake it in their own transaction, and the kit's queue deletes it
-- when there is nothing more to do. The runs and experiments a start used to sweep (running runs, succeeded draft-examples
-- runs to import, running, paused and stopped experiments) get their rows here.
CREATE TABLE pipeline_work (
 run_id text PRIMARY KEY REFERENCES execution(id) ON DELETE CASCADE,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_error text NULL
);
CREATE INDEX pipeline_work_due ON pipeline_work(next_attempt_at);
CREATE TABLE lab_work (
 experiment_id text PRIMARY KEY REFERENCES experiment(id) ON DELETE CASCADE,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_error text NULL
);
CREATE INDEX lab_work_due ON lab_work(next_attempt_at);
-- delegation_id names a delegation record of the document table (stage 8 makes it a table and this a foreign key);
-- payload is the source files to read, [{artifactId, path}].
CREATE TABLE delegation_source_work (
 delegation_id text PRIMARY KEY,
 payload jsonb NOT NULL,
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
 next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 last_error text NULL
);
CREATE INDEX delegation_source_work_due ON delegation_source_work(next_attempt_at);
SELECT set_config('fleet.project_ids', '*', true);
INSERT INTO pipeline_work(run_id) SELECT id FROM execution WHERE kind='pipeline_run' AND data ? 'pipelineId'
 AND (state='running' OR state='succeeded' AND data->>'pipelineId'='draft-examples'); -- a start imported finished drafts again
INSERT INTO lab_work(experiment_id) SELECT id FROM experiment WHERE state IN ('running','paused','stopped');
-- migration 80: the task's data mirror goes (docs/designs/fleet-standard.md S1, stage 3b). Every task field has its own
-- column; data was written as '{}' and read by nothing.
ALTER TABLE task DROP COLUMN data;
-- The task's latest result comes only from the artifact table: results kept as documents of kind 'artifact' (before
-- the artifact table) are no longer read anywhere (task_get, the entity card).
CREATE OR REPLACE VIEW task_view WITH (security_invoker = true) AS
 SELECT t.id AS task_id, t.project_id, t.state, t.wait_reason,
  coalesce((SELECT jsonb_object_agg(s.state, s.n) FROM (SELECT e.state, count(*) AS n FROM execution e WHERE e.task_id = t.id GROUP BY e.state) s), '{}') AS executions_by_state,
  (SELECT count(*) FROM execution e WHERE e.task_id = t.id) AS execution_count,
  (SELECT jsonb_build_object('tokens_input', sum(x.tokens_input), 'tokens_output', sum(x.tokens_output), 'tokens_cache_read', sum(x.tokens_cache_read), 'tokens_cache_write', sum(x.tokens_cache_write), 'wall_ms', sum(x.wall_ms), 'active_ms', sum(x.active_ms), 'cpu_ms', sum(x.cpu_ms), 'turn_count', sum(x.turn_count), 'peak_memory_bytes', max(x.peak_memory_bytes), 'measured_wall', count(*) FILTER (WHERE x.wall_ms_basis = 'measured'), 'measured_cpu', count(*) FILTER (WHERE x.cpu_ms_basis = 'measured'), 'executions_with_totals', count(*))
   FROM execution_totals x JOIN execution e ON e.id = x.execution_id WHERE e.task_id = t.id AND e.kind NOT IN ('pipeline_run', 'pipeline_step', 'tool_run')) AS totals,
  (WITH publications AS (
    SELECT 'artifact:'||a.id AS row_id, a.id, a.legacy_id, a.project_id, a.scope, a.content_hash, a.previous_artifact_id,
      a.seq, e.seq AS execution_seq, a.required, a.state='stored' AS stored, a.name NOT LIKE 'mcp-lifecycle-%' AS deliverable,
      jsonb_build_object('id', coalesce(a.legacy_id,a.id), 'artifactId',a.id, 'executionId',a.execution_id, 'path',a.name, 'mediaType',a.media_type, 'size',a.size, 'sha256',a.content_hash) AS v
    FROM artifact a JOIN execution e ON e.id=a.execution_id WHERE a.state IN ('stored','deleted') AND e.task_id=t.id
   ), predecessors AS (
    SELECT a.id,a.legacy_id,a.project_id,a.scope,a.seq FROM artifact a JOIN execution e ON e.id=a.execution_id WHERE e.task_id=t.id
   ), identities AS (
    SELECT c.*, CASE WHEN nullif(project_id,'') IS NOT NULL AND scope IN ('prod','lab') AND content_hash ~ '^[0-9a-f]{64}$' THEN NULL ELSE row_id END AS unknown_identity,
      CASE WHEN nullif(c.previous_artifact_id,'') IS NULL THEN false ELSE EXISTS (SELECT 1 FROM predecessors p WHERE (p.id=c.previous_artifact_id OR p.legacy_id=c.previous_artifact_id)
        AND p.seq<c.seq AND p.project_id=c.project_id AND p.scope=c.scope) END AS explicit_version
    FROM publications c
   ), anchors AS (
    SELECT i.*, max(seq) FILTER (WHERE explicit_version) OVER (
      PARTITION BY project_id,scope,content_hash,unknown_identity ORDER BY seq ROWS BETWEEN UNBOUNDED PRECEDING AND CURRENT ROW) AS anchor
    FROM identities i
   ), versions AS (
    SELECT a.*, CASE WHEN anchor IS NULL THEN min(execution_seq) OVER w
      ELSE max(execution_seq) FILTER (WHERE seq=anchor) OVER w END AS version_execution_seq
    FROM anchors a WINDOW w AS (PARTITION BY project_id,scope,content_hash,unknown_identity,anchor)
   ), positions AS (
    SELECT v.*, coalesce(anchor,min(seq) FILTER (WHERE execution_seq=version_execution_seq) OVER (
      PARTITION BY project_id,scope,content_hash,unknown_identity,anchor)) AS version_seq
    FROM versions v
   ) SELECT v FROM positions WHERE stored
   ORDER BY deliverable DESC,version_execution_seq DESC,version_seq DESC,required DESC,seq DESC LIMIT 1) AS latest_result,
  (SELECT coalesce(jsonb_agg(jsonb_build_object('id', q.id, 'kind', q.kind, 'text', q.text, 'options', q.options) ORDER BY q.seq), '[]') FROM question q WHERE q.task_id = t.id AND q.state = 'open') AS open_questions
 FROM task t;
-- migration 81: a pipeline run's plan out of its execution document (docs/designs/fleet-standard.md S1, stage 3c).
-- pipeline_run holds the plan head of a run (the definition hash it plans and its write revision, guardPipelineWrite);
-- pipeline_step one entry per definition step (activation, condition decision, execution reference, dispatch, frozen
-- inputs, problem), keyed by the step's index. getRun assembles the run's plan from them. execution.data keeps the rest
-- of the run document: the run fields other areas read there or through getRun until their own stages (pipelineId:
-- runRows in projects.go, queries/lab.sql; projectId, requestId, pipeline, rerunOf, definition, steps' outputs: lab.go,
-- lab_assessment.go, host_poll.go, tasks.go) and planVersion, the format marker a run without its plan is refused by.
-- decision is a contract.PipelineConditionDecision and inputs a list of contract.PipelineInputBinding, each one value.
-- A fresh selected entry's step record (its execution row <run>:step:<i>) keeps there its state, observation, times,
-- index and traceparent (telemetry.go reads it) and the open remainder of its record; what only the pipeline code reads
-- is the entry's: the native execution, resolved arguments, output and its artifact, job id and polls, usage, error,
-- problem and the step's millisecond times (a JSON value column holds JSON null when the record had the key as null).
CREATE TABLE pipeline_run (
 run_id text PRIMARY KEY REFERENCES execution(id) ON DELETE CASCADE,
 definition_sha256 text NOT NULL,
 revision bigint NOT NULL CHECK (revision >= 0)
);
CREATE TABLE pipeline_step (
 run_id text NOT NULL REFERENCES pipeline_run(run_id) ON DELETE CASCADE,
 step_index integer NOT NULL CHECK (step_index >= 0),
 step_id text NOT NULL,
 definition_sha256 text NOT NULL,
 activation text NOT NULL CHECK (activation IN ('pending','selected','not_selected','blocked')),
 decision jsonb NULL,
 ref_mode text NULL CHECK (ref_mode IN ('fresh','reused')),
 ref_execution_id text NULL,
 ref_source_run_id text NULL,
 ref_source_step_id text NULL,
 ref_source_step_index integer NULL,
 ref_source_definition_sha256 text NULL,
 ref_source_input_sha256 text NULL,
 dispatch_phase text NULL CHECK (dispatch_phase IN ('reserved','prepared','handed_off','settled')),
 dispatch_claim_id text NULL,
 dispatch_owner_boot_id text NULL,
 dispatch_handed_off_at_ms bigint NULL,
 dispatch_authority_sha256 text NULL,
 dispatch_native_execution_id text NULL,
 dispatch_command_id text NULL,
 dispatch_host_id text NULL,
 dispatch_settled_at_ms bigint NULL,
 inputs jsonb NULL CHECK (inputs IS NULL OR jsonb_typeof(inputs) = 'array'),
 problem_code text NULL,
 problem_message text NULL,
 native_execution_id text NULL,
 arguments jsonb NULL,
 output jsonb NULL,
 output_artifact_id text NULL,
 job_id text NULL,
 polls bigint NULL,
 usage jsonb NULL,
 error jsonb NULL,
 step_problem text NULL,
 started_at_ms bigint NULL,
 finished_at_ms bigint NULL,
 duration_ms bigint NULL,
 PRIMARY KEY (run_id, step_index)
);
SELECT set_config('fleet.project_ids', '*', true);
INSERT INTO pipeline_run(run_id,definition_sha256,revision)
 SELECT id,coalesce(data->'plan'->>'definitionSha256',''),
  CASE WHEN jsonb_typeof(data->'plan'->'revision')='number' THEN greatest((data->'plan'->>'revision')::numeric,0)::bigint ELSE 0 END
 FROM execution WHERE kind='pipeline_run' AND jsonb_typeof(data->'plan'->'entries')='array';
INSERT INTO pipeline_step(run_id,step_index,step_id,definition_sha256,activation,decision,ref_mode,ref_execution_id,ref_source_run_id,
 ref_source_step_id,ref_source_step_index,ref_source_definition_sha256,ref_source_input_sha256,dispatch_phase,dispatch_claim_id,
 dispatch_owner_boot_id,dispatch_handed_off_at_ms,dispatch_authority_sha256,dispatch_native_execution_id,dispatch_command_id,dispatch_host_id,
 dispatch_settled_at_ms,inputs,problem_code,problem_message)
 SELECT e.id,(x.ord-1)::integer,coalesce(x.v->>'id',''),coalesce(x.v->>'definitionSha256',''),coalesce(x.v->>'activation','pending'),
  CASE WHEN jsonb_typeof(x.v->'decision')='object' THEN x.v->'decision' END,
  x.v->'executionRef'->>'mode',x.v->'executionRef'->>'executionId',x.v->'executionRef'->>'sourceRunId',x.v->'executionRef'->>'sourceStepId',
  (x.v->'executionRef'->>'sourceStepIndex')::numeric::integer,x.v->'executionRef'->>'sourceDefinitionSha256',x.v->'executionRef'->>'sourceInputSha256',
  x.v->'dispatch'->>'phase',x.v->'dispatch'->>'claimId',x.v->'dispatch'->>'ownerBootId',(x.v->'dispatch'->>'handedOffAtMs')::numeric::bigint,
  x.v->'dispatch'->>'authoritySha256',x.v->'dispatch'->>'nativeExecutionId',x.v->'dispatch'->>'commandId',x.v->'dispatch'->>'hostId',
  (x.v->'dispatch'->>'settledAtMs')::numeric::bigint,
  CASE WHEN jsonb_typeof(x.v->'inputs')='array' THEN x.v->'inputs' END,x.v->'problem'->>'code',x.v->'problem'->>'message'
 FROM execution e CROSS JOIN LATERAL jsonb_array_elements(e.data->'plan'->'entries') WITH ORDINALITY AS x(v,ord)
 WHERE e.kind='pipeline_run' AND jsonb_typeof(e.data->'plan'->'entries')='array';
UPDATE execution SET data=data-'plan' WHERE id IN (SELECT run_id FROM pipeline_run);
UPDATE pipeline_step p SET
 native_execution_id=CASE WHEN jsonb_typeof(e.data->'executionId')='string' THEN e.data->>'executionId' END,
 arguments=e.data->'arguments',output=e.data->'output',usage=e.data->'usage',error=e.data->'error',
 output_artifact_id=CASE WHEN jsonb_typeof(e.data->'outputArtifactId')='string' THEN e.data->>'outputArtifactId' END,
 job_id=CASE WHEN jsonb_typeof(e.data->'jobId')='string' THEN e.data->>'jobId' END,
 polls=CASE WHEN jsonb_typeof(e.data->'polls')='number' AND (e.data->>'polls') ~ '^[0-9]+$' THEN (e.data->>'polls')::bigint END,
 step_problem=CASE WHEN jsonb_typeof(e.data->'problem')='string' THEN e.data->>'problem' END,
 started_at_ms=CASE WHEN jsonb_typeof(e.data->'startedAt')='number' AND (e.data->>'startedAt') ~ '^-?[0-9]+$' THEN (e.data->>'startedAt')::bigint END,
 finished_at_ms=CASE WHEN jsonb_typeof(e.data->'finishedAt')='number' AND (e.data->>'finishedAt') ~ '^-?[0-9]+$' THEN (e.data->>'finishedAt')::bigint END,
 duration_ms=CASE WHEN jsonb_typeof(e.data->'durationMs')='number' AND (e.data->>'durationMs') ~ '^-?[0-9]+$' THEN (e.data->>'durationMs')::bigint END
FROM execution e WHERE e.kind='pipeline_step' AND p.run_id=e.parent_execution_id AND p.ref_mode='fresh' AND p.ref_execution_id=e.id;
UPDATE execution e SET data=e.data-ARRAY['arguments','output','usage','error']
 -(CASE WHEN p.native_execution_id IS NOT NULL THEN 'executionId' ELSE '' END)
 -(CASE WHEN p.output_artifact_id IS NOT NULL THEN 'outputArtifactId' ELSE '' END)
 -(CASE WHEN p.job_id IS NOT NULL THEN 'jobId' ELSE '' END)
 -(CASE WHEN p.polls IS NOT NULL THEN 'polls' ELSE '' END)
 -(CASE WHEN p.step_problem IS NOT NULL THEN 'problem' ELSE '' END)
 -(CASE WHEN p.started_at_ms IS NOT NULL THEN 'startedAt' ELSE '' END)
 -(CASE WHEN p.finished_at_ms IS NOT NULL THEN 'finishedAt' ELSE '' END)
 -(CASE WHEN p.duration_ms IS NOT NULL THEN 'durationMs' ELSE '' END)
FROM pipeline_step p WHERE e.kind='pipeline_step' AND p.run_id=e.parent_execution_id AND p.ref_mode='fresh' AND p.ref_execution_id=e.id;
