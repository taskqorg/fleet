package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Requirement: publication promotes verified project files once and reconciles durable effects.
// Risk: replay overwrites a later publication, unsafe links escape the destination,
// or failed/cancelled verification replaces the previously accepted site.
func TestStaticPublicationSafetyAndRecovery(t *testing.T) {
	fixture := func(t *testing.T) (publicationDestination, publicationManifest) {
		t.Helper()
		base := t.TempDir()
		base, _ = filepath.EvalSymlinks(base)
		old := filepath.Join(base, "releases", "previous")
		if e := os.MkdirAll(old, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(old, "index.html"), []byte("previous"), 0600); e != nil {
			t.Fatal(e)
		}
		if e := os.Symlink(old, filepath.Join(base, "current")); e != nil {
			t.Fatal(e)
		}
		f := publicationFile{Name: "index.html", ArtifactID: "source", SHA256: sha256hex([]byte("published")), Size: 9}
		m := publicationManifest{Version: "fleet-0123456789abcdef0123456789abcdef", RunID: "run", ProjectID: "hello-world", DefinitionSHA256: sha256hex([]byte("definition")), Files: []publicationFile{f}}
		m.InputSHA256 = publicationInputHash(m.Files)
		return publicationDestination{Root: base, URL: "https://fleet.ufobe.com/test-sites/hello-world/index.html", Authority: "stale-program-authority"}, m
	}
	source := func(publicationFile) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("published")), nil }
	read := func(d publicationDestination) func(publicationFile) (io.ReadCloser, error) {
		return func(f publicationFile) (io.ReadCloser, error) {
			return os.Open(filepath.Join(d.Root, "current", f.Name))
		}
	}
	t.Run("success-restart-superseded", func(t *testing.T) {
		d, m := fixture(t)
		opens := 0
		open := func(f publicationFile) (io.ReadCloser, error) { opens++; return source(f) }
		out, e := publishStatic(context.Background(), d, m, open, read(d))
		if e != nil || out["verified"] != true || out["executorService"] != "coordinator" {
			t.Fatal(out, e)
		}
		if _, e = publishStatic(context.Background(), d, m, open, read(d)); e != nil || opens != 1 {
			t.Fatal("replay wrote source", opens, e)
		}
		os.Remove(filepath.Join(d.Root, "current"))
		os.Symlink("releases/another-version", filepath.Join(d.Root, "current"))
		if _, e = publishStatic(context.Background(), d, m, open, read(d)); e == nil || opens != 1 {
			t.Fatal("superseded replay", opens, e)
		}
	})
	t.Run("failed-readback-restores-and-never-republishes", func(t *testing.T) {
		d, m := fixture(t)
		previous, _ := os.Readlink(filepath.Join(d.Root, "current"))
		_, e := publishStatic(context.Background(), d, m, source, func(publicationFile) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("bad")), nil })
		current, _ := os.Readlink(filepath.Join(d.Root, "current"))
		if e == nil || current != previous {
			t.Fatal("failed readback changed site", e, current)
		}
		if _, e = publishStatic(context.Background(), d, m, source, read(d)); e == nil {
			t.Fatal("failed attempt replayed")
		}
	})
	t.Run("cancel-after-promotion-restores", func(t *testing.T) {
		d, m := fixture(t)
		previous, _ := os.Readlink(filepath.Join(d.Root, "current"))
		ctx, cancel := context.WithCancel(context.Background())
		_, e := publishStatic(ctx, d, m, source, func(publicationFile) (io.ReadCloser, error) { cancel(); return nil, ctx.Err() })
		current, _ := os.Readlink(filepath.Join(d.Root, "current"))
		if e == nil || current != previous {
			t.Fatal(e, current)
		}
	})
	t.Run("failed-readback-never-restores-over-later-publication", func(t *testing.T) {
		d, m := fixture(t)
		later := "releases/later"
		os.Mkdir(filepath.Join(d.Root, later), 0700)
		_, e := publishStatic(context.Background(), d, m, source, func(publicationFile) (io.ReadCloser, error) {
			os.Remove(filepath.Join(d.Root, "current"))
			os.Symlink(later, filepath.Join(d.Root, "current"))
			return nil, errors.New("readback failed")
		})
		current, _ := os.Readlink(filepath.Join(d.Root, "current"))
		if e == nil || current != later {
			t.Fatal("later publication overwritten", e, current)
		}
	})
	t.Run("root-ancestor-symlink-rejected", func(t *testing.T) {
		d, m := fixture(t)
		alias := filepath.Join(filepath.Dir(d.Root), "alias")
		os.Symlink(d.Root, alias)
		d.Root = alias
		if _, e := publishStatic(context.Background(), d, m, source, read(d)); e == nil {
			t.Fatal("indirect root accepted")
		}
	})
	t.Run("staged-next-pointer-reconciles", func(t *testing.T) {
		d, m := fixture(t)
		previous, _ := os.Readlink(filepath.Join(d.Root, "current"))
		opens := 0
		open := func(f publicationFile) (io.ReadCloser, error) { opens++; return source(f) }
		if _, e := publishStatic(context.Background(), d, m, open, read(d)); e != nil {
			t.Fatal(e)
		}
		os.Remove(filepath.Join(d.Root, "current"))
		os.Symlink(previous, filepath.Join(d.Root, "current"))
		os.Symlink("releases/"+m.Version, filepath.Join(d.Root, ".next-"+m.Version))
		if _, e := publishStatic(context.Background(), d, m, open, read(d)); e != nil || opens != 1 {
			t.Fatal("staged replay", opens, e)
		}
	})
	t.Run("rejection-directory-sync-failure-no-restore", func(t *testing.T) {
		d, m := fixture(t)
		previous, _ := os.Readlink(filepath.Join(d.Root, "current"))
		if _, e := publishStatic(context.Background(), d, m, source, read(d)); e != nil {
			t.Fatal(e)
		}
		root, e := publicationRoot(d.Root)
		if e != nil {
			t.Fatal(e)
		}
		defer root.Close()
		rel := "releases/" + m.Version
		e = publicationReject(root, rel, previous, m.Version, func() error {
			if _, e := root.Lstat(rel + "/failed"); e != nil {
				t.Fatal("rejection not written before barrier", e)
			}
			current, _ := root.Readlink("current")
			if current != rel {
				t.Fatal("restored before directory barrier")
			}
			return errors.New("injected directory sync failure")
		})
		current, _ := root.Readlink("current")
		if e == nil || !strings.Contains(e.Error(), "unknown") || current != rel {
			t.Fatal("failed durability claimed/restored", e, current)
		}
		e = publicationReconcileRejection(root, rel, previous, m.Version, func() error { return errors.New("injected replay directory sync failure") })
		current, _ = root.Readlink("current")
		if e == nil || !strings.Contains(e.Error(), "unknown") || current != rel {
			t.Fatal("replay bypassed rejection barrier", e, current)
		}
		opens := 0
		if _, e = publishStatic(context.Background(), d, m, func(f publicationFile) (io.ReadCloser, error) { opens++; return source(f) }, read(d)); e == nil || opens != 0 {
			t.Fatal("rejected attempt republished", e, opens)
		}
		current, _ = root.Readlink("current")
		if current != previous {
			t.Fatal("restart did not reconcile known rejection", current)
		}
	})
	for _, kind := range []string{"cancel", "integrity", "source-error", "traversal"} {
		t.Run(kind, func(t *testing.T) {
			d, m := fixture(t)
			previous, _ := os.Readlink(filepath.Join(d.Root, "current"))
			ctx := context.Background()
			open := source
			switch kind {
			case "cancel":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "integrity":
				open = func(publicationFile) (io.ReadCloser, error) { return io.NopCloser(strings.NewReader("wrong")), nil }
			case "source-error":
				open = func(publicationFile) (io.ReadCloser, error) { return nil, errors.New("unavailable") }
			case "traversal":
				m.Files[0].Name = "../index.html"
				m.InputSHA256 = publicationInputHash(m.Files)
			}
			if _, e := publishStatic(ctx, d, m, open, read(d)); e == nil {
				t.Fatal("accepted unsafe source")
			}
			current, _ := os.Readlink(filepath.Join(d.Root, "current"))
			if current != previous {
				t.Fatal("previous site changed")
			}
		})
	}
	for _, kind := range []string{"symlink", "hardlink", "manifest-conflict", "incomplete-staging"} {
		t.Run(kind, func(t *testing.T) {
			d, m := fixture(t)
			if kind == "incomplete-staging" {
				os.Mkdir(filepath.Join(d.Root, "releases", m.Version), 0700)
			} else {
				if _, e := publishStatic(context.Background(), d, m, source, read(d)); e != nil {
					t.Fatal(e)
				}
				name := filepath.Join(d.Root, "releases", m.Version, "index.html")
				os.Remove(name)
				switch kind {
				case "symlink":
					os.Symlink(filepath.Join(d.Root, "releases/previous/index.html"), name)
				case "hardlink":
					os.Link(filepath.Join(d.Root, "releases/previous/index.html"), name)
				case "manifest-conflict":
					m.RunID = "different"
				}
			}
			if _, e := publishStatic(context.Background(), d, m, source, read(d)); e == nil {
				t.Fatal("unsafe recovery accepted")
			}
		})
	}
}

