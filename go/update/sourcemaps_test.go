package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// Requirement: fleet-error-tracking.md, source maps: an installed app's scripts and maps reach Bugsink as one artifact
// bundle (chunk-upload, then assemble for fleet-web) keyed by each script's debug ID, with the host's token; without
// the settings nothing is sent, and a failing Bugsink never fails the install. Risk: production stack frames stay
// minified, or an upload problem blocks a release.
func TestUploadSourcemaps(t *testing.T) {
	app := t.TempDir()
	os.MkdirAll(filepath.Join(app, "assets"), 0o755)
	os.MkdirAll(filepath.Join(app, "sourcemaps"), 0o755)
	id := "83617169-5a90-4bc5-bf67-ad28697d5ad6"
	os.WriteFile(filepath.Join(app, "assets", "main-AB.js"), []byte("x();\n//# debugId="+id+"\n"), 0o644)
	os.WriteFile(filepath.Join(app, "sourcemaps", "main-AB.js.map"), []byte(`{"version":3}`), 0o644)
	os.WriteFile(filepath.Join(app, "assets", "chunk-NO.js"), []byte("y();\n"), 0o644) // no debug ID: left out

	var mu sync.Mutex
	chunks := map[string][]byte{}
	var assembled map[string]any
	auth := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		auth = r.Header.Get("Authorization")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/0/organizations/bugsinkhasnoorgs/chunk-upload/":
			io.WriteString(w, `{"chunkSize":64}`)
		case r.Method == "POST" && r.URL.Path == "/api/0/organizations/bugsinkhasnoorgs/chunk-upload/":
			r.ParseMultipartForm(1 << 20)
			for _, f := range r.MultipartForm.File["file"] {
				fh, _ := f.Open()
				b, _ := io.ReadAll(fh)
				if sha1hex(b) != f.Filename {
					w.WriteHeader(400)
				}
				chunks[f.Filename] = b
			}
		case r.Method == "POST" && r.URL.Path == "/api/0/organizations/bugsinkhasnoorgs/artifactbundle/assemble/":
			json.NewDecoder(r.Body).Decode(&assembled)
			io.WriteString(w, `{"state":"created","missingChunks":[]}`)
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()

	u := &updater{}
	u.uploadSourcemaps(app, "1.261117.4") // no settings: nothing sent
	conf := t.TempDir()
	u.bugsinkDir = conf
	u.uploadSourcemaps(app, "1.261117.4")
	if len(chunks) != 0 {
		t.Fatal("uploaded without settings")
	}
	os.WriteFile(filepath.Join(conf, "ci-token"), []byte("invented-ci-token\n"), 0o600)
	os.WriteFile(filepath.Join(conf, "ingest-url"), []byte(srv.URL+"\n"), 0o600)
	u.uploadSourcemaps(app, "1.261117.4")

	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer invented-ci-token" || assembled == nil || len(chunks) < 2 {
		t.Fatalf("auth %q, assembled %v, %d chunks", auth, assembled, len(chunks))
	}
	var bundle []byte
	for _, c := range assembled["chunks"].([]any) {
		bundle = append(bundle, chunks[c.(string)]...)
	}
	if sha1hex(bundle) != assembled["checksum"] || assembled["projects"].([]any)[0] != "fleet-web" {
		t.Fatalf("assemble %v", assembled)
	}
	z, err := zip.NewReader(bytes.NewReader(bundle), int64(len(bundle)))
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Files map[string]struct {
			Type    string
			Headers map[string]string
		} `json:"files"`
		Release string `json:"release"`
	}
	names := []string{}
	for _, f := range z.File {
		names = append(names, f.Name)
		if f.Name == "manifest.json" {
			r, _ := f.Open()
			json.NewDecoder(r).Decode(&manifest)
		}
	}
	js, smap := manifest.Files["files/_/_/main-AB.js"], manifest.Files["files/_/_/main-AB.js.map"]
	if strings.Join(names, ",") != "files/_/_/main-AB.js,files/_/_/main-AB.js.map,manifest.json" || manifest.Release != "1.261117.4" ||
		js.Type != "minified_source" || js.Headers["debug-id"] != id || smap.Type != "source_map" || smap.Headers["debug-id"] != id {
		t.Fatalf("bundle %v manifest %+v", names, manifest)
	}

	// A Bugsink that refuses: only a warning.
	os.WriteFile(filepath.Join(conf, "ingest-url"), []byte(srv.URL+"/down"), 0o600)
	if n, err := u.sendSourcemaps(app, "v", srv.URL+"/down", "bugsinkhasnoorgs", "t"); err == nil || n != 0 {
		t.Fatalf("refused upload: %d %v", n, err)
	}
}
