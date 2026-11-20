-- Current definitions (docs/designs/fleet-standard.md S1), applied after the migrations by applySchemaFiles
-- (store.go) whenever the file changes. Edit here, never in a migration; TestMigrationSchemaFiles keeps the file in
-- the database's deparsed form (FLEET_UPDATE_SCHEMA_FILES=1 go test ./coordinator -run TestMigrationSchemaFiles).

CREATE OR REPLACE FUNCTION public.fleet_append_only()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ BEGIN RAISE EXCEPTION '% rows are append-only', TG_TABLE_NAME; END $function$;

CREATE OR REPLACE FUNCTION public.fleet_artifact_change()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ DECLARE r record; k text; i text; t text; s text; h text; p text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'store' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'observed_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'observed_at' - 'updated_at') THEN RETURN NULL; END IF; k := 'host'; i := r.host_id; h := r.host_id; ELSE IF TG_TABLE_NAME = 'artifact_copy' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - 'lease_expires_at' - 'updated_at') IS NOT DISTINCT FROM (to_jsonb(NEW) - 'lease_expires_at' - 'updated_at') THEN RETURN NULL; END IF; i := r.artifact_id; ELSE i := r.id; END IF; k := 'artifact'; p := r.project_id; SELECT coalesce(a.task_id, e.task_id), coalesce(a.session_id, e.session_id) INTO t, s FROM artifact a LEFT JOIN execution e ON e.id = a.execution_id WHERE a.id = i; IF TG_TABLE_NAME = 'artifact' THEN s := coalesce(s, r.session_id); END IF; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op) VALUES (k, i, t, s, h, p, left(TG_OP, 1)); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $function$;

CREATE OR REPLACE FUNCTION public.fleet_change()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ DECLARE r record; k text; i text; t text; s text; h text; w text; ek text; st text; BEGIN IF TG_OP = 'DELETE' THEN r := OLD; ELSE r := NEW; END IF; IF TG_TABLE_NAME = 'audit_log' THEN k := 'setting-change'; i := r.id; w := r.holder; st := nullif(r.target, ''); ELSIF TG_TABLE_NAME = 'project_recommendation' THEN k := 'project-recommendation'; i := r.id; ELSIF TG_TABLE_NAME IN ('delivery_state', 'delivery_pr', 'delivery_run', 'delivery_release', 'delivery_check') THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - '{updated_at,attempts,next_attempt_at,last_error}'::text[]) = (to_jsonb(NEW) - '{updated_at,attempts,next_attempt_at,last_error}'::text[]) THEN RETURN NULL; END IF; k := 'delivery'; i := r.id; ELSIF TG_TABLE_NAME = 'host_observation' THEN IF TG_OP = 'UPDATE' AND (to_jsonb(OLD) - '{last_seen_at,updated_at}'::text[]) = (to_jsonb(NEW) - '{last_seen_at,updated_at}'::text[]) THEN RETURN NULL; END IF; k := 'host'; i := r.id; h := r.id; ELSIF TG_TABLE_NAME IN ('managed_account', 'api_connection') THEN k := replace(TG_TABLE_NAME, '_', '-'); i := r.id; w := r.holder; ELSIF TG_TABLE_NAME = 'connection_key' THEN k := 'connection-key'; i := r.id; ELSIF TG_TABLE_NAME = 'session' THEN k := 'session'; i := r.id; s := r.id; ELSIF TG_TABLE_NAME IN ('message', 'event') THEN k := 'event'; i := r.id; s := r.session_id; ELSIF TG_TABLE_NAME = 'command' THEN k := 'command'; i := r.id; h := r.host_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'task' THEN k := 'task'; i := r.id; t := r.id; ELSIF TG_TABLE_NAME = 'task_derived' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'task_dependency' THEN k := 'task'; i := r.task_id; t := r.task_id; ELSIF TG_TABLE_NAME = 'execution' THEN IF r.kind = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.id; t := r.task_id; s := r.session_id; h := r.host_id; ELSIF TG_TABLE_NAME = 'execution_totals' THEN SELECT e.kind::text, e.task_id, e.session_id, e.host_id INTO ek, t, s, h FROM execution e WHERE e.id = r.execution_id; IF ek = 'tool_run' THEN RETURN NULL; END IF; k := 'execution'; i := r.execution_id; ELSIF TG_TABLE_NAME = 'question' THEN k := 'question'; i := r.id; t := r.task_id; s := r.session_id; ELSIF TG_TABLE_NAME = 'experiment' THEN k := 'experiment'; i := r.id; ELSIF TG_TABLE_NAME = 'score' THEN k := 'score'; i := r.id; ELSIF TG_TABLE_NAME = 'definition' THEN k := 'definition'; i := r.kind || '/' || r.id; ELSE RETURN NULL; END IF; INSERT INTO change(kind, id, task_id, session_id, host_id, project_id, op, actor, holder, setting_target) VALUES (k, i, t, s, h, r.project_id, left(TG_OP, 1), nullif(current_setting('fleet.actor', true), ''), w, st); PERFORM fleet_enqueue_wake('fleet_change', ''); RETURN NULL; END $function$;

