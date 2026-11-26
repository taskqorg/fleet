package main

import (
	"context"
	"encoding/json"
	"testing"

	"fleet/contract"
)

// pipeline_run inputs: each path is a distinct relative file path outside Fleet's reserved folders, checked before any
// artifact read.
func TestPipelineRunInputPaths(t *testing.T) {
	for _, bad := range [][]contract.PipelineRunInput{
		{{Path: "../passport.jpg", ArtifactId: new("a")}},
		{{Path: "/etc/passwd", ArtifactId: new("a")}},
		{{Path: "__fleet_control/pipeline.json", ArtifactId: new("a")}},
		{{Path: "", ArtifactId: new("a")}},
		{{Path: "a.jpg", ArtifactId: new("a")}, {Path: "a.jpg", ArtifactId: new("b")}},
	} {
		if _, _, err := pipelineRunInputs(&Request{}, "dam", bad); err == nil {
			t.Fatalf("inputs %v must be refused", bad)
		}
	}
	if out, refs, err := pipelineRunInputs(&Request{}, "dam", nil); err != nil || len(out) != 0 || len(refs) != 0 {
		t.Fatalf("no inputs: %v %v %v", out, refs, err)
	}
}

// Requirement: pipeline_run with an input by reference answers its run in preparing before any file is read; the read
// runs on the input-refs queue, and a file that cannot be read fails the run with input:<path>: <reason> in its result,
// with the staging time on the run. A retried requestId answers the same run and reads nothing. Risk: a slow server
// holds the request past the client's and Cloudflare's timeouts and the run is never created (2026-11-27).
func TestPipelineRunRefStagedAfterAnswer(t *testing.T) {
	e := openTestCoordinator(t, false)
	for _, sql := range []string{
		`INSERT INTO access_server (id, address) VALUES ('pds9', '10.0.0.9')`,
		`INSERT INTO access_grant (id, project_id, server_id, kind, level, state, request_id, asked_by, payload)
		 VALUES ('ag-files', 'desiign', 'pds9', 'files', 'read', 'ready', 'g1', 'agent:worker', '{"connection": "desiign-files"}')`,
	} {
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	reads := 0
	pipelineRefFetch = func(_ context.Context, _ *App, _, input string, _ contract.PipelineRunRef, _ string) (string, refFile, error) {
		reads++
		return "", refFile{}, refInputError(400, input, "file-refused: no such file")
	}
	t.Cleanup(func() { pipelineRefFetch = pipelineRefRead })
	body := doc{"projectId": "desiign", "requestId": "ref-async", "query": "read", "wait": false,
		"inputs": []doc{{"path": "a.pdf", "ref": doc{"connection": "desiign-files", "path": "/srv/a.pdf"}}}}
	var run, again struct {
		ID     string `json:"id"`
		State  string `json:"state"`
		Result *struct {
			Error *pipelineStepError `json:"error"`
		} `json:"result"`
		InputRefs []pipelineInputRef `json:"inputRefs"`
	}
	e.opInto(t, "pipeline_run", body, e.ownerHeaders(), &run)
	if run.State != "preparing" || reads != 0 || len(run.InputRefs) != 1 || run.InputRefs[0].File != "/srv/a.pdf" {
		t.Fatalf("answer %+v after %d reads, want preparing with the ref as asked and no read", run, reads)
	}
	var key string
	if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT id FROM pipeline_input_ref WHERE run_id=$1`, run.ID).Scan(&key); err != nil {
		t.Fatalf("the ref waits on no queue row: %v", err)
	}
	if err := kitOf(e.app).runOne(e.ctx, "input-refs", &kitClaim{Key: key, Guard: []any{int32(0)}}); err != nil {
		t.Fatal(err)
	}
	e.opInto(t, "pipeline_get", doc{"runId": run.ID}, e.ownerHeaders(), &again)
	if again.State != "failed" || again.Result == nil || again.Result.Error == nil || again.Result.Error.Message != "input:a.pdf: file-refused: no such file" ||
		len(again.InputRefs) != 1 || again.InputRefs[0].StageMs == nil || reads != 1 {
		t.Fatalf("after staging %+v (%d reads), want failed with the input's message and its staging time", again, reads)
	}
	e.opInto(t, "pipeline_run", body, e.ownerHeaders(), &again)
	if again.ID != run.ID || reads != 1 {
		t.Fatalf("retry answered %s after %d reads, want %s and no new read", again.ID, reads, run.ID)
	}
}

// A promoted method carries its overrides (the Lab variant's profile): pipeline_run reads them from the catalogue.
func TestPipelineCatalogueMethodOverrides(t *testing.T) {
	var c pipelineCatalogue
	raw := `{"recommendedMethod":"m","methods":[{"id":"m","pipeline":"doc-recognize-agent.json","overrides":{"profile":"doc-reader-codex"}}]}`
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	var o pipelineOverrides
	if err := json.Unmarshal(c.Methods[0].Overrides, &o); err != nil || o.Profile != "doc-reader-codex" {
		t.Fatalf("method overrides lost: %s %v", c.Methods[0].Overrides, err)
	}
}
