package contract

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func code(l CallLine) string {
	if l.ErrorCode == nil {
		return ""
	}
	return *l.ErrorCode
}

func shaOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func target(url string, idempotent bool) GatewayTarget {
	return GatewayTarget{URL: url, Token: "invented-token", Idempotent: idempotent}
}

// cut drops the connection without an answer.
func cut(w http.ResponseWriter) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		conn.Close()
	}
}

// Requirement: §9 Edge errors: a 5xx without x-fleet-call-id came from Cloudflare or Caddy, so the gateway may have sent
// the call; it reads unknown for a tool that may not be repeated and error for one that may, both edge-<status>.
// Risk: an upload whose outcome is open reads as a plain error and the agent attaches the file twice.
func TestEdge5xxUnknownForNonIdempotent(t *testing.T) {
	for _, status := range []int{502, 524} {
		gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			io.Copy(io.Discard, r.Body)
			w.WriteHeader(status)
			io.WriteString(w, "<html>bad gateway</html>")
		}))
		data := []byte("png")
		up := GatewayUpload(context.Background(), target(gw.URL, false), "youtrack", "attachment_add", map[string]any{"issue": "TGT-1"},
			Upload{File: bytes.NewReader(data), Size: 3, Name: "a.png", MediaType: "image/png", Sha256: shaOf(data)})
		read := GatewayCall(context.Background(), target(gw.URL, true), "youtrack", "issue_get", []byte(`{"issue":"TGT-1"}`))
		write := GatewayCall(context.Background(), target(gw.URL, false), "youtrack", "comment_add", []byte(`{}`))
		gw.Close()
		want := "edge-" + strconv.Itoa(status)
		for _, c := range []struct {
			line    CallLine
			outcome CallOutcome
		}{{up, CallOutcomeUnknown}, {read, CallOutcomeError}, {write, CallOutcomeUnknown}} {
			if c.line.Outcome != c.outcome || code(c.line) != want {
				t.Fatalf("%d %s: %s %s", status, c.line.Tool, c.line.Outcome, code(c.line))
			}
		}
	}
	// With the gateway's call id it is the gateway's own answer: its outcome header decides.
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fleet-Call-Id", "c1")
		w.Header().Set("X-Fleet-Outcome", "error")
		w.Header().Set("X-Fleet-Error", "provider-body")
		w.WriteHeader(502)
		io.WriteString(w, `{"error":"provider-body"}`)
	}))
	defer gw.Close()
	if l := GatewayCall(context.Background(), target(gw.URL, false), "c", "t", []byte(`{}`)); l.Outcome != CallOutcomeError || code(l) != "provider-body" {
		t.Fatalf("gateway answer: %s %s", l.Outcome, code(l))
	}
}

// Requirement: §9 Client timeouts: the client waits on progress, not on a total deadline; a slow, steady answer longer
// than the stall wait succeeds, a silence longer than it fails client-timeout. Risk: a long transfer cut by a fixed
// 60 s request deadline. (FLEET_STALL_MS scaled down to 1 s: 250 ms gaps for 2 s, then 2.5 s of silence.)
func TestStallTimeoutNotTotalDeadline(t *testing.T) {
	t.Setenv("FLEET_STALL_MS", "1000")
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fleet-Call-Id", "c1")
		if r.URL.Path == "/v1/call/c/silent" {
			time.Sleep(2500 * time.Millisecond)
			return
		}
		io.WriteString(w, "[")
		for i := 0; i < 8; i++ {
			w.(http.Flusher).Flush()
			time.Sleep(250 * time.Millisecond)
			io.WriteString(w, "1,")
		}
		io.WriteString(w, "1]")
	}))
	defer gw.Close()
	started := time.Now()
	if l := GatewayCall(context.Background(), target(gw.URL, true), "c", "steady", []byte(`{}`)); l.Outcome != CallOutcomeOk || time.Since(started) < 1800*time.Millisecond {
		t.Fatalf("steady: %s %s after %v", l.Outcome, code(l), time.Since(started))
	}
	if l := GatewayCall(context.Background(), target(gw.URL, true), "c", "silent", []byte(`{}`)); l.Outcome != CallOutcomeUnknown || code(l) != "client-timeout" {
		t.Fatalf("silent: %s %s", l.Outcome, code(l))
	}
}

