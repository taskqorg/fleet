package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.261111.1", "1.261111.0", true}, {"1.261111.10", "1.261111.9", true}, {"2.250101.0", "1.261231.5", true},
		{"1.261111.0", "1.261111.0", false}, {"1.261110.0", "1.261111.0", false}, {"1.261111.0", "", true},
		{"1.261111.0", "dev", true}, {"dev", "", false}, {"1.261111.01", "", false}, {"0.261111.0", "", false},
		{"v1.261111.0", "", false}, {"1.26111.0", "", false},
	} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

// fixture serves releases signed with a test key; mutate changes what is served.
type fixture struct {
	t        *testing.T
	priv     ed25519.PrivateKey
	files    map[string][]byte // path under /download/ -> body
	served   map[string]int
	override func(path string, n int) []byte // per-request body override
	root     string
	health   map[string]probe // version -> probe answer
	restarts int
	u        *updater
	p        *part
}

func newFixture(t *testing.T) *fixture {
	pub, priv, _ := ed25519.GenerateKey(nil)
	f := &fixture{t: t, priv: priv, files: map[string][]byte{}, served: map[string]int{}, health: map[string]probe{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.served[r.URL.Path]++
		b, ok := f.files[r.URL.Path]
		if f.override != nil {
			if o := f.override(r.URL.Path, f.served[r.URL.Path]); o != nil {
				b, ok = o, true
			}
		}
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(b)
	}))
	t.Cleanup(srv.Close)
	f.u = newUpdater(pub)
	f.u.base, f.u.target, f.u.window, f.u.tick = srv.URL, "linux-amd64", 50*time.Millisecond, 5*time.Millisecond
	f.root = t.TempDir()
	r, err := openReleases(f.root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	f.p = &part{r: r, name: "executor", root: f.root, stage: f.root, bins: []string{"fleet-executor"}, st: &State{},
		restart: func() error { f.restarts++; return nil },
		health: func(ver string) probe {
			if p, ok := f.health[ver]; ok {
				return p
			}
			return up
		}}
	return f
}

// release publishes ver; selftest is the exit code of its fleet-executor selftest.
func (f *fixture) release(ver string, selftest int) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(name, body string, mode int64) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: mode, Size: int64(len(body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(body))
	}
	tw.WriteHeader(&tar.Header{Name: "./config/", Mode: 0o755, Typeflag: tar.TypeDir})
	add("./release.json", `{"version":"`+ver+`"}`, 0o644)
	add("./fleet-executor", "#!/bin/sh\nexit "+string(rune('0'+selftest))+"\n", 0o755)
	tw.Close()
	gz.Close()
	file := "fleet-" + ver + "-linux-amd64.tgz"
	sum := sha256.Sum256(buf.Bytes())
	sums := []byte(hex.EncodeToString(sum[:]) + "  " + file + "\n")
	at := "/download/v" + ver + "/"
	f.files[at+file], f.files[at+"SHA256SUMS"], f.files[at+"SHA256SUMS.sig"] = buf.Bytes(), sums, ed25519.Sign(f.priv, sums)
}

func (f *fixture) run(target string) *Result {
	r := f.u.cycle(f.p, "on", func() string { return target })
	if r != nil {
		f.p.st.Last = r
	}
	return r
}

func (f *fixture) current() string { return releaseVersion(filepath.Join(f.root, "current")) }

func TestInstallAndNeverDowngrade(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 0)
	f.release("1.261111.1", 0)
	if r := f.run("1.261111.0"); r.State != "ok" || f.current() != "1.261111.0" {
		t.Fatalf("first install: %+v current %q", r, f.current())
	}
	if r := f.run("1.261111.1"); r.State != "ok" || f.current() != "1.261111.1" {
		t.Fatalf("second install: %+v", r)
	}
	if prev := releaseVersion(filepath.Join(f.root, "previous")); prev != "1.261111.0" {
		t.Fatalf("previous = %q", prev)
	}
	if !exists(filepath.Join(f.root, "1.261111.1", "healthy")) {
		t.Fatal("no healthy marker")
	}
	if st, _ := os.Stat(filepath.Join(f.root, "1.261111.1", "fleet-executor")); st.Mode().Perm()&0o222 != 0 {
		t.Fatalf("release file writable: %v", st.Mode())
	}
	if r := f.run("1.261111.0"); r != nil || f.current() != "1.261111.1" {
		t.Fatalf("downgrade: %+v current %q", r, f.current())
	}
	if r := f.run("dev"); r != nil {
		t.Fatalf("dev installed: %+v", r)
	}
	f.release("1.261111.2", 0)
	f.run("1.261111.2")
	pruneReleases(f.p.r)
	if exists(filepath.Join(f.root, "1.261111.0")) || !exists(filepath.Join(f.root, "1.261111.1")) {
		t.Fatal("prune kept the wrong releases")
	}
}

