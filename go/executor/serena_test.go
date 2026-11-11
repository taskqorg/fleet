package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// R3; risk: another harness can call tools hidden only by Codex client flags, or switch project scope.
func TestSerenaServerGrantFiltersEveryHarness(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.go")
	os.WriteFile(outside, []byte("package outside"), 0600)
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skip("symlink unavailable")
	}
	input := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"execute_shell_command","arguments":{}}}
{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"activate_project","arguments":{"project":"foreign"}}}
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"get_symbols_overview","arguments":{"relative_path":"escape"}}}
`)
	tools := []any{M{"name": "execute_shell_command"}}
	for _, name := range serenaTools {
		tools = append(tools, M{"name": name})
	}
	replies := strings.NewReader(enc(M{"jsonrpc": "2.0", "method": "notifications/message", "params": M{"data": "secret-sentinel"}}) + "\n" + enc(M{"jsonrpc": "2.0", "id": 1, "result": M{"tools": tools}}) + "\n")

	var out, upstream bytes.Buffer
	if err := filterSerena(input, &out, &upstream, replies, root); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(upstream.String(), "execute_shell_command") || strings.Contains(upstream.String(), "activate_project") || strings.Contains(upstream.String(), "escape") {
		t.Fatal("forbidden request forwarded", upstream.String())
	}
	if strings.Contains(out.String(), "secret-sentinel") || strings.Contains(out.String(), `"name":"execute_shell_command"`) {
		t.Fatal("ungranted advertisement/log forwarded", out.String())
	}
	if strings.Count(out.String(), `"error"`) != 3 {
		t.Fatal(out.String())
	}
	for _, h := range []string{"codex", "claude", "antigravity", "acp"} {
		if _, err := serenaArguments(root, h); err != nil {
			t.Fatal(h, err)
		}
	}
}

// R1; risk: selected profile is silently dropped, PM selection is refused, or old design-rules wiring changes.
func TestNamedProfileServerMapping(t *testing.T) {
	root := t.TempDir()
	profile := M{"mcpServers": []any{M{"id": "serena", "data": M{"language": "go"}}, M{"id": "design-rules", "data": M{"rule": "retained"}}}}
	for _, h := range []string{"codex", "claude", "antigravity"} {
		servers := profileServers(profile, root, h, "test-account")
		if len(servers) != 2 || jStr(servers, "serena", "command") != exe() || jStr(servers, "design-rules", "command") != exe() {
			t.Fatal(h, servers)
		}
	}
}

// R1; risk: MCP isError=false is mistaken for a working LSP, or missing tools silently degrade a selected capability.
func TestSerenaSemanticFailureAndMissingDependency(t *testing.T) {
	if err := serenaSymbolWitness(`Error executing tool: language server manager is not initialized; gopls is not installed`); err == nil {
		t.Fatal("semantic failure accepted")
	}
	if err := serenaSymbolWitness(`{"Function":["Other"]}`); err == nil {
		t.Fatal("wrong source witness accepted")
	}
	if err := serenaSymbolWitness(`{"Function":["CapabilityWitness"]}`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := serenaDependencies("", "test-account"); err == nil || !strings.Contains(err.Error(), "FLEET_NATIVE_ACCOUNTS") {
		t.Fatal("missing dependency lost", err)
	}
}

// R1; risk: a required startup tool omitted by the installed server is silently treated as ready.
func TestSerenaMissingStartupToolIsActionable(t *testing.T) {
	var output, upstream bytes.Buffer
	err := filterSerena(strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`), &output, &upstream, strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"tools":[]}}`), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var reply M
	if err := json.Unmarshal(output.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(jStr(reply, "error", "message"), "initial_instructions") {
		t.Fatal(output.String())
	}
}

// R3; risk: a project supplies arbitrary tool paths, writable executables, or broadens the native environment.
func TestSerenaTrustedRegistryBinding(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "tool")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "accounts.json")
	binding := M{"executable": executable, "go": executable, "gopls": executable}
	if err := os.WriteFile(registry, []byte(enc(M{"native-account": M{"serena": binding}})), 0600); err != nil {
		t.Fatal(err)
	}
	resolved, err := serenaDependencies(registry, "native-account")
	if err != nil || jStr(resolved, "serena") != func() string { p, _ := filepath.EvalSymlinks(executable); return p }() {
		t.Fatal(resolved, err)
	}
	if _, err := serenaDependencies(registry, "foreign-account"); err == nil {
		t.Fatal("unbound account accepted")
	}
	if err := os.Chmod(executable, 0770); err != nil {
		t.Fatal(err)
	}
	if _, err := serenaDependencies(registry, "native-account"); err == nil {
		t.Fatal("group-writable executable accepted")
	}
}

// R1/R3; risk: native client mapping omits the mandatory manual, or an unselected profile inherits Serena.
func TestSerenaNativeClientSelection(t *testing.T) {
	profile := M{"mcpServers": []any{M{"id": "serena", "data": M{"language": "go"}}}}
	for _, name := range []string{"codex", "claude"} {
		cmd := exec.Command("not-invoked")
		h := harness{name: name}
		h.WireMCP(cmd, profile, profileServers(profile, t.TempDir(), name, "fixture"), nil)
		args := strings.Join(cmd.Args, " ")
		if !strings.Contains(args, "initial_instructions") || !strings.Contains(args, "serena") {
			t.Fatal(name, args)
		}
		plain := exec.Command("not-invoked")
		h.WireMCP(plain, M{}, profileServers(M{}, t.TempDir(), name, "fixture"), nil)
		if strings.Contains(strings.Join(plain.Args, " "), "serena") || len(plain.Env) != 0 {
			t.Fatal("unselected capability leaked", name, plain)
		}
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "fixture.go"), []byte("package fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := serenaAllowed(M{"method": "tools/call", "params": M{"name": "get_symbols_overview", "arguments": M{"relative_path": "fixture.go"}}}, root); err != nil {
		t.Fatal("valid path rejected", err)
	}
}

// R3/R5; risk: PATH discovers a different gopls than the receipt's validated binary.
func TestSerenaChildPathUsesExactBindings(t *testing.T) {
	root := t.TempDir()
	goDir := filepath.Join(root, "go-bin")
	os.Mkdir(goDir, 0700)
	wrong := filepath.Join(goDir, "gopls")
	os.WriteFile(wrong, []byte("#!/bin/sh\necho wrong\n"), 0700)
	selected := filepath.Join(root, "renamed-language-server")
	os.WriteFile(selected, []byte("#!/bin/sh\necho selected\n"), 0700)
	path, cleanup, err := serenaPath(M{"go": filepath.Join(goDir, "go"), "gopls": selected})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	t.Setenv("PATH", path)
	got, err := exec.Command("gopls", "version").Output()
	if err != nil || strings.TrimSpace(string(got)) != "selected" {
		t.Fatal(string(got), err)
	}
}

// R3; risk: a hung MCP response keeps the owned server alive after the native client closes.
func TestSerenaDownstreamCloseStopsHungServer(t *testing.T) {
	cmd := exec.Command("/bin/sleep", "60")
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	client, writer := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- serveSerena(func() error { return cmd.Process.Kill() }, client, io.Discard, in, out, t.TempDir())
	}()
	writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"))
	writer.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatal("lost response not explicit", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("closed client left server response blocked")
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("server was not terminated")
	}
}

// R3; risk: killing the MCP leader leaves its language server running outside the completed probe.
func TestSerenaShutdownReapsOwnedDescendants(t *testing.T) {
	cmd := exec.Command("/bin/sh", "-c", "sleep 60 & echo $!; wait")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stop, err := ownSerena(cmd)
	if err != nil {
		cmd.Process.Kill()
		cmd.Wait()
		t.Fatal(err)
	}
	defer func() { stop(); cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	table, err := processes()
	if err != nil {
		t.Fatal(err)
	}
	if running(table, pid) {
		t.Fatal("owned language-server child survived shutdown", pid)
	}
}