CREATE OR REPLACE FUNCTION public.fleet_deleted_projects()
 RETURNS text[]
 LANGUAGE sql
 STABLE
AS $function$ SELECT coalesce(array_agg(id), '{}') FROM definition WHERE kind = 'project' AND deleted_at IS NOT NULL $function$;

CREATE OR REPLACE FUNCTION public.fleet_enqueue_wake(ch text, body text)
 RETURNS void
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;
REVOKE ALL ON FUNCTION fleet_enqueue_wake(ch text, body text) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION fleet_enqueue_wake(ch text, body text) TO fleet_app;

CREATE OR REPLACE FUNCTION public.fleet_entity_code()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_exec_conn_meta(conn_name text, aid text)
 RETURNS TABLE(provider text, is_api_conn boolean)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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

CREATE OR REPLACE FUNCTION public.fleet_experiment_record()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ BEGIN IF NEW.state IN ('stopped', 'done') AND NEW.decision IS NULL THEN NEW.decision := 'pending'; END IF; IF OLD.state = 'draft' AND NEW.state <> 'draft' AND NEW.launched_at IS NULL THEN NEW.launched_at := now(); END IF; RETURN NEW; END $function$;

CREATE OR REPLACE FUNCTION public.fleet_inherit_tombstone()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ DECLARE e text; s text; BEGIN
 IF NEW.tombstoned_at IS NOT NULL OR NOT fleet_show_deleted() THEN RETURN NEW; END IF;
 e := to_jsonb(NEW)->>TG_ARGV[0];
 IF TG_NARGS > 1 THEN s := to_jsonb(NEW)->>TG_ARGV[1]; END IF;
 IF e IS NULL AND s IS NOT NULL AND EXISTS (SELECT 1 FROM session WHERE id = s AND tombstoned_at IS NOT NULL) THEN
  NEW.tombstoned_at := now(); RETURN NEW;
 END IF;
 IF e IS NOT NULL AND EXISTS (SELECT 1 FROM execution WHERE id = e AND tombstoned_at IS NOT NULL) THEN NEW.tombstoned_at := now(); END IF;
 RETURN NEW;
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_issue_code(p_type text, p_project text, p_entity text)
 RETURNS text
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_note_append_only()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ BEGIN
 IF TG_OP = 'DELETE' AND coalesce(current_setting('fleet.purge', true), '') = 'on' THEN RETURN OLD; END IF;
 IF TG_OP = 'UPDATE' AND to_jsonb(NEW) - 'tombstoned_at' = to_jsonb(OLD) - 'tombstoned_at' THEN RETURN NEW; END IF;
 RAISE EXCEPTION 'experiment notes are append-only';
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_own_projectless_execution(eid text)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
DECLARE prev text := current_setting('fleet.project_ids',true); who text := current_setting('fleet.person',true); sid text; director text;
BEGIN
 IF coalesce(who,'')='' OR eid IS NULL THEN RETURN false; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 SELECT session_id,directed_by INTO sid,director FROM execution WHERE id=eid AND project_id IS NULL;
 PERFORM set_config('fleet.project_ids',coalesce(prev,''),true);
 RETURN coalesce(director=who AND fleet_own_projectless_session(sid),false);
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_own_projectless_session(sid text)
 RETURNS boolean
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
DECLARE prev text := current_setting('fleet.project_ids',true); who text := current_setting('fleet.person',true); allowed boolean;
BEGIN
 IF coalesce(who,'')='' OR sid IS NULL THEN RETURN false; END IF;
 PERFORM set_config('fleet.project_ids','*',true);
 SELECT EXISTS(SELECT 1 FROM session WHERE id=sid AND project_id IS NULL AND directed_by=who AND director_access_revoked_at IS NULL) INTO allowed;
 PERFORM set_config('fleet.project_ids',coalesce(prev,''),true); RETURN allowed;
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_project_code(p_project text)
 RETURNS text
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_project_code_set(p_project text, p_code text)
 RETURNS text
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_resource_summary_change()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ DECLARE k text; BEGIN
 k := 'resource_summary';
 INSERT INTO change(kind,id,project_id,op) VALUES(k,NEW.project_id,NEW.project_id,'U');
 PERFORM fleet_enqueue_wake('fleet_change','');
 RETURN NULL;
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_session_director_immutable()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ BEGIN
 IF OLD.directed_by IS NOT NULL AND OLD.directed_by IS DISTINCT FROM NEW.directed_by THEN RAISE EXCEPTION 'session director is immutable'; END IF;
 RETURN NEW;
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_session_hidden_lineage_count(sid text)
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
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_show_deleted()
 RETURNS boolean
 LANGUAGE sql
 STABLE