// Requirement: destination is a configured protected Fleet project, never a caller path/URL.
// Risk: a pipeline obtains coordinator filesystem authority outside its project binding.
func TestStaticPublicationDestination(t *testing.T) {
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	root := filepath.Join(home, "sites")
	os.MkdirAll(filepath.Join(root, "hello-world"), 0700)
	os.MkdirAll(filepath.Join(home, ".config/fleet"), 0700)
	t.Setenv("HOME", home)
	t.Setenv("FLEET_TEST_SITE_ROOT", root)
	t.Setenv("PORT", "80")
	cfg := doc{"hostId": "publication-authority", "projects": doc{"hello-world": doc{"publicationRoot": filepath.Join(root, "hello-world"), "publicUrl": "https://fleet.ufobe.com/test-sites/hello-world/index.html", "checkUrl": "http://127.0.0.1/test-sites/hello-world/index.html"}}}
	path := filepath.Join(home, ".config/fleet/program-worker.prototype.json")
	os.WriteFile(path, jsonBytes(cfg), 0600)
	if _, e := publicationTarget("hello-world"); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PORT", "9470")
	binding := mapOf(mapOf(cfg["projects"])["hello-world"])
	binding["checkUrl"] = "http://127.0.0.1:9470/test-sites/hello-world/index.html"
	os.WriteFile(path, jsonBytes(cfg), 0600)
	if _, e := publicationTarget("hello-world"); e != nil {
		t.Fatal("configured coordinator-port route rejected", e)
	}
	binding["checkUrl"] = "http://127.0.0.1/test-sites/hello-world/index.html"
	os.WriteFile(path, jsonBytes(cfg), 0600)
	if _, e := publicationTarget("hello-world"); e == nil {
		t.Fatal("port80 binding silently rerouted to different service")
	}
	binding["checkUrl"] = "http://127.0.0.1:9471/test-sites/hello-world/index.html"
	os.WriteFile(path, jsonBytes(cfg), 0600)
	if _, e := publicationTarget("hello-world"); e == nil {
		t.Fatal("different-service route accepted")
	}
	binding["checkUrl"] = "http://127.0.0.1:9470/test-sites/hello-world/index.html"
	os.WriteFile(path, jsonBytes(cfg), 0600)
	if _, e := publicationTarget("another-project"); e == nil {
		t.Fatal("arbitrary project accepted")
	}
	mapOf(mapOf(cfg["projects"])["hello-world"])["publicationRoot"] = home
	os.WriteFile(path, jsonBytes(cfg), 0600)
	if _, e := publicationTarget("hello-world"); e == nil {
		t.Fatal("arbitrary destination accepted")
	}
}

