package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// R3/R5; risk: startup evidence disappears with temporary ownership state or retains raw credentials/errors.
func TestMCPDurableSafeReceiptsAndCollection(t *testing.T) {
	for _, failure := range []string{"socket", "registration", "binding", "server-exit", "eof"} {
		t.Run(failure, func(t *testing.T) {
			o, e := testMCPOwner(t)
			session, _ := filepath.EvalSymlinks(t.TempDir())
			execution := "a1b2c3d4-5678-4abc-8def-0123456789ab"
			directory := filepath.Join(session, execution+".mcp")
			marker := filepath.Join(t.TempDir(), "starts")
			spec := fixtureSpec("navigate")
			switch failure {
			case "binding":
				spec = M{"command": "/absent/SECRET_SENTINEL_DO_NOT_RETAIN", "args": []any{}}
			case "server-exit":
				spec = fixtureSpec("diagnostic-exit", marker)
			}
			wrapped, err := wrapMCPServers(M{"fixture": spec}, o.socket, session, directory)
			if err != nil {
				t.Fatal(err)
			}
			launch := jObj(wrapped["fixture"])
			args := []string{}
			for _, a := range jArr(launch, "args") {
				args = append(args, jStr(a))
			}
			if failure == "socket" {
				args[2] = filepath.Join(o.dir, "missing.sock")
			}
			if failure == "registration" {
				if _, err := o.db.Exec("UPDATE launches SET mcp_fenced=1 WHERE execution='execution'"); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(jStr(launch, "command"), args...)
			cmd.Env = mcpChildEnv(os.Getenv("PATH"))
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			in, _ := cmd.StdinPipe()
			out, _ := cmd.StdoutPipe()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			json.NewEncoder(in).Encode(M{"id": 1, "method": "initialize"})
			var reply M
			decoded := json.NewDecoder(out).Decode(&reply)
			in.Close()
			exit := cmd.Wait()
			if failure == "eof" {
				if decoded != nil || exit != nil {
					t.Fatal(decoded, exit, stderr.String())
				}
			} else if decoded == nil || exit == nil {
				t.Fatal("failure hidden", decoded, exit, reply)
			}
			o.close()
			if err := cleanupMCPGroups(o.db, "execution"); err != nil {
				t.Fatal(err)
			}
			removeMCPOwnerDir(o.dir)
			files, err := os.ReadDir(directory)
			if err != nil || len(files) != 1 {
				t.Fatal(files, err)
			}
			file := filepath.Join(directory, files[0].Name())
			st, _ := os.Stat(file)
			if st.Mode().Perm() != 0600 {
				t.Fatal(st.Mode())
			}
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "SECRET_SENTINEL_DO_NOT_RETAIN") {
				t.Fatal("raw secret/error retained")
			}
			decoder := json.NewDecoder(bytes.NewReader(raw))
			var first, last M
			if err := decoder.Decode(&first); err != nil {
				t.Fatal(err)
			}
			if err := decoder.Decode(&last); err != nil {
				t.Fatal(err)
			}
			var extra M
			if decoder.Decode(&extra) != io.EOF {
				t.Fatal("unexpected receipt shape")
			}
			if first["returnedError"] != nil || jStr(first, "status", "outcome") != "unknown" || jStr(last, "provenance") != "runtime-sidecar-observation" {
				t.Fatal(first, last)
			}
			switch failure {
			case "socket":
				if jStr(last, "phase") != "socket" || jStr(last, "status", "provenance") != "status-unavailable" {
					t.Fatal(last)
				}
			case "registration":
				if jStr(last, "status", "registration") != "refused" {
					t.Fatal(last)
				}
			case "binding":
				if jStr(last, "status", "stage") != "binding" {
					t.Fatal(last)
				}
			case "server-exit":
				if jStr(last, "status", "stage") != "server-exit" || jAt(last, "status", "exitCode") != float64(23) {
					t.Fatal(last)
				}
			case "eof":
				if last["returnedError"] != false {
					t.Fatal(last)
				}
			}
			// Same safe file reaches the existing upload spool after all temporary MCP state was removed.
			e.root = t.TempDir()
			os.Mkdir(filepath.Join(e.root, "spool"), 0700)
			var declared []string
			if err := e.artifacts(session, []string{execution + ".mcp"}, execution, &declared); err != nil {
				t.Fatal(err)
			}
			var body string
			if err := e.db.QueryRow("SELECT body FROM transfers WHERE kind='upload'").Scan(&body); err != nil {
				t.Fatal(err)
			}
			upload, err := dec(body)
			if err != nil {
				t.Fatal(err)
			}
			retained, err := os.ReadFile(jStr(upload, "path"))
			if err != nil || !bytes.Equal(retained, raw) || len(declared) != 1 {
				t.Fatal("receipt not retained", err, declared)
			}
		})
	}
}

