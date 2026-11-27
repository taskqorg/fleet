package main

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"modernc.org/sqlite"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Test children run the real guardian/stdio entrypoints, not a mocked ownership protocol.
func TestMain(m *testing.M) {
	// The fixtures run this binary as a trusted MCP executable, which must not be group/other writable. Under umask 002
	// (a login shell, a runner with UMask=0002) or a build cache with a group-write default ACL, go test leaves it
	// group-writable and every owned-dependency fixture is refused ("MCP executable must be trusted").
	if self, err := os.Executable(); err == nil {
		if st, err := os.Stat(self); err == nil && st.Mode().Perm()&0022 != 0 {
			os.Chmod(self, st.Mode().Perm()&^0022)
		}
	}
	if mcpDependencyInvocation() {
		main()
		os.Exit(0)
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp", "mcp-guardian", "qualify-mcp", "code-shell", "run-worker":
			main()
			os.Exit(0)
		case "mcp-fixture":
			mcpFixture()
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

// Readers treat marker existence as readiness; publish only complete contents.
func fixtureMarker(path string, data []byte, mode os.FileMode) {
	tmp := path + ".tmp-" + strconv.Itoa(os.Getpid())
	if err := os.WriteFile(tmp, data, mode); err != nil {
		panic(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		panic(err)
	}
}

func mcpFixture() {
	tool := os.Args[2]
	if tool == "home" {
		h := os.Getenv("HOME")
		st, err := os.Stat(h)
		if err != nil {
			os.Exit(4)
		}
		os.WriteFile(filepath.Join(h, "cache-proof"), []byte("owned"), 0600)
		fixtureMarker(os.Args[3], []byte(enc(M{"home": h, "mode": int(st.Mode().Perm()), "ambientConfig": exists(filepath.Join(h, ".serena", "serena_config.yml")), "serenaHomeEnv": os.Getenv("SERENA_HOME") != ""})), 0600)
	}

	if strings.HasPrefix(tool, "diagnostic-") {
		f, err := os.OpenFile(os.Args[3], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		fmt.Fprintln(f, os.Getpid())
		f.Close()
		fmt.Fprintln(os.Stderr, "SECRET_SENTINEL_DO_NOT_RETAIN")
		if tool == "diagnostic-exit" || tool == "diagnostic-signal" {
			var request M
			json.NewDecoder(os.Stdin).Decode(&request)
			if tool == "diagnostic-signal" {
				syscall.Kill(os.Getpid(), syscall.SIGTERM)
				select {}
			}
			os.Exit(23)
		}
	}

	if tool == "version" {
		fixtureMarker(os.Args[3], []byte(enc(M{"home": os.Getenv("HOME")})), 0600)
		fmt.Fprintln(os.Stdout, "gopls-fixture v1")
		return
	}
	if tool == "dependency" {
		payload := M{"pid": os.Getpid(), "argv": os.Args[4:], "home": os.Getenv("HOME")}
		fixtureMarker(os.Args[3], []byte(enc(payload)), 0600)
		io.Copy(io.Discard, os.Stdin)
		time.Sleep(60 * time.Second)
		return
	}
	if tool == "escape" || tool == "escape-hold" {
		probe := exec.Command("gopls", "mcp-fixture", "version", os.Args[4])
		probe.Stdin = os.Stdin
		data, err := probe.Output()
		if err != nil || string(data) != "gopls-fixture v1\n" {
			os.Exit(4)
		}
		line, e := bufio.NewReader(os.Stdin).ReadString('\n')
		if e != nil || !strings.Contains(line, "initialize") {
			os.Exit(9)
		}
		cmd := exec.Command("gopls", "mcp-fixture", "dependency", os.Args[3], "literal spaces", "$(must-not-execute)")
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			os.Exit(5)
		}
		defer stdin.Close()
		if cmd.Start() != nil {
			os.Exit(6)
		}
		for i := 0; i < 1500; i++ { // 30 s: a -race process chain on a slow CPU starts in about 4 s
			if exists(os.Args[3]) {
				if tool == "escape-hold" {
					io.Copy(io.Discard, os.Stdin)
				}
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		os.Exit(7)
	}
	if tool == "lookup" {
		for !exists(os.Args[4]) {
			time.Sleep(10 * time.Millisecond)
		}
		answer, c, err := mcpOwnerCall(os.Getenv("FLEET_MCP_OWNER"), M{"action": "lookup", "generation": os.Getenv("FLEET_MCP_GENERATION"), "dependency": "gopls"})
		if c != nil {
			c.Close()
		}
		fixtureMarker(os.Args[3], []byte(enc(M{"accepted": err == nil, "answer": answer})), 0600)
		return
	}

	if tool == "large-response" {
		var req M
		json.NewDecoder(os.Stdin).Decode(&req)
		json.NewEncoder(os.Stdout).Encode(M{"id": req["id"], "result": M{"text": strings.Repeat("x", 2*1024*1024)}})
		fixtureMarker(os.Args[3], []byte(strconv.Itoa(os.Getpid())), 0600)
		for !exists(os.Args[3] + "-exit") {
			time.Sleep(10 * time.Millisecond)
		}
		return
	}
	if tool == "orphan" || tool == "orphan-stdout" {
		cmd := exec.Command("/bin/sleep", "60")
		if tool == "orphan-stdout" {
			cmd.Stdout = os.Stdout
		}
		if cmd.Start() != nil {
			os.Exit(2)
		}
		fixtureMarker(os.Args[3], []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
		return
	}
	if tool == "hold" {
		fixtureMarker(os.Args[3], []byte(strconv.Itoa(os.Getpid())), 0600)
		io.Copy(io.Discard, os.Stdin)
		return
	}
	if tool == "fd" {
		expected := strings.Split(os.Args[3], ",")
		for fd := 3; fd < 128; fd++ {
			var st syscall.Stat_t
			if syscall.Fstat(fd, &st) == nil && strings.Contains(","+strings.Join(expected, ",")+",", ","+strconv.FormatUint(st.Ino, 10)+",") {
				os.Exit(3)
			}
		}
		fixtureMarker(os.Args[4], []byte(strconv.Itoa(os.Getpid())), 0600)
		tool = "witness"
	}
	scanner := bufio.NewScanner(os.Stdin)
	out := json.NewEncoder(os.Stdout)
	callCount := 0
	for scanner.Scan() {
		var req M
		json.Unmarshal(scanner.Bytes(), &req)
		if !has(req, "id") {
			continue
		}
		result := M{}
		switch jStr(req, "method") {
		case "initialize":
			result = M{"protocolVersion": "2024-11-05", "capabilities": M{"tools": M{}}, "serverInfo": M{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = M{"tools": []any{M{"name": tool, "inputSchema": M{"type": "object"}}, M{"name": "forbidden", "inputSchema": M{"type": "object"}}}}
		case "tools/call":
			callCount++
			if tool == "diagnostic-call-exit" && callCount == 2 {
				os.Exit(24)
			}
			result = M{"content": []any{M{"type": "text", "text": tool + "-observed"}}}
		}
		if tool == "diagnostic-call-exit" {
			if jStr(req, "method") == "initialize" {
				result["serverInfo"] = M{"name": "SECRET_SENTINEL_DO_NOT_RETAIN", "version": "SECRET_SENTINEL_DO_NOT_RETAIN"}
			}
			if jStr(req, "method") == "tools/call" {
				result = M{"content": []any{M{"type": "text", "text": "SECRET_SENTINEL_DO_NOT_RETAIN"}}}
			}
		}
		out.Encode(M{"jsonrpc": "2.0", "id": req["id"], "result": result})
	}
}
func testMCPOwner(t *testing.T) (*mcpOwner, *executor) {
	t.Helper()
	db := journal(t)
	e := &executor{db: db, ex: db}
	db.Exec("INSERT INTO commands(id,body) VALUES('command',?)", enc(M{"id": "command", "executionId": "execution", "sessionId": "session", "kind": "start"}))
	if err := e.launch("execution", "command", "session"); err != nil {
		t.Fatal(err)
	}
	id, err := identity(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err = e.ownGroup("execution", os.Getpid(), &live{owned: map[int]string{os.Getpid(): id}}); err != nil {
		t.Fatal(err)
	}
	o, err := newMCPOwner(db, "execution")
	if err != nil {
		t.Fatal(err)
	}
	db.Exec("UPDATE launches SET mcp_dir=? WHERE execution='execution'", o.dir)
	o.readyOnce.Do(func() { close(o.ready) })
	t.Cleanup(func() {
		o.close()
		if err := cleanupMCPGroups(db, "execution"); err != nil {
			t.Error(err)
		}
		removeMCPOwnerDir(o.dir)
	})
	return o, e
}
func fixtureSpec(tool string, args ...any) M {
	a := []any{"mcp-fixture", tool}
	a = append(a, args...)
	return M{"command": exe(), "args": a, "tools": []any{tool}}
}
func startTestGuardian(t *testing.T, o *mcpOwner, spec M) (*exec.Cmd, *os.File, io.ReadCloser) {
	t.Helper()
	lease, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	cmd := newCmd(exe(), t.TempDir())
	cmd.Env = mcpChildEnv(os.Getenv("PATH"))
	cmd.Args = append(cmd.Args, "mcp-guardian", o.socket, enc(spec))
	statusReader, statusWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { statusReader.Close() })
	if args := jArr(spec, "args"); len(args) > 1 && args[1] == "fd" {
		var control, receipt syscall.Stat_t
		syscall.Fstat(int(lease.Fd()), &control)
		syscall.Fstat(int(statusWriter.Fd()), &receipt)
		spec["args"] = []any{"mcp-fixture", "fd", strconv.FormatUint(control.Ino, 10) + "," + strconv.FormatUint(receipt.Ino, 10), args[2]}
		cmd.Args = []string{exe(), "mcp-guardian", o.socket, enc(spec)}
	}
	cmd.ExtraFiles = []*os.File{lease, statusWriter}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	statusWriter.Close()
	waited := make(chan struct{})
	go func() { cmd.Wait(); close(waited) }()
	t.Cleanup(func() {
		writer.Close()
		select {
		case <-waited:
		case <-time.After(3 * time.Second):
			cmd.Process.Kill()
			t.Error("guardian failed to terminate")
		}
	})
	return cmd, writer, out
}
func awaitFile(t *testing.T, path string) int {
	t.Helper()
	var lastErr error
	for i := 0; i < 500; i++ { // 10 s: -race guardian + fixture process starts under CI load
		b, err := os.ReadFile(path)
		if err == nil {
			s := strings.TrimSpace(string(b))
			pid, e := strconv.Atoi(s)
			if e == nil && pid > 0 {
				return pid
			}
			if e != nil {
				lastErr = e
			} else {
				lastErr = fmt.Errorf("invalid pid %q", s)
			}
		} else {
			lastErr = err
		}
		time.Sleep(20 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("fixture did not start: %v", lastErr)
	}
	t.Fatal("fixture did not start")
	return 0
}

// R3; risk: a foreign execution, reused incarnation or untrusted executable enrolls an unrelated process group.
func TestMCPRegistrationRefusesForeignAndStale(t *testing.T) {
	o, e := testMCPOwner(t)
	id, _ := identity(os.Getpid())
	if err := o.register(os.Getpid(), id); err == nil {
		t.Fatal("non-guardian caller accepted")
	}
	cmd := newCmd("/bin/sleep", t.TempDir())
	cmd.Args = append(cmd.Args, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	id, _ = identity(cmd.Process.Pid)
	if err := o.register(cmd.Process.Pid, id); err == nil {
		t.Fatal("foreign executable accepted")
	}
	if err := o.register(cmd.Process.Pid, "stale"); err == nil {
		t.Fatal("stale incarnation accepted")
	}
	c, err := net.Dial("unix", o.socket)
	if err != nil {
		t.Fatal(err)
	}
	json.NewEncoder(c).Encode(M{"pid": cmd.Process.Pid, "identity": id, "execution": "foreign"})
	var reply M
	json.NewDecoder(c).Decode(&reply)
	c.Close()
	if jBoolOr(false, reply, "registered") {
		t.Fatal("peer substituted a different PID", reply)
	}
	if err := cmd.Process.Signal(syscall.Signal(0)); err != nil {
		t.Fatal("unrelated process was signaled", err)
	}
	// Same executable but outside the execution's actual root still cannot register or start useful work.
	e.db.Exec("DELETE FROM owned WHERE execution=?", o.execution)
	if err := e.ownGroup(o.execution, cmd.Process.Pid, &live{owned: map[int]string{cmd.Process.Pid: id}}); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "foreign-start")
	_, lease, out := startTestGuardian(t, o, fixtureSpec("hold", marker))
	io.ReadAll(out)
	lease.Close()
	if exists(marker) || cmd.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("cross-execution guardian admitted or unrelated root changed")
	}
	o.close()
	if err := o.register(cmd.Process.Pid, id); err == nil {
		t.Fatal("terminal fence accepted registration")
	}
}

// R3; risk: missing acknowledgement starts useful work or a committed registration is lost after owner death.
func TestMCPRegistrationCrashBoundaries(t *testing.T) {
	o, _ := testMCPOwner(t)
	marker := filepath.Join(t.TempDir(), "started")
	// A fenced owner cannot acknowledge, so the actual server must not start.
	o.close()
	_, lease, out := startTestGuardian(t, o, fixtureSpec("hold", marker))
	io.ReadAll(out)
	lease.Close()
	if exists(marker) {
		t.Fatal("server started without ownership acknowledgement")
	}
}

// R1; risk: isError=false is mistaken for a working language server.
func TestMCPSemanticFailure(t *testing.T) {
	for _, text := range []string{"Error executing tool: gopls missing", `{"Function":["Other"]}`} {
		if serenaSymbolWitness(text) == nil {
			t.Fatal(text)
		}
	}
	text, err := mcpResultText(M{"isError": false, "content": []any{M{"type": "text", "text": "Error executing tool: missing gopls"}}})
	if err != nil || serenaSymbolWitness(text) == nil {
		t.Fatal("semantic error hidden by successful MCP response", err)
	}
	if err := serenaSymbolWitness(`{"Function":["CapabilityWitness"]}`); err != nil {
		t.Fatal(err)
	}
}

// R3; risk: queued unread requests hide downstream EOF while a tool response is hung.
func TestMCPQueuedInputClosure(t *testing.T) {
	o, _ := testMCPOwner(t)
	marker := filepath.Join(t.TempDir(), "server")
	input, writer, _ := os.Pipe()
	reader, output, _ := os.Pipe()
	defer reader.Close()
	done := make(chan error, 1)
	go func() {
		done <- mcpStdio(o.socket, fixtureSpec("hold", marker), input, output, t.TempDir(), "")
		output.Close()
	}()
	writer.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	awaitFile(t, marker)
	writer.Write([]byte(`{"id":2,"method":"tools/list"}` + "\n"))
	writer.Close()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("queued input hid owner closure")
	}
}

// R3; risk: committed ownership with a lost acknowledgement launches useful work or is retried blindly.
func TestMCPLostRegistrationAck(t *testing.T) {
	o, _ := testMCPOwner(t)
	socket := filepath.Join(o.dir, "lost.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	os.Chmod(socket, 0600)
	registered := make(chan error, 1)
	go func() {
		c, err := listener.AcceptUnix()
		if err != nil {
			registered <- err
			return
		}
		defer c.Close()
		var request M
		err = json.NewDecoder(c).Decode(&request)
		if err == nil {
			pid, _, e := mcpPeer(c)
			err = e
			if err == nil {
				err = o.register(pid, jStr(request, "identity"))
			}
		}
		registered <- err // close without acknowledgement after the real transaction
	}()
	marker := filepath.Join(t.TempDir(), "not-started")
	copyOwner := &mcpOwner{socket: socket}
	_, lease, out := startTestGuardian(t, copyOwner, fixtureSpec("hold", marker))
	io.ReadAll(out)
	lease.Close()
	if err := <-registered; err != nil {
		t.Fatal(err)
	}
	if exists(marker) {
		t.Fatal("server started after a lost acknowledgement")
	}
	if err := cleanupMCPGroups(o.db, o.execution); err != nil {
		t.Fatal(err)
	}
}

// R1/R3; risk: named profiles gain host commands/environment or unselected tools/defaults through a generic adapter.
func TestNamedMCPBindingsAndClientMappings(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "accounts.json")
	binding := M{"transport": "stdio", "command": exe(), "args": []any{"mcp-fixture", "navigate", "${executionRoot}"}}
	raw := M{"account": M{"mcpServers": M{"first": binding, "second": M{"transport": "stdio", "command": exe(), "args": []any{"mcp-fixture", "inspect"}}}}}
	if err := os.WriteFile(registry, []byte(enc(raw)), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_NATIVE_ACCOUNTS", registry)
	profile := M{"mcpServers": []any{M{"id": "first", "data": M{"tools": []any{"navigate"}}}, M{"id": "second", "data": M{"tools": []any{"inspect"}}}, M{"id": "design-rules", "data": M{"rule": "retained"}}}}
	for _, name := range []string{"codex", "claude", "antigravity"} {
		servers, err := profileServers(profile, t.TempDir(), name, "account")
		if err != nil || len(servers) != 3 {
			t.Fatal(name, servers, err)
		}
		if name != "antigravity" {
			cmd := exec.Command("not-invoked")
			harness{name: name}.WireMCP(cmd, profile, servers, nil)
			args := strings.Join(cmd.Args, " ")
			if !strings.Contains(args, "navigate") || !strings.Contains(args, "inspect") || !strings.Contains(args, "design-rules") {
				t.Fatal(name, args)
			}
		}
		none, err := profileServers(M{}, t.TempDir(), name, "account")
		if err != nil || len(none) != 0 {
			t.Fatal("unselected MCP appeared", none, err)
		}
	}
	if _, err := namedMCPBinding(registry, "foreign", "first", t.TempDir()); err == nil {
		t.Fatal("foreign account binding accepted")
	}
	binding["transport"] = "https"
	os.WriteFile(registry, []byte(enc(raw)), 0600)
	if _, err := namedMCPBinding(registry, "account", "first", t.TempDir()); err == nil {
		t.Fatal("unsupported transport declared applied")
	}
}

// R1/R3; risk: listing a tool or isError=false hides missing startup capability, logs leak, or a symlink leaves project scope.
func TestMCPFilterMissingToolAndProjectEscape(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(t.TempDir(), "foreign")
	os.WriteFile(target, []byte("private"), 0600)
	if err := os.Symlink(target, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	request := M{"method": "tools/call", "params": M{"name": "read", "arguments": M{"relative_path": "escape"}}}
	if err := mcpAllowed(request, root, []any{"read"}, "project-relative"); err == nil {
		t.Fatal("symlink escaped")
	}
	var output, upstream strings.Builder
	replies := strings.NewReader(`{"jsonrpc":"2.0","method":"notifications/message","params":{"data":"secret-sentinel"}}` + "\n" + `{"id":1,"result":{"tools":[]}}`)
	if err := filterMCP(strings.NewReader(`{"id":1,"method":"tools/list"}`), &output, &upstream, replies, root, []any{"initial_instructions"}, ""); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "secret-sentinel") || !strings.Contains(output.String(), "initial_instructions") || !strings.Contains(output.String(), "error") {
		t.Fatal(output.String())
	}
}

// R5; risk: the instruction receipt hashes the raw profile while the native request loads appended skills.
func TestMCPInstructionReceiptUsesAppliedSkills(t *testing.T) {
	profile := M{"id": "p", "model": "m", "effort": "low", "instructions": "base", "mcpServers": []any{}}
	skills := []any{M{"id": "startup", "text": "loaded manual instruction"}}
	receipt, _, err := profilePrepare(profile, "account", skills)
	if err != nil {
		t.Fatal(err)
	}
	applied := M{"instructions": skillText("base", skills)}
	if jStr(receipt, "instructionsSha256") != sha([]byte(jStr(applied, "instructions"))) || jStr(receipt, "applied", "instructions") != jStr(applied, "instructions") {
		t.Fatal(receipt)
	}
	if len(jArr(receipt, "resolvedSkills")) != 1 || jStr(receipt, "requested", "instructions") != "base" {
		t.Fatal(receipt)
	}
}

// R5; risk: the preparation receipt resolves one binding while launch rereads a changed registry and uses another.
func TestMCPPreparedBindingSnapshot(t *testing.T) {
	root := t.TempDir()
	registry := filepath.Join(root, "registry.json")
	binding := M{"transport": "stdio", "command": exe(), "args": []any{"old", "${executionRoot}"}}
	data := M{"account": M{"mcpServers": M{"selected": binding}}}
	os.WriteFile(registry, []byte(enc(data)), 0600)
	t.Setenv("FLEET_NATIVE_ACCOUNTS", registry)
	profile := M{"id": "p", "model": "m", "effort": "low", "instructions": "x", "mcpServers": []any{M{"id": "selected", "data": M{"tools": []any{"read"}}}}}
	receipt, selected, err := profilePrepare(profile, "account", nil)
	if err != nil {
		t.Fatal(err)
	}
	binding["args"] = []any{"changed", "${executionRoot}"}
	os.WriteFile(registry, []byte(enc(data)), 0600)
	applied := mcpAtRoot(selected, root)
	if enc(jAt(applied, "selected", "args")) != enc([]any{"old", root}) {
		t.Fatal("launch chose a different binding", applied)
	}
	if jStr(receipt, "capabilityReadiness", "selected", "bindingSha256") != profileHash(jAt(selected, "selected")) {
		t.Fatal("wrong binding receipt", receipt)
	}
	fresh, err := profileServers(profile, root, "codex", "account")
	if err != nil || jArr(fresh, "selected", "args")[0] != "changed" {
		t.Fatal("future launch did not see new config", fresh, err)
	}
}

// R1; risk: native clients are told resources/prompts/list-changed work although the selected bridge serves only tools.
func TestMCPAdvertisesOnlySupportedToolProtocol(t *testing.T) {
	var output, upstream strings.Builder
	request := strings.NewReader(`{"id":1,"method":"initialize"}`)
	reply := strings.NewReader(`{"id":1,"result":{"protocolVersion":"2024-11-05","serverInfo":{"name":"fixture","version":"1"},"capabilities":{"resources":{},"prompts":{},"tools":{"listChanged":true}}}}`)
	if err := filterMCP(request, &output, &upstream, reply, t.TempDir(), []any{"read"}, ""); err != nil {
		t.Fatal(err)
	}
	var actual M
	json.Unmarshal([]byte(output.String()), &actual)
	if enc(jAt(actual, "result", "capabilities")) != `{"tools":{}}` || jStr(actual, "result", "serverInfo", "name") != "fixture" {
		t.Fatal(actual)
	}
}

// R3; risk: an in-group orphan holds stdout after server exit while an open native input leaves a response blocked.
func TestMCPServerExitWithInheritedStdoutInterruptsResponse(t *testing.T) {
	o, _ := testMCPOwner(t)
	marker := filepath.Join(t.TempDir(), "orphan")
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	done := make(chan error, 1)
	go func() {
		done <- mcpStdio(o.socket, fixtureSpec("orphan-stdout", marker), input, output, t.TempDir(), "")
	}()
	writer.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	pid := awaitFile(t, marker)
	// Deliberately keep downstream input open: server status, not client EOF, must interrupt the blocked read.
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatal("server exit reported success", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("inherited stdout hid server exit")
	}
	if err := cleanupMCPGroups(o.db, o.execution); err != nil {
		t.Fatal(err)
	}
	table, err := processes()
	if err != nil || running(table, pid) {
		t.Fatal("stdout orphan survived", pid, err)
	}
}

// R3; risk: an unread native output pipe hides server exit while input stays open.
func TestMCPServerExitInterruptsBlockedOutput(t *testing.T) {
	o, _ := testMCPOwner(t)
	marker := filepath.Join(t.TempDir(), "server-exited")
	input, writer, _ := os.Pipe()
	defer writer.Close()
	reader, output, _ := os.Pipe()
	defer reader.Close()
	defer output.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- mcpStdio(o.socket, fixtureSpec("large-response", marker), input, output, t.TempDir(), "")
	}()
	writer.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	pid := awaitFile(t, marker)
	// One byte proves the filter reached its downstream write; leave the rest of the large response unread.
	started := make(chan error, 1)
	go func() { var b [1]byte; _, err := reader.Read(b[:]); started <- err }()
	select {
	case err := <-started:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second): // 2 MiB JSON decode+encode in filterMCP under -race load
		t.Fatal("response write did not start")
	}
	os.WriteFile(marker+"-exit", []byte("exit"), 0600)
	// Keep input open: only the private exit signal can unblock the pending write.
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "unknown") {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unread output hid server exit")
	}
	if err := cleanupMCPGroups(o.db, o.execution); err != nil {
		t.Fatal(err)
	}
	table, err := processes()
	if err != nil || running(table, pid) {
		t.Fatal("server survived", pid, err)
	}
}

// R3/R5; risk: an early guardian rejection is lost, or missing/untrusted status is mistaken for a server exit.
func TestMCPGuardianEarlyDiagnostic(t *testing.T) {
	o, _ := testMCPOwner(t)
	input, writer, _ := os.Pipe()
	defer writer.Close()
	reader, output, _ := os.Pipe()
	defer reader.Close()
	defer output.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() {
		done <- mcpStdio(o.socket, M{"command": "/absent-fleet-fixture-server", "args": []any{}, "tools": []any{"read"}}, input, output, t.TempDir(), "")
	}()
	writer.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	select {
	case err := <-done:
		var life *mcpLifecycleError
		if !errors.As(err, &life) || jStr(life.status, "stage") != "binding" || jStr(life.status, "registration") != "acknowledged" || jStr(life.status, "outcome") != "unknown" || has(life.status, "exitCode") {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("early guardian error lost")
	}
	if err := cleanupMCPGroups(o.db, o.execution); err != nil {
		t.Fatal(err)
	}
}

// R3/R5; risk: ownership refusal becomes an opaque handshake failure or falsely establishes exit.
func TestMCPGuardianRetainsOwnershipRefusal(t *testing.T) {
	o, e := testMCPOwner(t)
	if _, err := e.db.Exec("UPDATE launches SET mcp_fenced=1 WHERE execution=?", o.execution); err != nil {
		t.Fatal(err)
	}
	input, writer, _ := os.Pipe()
	defer input.Close()
	defer writer.Close()
	done := make(chan error, 1)
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	go func() { done <- mcpStdio(o.socket, fixtureSpec("read"), input, output, t.TempDir(), "") }()
	writer.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	select {
	case err := <-done:
		var life *mcpLifecycleError
		if !errors.As(err, &life) || jStr(life.status, "refusalReason") != "MCP execution launch is not open" || jStr(life.status, "registration") != "refused" || jStr(life.status, "outcome") != "unknown" {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("ownership refusal diagnostic lost")
	}
}

// R3; risk: a concurrent journal writer invalidates the ownership read snapshot before registration.
func TestMCPGuardianRegistrationSerializesWriter(t *testing.T) {
	o, _ := testMCPOwner(t)
	var seq int
	var name, path string
	if err := o.db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(0)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	original := identity
	defer func() { identity = original }()
	var once sync.Once
	identity = func(pid int) (string, error) {
		once.Do(func() {
			_, err := other.Exec("INSERT INTO sessions(id,native) VALUES('concurrent-writer','fixture')")
			var busy *sqlite.Error
			if !errors.As(err, &busy) || busy.Code() != 5 {
				t.Errorf("registration did not reserve writer: %v", err)
			}
		})
		return original(pid)
	}
	input, writer, _ := os.Pipe()
	defer input.Close()
	defer writer.Close()
	reader, output, _ := os.Pipe()
	defer reader.Close()
	defer output.Close()
	done := make(chan error, 1)
	go func() { done <- mcpStdio(o.socket, fixtureSpec("read"), input, output, t.TempDir(), "") }()
	writer.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	reader.SetReadDeadline(time.Now().Add(3 * time.Second))
	var reply M
	if err := json.NewDecoder(reader).Decode(&reply); err != nil {
		t.Fatal(err)
	}
	if jAt(reply, "result") == nil {
		t.Fatal(reply)
	}
	writer.Close()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("registered server did not finish")
	}
	if _, err := other.Exec("INSERT INTO sessions(id,native) VALUES('after-registration','fixture')"); err != nil {
		t.Fatal(err)
	}
}

// R5; risk: a database refusal loses its actionable code or retains arbitrary error text.
func TestMCPGuardianRetainsSQLiteRefusalCode(t *testing.T) {
	o, _ := testMCPOwner(t)
	if _, err := o.db.Exec(`CREATE TRIGGER refuse_registration BEFORE INSERT ON mcp_groups BEGIN SELECT RAISE(ABORT,'SECRET_SENTINEL_DO_NOT_RETAIN'); END`); err != nil {
		t.Fatal(err)
	}
	input, writer, _ := os.Pipe()
	defer input.Close()
	defer writer.Close()
	output, err := os.CreateTemp(t.TempDir(), "output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	done := make(chan error, 1)
	go func() { done <- mcpStdio(o.socket, fixtureSpec("read"), input, output, t.TempDir(), "") }()
	writer.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	select {
	case err := <-done:
		var life *mcpLifecycleError
		if !errors.As(err, &life) || jStr(life.status, "errorKind") != "sqlite" || jAt(life.status, "errorCode") != float64(1811) || jStr(life.status, "outcome") != "unknown" || strings.Contains(err.Error(), "SECRET_SENTINEL_DO_NOT_RETAIN") {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SQLite refusal diagnostic lost")
	}
}

// R5; risk: opaque payload/status values leak into retained diagnostics or a diagnostic write masks the primary failure.
func TestMCPDiagnosticProjectionAndRetentionFailure(t *testing.T) {
	sentinel := "SECRET_SENTINEL_DO_NOT_RETAIN"
	raw := M{"stage": sentinel, "registration": sentinel, "outcome": "exited", "exitCode": float64(9999), "signal": float64(-2), "guardianPid": float64(1.5), "guardianIdentity": sentinel, "executionId": sentinel, "refusalReason": sentinel, "errorKind": sentinel, "errorCode": sentinel, "stderr": sentinel, "params": M{"token": sentinel}}
	safe := safeMCPStatus(raw)
	if got := safeMCPStatus(M{"registration": "refused", "refusalReason": "MCP guardian executable differs"}); jStr(got, "refusalReason") != "MCP guardian executable differs" || jStr(got, "outcome") != "unknown" {
		t.Fatal(got)
	}
	if strings.Contains(enc(safe), sentinel) || jStr(safe, "outcome") != "unknown" || has(safe, "exitCode") || has(safe, "signal") || has(safe, "guardianPid") {
		t.Fatal(safe)
	}
	if jStr(readMCPStatus(strings.NewReader(`{"outcome":"exited","exitCode":0`)), "outcome") != "unknown" {
		t.Fatal("truncated status inferred exit")
	}
	if jStr(safeMCPStatus(nil), "outcome") != "unknown" {
		t.Fatal("missing status inferred exit")
	}
	output := filepath.Join(t.TempDir(), "blocked")
	os.WriteFile(output, []byte("file"), 0600)
	t.Setenv("FLEET_OUTPUT_DIR", output)
	err := qualifyMCP(nil)
	if err == nil || !strings.Contains(err.Error(), "requires profile") || !strings.Contains(err.Error(), "diagnostic retention failed") {
		t.Fatal(err)
	}
	t.Setenv("FLEET_OUTPUT_DIR", t.TempDir())
	err = qualifyMCP([]string{sentinel})
	if err == nil {
		t.Fatal("invalid invocation accepted")
	}
	receipt, e := os.ReadFile(filepath.Join(os.Getenv("FLEET_OUTPUT_DIR"), "capability-receipt.json"))
	if e != nil || strings.Contains(string(receipt), sentinel) {
		t.Fatal(e, string(receipt))
	}
	var actual M
	json.Unmarshal(receipt, &actual)
	if jStr(actual, "diagnostic", "phase") != "arguments" {
		t.Fatal(string(receipt))
	}
}