AS $function$ SELECT coalesce(current_setting('fleet.deleted', true), '') = 'on' $function$;

CREATE OR REPLACE FUNCTION public.fleet_task_derived(p_task text)
 RETURNS void
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$ DECLARE prev text := current_setting('fleet.project_ids', true); BEGIN IF p_task IS NULL THEN RETURN; END IF; PERFORM set_config('fleet.project_ids', '*', true); PERFORM task_derived_recompute(p_task); PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true); END $function$;

CREATE OR REPLACE FUNCTION public.fleet_task_derived_touch()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ DECLARE a text; b text; BEGIN IF TG_TABLE_NAME = 'task' THEN PERFORM fleet_task_derived(NEW.id); RETURN NULL; END IF; IF TG_OP <> 'INSERT' THEN a := OLD.task_id; END IF; IF TG_OP <> 'DELETE' THEN b := NEW.task_id; END IF; IF a IS NOT NULL AND b IS NOT NULL AND a <> b THEN PERFORM fleet_task_derived(least(a, b)); PERFORM fleet_task_derived(greatest(a, b)); ELSE PERFORM fleet_task_derived(coalesce(b, a)); END IF; RETURN NULL; END $function$;

CREATE OR REPLACE FUNCTION public.fleet_task_hidden_lineage_count(tid text)
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
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_task_state_at()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ BEGIN IF (NEW.state, NEW.pause_reason, NEW.wait_reason) IS DISTINCT FROM (OLD.state, OLD.pause_reason, OLD.wait_reason) THEN NEW.state_at := now(); END IF; RETURN NEW; END $function$;

CREATE OR REPLACE FUNCTION public.fleet_token_count(v jsonb)
 RETURNS bigint
 LANGUAGE sql
 IMMUTABLE
AS $function$
 SELECT CASE WHEN jsonb_typeof(v)='number' AND v::text ~ '^[0-9]{1,18}$' THEN v::text::bigint END
$function$;

CREATE OR REPLACE FUNCTION public.fleet_visible(p_project text)
 RETURNS boolean
 LANGUAGE sql
 STABLE PARALLEL SAFE
AS $function$
 SELECT current_setting('fleet.project_ids', true) = '*' OR coalesce(p_project, '') = ANY (string_to_array(current_setting('fleet.project_ids', true), ','))
$function$;

CREATE OR REPLACE FUNCTION public.fleet_window_leaf_count(win_from timestamp with time zone, win_to timestamp with time zone)
 RETURNS bigint
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;

CREATE OR REPLACE FUNCTION public.fleet_write_model(eid text)
 RETURNS void
 LANGUAGE sql
AS $function$
 UPDATE execution_totals SET model=(SELECT payload->'data'->>'model' FROM event WHERE NOT delta AND execution_id=eid
  AND jsonb_typeof(payload->'data'->'model')='string' ORDER BY seq DESC LIMIT 1) WHERE execution_id=eid
$function$;

CREATE OR REPLACE FUNCTION public.fleet_write_native_observations(eid text)
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
$function$;