func TestBadSignatureRefused(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 0)
	f.files["/download/v1.261111.0/SHA256SUMS.sig"][0] ^= 1
	if r := f.run("1.261111.0"); r.State != "refused" || r.Error != "provenance" || f.current() != "" {
		t.Fatalf("%+v", r)
	}
	if r := f.run("1.261111.0"); r != nil {
		t.Fatalf("refused version retried: %+v", r)
	}
}

func TestDigestMismatchRetriedOnce(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 0)
	tgz := "/download/v1.261111.0/fleet-1.261111.0-linux-amd64.tgz"
	f.override = func(path string, n int) []byte {
		if path == tgz && n == 1 {
			return []byte("truncated")
		}
		return nil
	}
	if r := f.run("1.261111.0"); r.State != "ok" || f.served[tgz] != 2 {
		t.Fatalf("%+v served %d", r, f.served[tgz])
	}
	f.release("1.261111.1", 0)
	f.override = func(path string, n int) []byte {
		if path == "/download/v1.261111.1/fleet-1.261111.1-linux-amd64.tgz" {
			return []byte("tampered")
		}
		return nil
	}
	if r := f.run("1.261111.1"); r.State != "refused" || r.Error != "checksum" {
		t.Fatalf("%+v", r)
	}
}

func TestTransientError(t *testing.T) {
	f := newFixture(t)
	if r := f.run("1.261111.0"); r.State != "error" || len(f.p.st.Refused) != 0 {
		t.Fatalf("%+v", r)
	}
}

func TestSelftestRefused(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 1)
	if r := f.run("1.261111.0"); r.State != "refused" || r.Error != "selftest" || f.current() != "" {
		t.Fatalf("%+v", r)
	}
}

func TestThreeStartsRollBack(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 0)
	f.release("1.261111.1", 0)
	f.run("1.261111.0")
	f.restarts = 0
	f.health["1.261111.1"] = down
	if r := f.run("1.261111.1"); r.State != "refused" || r.Error != "unhealthy" || f.current() != "1.261111.0" {
		t.Fatalf("%+v current %q", r, f.current())
	}
	if f.restarts != 4 { // three starts plus the restart of previous
		t.Fatalf("restarts = %d", f.restarts)
	}
	if r := f.run("1.261111.1"); r != nil {
		t.Fatalf("refused version retried: %+v", r)
	}
}

func TestNoPreviousFails(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 0)
	f.health["1.261111.0"] = down
	if r := f.run("1.261111.0"); r.State != "failed" || f.current() != "1.261111.0" {
		t.Fatalf("%+v", r)
	}
}

func TestPendingNotCounted(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 0)
	f.health["1.261111.0"] = waiting
	if r := f.run("1.261111.0"); r.State != "pending" || exists(filepath.Join(f.root, "1.261111.0", "starts")) {
		t.Fatalf("%+v", r)
	}
	f.restarts = 0
	f.health["1.261111.0"] = up
	if r := f.run(""); r.State != "ok" || f.restarts != 0 { // the next run checks again without a restart
		t.Fatalf("%+v restarts %d", r, f.restarts)
	}
}

func TestHeld(t *testing.T) {
	f := newFixture(t)
	f.u.now = func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }
	for _, c := range []struct {
		au   string
		held bool
	}{{"on", false}, {"off", true}, {"2026-11-13T09:00:00Z", true}, {"2026-11-11T09:00:00Z", false}, {"", false}} {
		if f.u.held(c.au) != c.held {
			t.Errorf("held(%q) != %v", c.au, c.held)
		}
	}
	f.release("1.261111.0", 0)
	if r := f.u.cycle(f.p, "off", func() string { return "1.261111.0" }); r.State != "held" || f.current() != "" {
		t.Fatalf("%+v", r)
	}
}

func TestExecutorHealth(t *testing.T) {
	sig := filepath.Join(t.TempDir(), "update.json")
	h := newUpdater(nil).executorHealth(sig)
	for _, c := range []struct {
		body string
		want probe
	}{
		{`{"running":"1.261111.0","pollOk":true}`, up},
		{`{"running":"1.261111.0","pollError":"network"}`, waiting},
		{`{"running":"1.261111.0","pollError":"http-503"}`, waiting},
		{`{"running":"1.261111.0","pollError":"http-401"}`, down},
		{`{"running":"1.261110.0","pollOk":true}`, down},
	} {
		os.WriteFile(sig, []byte(c.body), 0o600)
		if got := h("1.261111.0"); got != c.want {
			t.Errorf("%s: %v", c.body, got)
		}
	}
}

func TestSymlinkOutOfReleasesRefused(t *testing.T) {
	f := newFixture(t)
	f.release("1.261111.0", 0)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(f.root, "1.261111.0.tmp")) // planted by the releases directory's owner
	f.p.stage = t.TempDir()                                      // PDS18 shape: staged elsewhere, copied in
	f.run("1.261111.0")
	if entries, _ := os.ReadDir(outside); len(entries) != 0 {
		t.Fatalf("wrote through a symlink out of the releases root: %v", entries)
	}
}

