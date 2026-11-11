package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		if err := e.stageInputs(M{"inputs": []any{M{"artifactId": "a", "path": rel}}}, input, "e1"); err == nil || !strings.Contains(err.Error(), "escapes") {
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
