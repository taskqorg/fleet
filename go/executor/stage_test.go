package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: staging creates inputs, and their parent directories, only inside the input folder (review W3-5). Risk:
// a parent that is (or is swapped for) a link to elsewhere redirects a creation outside: a pathname check followed by a
// pathname open leaves that window, and MkdirAll on the pathname follows the link before any check.
func TestStageInputsNeverLeaveTheInputFolder(t *testing.T) {
	base := t.TempDir()
	input, outside := filepath.Join(base, "input"), filepath.Join(base, "outside")
	if err := errors.Join(os.MkdirAll(input, 0o755), os.MkdirAll(outside, 0o755)); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(input, "sub")); err != nil {
		t.Fatal(err)
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/blob" {
			w.Write([]byte("artifact bytes"))
			return
		}
		w.Write([]byte(`{"url":"` + server.URL + `/blob"}`))
	}))
	defer server.Close()
	t.Setenv("FLEET_CF_ACCESS_FILE", filepath.Join(base, "none"))
	e := &executor{url: server.URL, token: "t", host: "h"}
	for _, rel := range []string{"sub/f", "sub/deeper/f"} {
		if err := e.stageInputs(M{"inputs": []any{M{"artifactId": "a", "path": rel}}}, input, "e1"); err == nil || !strings.Contains(err.Error(), "escapes") && !errors.Is(err, os.ErrExist) { // darwin: escapes; linux: the link is not followed
			t.Errorf("%s: %v; want the escape refused", rel, err)
		}
	}
	if left, _ := os.ReadDir(outside); len(left) > 0 {
		t.Fatalf("staging created %s outside the input folder", left[0].Name())
	}
	if err := e.stageInputs(M{"inputs": []any{M{"artifactId": "a", "path": "in/f"}}}, input, "e1"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(input, "in/f")); string(b) != "artifact bytes" {
		t.Fatalf("staged %q", b)
	}
}

// A resumed writer shares the session input root, including earlier staged files.
// Its new command must download to fresh paths using the ordinary exclusive staging path.
func TestStageDesignInputsAcrossWriterExecutions(t *testing.T) {
	input := t.TempDir()
	t.Setenv("FLEET_CF_ACCESS_FILE", filepath.Join(input, "none"))
	artifacts := map[string]string{"source": "invented source bytes", "recipe": "invented recipe bytes", "empty": ""}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == contract.RouteArtifactsGet.Path {
			var request contract.HostArtifactGetRequest
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.HostId != "h" || str0(request.ExecutionId) == "" {
				http.Error(w, "missing fixture authorization", http.StatusForbidden)
				return
			}
			json.NewEncoder(w).Encode(M{"url": "http://" + r.Host + "/blob/" + request.ArtifactId})
			return
		}
		if bytes, ok := artifacts[strings.TrimPrefix(r.URL.Path, "/blob/")]; ok {
			w.Write([]byte(bytes))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()
	e := &executor{url: server.URL, token: "t", host: "h"}
	first := []contract.InputArtifact{
		{ArtifactId: "source", Path: "design-review/first/start-one/selected-source"},
		{ArtifactId: "recipe", Path: "design-review/first/start-one/case-1-recipe.json"},
	}
	if err := e.stageArtifactInputs(first, input, "first", nil); err != nil {
		t.Fatal(err)
	}
	// Even a stale old selection must leave the next writer free to fetch the same artifacts.
	if err := os.WriteFile(filepath.Join(input, first[0].Path), []byte("old session file"), 0o600); err != nil {
		t.Fatal(err)
	}
	next := []contract.InputArtifact{
		{ArtifactId: "source", Path: "design-review/resume/start-two/selected-source"},
		{ArtifactId: "recipe", Path: "design-review/resume/start-two/case-1-recipe.json"},
	}
	if err := e.stageArtifactInputs(next, input, "resume", nil); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(input, first[0].Path)); err != nil || string(got) != "old session file" {
		t.Fatalf("old session file changed: %q, %v", got, err)
	}
	for _, file := range next {
		if got, err := os.ReadFile(filepath.Join(input, file.Path)); err != nil || string(got) != artifacts[file.ArtifactId] {
			t.Fatalf("resumed file %s = %q, %v", file.Path, got, err)
		}
	}
	legacy := []contract.InputArtifact{{ArtifactId: "empty", Path: "design-review/empty-output"}}
	if err := e.stageArtifactInputs(legacy, input, "pipeline", nil); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(input, legacy[0].Path)); err != nil || !info.Mode().IsRegular() || info.Size() != 0 {
		t.Fatalf("expected the valid empty pipeline output: %v, %v", info, err)
	}
	if err := e.stageArtifactInputs(legacy, input, "pipeline-retry", nil); !errors.Is(err, os.ErrExist) {
		t.Fatalf("ordinary input lost exclusive creation: %v", err)
	}
}
