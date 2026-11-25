package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
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

// testPGClusterEnv hands a running test cluster ("sockdir|port|bindir") to a child go test (TestLawMutants), so the
// child starts no cluster of its own: a cluster per child can exhaust the host's SysV shared memory (macOS allows 32
// segments), and a child that cannot start one skips its tests and looks green.
const testPGClusterEnv = "FLEET_TEST_PG_CLUSTER"

// TestMain leaves the machine's test cluster running (pg_fixture_test.go); it drops this process's law template.
func TestMain(m *testing.M) {
	code := m.Run()
	if lawTemplate.name != "" {
		dropTestDB(lawTemplate.sock, lawTemplate.port, lawTemplate.name)
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
		pgUnavailable(t, "postgres unavailable: initdb cannot run as root")
	}
	testPGOnce.Do(func() {
		if v := os.Getenv(testPGClusterEnv); v != "" {
			parts := strings.Split(v, "|")
			if len(parts) == 3 {
				if p, err := strconv.Atoi(parts[1]); err == nil {
					testPGDir, testPGPort, testPGBin = parts[0], p, parts[2]
					return
				}
			}
			testPGErr = fmt.Errorf("%s=%q is not sockdir|port|bindir", testPGClusterEnv, v)
			return
		}
		bin, err := findPostgresBinDir()
		if err != nil {
			testPGErr = err
			return
		}
		dir, err := testPGDataDir()
		if err != nil {
			testPGErr = err
			return
		}
		p, err := testPGPortNumber()
		if err != nil {
			testPGErr = fmt.Errorf("FLEET_TEST_PGPORT: %v", err)
			return
		}
		if err := startTestCluster(bin, dir, p); err != nil {
			testPGErr = err
			return
		}
		testPGDir, testPGBin, testPGPort = dir, bin, p
	})
	if testPGErr != nil {
		pgUnavailable(t, "postgres unavailable: %v", testPGErr)
	}
	return testPGDir, testPGPort, testPGBin
}

// testConfigDir: the shipped config/ (but its Lab assets) plus the fixture projects of tests/fixtures/projects, in a
// fresh directory per test (tests/harness.ts testConfig does the same for the outside tests).
func testConfigDir(t *testing.T, root string) string {
	t.Helper()
	dir := t.TempDir()
	copyTree := func(from, to string, skip string) {
		err := filepath.WalkDir(from, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if skip != "" && path == skip {
				return filepath.SkipDir
			}
			rel, _ := filepath.Rel(from, path)
			target := filepath.Join(to, rel)
			if d.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return os.WriteFile(target, b, 0o644)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	copyTree(filepath.Join(root, "config"), dir, filepath.Join(root, "config", "lab"))
	copyTree(filepath.Join(root, "tests", "fixtures", "projects"), filepath.Join(dir, "projects"), "")
	return dir
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
	cloneTestDB(t, db) // migrated already; dropped when the test ends

	root := filepath.Join("..", "..")
	t.Setenv("FLEET_PROFILES_DIRECTORY", filepath.Join(root, "config", "agents"))
	t.Setenv("FLEET_PROJECT_CONFIG_DIRECTORY", testConfigDir(t, root))
	t.Setenv("FLEET_LAB_DIRECTORY", filepath.Join(root, "config", "lab"))

	ctx, cancel := context.WithCancel(context.Background())
	ownerURL := fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", db, sockDir, pgPort)
	appURL := fmt.Sprintf("postgresql://fleet_app@/%s?host=%s&port=%d", db, sockDir, pgPort)

	ownerStore, err := openStore(ctx, ownerURL)
	if err != nil {
		cancel()
		t.Fatalf("open owner store: %v", err)
	}
	appStore, err := openStore(ctx, appURL)
	if err != nil {
		ownerStore.Close()
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
		appStore.Close()
		ownerStore.Close()
		cancel()
		t.Fatalf("loadPeople: %v", err)
	}
	files, broken := shippedDefinitions()
	if err := a.store.Write(ctx, nil, []string{GLOBAL}, func(q *Request) error {
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
		appStore.Close()
		ownerStore.Close()
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

// Requirement: PR5 immediate polls complete for their captured dispatch generation.
// Risk: unrelated dispatch requests force a poll to wait for global quiescence indefinitely.
func TestDispatchWaitIgnoresLaterRequests(t *testing.T) {
	a := &App{}
	a.ensureRuntime()
	a.dispatchLoopStarted.Store(true)
	a.dispatchMu.Lock()
	a.dispatchReqGen = 2
	a.dispatchDoneGen = 1
	a.dispatchMu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.waitDispatchCaughtUp(ctx, 1); err != nil {
		t.Fatalf("captured generation already completed despite later requests: %v", err)
	}
}