CREATE OR REPLACE FUNCTION public.fleet_write_tokens(eid text)
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
$function$;

CREATE OR REPLACE FUNCTION public.resource_align_totals_owner()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;
REVOKE ALL ON FUNCTION resource_align_totals_owner() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.resource_capture_execution()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;
REVOKE ALL ON FUNCTION resource_capture_execution() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.resource_capture_metadata()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;
REVOKE ALL ON FUNCTION resource_capture_metadata() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.resource_capture_totals()
 RETURNS trigger
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$
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
END $function$;
REVOKE ALL ON FUNCTION resource_capture_totals() FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.resource_enqueue_source(eid text, old_project text, new_project text)
 RETURNS void
 LANGUAGE plpgsql
 SET search_path TO 'public'
AS $function$
DECLARE wid uuid; pending timestamptz;
BEGIN
 INSERT INTO resource_work(execution_id) VALUES(eid)
 ON CONFLICT ON CONSTRAINT resource_work_source_key DO UPDATE SET execution_id=excluded.execution_id
 RETURNING work_id,first_pending_at INTO wid,pending;
 INSERT INTO resource_work_scope(work_id,project_id,first_pending_at)
 SELECT wid,p,pending FROM (VALUES(old_project),(new_project)) v(p) WHERE p IS NOT NULL
 ON CONFLICT DO NOTHING;
 PERFORM fleet_enqueue_wake('fleet_work','resource-work');
