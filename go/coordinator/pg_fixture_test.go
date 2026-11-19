package main

import (
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// PostgreSQL tests never pass silently (README.md#verification). Two kinds exist:
//   - local: ensureTestPostgres starts a private server (initdb, pg_ctl) and openTestCoordinator a database on it;
//   - fixture halves: the Go half of an outside test file in tests/go, which installs fault triggers, seeds through the
//     coordinator's HTTP API and restarts it around the Go half, and passes the database URL in an environment variable.
//
// FLEET_REQUIRE_PG=1 turns every "cannot run here" skip into a failure. FLEET_PG_FIXTURES=outside makes the fixture
// halves skip instead of running their owner file: CI's unit part, whose outside part runs those files.

// pgChildEnv: the environment of initdb, pg_ctl and psql. initdb refuses a missing or unknown locale ("invalid locale
// settings"), the default on macOS without LANG; C.UTF-8 exists on macOS and on glibc 2.35 and later.
func pgChildEnv() []string { return append(os.Environ(), "LC_ALL=C.UTF-8", "LANG=C.UTF-8") }

// pgUnavailable skips a PostgreSQL test that cannot run on this machine, naming why, or fails it with FLEET_REQUIRE_PG=1.
func pgUnavailable(t *testing.T, format string, args ...any) {
	t.Helper()
	if os.Getenv("FLEET_REQUIRE_PG") == "1" {
		t.Fatalf(format+" (FLEET_REQUIRE_PG=1: every PostgreSQL test must run)", args...)
	}
	t.Skipf(format+" (FLEET_REQUIRE_PG=1 fails instead)", args...)
}

type outsideRun struct {
	once sync.Once
	out  string
	err  error
}

var outsideRuns sync.Map // outside test file -> *outsideRun

// outsideFixtureURL returns the database URL that file (tests/go/<name>.test.ts, the owner of this test's fixture)
// passes in env. Without it the test runs that file instead, once per test process: it builds go/bin/fleet-coordinator,
// runs `node tests/go/run-files.ts 1 <file>` and passes when the file passed and names this test; ok false then
// means the test is done.
func outsideFixtureURL(t *testing.T, env, file string) (url string, ok bool) {
	t.Helper()
	if u := os.Getenv(env); u != "" {
		return u, true
	}
	if os.Getenv("FLEET_PG_FIXTURES") == "outside" {
		t.Skipf("fixture half of %s, run there (FLEET_PG_FIXTURES=outside): node tests/go/run-files.ts 1 %s", file, file)
	}
	v, _ := outsideRuns.LoadOrStore(file, &outsideRun{})
	r := v.(*outsideRun)
	r.once.Do(func() { r.out, r.err = runOutsideFile(file) })
	if errors.Is(r.err, osexec.ErrNotFound) {
		pgUnavailable(t, "fixture half of %s: %v", file, r.err)
	}
	if r.err != nil {
		t.Fatalf("%s (the owner of this test's fixture): %v\n%s", file, r.err, outputTail(r.out))
	}
	// Some owner files run their Go half without -v (no PASS line); each asserts its exit or PASS line itself.
	if src, err := os.ReadFile(filepath.Join("..", "..", file)); err != nil || !strings.Contains(string(src), t.Name()) {
		t.Fatalf("%s does not run %s (%v)", file, t.Name(), err)
	}
	t.Logf("ran as the fixture half of %s: passed", file)
	return "", false
}

func runOutsideFile(file string) (string, error) {
	node, err := osexec.LookPath("node")
	if err != nil {
		return "", err
	}
	goBin := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(goBin); err != nil {
		if goBin, err = osexec.LookPath("go"); err != nil {
			return "", err
		}
	}
	// The binary the outside tests start (tests/go/run-files.ts), built from this checkout as pnpm run go:build does.
	build := osexec.Command(goBin, "build", "-trimpath", "-o", "bin/fleet-coordinator", "./coordinator")
	build.Dir = ".."
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		return string(out), fmt.Errorf("go build ./coordinator: %w", err)
	}
	run := osexec.Command(node, "tests/go/run-files.ts", "1", file)
	run.Dir = filepath.Join("..", "..")
	run.Env = pgChildEnv()
	out, err := run.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("node tests/go/run-files.ts 1 %s: %w", file, err)
	}
	return string(out), nil
}

func outputTail(s string) string {
	if len(s) <= 8000 {
		return s
	}
	return "...\n" + s[len(s)-8000:]
}
