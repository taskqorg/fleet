-- Recount existing task attempts from agent turns. Tool and child runs remain in execution history.
WITH counts AS (
  SELECT task_id, count(*)::integer AS attempts
  FROM execution WHERE task_id IS NOT NULL AND kind = 'agent_run' AND tombstoned_at IS NULL
  GROUP BY task_id
), wanted AS (
  SELECT d.task_id, coalesce(c.attempts, 0) AS attempts
  FROM task_derived d LEFT JOIN counts c USING (task_id)
)
UPDATE task_derived d SET executions = w.attempts
FROM wanted w WHERE w.task_id = d.task_id AND d.executions IS DISTINCT FROM w.attempts;

CREATE OR REPLACE VIEW task_view WITH (security_invoker = true) AS
 SELECT t.id AS task_id, t.project_id, t.state, t.wait_reason,
  coalesce((SELECT jsonb_object_agg(s.state, s.n) FROM (SELECT e.state, count(*) AS n FROM execution e WHERE e.task_id = t.id AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL GROUP BY e.state) s), '{}') AS executions_by_state,
  (SELECT count(*) FROM execution e WHERE e.task_id = t.id AND e.kind = 'agent_run' AND e.tombstoned_at IS NULL) AS execution_count,
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
