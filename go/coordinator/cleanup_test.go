package main

import (
	"slices"
	"strings"
	"testing"

	"fleet/contract"

	"github.com/jackc/pgx/v5"
)

// schemaRefColumns: every *_id, *_ids and id column and every foreign-key column of the public tables.
const schemaRefColumns = `SELECT c.table_name||'.'||c.column_name FROM information_schema.columns c
 JOIN information_schema.tables t ON t.table_schema=c.table_schema AND t.table_name=c.table_name
 WHERE c.table_schema='public' AND t.table_type='BASE TABLE' AND (c.column_name='id' OR c.column_name LIKE '%\_id' OR c.column_name LIKE '%\_ids')
UNION SELECT k.table_name||'.'||k.column_name FROM information_schema.table_constraints tc
 JOIN information_schema.key_column_usage k ON k.constraint_schema=tc.constraint_schema AND k.constraint_name=tc.constraint_name
 WHERE tc.constraint_type='FOREIGN KEY' AND tc.table_schema='public' ORDER BY 1`

func unmappedColumns(cols []string) []string {
	out := []string{}
	for _, c := range cols {
		if !slices.ContainsFunc(referenceMap, func(r objectRef) bool { return r.table+"."+r.column == c }) {
			out = append(out, c)
		}
	}
	return out
}

