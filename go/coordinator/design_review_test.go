package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleet/contract"
	"github.com/jackc/pgx/v5/pgconn"
)

// Requirement: a designer offers retained originals and only the owner chooses an active revision.
// A missing photo, concurrent replacement or retry must never turn a different option into an approval.
func TestDesignReviewOwnerRoundTrip(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	const project = "fleet"
	var task contract.TaskDetail
	e.opInto(t, "task_create", doc{"title": "Compare a design", "projectId": project, "completionRequirements": doc{"requireHumanAcceptance": true}}, e.ownerHeaders(), &task)
	e.opInto(t, "task_update", doc{"taskId": task.Id, "visualImpact": "unknown"}, e.ownerHeaders(), &task)
	if designImpactOf(task.CompletionRequirements) != "unknown" || !bytes.Contains(task.CompletionRequirements, []byte("requireHumanAcceptance")) {
		t.Fatalf("enrollment did not preserve the existing task requirements: %s", task.CompletionRequirements)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/task_update", doc{"taskId": task.Id, "visualImpact": "nonvisual"}, e.ownerHeaders()); code != 409 {
		t.Fatalf("design enrollment weakened: %d", code)
	}
	eid, sid := newID(), newID()
	e.registerHost(t, "design-host")
	exec("INSERT INTO session (id,project_id,task_id,directed_by) VALUES ($1,$2,$3,$4)", sid, project, task.Id, testOwnerEmail)
	payload := doc{"taskId": task.Id, "agentConfig": doc{"id": "codex-designer"}, "projectSnapshot": doc{"settings": doc{"repos": []doc{{"id": "fleet", "remote": "https://github.com/taskqorg/fleet.git"}}}}}
	exec("INSERT INTO execution (id,kind,state,host_id,project_id,session_id,task_id,harness,started_at,data,observation,config_ref) VALUES ($1,'agent_run','running','design-host',$2,$3,$4,'codex',now(),$5,'confirmed','{}')", eid, project, sid, task.Id, jsonBytes(payload))
	objects := map[string][]byte{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := objects[filepath.Base(r.URL.Path)]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	old := storeSecretsDir
	storeSecretsDir = t.TempDir()
	t.Cleanup(func() { storeSecretsDir = old })
	if err := os.WriteFile(filepath.Join(storeSecretsDir, "design-store.key"), []byte(`{"accessKeyId":"test","secretAccessKey":"test"}`), 0600); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO store (id,host_id,bucket,endpoint,state) VALUES ('design-store','design-host','fleet',$1,'reachable')", srv.URL)
	retain := func(mime string, b []byte) string {
		id := newID()
		hash := sha256hex(b)
		objects[hash] = b
		exec("INSERT INTO artifact (id,project_id,scope,content_hash,size,media_type,name,task_id,execution_id,state) VALUES ($1,$2,'prod',$3,$4,$5,$1,$6,$7,'stored')", id, project, hash, len(b), mime, task.Id, eid)
		exec("INSERT INTO artifact_copy (artifact_id,store_id,project_id,state) VALUES ($1,'design-store',$2,'verified')", id, project)
		return id
	}
	recipe := doc{"version": 1, "scene": "pages/history/events", "state": "default", "path": "/history", "landmark": "main", "clock": 123, "timeZone": "UTC", "locale": "en-US", "dpr": 1, "reducedMotion": true, "css": strings.Repeat("c", 64), "inputs": []doc{{"path": "source.tsx", "sha256": strings.Repeat("d", 64)}}}
	var matrix []doc
	for _, m := range designMatrix {
		matrix = append(matrix, doc{"viewport": m.viewport, "theme": m.theme, "width": m.width, "height": m.height})
	}
	recipe["matrix"] = matrix
	var binding designRecipe
	if err := json.Unmarshal(jsonBytes(recipe), &binding); err != nil {
		t.Fatal(err)
	}
	digest := binding.digest()
	recipe["digest"] = digest
	recipe["html"] = strings.Repeat("b", 64)
	c := contract.DesignReviewCase{ScopeKey: "pages/history/events:default", Recipe: digest, RecipeArtifactId: retain("application/json", jsonBytes(recipe))}
	for i, m := range designMatrix {
		img := image.NewPaletted(image.Rect(0, 0, m.width, m.height), color.Palette{color.Gray{Y: uint8(i)}})
		var b bytes.Buffer
		if err := png.Encode(&b, img); err != nil {
			t.Fatal(err)
		}
		c.Frames = append(c.Frames, contract.DesignReviewFrame{Viewport: m.viewport, Theme: m.theme, ArtifactId: retain("image/png", b.Bytes()), Sha256: sha256hex(b.Bytes()), Critique: "The event rows and filters are visible."})
	}
	// A publisher must wait for a first in-flight design offer even before a prior question exists.
	hold, err := e.ownerStore.pool.Begin(e.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Rollback(e.ctx)
	if _, err := hold.Exec(e.ctx, "SELECT 1 FROM task WHERE id=$1 FOR UPDATE", task.Id); err != nil {
		t.Fatal(err)
	}
	err = e.appStore.Write(e.ctx, &Request{app: e.app}, nil, func(q *Request) error {
		if _, err := q.tx.Exec(q.ctx(), "SET LOCAL lock_timeout='75ms'"); err != nil {
			return err
		}
		_, err := activeDesignOffer(q, task.Id)
		return err
	})
	var locked *pgconn.PgError
	if !errors.As(err, &locked) || locked.Code != "55P03" {
		t.Fatalf("admission did not wait for enrollment: %v", err)
	}
	if err := hold.Rollback(e.ctx); err != nil {
		t.Fatal(err)
	}
	err = e.appStore.Write(e.ctx, &Request{app: e.app}, nil, func(q *Request) error {
		_, err := claimDesignBuild(q, task.Id, newID(), "github.com/taskqorg/fleet")
		return err
	})
	var pending *designBuildWait
	if !errors.As(err, &pending) {
		t.Fatalf("writer admitted before the first offer: %v", err)
	}
	r := contract.DesignOfferRequest{Repository: "github.com/taskqorg/fleet", RequestId: "first", WalkArtifactId: retain("text/markdown", []byte("Observed task filter")), Options: []contract.DesignReviewOption{{Key: "A", Label: "Shared history", SourceArtifactId: retain("application/zip", []byte("test source object")), Cases: []contract.DesignReviewCase{c}}}}
	var offer contract.DesignOfferReply
	submit := func() error {
		return e.appStore.Write(e.ctx, &Request{app: e.app, person: e.app.peopleSnapshot().person(testOwnerEmail), agent: "execution:" + eid}, nil, func(q *Request) error { var err error; offer, err = createDesignOffer(q, &r); return err })
	}
	if err := submit(); err != nil {
		t.Fatal(err)
	}
	id := offer.QuestionId
	if err := submit(); err != nil || offer.QuestionId != id {
		t.Fatalf("retry: %v %s", err, offer.QuestionId)
	}
	r.RequestId = "second"
	r.Replaces = &id
	if err := submit(); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/questions_answer", doc{"questionId": id, "answer": "A"}, e.ownerHeaders()); code != 409 {
		t.Fatalf("old question %d", code)
	}
	id = offer.QuestionId
	bad := func(actor *Person, agent, answer string) error {
		return e.appStore.Write(e.ctx, &Request{app: e.app, person: actor, agent: agent}, nil, func(q *Request) error { _, err := answerQuestion(q, id, answer, "bot", nil); return err })
	}
	if err := bad(e.app.peopleSnapshot().person(testOwnerEmail), "execution:"+eid, "A"); err == nil {
		t.Fatal("designer selected an option")
	}
	if err := bad(&Person{Email: "site-agents", Owner: true}, "", "A"); err == nil {
		t.Fatal("machine selected an option")
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/questions_answer", doc{"questionId": id, "answer": "accepted"}, e.ownerHeaders()); code != 400 {
		t.Fatalf("task acceptance %d", code)
	}
	builder := newID()
	exec("INSERT INTO execution (id,kind,state,project_id,task_id,started_at,data,observation,config_ref) VALUES ($1,'host_job','running',$2,$3,now(),'{}','confirmed','{}')", builder, project, task.Id)
	write := func(fn func(q *Request) error) error {
		return e.appStore.Write(e.ctx, &Request{app: e.app, person: e.app.peopleSnapshot().person(testOwnerEmail)}, nil, fn)
	}
	claim := func(id string) ([]contract.InputArtifact, error) {
		var files []contract.InputArtifact
		err := write(func(q *Request) error {
			var err error
			files, err = claimDesignBuild(q, task.Id, id, r.Repository)
			return err
		})
		return files, err
	}
	if _, err := claim(builder); err == nil {
		t.Fatal("writer started before selection")
	}
	if code, body := e.callJSON(t, "POST", "/api/ops/questions_answer", doc{"questionId": id, "answer": "A"}, e.ownerHeaders()); code != 200 {
		t.Fatalf("owner choice %d %v", code, body)
	}
	var review contract.DesignReviewGetReply
	e.opInto(t, "design_review_get", doc{"taskId": task.Id}, e.ownerHeaders(), &review)
	if review.State != "selected" || deref(review.Selected) != "A" || deref(review.Owner) != testOwnerEmail || review.Digest != offer.Digest {
		t.Fatal("different selection")
	}
	files, err := claim(builder)
	if err != nil || len(files) != 2 || files[0].ArtifactId != r.Options[0].SourceArtifactId || files[1].ArtifactId != c.RecipeArtifactId {
		t.Fatalf("build inputs: %v %v", files, err)
	}
	if _, err := claim(newID()); err == nil {
		t.Fatal("two simultaneous writers")
	}
	if err := write(func(q *Request) error { return checkDesignPublication(q, task.Id, builder, r.Repository) }); err != nil {
		t.Fatal(err)
	}
	if err := write(func(q *Request) error { return checkDesignPublication(q, task.Id, eid, r.Repository) }); err == nil {
		t.Fatal("designer published")
	}
	exec("UPDATE artifact SET state='failed' WHERE id=$1", c.Frames[0].ArtifactId)
	if code, _ := e.callJSON(t, "POST", "/api/ops/design_review_get", doc{"taskId": task.Id}, e.ownerHeaders()); code != 409 {
		t.Fatalf("missing evidence %d", code)
	}
}

func TestDesignOriginalDimensionsAndRecipe(t *testing.T) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewGray(image.Rect(0, 0, 390, 844))); err != nil {
		t.Fatal(err)
	}
	if checkDesignPNG(buf.Bytes(), designMatrix[1]) != nil || checkDesignPNG(buf.Bytes(), designMatrix[0]) == nil || checkDesignPNG(buf.Bytes()[:48], designMatrix[1]) == nil {
		t.Fatal("wrong or truncated matrix PNG")
	}
	var req contract.DesignOfferRequest
	_ = json.Unmarshal([]byte(`{"repository":"repo","requestId":"r","options":[]}`), &req)
	if designEvidence(&req, nil) == nil {
		t.Fatal("empty options")
	}
}

