package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fleet/contract"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const testOwnerEmail = "owner@trial.test"

var (
	testPGOnce sync.Once
	testPGDir  string
	testPGBin  string
	testPGPort int
	testPGErr  error
	testDBSeq  atomic.Int64
)

func TestMain(m *testing.M) {
	code := m.Run()
	if testPGDir != "" && testPGBin != "" {
		cmd := osexec.Command(filepath.Join(testPGBin, "pg_ctl"), "-D", testPGDir, "-m", "immediate", "-w", "stop")
		_ = cmd.Run()
		_ = os.RemoveAll(testPGDir)
	}
	os.Exit(code)
}

func findPostgresBinDir() (string, error) {
	if p, err := osexec.LookPath("initdb"); err == nil {
		dir := filepath.Dir(p)
		if _, err := os.Stat(filepath.Join(dir, "pg_ctl")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "psql")); err == nil {
				return dir, nil
			}
		}
	}
	var dirs []string
	for _, pat := range []string{"/usr/lib/postgresql/*/bin", "/opt/homebrew/opt/postgresql*/bin"} {
		matches, _ := filepath.Glob(pat)
		dirs = append(dirs, matches...)
	}
	dirs = append(dirs, "/opt/homebrew/bin", "/usr/local/bin")
	for i := len(dirs) - 1; i >= 0; i-- {
		d := dirs[i]
		ok := true
		for _, bin := range []string{"initdb", "pg_ctl", "psql"} {
			if _, err := os.Stat(filepath.Join(d, bin)); err != nil {
				ok = false
				break
			}
		}
		if ok {
			return d, nil
		}
	}
	return "", errors.New("postgres binaries (initdb, pg_ctl, psql) not found")
}

func ensureTestPostgres(t *testing.T) (sockDir string, port int, binDir string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("postgres initdb cannot run as root")
	}
	testPGOnce.Do(func() {
		bin, err := findPostgresBinDir()
		if err != nil {
			testPGErr = err
			return
		}
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			testPGErr = err
			return
		}
		p := ln.Addr().(*net.TCPAddr).Port
		_ = ln.Close()
		dir, err := os.MkdirTemp("", "fleet-coord-pg-*")
		if err != nil {
			testPGErr = err
			return
		}
		initCmd := osexec.Command(filepath.Join(bin, "initdb"), "-D", dir, "-U", "postgres", "-A", "trust", "-E", "UTF8", "--no-instructions", "--no-sync")
		if out, err := initCmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(dir)
			testPGErr = fmt.Errorf("initdb: %v: %s", err, out)
			return
		}
		pgOpts := fmt.Sprintf("-k %s -p %d -c listen_addresses= -c fsync=off -c synchronous_commit=off -c full_page_writes=off -c max_connections=200", dir, p)
		startCmd := osexec.Command(filepath.Join(bin, "pg_ctl"), "-D", dir, "-l", filepath.Join(dir, "server.log"), "-w", "-o", pgOpts, "start")
		if out, err := startCmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(dir)
			testPGErr = fmt.Errorf("pg_ctl start: %v: %s", err, out)
			return
		}
		testPGDir = dir
		testPGBin = bin
		testPGPort = p
	})
	if testPGErr != nil {
		t.Skipf("postgres unavailable: %v", testPGErr)
	}
	return testPGDir, testPGPort, testPGBin
}

func runTestPSQL(t *testing.T, binDir, sockDir string, port int, db, user, sqlInput string) string {
	t.Helper()
	args := []string{"-h", sockDir, "-p", fmt.Sprintf("%d", port), "-U", user, "-d", db, "-v", "ON_ERROR_STOP=1", "-At"}
	cmd := osexec.Command(filepath.Join(binDir, "psql"), args...)
	cmd.Stdin = strings.NewReader(sqlInput)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("psql (%s): %v\n%s", db, err, out)
	}
	return string(out)
}

type testCoordEnv struct {
	app           *App
	ownerStore    *Store
	appStore      *Store
	server        *httptest.Server
	dbName        string
	sockDir       string
	pgPort        int
	binDir        string
	browserSecret string
	ctx           context.Context
	cancel        context.CancelFunc
}

