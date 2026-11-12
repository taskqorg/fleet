package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Requirement: signed independent UI installs settle, resume and roll back while retaining assets.
// Risk: an unhealthy UI displaces the working UI or is retried after updater restart.
func TestUICycleSignedRollbackResume(t *testing.T) {
	f := newFixture(t)
	const backend = "1.261112.0"
	const good = "1.261112.1"
	const mismatch = "1.261112.2"
	const bad = "1.261112.3"
	root, stage := t.TempDir(), t.TempDir()
	stateFile := filepath.Join(t.TempDir(), "state.json")
	for _, ver := range []string{good, mismatch, bad} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		files := map[string]string{
			"release.json": fmt.Sprintf(`{"kind":"ui","version":%q,"backendVersion":%q,"commit":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`, ver, backend),
			"index.html":   ver, "screens.json": "{}", "sw.js": "// service worker",
			"assets/" + ver + ".js": "asset-" + ver,
		}
		for name, body := range files {
			if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := io.WriteString(tw, body); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := gz.Close(); err != nil {
			t.Fatal(err)
		}
		file := "fleet-ui-" + ver + ".tgz"
		sum := sha256.Sum256(buf.Bytes())
		sums := []byte(fmt.Sprintf("%x  %s\n", sum, file))
		at := "/download/ui-" + backend + "-v" + ver + "/"
		f.files[at+file], f.files[at+"SHA256SUMS"], f.files[at+"SHA256SUMS.sig"] = buf.Bytes(), sums, ed25519.Sign(f.priv, sums)
	}
	candidate := good
	unhealthy := false
	servedVersion, servedBackend := good, backend
	var events []string
	downloads, probes := 0, 0
	transport := http.DefaultTransport
	f.u.window = 0 // Each explicit health response determines one start; no timing race.
	f.u.coordURL = "https://fixture.invalid/api/health"
	f.u.client = &http.Client{Transport: uiTransport(func(r *http.Request) (*http.Response, error) {
		body, code := "", 200
		switch {
		case r.URL.Host == "api.github.com":
			events = append(events, "discovery")
			body = fmt.Sprintf(`[{"tag_name":"ui-%s-v%s"}]`, backend, candidate)
		case r.URL.Host == "fixture.invalid":
			if r.Method != http.MethodGet || r.URL.Path != "/ui-release.json" || r.URL.RawQuery != "" {
				t.Fatalf("unexpected UI health request: %s %s", r.Method, r.URL)
			}
			events = append(events, "health")
			probes++
			if unhealthy {
				code = 503
			} else {
				body = fmt.Sprintf(`{"version":%q,"backendVersion":%q}`, servedVersion, servedBackend)
			}
		default:
			downloads++
			return transport.RoundTrip(r)
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})}
	cycle := func() {
		t.Helper()
		if err := f.u.cycleUI(backend, "on", root, stage, stateFile); err != nil {
			t.Fatal(err)
		}
	}
	cycle()
	st := &State{}
	if !readJSON(stateFile, st) || st.Last == nil || st.Last.State != "ok" || releaseVersion(filepath.Join(root, "current")) != good {
		t.Fatalf("install: %+v", st)
	}
	if downloads != 3 || !exists(filepath.Join(root, good, "healthy")) {
		t.Fatalf("signed install downloads=%d", downloads)
	}
	// HTTP 200 from the old served UI must not qualify the newly installed UI.
	candidate = mismatch
	cycle()
	st = &State{}
	if !readJSON(stateFile, st) || st.Last == nil || st.Last.State != "refused" || st.Refused[mismatch] != "unhealthy" || releaseVersion(filepath.Join(root, "current")) != good {
		t.Fatalf("served identity mismatch: %+v", st)
	}
	candidate, unhealthy = bad, true
	cycle()
	st = &State{}
	if !readJSON(stateFile, st) || st.Last == nil || st.Last.State != "refused" || st.Refused[bad] != "unhealthy" || releaseVersion(filepath.Join(root, "current")) != good {
		t.Fatalf("rollback: %+v", st)
	}
	// Simulate interruption before settlement: resume must inspect current before discovery.
	if err := os.Remove(filepath.Join(root, good, "healthy")); err != nil {
		t.Fatal(err)
	}
	unhealthy = false
	events = nil
	cycle()
	if strings.Join(events, ",") != "health,discovery" {
		t.Fatalf("resume order: %v", events)
	}
	if !exists(filepath.Join(root, good, "healthy")) {
		t.Fatal("resume did not settle current")
	}
	beforeDownloads, beforeProbes := downloads, probes
	old := f.u
	f.u = newUpdater(f.priv.Public().(ed25519.PublicKey))
	f.u.base, f.u.target, f.u.window, f.u.tick = old.base, old.target, old.window, old.tick
	f.u.coordURL, f.u.client = old.coordURL, old.client
	events = nil
	cycle()
	if strings.Join(events, ",") != "discovery" {
		t.Fatalf("restart did not discover refused candidate: %v", events)
	}
	if downloads != beforeDownloads || probes != beforeProbes {
		t.Fatalf("refused release retried: downloads %d -> %d, probes %d -> %d", beforeDownloads, downloads, beforeProbes, probes)
	}
	for _, ver := range []string{good, mismatch, bad} {
		b, err := os.ReadFile(filepath.Join(root, "assets", ver+".js"))
		if err != nil || string(b) != "asset-"+ver {
			t.Fatalf("retained asset %s: %q %v", ver, b, err)
		}
	}
	st = &State{}
	if !readJSON(stateFile, st) || st.Refused[bad] != "unhealthy" || releaseVersion(filepath.Join(root, "current")) != good {
		t.Fatalf("resume lost refusal/current: %+v", st)
	}
}