// Requirement: §9 Edge errors: a connection lost before the client sent the last byte of an upload reached nothing
// (the gateway posts only a whole, checked file): error upload-interrupted; lost after the last byte and before the
// reply it is unknown connection-lost; the gateway's 503 draining is an error. Risk: a safe retry reported unknown, or
// an upload that may be stored reported safe to retry.
func TestUploadInterruptedBeforeBodyEnd(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 32<<20)
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get("X-Test") {
		case "half":
			io.CopyN(io.Discard, r.Body, int64(len(data)/2))
			cut(w)
		case "whole":
			io.Copy(io.Discard, r.Body)
			cut(w)
		case "draining":
			w.Header().Set("X-Fleet-Call-Id", "c1")
			w.Header().Set("X-Fleet-Outcome", "error")
			w.Header().Set("X-Fleet-Error", "draining")
			w.Header().Set("Connection", "close")
			w.WriteHeader(503)
			io.WriteString(w, `{"error":"draining"}`)
		}
	}))
	defer gw.Close()
	for mode, want := range map[string][2]string{"half": {"error", "upload-interrupted"}, "whole": {"unknown", "connection-lost"}, "draining": {"error", "draining"}} {
		tg := target(gw.URL, false)
		tg.Header = map[string]string{"X-Test": mode}
		l := GatewayUpload(context.Background(), tg, "youtrack", "attachment_add", map[string]any{"issue": "TGT-1"},
			Upload{File: bytes.NewReader(data), Size: int64(len(data)), Name: "big.mov", MediaType: "video/quicktime", Sha256: shaOf(data)})
		if string(l.Outcome) != want[0] || code(l) != want[1] {
			t.Fatalf("%s: %s %s", mode, l.Outcome, code(l))
		}
	}
}

// Requirement: mark an upload potentially sent when transport receives its last byte.
// Risk: a disconnect before a separate EOF read is reported as safe to retry.
func TestUploadLastByteMarksBodySent(t *testing.T) {
	dog := newWatchdog(func() {}, time.Minute)
	defer dog.stop()
	p := &bodyReader{r: strings.NewReader("data"), dog: dog, stall: time.Minute, after: time.Minute, want: 4}
	buf := make([]byte, 4)
	if n, err := p.Read(buf); n != len(buf) || err != nil || !p.done.Load() {
		t.Fatalf("last byte: n=%d err=%v sent=%v", n, err, p.done.Load())
	}
}

// Requirement: §9 upload: the second pass streams the file as it is now, so a file changed after the first pass reaches
// the gateway with another hash than X-Fleet-Sha256 (and the gateway refuses it, sha-mismatch). Risk: bytes nobody
// hashed reach YouTrack under a matching hash.
func TestUploadFileChangedBetweenPasses(t *testing.T) {
	var got, claimed string
	var args map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got, claimed = shaOf(b), r.Header.Get("X-Fleet-Sha256")
		raw, _ := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Fleet-Args"))
		json.Unmarshal(raw, &args)
		if r.Header.Get("Content-Type") != "application/octet-stream" || r.ContentLength != int64(len(b)) {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("X-Fleet-Call-Id", "c1")
		io.WriteString(w, `[{"id":"85-1"}]`)
	}))
	defer gw.Close()
	path := filepath.Join(t.TempDir(), "shot.png")
	os.WriteFile(path, []byte("first"), 0o600)
	f, _ := os.Open(path)
	defer f.Close()
	first := shaOf([]byte("first"))
	os.WriteFile(path, []byte("other"), 0o600) // same size, other bytes
	l := GatewayUpload(context.Background(), target(gw.URL, false), "youtrack", "attachment_add", map[string]any{"issue": "TGT-1"},
		Upload{File: f, Size: 5, Name: "shot.png", MediaType: "image/png", Sha256: first})
	if claimed != first || got != shaOf([]byte("other")) {
		t.Fatalf("claimed %s, sent %s", claimed, got)
	}
	if l.Outcome != CallOutcomeOk || args["issue"] != "TGT-1" || args["name"] != "shot.png" || args["mediaType"] != "image/png" || len(args) != 3 {
		t.Fatalf("%s %v", l.Outcome, args)
	}
	var out map[string]any
	json.Unmarshal(l.Output, &out)
	if out["sha256"] != first || out["callId"] != "c1" || out["reply"] == nil || l.Transfer == nil || l.Transfer.Direction != CallTransferDirectionUpload {
		t.Fatalf("output %s", l.Output)
	}
}