func TestPruneDumps(t *testing.T) {
	root, dumps := t.TempDir(), t.TempDir()
	os.Symlink("1.261111.2", filepath.Join(root, "current"))
	os.Symlink("1.261111.1", filepath.Join(root, "previous"))
	for _, v := range []string{"1.261111.0", "1.261111.1", "1.261111.2"} {
		os.WriteFile(filepath.Join(dumps, "fleet-pre-"+v+".dump"), nil, 0o600)
	}
	os.WriteFile(filepath.Join(dumps, "fleet-pre-1.261111.3.dump"), nil, 0o600)
	r, _ := os.OpenRoot(root)
	defer r.Close()
	pruneDumps(dumps, r, &State{Refused: map[string]string{"1.261111.3": "unhealthy"}})
	if exists(filepath.Join(dumps, "fleet-pre-1.261111.0.dump")) || !exists(filepath.Join(dumps, "fleet-pre-1.261111.1.dump")) ||
		!exists(filepath.Join(dumps, "fleet-pre-1.261111.3.dump")) {
		t.Fatal("wrong dumps pruned")
	}
}

// The PDS18 smoke: up only when the guard refuses a request without an Access assertion (403); down otherwise.
func TestSmoke(t *testing.T) {
	code := 403
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
	u := &updater{guardURL: srv.URL + "/verify"}
	c := &http.Client{Timeout: time.Second}
	if u.smoke(c) != up {
		t.Fatal("403 from the guard must be up")
	}
	code = 404
	if u.smoke(c) != down {
		t.Fatal("another status must be down")
	}
	srv.Close()
	if u.smoke(c) != down {
		t.Fatal("guard not answering must be down")
	}
}

// The trigger names the real data dir, is rewritten only on change, and is skipped where fleet-update is not installed.
func TestTrigger(t *testing.T) {
	home := t.TempDir()
	if triggerFor(home, "linux", "/d/update/update.json", 501) != nil {
		t.Fatal("no fleet-update.service: no trigger")
	}
	os.MkdirAll(filepath.Join(home, ".config", "systemd", "user"), 0o755)
	os.WriteFile(filepath.Join(home, ".config", "systemd", "user", "fleet-update.service"), nil, 0o644)
	tr := triggerFor(home, "linux", "/d/update/update.json", 501)
	if tr == nil || !bytes.Contains(tr.body, []byte("PathChanged=/d/update/update.json\n")) {
		t.Fatalf("linux trigger: %+v", tr)
	}
	os.MkdirAll(filepath.Join(home, "Library", "LaunchAgents"), 0o755)
	os.WriteFile(filepath.Join(home, "Library", "LaunchAgents", "com.ufobe.fleet-update.plist"), nil, 0o644)
	if !bytes.Contains(triggerFor(home, "linux", "/d%1/update.json", 501).body, []byte("PathChanged=/d%%1/update.json")) {
		t.Fatal("% must be escaped for systemd")
	}
	m := triggerFor(home, "darwin", "/Users/x/a&b/update/update.json", 501)
	if m == nil || !bytes.Contains(m.body, []byte("<string>/Users/x/a&amp;b/update/update.json</string>")) || !bytes.Contains(m.body, []byte("gui/501/com.ufobe.fleet-update<")) {
		t.Fatalf("darwin trigger: %s", m.body)
	}
}

// A failed load removes the unit so the next run retries; an unchanged unit runs nothing.
func TestLoadTrigger(t *testing.T) {
	var ran int
	fail := true
	old, oldWait := runTrigger, triggerWait
	defer func() { runTrigger, triggerWait = old, oldWait }()
	triggerWait = 0
	runTrigger = func(argv []string) error {
		ran++
		if fail && argv[0] == "load" {
			return os.ErrPermission
		}
		return nil
	}
	tr := &trigger{file: filepath.Join(t.TempDir(), "u"), body: []byte("x"), load: []step{{argv: []string{"out"}, tolerate: true}, {argv: []string{"load"}, retry: 3}}}
	loadTrigger(tr)
	if exists(tr.file) || ran != 4 {
		t.Fatalf("failed load: file kept or %d runs (want 1 + 3 retries)", ran)
	}
	fail, ran = false, 0
	loadTrigger(tr)
	loadTrigger(tr)
	if !exists(tr.file) || ran != 2 {
		t.Fatalf("load then unchanged: %d runs, want 2", ran)
	}
}

// latest prefers the API's tag_name and falls back to latest.json when the API is rate limited.
func TestLatestAPI(t *testing.T) {
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/releases/latest":
			w.WriteHeader(code)
			w.Write([]byte(`{"tag_name":"v1.261111.5"}`))
		case "/rel/latest/download/latest.json":
			w.Write([]byte(`{"version":"1.261111.4"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u := &updater{base: srv.URL + "/rel", api: srv.URL + "/api", client: srv.Client()}
	if v := u.latest(); v != "1.261111.5" {
		t.Fatalf("API: %q", v)
	}
	for _, code = range []int{403, 429, 500} {
		if v := u.latest(); v != "1.261111.4" {
			t.Fatalf("HTTP %d: %q, want the redirect's version", code, v)
		}
	}
}
