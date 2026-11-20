package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

func unmappedColumns(refs []objectRef, cols []string) []string {
	out := []string{}
	for _, c := range cols {
		if !slices.ContainsFunc(refs, func(r objectRef) bool { return r.table+"."+r.column == c }) {
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
	var refs []objectRef
	if err := e.ownerStore.Tx(e.ctx, nil, false, "", func(tx pgx.Tx) (err error) {
		refs, err = references(backgroundRequest(e.ctx, nil, tx))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if u := unmappedColumns(refs, cols); len(u) > 0 {
		t.Fatalf("columns missing from referenceMap (cleanup.go): %v", u)
	}
	all := psqlLines(t, e, "SELECT table_name||'.'||column_name FROM information_schema.columns WHERE table_schema='public'")
	ownTables := map[string]bool{}
	for _, r := range refs {
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
	if u := unmappedColumns(refs, psqlLines(t, e, schemaRefColumns)); !slices.Equal(u, []string{"task.foo_project_id"}) {
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
INSERT INTO session(id,project_id) VALUES ('s1','acc');
INSERT INTO execution(id,kind,state,project_id,task_id,session_id,data) VALUES
 ('e1','agent_run','succeeded','acc','t1','s1','{"id":"e1","sessionId":"s1"}'),
 ('e2','agent_run','running','acc','t1','s1','{"id":"e2","sessionId":"s1","hostId":"h-del"}');
INSERT INTO event(id,execution_id,project_id,event_seq,type) VALUES ('event:e1:x','e1','acc',0,'x');
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
		"SELECT count(*) FROM session WHERE project_id='acc'",
		"SELECT count(*) FROM event WHERE project_id='acc'", "SELECT count(*) FROM command WHERE project_id='acc'",
		"SELECT count(*) FROM execution_totals WHERE project_id='acc'",
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
	// A host route an agent's tools reach (artifact read) does not see a deleted artifact; poll above did see the
	// deleted execution's commands.
	if c, body := e.callJSON(t, "POST", "/api/hosts/artifacts/get", doc{"hostId": "h-del", "artifactId": "a1"}, map[string]string{"Authorization": "Bearer " + secret}); c != 404 {
		t.Fatalf("a host reads a deleted artifact: %d %v", c, body)
	}

	// Another experiment's score cites a trial of x1: the delete is refused while it does (design: delete checks scores).
	if c, body := call("object_delete", doc{"kind": "experiment", "id": "x1", "preview": true}, owner); c != 409 || !strings.Contains(astr(body["message"]), "score.trial_ids") {
		t.Fatalf("delete of a cited experiment = %d %v", c, body)
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", "DELETE FROM score WHERE id='sc2'")
	c, xd := call("object_delete", doc{"kind": "experiment", "id": "x1", "preview": false}, owner)
	if wantX := map[string]float64{"experiment": 1, "execution": 2, "question": 1}; c != 200 || !mapsEqual(kindCounts(xd["affected"]), wantX) {
		t.Fatalf("experiment delete = %d %v", c, xd)
	}
	for _, sql := range []string{"SELECT count(*) FROM experiment WHERE id='x1'", "SELECT count(*) FROM score WHERE experiment_id='x1'",
		"SELECT count(*) FROM experiment_note WHERE experiment_id='x1'", "SELECT count(*) FROM question WHERE id='qr'"} {
		if n := appCount(t, e, false, sql); n != 0 {
			t.Errorf("deleted experiment rows still read: %s = %d", sql, n)
		}
	}
	if c, _ := call("object_delete", doc{"kind": "experiment", "id": "x1", "preview": true}, owner); c != 409 {
		t.Fatalf("a second experiment delete: %d", c)
	}
	// A host reports for the deleted trial after the delete: the row is born tombstoned.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `SET fleet.deleted = 'on'; INSERT INTO event(id,execution_id,project_id,event_seq,type) VALUES ('event:r1:late','r1','keep',1,'x')`)
	if n := appCount(t, e, false, "SELECT count(*) FROM event WHERE id='event:r1:late'"); n != 0 {
		t.Fatalf("a late row of a deleted trial is read")
	}
	// A row written later that names a tombstoned trial keeps it at purge (design test 4).
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", "INSERT INTO score(id,experiment_id,project_id,trial_ids,evaluator,evaluator_version,form,value) VALUES ('sc3','x2','keep','{r1}','j','1','typed','{}')")

	// The stop and teardown the host fetched but has not finished keep the project: purging them would leave the agent
	// running. The host's receipts settle them.
	if c, p := call("system_purge", doc{"preview": true, "kinds": []string{"project"}}, owner); c != 200 || !slices.Equal(strs(mapOf(anys(p["kept"])[0])["ids"]), []string{"acc"}) ||
		!strings.HasPrefix(astr(mapOf(anys(mapOf(anys(p["kept"])[0])["reasons"])[0])["reason"]), "pending stop command ") {
		t.Fatalf("a project with pending commands is purgeable, or no reason given: %d %v", c, p)
	}
	// The host settles them; a command for a host that is gone (removed, then purged) never is, and does not keep it.
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `UPDATE command SET status='completed' WHERE execution_id='e2';
INSERT INTO command(id,kind,execution_id,host_id,status) VALUES ('c-ghost','teardown','e2','h-ghost','pending')`)
	c, p := call("system_purge", doc{"preview": true}, owner)
	removed, kept := map[string][]string{}, map[string][]string{}
	for _, x := range anys(p["removed"]) {
		removed[astr(mapOf(x)["kind"])] = strs(mapOf(x)["ids"])
	}
	for _, x := range anys(p["kept"]) {
		kept[astr(mapOf(x)["kind"])] = strs(mapOf(x)["ids"])
	}
	reasons := map[string]string{}
	for _, x := range anys(p["kept"]) {
		for _, r := range anys(mapOf(x)["reasons"]) {
			reasons[astr(mapOf(r)["id"])] = astr(mapOf(r)["reason"])
		}
	}
	if reasons["x1"] != "score.trial_ids by score sc3" {
		t.Fatalf("kept reason of x1 = %q", reasons["x1"])
	}
	if c != 200 || astr(mapOf(anys(p["removed"])[0])["kind"]) != "host" || !slices.Equal(removed["project"], []string{"acc"}) || !slices.Equal(kept["experiment"], []string{"x1"}) || p["purged"] != false {
		t.Fatalf("purge preview = %d %v", c, p)
	}
	if c, _ := call("system_purge", doc{"preview": false}, map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}); c != 403 {
		t.Fatalf("a member purges: %d", c)
	}
	// A call past its time budget checks nothing more and says so.
	prev := purgeBudget
	t.Cleanup(func() { purgeBudget = prev })
	purgeBudget = 0
	if c, p := call("system_purge", doc{"preview": true, "kinds": []string{"project"}}, owner); c != 200 || p["more"] != true || !strings.HasPrefix(astr(mapOf(anys(mapOf(anys(p["kept"])[0])["reasons"])[0])["reason"]), "deferred") {
		t.Fatalf("budget = %d %v", c, p)
	}
	purgeBudget = prev
	c, purged := call("system_purge", doc{"preview": false}, owner)
	if c != 200 || purged["purged"] != true || num(purged["retryLater"]) != 0 {
		t.Fatalf("purge = %d %v", c, purged)
	}
	for _, tbl := range []string{"task", "execution", "question", "artifact", "session", "message", "event", "command", "execution_totals", "task_derived", "entity_code"} {
		if n := psqlLines(t, e, "SELECT count(*) FROM "+tbl+" WHERE project_id='acc'"); n[0] != "0" {
			t.Errorf("purged project rows left in %s: %v", tbl, n)
		}
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM command WHERE id='c-ghost'"); n[0] != "0" {
		t.Fatalf("the dead host's command stays")
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM definition WHERE kind='project' AND id='acc' AND purged_at IS NOT NULL AND deleted_at IS NOT NULL"); n[0] != "1" {
		t.Fatalf("the purged project's definition row must stay (its id is never reused)")
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM execution WHERE id IN ('r1','r1s')"); n[0] != "2" {
		t.Fatalf("a trial another experiment's score cites was purged")
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", "DELETE FROM score WHERE id='sc3'")
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
	if n := psqlLines(t, e, "SELECT count(*) FROM audit_log WHERE field IN ('deletedAt','purge')"); n[0] != "4" {
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

// Requirement: fleet-system-management.md, purge ("then their store objects through the artifact purge path ... a store
// object that fails to delete stays as a purge task"). Risk: an object is content-addressed (project/scope/hash), so
// deleting it after another artifact deduplicated onto it, between the purge and the object deletion, loses that
// artifact's bytes.
func TestPurgeTaskSkipsObjectsInUseAgain(t *testing.T) {
	e := openTestCoordinator(t, false)
	var mu sync.Mutex
	deleted := []string{}
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			mu.Lock()
			deleted = append(deleted, r.URL.Path)
			mu.Unlock()
		}
		w.WriteHeader(204)
	}))
	defer s3.Close()
	prev := storeSecretsDir
	storeSecretsDir = t.TempDir()
	defer func() { storeSecretsDir = prev }()
	if err := os.WriteFile(filepath.Join(storeSecretsDir, "st1.key"), []byte(`{"accessKeyId":"AKIAINVENTED","secretAccessKey":"invented"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO store(id,host_id,bucket,endpoint,state) VALUES ('st1','h-x','b','`+s3.URL+`','reachable');
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,state) VALUES ('a3','keep','prod','h3',1,'text/plain','n','stored');
INSERT INTO artifact_copy(artifact_id,store_id,project_id,state) VALUES ('a3','st1','keep','verified');
INSERT INTO purge_task(id,store_id,payload) VALUES ('pt1','st1','{"objects":[
 {"key":"keep/prod/h3","projectId":"keep","scope":"prod","hash":"h3"},{"key":"gone/prod/h4","projectId":"gone","scope":"prod","hash":"h4"},{"key":"gone/prod/.pending/a4.st1"}]}');`)
	left, err := runPurgeTasks(e.ctx, e.app, time.Now().Add(time.Minute))
	if err != nil || left != 0 {
		t.Fatalf("runPurgeTasks = %d, %v", left, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if !slices.Contains(deleted, "/b/gone/prod/h4") || !slices.Contains(deleted, "/b/gone/prod/.pending/a4.st1") || slices.ContainsFunc(deleted, func(p string) bool { return strings.Contains(p, "h3") }) {
		t.Fatalf("deleted %v: want the unused object and the staging key, never keep/prod/h3", deleted)
	}
	if n := psqlLines(t, e, "SELECT count(*) FROM purge_task"); n[0] != "0" {
		t.Fatalf("a finished purge task stays: %v", n)
	}
}

// Requirement: migration 67 (host calls that deliver and settle deleted work see deleted rows). Risk: a renamed
// executor route silently loses that visibility, so the stop of a deleted run is never delivered.
func TestDeletedHostRoutesAreRegistered(t *testing.T) {
	for path := range deletedHostRoutes {
		if !slices.ContainsFunc(routeTable, func(r registeredRoute) bool { return r.pattern == "POST "+path }) {
			t.Errorf("deletedHostRoutes names %s, which no route registers", path)
		}
	}
}

// Requirement: fleet-system-management.md, Archive, delete, purge (object_delete kinds task and session): a task takes
// its executions, questions and artifacts; a session its executions and messages; a live dependent refuses the delete
// with its reason. Risk: deleting a task another live task depends on, or a session whose runs belong to a live task or
// that a live session forked from, leaves live work pointing at nothing.
func TestDeleteTaskAndSession(t *testing.T) {
	e := openTestCoordinator(t, false)
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `
INSERT INTO definition(kind,id,path,head,project_id) VALUES ('project','keep','config/projects/keep/project.json',1,'keep');
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('project','keep',1,'{"settings":{"id":"keep","name":"Keep","contextFiles":[]},"contextFiles":[]}','y','editor','person','t');
INSERT INTO task(id,title,project_id,state) VALUES ('t1','old task','keep','completed'),('t2','live task','keep','queued'),('t3','needed task','keep','completed');
INSERT INTO task_dependency(task_id,depends_on_task_id,project_id) VALUES ('t2','t3','keep');
INSERT INTO session(id,project_id,task_id,source_session_id) VALUES ('s1','keep','t1',NULL), ('s2','keep',NULL,NULL), ('s3','keep',NULL,'s2');
INSERT INTO message(id,session_id,text,project_id) VALUES ('m1','s1','hi','keep');
INSERT INTO execution(id,kind,state,project_id,task_id,session_id,data) VALUES ('e1','agent_run','succeeded','keep','t1','s1','{"id":"e1","sessionId":"s1"}');
INSERT INTO question(id,kind,text,project_id,task_id,state) VALUES ('q1','operator_decision','decide','keep','t1','answered');
INSERT INTO artifact(id,project_id,scope,content_hash,size,media_type,name,execution_id,task_id,state) VALUES ('a1','keep','prod','h1',4,'text/plain','n','e1','t1','stored');`)
	call := func(body any) (int, map[string]any) {
		return e.callJSON(t, "POST", "/api/ops/object_delete", body, e.ownerHeaders())
	}
	if c, b := call(doc{"kind": "task", "id": "t3", "preview": true}); c != 409 || !strings.Contains(astr(b["message"]), "task_dependency.depends_on_task_id") {
		t.Fatalf("a task a live task depends on = %d %v", c, b)
	}
	if c, b := call(doc{"kind": "session", "id": "s1", "preview": true}); c != 409 || !strings.Contains(astr(b["message"]), "execution.task_id by task t1") {
		t.Fatalf("a session whose run belongs to a live task = %d %v", c, b)
	}
	if c, b := call(doc{"kind": "session", "id": "s2", "preview": true}); c != 409 || !strings.Contains(astr(b["message"]), "sourceSessionId") {
		t.Fatalf("a session a live session forked from = %d %v", c, b)
	}
	if c, b := call(doc{"kind": "task", "id": "t1", "preview": true}); c != 409 || !strings.Contains(astr(b["message"]), "open session s1") {
		t.Fatalf("a task an open session works for = %d %v", c, b)
	}
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", `UPDATE session SET archived_at=to_timestamp(0.001) WHERE id='s1'`)
	c, b := call(doc{"kind": "task", "id": "t1", "preview": false})
	if want := map[string]float64{"task": 1, "execution": 1, "question": 1, "artifact": 1, "artifact.bytes": 4}; c != 200 || !mapsEqual(kindCounts(b["affected"]), want) {
		t.Fatalf("task delete = %d %v", c, b)
	}
	c, b = call(doc{"kind": "session", "id": "s1", "preview": false})
	if c != 200 || !mapsEqual(kindCounts(b["affected"]), map[string]float64{"session": 1}) {
		t.Fatalf("session delete once its task went = %d %v", c, b)
	}
	if n := appCount(t, e, false, "SELECT (SELECT count(*) FROM session WHERE id='s1')+(SELECT count(*) FROM message WHERE id='m1')"); n != 0 {
		t.Fatalf("the deleted session or its message is still read: %d", n)
	}
	if n := appCount(t, e, false, "SELECT count(*) FROM task WHERE id IN ('t2','t3')"); n != 2 {
		t.Fatalf("live tasks hidden: %d", n)
	}
	c, p := e.callJSON(t, "POST", "/api/ops/system_purge", doc{"preview": false, "kinds": []string{"task", "session"}}, e.ownerHeaders())
	removed := map[string][]string{}
	for _, x := range anys(p["removed"]) {
		removed[astr(mapOf(x)["kind"])] = strs(mapOf(x)["ids"])
	}
	if c != 200 || !slices.Equal(removed["task"], []string{"t1"}) || !slices.Equal(removed["session"], []string{"s1"}) {
		t.Fatalf("purge = %d %v", c, p)
	}
	for _, sql := range []string{"SELECT count(*) FROM task WHERE id='t1'", "SELECT count(*) FROM execution WHERE id='e1'", "SELECT count(*) FROM question WHERE id='q1'",
		"SELECT count(*) FROM artifact WHERE id='a1'", "SELECT (SELECT count(*) FROM session WHERE id='s1')+(SELECT count(*) FROM message WHERE id='m1')"} {
		if n := psqlLines(t, e, sql); n[0] != "0" {
			t.Errorf("purge left %s = %v", sql, n)
		}
	}
}

// Requirement: fleet-definition-editors.md, Delete (definition_delete). Risk: a definition still in use, or one the
// repository ships, disappears (and comes back with the next release); or a deleted id is taken again.
func TestDefinitionDelete(t *testing.T) {
	e := openTestCoordinator(t, false)
	pipe := `{"id":"%s","steps":[{"id":"s","kind":"script","command":"true"}],"version":2}`
	runTestPSQL(t, e.binDir, e.sockDir, e.pgPort, e.dbName, "postgres", strings.NewReplacer("%a", fmt.Sprintf(pipe, "p-store"), "%b", fmt.Sprintf(pipe, "p-exp")).Replace(`
INSERT INTO definition(kind,id,path,head) VALUES ('pipeline','p-store','config/pipelines/p-store.json',1),('pipeline','p-exp','config/pipelines/p-exp.json',1);
INSERT INTO definition_revision(kind,id,revision,content,content_sha,source,actor_kind,message) VALUES
 ('pipeline','p-store',1,'%a','x','editor','person','t'),('pipeline','p-exp',1,'%b','y','editor','person','t');
INSERT INTO experiment(id,project_id,kind,pipeline_id,state) VALUES ('x1','keep','single','p-exp','draft');`))
	call := func(op string, body any) (int, map[string]any) {
		return e.callJSON(t, "POST", "/api/ops/"+op, body, e.ownerHeaders())
	}
	if c, b := call("definition_delete", doc{"kind": "pipeline", "id": "search-basic", "preview": true}); c != 200 || !strings.HasPrefix(astr(b["refused"]), "shipped by the repository") {
		t.Fatalf("a shipped pipeline = %d %v", c, b)
	}
	if c, b := call("definition_delete", doc{"kind": "pipeline", "id": "p-exp", "preview": false}); c != 409 || !strings.Contains(astr(b["message"]), "live Lab experiments run it: x1") {
		t.Fatalf("a pipeline a live experiment runs = %d %v", c, b)
	}
	if c, b := call("definition_delete", doc{"kind": "pipeline", "id": "p-store", "preview": true}); c != 200 || b["refused"] != nil || b["state"] != "store-only" || b["deleted"] != false {
		t.Fatalf("preview = %d %v", c, b)
	}
	if c, b := call("definition_delete", doc{"kind": "pipeline", "id": "p-store", "preview": false}); c != 200 || b["deleted"] != true {
		t.Fatalf("delete = %d %v", c, b)
	}
	if c, _ := call("definition_get", doc{"kind": "pipeline", "id": "p-store"}); c != 404 {
		t.Fatalf("a deleted definition is still read: %d", c)
	}
	if c, b := call("definition_list", doc{"kind": "pipeline"}); c != 200 || strings.Contains(js(b), `"p-store"`) {
		t.Fatalf("a deleted definition is still listed: %d", c)
	}
	if c, _ := call("definition_create", doc{"kind": "pipeline", "id": "p-store", "content": doc{"pipeline": doc{"id": "p-store", "version": 2, "steps": []any{doc{"id": "s", "kind": "script", "command": "true"}}}}, "message": "again"}); c != 409 {
		t.Fatalf("a deleted id was taken again: %d", c)
	}
	if c, _ := call("definition_delete", doc{"kind": "pipeline", "id": "p-store", "preview": true}); c != 404 {
		t.Fatalf("a second delete: %d", c)
	}
}