// Requirement: the ordinary HTTP pipeline tool publishes within current grants,
// records its actual service and counts only the program leaf in totals.
// Risk: local publication bypasses RLS, masquerades as the stale host, loses
// lineage or reports success from stored metadata without protected HTTP bytes.
func TestStaticPublicationHTTPPipeline(t *testing.T) {
	env := openTestCoordinator(t, false)
	home := t.TempDir()
	home, _ = filepath.EvalSymlinks(home)
	root := filepath.Join(home, "sites")
	os.MkdirAll(filepath.Join(root, "hello-world"), 0700)
	os.MkdirAll(filepath.Join(home, ".config/fleet"), 0700)
	t.Setenv("HOME", home)
	t.Setenv("FLEET_TEST_SITE_ROOT", root)
	t.Setenv("PORT", strings.TrimPrefix(env.server.URL, "http://127.0.0.1:"))
	cfg := doc{"hostId": "pub-authority", "projects": doc{"hello-world": doc{"publicationRoot": filepath.Join(root, "hello-world"), "publicUrl": "https://fleet.ufobe.com/test-sites/hello-world/index.html", "checkUrl": env.server.URL + "/test-sites/hello-world/index.html"}}}
	os.WriteFile(filepath.Join(home, ".config/fleet/program-worker.prototype.json"), jsonBytes(cfg), 0600)
	objects := map[string][]byte{}
	var mu sync.Mutex
	s3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			http.Error(w, "unsigned", 403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "PUT" {
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			return
		}
		b, ok := objects[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(b)))
		if r.Method != "HEAD" {
			w.Write(b)
		}
	}))
	defer s3.Close()
	previousSecrets, previousLocations := storeSecretsDir, defaultLocations
	storeSecretsDir = t.TempDir()
	defaultLocations = []string{"pub-store-host"}
	defer func() { storeSecretsDir = previousSecrets; defaultLocations = previousLocations }()
	os.WriteFile(storeKeyFile("pub-store"), jsonBytes(storeKey{AccessKeyID: "test-key", SecretAccessKey: "test-secret"}), 0600)
	code, out := env.callJSON(t, "POST", "/api/ops/hosts_register", doc{"id": "pub-authority", "roles": []string{"publication"}}, env.ownerHeaders())
	if code != 200 {
		t.Fatal(code, out)
	}
	if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		return exec(q, `INSERT INTO store(id,host_id,kind,bucket,endpoint,state,observed_at,updated_at) VALUES('pub-store','pub-store-host','s3','fleet',$1,'reachable',now(),now())`, s3.URL)
	}); err != nil {
		t.Fatal(err)
	}
	body := doc{"requestId": "publication-http-fixture", "projectId": "hello-world", "example": doc{"inputs": []any{doc{"path": "index.html", "text": "<h1>publication verified</h1>"}}}}
	code, started := env.callJSON(t, "POST", "/api/pipelines/publish-static/runs", body, env.ownerHeaders())
	if code != 200 {
		t.Fatal(code, started)
	}
	runID := astr(started["id"])
	runWG.Wait()
	code, finished := env.callJSON(t, "GET", "/api/pipeline-runs/"+runID, nil, env.ownerHeaders())
	if code != 200 || finished["state"] != "succeeded" {
		t.Fatal(code, finished)
	}
	step := mapOf(anys(finished["steps"])[0])
	job := astr(step["executionId"])
	if step["executorService"] != "coordinator" || mapOf(step["output"])["verified"] != true {
		t.Fatal(step)
	}
	code, repeated := env.callJSON(t, "POST", "/api/pipelines/publish-static/runs", body, env.ownerHeaders())
	runWG.Wait()
	if code != 200 || repeated["id"] != runID {
		t.Fatal("idempotency", code, repeated)
	}
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		row, e := first(q, `SELECT host_id,kind::text,state::text,parent_execution_id,config_ref,pipeline_id,step_id,data->>'traceparent' AS trace FROM execution WHERE id=$1`, job)
		if e != nil {
			return e
		}
		if row["host_id"] != nil || row["kind"] != "host_job" || row["state"] != "succeeded" || row["parent_execution_id"] != stepRow(finished, 0) || mapOf(row["config_ref"])["executorService"] != "coordinator" || !validTraceparent(row["trace"]) {
			t.Fatal("false attribution", row)
		}
		var n int
		var wall *int64
		e = q.tx.QueryRow(q.ctx(), `SELECT count(*),max(wall_ms) FROM execution_totals WHERE execution_id IN ($1,$2,$3)`, runID, stepRow(finished, 0), job).Scan(&n, &wall)
		if e != nil {
			return e
		}
		if n != 1 || wall == nil {
			t.Fatal("doublecount/missing leaf", n, wall)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Reject the terminal database commit after the filesystem effect. Restart
	// reconciliation must use the durable manifest rather than publish again.
	if _, e := env.ownerStore.pool.Exec(env.ctx, `CREATE FUNCTION publication_commit_fixture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.kind='host_job' AND NEW.state='succeeded' THEN RAISE EXCEPTION 'publication fixture commit failure'; END IF; RETURN NEW; END $$; CREATE CONSTRAINT TRIGGER publication_commit_fixture AFTER UPDATE ON execution DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION publication_commit_fixture()`); e != nil {
		t.Fatal(e)
	}
	recoveryBody := doc{"requestId": "publication-commit-recovery", "projectId": "hello-world", "example": doc{"inputs": []any{doc{"path": "index.html", "text": "<h1>recovered publication</h1>"}}}}
	code, recovering := env.callJSON(t, "POST", "/api/pipelines/publish-static/runs", recoveryBody, env.ownerHeaders())
	if code != 200 {
		t.Fatal(code, recovering)
	}
	runWG.Wait()
	selected, _ := os.Readlink(filepath.Join(root, "hello-world/current"))
	code, interrupted := env.callJSON(t, "GET", "/api/pipeline-runs/"+astr(recovering["id"]), nil, env.ownerHeaders())
	if code != 200 || interrupted["state"] != "running" {
		t.Fatal("fixture did not roll back terminal state", code, interrupted)
	}
	if _, e := env.ownerStore.pool.Exec(env.ctx, "DROP TRIGGER publication_commit_fixture ON execution; DROP FUNCTION publication_commit_fixture()"); e != nil {
		t.Fatal(e)
	}
	pipelineAdvance(env.app, astr(recovering["id"]))
	runWG.Wait()
	code, recovered := env.callJSON(t, "GET", "/api/pipeline-runs/"+astr(recovering["id"]), nil, env.ownerHeaders())
	after, _ := os.Readlink(filepath.Join(root, "hello-world/current"))
	if code != 200 || recovered["state"] != "succeeded" || selected != after {
		t.Fatal("effect repeated/lost after commit rollback", code, recovered, selected, after)
	}
	for _, state := range []string{"foreign", "pending", "unverified"} {
		var id string
		if e := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
			project := "hello-world"
			if state == "foreign" {
				project = "hello-world-alt"
			}
			var e error
			id, e = artifactSave(q, project, "prod", runID, "candidate.html", []byte("candidate"), "text/html")
			if e != nil {
				return e
			}
			if state == "pending" {
				return exec(q, "UPDATE artifact SET state='pending' WHERE id=$1", id)
			}
			if state == "unverified" {
				return exec(q, "UPDATE artifact_copy SET state='pending' WHERE artifact_id=$1", id)
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		var bad doc
		jsonRoundTrip(finished, &bad)
		bad["id"] = newID()
		bad["state"] = "running"
		bad["example"] = doc{}
		bad["exampleInputs"] = []any{doc{"artifactId": id, "path": "example/index.html"}}
		bad["steps"] = []any{doc{"id": "publish", "kind": "publication", "state": "preparing"}}
		previous, _ := os.Readlink(filepath.Join(root, "hello-world/current"))
		_, e := runPublication(env.ctx, env.app, bad, 0, mapOf(anys(mapOf(bad["definition"])["steps"])[0]))
		if e != nil {
			t.Fatal(e)
		}
		current, _ := os.Readlink(filepath.Join(root, "hello-world/current"))
		if bad["state"] != "failed" || current != previous {
			t.Fatal("unsafe source published", state, bad["state"], current)
		}
	}
	// A retained run cannot borrow the owner after its requester loses access.
	var people []doc
	raw, e := os.ReadFile(env.app.peopleFile)
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &people); e != nil {
		t.Fatal(e)
	}
	for _, p := range people {
		if p["email"] == "member@trial.test" {
			p["grants"] = []any{doc{"kind": "project", "id": "hello-world"}}
		}
	}
	code, _ = env.callJSON(t, "POST", "/api/ops/people_set", doc{"people": people}, env.ownerHeaders())
	if code != 200 {
		t.Fatal("grant fixture", code)
	}
	var revoked doc
	if e = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		q.person = &Person{Email: "member@trial.test"}
		var e error
		revoked, e = pipelineStart(q, doc{"file": "publish-static.json", "projectId": "hello-world", "requestId": "revoked", "example": body["example"]})
		return e
	}); e != nil {
		t.Fatal(e)
	}
	for _, p := range people {
		if p["email"] == "member@trial.test" {
			p["grants"] = []any{}
		}
	}
	code, _ = env.callJSON(t, "POST", "/api/ops/people_set", doc{"people": people}, env.ownerHeaders())
	if code != 200 {
		t.Fatal("revoke fixture", code)
	}
	before, _ := os.Readlink(filepath.Join(root, "hello-world/current"))
	if _, e = runPublication(env.ctx, env.app, revoked, 0, mapOf(anys(mapOf(revoked["definition"])["steps"])[0])); e != nil {
		t.Fatal(e)
	}
	current, _ := os.Readlink(filepath.Join(root, "hello-world/current"))
	if revoked["state"] != "failed" || before != current {
		t.Fatal("revoked requester published", revoked["state"])
	}
	code, _ = env.callJSON(t, "GET", "/test-sites/hello-world/index.html", nil, map[string]string{"Authorization": "Bearer " + env.browserSecret, "X-Fleet-Person": "outsider@trial.test"})
	if code != 404 {
		t.Fatal("ungranted protected site", code)
	}
	code, _ = env.callJSON(t, "POST", "/api/pipelines/publish-static/runs", doc{"requestId": "outsider", "projectId": "hello-world", "example": body["example"]}, map[string]string{"Authorization": "Bearer " + env.browserSecret, "X-Fleet-Person": "outsider@trial.test"})
	if code != 404 && code != 403 {
		t.Fatal("ungranted publication", code)
	}
}

