package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	osexec "os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

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

// The test PostgreSQL (docs/designs/fleet-host-cleanup.md#8-test-postgresql): one cluster per machine in
// FLEET_TEST_PGDATA (default ~/.cache/fleet-test/pg) on FLEET_TEST_PGPORT (default 54339), Unix socket in the data
// directory, started on first use and left running; tests/harness.ts uses the same one. A test database is a file copy
// of fleet_tmpl_<schema hash>, migrated once per schema version.
const (
	testPGDefaultPort = 54339
	// testPGOptions: tests/harness.ts starts the cluster the same way. The cluster stays durable (an outside test checks
	// fsync); each Go test database turns synchronous_commit off for itself.
	testPGOptions = "-c listen_addresses= -c max_connections=400"
	// testPGStaleAge: a test database this old is a killed run's leftover.
	testPGStaleAge = 24 * time.Hour
)

func testPGDataDir() (string, error) {
	if d := os.Getenv("FLEET_TEST_PGDATA"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "fleet-test", "pg"), nil
}

func testPGPortNumber() (int, error) {
	if v := os.Getenv("FLEET_TEST_PGPORT"); v != "" {
		return strconv.Atoi(v)
	}
	return testPGDefaultPort, nil
}

// pgServerEnv: the environment of the long-lived server. Without the run marker, so the executor's cleanup does not
// take a cluster other runs share for an orphan of the run that happened to start it.
func pgServerEnv() []string {
	return slices.DeleteFunc(pgChildEnv(), func(kv string) bool { return strings.HasPrefix(kv, "FLEET_EXECUTION_ID=") })
}

func testPGReady(bin, dir string, port int) bool {
	cmd := osexec.Command(filepath.Join(bin, "pg_isready"), "-q", "-h", dir, "-p", strconv.Itoa(port))
	cmd.Env = pgChildEnv()
	return cmd.Run() == nil
}

// startTestCluster makes the machine's test cluster ready: initdb only when the directory has none, pg_ctl start only
// when it does not answer. Two processes may race: initdb goes to a private directory renamed into place (the loser's
// rename fails and it uses the winner's), and the loser of pg_ctl start waits for the winner's server.
func startTestCluster(bin, dir string, port int) error {
	if testPGReady(bin, dir, port) {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "PG_VERSION")); err != nil {
		if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
			return err
		}
		tmp := fmt.Sprintf("%s.init-%d", dir, os.Getpid())
		_ = os.RemoveAll(tmp)
		initCmd := osexec.Command(filepath.Join(bin, "initdb"), "-D", tmp, "-U", "postgres", "-A", "trust", "-E", "UTF8", "--no-instructions", "--no-sync")
		initCmd.Env = pgChildEnv()
		if out, err := initCmd.CombinedOutput(); err != nil {
			_ = os.RemoveAll(tmp)
			return fmt.Errorf("initdb: %v: %s", err, out)
		}
		if err := os.Rename(tmp, dir); err != nil {
			_ = os.RemoveAll(tmp)
			if _, statErr := os.Stat(filepath.Join(dir, "PG_VERSION")); statErr != nil {
				return fmt.Errorf("initdb into %s: %v", dir, err)
			}
		}
	}
	// pg_ctl hands -o to /bin/sh: quote the socket directory, which may hold spaces (macOS Application Support).
	opts := fmt.Sprintf("-k '%s' -p %d %s", strings.ReplaceAll(dir, "'", `'\''`), port, testPGOptions)
	start := osexec.Command(filepath.Join(bin, "pg_ctl"), "-D", dir, "-l", filepath.Join(dir, "server.log"), "-w", "-t", "60", "-o", opts, "start")
	start.Env = pgServerEnv()
	out, startErr := start.CombinedOutput()
	if startErr == nil {
		return nil
	}
	for deadline := time.Now().Add(60 * time.Second); time.Now().Before(deadline); time.Sleep(200 * time.Millisecond) {
		if testPGReady(bin, dir, port) {
			return nil
		}
	}
	return fmt.Errorf("pg_ctl start: %v: %s", startErr, out)
}

// testSchemaHash: what decides a migrated database's content (schema.sql, migrations/, schema/*.sql and the roles script).
func testSchemaHash() (string, error) {
	h := sha256.New()
	h.Write([]byte(migrationsText()))
	files, err := fs.Glob(schemaFS, "schema/*.sql")
	if err != nil {
		return "", err
	}
	sort.Strings(files)
	for _, f := range files {
		b, err := schemaFS.ReadFile(f)
		if err != nil {
			return "", err
		}
		h.Write([]byte(f))
		h.Write(b)
	}
	roles, err := os.ReadFile(filepath.Join("..", "..", "deploy", "infrastructure", "fleet-postgres.sql"))
	if err != nil {
		return "", err
	}
	h.Write(roles)
	return hex.EncodeToString(h.Sum(nil))[:12], nil
}

// testRolesSQL: deploy/infrastructure/fleet-postgres.sql for database db (roles are per cluster).
func testRolesSQL(db string) (string, error) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "deploy", "infrastructure", "fleet-postgres.sql"))
	if err != nil {
		return "", err
	}
	return strings.NewReplacer("DATABASE fleet", "DATABASE "+db, "datname = 'fleet'", "datname = '"+db+"'",
		"\\connect fleet", "\\connect "+db).Replace(string(raw)), nil
}

func testPGURL(role, db, sock string, port int) string {
	return fmt.Sprintf("postgresql://%s@/%s?host=%s&port=%d", role, db, sock, port)
}

var testTemplate struct {
	once sync.Once
	name string
	err  error
}

