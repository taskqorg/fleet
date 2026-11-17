package main

// Source maps of an installed app go to Bugsink (docs/designs/fleet-error-tracking.md, Source maps): the host that
// installs the app uploads them over Bugsink's loopback address, so no upload path passes Cloudflare Access. The
// upload is the Sentry artifact-bundle protocol sentry-cli uses (chunk-upload, then artifactbundle/assemble), which
// Bugsink implements: one zip with manifest.json naming each script and its map by debug ID. Best effort: a failure
// is logged and never blocks an install.

import (
	"archive/zip"
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// bugsinkProject: the Bugsink project of the operator app.
const bugsinkProject = "fleet-web"

var debugIDLine = regexp.MustCompile(`(?m)^//# debugId=([0-9a-f-]{36})\s*$`)

// uploadSourcemaps sends the maps of app (the directory holding assets/ and sourcemaps/) with release ver. Off unless
// u.bugsinkDir holds ci-token and ingest-url (PDS18's /etc/fleet/bugsink); sentry-org defaults to Bugsink's
// placeholder.
func (u *updater) uploadSourcemaps(app, ver string) {
	if u.bugsinkDir == "" {
		return
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(u.bugsinkDir, name))
		return strings.TrimSpace(string(b))
	}
	token, base, org := read("ci-token"), strings.TrimRight(read("ingest-url"), "/"), read("sentry-org")
	if token == "" || base == "" {
		return
	}
	if org == "" {
		org = "bugsinkhasnoorgs"
	}
	n, err := u.sendSourcemaps(app, ver, base, org, token)
	switch {
	case err != nil:
		log.Printf("warning: source maps of %s not uploaded to Bugsink: %v", ver, err)
	case n > 0:
		log.Printf("%s: %d source maps uploaded to Bugsink", ver, n)
	}
}

func (u *updater) sendSourcemaps(app, ver, base, org, token string) (int, error) {
	bundle, n, err := sourcemapBundle(app, ver, org)
	if err != nil || n == 0 {
		return 0, err
	}
	client := &http.Client{Timeout: time.Minute}
	call := func(method, url, contentType string, body io.Reader) ([]byte, error) {
		req, err := http.NewRequest(method, url, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("%s %s: HTTP %d", method, strings.TrimPrefix(url, base), resp.StatusCode)
		}
		return b, nil
	}
	upload := base + "/api/0/organizations/" + org + "/chunk-upload/"
	b, err := call("GET", upload, "", nil)
	if err != nil {
		return 0, err
	}
	var opts struct {
		ChunkSize int `json:"chunkSize"`
	}
	if json.Unmarshal(b, &opts) != nil || opts.ChunkSize <= 0 {
		return 0, errors.New("chunk-upload settings unreadable")
	}
	chunks := []string{}
	for off := 0; off < len(bundle); off += opts.ChunkSize {
		chunk := bundle[off:min(off+opts.ChunkSize, len(bundle))]
		sum := sha1hex(chunk)
		chunks = append(chunks, sum)
		var form bytes.Buffer
		w := multipart.NewWriter(&form)
		part, _ := w.CreateFormFile("file", sum)
		part.Write(chunk)
		w.Close()
		if _, err := call("POST", upload, w.FormDataContentType(), &form); err != nil {
			return 0, err
		}
	}
	assemble, _ := json.Marshal(map[string]any{"checksum": sha1hex(bundle), "chunks": chunks, "projects": []string{bugsinkProject}})
	b, err = call("POST", base+"/api/0/organizations/"+org+"/artifactbundle/assemble/", "application/json", bytes.NewReader(assemble))
	if err != nil {
		return 0, err
	}
	var state struct {
		State string `json:"state"`
	}
	if json.Unmarshal(b, &state) == nil && state.State == "not_found" {
		return 0, errors.New("assemble: chunks missing")
	}
	return n, nil
}

// sourcemapBundle: the artifact bundle (the zip sentry-cli builds) of every script in app/assets with its debug ID
// and its map in app/sourcemaps; scripts without either are left out. n counts the maps.
func sourcemapBundle(app, ver, org string) (bundle []byte, n int, err error) {
	scripts, err := filepath.Glob(filepath.Join(app, "assets", "*.js"))
	if err != nil {
		return nil, 0, err
	}
	files := map[string]any{}
	var out bytes.Buffer
	z := zip.NewWriter(&out)
	add := func(name string, b []byte) error {
		w, err := z.Create(name)
		if err == nil {
			_, err = w.Write(b)
		}
		return err
	}
	for _, script := range scripts {
		js, err := os.ReadFile(script)
		if err != nil {
			return nil, 0, err
		}
		m := debugIDLine.FindSubmatch(js)
		name := filepath.Base(script)
		smap, err := os.ReadFile(filepath.Join(app, "sourcemaps", name+".map"))
		if m == nil || err != nil {
			continue
		}
		id := string(m[1])
		files["files/_/_/"+name] = map[string]any{"url": "~/assets/" + name, "type": "minified_source", "headers": map[string]string{"debug-id": id, "sourcemap": name + ".map"}}
		files["files/_/_/"+name+".map"] = map[string]any{"url": "~/assets/" + name + ".map", "type": "source_map", "headers": map[string]string{"debug-id": id}}
		if err := add("files/_/_/"+name, js); err != nil {
			return nil, 0, err
		}
		if err := add("files/_/_/"+name+".map", smap); err != nil {
			return nil, 0, err
		}
		n++
	}
	if n == 0 {
		return nil, 0, nil
	}
	manifest, _ := json.Marshal(map[string]any{"files": files, "org": org, "project": bugsinkProject, "release": ver})
	if err := add("manifest.json", manifest); err != nil {
		return nil, 0, err
	}
	if err := z.Close(); err != nil {
		return nil, 0, err
	}
	return out.Bytes(), n, nil
}

func sha1hex(b []byte) string { s := sha1.Sum(b); return hex.EncodeToString(s[:]) }
