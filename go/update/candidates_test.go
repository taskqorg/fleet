package main

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStageCandidate: a proposed CLI stages into the candidate cache, verified and version-checked, and replaces the
// component's earlier candidate; a URL from outside the vendor's origin is refused.
func TestStageCandidate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root does not run vendor CLIs")
	}
	script := map[string]string{"/v1": "#!/bin/sh\necho 'codex-cli 1.0.0'\n", "/v2": "#!/bin/sh\necho 'codex-cli 2.0.0'\n", "/v3": "#!/bin/sh\necho 'codex-cli 2.0.1'\n"}
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(script[r.URL.Path])) }))
	t.Cleanup(tls.Close)
	f := newFixture(t)
	f.u.client = tls.Client()
	dir := t.TempDir()
	writableOnCleanup(t, dir)
	asset := func(ver, path string) string {
		sum := sha256.Sum256([]byte(script[path]))
		return `{"version":"` + ver + `","url":"` + tls.URL + path + `","sha256":"` + hex.EncodeToString(sum[:]) + `"}`
	}
	if _, err := f.u.stageCandidate("cli", "codex", dir, []byte(asset("1.0.0", "/v1"))); err == nil || !strings.Contains(err.Error(), "is not from") {
		t.Fatalf("foreign origin staged: %v", err)
	}
	vendorOrigins(t, tls.URL)
	first, err := f.u.stageCandidate("cli", "codex", dir, []byte(asset("1.0.0", "/v1")))
	if err != nil || !strings.HasPrefix(first, dir) {
		t.Fatalf("%q %v", first, err)
	}
	second, err := f.u.stageCandidate("cli", "codex", dir, []byte(asset("2.0.0", "/v2")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("earlier candidate kept: %v", err)
	}
	if out, err := os.ReadFile(second); err != nil || !strings.Contains(string(out), "2.0.0") || filepath.Base(second) != "codex" {
		t.Fatalf("%s %v", out, err)
	}
	if _, err := f.u.stageCandidate("cli", "codex", dir, []byte(asset("3.0.0", "/v3"))); err == nil {
		t.Fatal("a candidate naming another version staged")
	}
}