func psqlLines(t *testing.T, e *testCoordEnv, sql string) []string {
	t.Helper()
	out := strings.TrimSpace(runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", sql))
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// Requirement: fleet-system-management.md, Archive, delete, purge: "Reference map first ... a test compares it with the
// schema and fails when a new foreign key or *_id column is not listed". Risk: a new column names a deleted object and
// purge neither removes its row nor keeps the object, leaving rows pointing at nothing (or a foreign key failing purge).
func TestReferenceMapCoversSchema(t *testing.T) {
	e := openTestCoordinator(t, false)
	cols := psqlLines(t, e, schemaRefColumns)
	if len(cols) < 50 {
		t.Fatalf("schema columns not read: %v", cols)
	}
	if u := unmappedColumns(cols); len(u) > 0 {
		t.Fatalf("columns missing from referenceMap (cleanup.go): %v", u)
	}
	all := psqlLines(t, e, "SELECT table_name||'.'||column_name FROM information_schema.columns WHERE table_schema='public'")
	ownTables := map[string]bool{}
	for _, r := range referenceMap {
		if !slices.Contains(all, r.table+"."+r.column) {
			t.Errorf("referenceMap names %s.%s, which the schema does not have", r.table, r.column)
		}
		if r.mode != refNone && r.kind == "" {
			t.Errorf("%s.%s: a reference without a kind", r.table, r.column)
		}
		if r.mode == refOwn || r.mode == refWithProject {
			ownTables[r.table] = true
		}
	}
	for tbl := range ownTables {
		if !slices.Contains(purgeOrder, tbl) {
			t.Errorf("rows of %s go with an object but purgeOrder does not delete them", tbl)
		}
	}
	for _, tbl := range purgeOrder {
		if !ownTables[tbl] {
			t.Errorf("purgeOrder deletes from %s, which no refOwn column covers", tbl)
		}
	}
	// The failure the test exists for: a new project column nobody listed.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", "ALTER TABLE task ADD COLUMN foo_project_id text")
	if u := unmappedColumns(psqlLines(t, e, schemaRefColumns)); !slices.Equal(u, []string{"task.foo_project_id"}) {
		t.Fatalf("an unlisted column must be reported, got %v", u)
	}
}

func appCount(t *testing.T, e *testCoordEnv, show bool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.appStore.Tx(e.ctx, nil, false, "", func(tx pgx.Tx) error {
		if show {
			if _, err := tx.Exec(e.ctx, "SELECT set_config('fleet.deleted','on',true)"); err != nil {
				return err
			}
		}
		return tx.QueryRow(e.ctx, sql, args...).Scan(&n)
	}); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

func kindCounts(v any) map[string]float64 {
	out := map[string]float64{}
	for _, x := range anys(v) {
		m := mapOf(x)
		out[astr(m["kind"])] = num(m["count"])
		if b, ok := m["bytes"].(float64); ok {
			out[astr(m["kind"])+".bytes"] = b
		}
	}
	return out
}

// Requirement: fleet-system-management.md, Archive, delete, purge (object_delete, system_purge), CUJ 2 and test 4, NFR
// "Preview equals effect". Risk: deleted test data still shows in lists, keeps running or dispatching, or purge removes a
// trial another experiment's score cites, or leaves rows of a purged project behind.
func TestObjectDeleteAndPurge(t *testing.T) {
	e := openTestCoordinator(t, false)
	secret := e.registerHost(t, "h-del")
	setup := `
INSERT INTO definition(kind,id,path,head,project_id) VALUES ('project','acc','config/projects/acc/project.json',1,'acc'),('project','keep','config/projects/keep/project.json',1,'keep');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','acc',1,'{"settings":{"id":"acc","name":"Acc","contextFiles":[]},"contextFiles":[]}','x','editor','person','t'),('project','keep',1,'{"settings":{"id":"keep","name":"Keep","contextFiles":[]},"contextFiles":[]}','y','editor','person','t');
INSERT INTO task(id,title,project_id) VALUES ('t1','acceptance task','acc'),('t2','live task','keep');
INSERT INTO document(kind,id,data,project_id) VALUES ('session','s1','{"id":"s1","projectId":"acc"}','acc'),
 ('event','event:e1:x','{"id":"event:e1:x","executionId":"e1","seq":0,"type":"x"}','acc');
INSERT INTO execution(id,kind,state,project_id,task_id,session_id,data) VALUES
 ('e1','agent_run','succeeded','acc','t1','s1','{"id":"e1","sessionId":"s1"}'),
 ('e2','agent_run','running','acc','t1','s1','{"id":"e2","sessionId":"s1","hostId":"h-del"}');
UPDATE execution SET host_id='h-del' WHERE id='e2';
INSERT INTO execution_totals(execution_id,project_id) VALUES ('e1','acc');
INSERT INTO question(id,kind,text,project_id,task_id,state) VALUES ('q1','operator_decision','decide','acc','t1','open');
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,state) VALUES ('a1','acc','prod','h1',10,'text/plain','n','e1','t1','stored');
INSERT INTO experiment(id,project_id,kind,pipeline_id,state) VALUES ('x1','keep','single','p','running'),('x2','keep','single','p','done');
INSERT INTO execution(id,kind,state,project_id,experiment_id,data) VALUES ('r1','pipeline_run','succeeded','keep','x1','{"id":"r1"}');
INSERT INTO execution(id,kind,state,project_id,experiment_id,parent_execution_id,data) VALUES ('r1s','pipeline_step','succeeded','keep','x1','r1','{"id":"r1s"}');
INSERT INTO score(id,experiment_id,project_id,trial_ids,evaluator,evaluator_version,form,value) VALUES
 ('sc1','x1','keep','{r1}','j','1','typed','{}'),('sc2','x2','keep','{r1}','j','1','typed','{}');
INSERT INTO experiment_note(id,experiment_id,project_id,kind,text,author) VALUES ('n1','x1','keep','note','n','owner@trial.test');
INSERT INTO question(id,kind,text,project_id,state,options) VALUES ('qr','rating','rate','keep','open','{"experiment_id":"x1"}');`
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", setup)
	owner := e.ownerHeaders()
	call := func(op string, body any, headers map[string]string) (int, map[string]any) {
		return e.callJSON(t, "POST", "/api/ops/"+op, body, headers)
	}
	if c, _ := call("object_delete", doc{"kind": "project", "id": "acc", "preview": true}, map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}); c != 403 {
		t.Fatalf("a member deletes: %d", c)
	}
	want := map[string]float64{"project": 1, "task": 1, "execution": 2, "session": 1, "question": 1, "artifact": 1, "artifact.bytes": 10}
	c, preview := call("object_delete", doc{"kind": "project", "id": "acc", "preview": true}, owner)
	if c != 200 || !mapsEqual(kindCounts(preview["affected"]), want) || num(preview["running"]) != 1 || preview["deleted"] != false {
		t.Fatalf("preview = %d %v", c, preview)
	}
	if n := appCount(t, e, false, "SELECT count(*) FROM task WHERE project_id='acc'"); n != 1 {
		t.Fatalf("a preview changed something: %d tasks", n)
	}
	c, done := call("object_delete", doc{"kind": "project", "id": "acc", "preview": false}, owner)
	if c != 200 || !mapsEqual(kindCounts(done["affected"]), want) || done["deleted"] != true || num(done["teardowns"]) != 1 {
		t.Fatalf("confirm = %d %v (want the preview's counts)", c, done)
	}
	for _, sql := range []string{"SELECT count(*) FROM task WHERE project_id='acc'", "SELECT count(*) FROM execution WHERE project_id='acc'",
		"SELECT count(*) FROM question WHERE project_id='acc'", "SELECT count(*) FROM artifact WHERE project_id='acc'",
		"SELECT count(*) FROM document WHERE project_id='acc'", "SELECT count(*) FROM execution_totals WHERE project_id='acc'",
		"SELECT count(*) FROM definition WHERE kind='project' AND id='acc' AND retired_at IS NULL"} {
		if n := appCount(t, e, false, sql); n != 0 {
			t.Errorf("deleted rows still read: %s = %d", sql, n)
		}
	}
	if n := appCount(t, e, false, "SELECT count(*) FROM task WHERE project_id='keep'"); n != 1 {
		t.Fatalf("another project's task hidden: %d", n)
	}
	if n := appCount(t, e, true, "SELECT count(*) FROM execution WHERE id IN ('e1','e2') AND tombstoned_at IS NOT NULL"); n != 2 {
		t.Fatalf("tombstones: %d", n)
	}
	if n := appCount(t, e, true, "SELECT count(*) FROM execution WHERE id='e2' AND data->>'stopDetail'='deleted' AND data->>'stopRequested'='true'"); n != 1 {
		t.Fatalf("the running execution was not stopped")
	}
	if n := appCount(t, e, true, "SELECT count(*) FROM question WHERE id='q1' AND state='withdrawn'"); n != 1 {
		t.Fatalf("the open question was not withdrawn")
	}
	c, list := call("executions_list", doc{}, owner)
	if c != 200 || slices.ContainsFunc(anys(list["executions"]), func(x any) bool { id := astr(mapOf(x)["id"]); return id == "e1" || id == "e2" }) {
		t.Fatalf("executions_list = %d %v", c, list)
	}
	c, tasks := call("task_list", doc{}, owner)
	if c != 200 || strings.Contains(js(tasks), "acceptance task") || !strings.Contains(js(tasks), "live task") {
		t.Fatalf("task_list = %d %v", c, tasks)
	}
	// The host still gets the stop and the teardown of the deleted execution (host calls see deleted rows).
	c, polled := e.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{HostId: "h-del", Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}}, map[string]string{"Authorization": "Bearer " + secret})
	kinds := []string{}
	for _, x := range anys(polled["commands"]) {
		kinds = append(kinds, astr(mapOf(x)["kind"]))
	}
	if c != 200 || !slices.Contains(kinds, "stop") || !slices.Contains(kinds, "teardown") {
		t.Fatalf("poll = %d %v", c, polled)
	}
	if c, _ := call("object_delete", doc{"kind": "project", "id": "acc", "preview": true}, owner); c != 409 {
		t.Fatalf("a second delete: %d", c)
	}

	c, xd := call("object_delete", doc{"kind": "experiment", "id": "x1", "preview": false}, owner)
	if wantX := map[string]float64{"experiment": 1, "execution": 2, "question": 1}; c != 200 || !mapsEqual(kindCounts(xd["affected"]), wantX) {
		t.Fatalf("experiment delete = %d %v", c, xd)
	}
	if n := appCount(t, e, false, "SELECT count(*) FROM experiment WHERE id='x1'"); n != 0 {
		t.Fatalf("deleted experiment still read")
	}

	c, p := call("system_purge", doc{"preview": true}, owner)
	removed, kept := map[string][]string{}, map[string][]string{}
	for _, x := range anys(p["removed"]) {
		removed[astr(mapOf(x)["kind"])] = strs(mapOf(x)["ids"])
	}
	for _, x := range anys(p["kept"]) {
		kept[astr(mapOf(x)["kind"])] = strs(mapOf(x)["ids"])
	}
	if c != 200 || astr(mapOf(anys(p["removed"])[0])["kind"]) != "host" || !slices.Equal(removed["project"], []string{"acc"}) || !slices.Equal(kept["experiment"], []string{"x1"}) || p["purged"] != false {
		t.Fatalf("purge preview = %d %v", c, p)
	}
	if c, _ := call("system_purge", doc{"preview": false}, map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}); c != 403 {
		t.Fatalf("a member purges: %d", c)
	}
	c, purged := call("system_purge", doc{"preview": false}, owner)
	if c != 200 || purged["purged"] != true || num(purged["retryLater"]) != 0 {
		t.Fatalf("purge = %d %v", c, purged)
	}
	for _, tbl := range []string{"task", "execution", "question", "artifact", "document", "execution_totals", "task_derived", "entity_code"} {
		if n := psqlLines(t, e, "SELECT count(*) FROM "+tbl+" WHERE project_id='acc'"); n[0] != "0" {
			t.Errorf("purged project rows left in %s: %v", tbl, n)
		}
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM definition WHERE kind='project' AND id='acc' AND purged_at IS NOT NULL AND deleted_at IS NOT NULL"); n[0] != "1" {
		t.Fatalf("the purged project's definition row must stay (its id is never reused)")
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM execution WHERE id IN ('r1','r1s')"); n[0] != "2" {
		t.Fatalf("a trial another experiment's score cites was purged")
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", "DELETE FROM score WHERE id='sc2'")
	c, again := call("system_purge", doc{"preview": false, "kinds": []string{"experiment"}}, owner)
	if c != 200 || !slices.Equal(strs(mapOf(anys(again["removed"])[0])["ids"]), []string{"x1"}) {
		t.Fatalf("purge after the citing score went = %d %v", c, again)
	}
	for _, sql := range []string{"SELECT count(*) FROM experiment WHERE id='x1'", "SELECT count(*) FROM execution WHERE id IN ('r1','r1s')",
		"SELECT count(*) FROM score WHERE experiment_id='x1'", "SELECT count(*) FROM experiment_note WHERE experiment_id='x1'", "SELECT count(*) FROM question WHERE id='qr'"} {
		if n := psqlLines(t, e, sql); n[0] != "0" {
			t.Errorf("experiment purge left %s = %v", sql, n)
		}
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM experiment WHERE id='x2'"); n[0] != "1" {
		t.Fatalf("a live experiment was purged")
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM document WHERE kind='setting-change' AND data->>'field' IN ('deletedAt','purge')"); n[0] != "4" {
		t.Fatalf("audit rows: %v", n)
	}
}

func mapsEqual(a, b map[string]float64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
