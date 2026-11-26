package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// fakeS3: a path-style store that keeps object sizes only (HEAD, copy PUT, DELETE).
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]int64
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case "HEAD":
		n, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(404)
			return
		}
		w.Header().Set("Content-Length", strconv.FormatInt(n, 10))
	case "DELETE":
		delete(f.objects, r.URL.Path)
		w.WriteHeader(204)
	case "PUT":
		src := "/" + strings.TrimPrefix(r.Header.Get("x-amz-copy-source"), "/")
		n, ok := f.objects[src]
		if !ok {
			w.WriteHeader(404)
			w.Write([]byte("<Error><Code>NoSuchKey</Code></Error>"))
			return
		}
		f.objects[r.URL.Path] = n
		w.Write([]byte("<CopyObjectResult></CopyObjectResult>"))
	default:
		w.WriteHeader(405)
	}
}

func (f *fakeS3) put(path string, n int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.objects[path] = n
}

func (f *fakeS3) del(path string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.objects, path)
}

func (f *fakeS3) has(path string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.objects[path]
	return ok
}

// Requirement: a committed upload whose staging object is gone stops the verify loop: renew answers "stored" when the
// content key holds the object, "missing" (the uploader's re-upload path) when it does not, and queues no verify; a
// present staging object whose verifications failed five times is rejected. Risk: 2026-11-26, a verified report deleted
// the staging object before its database write timed out; every renew then queued an artifact-verify that failed with
// NoSuchKey, the copy stayed pending, and owner-mac's open transfers held its update drain.
func TestReverifyStopsOnMissingStagingObject(t *testing.T) {
	e := openTestCoordinator(t, false)
	store := &fakeS3{objects: map[string]int64{}}
	srv := httptest.NewServer(store)
	t.Cleanup(srv.Close)
	keys := t.TempDir()
	old := storeSecretsDir
	storeSecretsDir = keys
	t.Cleanup(func() { storeSecretsDir = old })
	if err := os.WriteFile(filepath.Join(keys, "s-reverify.key"), []byte(`{"accessKeyId":"k","secretAccessKey":"s"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	upSecret := e.registerHost(t, "h-up")
	e.registerHost(t, "h-store")
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO store (id, host_id, bucket, endpoint, state) VALUES ('s-reverify', 'h-store', 'fleet', $1, 'reachable')`, srv.URL)
	exec(`INSERT INTO execution (id, kind, state, host_id, project_id, started_at, data, observation, config_ref)
		VALUES ('x-reverify', 'host_job', 'running', 'h-up', 'hello-world', now(), '{}', 'confirmed', '{}')`)
	hash := strings.Repeat("ab", 32)
	committed := func(id string) {
		exec(`INSERT INTO artifact (id, project_id, scope, content_hash, size, media_type, name, execution_id, state)
			VALUES ($1, 'hello-world', 'prod', $2, 10, 'text/plain', $1, 'x-reverify', 'pending')`, id, hash)
		exec(`INSERT INTO artifact_copy (artifact_id, store_id, project_id, state, staging_key, lease_expires_at)
			VALUES ($1, 's-reverify', 'hello-world', 'pending', 'hello-world/prod/.pending/' || $1 || '.s-reverify', now() + interval '1 hour')`, id)
	}
	renew := func(id string) string {
		t.Helper()
		code, reply := e.callJSON(t, "POST", "/api/hosts/artifacts/renew", doc{"hostId": "h-up", "artifactId": id, "storeId": "s-reverify"},
			map[string]string{"Authorization": "Bearer " + upSecret})
		if code != 200 {
			t.Fatalf("renew %s = %d %v", id, code, reply)
		}
		s, _ := reply["state"].(string)
		return s
	}
	verifies := func(id string) (n int) {
		t.Helper()
		if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT count(*) FROM command WHERE kind='artifact-verify' AND payload->'payload'->>'artifactId'=$1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	copyState := func(id string) (s string) {
		t.Helper()
		if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT state FROM artifact_copy WHERE artifact_id=$1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// Promoted already (the incident): the content key holds the object, the staging object is gone.
	committed("a-promoted-0001")
	store.put("/fleet/hello-world/prod/"+hash, 10)
	if got := renew("a-promoted-0001"); got != "stored" {
		t.Fatalf("promoted renew = %q, want stored", got)
	}
	if s := copyState("a-promoted-0001"); s != "verified" || verifies("a-promoted-0001") != 0 {
		t.Fatalf("promoted copy %s with %d verifies", s, verifies("a-promoted-0001"))
	}

	// Gone without its content: every renew answers "missing" and queues nothing.
	store.del("/fleet/hello-world/prod/" + hash)
	committed("a-missing-0001")
	for range 3 {
		if got := renew("a-missing-0001"); got != "missing" {
			t.Fatalf("missing renew = %q, want missing", got)
		}
	}
	if n := verifies("a-missing-0001"); n != 0 || copyState("a-missing-0001") != "pending" {
		t.Fatalf("missing copy: %d verifies queued", n)
	}

	// Present: one verify at a time; the fifth failed verification rejects the copy.
	committed("a-present-0001")
	staging := "/fleet/hello-world/prod/.pending/a-present-0001.s-reverify"
	store.put(staging, 10)
	for i := range maxVerifyFailures {
		if got := renew("a-present-0001"); got != "pending" {
			t.Fatalf("present renew %d = %q, want pending", i, got)
		}
		if got := renew("a-present-0001"); got != "pending" || verifies("a-present-0001") != i+1 {
			t.Fatalf("present renew %d queued %d verifies", i, verifies("a-present-0001"))
		}
		exec(`UPDATE command SET status='failed' WHERE kind='artifact-verify' AND payload->'payload'->>'artifactId'='a-present-0001'`)
	}
	if got := renew("a-present-0001"); got != "failed" {
		t.Fatalf("renew after %d failed verifies = %q, want failed", maxVerifyFailures, got)
	}
	if s := copyState("a-present-0001"); s != "failed" || store.has(staging) || verifies("a-present-0001") != maxVerifyFailures {
		t.Fatalf("rejected copy %s, staging kept %v, %d verifies", s, store.has(staging), verifies("a-present-0001"))
	}

	// An expired lease on a promoted upload verifies it instead of failing it.
	committed("a-expired-0001")
	exec(`UPDATE artifact SET content_hash=$1 WHERE id='a-expired-0001'`, strings.Repeat("cd", 32))
	store.put("/fleet/hello-world/prod/"+strings.Repeat("cd", 32), 10)
	exec(`UPDATE artifact_copy SET lease_expires_at=now() - interval '1 minute' WHERE artifact_id='a-expired-0001'`)
	if err := expireLeases(e.ctx, e.app); err != nil {
		t.Fatal(err)
	}
	if s := copyState("a-expired-0001"); s != "verified" {
		t.Fatalf("expired promoted copy = %s, want verified", s)
	}
}