// testTemplateDB returns fleet_tmpl_<schema hash>, migrated. Built once per schema version under an advisory lock, so
// parallel test processes wait for one build; datistemplate marks it complete. The first call per process also drops
// test databases older than a day (testPGSweep).
func testTemplateDB(t *testing.T) string {
	t.Helper()
	sock, port, bin := ensureTestPostgres(t)
	testTemplate.once.Do(func() {
		testTemplate.name, testTemplate.err = buildTestTemplate(sock, port, bin)
	})
	if testTemplate.err != nil {
		t.Fatalf("test template database: %v", testTemplate.err)
	}
	return testTemplate.name
}

func buildTestTemplate(sock string, port int, bin string) (string, error) {
	hash, err := testSchemaHash()
	if err != nil {
		return "", err
	}
	name := "fleet_tmpl_" + hash
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, testPGURL("postgres", "postgres", sock, port))
	if err != nil {
		return "", err
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `SELECT pg_advisory_lock(hashtext('fleet-test-template'))`); err != nil {
		return "", err
	}
	defer admin.Exec(ctx, `SELECT pg_advisory_unlock(hashtext('fleet-test-template'))`) //nolint:errcheck
	testPGSweep(ctx, admin, name)
	var ready bool
	err = admin.QueryRow(ctx, `SELECT datistemplate FROM pg_database WHERE datname = $1`, name).Scan(&ready)
	if err == nil && ready {
		return name, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return "", err
	}
	// A build cut short leaves the database without the mark: start over.
	if _, err := admin.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`); err != nil {
		return "", err
	}
	rolesSQL, err := testRolesSQL(name)
	if err != nil {
		return "", err
	}
	psql := osexec.Command(filepath.Join(bin, "psql"), "-h", sock, "-p", strconv.Itoa(port), "-U", "postgres", "-d", "postgres", "-v", "ON_ERROR_STOP=1", "-q")
	psql.Env = pgChildEnv()
	psql.Stdin = strings.NewReader(rolesSQL)
	if out, err := psql.CombinedOutput(); err != nil {
		return "", fmt.Errorf("roles: %v: %s", err, out)
	}
	if err := migrate(ctx, testPGURL("fleet", name, sock, port)); err != nil {
		return "", fmt.Errorf("migrate: %w", err)
	}
	if _, err := admin.Exec(ctx, `ALTER DATABASE `+name+` WITH IS_TEMPLATE true`); err != nil {
		return "", err
	}
	return name, nil
}

// testPGSweep drops what killed runs left: Go test databases (fleet_go_test_, fleet_mig52_test_, fleet_law_) whose
// creating process (the pid in the name) is gone, and those and templates of other schema versions created more than a
// day ago (the mtime of the database directory's PG_VERSION). A process that dies without its cleanups (a panic, a
// kill, a planted mutant) leaves its databases only until the next test process starts.
func testPGSweep(ctx context.Context, admin *pgx.Conn, keep string) {
	rows, err := admin.Query(ctx, `SELECT datname, coalesce((pg_stat_file('base/' || oid || '/PG_VERSION', true)).modification < now() - make_interval(secs => $2), false)
		FROM pg_database WHERE datname ~ '^fleet_(go_test|mig52_test|law|tmpl)_' AND datname <> $1`, keep, testPGStaleAge.Seconds())
	if err != nil {
		return
	}
	type db struct {
		name  string
		stale bool
	}
	dbs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (db, error) {
		var d db
		return d, r.Scan(&d.name, &d.stale)
	})
	if err != nil {
		return
	}
	for _, d := range dbs {
		if !d.stale && !testDBOwnerGone(d.name) {
			continue
		}
		if strings.HasPrefix(d.name, "fleet_tmpl_") {
			_, _ = admin.Exec(ctx, `ALTER DATABASE `+d.name+` WITH IS_TEMPLATE false`)
		}
		_, _ = admin.Exec(ctx, `DROP DATABASE IF EXISTS `+d.name+` WITH (FORCE)`)
	}
}

var testDBPid = regexp.MustCompile(`^fleet_(?:go_test|mig52_test|law|law_tmpl)_(\d+)(?:_\d+)?$`)

// testDBOwnerGone: the test process named in a test database's name no longer exists (the cluster is per machine).
func testDBOwnerGone(name string) bool {
	m := testDBPid.FindStringSubmatch(name)
	if m == nil {
		return false
	}
	pid, err := strconv.Atoi(m[1])
	if err != nil || pid == os.Getpid() {
		return false
	}
	p, err := os.FindProcess(pid)
	return err == nil && errors.Is(p.Signal(syscall.Signal(0)), os.ErrProcessDone)
}

// cloneTestDB creates database name as a copy of the template, with the database grants of fleet-postgres.sql
// (database ACLs are not copied), and drops it when the test ends.
func cloneTestDB(t *testing.T, name string) {
	t.Helper()
	tmpl := testTemplateDB(t)
	sock, port, _ := ensureTestPostgres(t)
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, testPGURL("postgres", "postgres", sock, port))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	for _, s := range []string{`CREATE DATABASE ` + name + ` TEMPLATE ` + tmpl + ` OWNER fleet STRATEGY FILE_COPY`,
		`REVOKE ALL ON DATABASE ` + name + ` FROM PUBLIC`, `GRANT CONNECT ON DATABASE ` + name + ` TO fleet_app`,
		`ALTER DATABASE ` + name + ` SET synchronous_commit = off`} {
		if _, err := admin.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
	t.Cleanup(func() { dropTestDB(sock, port, name) })
}

func dropTestDB(sock string, port int, name string) {
	ctx := context.Background()
	if c, err := pgx.Connect(ctx, testPGURL("postgres", "postgres", sock, port)); err == nil {
		_, _ = c.Exec(ctx, `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
		_ = c.Close(ctx)
	}
}
