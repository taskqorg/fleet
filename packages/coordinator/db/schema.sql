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

-- migration 8: artifact store (design §3, Phase 3 item 21). Bytes live in the per-host S3 stores; rows hold metadata,
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
CREATE POLICY project_scope ON artifact_rule_confirmation USING (current_setting('fleet.project_ids', true) = '*' OR coalesce(project_id, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ',')))