func openTestCoordinator(t *testing.T, withDispatchLoop bool) *testCoordEnv {
	t.Helper()
	sockDir, pgPort, binDir := ensureTestPostgres(t)
	db := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	rolesRaw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "infrastructure", "fleet-postgres.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rolesSQL := strings.NewReplacer(
		"DATABASE fleet", "DATABASE "+db,
		"datname = 'fleet'", "datname = '"+db+"'",
		"\\connect fleet", "\\connect "+db,
	).Replace(string(rolesRaw))
	runTestPSQL(t, binDir, sockDir, pgPort, "postgres", "postgres", rolesSQL)

	root := filepath.Join("..", "..")
	t.Setenv("FLEET_PROFILES_DIRECTORY", filepath.Join(root, "config", "agents"))
	t.Setenv("FLEET_PROJECT_CONFIG_DIRECTORY", filepath.Join(root, "config"))
	t.Setenv("FLEET_LAB_DIRECTORY", filepath.Join(root, "config", "lab"))

	ctx, cancel := context.WithCancel(context.Background())
	ownerURL := fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", db, sockDir, pgPort)
	appURL := fmt.Sprintf("postgresql://fleet_app@/%s?host=%s&port=%d", db, sockDir, pgPort)

	ownerStore, err := openStore(ctx, ownerURL)
	if err != nil {
		cancel()
		t.Fatalf("open owner store: %v", err)
	}
	if err := migrate(ctx, ownerURL); err != nil {
		ownerStore.pool.Close()
		cancel()
		t.Fatalf("migrate: %v", err)
	}
	appStore, err := openStore(ctx, appURL)
	if err != nil {
		ownerStore.pool.Close()
		cancel()
		t.Fatalf("open app store: %v", err)
	}

	tmpDir := t.TempDir()
	peoplePath := filepath.Join(tmpDir, "people.json")
	peopleRaw, err := os.ReadFile(filepath.Join(root, "tests", "trial", "people.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(peoplePath, peopleRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	a := &App{store: appStore, browserSecret: "test-browser-secret", hostID: "test-coord"}
	a.ensureRuntime()
	if err := a.loadPeople(ctx, peoplePath); err != nil {
		appStore.pool.Close()
		ownerStore.pool.Close()
		cancel()
		t.Fatalf("loadPeople: %v", err)
	}
	files, broken := shippedDefinitions()
	if err := runTx(ctx, a, nil, true, GLOBAL, func(q *Request) error {
		return reconcileDefinitions(q, files, broken, "test")
	}); err != nil {
		t.Fatalf("reconcileDefinitions: %v", err)
	}

	if withDispatchLoop {
		startDispatchLoop(ctx, a)
	}
	srv := httptest.NewServer(a.routes())
	env := &testCoordEnv{
		app:           a,
		ownerStore:    ownerStore,
		appStore:      appStore,
		server:        srv,
		dbName:        db,
		sockDir:       sockDir,
		pgPort:        pgPort,
		binDir:        binDir,
		browserSecret: "test-browser-secret",
		ctx:           ctx,
		cancel:        cancel,
	}
	t.Cleanup(func() {
		a.closeLongPolls()
		cancel()
		srv.Close()
		appStore.pool.Close()
		ownerStore.pool.Close()
		_, _ = osexec.Command(filepath.Join(binDir, "psql"), "-h", sockDir, "-p", fmt.Sprintf("%d", pgPort), "-U", "postgres", "-d", "postgres", "-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", db)).CombinedOutput()
	})
	return env
}

