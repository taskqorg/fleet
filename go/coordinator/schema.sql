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
