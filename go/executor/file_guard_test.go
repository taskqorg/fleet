package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"

	"fleet/contract"
)

func write(t *testing.T, path string, data string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func guardRefused(t *testing.T, path, base, why string) {
	t.Helper()
	f, _, err := openForUpload(path, base)
	if err == nil {
		f.Close()
		t.Fatalf("%s: opened %s", why, path)
	}
	if _, ok := err.(guardError); !ok {
		t.Fatalf("%s: %v", why, err)
	}
}

// Requirement: §9 path guard (owner decision OD2 = B): an upload opens one regular file with a single link, outside
// every credential store in code, inside FLEET_FILES_ROOTS when set; links are resolved first and checked by device
// and inode. Risk: a prompt-injected agent attaches ~/.ssh/id_ed25519, a harness login or Fleet's own token file to an
// issue through a symlink, a hard link or a relative path.
func TestGuardRefuses(t *testing.T) {
	home, work, outside := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	claudeDir, codexDir := t.TempDir(), t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	t.Setenv("CODEX_HOME", codexDir)
	t.Setenv("FLEET_TOKEN_FILE", write(t, filepath.Join(outside, "owner-token"), "tok"))
	t.Setenv("FLEET_CF_ACCESS_FILE", write(t, filepath.Join(outside, "access.env"), "CF_ACCESS_CLIENT_SECRET=s"))
	gitConnections := filepath.Join(outside, "forge-keys")
	t.Setenv("FLEET_CONNECTIONS_DIR", gitConnections)
	gitKey := write(t, filepath.Join(gitConnections, "repo", "id_ed25519"), "invented key")
	guardRefused(t, gitKey, work, gitConnections)
	t.Setenv("FLEET_FILES_ROOTS", "")
	runs := filepath.Join(outside, "runs")
	runFilePath = write(t, filepath.Join(runs, "exec-1.env"), "FLEET_RUN_TOKEN=x")
	t.Cleanup(func() { runFilePath = "" })
	other := write(t, filepath.Join(runs, "exec-2.env"), "FLEET_RUN_TOKEN=y")

	ok := write(t, filepath.Join(work, "shot.png"), "png")
	f, info, err := openForUpload("shot.png", work)
	if err != nil || info.Size() != 3 {
		t.Fatalf("a plain file in the work folder: %v", err)
	}
	f.Close()

	for _, p := range []string{
		filepath.Join(home, ".ssh/id_ed25519"), filepath.Join(home, ".aws/credentials"), filepath.Join(home, ".gnupg/private-keys-v1.d/k.key"),
		filepath.Join(home, ".netrc"), filepath.Join(home, ".config/gh/hosts.yml"), filepath.Join(home, ".config/fleet/tokens/mac-claude"),
		filepath.Join(home, ".hermes/.env"), filepath.Join(home, "Library/Keychains/login.keychain-db"),
		filepath.Join(home, ".claude/.credentials.json"), filepath.Join(home, ".codex/auth.json"),
		filepath.Join(claudeDir, ".credentials.json"), filepath.Join(codexDir, "auth.json"),
	} {
		write(t, p, "secret")
		guardRefused(t, p, work, p)
	}
	for _, p := range []string{os.Getenv("FLEET_TOKEN_FILE"), os.Getenv("FLEET_CF_ACCESS_FILE"), runFilePath, other} {
		guardRefused(t, p, work, p)
	}
	// In a run HOME is the harness's home: the run file names the executor user's home and the executor's own stores
	// (data folder with the journal, host secret). The default data folder and the gateway's state are covered too.
	userHome, data := t.TempDir(), t.TempDir()
	t.Setenv("FLEET_USER_HOME", userHome)
	t.Setenv("FLEET_RUN_DENY", enc([]any{data, filepath.Join(outside, "host-secret")}))
	dataDefault := ".local/state/fleet/executor/journal.sqlite"
	if runtime.GOOS == "darwin" {
		dataDefault = "Library/Application Support/Fleet/executor/journal.sqlite"
	}
	for _, p := range []string{filepath.Join(userHome, ".ssh/id_ed25519"), filepath.Join(userHome, ".config/fleet/host-secret"),
		filepath.Join(data, "journal.sqlite"), write(t, filepath.Join(outside, "host-secret"), "s"),
		filepath.Join(home, dataDefault), filepath.Join(home, ".local/share/fleet/gateway/state/spool/d.jsonl")} {
		write(t, p, "secret")
		guardRefused(t, p, work, p)
	}
	// Reached through a symlink in the work folder, a symlinked folder, or a relative path.
	os.Symlink(filepath.Join(home, ".ssh/id_ed25519"), filepath.Join(work, "innocent.png"))
	guardRefused(t, "innocent.png", work, "symlink to a key")
	os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(work, "keys"))
	guardRefused(t, "keys/id_ed25519", work, "symlinked folder")
	rel, _ := filepath.Rel(work, filepath.Join(home, ".netrc"))
	guardRefused(t, rel, work, "relative path")
	// A hard link to a credential, a FIFO, a directory.
	os.Link(filepath.Join(home, ".codex/auth.json"), filepath.Join(work, "auth-copy.json"))
	guardRefused(t, "auth-copy.json", work, "hard link")
	syscall.Mkfifo(filepath.Join(work, "pipe"), 0o600)
	guardRefused(t, "pipe", work, "FIFO")
	guardRefused(t, outside, work, "directory")
	guardRefused(t, "missing.png", work, "missing")
	// A file swapped for a symlink between resolving and opening.
	guardBeforeOpen = func() { os.Remove(ok); os.Symlink(filepath.Join(home, ".ssh/id_ed25519"), ok) }
	guardRefused(t, "shot.png", work, "swapped for a symlink")
	// Sessions and worktrees inside the executor's data folder stay uploadable; its stores do not.
	if runtime.GOOS == "darwin" {
		shot := write(t, filepath.Join(home, "Library/Application Support/Fleet/executor/sessions/s1/worktree/out.png"), "png")
		if f, _, err := openForUpload(shot, work); err != nil {
			t.Fatalf("a worktree file in the data folder: %v", err)
		} else {
			f.Close()
		}
	}
	// A folder that may be entered but not listed (mode 0311) still lets its readable file through.
	hidden := write(t, filepath.Join(work, "searchonly", "s.png"), "s")
	os.Chmod(filepath.Dir(hidden), 0o311)
	t.Cleanup(func() { os.Chmod(filepath.Dir(hidden), 0o700) })
	if f, _, err := openForUpload(hidden, work); err != nil {
		t.Fatalf("a file under a search-only folder: %v", err)
	} else {
		f.Close()
	}
	// A root of / holds every path.
	t.Setenv("FLEET_FILES_ROOTS", "/")
	if f, _, err := openForUpload(write(t, filepath.Join(work, "plain.png"), "p"), work); err != nil {
		t.Fatalf("root /: %v", err)
	} else {
		f.Close()
	}
	t.Setenv("FLEET_FILES_ROOTS", "")
	// A folder on the path swapped for a symlink to the key folder: the walk opens each folder without following links.
	decoy := write(t, filepath.Join(work, "sub", "id_ed25519"), "decoy")
	guardBeforeOpen = func() {
		os.RemoveAll(filepath.Dir(decoy))
		os.Symlink(filepath.Join(home, ".ssh"), filepath.Dir(decoy))
	}
	guardRefused(t, "sub/id_ed25519", work, "folder swapped for a symlink")
	guardBeforeOpen = func() {}
	// Code mode: a bridged upload reads only inside the sandbox's read roots.
	uploadRoots = []string{filepath.Join(work, "allowed")}
	write(t, filepath.Join(work, "allowed", "a.txt"), "a")
	write(t, filepath.Join(work, "elsewhere.txt"), "e")
	guardRefused(t, "elsewhere.txt", work, "outside the code read roots")
	if f, _, err := openForUpload("allowed/a.txt", work); err != nil {
		t.Fatalf("inside the code read roots: %v", err)
	} else {
		f.Close()
	}
	uploadRoots = nil
	// Roots: a link inside a root to a file outside it, and .. that leaves the root.
	t.Setenv("FLEET_FILES_ROOTS", work)
	write(t, filepath.Join(outside, "notes.txt"), "n")
	os.Symlink(filepath.Join(outside, "notes.txt"), filepath.Join(work, "notes.txt"))
	guardRefused(t, "notes.txt", work, "symlink out of the root")
	guardRefused(t, "../"+filepath.Base(outside)+"/notes.txt", work, ".. out of the root")
	write(t, filepath.Join(work, "in.txt"), "i")
	if f, _, err := openForUpload("in.txt", work); err != nil {
		t.Fatalf("inside the root: %v", err)
	} else {
		f.Close()
	}
}