// fileServer answers a download with name and body; cutAt >= 0 drops the connection after that many bytes.
func fileServer(t *testing.T, name string, body []byte, cutAt int) *httptest.Server {
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/octet-stream" {
			w.WriteHeader(400)
			return
		}
		meta, _ := json.Marshal(map[string]any{"name": name, "mediaType": "image/png", "bytes": len(body)})
		w.Header().Set("X-Fleet-Call-Id", "c1")
		w.Header().Set("X-Fleet-File", base64.RawURLEncoding.EncodeToString(meta))
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		if cutAt >= 0 {
			w.Write(body[:cutAt])
			w.(http.Flusher).Flush()
			cut(w)
			return
		}
		w.Write(body)
	}))
	t.Cleanup(gw.Close)
	return gw
}

func entries(t *testing.T, dir string) []string {
	var names []string
	filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			names = append(names, strings.TrimPrefix(p, dir+"/"))
		}
		return nil
	})
	return names
}

// Requirement: §9 download writer: the file lands in <root>/<issue>/<attachment>-<name> whatever the provider's name
// holds; a symlink planted at the attachments folder, the issue folder, the part file or the final name is refused and
// nothing is written outside the root. Risk: a name such as ../../.ssh/x writes
// outside the download folder.
func TestDownloadWriteStaysInFolder(t *testing.T) {
	body := []byte("\x89PNG bytes")
	root := t.TempDir()
	gw := fileServer(t, "../../.ssh/\x01evil‮gnp.png", body, -1)
	l := GatewayDownload(context.Background(), target(gw.URL, true), "youtrack", "attachment_get", []byte(`{}`), Download{Root: root, Folder: []string{"TGT-1"}, Prefix: "85-1"})
	if l.Outcome != CallOutcomeOk {
		t.Fatalf("%s %s", l.Outcome, code(l))
	}
	var out map[string]any
	json.Unmarshal(l.Output, &out)
	want := filepath.Join(root, "TGT-1", "85-1-_._.._.ssh__evil_gnp.png")
	if out["path"] != want || out["sha256"] != shaOf(body) || out["bytes"] != float64(len(body)) || *l.Transfer.Sha256 != shaOf(body) {
		t.Fatalf("output %s, want %s", l.Output, want)
	}
	if b, _ := os.ReadFile(want); !bytes.Equal(b, body) {
		t.Fatal("content")
	}
	if got := entries(t, root); len(got) != 1 {
		t.Fatalf("files %v", got)
	}
	if fi, _ := os.Stat(filepath.Join(root, "TGT-1")); fi.Mode().Perm() != 0o700 {
		t.Fatalf("folder mode %v", fi.Mode())
	}
	// A repeat download of the same attachment replaces the file.
	if l := GatewayDownload(context.Background(), target(gw.URL, true), "youtrack", "attachment_get", []byte(`{}`), Download{Root: root, Folder: []string{"TGT-1"}, Prefix: "85-1"}); l.Outcome != CallOutcomeOk {
		t.Fatalf("repeat: %s", code(l))
	}
	// A folder argument that is a path.
	if l := GatewayDownload(context.Background(), target(gw.URL, true), "y", "g", []byte(`{}`), Download{Root: root, Folder: []string{".."}, Prefix: "1"}); code(l) != "bad-request" {
		t.Fatalf("..: %s", code(l))
	}
	// Symlinks planted below the root: the attachments folder, the issue folder, the part file and the final name.
	outside := t.TempDir()
	gw = fileServer(t, "shot.png", body, -1)
	input := t.TempDir()
	os.Symlink(outside, filepath.Join(input, "attachments"))
	os.MkdirAll(filepath.Join(root, "attachments"), 0o700)
	os.Symlink(outside, filepath.Join(root, "attachments", "TGT-2"))
	os.MkdirAll(filepath.Join(root, "attachments", "TGT-3"), 0o700)
	os.Symlink(filepath.Join(outside, "final"), filepath.Join(root, "attachments", "TGT-3", "1-shot.png"))
	os.MkdirAll(filepath.Join(root, "attachments", "TGT-4"), 0o700)
	os.Symlink(filepath.Join(outside, "part"), filepath.Join(root, "attachments", "TGT-4", "1-shot.png.fixed.part"))
	random := partTag
	partTag = func() string { return "fixed" }
	defer func() { partTag = random }()
	for _, c := range []struct {
		root, issue, want string
	}{{input, "TGT-1", "path-refused"}, {root, "TGT-2", "path-refused"}, {root, "TGT-3", "path-refused"}, {root, "TGT-4", "write-failed"}} {
		l := GatewayDownload(context.Background(), target(gw.URL, true), "y", "g", []byte(`{}`), Download{Root: c.root, Folder: []string{"attachments", c.issue}, Prefix: "1"})
		if code(l) != c.want {
			t.Fatalf("%s %s: %s", c.root, c.issue, code(l))
		}
	}
	if got := entries(t, outside); len(got) != 0 {
		t.Fatalf("written outside: %v", got)
	}
}