func (e *testCoordEnv) callJSON(t *testing.T, method, path string, body any, headers map[string]string) (int, map[string]any) {
	t.Helper()
	var buf []byte
	if body != nil {
		var err error
		buf, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	req, err := http.NewRequestWithContext(e.ctx, method, e.server.URL+path, bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var parsed map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&parsed)
	return resp.StatusCode, parsed
}

func (e *testCoordEnv) ownerHeaders() map[string]string {
	return map[string]string{
		"Authorization":  "Bearer " + e.browserSecret,
		"X-Fleet-Person": testOwnerEmail,
	}
}

func (e *testCoordEnv) registerHost(t *testing.T, hostID string) string {
	t.Helper()
	code, body := e.callJSON(t, "POST", "/api/ops/hosts_register", doc{"id": hostID}, e.ownerHeaders())
	if code != 200 {
		t.Fatalf("hosts_register(%s) = %d %v", hostID, code, body)
	}
	sec, _ := body["secret"].(string)
	if sec == "" {
		t.Fatalf("hosts_register(%s) missing secret: %v", hostID, body)
	}
	return sec
}

// Requirement: PR5 canonical advisory-lock rank order, 64-bit hashtextextended locking, and out-of-order lock rejection.
// Risk: mixed lock ordering causes PostgreSQL 40P01 deadlocks or 32-bit hash collisions serialize unrelated scopes.
func TestLockOrderAnd64BitHashing(t *testing.T) {
	input := []string{
		"person-setting:a@b",
		"usageKey:u1",
		"host:h2",
		"host:h1",
		"execution:ex1",
		"experiment:e1",
		"session:s1",
		"task:t1",
		"project:p1",
		DISPATCH,
		GLOBAL,
		"session:s1",
	}
	sorted, err := sortLocks(input)
	if err != nil {
		t.Fatalf("sortLocks: %v", err)
	}
	want := []string{
		GLOBAL,
		DISPATCH,
		"project:p1",
		"task:t1",
		"session:s1",
		"experiment:e1",
		"execution:ex1",
		"host:h1",
		"host:h2",
		"usageKey:u1",
		"person-setting:a@b",
	}
	if !slices.Equal(sorted, want) {
		t.Fatalf("sortLocks = %v, want %v", sorted, want)
	}
	if got, err := sortLocks([]string{NOLOCK}); err != nil || len(got) != 0 {
		t.Fatalf("expected sortLocks(NOLOCK) = empty nil, got (%v, %v)", got, err)
	}
	if _, err := lockRank(NOLOCK); err == nil {
		t.Fatal("expected lockRank(NOLOCK) to error")
	}
	if _, err := sortLocks([]string{"unknown:1"}); err == nil {
		t.Fatal("expected sortLocks to reject unknown lock prefix")
	}

	env := openTestCoordinator(t, false)
	if err := env.appStore.TxLocks(env.ctx, nil, true, []string{"execution:ex-check", "session:sess-check"}, func(tx pgx.Tx) error {
		if !txHasLock(tx, "session:sess-check") || !txHasLock(tx, "execution:ex-check") {
			t.Fatal("expected txHasLock true for pre-acquired locks")
		}
		if canAcquireTxLock(tx, GLOBAL) || canAcquireTxLock(tx, "project:p1") {
			t.Fatal("expected lower-rank lock to be disallowed while holding higher-rank locks")
		}
		if !canAcquireTxLock(tx, "host:h-late") {
			t.Fatal("expected higher-rank lock to be allowed")
		}
		if err := acquireTxLocks(env.ctx, tx, "session:sess-check"); err != nil {
			t.Fatalf("re-acquiring held lock should be idempotent: %v", err)
		}
		if err := acquireTxLocks(env.ctx, tx, GLOBAL); err == nil {
			t.Fatal("expected out-of-order GLOBAL acquisition to fail")
		}
		if err := acquireTxLocks(env.ctx, tx, "host:h-late"); err != nil {
			t.Fatalf("higher-rank dynamic acquisition failed: %v", err)
		}
		var cnt64, cnt32 int
		if err := tx.QueryRow(env.ctx, "SELECT count(*) FILTER (WHERE objsubid=1), count(*) FILTER (WHERE objsubid=2) FROM pg_locks WHERE locktype='advisory' AND pid=pg_backend_pid()").Scan(&cnt64, &cnt32); err != nil {
			return err
		}
		if cnt64 != 3 || cnt32 != 0 {
			t.Fatalf("expected 3 64-bit advisory locks (objsubid=1) and 0 32-bit locks, got (%d, %d)", cnt64, cnt32)
		}
		return nil
	}); err != nil {
		t.Fatalf("TxLocks: %v", err)
	}
}

// Requirement: PR5 bug fix A2 — labRequester reads experiment then person/person_grant in FOR SHARE without GLOBAL lock inversion.
// Risk: experiment:<id> -> GLOBAL acquisition deadlocks (40P01) against setPeople (GLOBAL -> person -> experiment).
func TestLabRequesterNoGlobalDeadlock(t *testing.T) {
	env := openTestCoordinator(t, false)
	if _, err := env.ownerStore.pool.Exec(env.ctx, `
		INSERT INTO experiment(id, project_id, kind, pipeline_id, requested_by)
		VALUES ('exp-1', 'hello-world', 'single', 'default', $1)`, testOwnerEmail); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 64)
	deadline := time.Now().Add(400 * time.Millisecond)

	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				err := env.appStore.Tx(env.ctx, nil, true, GLOBAL, func(tx pgx.Tx) error {
					if _, err := tx.Exec(env.ctx, "UPDATE person SET name=name WHERE email=$1", testOwnerEmail); err != nil {
						return err
					}
					_, err := tx.Exec(env.ctx, "SELECT id FROM experiment WHERE id='exp-1' FOR SHARE")
					return err
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for time.Now().Before(deadline) {
				err := runTx(env.ctx, env.app, nil, true, "experiment:exp-1", func(q *Request) error {
					var id string
					if err := q.tx.QueryRow(q.ctx(), "SELECT id FROM experiment WHERE id='exp-1' FOR UPDATE").Scan(&id); err != nil {
						return err
					}
					p, err := labRequester(q, "exp-1")
					if err != nil {
						return err
					}
					if p == nil || p.Email != testOwnerEmail {
						return fmt.Errorf("unexpected requester: %+v", p)
					}
					return nil
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "40P01" {
			t.Fatalf("unexpected deadlock 40P01: %v", err)
		}
		t.Fatalf("unexpected concurrent error: %v", err)
	}
}

// Requirement: PR5 poll/receipt/session-message decoupling from GLOBAL under 50+ concurrent hosts and concurrent GLOBAL writer.
// Risk: holding GLOBAL inside any unrelated write blocks all host heartbeats, non-terminal receipts, or session message queues.
func TestConcurrentHostPollsReceiptsMessagesNoGlobalContention(t *testing.T) {
	env := openTestCoordinator(t, true)
	const numHosts = 55
	hostSecrets := make([]string, numHosts)
	for i := range numHosts {
		hostSecrets[i] = env.registerHost(t, fmt.Sprintf("h-conc-%02d", i))
	}

	// Prepare one online host with two dispatched commands so concurrent non-terminal receipts (accepted, started) do not contend on one command.
	cmdHostID := "h-rcpt-00"
	cmdHostSecret := env.registerHost(t, cmdHostID)
	c, poll0 := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{HostId: cmdHostID, Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}}, map[string]string{"Authorization": "Bearer " + cmdHostSecret})
	if c != 200 {
		t.Fatalf("initial cmd host poll = %d %v", c, poll0)
	}
	type cmdTarget struct {
		hostID string
		secret string
		execID string
		cmdID  string
	}
	var targets [2]cmdTarget
	for idx := range targets {
		targets[idx].hostID = cmdHostID
		targets[idx].secret = cmdHostSecret
		c, sessBody := eCreateSession(t, env)
		if c != 200 {
			t.Fatalf("create session %d = %d %v", idx, c, sessBody)
		}
		sid := sessBody["sessionId"].(string)
		c, msgBody := env.callJSON(t, "POST", "/api/sessions/"+sid+"/messages", doc{"requestId": fmt.Sprintf("req-rcpt-%d", idx), "text": "go"}, env.ownerHeaders())
		if c != 200 {
			t.Fatalf("first message %d = %d %v", idx, c, msgBody)
		}
		targets[idx].execID = msgBody["id"].(string)
	}
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	c, pollCmds := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{HostId: cmdHostID, Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}}, map[string]string{"Authorization": "Bearer " + cmdHostSecret})
	if c != 200 {
		t.Fatalf("poll for commands = %d %v", c, pollCmds)
	}
	cmds, _ := pollCmds["commands"].([]any)
	for _, item := range cmds {
		m := item.(map[string]any)
		for idx := range targets {
			if m["executionId"] == targets[idx].execID {
				targets[idx].cmdID = m["id"].(string)
			}
		}
	}
	for idx := range targets {
		if targets[idx].cmdID == "" {
			t.Fatalf("expected command assigned to %s for exec %s, got %v", cmdHostID, targets[idx].execID, pollCmds)
		}
	}

	// Pre-create sessions for concurrent message appends.
	const numMsgs = 10
	sessIDs := make([]string, numMsgs)
	for i := range numMsgs {
		c, b := eCreateSession(t, env)
		if c != 200 {
			t.Fatalf("create sess %d = %d %v", i, c, b)
		}
		sessIDs[i] = b["sessionId"].(string)
	}

	globalHeld := make(chan struct{})
	releaseGlobal := make(chan struct{})
	globalDone := make(chan error, 1)
	go func() {
		globalDone <- env.appStore.Tx(env.ctx, nil, true, GLOBAL, func(tx pgx.Tx) error {
			close(globalHeld)
			select {
			case <-releaseGlobal:
				return nil
			case <-time.After(5 * time.Second):
				return errors.New("test timeout waiting for concurrent work while GLOBAL held")
			}
		})
	}()
	<-globalHeld

	var wg sync.WaitGroup
	errs := make(chan error, numHosts+numMsgs+2)
	for i := range numHosts {
		i := i
		hostID := fmt.Sprintf("h-conc-%02d", i)
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, b := env.callJSON(t, "POST", "/api/hosts/poll", contract.PollRequest{
				HostId: hostID, Platform: contract.Platform("darwin"), Capabilities: []string{"codex"}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{},
			}, map[string]string{"Authorization": "Bearer " + hostSecrets[i]})
			if c != 200 {
				errs <- fmt.Errorf("host %d poll = %d %v", i, c, b)
			}
		}()
	}
	for i, status := range []string{"accepted", "started"} {
		i, status := i, status
		tgt := targets[i]
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, b := env.callJSON(t, "POST", "/api/hosts/receipts", doc{
				"commandId": tgt.cmdID, "executionId": tgt.execID, "status": status, "nativeSessionId": fmt.Sprintf("nat-%d", i),
			}, map[string]string{"Authorization": "Bearer " + tgt.secret})
			if c != 200 {
				errs <- fmt.Errorf("non-terminal receipt %d (%s) = %d %v", i, status, c, b)
			}
		}()
	}
	for i := range numMsgs {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, b := env.callJSON(t, "POST", "/api/sessions/"+sessIDs[i]+"/messages", doc{
				"requestId": fmt.Sprintf("req-conc-%d", i), "text": "hello under global lock",
			}, env.ownerHeaders())
			if c != 200 {
				errs <- fmt.Errorf("session msg %d = %d %v", i, c, b)
			}
		}()
	}

	doneWait := make(chan struct{})
	go func() {
		wg.Wait()
		close(doneWait)
	}()
	select {
	case <-doneWait:
	case <-time.After(4 * time.Second):
		close(releaseGlobal)
		t.Fatal("concurrent polls/receipts/messages blocked waiting on GLOBAL lock")
	}
	close(releaseGlobal)
	if err := <-globalDone; err != nil {
		t.Fatalf("GLOBAL holder: %v", err)
	}
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func eCreateSession(t *testing.T, env *testCoordEnv) (int, map[string]any) {
	t.Helper()
	return env.callJSON(t, "POST", "/api/sessions", doc{}, env.ownerHeaders())
}

