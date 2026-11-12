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

// Requirement: UI discovery asks git protocol v2 ls-refs for this backend's tags only, never api.github.com
// (60 unauthenticated requests per hour, 304 included, which a 1 minute timer alone exhausts), and keeps the newest.
func TestUIDiscoveryTags(t *testing.T) {
	u := newUpdater(nil)
	body := ""
	u.client = &http.Client{Transport: uiTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method != "POST" || r.URL.String() != "https://github.com/taskqorg/fleet.git/git-upload-pack" || r.Header.Get("Git-Protocol") != "version=2" {
			t.Fatal("wrong discovery request", r.Method, r.URL)
		}
		req, _ := io.ReadAll(r.Body)
		if want := "0014command=ls-refs\n00010029ref-prefix refs/tags/ui-1.261112.0-v\n0000"; string(req) != want {
			t.Fatalf("request body %q", req)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	sha := strings.Repeat("a", 40)
	body = pkt(sha+" HEAD symref-target:refs/heads/main\n") + pkt(sha+" refs/tags/ui-1.261112.0-v1.261112.1\n") + pkt(sha+" refs/tags/ui-1.261112.0-v1.261112.3\n") +
		pkt(sha+" refs/tags/ui-1.261112.0-v1.261112.2\n") + pkt(sha+" refs/tags/ui-1.261111.9-v1.261112.4\n") +
		pkt(sha+" refs/tags/v1.261112.5\n") + "0000" // other refs: a server may answer beyond ref-prefix
	got, err := u.latestUI("1.261112.0")
	if err != nil || got != "1.261112.3" {
		t.Fatalf("%q %v", got, err)
	}
	body = "0000" // no tag for this backend yet
	if got, err := u.latestUI("1.261112.0"); err != nil || got != "" {
		t.Fatalf("%q %v", got, err)
	}
	body = pkt(sha+" refs/tags/ui-1.261112.0-v1.261112.6") + "0000" // the trailing LF is optional
	if got, err := u.latestUI("1.261112.0"); err != nil || got != "1.261112.6" {
		t.Fatalf("%q %v", got, err)
	}
	body = pkt("ERR upload-pack: nope") + "0000" // git writes ERR packets without LF
	if _, err := u.latestUI("1.261112.0"); err == nil || !strings.Contains(err.Error(), "ERR upload-pack: nope") {
		t.Fatalf("ERR packet not surfaced: %v", err)
	}
	for _, body = range []string{"<html>", pkt(sha + " refs/tags/ui-1.261112.0-v1.261112.9\n"), // no flush
		"0050" + sha + " refs/tags/ui-1.261112.0-v1.261112.9\n0000", pkt("junk") + "0000"} {
		if got, err := u.latestUI("1.261112.0"); err == nil {
			t.Fatalf("%q accepted as %q", body, got)
		}
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

// Requirement: a UI release that fetch refuses (bad signature) is recorded in ui-state.json and not downloaded again
// by the next run; a 404 (a deleted release whose tag stays) is an error retried next run, nothing refused.
func TestUIFetchRefusalRecorded(t *testing.T) {
	f := newFixture(t)
	f.u.window = 0
	backend, ver := "1.261112.0", "1.261112.1"
	var sums []byte
	f.u.client = &http.Client{Transport: uiTransport(func(r *http.Request) (*http.Response, error) {
		body, code := "", 200
		switch {
		case r.URL.Path == "/taskqorg/fleet.git/git-upload-pack":
			body = pkt(strings.Repeat("a", 40)+" refs/tags/ui-"+backend+"-v"+ver+"\n") + "0000"
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS") && sums != nil:
			body = string(sums)
		case strings.HasSuffix(r.URL.Path, "/SHA256SUMS.sig") && sums != nil:
			body = "not a signature"
		default:
			code = 404
		}
		return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	root, stage, stateFile := t.TempDir(), t.TempDir(), filepath.Join(t.TempDir(), "state.json")
	st := &State{}
	if err := f.u.cycleUI(backend, "on", root, stage, stateFile); err == nil || !strings.Contains(err.Error(), "Not Found") || readJSON(stateFile, st) {
		t.Fatalf("404 not a transient error: %v %+v", err, st)
	}
	sums = []byte("00 fleet-ui-" + ver + ".tgz\n")
	if err := f.u.cycleUI(backend, "on", root, stage, stateFile); err != nil {
		t.Fatal(err)
	}
	if !readJSON(stateFile, st) || st.Refused[ver] != "provenance" || st.Last == nil || st.Last.State != "refused" {
		t.Fatalf("bad signature not refused: %+v", st)
	}
	f.u.client.Transport = uiTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/taskqorg/fleet.git/git-upload-pack" {
			t.Fatal("refused release fetched again:", r.URL)
		}
		body := pkt(strings.Repeat("a", 40)+" refs/tags/ui-"+backend+"-v"+ver+"\n") + "0000"
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	if err := f.u.cycleUI(backend, "on", root, stage, stateFile); err != nil {
		t.Fatal(err)
	}
	f.u.client.Transport = uiTransport(func(r *http.Request) (*http.Response, error) {
		t.Fatal("held host still discovers:", r.URL)
		return nil, nil
	})
	if err := f.u.cycleUI(backend, "off", root, stage, stateFile); err != nil {
		t.Fatal(err)
	}
}