// R3; risk: diagnostics overwrite data or follow symlinks/traversal/untrusted ancestor directories.
func TestMCPReceiptPathBoundaryAndUniqueInvocation(t *testing.T) {
	parent, _ := filepath.EvalSymlinks(t.TempDir())
	os.Chmod(parent, 0700)
	prefix := filepath.Join(parent, "receipt")
	first, err := openMCPReceipt(prefix)
	if err != nil {
		t.Fatal(err)
	}
	first.Write([]byte("first"))
	first.Close()
	second, err := openMCPReceipt(prefix)
	if err != nil {
		t.Fatal(err)
	}
	second.Write([]byte("second"))
	second.Close()
	files, _ := os.ReadDir(parent)
	if len(files) != 2 {
		t.Fatal("invocations reused a path", files)
	}
	seen := map[string]bool{}
	for _, file := range files {
		b, _ := os.ReadFile(filepath.Join(parent, file.Name()))
		seen[string(b)] = true
	}
	if !seen["first"] || !seen["second"] {
		t.Fatal("overwritten receipt")
	}
	target := filepath.Join(parent, "target")
	os.Mkdir(target, 0700)
	link := filepath.Join(parent, "link")
	os.Symlink(target, link)
	unsafe := filepath.Join(parent, "untrusted")
	os.Mkdir(unsafe, 0777)
	os.Chmod(unsafe, 0777)
	for _, path := range []string{"relative", parent + "/target/../receipt", filepath.Join(link, "receipt"), filepath.Join(unsafe, "receipt")} {
		if f, err := openMCPReceipt(path); err == nil {
			f.Close()
			t.Fatal("unsafe receipt accepted", path)
		}
	}
	os.WriteFile(prefix, []byte("preserve"), 0600)
	if f, err := openMCPReceipt(prefix); err == nil {
		f.Close()
		t.Fatal("existing prefix overwritten")
	}
}

// R3/R5; risk: a failed real script loses its safe MCP receipt before normal execution artifact collection.
func TestMCPFailedScriptCollectsSafeReceipt(t *testing.T) {
	e, root := scriptExecutor(t)
	execution := "a1b2c3d4-5678-4abc-8def-0123456789ab"
	session := "receipt-script"
	directory := filepath.Join(e.sessionDir(session), execution+".mcp")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "starts")
	spec := fixtureSpec("diagnostic-exit", marker)
	spec["receiptPath"] = filepath.Join(directory, "mcp-lifecycle-fixture")
	// Keep the client input open until the server exits; a here-doc EOF intentionally fences a pending child.
	fifo := filepath.Join(root, "client-input")
	if err := syscall.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	command := fmt.Sprintf("exec 3<>%q; printf '{\"id\":1,\"method\":\"initialize\"}\\n' >&3; %q mcp stdio \"$FLEET_MCP_OWNER\" %q %q <&3", fifo, exe(), enc(spec), root)
	if err := e.run(M{"id": "receipt-command", "executionId": execution, "sessionId": session, "kind": "script", "payload": M{"command": command}}); err != nil {
		t.Fatal(err)
	}
	settle(t, e, execution)
	if lastReceipt(e, "receipt-command") != "failed" {
		t.Fatal("diagnostics changed primary failure", lastReceipt(e, "receipt-command"))
	}
	rows, err := e.db.Query("SELECT body FROM transfers WHERE kind='upload'")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var body string
		rows.Scan(&body)
		upload, _ := dec(body)
		if strings.HasPrefix(jStr(upload, "name"), "mcp-lifecycle-fixture-") {
			raw, err := os.ReadFile(jStr(upload, "path"))
			if err != nil || strings.Contains(string(raw), "SECRET_SENTINEL_DO_NOT_RETAIN") || !strings.Contains(string(raw), "server-exit") || !strings.Contains(string(raw), execution) {
				t.Fatal("missing or unsafe retained receipt", err, string(raw))
			}
			found = true
		}
	}
	if !found {
		t.Fatal("failed execution did not enqueue safe receipt")
	}
	starts, _ := os.ReadFile(marker)
	if len(strings.Fields(string(starts))) != 1 {
		t.Fatal("duplicate or missing server start", string(starts))
	}
	var launches, groups int
	e.db.QueryRow("SELECT count(*) FROM launches WHERE execution=?", execution).Scan(&launches)
	e.db.QueryRow("SELECT count(*) FROM mcp_groups WHERE execution=?", execution).Scan(&groups)
	if launches != 0 || groups != 0 {
		t.Fatal("owned cleanup incomplete", launches, groups)
	}
}
