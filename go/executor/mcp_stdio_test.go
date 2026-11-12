package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Test children run the real guardian/stdio entrypoints, not a mocked ownership protocol.
func TestMain(m *testing.M) {

	if mcpDependencyInvocation() {
		main()
		os.Exit(0)
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "mcp", "mcp-guardian", "qualify-mcp":
			main()
			os.Exit(0)
		case "mcp-fixture":
			mcpFixture()
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
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
		os.WriteFile(os.Args[3], []byte(enc(M{"home": h, "mode": int(st.Mode().Perm()), "ambientConfig": exists(filepath.Join(h, ".serena", "serena_config.yml")), "serenaHomeEnv": os.Getenv("SERENA_HOME") != ""})), 0600)
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
		os.WriteFile(os.Args[3], []byte(enc(M{"home": os.Getenv("HOME")})), 0600)
		fmt.Fprintln(os.Stdout, "gopls-fixture v1")
		return
	}
	if tool == "dependency" {
		payload := M{"pid": os.Getpid(), "argv": os.Args[4:], "home": os.Getenv("HOME")}
		os.WriteFile(os.Args[3], []byte(enc(payload)), 0600)
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
		for i := 0; i < 150; i++ {
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
		os.WriteFile(os.Args[3], []byte(enc(M{"accepted": err == nil, "answer": answer})), 0600)
		return
	}

	if tool == "large-response" {
		var req M
		json.NewDecoder(os.Stdin).Decode(&req)
		json.NewEncoder(os.Stdout).Encode(M{"id": req["id"], "result": M{"text": strings.Repeat("x", 2*1024*1024)}})
		os.WriteFile(os.Args[3], []byte(strconv.Itoa(os.Getpid())), 0600)
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
		os.WriteFile(os.Args[3], []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
		return
	}
	if tool == "hold" {
		os.WriteFile(os.Args[3], []byte(strconv.Itoa(os.Getpid())), 0600)
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
		os.WriteFile(os.Args[4], []byte(strconv.Itoa(os.Getpid())), 0600)
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
	for i := 0; i < 100; i++ {
		if b, err := os.ReadFile(path); err == nil {
			pid, e := strconv.Atoi(string(b))
			if e != nil {
				t.Fatal(e)
			}
			return pid
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("fixture did not start")
	return 0
}

// R1/R3/R5; risk: generic wiring confuses servers, drops schemas/manuals or forwards an unselected tool.
func TestSharedMCPSelectsTwoServers(t *testing.T) {
	o, _ := testMCPOwner(t)
	closers := []func(){}
	results := []chan error{}
	for _, tool := range []string{"navigate", "inspect"} {
		input, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		reader, output, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			err := mcpStdio(o.socket, fixtureSpec(tool), input, output, t.TempDir(), "")
			output.Close()
			done <- err
		}()
		encoder, decoder := json.NewEncoder(writer), json.NewDecoder(reader)
		for _, req := range []M{{"id": 1, "method": "initialize"}, {"id": 2, "method": "tools/list"}, {"id": 3, "method": "tools/call", "params": M{"name": tool, "arguments": M{}}}, {"id": 4, "method": "tools/call", "params": M{"name": "forbidden", "arguments": M{}}}} {
			encoder.Encode(req)
			var reply M
			if err := decoder.Decode(&reply); err != nil {
				t.Fatal(err)
			}
			switch jStr(req, "method") {
			case "tools/list":
				if len(jArr(reply, "result", "tools")) != 1 || jStr(jArr(reply, "result", "tools")[0], "name") != tool {
					t.Fatal(reply)
				}
			}
			if req["id"] == 3 && !strings.Contains(enc(reply), tool+"-observed") {
				t.Fatal(reply)
			}
			if req["id"] == 4 && !has(reply, "error") {
				t.Fatal("unselected tool forwarded", reply)
			}
		}
		closers = append(closers, func() { writer.Close(); reader.Close() })
		results = append(results, done)
	}
	var groups int
	o.db.QueryRow("SELECT count(*) FROM mcp_groups WHERE execution=?", o.execution).Scan(&groups)
	if groups != 2 {
		t.Fatal("two server instances not owned together", groups)
	}
	for i, close := range closers {
		close()
		if err := <-results[i]; err != nil {
			t.Fatal(err)
		}
	}
}

// R3; risk: server exit reparents its LSP, and cleanup falsely declares completion.
func TestMCPLeaderExitAndControlCLOEXEC(t *testing.T) {
	o, _ := testMCPOwner(t)
	marker := filepath.Join(t.TempDir(), "child")
	_, lease, out := startTestGuardian(t, o, fixtureSpec("orphan", marker))
	pid := awaitFile(t, marker)
	if _, err := io.ReadAll(out); err != nil {
		t.Fatal(err)
	}
	lease.Close()
	if err := cleanupMCPGroups(o.db, o.execution); err != nil {
		t.Fatal(err)
	}
	table, err := processes()
	if err != nil || running(table, pid) {
		t.Fatal("reparented LSP survived", pid, err)
	}
	marker = filepath.Join(t.TempDir(), "fd-checked")
	_, lease, out = startTestGuardian(t, o, fixtureSpec("fd", marker))
	awaitFile(t, marker)
	lease.Close()
	out.Close()
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

// R3; risk: sidecar SIGKILL leaves its separately contained server running because the lifetime writer leaked.
func TestMCPSidecarSIGKILL(t *testing.T) {
	o, _ := testMCPOwner(t)
	marker := filepath.Join(t.TempDir(), "server")
	cmd := exec.Command(exe(), "mcp", "stdio", o.socket, enc(fixtureSpec("hold", marker)), t.TempDir())
	cmd.Env = mcpChildEnv(os.Getenv("PATH"))
	stdin, _ := cmd.StdinPipe()
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	pid := awaitFile(t, marker)
	cmd.Process.Kill()
	cmd.Wait()
	if err := cleanupMCPGroups(o.db, o.execution); err != nil {
		t.Fatal(err)
	}
	table, err := processes()
	if err != nil || running(table, pid) {
		t.Fatal("server survived sidecar death", pid, err)
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

// R3; risk: a stale group record signals an unrelated current incarnation, or disappears despite uncertainty.
func TestMCPUnknownGroupRetainsFence(t *testing.T) {
	o, e := testMCPOwner(t)
	home, _ := os.MkdirTemp(o.dir, "mcp-home-")
	os.WriteFile(filepath.Join(home, "cache-proof"), []byte("retain"), 0600)
	cmd := newCmd("/bin/sleep", t.TempDir())
	cmd.Args = append(cmd.Args, "60")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	o.db.Exec("INSERT INTO mcp_groups(execution,pid,identity) VALUES(?,?,?)", o.execution, cmd.Process.Pid, "wrong-incarnation")
	if err := e.release(o.execution); err == nil {
		t.Fatal("unknown group was discarded")
	}
	if !exists(filepath.Join(home, "cache-proof")) {
		t.Fatal("unknown cleanup discarded MCP HOME")
	}
	var count int
	o.db.QueryRow("SELECT count(*) FROM mcp_groups WHERE execution=?", o.execution).Scan(&count)
	if count != 1 || cmd.Process.Signal(syscall.Signal(0)) != nil {
		t.Fatal("unrelated group altered or receipt erased")
	}
	o.db.Exec("DELETE FROM mcp_groups WHERE execution=? AND pid=?", o.execution, cmd.Process.Pid) // fixture-only cleanup after the negative assertion
}

// R3; risk: executor restart forgets separately contained, acknowledged MCP work and permits a duplicate launch.
func TestMCPExecutorRestartReconcilesOriginalGroup(t *testing.T) {
	o, e := testMCPOwner(t)
	marker := filepath.Join(t.TempDir(), "server")
	cmd := newCmd(exe(), t.TempDir())
	cmd.Env = mcpChildEnv(os.Getenv("PATH"))
	cmd.Args = append(cmd.Args, "mcp", "stdio", o.socket, enc(fixtureSpec("hold", marker)), t.TempDir())
	stdin, _ := cmd.StdinPipe()
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan struct{})
	go func() { cmd.Wait(); close(waited) }()
	defer func() { cmd.Process.Kill(); <-waited }()
	pid := awaitFile(t, marker)
	// Replace only this test execution's root ownership with its actual launch, never the test process.
	rootID, err := identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	e.db.Exec("DELETE FROM owned WHERE execution=?", o.execution)
	if err = e.ownGroup(o.execution, cmd.Process.Pid, &live{owned: map[int]string{cmd.Process.Pid: rootID}}); err != nil {
		t.Fatal(err)
	}
	o.close() // the old local endpoint is gone; recovery uses only durable journal identity
	recovered := &executor{db: e.db, ex: e.db, root: t.TempDir(), host: "fake"}
	if err := recovered.reconcileProcesses(); err != nil {
		t.Fatal(err)
	}
	table, err := processes()
	if err != nil || running(table, pid) {
		t.Fatal("registered MCP work survived recovery", pid, err)
	}
	var launches int
	e.db.QueryRow("SELECT count(*) FROM launches WHERE execution=?", o.execution).Scan(&launches)
	if launches != 0 {
		t.Fatal("resolved launch retained", launches)
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

// R3; risk: a selected executable is shadowed by PATH or a renamed symlink target is not what the receipt identifies.
func TestMCPExactDependencyAliases(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "renamed-server")
	os.WriteFile(selected, []byte("#!/bin/sh\necho selected\n"), 0700)
	wrong := filepath.Join(t.TempDir(), "gopls")
	os.WriteFile(wrong, []byte("#!/bin/sh\necho wrong\n"), 0700)
	t.Setenv("PATH", filepath.Dir(wrong))
	path, err := mcpPrivatePath(M{"gopls": selected}, root, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", path)
	t.Cleanup(func() { os.Chmod(strings.Split(path, string(os.PathListSeparator))[0], 0700) })
	got, err := exec.Command("gopls", "version").Output()
	if err != nil || strings.TrimSpace(string(got)) != "selected" {
		t.Fatal(string(got), err)
	}
	os.Chmod(selected, 0770)
	if _, err := trustedMCPExecutable(selected); err == nil {
		t.Fatal("writable executable accepted")
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
	applied := withSkills(profile, skills)
	if jStr(receipt, "instructionsSha256") != sha([]byte(jStr(applied, "instructions"))) || jStr(receipt, "applied", "instructions") != jStr(applied, "instructions") {
		t.Fatal(receipt)
	}
	if len(jArr(receipt, "resolvedSkills")) != 1 || jStr(receipt, "requested", "instructions") != "base" {
		t.Fatal(receipt)
	}
}

// R3; risk: SolidLSP setsid escapes the MCP group; version probes with empty stdin must still work and argv cannot be evaluated.
func TestMCPOwnedDependencyNewSession(t *testing.T) {
	for _, killSidecar := range []bool{false, true} {
		t.Run(fmt.Sprint(killSidecar), func(t *testing.T) {
			o, _ := testMCPOwner(t)
			root := t.TempDir()
			marker, version := filepath.Join(root, "dependency"), filepath.Join(root, "version")
			mode := "escape"
			if killSidecar {
				mode = "escape-hold"
			}
			spec := fixtureSpec(mode, marker, version)
			spec["dependencies"] = M{"gopls": exe()}
			spec["ownedDependencies"] = []any{"gopls"}
			cmd := exec.Command(exe(), "mcp", "stdio", o.socket, enc(spec), root)
			cmd.Env = mcpChildEnv(os.Getenv("PATH"))
			stdin, _ := cmd.StdinPipe()
			defer stdin.Close()
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			stdin.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
			defer func() { cmd.Process.Kill(); cmd.Wait() }()
			var receipt M
			for i := 0; i < 200; i++ {
				if raw, err := os.ReadFile(marker); err == nil {
					json.Unmarshal(raw, &receipt)
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if receipt == nil {
				t.Fatal("owned dependency did not start after successful version probe")
			}
			versionRaw, _ := os.ReadFile(version)
			if jStr(decM(string(versionRaw)), "home") != jStr(receipt, "home") || filepath.Dir(jStr(receipt, "home")) != o.dir {
				t.Fatal("owned dependency HOME differs", receipt)
			}
			if !exists(version) || enc(jAt(receipt, "argv")) != `["literal spaces","$(must-not-execute)"]` {
				t.Fatal(receipt)
			}
			number, ok := num(jAt(receipt, "pid"))
			if !ok {
				t.Fatal(receipt)
			}
			pid := int(number)
			if killSidecar {
				cmd.Process.Kill()
			} else {
				stdin.Close()
			}
			cmd.Wait()
			if err := cleanupMCPGroups(o.db, o.execution); err != nil {
				t.Fatal(err)
			}
			table, err := processes()
			if err != nil || running(table, pid) {
				t.Fatal("setsid dependency survived", pid, err)
			}
		})
	}
}

// R3; risk: a live alias generation silently switches to an updated/replaced dependency target.
func TestMCPOwnedDependencyRejectsStaleGenerationAndTarget(t *testing.T) {
	o, _ := testMCPOwner(t)
	root := t.TempDir()
	target := filepath.Join(root, "target")
	os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0700)
	answer, c, err := mcpOwnerCall(o.socket, M{"action": "prepare", "dependencies": M{"gopls": target}, "ownedDependencies": []any{"gopls"}})
	if err != nil {
		t.Fatal(err)
	}
	c.Close()
	generation := jStr(answer, "generation")
	marker, trigger := filepath.Join(root, "answer"), filepath.Join(root, "lookup")
	spec := fixtureSpec("lookup", marker, trigger)
	spec["generation"] = generation
	spec["ownerSocket"] = o.socket
	_, lease, out := startTestGuardian(t, o, spec)
	os.WriteFile(target, []byte("#!/bin/sh\necho changed\n"), 0700)
	os.WriteFile(trigger, []byte("now"), 0600)
	io.ReadAll(out)
	lease.Close()
	raw, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	var observation M
	json.Unmarshal(raw, &observation)
	if jBoolOr(true, observation, "accepted") {
		t.Fatal("changed target accepted", observation)
	}
	if err := cleanupMCPGroups(o.db, o.execution); err != nil {
		t.Fatal(err)
	}
	_, c, err = mcpOwnerCall(o.socket, M{"action": "lookup", "generation": generation, "dependency": "gopls"})
	if c != nil {
		c.Close()
	}
	if err == nil {
		t.Fatal("stale generation accepted")
	}
}

// R3; risk: a committed dependency registration with a lost ack starts its fixed executable anyway.
func TestMCPOwnedDependencyLostAckDoesNotStart(t *testing.T) {
	original, _ := testMCPOwner(t)
	socket := filepath.Join(original.dir, "dependency-lost.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	os.Chmod(socket, 0600)
	fault := &mcpOwner{db: original.db, execution: original.execution, dir: original.dir, socket: socket, listener: listener, ready: make(chan struct{}), connections: map[*net.UnixConn]bool{}, generations: map[string]*mcpGeneration{}}
	fault.readyOnce.Do(func() { close(fault.ready) })
	var lost bool
	go func() {
		for {
			c, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			fault.mu.Lock()
			if fault.closed {
				fault.mu.Unlock()
				c.Close()
				return
			}
			fault.connections[c] = true
			fault.pending.Add(1)
			fault.mu.Unlock()
			go func() {
				defer fault.pending.Done()
				defer c.Close()
				defer func() { fault.mu.Lock(); delete(fault.connections, c); fault.mu.Unlock() }()
				var request M
				if json.NewDecoder(c).Decode(&request) != nil {
					return
				}
				pid, uid, err := mcpPeer(c)
				if err != nil || uid != os.Geteuid() {
					return
				}
				fault.mu.Lock()
				answer, keep, end, err := fault.generationRequest(c, pid, request)
				// The actual generation/transaction code ran; fault only the acknowledgement boundary.
				if err == nil && jStr(request, "dependency") != "" && jStr(request, "action") == "" {
					lost = true
					fault.mu.Unlock()
					return
				}
				if answer == nil {
					answer = M{}
				}
				answer["registered"] = err == nil
				json.NewEncoder(c).Encode(answer)
				fault.mu.Unlock()
				if keep && err == nil {
					io.Copy(io.Discard, c)
					fault.mu.Lock()
					end()
					fault.mu.Unlock()
				}
			}()
		}
	}()
	t.Cleanup(func() { fault.close() })
	root := t.TempDir()
	marker, version := filepath.Join(root, "dependency"), filepath.Join(root, "version")
	spec := fixtureSpec("escape", marker, version)
	spec["dependencies"] = M{"gopls": exe()}
	spec["ownedDependencies"] = []any{"gopls"}
	cmd := exec.Command(exe(), "mcp", "stdio", socket, enc(spec), root)
	cmd.Env = mcpChildEnv(os.Getenv("PATH"))
	stdin, _ := cmd.StdinPipe()
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stdin.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	done := make(chan struct{})
	var waitErr error
	go func() { waitErr = cmd.Wait(); close(done) }()
	defer func() { cmd.Process.Kill(); <-done }()
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		t.Fatal("faulted acknowledgement did not terminate wrapper")
	}
	if waitErr == nil {
		t.Fatal("lost acknowledgement reported native success")
	}
	fault.mu.Lock()
	sawLost := lost
	fault.mu.Unlock()
	if !sawLost || exists(version) || exists(marker) {
		t.Fatal("lost dependency ack started useful work", sawLost)
	}
	if err := cleanupMCPGroups(original.db, original.execution); err != nil {
		t.Fatal(err)
	}
}

// R3; risk: restart cannot reconcile a setsid dependency using only the original durable ownership records.
func TestMCPOwnedDependencyJournalRecovery(t *testing.T) {
	o, e := testMCPOwner(t)
	root := t.TempDir()
	marker, version := filepath.Join(root, "dependency"), filepath.Join(root, "version")
	spec := fixtureSpec("escape-hold", marker, version)
	spec["dependencies"] = M{"gopls": exe()}
	spec["ownedDependencies"] = []any{"gopls"}
	cmd := newCmd(exe(), root)
	cmd.Env = mcpChildEnv(os.Getenv("PATH"))
	cmd.Args = append(cmd.Args, "mcp", "stdio", o.socket, enc(spec), root)
	stdin, _ := cmd.StdinPipe()
	defer stdin.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	defer func() { cmd.Process.Kill(); <-done }()
	stdin.Write([]byte(`{"id":1,"method":"initialize"}` + "\n"))
	var receipt M
	for i := 0; i < 200; i++ {
		if raw, err := os.ReadFile(marker); err == nil {
			json.Unmarshal(raw, &receipt)
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if receipt == nil {
		t.Fatal("dependency not running")
	}
	value, _ := num(jAt(receipt, "pid"))
	pid := int(value)
	id, err := identity(cmd.Process.Pid)
	if err != nil {
		t.Fatal(err)
	}
	e.db.Exec("DELETE FROM owned WHERE execution=?", o.execution)
	if err = e.ownGroup(o.execution, cmd.Process.Pid, &live{owned: map[int]string{cmd.Process.Pid: id}}); err != nil {
		t.Fatal(err)
	}
	recovered := &executor{db: e.db, ex: e.db, root: t.TempDir(), host: "fake"}
	if err := recovered.reconcileProcesses(); err != nil {
		t.Fatal(err)
	}
	table, err := processes()
	if err != nil || running(table, pid) {
		t.Fatal("setsid target survived journal recovery", pid, err)
	}
	var count int
	e.db.QueryRow("SELECT count(*) FROM launches WHERE execution=?", o.execution).Scan(&count)
	if count != 0 {
		t.Fatal("resolved launch not released")
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
	case <-time.After(3 * time.Second):
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

// R3; risk: in-process checker tests miss the real script -> checker -> guardian boundary.
func TestScriptMCPQualification(t *testing.T) {
	e, root := scriptExecutor(t)
	fixture := filepath.Join(root, "fixture")
	os.MkdirAll(fixture, 0700)
	profile := M{"id": "program-fixture", "model": "not-invoked", "effort": "low", "instructions": "Program-only fixture", "mcpServers": []any{M{"id": "fixture", "data": M{"tools": []any{"witness"}}}}}
	registry := M{"fixture-account": M{"mcpServers": M{"fixture": M{"transport": "stdio", "command": exe(), "args": []any{"mcp-fixture", "witness"}}}}}
	checks := M{"sources": []any{"fixture.go"}, "servers": M{"fixture": []any{M{"tool": "witness", "arguments": M{}, "oracle": "text-contains", "expected": "witness-observed", "phase": "work"}}}}
	for name, v := range map[string]any{"profile.json": profile, "registry.json": registry, "checks.json": checks} {
		os.WriteFile(filepath.Join(fixture, name), []byte(enc(v)), 0600)
	}
	os.WriteFile(filepath.Join(fixture, "fixture.go"), []byte("package fixture\n"), 0600)
	command := fmt.Sprintf("%q qualify-mcp %q %q \"$FLEET_OUTPUT_DIR/receipt.json\" fixture-account %q %q", exe(), filepath.Join(fixture, "profile.json"), fixture, filepath.Join(fixture, "registry.json"), filepath.Join(fixture, "checks.json"))
	if err := e.run(M{"id": "program-command", "executionId": "program-execution", "sessionId": "program-session", "kind": "script", "payload": M{"command": command}}); err != nil {
		t.Fatal(err)
	}
	settle(t, e, "program-execution")
	if lastReceipt(e, "program-command") != "completed" {
		b, _ := os.ReadFile(filepath.Join(root, "sessions/program-session/output/script.stderr"))
		t.Fatalf("qualification failed: %s", b)
	}
	raw, err := os.ReadFile(filepath.Join(root, "sessions/program-session/output/receipt.json"))
	if err != nil {
		t.Fatal(err)
	}
	var receipt M
	if json.Unmarshal(raw, &receipt) != nil || jAt(receipt, "modelInvoked") != false || len(jArr(receipt, "calls")) != 1 {
		t.Fatal(string(raw))
	}
}

// R3/R5; risk: the native script loses the failure phase or retains credentials while an unknown call is replayed.
func TestScriptMCPQualificationRetainsSafeFailure(t *testing.T) {
	for _, tc := range []struct {
		tool, method             string
		ordinal, exit, completed int
	}{{"diagnostic-exit", "initialize", 1, 23, 0}, {"diagnostic-call-exit", "tools/call", 4, 24, 1}, {"diagnostic-signal", "initialize", 1, -1, 0}} {
		t.Run(tc.tool, func(t *testing.T) {
			e, root := scriptExecutor(t)
			fixture := filepath.Join(root, "fixture")
			os.MkdirAll(fixture, 0700)
			marker := filepath.Join(root, "starts")
			profile := M{"id": "program-fixture", "model": "not-invoked", "effort": "low", "instructions": "SECRET_SENTINEL_DO_NOT_RETAIN", "mcpServers": []any{M{"id": "fixture", "data": M{"tools": []any{tc.tool}}}}}
			registry := M{"fixture-account": M{"privateValue": "SECRET_SENTINEL_DO_NOT_RETAIN", "mcpServers": M{"fixture": M{"transport": "stdio", "command": exe(), "args": []any{"mcp-fixture", tc.tool, marker}}}}}
			call := M{"tool": tc.tool, "arguments": M{"token": "SECRET_SENTINEL_DO_NOT_RETAIN"}, "oracle": "text-contains", "expected": "SECRET_SENTINEL_DO_NOT_RETAIN", "phase": "work"}
			checks := M{"sources": []any{"fixture.go"}, "servers": M{"fixture": []any{call, call}}}
			for name, value := range map[string]any{"profile.json": profile, "registry.json": registry, "checks.json": checks} {
				os.WriteFile(filepath.Join(fixture, name), []byte(enc(value)), 0600)
			}
			os.WriteFile(filepath.Join(fixture, "fixture.go"), []byte("package fixture\n"), 0600)
			command := fmt.Sprintf("%q qualify-mcp %q %q \"$FLEET_OUTPUT_DIR/receipt.json\" fixture-account %q %q", exe(), filepath.Join(fixture, "profile.json"), fixture, filepath.Join(fixture, "registry.json"), filepath.Join(fixture, "checks.json"))
			execution := "a1b2c3d4-5678-4abc-8def-0123456789ab"
			if err := e.run(M{"id": "failure-command", "executionId": execution, "sessionId": "failure-session", "kind": "script", "payload": M{"command": command}}); err != nil {
				t.Fatal(err)
			}
			settle(t, e, execution)
			if lastReceipt(e, "failure-command") != "failed" {
				t.Fatal("failed qualification reported success")
			}
			output := filepath.Join(root, "sessions/failure-session/output")
			raw, err := os.ReadFile(filepath.Join(output, "capability-receipt.json"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "SECRET_SENTINEL_DO_NOT_RETAIN") {
				t.Fatal("secret retained", string(raw))
			}
			var receipt M
			if json.Unmarshal(raw, &receipt) != nil {
				t.Fatal(string(raw))
			}
			d := jObj(receipt, "diagnostic")
			life := jObj(d, "lifecycle")
			if jStr(receipt, "status") != "failed" || jStr(receipt, "executionId") != execution || jStr(d, "method") != tc.method || jAt(d, "requestOrdinal") != float64(tc.ordinal) || jStr(life, "stage") != "server-exit" || (tc.exit >= 0 && jAt(life, "exitCode") != float64(tc.exit) || tc.exit < 0 && (jAt(life, "signal") != float64(syscall.SIGTERM) || jStr(life, "outcome") != "signaled")) || jStr(life, "registration") != "acknowledged" || jStr(life, "guardianIdentity") == "" || jStr(life, "executionId") != execution || len(jArr(receipt, "calls")) != tc.completed {
				t.Fatal(string(raw))
			}
			starts, _ := os.ReadFile(marker)
			if len(strings.Fields(string(starts))) != 1 {
				t.Fatal("duplicate server start", string(starts))
			}
			var groups, launches int
			e.db.QueryRow("SELECT count(*) FROM mcp_groups WHERE execution=?", execution).Scan(&groups)
			e.db.QueryRow("SELECT count(*) FROM launches WHERE execution=?", execution).Scan(&launches)
			if groups != 0 || launches != 0 {
				t.Fatal("owned cleanup incomplete", groups, launches)
			}
			stderr, _ := os.ReadFile(filepath.Join(output, "script.stderr"))
			if !strings.Contains(string(stderr), "MCP response unavailable") || strings.Contains(string(stderr), "SECRET_SENTINEL_DO_NOT_RETAIN") {
				t.Fatal("primary error lost or stderr leaked", string(stderr))
			}
		})
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

// R5; risk: opaque payload/status values leak into retained diagnostics or a diagnostic write masks the primary failure.
func TestMCPDiagnosticProjectionAndRetentionFailure(t *testing.T) {
	sentinel := "SECRET_SENTINEL_DO_NOT_RETAIN"
	raw := M{"stage": sentinel, "registration": sentinel, "outcome": "exited", "exitCode": float64(9999), "signal": float64(-2), "guardianPid": float64(1.5), "guardianIdentity": sentinel, "executionId": sentinel, "stderr": sentinel, "params": M{"token": sentinel}}
	safe := safeMCPStatus(raw)
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

func mcpFixtureExchange(t *testing.T, o *mcpOwner, spec M, tool string) M {
	t.Helper()
	input, writer, _ := os.Pipe()
	reader, output, _ := os.Pipe()
	defer reader.Close()
	defer output.Close()
	defer writer.Close()
	done := make(chan error, 1)
	go func() { done <- mcpStdio(o.socket, spec, input, output, t.TempDir(), "") }()
	encoder, decoder := json.NewEncoder(writer), json.NewDecoder(reader)
	var answer M
	for i, request := range []M{{"method": "initialize"}, {"method": "tools/list"}, {"method": "tools/call", "params": M{"name": tool, "arguments": M{}}}} {
		request["id"] = i + 1
		if err := encoder.Encode(request); err != nil {
			t.Fatal(err)
		}
		if err := decoder.Decode(&answer); err != nil {
			t.Fatal(err)
		}
	}
	writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return answer
}

// R3; risk: selected MCPs read shared account guidance/cache or change the harness credential HOME.
func TestMCPFreshChildHomes(t *testing.T) {
	o, e := testMCPOwner(t)
	ambient := t.TempDir()
	os.MkdirAll(filepath.Join(ambient, ".serena"), 0700)
	config := filepath.Join(ambient, ".serena/serena_config.yml")
	os.WriteFile(config, []byte("SECRET_AMBIENT_GUIDANCE"), 0600)
	t.Setenv("HOME", ambient)
	t.Setenv("SERENA_HOME", ambient)
	homes := map[string]bool{}
	for i := 0; i < 2; i++ {
		marker := filepath.Join(t.TempDir(), "home.json")
		mcpFixtureExchange(t, o, fixtureSpec("home", marker), "home")
		raw, _ := os.ReadFile(marker)
		actual := decM(string(raw))
		h := jStr(actual, "home")
		if filepath.Dir(h) != o.dir || homes[h] || jAt(actual, "mode") != json.Number("448") || jAt(actual, "ambientConfig") != false || jAt(actual, "serenaHomeEnv") != false || !exists(filepath.Join(h, "cache-proof")) {
			t.Fatal(actual)
		}
		homes[h] = true
	}
	raw, _ := os.ReadFile(config)
	if os.Getenv("HOME") != ambient || string(raw) != "SECRET_AMBIENT_GUIDANCE" {
		t.Fatal("harness/shared home changed")
	}
	if err := e.release(o.execution); err != nil {
		t.Fatal(err)
	}
	for h := range homes {
		if exists(h) {
			t.Fatal("resolved cleanup retained child home", h)
		}
	}
}

// R3; risk: child HOME isolation breaks internal design-rules or gateway authority supplied explicitly in args/run-file.
func TestMCPFreshHomePreservesInternalAdapters(t *testing.T) {
	o, _ := testMCPOwner(t)
	rules := M{"heading": "explicit-rules"}
	result := mcpFixtureExchange(t, o, M{"command": exe(), "args": []any{"mcp", "design-rules", enc(rules)}, "tools": []any{"get_design_rules"}}, "get_design_rules")
	if !strings.Contains(enc(result), "explicit-rules") {
		t.Fatal(result)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-run-token" {
			w.WriteHeader(403)
			return
		}
		w.Write([]byte(`{"answer":"explicit-gateway"}`))
	}))
	defer gateway.Close()
	file := filepath.Join(t.TempDir(), "run.env")
	os.WriteFile(file, []byte("FLEET_RUN_TOKEN=fixture-run-token\nFLEET_GATEWAY_URL="+gateway.URL+"\n"), 0600)
	spec := M{"tools": []any{M{"name": "read", "inputSchema": M{"type": "object"}}}}
	result = mcpFixtureExchange(t, o, M{"command": exe(), "args": []any{"mcp", "connection", "web", enc(spec), file}, "tools": []any{"read"}}, "read")
	if jAt(result, "result", "isError") != false || !strings.Contains(enc(result), "explicit-gateway") {
		t.Fatal(result)
	}
}