// Requirement: a pipeline called by an execution inherits only server-owned
// task/session/project lineage; callers cannot select another project's parent.
// Risk: publication work is absent from task statistics or attributed elsewhere.
func TestPublicationAgentPipelineLineage(t *testing.T) {
	env := openTestCoordinator(t, false)
	code, task := env.callJSON(t, "POST", "/api/ops/task_create", doc{"projectId": "hello-world", "title": "publication lineage fixture"}, env.ownerHeaders())
	if code != 200 {
		t.Fatal(code, task)
	}
	var run doc
	err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		if e := put(q, "session", doc{"id": "publication-caller-session", "projectId": "hello-world", "directedBy": "owner@trial.test"}); e != nil {
			return e
		}
		if _, e := insertExecution(q, doc{"id": "publication-caller", "kind": "agent_run", "state": "running", "projectId": "hello-world", "taskId": task["id"], "sessionId": "publication-caller-session", "directedBy": "owner@trial.test"}); e != nil {
			return e
		}
		q.person = &Person{Email: "owner@trial.test", Owner: true}
		q.agent = "execution:publication-caller"
		var e error
		run, e = pipelineStart(q, doc{"file": "publish-static.json", "projectId": "hello-world", "requestId": "agent-call"})
		return e
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		rs, e := queryRows(q, `SELECT id,task_id,session_id,parent_execution_id FROM execution WHERE id=$1 OR parent_execution_id=$1 ORDER BY kind`, run["id"])
		if e != nil {
			return e
		}
		if len(rs) != 2 {
			t.Fatal(rs)
		}
		for _, r := range rs {
			if r["task_id"] != task["id"] || r["session_id"] != "publication-caller-session" {
				t.Fatal("lineage lost", r)
			}
		}
		parent, e := first(q, `SELECT parent_execution_id,data->>'traceparent' AS trace FROM execution WHERE id=$1`, run["id"])
		if e != nil {
			return e
		}
		if parent["parent_execution_id"] != "publication-caller" || !validTraceparent(parent["trace"]) {
			t.Fatal(parent)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
		q.person = &Person{Email: "owner@trial.test", Owner: true}
		q.agent = "execution:publication-caller"
		_, e := pipelineStart(q, doc{"file": "publish-static.json", "projectId": "hello-world-alt", "requestId": "foreign-call"})
		return e
	})
	if err == nil {
		t.Fatal("cross-project caller accepted")
	}
	code, foreignTask := env.callJSON(t, "POST", "/api/ops/task_create", doc{"projectId": "hello-world-alt", "title": "foreign fixture"}, env.ownerHeaders())
	if code != 200 {
		t.Fatal(code, foreignTask)
	}
	for _, foreign := range []string{"task", "session"} {
		err = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
			if foreign == "task" {
				if e := exec(q, "UPDATE execution SET task_id=$2 WHERE id=$1", "publication-caller", foreignTask["id"]); e != nil {
					return e
				}
			} else {
				if e := exec(q, "UPDATE execution SET task_id=$2 WHERE id=$1", "publication-caller", task["id"]); e != nil {
					return e
				}
				if e := put(q, "session", doc{"id": "publication-caller-session", "projectId": "hello-world-alt", "directedBy": "owner@trial.test"}); e != nil {
					return e
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		err = runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
			q.person = &Person{Email: "owner@trial.test", Owner: true}
			q.agent = "execution:publication-caller"
			_, e := pipelineStart(q, doc{"file": "publish-static.json", "projectId": "hello-world", "requestId": "foreign-" + foreign})
			return e
		})
		if err == nil {
			t.Fatal("foreign ownership accepted", foreign)
		}
	}

}