END $function$;
REVOKE ALL ON FUNCTION resource_enqueue_source(eid text, old_project text, new_project text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.resource_execution_facts(r execution)
 RETURNS jsonb
 LANGUAGE sql
 IMMUTABLE
AS $function$
 SELECT jsonb_build_array(r.kind,r.project_id,r.state,r.account_id,r.harness,r.model,r.started_at,r.ended_at,
 CASE WHEN r.started_at IS NULL AND NOT (coalesce(r.data->>'startedAt','') ~ '^[0-9]{1,15}$') AND r.ended_at IS NULL THEN r.last_seen_at END,
 r.data->'provider',r.data->'connection',r.data->'tool',r.data->'outcome',r.data->'startedAt')
$function$;

CREATE OR REPLACE FUNCTION public.resource_metadata_class(p_kind text, p_id text, p_provider jsonb)
 RETURNS jsonb
 LANGUAGE sql
 IMMUTABLE
AS $function$
 SELECT jsonb_build_object('accountId', p_id, 'connectionName', CASE WHEN p_kind = 'api-connection' THEN substr(p_id, 12) END,
  'kind', p_kind, 'provider', p_provider)
$function$;

CREATE OR REPLACE FUNCTION public.resource_reasons_merge(old jsonb, delta jsonb)
 RETURNS jsonb
 LANGUAGE plpgsql
 IMMUTABLE
 SET search_path TO 'public'
AS $function$
DECLARE merged jsonb;
BEGIN
 SELECT coalesce(jsonb_object_agg(k,v),'{}'::jsonb) INTO merged FROM (
  SELECT k,sum(v) v FROM (SELECT key k,value::bigint v FROM jsonb_each_text(old) UNION ALL SELECT key,value::bigint FROM jsonb_each_text(delta)) x
  GROUP BY k HAVING sum(v)<>0) y;
 IF EXISTS(SELECT 1 FROM jsonb_each_text(merged) WHERE value::bigint<0) THEN RAISE EXCEPTION 'negative resource reason count'; END IF;
 RETURN merged;
END $function$;

CREATE OR REPLACE FUNCTION public.resource_work_pending_copy()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'public'
AS $function$
BEGIN UPDATE resource_work_scope SET first_pending_at=NEW.first_pending_at WHERE work_id=NEW.work_id; RETURN NULL; END $function$;

CREATE OR REPLACE FUNCTION public.resource_work_scope_stamp()
 RETURNS trigger
 LANGUAGE plpgsql
 SET search_path TO 'public'
AS $function$
BEGIN
 IF NEW.first_pending_at IS NULL THEN SELECT first_pending_at INTO NEW.first_pending_at FROM resource_work WHERE work_id=NEW.work_id; END IF;
 RETURN NEW;
END $function$;

CREATE OR REPLACE FUNCTION public.task_derived_drift()
 RETURNS TABLE(task_id text, present boolean, stored jsonb, expected jsonb)
 LANGUAGE plpgsql
 SECURITY DEFINER
 SET search_path TO 'public'
AS $function$ DECLARE prev text := current_setting('fleet.project_ids', true); BEGIN PERFORM set_config('fleet.project_ids', '*', true); RETURN QUERY SELECT t.id, d.task_id IS NOT NULL, CASE WHEN d.task_id IS NULL THEN NULL ELSE jsonb_build_object('projectId', d.project_id, 'agent', d.agent, 'host', d.host, 'startedAt', d.started_at, 'openQuestions', d.open_questions, 'executions', d.executions, 'lastAgent', d.last_agent) END, jsonb_build_object('projectId', v.project_id, 'agent', v.agent, 'host', v.host, 'startedAt', v.started_at, 'openQuestions', v.open_questions, 'executions', v.executions, 'lastAgent', v.last_agent) FROM task t CROSS JOIN LATERAL task_derived_values(t.id) v LEFT JOIN task_derived d ON d.task_id = t.id WHERE (prev = '*' OR coalesce(t.project_id, '') = ANY (string_to_array(coalesce(prev, ''), ','))) AND (d.task_id IS NULL OR (d.project_id, d.agent, d.host, d.started_at, d.open_questions, d.executions, d.last_agent) IS DISTINCT FROM (v.project_id, v.agent, v.host, v.started_at, v.open_questions, v.executions, v.last_agent)) ORDER BY t.id; PERFORM set_config('fleet.project_ids', coalesce(prev, ''), true); END $function$;

CREATE OR REPLACE FUNCTION public.task_derived_recompute(p_task text)
 RETURNS void
 LANGUAGE plpgsql
AS $function$ DECLARE v record; BEGIN IF NOT EXISTS (SELECT 1 FROM task WHERE id = p_task) THEN RETURN; END IF; PERFORM 1 FROM task_derived WHERE task_id = p_task FOR UPDATE; IF NOT FOUND THEN INSERT INTO task_derived(task_id, project_id, agent, host, started_at, open_questions, executions, last_agent) SELECT p_task, x.* FROM task_derived_values(p_task) x ON CONFLICT (task_id) DO NOTHING; IF FOUND THEN RETURN; END IF; PERFORM 1 FROM task_derived WHERE task_id = p_task FOR UPDATE; END IF; SELECT * INTO v FROM task_derived_values(p_task); UPDATE task_derived d SET project_id = v.project_id, agent = v.agent, host = v.host, started_at = v.started_at, open_questions = v.open_questions, executions = v.executions, last_agent = v.last_agent WHERE d.task_id = p_task AND (d.project_id, d.agent, d.host, d.started_at, d.open_questions, d.executions, d.last_agent) IS DISTINCT FROM (v.project_id, v.agent, v.host, v.started_at, v.open_questions, v.executions, v.last_agent); END $function$;
REVOKE ALL ON FUNCTION task_derived_recompute(p_task text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.task_derived_values(p_task text)
 RETURNS TABLE(project_id text, agent text, host text, started_at timestamp with time zone, open_questions integer, executions integer, last_agent text)
 LANGUAGE sql
 STABLE
 SET search_path TO 'public'
AS $function$ SELECT (SELECT t.project_id FROM task t WHERE t.id = p_task), (SELECT e.harness FROM execution e WHERE e.task_id = p_task AND e.state IN ('preparing', 'running') ORDER BY e.seq DESC LIMIT 1), (SELECT e.host_id FROM execution e WHERE e.task_id = p_task ORDER BY e.seq DESC LIMIT 1), (SELECT min(e.started_at) FROM execution e WHERE e.task_id = p_task), (SELECT count(*)::int FROM question q WHERE q.task_id = p_task AND q.state = 'open'), (SELECT count(*)::int FROM execution e WHERE e.task_id = p_task), (SELECT e.harness FROM execution e WHERE e.task_id = p_task ORDER BY e.seq DESC LIMIT 1) $function$;
REVOKE ALL ON FUNCTION task_derived_values(p_task text) FROM PUBLIC;

CREATE OR REPLACE FUNCTION public.touch_updated_at()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$ BEGIN NEW.updated_at := now(); RETURN NEW; END $function$;