// Requirement: PR5 long-poll wait parameter clamping (< executor 35s client timeout), immediate return for queued commands, and instant channel wakeup.
// Risk: idle hosts poll every 100ms or hang until executor's HTTP client times out.
func TestHostLongPollImmediateAndWakeup(t *testing.T) {
	if got := parsePollWait(httptest.NewRequest("POST", "/api/hosts/poll?wait=90s", nil)); got != maxHostPollWait || maxHostPollWait >= 35*time.Second {
		t.Fatalf("clamped wait = %s (must be maxHostPollWait < 35s)", got)
	}
	if got := parsePollWait(httptest.NewRequest("POST", "/api/hosts/poll", nil)); got != 0 {
		t.Fatalf("default wait = %s, want 0", got)
	}

	env := openTestCoordinator(t, true)
	sec := env.registerHost(t, "h-lp")
	hdr := map[string]string{"Authorization": "Bearer " + sec}

	// First empty poll returns cleanly within deadline.
	t0 := time.Now()
	c, b := env.callJSON(t, "POST", "/api/hosts/poll?wait=120ms", contract.PollRequest{
		HostId: "h-lp", Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{},
	}, hdr)
	if c != 200 || time.Since(t0) < 90*time.Millisecond || time.Since(t0) > 2*time.Second {
		t.Fatalf("timed long-poll status=%d elapsed=%s body=%v", c, time.Since(t0), b)
	}

	// Start a 25s long-poll and wake it immediately via a queued command.
	type resp struct {
		code    int
		body    map[string]any
		elapsed time.Duration
	}
	ch := make(chan resp, 1)
	start := time.Now()
	go func() {
		code, body := env.callJSON(t, "POST", "/api/hosts/poll?wait=25s", contract.PollRequest{
			HostId: "h-lp", Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{},
		}, hdr)
		ch <- resp{code, body, time.Since(start)}
	}()
	time.Sleep(80 * time.Millisecond)

	sc, sb := eCreateSession(t, env)
	if sc != 200 {
		t.Fatalf("create session = %d %v", sc, sb)
	}
	mc, mb := env.callJSON(t, "POST", "/api/sessions/"+sb["sessionId"].(string)+"/messages", doc{"requestId": "lp-req-1", "text": "wake host"}, env.ownerHeaders())
	if mc != 200 {
		t.Fatalf("message = %d %v", mc, mb)
	}

	got := <-ch
	if got.code != 200 || got.elapsed > 3*time.Second {
		t.Fatalf("long-poll wakeup slow/failed: status=%d elapsed=%s body=%v", got.code, got.elapsed, got.body)
	}
	cmds, _ := got.body["commands"].([]any)
	if len(cmds) == 0 {
		t.Fatalf("expected woken long-poll to carry start command, got %v", got.body)
	}

	// Verify immediate return when command already pending.
	sc2, sb2 := eCreateSession(t, env)
	if sc2 != 200 {
		t.Fatal(sc2)
	}
	mc2, _ := env.callJSON(t, "POST", "/api/sessions/"+sb2["sessionId"].(string)+"/messages", doc{"requestId": "lp-req-2", "text": "pending already"}, env.ownerHeaders())
	if mc2 != 200 {
		t.Fatal(mc2)
	}
	_ = env.app.waitDispatchCaughtUp(env.ctx, env.app.triggerDispatch())
	tImm := time.Now()
	c2, b2 := env.callJSON(t, "POST", "/api/hosts/poll?wait=25s", contract.PollRequest{
		HostId: "h-lp", Platform: contract.Platform("darwin"), Capabilities: []string{}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{},
	}, hdr)
	if c2 != 200 || time.Since(tImm) > 500*time.Millisecond || len(b2["commands"].([]any)) == 0 {
		t.Fatalf("expected immediate command return, status=%d elapsed=%s body=%v", c2, time.Since(tImm), b2)
	}
}