// fileGateway is a fake gateway for the file modes: it counts requests, checks an upload's raw body against its
// headers and answers a download with body.
type fileGateway struct {
	*httptest.Server
	requests atomic.Int64
	args     map[string]any
	sha      string
}

func newFileGateway(t *testing.T, body []byte) *fileGateway {
	g := &fileGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.requests.Add(1)
		w.Header().Set("X-Fleet-Call-Id", "c1")
		switch r.URL.Path {
		case "/v1/call/youtrack/attachment_add":
			b, _ := io.ReadAll(r.Body)
			sum := sha256.Sum256(b)
			raw, _ := base64.RawURLEncoding.DecodeString(r.Header.Get("X-Fleet-Args"))
			json.Unmarshal(raw, &g.args)
			g.sha = hex.EncodeToString(sum[:])
			if r.Header.Get("Content-Type") != "application/octet-stream" || r.Header.Get("X-Fleet-Sha256") != g.sha || r.ContentLength != int64(len(b)) {
				w.WriteHeader(400)
				return
			}
			io.WriteString(w, `[{"id":"85-7","name":"shot.png","size":3,"mimeType":"image/png"}]`)
		case "/v1/call/youtrack/attachment_get":
			meta, _ := json.Marshal(map[string]any{"name": "screen.png", "mediaType": "image/png", "bytes": len(body)})
			w.Header().Set("X-Fleet-File", base64.RawURLEncoding.EncodeToString(meta))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.Write(body)
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

var fileSpec = M{"name": "youtrack", "instructions": "i", "tools": []any{
	M{"name": "issue_get", "description": "d", "inputSchema": M{"type": "object"}, "idempotent": true},
	M{"name": "attachment_add", "description": "d", "inputSchema": M{"type": "object", "required": []any{"issue", "path"}}, "idempotent": false, "file": "upload"},
	M{"name": "attachment_get", "description": "d", "inputSchema": M{"type": "object", "required": []any{"issue", "attachment"}}, "idempotent": true, "file": "download"},
}}

// mcpSession sends requests to connectionMCP and answers the replies by id.
func mcpSession(t *testing.T, requests ...M) map[int]M {
	var in bytes.Buffer
	for i, r := range requests {
		r["jsonrpc"], r["id"] = "2.0", strconv.Itoa(i+1)
		in.WriteString(enc(r) + "\n")
	}
	var out bytes.Buffer
	if err := connectionMCP("youtrack", jClone(fileSpec), &in, &out); err != nil {
		t.Fatal(err)
	}
	replies := map[int]M{}
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		m := decM(sc.Text())
		id, _ := strconv.Atoi(jStr(m, "id"))
		replies[id] = m
	}
	return replies
}

func resultText(r M) string { return jStr(jArr(r, "result", "content")[0], "text") }

// Requirement: §9 the client: tools/list hides file and idempotent; an upload sends the work-folder file's raw bytes
// with X-Fleet-Args (path fields, name, mediaType) and its sha256; a download lands in
// FLEET_INPUT_DIR/attachments/<issue>/<attachment>-<name> and answers {path, name, bytes, mimeType, sha256, callId}.
// Risk: the model sees client flags, a relative path read from the server's own folder, or a download outside the
// run's input folder.
func TestConnectionFileTools(t *testing.T) {
	body := []byte("\x89PNG screen")
	gw := newFileGateway(t, body)
	work, input := t.TempDir(), t.TempDir()
	t.Setenv("FLEET_GATEWAY_URL", gw.URL)
	t.Setenv("FLEET_RUN_TOKEN", "invented-run-token")
	t.Setenv("FLEET_WORK_DIR", work)
	t.Setenv("FLEET_INPUT_DIR", input)
	write(t, filepath.Join(work, "out", "shot.png"), "png")
	r := mcpSession(t, M{"method": "tools/list"},
		M{"method": "tools/call", "params": M{"name": "attachment_add", "arguments": M{"issue": "TGT-1", "path": "out/shot.png"}}},
		M{"method": "tools/call", "params": M{"name": "attachment_get", "arguments": M{"issue": "TGT-1", "attachment": "85-7"}}})
	for _, tool := range jArr(r[1], "result", "tools") {
		if has(tool, "file") || has(tool, "idempotent") || jStr(tool, "name") == "" {
			t.Fatalf("listed %v", tool)
		}
	}
	up := decM(resultText(r[2]))
	if jAt(r[2], "result", "isError") != false || gw.args["issue"] != "TGT-1" || gw.args["name"] != "shot.png" || gw.args["mediaType"] != "image/png" || len(gw.args) != 3 ||
		jStr(up, "sha256") != gw.sha || jStr(up, "callId") != "c1" || jAt(up, "reply") == nil {
		t.Fatalf("upload: %s, args %v", resultText(r[2]), gw.args)
	}
	down := decM(resultText(r[3]))
	want := filepath.Join(input, "attachments", "TGT-1", "85-7-screen.png")
	sum := sha256.Sum256(body)
	if jAt(r[3], "result", "isError") != false || jStr(down, "path") != want || jStr(down, "mimeType") != "image/png" || jStr(down, "sha256") != hex.EncodeToString(sum[:]) ||
		enc(jAt(down, "bytes")) != strconv.Itoa(len(body)) || jStr(down, "name") != "screen.png" {
		t.Fatalf("download: %s", resultText(r[3]))
	}
	if b, _ := os.ReadFile(want); !bytes.Equal(b, body) {
		t.Fatal("downloaded bytes")
	}
	// Outside a Fleet run the download goes to FLEET_FILES_OUT.
	t.Setenv("FLEET_INPUT_DIR", "")
	out := t.TempDir()
	t.Setenv("FLEET_FILES_OUT", out)
	r = mcpSession(t, M{"method": "tools/call", "params": M{"name": "attachment_get", "arguments": M{"issue": "TGT-1", "attachment": "85-7"}}})
	if p := jStr(decM(resultText(r[1])), "path"); p != filepath.Join(out, "TGT-1", "85-7-screen.png") {
		t.Fatalf("owner host download: %s", resultText(r[1]))
	}
}

// Requirement: §9 client tripwire: a file holding the caller's own bearer (also split across a read boundary) or the
// Access secret is refused before any request. Risk: an agent uploads its own token or the machine's Access secret.
func TestClientTripwire(t *testing.T) {
	gw := newFileGateway(t, nil)
	work := t.TempDir()
	t.Setenv("FLEET_WORK_DIR", work)
	t.Setenv("FLEET_RUN_TOKEN", "invented-run-token-0123456789")
	access := write(t, filepath.Join(t.TempDir(), "access.env"), "CF_ACCESS_CLIENT_ID=id\nCF_ACCESS_CLIENT_SECRET=invented-access-secret\n")
	t.Setenv("FLEET_CF_ACCESS_FILE", access)
	tool := fileSpec["tools"].([]any)[1]
	split := strings.Repeat("a", 256<<10-5) + "invented-run-token-0123456789" + strings.Repeat("b", 100)
	write(t, filepath.Join(work, "split.log"), split)
	write(t, filepath.Join(work, "access.txt"), "x invented-access-secret y")
	write(t, filepath.Join(work, "clean.txt"), "clean")
	for url, files := range map[string][]string{gw.URL: {"split.log", "access.txt"}, "https://" + gw.Listener.Addr().String(): {"split.log", "access.txt"}} {
		t.Setenv("FLEET_GATEWAY_URL", url)
		for _, f := range files {
			l := connectionCall("youtrack", tool, M{"issue": "TGT-1", "path": f})
			if l.Outcome != contract.CallOutcomeError || *l.ErrorCode != "secret-in-file" {
				t.Fatalf("%s %s: %s", url, f, enc(l))
			}
		}
	}
	if gw.requests.Load() != 0 {
		t.Fatal("a refused file reached the gateway")
	}
	t.Setenv("FLEET_GATEWAY_URL", gw.URL)
	if l := connectionCall("youtrack", tool, M{"issue": "TGT-1", "path": "clean.txt"}); l.Outcome != contract.CallOutcomeOk || gw.requests.Load() != 1 {
		t.Fatalf("clean file: %s", enc(l))
	}
}

// Requirement: §9 `connection call` CLI replaces connection_call.py call: JSON arguments on stdin, one CallLine on stdout;
// with a spec file (a token-create reply here) a file tool uses its file mode, without one the tool is called as JSON.
// Risk: scripts keep the Python client, or the CLI sends a file path to an upload tool as JSON.
func TestConnectionCLI(t *testing.T) {
	gw := newFileGateway(t, nil)
	work := t.TempDir()
	t.Setenv("FLEET_GATEWAY_URL", gw.URL)
	t.Setenv("FLEET_RUN_TOKEN", "")
	t.Setenv("FLEET_TOKEN_FILE", write(t, filepath.Join(t.TempDir(), "token"), "invented-owner-token\n"))
	t.Setenv("FLEET_WORK_DIR", work)
	write(t, filepath.Join(work, "shot.png"), "png")
	spec := write(t, filepath.Join(t.TempDir(), "mac.spec"), enc(M{"name": "mac", "token": nil, "connections": []any{fileSpec}}))
	var out bytes.Buffer
	ok, err := connectionCommand([]string{"call", "youtrack", "issue_get"}, strings.NewReader(`{"issue":"TGT-1"}`), &out)
	var line contract.CallLine
	json.Unmarshal(out.Bytes(), &line)
	if err != nil || !ok || line.CallId != "c1" || string(line.Output) != `{"ok":true}` {
		t.Fatalf("json call: %v %s", err, out.String())
	}
	out.Reset()
	ok, err = connectionCommand([]string{"call", "youtrack", "attachment_add", spec}, strings.NewReader(`{"issue":"TGT-1","path":"shot.png"}`), &out)
	json.Unmarshal(out.Bytes(), &line)
	if err != nil || !ok || line.Transfer == nil || line.Transfer.Direction != contract.CallTransferDirectionUpload || gw.args["name"] != "shot.png" {
		t.Fatalf("upload: %v %s", err, out.String())
	}
	if _, err := connectionCommand([]string{"call", "youtrack", "nope", spec}, strings.NewReader(`{}`), &out); err == nil {
		t.Fatal("a tool missing from the spec file")
	}
}

// Requirement: §9 path guard in a run: the run file anchors the denylist on the executor user's home, the executor's
// stores and every registry account's login folders, and carries the executor's FLEET_FILES_ROOTS. Risk: an agent whose
// HOME is the harness home attaches another account's Claude or Codex login, or the journal with every run token.
func TestUploadDenyLines(t *testing.T) {
	dir := t.TempDir()
	native, credential := filepath.Join(dir, "b-native"), filepath.Join(dir, "b-home")
	registry := write(t, filepath.Join(dir, "accounts.json"), enc(M{"b": M{"harness": "claude", "home": filepath.Join(dir, "b"), "nativeHome": native, "credentialHome": credential}}))
	t.Setenv("FLEET_NATIVE_ACCOUNTS", registry)
	t.Setenv("FLEET_CODEX_HOME", filepath.Join(dir, "codex"))
	t.Setenv("FLEET_FILES_ROOTS", "/srv/share")
	lines := uploadDenyLines(filepath.Join(dir, "data"))
	for _, want := range []string{native, filepath.Join(credential, ".claude/.credentials.json"), filepath.Join(dir, "data", "journal.sqlite"),
		filepath.Join(dir, "data", "runs"), filepath.Join(dir, "codex"), registry, "FLEET_FILES_ROOTS=/srv/share\n", "FLEET_USER_HOME="} {
		if !strings.Contains(lines, want) {
			t.Fatalf("%s missing from %s", want, lines)
		}
	}
	if strings.Contains(lines, `"`+filepath.Join(dir, "data")+`"`) {
		t.Fatal("the whole data folder (sessions and worktrees) is denied")
	}
}