// Requirement: §9 download writer: a stream that ends early leaves neither the final file nor its part file. Risk: an
// agent opens a truncated screenshot as the whole file.
func TestDownloadCutLeavesNoFile(t *testing.T) {
	body := bytes.Repeat([]byte("y"), 1<<20)
	root := t.TempDir()
	gw := fileServer(t, "shot.png", body, 1000)
	l := GatewayDownload(context.Background(), target(gw.URL, true), "youtrack", "attachment_get", []byte(`{}`), Download{Root: root, Folder: []string{"TGT-1"}, Prefix: "85-1"})
	if l.Outcome != CallOutcomeError || code(l) != "download-cut" || l.Transfer == nil || l.Transfer.Bytes != 1000 {
		t.Fatalf("%s %s", l.Outcome, code(l))
	}
	if got := entries(t, root); len(got) != 0 {
		t.Fatalf("left %v", got)
	}
	// A refusal before the stream is the gateway's JSON answer.
	gw = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Fleet-Call-Id", "c2")
		w.Header().Set("X-Fleet-Outcome", "error")
		w.Header().Set("X-Fleet-Error", "reply-too-large")
		w.WriteHeader(413)
		io.WriteString(w, `{"error":"reply-too-large"}`)
	}))
	defer gw.Close()
	if l := GatewayDownload(context.Background(), target(gw.URL, true), "y", "g", []byte(`{}`), Download{Root: root, Folder: []string{"TGT-1"}, Prefix: "1"}); code(l) != "reply-too-large" || l.CallId != "c2" {
		t.Fatalf("refusal: %s", code(l))
	}
}

// Requirement: §9 download names: Cyrillic names with spaces stay readable, a leading dot and separators become '_',
// the name is cut to 200 bytes on a rune boundary. Risk: a hidden file, a path, or a broken UTF-8 name on disk.
func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{"запись экрана.mov": "запись экрана.mov", ".env": "_env", "a/b\\c": "a_b_c", "": "file", strings.Repeat("я", 150): strings.Repeat("я", 100), strings.Repeat("я", 150) + ".png": strings.Repeat("я", 98) + ".png"} {
		if got := SafeName(in); got != want {
			t.Fatalf("%q: %q", in, got)
		}
	}
}