// Requirement: PR5 heartbeat DB write debouncing (< 30s unchanged skips PostgreSQL writes; online overlay keeps host online; state change or >= 30s flushes).
// Risk: unchanged host polls cause unnecessary PostgreSQL writes or debounced hosts falsely flip offline at 30s.
func TestHeartbeatDebounceAndOnlineOverlay(t *testing.T) {
	env := openTestCoordinator(t, false)
	sec := env.registerHost(t, "h-hb")
	hdr := map[string]string{"Authorization": "Bearer " + sec}
	reqBody := contract.PollRequest{
		HostId: "h-hb", Platform: contract.Platform("darwin"), Capabilities: []string{"codex"}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{},
	}

	c, _ := env.callJSON(t, "POST", "/api/hosts/poll", reqBody, hdr)
	if c != 200 {
		t.Fatal(c)
	}
	var updated1 time.Time
	if err := env.ownerStore.pool.QueryRow(env.ctx, "SELECT updated_at FROM document WHERE id='h-hb' AND kind='host-observation'").Scan(&updated1); err != nil {
		t.Fatal(err)
	}

	// Second unchanged poll within 30s must NOT rewrite host-observation in DB.
	time.Sleep(15 * time.Millisecond)
	c, _ = env.callJSON(t, "POST", "/api/hosts/poll", reqBody, hdr)
	if c != 200 {
		t.Fatal(c)
	}
	var updated2 time.Time
	if err := env.ownerStore.pool.QueryRow(env.ctx, "SELECT updated_at FROM document WHERE id='h-hb' AND kind='host-observation'").Scan(&updated2); err != nil {
		t.Fatal(err)
	}
	if !updated2.Equal(updated1) {
		t.Fatalf("unchanged heartbeat within 30s rewrote DB (updated_at %v -> %v)", updated1, updated2)
	}

	// Simulate DB timestamp aging beyond 30s while memory heartbeat saw a recent poll: overlay must keep host online.
	staleDBTime := nowMs() - onlineMs - 5000
	if _, err := env.ownerStore.pool.Exec(env.ctx, "UPDATE document SET data = jsonb_set(data, '{lastSeenAt}', to_jsonb($1::bigint)) WHERE id='h-hb' AND kind='host-observation'", staleDBTime); err != nil {
		t.Fatal(err)
	}
	var online bool
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		dctx, err := loadDispatchContext(q)
		if err != nil {
			return err
		}
		online = float64(nowMs())-dctx.seen["h-hb"] <= onlineMs
		return nil
	}); err != nil || !online {
		t.Fatalf("expected effectiveHostSeen overlay to keep h-hb online (err=%v, online=%v)", err, online)
	}

	// State change must flush immediately.
	time.Sleep(15 * time.Millisecond)
	changed := reqBody
	changed.Capabilities = []string{"codex", "steer"}
	c, _ = env.callJSON(t, "POST", "/api/hosts/poll", changed, hdr)
	if c != 200 {
		t.Fatal(c)
	}
	var updated3 time.Time
	if err := env.ownerStore.pool.QueryRow(env.ctx, "SELECT updated_at FROM document WHERE id='h-hb' AND kind='host-observation'").Scan(&updated3); err != nil {
		t.Fatal(err)
	}
	if !updated3.After(updated2) {
		t.Fatalf("state change did not flush host-observation (%v -> %v)", updated2, updated3)
	}

	// Backdate lastPersistedAt by >= 30s: next unchanged poll must flush DB.
	env.app.pollMu.Lock()
	env.app.heartbeats["h-hb"].lastPersistedAt -= hostHeartbeatFlushMs + 1000
	env.app.pollMu.Unlock()
	time.Sleep(15 * time.Millisecond)
	c, _ = env.callJSON(t, "POST", "/api/hosts/poll", changed, hdr)
	if c != 200 {
		t.Fatal(c)
	}
	var updated4 time.Time
	if err := env.ownerStore.pool.QueryRow(env.ctx, "SELECT updated_at FROM document WHERE id='h-hb' AND kind='host-observation'").Scan(&updated4); err != nil {
		t.Fatal(err)
	}
	if !updated4.After(updated3) {
		t.Fatalf("heartbeat after >= 30s interval did not flush DB (%v -> %v)", updated3, updated4)
	}
}

