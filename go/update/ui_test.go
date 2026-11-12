package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: independently published UI matches its qualified backend.
// Risk: a signed but incompatible or incomplete UI replaces the working app.
func TestUICompatibility(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"index.html", "screens.json", "sw.js"} {
		os.WriteFile(filepath.Join(dir, name), []byte("fixture"), 0600)
	}
	os.WriteFile(filepath.Join(dir, "release.json"), []byte(`{"kind":"ui","version":"1.261112.1","backendVersion":"1.261112.0","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), 0600)
	if err := validateUI(dir, "1.261112.1", "1.261112.0"); err != nil {
		t.Fatal(err)
	}
	if err := validateUI(dir, "1.261112.1", "1.261112.2"); err == nil {
		t.Fatal("accepted another backend")
	}
	if err := validateUI(dir, "1.261112.2", "1.261112.0"); err == nil {
		t.Fatal("accepted another UI version")
	}
	os.Remove(filepath.Join(dir, "index.html"))
	if err := validateUI(dir, "1.261112.1", "1.261112.0"); err == nil {
		t.Fatal("accepted incomplete app")
	}
}

// Requirement: UI staging cannot reuse a native archive or bypass compatibility.
// Risk: same-version cache entries skip signature/manifest admission.
func TestUIStagingAdmission(t *testing.T) {
	f := newFixture(t)
	f.p.stage = t.TempDir()
	f.p.uiBackend = "1.261112.0"
	ver := "1.261112.1"
	native := filepath.Join(f.p.stage, ver+".tmp")
	os.MkdirAll(native, 0700)
	os.WriteFile(filepath.Join(native, "release.json"), []byte(`{"version":"`+ver+`"}`), 0600)
	if _, err := f.u.fetch(f.p, ver); err == nil {
		t.Fatal("reused native staging")
	}
	ui := filepath.Join(f.p.stage, "ui-"+ver+".tmp")
	os.MkdirAll(ui, 0700)
	for _, name := range []string{"index.html", "screens.json", "sw.js"} {
		os.WriteFile(filepath.Join(ui, name), []byte("fixture"), 0600)
	}
	os.WriteFile(filepath.Join(ui, "release.json"), []byte(`{"kind":"ui","version":"`+ver+`","backendVersion":"1.261112.9","commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`), 0600)
	if _, err := f.u.fetch(f.p, ver); err == nil {
		t.Fatal("reused incompatible UI staging")
	}
}

type uiTransport func(*http.Request) (*http.Response, error)

func (f uiTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Requirement: UI discovery reads the repository tag advertisement, never api.github.com (60 unauthenticated
// requests per hour, 304 included, which a 1 minute timer alone exhausts), and keeps the newest tag of this backend.
func TestUIDiscoveryTags(t *testing.T) {
	u := newUpdater(nil)
	u.client = &http.Client{Transport: uiTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "github.com" || r.URL.Path != "/taskqorg/fleet.git/info/refs" || r.URL.RawQuery != "service=git-upload-pack" {
			t.Fatal("wrong discovery URL", r.URL)
		}
		body := "001e# service=git-upload-pack\n0000" +
			"0158aaaa HEAD\x00multi_ack symref=HEAD:refs/heads/main agent=git/github\n" +
			"003daaaa refs/heads/main\n" +
			"003faaaa refs/tags/ui-1.261112.0-v1.261112.1\n" +
			"003faaaa refs/tags/ui-1.261112.0-v1.261112.1^{}\n" +
			"003faaaa refs/tags/ui-1.261112.0-v1.261112.3\n" +
			"003faaaa refs/tags/ui-1.261111.9-v1.261112.4\n" +
			"003faaaa refs/tags/v1.261112.5\n0000"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	got, err := u.latestUI("1.261112.0")
	if err != nil || got != "1.261112.3" {
		t.Fatalf("%q %v", got, err)
	}
	u.client.Transport = uiTransport(func(*http.Request) (*http.Response, error) { return nil, fmt.Errorf("fixture unavailable") })
	if _, err := u.latestUI("1.261112.0"); err == nil {
		t.Fatal("hid discovery failure")
	}
}

// Requirement: failed first UI installation restores bundled UI and stays refused.
// Risk: a bad UI is repeatedly exposed by each updater timer run.
func TestUIFailedFirstInstallResume(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	ver := "1.261112.1"
	tree := filepath.Join(root, ver)
	if err := os.MkdirAll(tree, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, "release.json"), []byte(`{"version":"`+ver+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(ver, filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	f.u.window = 0
	f.u.client = &http.Client{Transport: uiTransport(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Body: io.NopCloser(strings.NewReader("fixture down")), Header: make(http.Header)}, nil
	})}
	stateFile := filepath.Join(t.TempDir(), "state.json")
	if err := f.u.cycleUI("1.261112.0", "on", root, t.TempDir(), stateFile); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(root, "current")); !os.IsNotExist(err) {
		t.Fatalf("current remains: %v", err)
	}
	st := &State{}
	if !readJSON(stateFile, st) || st.Refused[ver] != "unhealthy" {
		t.Fatalf("missing refusal: %+v", st)
	}
	if st.Last == nil || st.Last.State != "refused" {
		t.Fatalf("missing restored status: %+v", st.Last)
	}
}
