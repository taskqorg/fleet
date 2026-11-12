package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// Requirement: UI switches preserve backend compatibility and old lazy assets.
// Risk: backend rollback serves incompatible UI or breaks already open tabs.
func TestIndependentUIStatic(t *testing.T) {
	root := t.TempDir()
	bundled := filepath.Join(root, "bundled")
	uiRoot := filepath.Join(root, "ui")
	current := filepath.Join(uiRoot, "v1")
	for _, dir := range []string{bundled, current, filepath.Join(uiRoot, "assets")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(bundled, "index.html", "bundled")
	write(bundled, "screens.json", `["overview"]`)
	write(current, "index.html", "independent")
	write(current, "screens.json", `["overview","newscreen"]`)
	write(current, "release.json", `{"kind":"ui","backendVersion":"dev"}`)
	write(filepath.Join(uiRoot, "assets"), "old.js", "old chunk")
	if err := os.Symlink("v1", filepath.Join(uiRoot, "current")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_APP_DIR", bundled)
	t.Setenv("FLEET_UI_ROOT", uiRoot)
	oldScreen := appScreen
	t.Cleanup(func() { appScreen = oldScreen })
	oldRoutes := routeTable
	routeTable = nil
	t.Cleanup(func() { routeTable = oldRoutes })
	appRoutes()
	mux := http.NewServeMux()
	for _, route := range routeTable {
		mux.HandleFunc(route.pattern, route.public)
	}
	request := func(path, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", path, nil)
		if _, pattern := mux.Handler(r); pattern != "" {
			mux.ServeHTTP(w, r)
		} else if !appScreen(w, r) {
			http.NotFound(w, r)
		}
		if w.Code != 200 || w.Body.String() != want {
			t.Fatalf("%s: %d %q", path, w.Code, w.Body.String())
		}
	}
	request("/overview", "independent")
	request("/newscreen", "independent")
	request("/assets/old.js", "old chunk")
	write(current, "release.json", `{"kind":"ui","backendVersion":"other"}`)
	request("/overview", "bundled")
	w := httptest.NewRecorder()
	if appScreen(w, httptest.NewRequest("GET", "/newscreen", nil)) {
		t.Fatal("retained incompatible screen")
	}
	if appScreen(w, httptest.NewRequest("GET", "/api/stream", nil)) {
		t.Fatal("intercepted API stream")
	}

	request("/assets/old.js", "old chunk")
}