// Requirement: PR5 env cache hits across dispatch passes and invalidates cleanly under concurrency on project/definition writes.
// Risk: stale cached project environments leak deleted definitions or race with concurrent readers.
func TestDispatchLoopAndEnvCacheInvalidation(t *testing.T) {
	env := openTestCoordinator(t, true)
	var g0 uint64
	if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
		envs1, err := env.app.projectEnvironmentsCached(q)
		if err != nil {
			return err
		}
		if _, ok := envs1["hello-world"]; !ok {
			return fmt.Errorf("expected hello-world in project environments, got %v", envs1)
		}
		g0 = env.app.envCacheGen.Load()
		envs2, err := env.app.projectEnvironmentsCached(q)
		if err != nil {
			return err
		}
		if _, ok := envs2["hello-world"]; !ok {
			return errors.New("cached envs missing hello-world")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Run concurrent cached lookups while archiving/invalidating on commit under -race.
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				if err := runTx(env.ctx, env.app, nil, false, "", func(q *Request) error {
					_, err := env.app.projectEnvironmentsCached(q)
					return err
				}); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 5 {
			if err := runTx(env.ctx, env.app, nil, true, GLOBAL, func(q *Request) error {
				invalidateEnvCacheOnCommit(q)
				return nil
			}); err != nil {
				errs <- err
				return
			}
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if env.app.envCacheGen.Load() <= g0 {
		t.Fatalf("expected envCacheGen > %d after commit invalidations, got %d", g0, env.app.envCacheGen.Load())
	}
}

// Requirement: PR5 migration 52 fleet_artifact_change() suppresses no-op UPDATE store change rows while preserving real changes.
// Risk: periodic store observations flood change/NOTIFY feed on unchanged store tuples or migration fails on existing DBs.
func TestNoOpStoreTriggerSuppression(t *testing.T) {
	sockDir, pgPort, binDir := ensureTestPostgres(t)
	db := fmt.Sprintf("fleet_mig52_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	rolesRaw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "infrastructure", "fleet-postgres.sql"))
	if err != nil {
		t.Fatal(err)
	}
	rolesSQL := strings.NewReplacer(
		"DATABASE fleet", "DATABASE "+db,
		"datname = 'fleet'", "datname = '"+db+"'",
		"\\connect fleet", "\\connect "+db,
	).Replace(string(rolesRaw))
	runTestPSQL(t, binDir, sockDir, pgPort, "postgres", "postgres", rolesSQL)
	t.Cleanup(func() {
		_, _ = osexec.Command(filepath.Join(binDir, "psql"), "-h", sockDir, "-p", fmt.Sprintf("%d", pgPort), "-U", "postgres", "-d", "postgres", "-c", fmt.Sprintf("DROP DATABASE IF EXISTS %s WITH (FORCE)", db)).CombinedOutput()
	})

	ctx := context.Background()
	ownerURL := fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", db, sockDir, pgPort)
	s, err := openStore(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer s.pool.Close()

	mig52At := strings.Index(schemaSQL, "-- migration 52")
	if mig52At < 0 {
		t.Fatal("schema.sql missing -- migration 52")
	}
	if err := migrateSQL(ctx, ownerURL, schemaSQL[:mig52At]); err != nil {
		t.Fatalf("pre-52 migrate: %v", err)
	}
	t0 := time.Now()
	if err := migrate(ctx, ownerURL); err != nil {
		t.Fatalf("migration 52 upgrade: %v", err)
	}
	if elapsed := time.Since(t0); elapsed > time.Second {
		t.Fatalf("migration 52 took %s (> 1s limit)", elapsed)
	}

	var maxVer int
	if err := s.pool.QueryRow(ctx, "SELECT max(version) FROM schema_migration").Scan(&maxVer); err != nil {
		t.Fatal(err)
	}
	if maxVer < 52 {
		t.Fatalf("expected schema_migration max(version) >= 52, got %d", maxVer)
	}

	if _, err := s.pool.Exec(ctx, `
		INSERT INTO store(id, host_id, kind, bucket, endpoint, capacity_bytes, used_bytes, state, observed_at, updated_at)
		VALUES ('store-trig-1', 'h-trig', 's3', 'fleet-test', 'https://s3.test', 1000, 100, 'reachable', now(), now())`); err != nil {
		t.Fatal(err)
	}
	countChanges := func() int {
		var n int
		if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM change WHERE kind='host'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := countChanges(); n != 1 {
		t.Fatalf("expected 1 change after store INSERT, got %d", n)
	}
	// Timestamp-only UPDATE must emit 0 new change rows.
	if _, err := s.pool.Exec(ctx, `
		UPDATE store SET observed_at = now() + interval '10 seconds', updated_at = now() + interval '10 seconds'
		WHERE id='store-trig-1'`); err != nil {
		t.Fatal(err)
	}
	if n := countChanges(); n != 1 {
		t.Fatalf("expected no-op store UPDATE to emit 0 change rows, total became %d", n)
	}
	// Real state/usage UPDATE must emit 1 change row.
	if _, err := s.pool.Exec(ctx, `
		UPDATE store SET used_bytes = 250
		WHERE id='store-trig-1'`); err != nil {
		t.Fatal(err)
	}
	if n := countChanges(); n != 2 {
		t.Fatalf("expected real store UPDATE to emit 1 change row, total became %d", n)
	}

	// Verify storesObserve SQL guard also skips unnecessary timestamp update inside 30s.
	storesPayload := []any{
		doc{"id": "store-trig-1", "capacityBytes": float64(1000), "usedBytes": float64(250), "reachable": true},
	}
	if err := s.Tx(ctx, nil, true, "host:h-trig", func(tx pgx.Tx) error {
		return storesObserve(ctx, tx, "h-trig", storesPayload)
	}); err != nil {
		t.Fatal(err)
	}
	var obsMid time.Time
	if err := s.pool.QueryRow(ctx, "SELECT observed_at FROM store WHERE id='store-trig-1'").Scan(&obsMid); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	if err := s.Tx(ctx, nil, true, "host:h-trig", func(tx pgx.Tx) error {
		return storesObserve(ctx, tx, "h-trig", storesPayload)
	}); err != nil {
		t.Fatal(err)
	}
	var obsAfter time.Time
	if err := s.pool.QueryRow(ctx, "SELECT observed_at FROM store WHERE id='store-trig-1'").Scan(&obsAfter); err != nil {
		t.Fatal(err)
	}
	if !obsAfter.Equal(obsMid) {
		t.Fatalf("expected storesObserve unchanged within 30s to skip UPDATE (observed_at %v -> %v)", obsMid, obsAfter)
	}
}