// The dispatcher gives the Fleet design stage a writable draft and no forge publication path.
func TestDesignStageDoesNotPublish(t *testing.T) {
	run := dispatchExecution{AgentConfig: jsonBytes(doc{"id": designProfileID}), Dispatch: &dispatchSpec{ProjectID: ptr("fleet")}}
	if publishes(&run) {
		t.Fatal("Fleet designer received publishing access")
	}
	run.AgentConfig = jsonBytes(doc{"id": "coding-worker"})
	if !publishes(&run) {
		t.Fatal("build profile lost its existing access")
	}
	ps, _, err := presetsOf(nil, "fleet")
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID == "design" && (p.Role != "designer" || p.Profile != "" || p.Branch) {
			t.Fatalf("unexpected preview preset: %+v", p)
		}
	}
	project := rawObject{
		"repos": jsonBytes([]doc{
			{"id": "main", "remote": "https://github.com/taskqorg/fleet.git", "connection": "invented-secret"},
			{"id": "docs", "remote": "https://github.com/taskqorg/specq.git", "connection": "invented-context-connection"},
			{"id": "other", "remote": "https://github.com/taskqorg/other.git"},
		}),
		"environments": jsonBytes([]doc{
			{"hostId": "preview-host", "repo": "docs", "checkoutPath": "/invented/docs"},
			{"hostId": "preview-host", "repo": "main", "checkoutPath": "/invented/fleet"},
			{"hostId": "preview-host", "repo": "other", "checkoutPath": "/invented/other"},
		}),
		"contextSources": jsonBytes([]contract.ContextSource{{
			Repo: "docs", Revision: strings.Repeat("a", 40),
			Core: []contract.ContextSourceFile{{Path: "rules.md", Sha256: strings.Repeat("b", 64)}},
		}}),
		"execution": jsonBytes(doc{"forge": true, "tools": []string{"browser"}}),
		"worktree":  jsonBytes(doc{"linkPaths": []string{"shared-path"}}),
	}
	start := &contract.StartPayload{Harness: ptr("codex"), Project: project, WorkingDirectory: ptr("/invented/override")}
	profile := rawObject{"settings": jsonBytes(doc{"sandbox": false})}
	if err := restrictFleetDesignStart(start, profile, "preview-host"); err != nil {
		t.Fatal(err)
	}
	var parsed contract.ProjectContext
	if err := json.Unmarshal(jsonBytes(start.Project), &parsed); err != nil {
		t.Fatal(err)
	}
	if start.WorkingDirectory != nil || start.Branch == nil || *start.Branch || isTrue(start.GitNetwork) ||
		parsed.Execution == nil || isTrue(parsed.Execution.Forge) || len(parsed.Execution.Tools) != 1 ||
		len(parsed.Environments) != 2 || deref(parsed.Environments[0].CheckoutPath) != "/invented/fleet" ||
		len(parsed.Repos) != 2 || parsed.Repos[0].Connection != nil || len(parsed.Worktree.LinkPaths) != 0 ||
		!bytes.Equal(profile["settings"], []byte(`{"sandbox":true}`)) {
		t.Fatalf("preview start has incorrect bindings: %d repositories, %d checkouts", len(parsed.Repos), len(parsed.Environments))
	}
	if deref(parsed.Repos[1].Id) != "docs" || parsed.Repos[1].Connection != nil ||
		deref(parsed.Environments[1].Repo) != "docs" || deref(parsed.Environments[1].CheckoutPath) != "/invented/docs" ||
		len(parsed.ContextSources) != 1 {
		t.Fatal("the executor needs the pinned context repository and local host binding without a Git connection")
	}
}
