package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"fleet/contract"
)

// testPGBin: the directory with initdb, pg_ctl and psql, or "" (the test then skips, or fails with FLEET_REQUIRE_PG=1).
func testPGBin() string {
	dirs := []string{}
	if p, err := osexec.LookPath("pg_ctl"); err == nil {
		dirs = append(dirs, filepath.Dir(p))
	}
	matches, _ := filepath.Glob("/usr/lib/postgresql/*/bin")
	dirs = append(append(dirs, matches...), "/opt/homebrew/bin", "/usr/local/bin")
	for _, d := range dirs {
		if _, err := os.Stat(filepath.Join(d, "initdb")); err == nil {
			return d
		}
	}
	return ""
}

// testPGCluster starts a throwaway PostgreSQL on a Unix socket only and answers the socket path.
func testPGCluster(t *testing.T) (sock string, psql func(db, sql string)) {
	t.Helper()
	bin := testPGBin()
	if bin == "" || os.Geteuid() == 0 {
		if os.Getenv("FLEET_REQUIRE_PG") == "1" {
			t.Fatal("postgres binaries missing or running as root (FLEET_REQUIRE_PG=1)")
		}
		t.Skip("postgres unavailable (FLEET_REQUIRE_PG=1 fails instead)")
	}
	dir, err := os.MkdirTemp("/tmp", "fgw-pg-") // a Unix socket path must stay short
	if err != nil {
		t.Fatal(err)
	}
	env := append(os.Environ(), "LC_ALL=C.UTF-8", "LANG=C.UTF-8")
	data := filepath.Join(dir, "d")
	t.Cleanup(func() {
		cmd := osexec.Command(filepath.Join(bin, "pg_ctl"), "-D", data, "-m", "immediate", "stop")
		cmd.Env = env
		_ = cmd.Run()
		_ = os.RemoveAll(dir)
	})
	run := func(name string, args ...string) {
		cmd := osexec.Command(filepath.Join(bin, name), args...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s: %v: %s", name, err, out)
		}
	}
	run("initdb", "-D", data, "-U", "postgres", "-A", "trust", "-E", "UTF8", "--no-instructions", "--no-sync")
	run("pg_ctl", "-D", data, "-l", filepath.Join(dir, "log"), "-w", "-o", fmt.Sprintf("-k %s -p 5432 -c listen_addresses=", dir), "start")
	return filepath.Join(dir, ".s.PGSQL.5432"), func(db, sql string) {
		run("psql", "-X", "-q", "-v", "ON_ERROR_STOP=1", "-h", dir, "-p", "5432", "-U", "postgres", "-d", db, "-c", sql)
	}
}

// testSSHForwarder: an SSH server that accepts key and forwards every direct-tcpip channel to the Unix socket sock,
// as the grant's OS user's forwarding to 127.0.0.1:5432 does.
func testSSHForwarder(t *testing.T, key ssh.PublicKey, sock string) (addr, hostKey string) {
	t.Helper()
	_, hpriv, _ := ed25519.GenerateKey(rand.Reader)
	hs, _ := ssh.NewSignerFromKey(hpriv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) != string(key.Marshal()) {
			return nil, fmt.Errorf("unknown key")
		}
		return nil, nil
	}}
	cfg.AddHostKey(hs)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			go func() {
				_, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				go ssh.DiscardRequests(reqs)
				for nc := range chans {
					if nc.ChannelType() != "direct-tcpip" {
						_ = nc.Reject(ssh.UnknownChannelType, "forwarding only")
						continue
					}
					ch, creqs, err := nc.Accept()
					if err != nil {
						continue
					}
					go ssh.DiscardRequests(creqs)
					up, err := net.Dial("unix", sock)
					if err != nil {
						ch.Close()
						continue
					}
					go func() { _, _ = io.Copy(up, ch); up.Close() }()
					go func() { _, _ = io.Copy(ch, up); ch.Close() }()
				}
			}()
		}
	}()
	return l.Addr().String(), strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hs.PublicKey())))
}

// Requirement: a project-postgres read grant answers sql_query and sql_schema through the grant's SSH login; an
// excluded table and a write are refused by the database, and every call carries its SQL as the audit detail.
func TestAccessPostgresReadGrant(t *testing.T) {
	sock, psql := testPGCluster(t)
	psql("postgres", "CREATE DATABASE fleet")
	psql("fleet", `CREATE TABLE schema_migration(id text); INSERT INTO schema_migration VALUES ('a'), ('b');
		CREATE TABLE run_token(sha256 text); ALTER TABLE schema_migration ENABLE ROW LEVEL SECURITY; ALTER TABLE schema_migration FORCE ROW LEVEL SECURITY;
		CREATE POLICY none ON schema_migration USING (false);
		CREATE ROLE fleet_fleet_abc123 LOGIN BYPASSRLS PASSWORD 'pw0123456789abcdefghijkl';
		ALTER ROLE fleet_fleet_abc123 SET default_transaction_read_only = on;
		GRANT USAGE ON SCHEMA public TO fleet_fleet_abc123; GRANT SELECT ON schema_migration TO fleet_fleet_abc123`)

	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	block, _ := ssh.MarshalPrivateKey(priv, "")
	spub, _ := ssh.NewPublicKey(pub)
	addr, hostKey := testSSHForwarder(t, spub, sock)
	root := t.TempDir()
	access := filepath.Join(root, "access", "fleet@pds18")
	if err := os.MkdirAll(access, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(access, "id_ed25519"), pem.EncodeToMemory(block), 0o600)
	_ = os.WriteFile(filepath.Join(access, "u.pw"), []byte("pw0123456789abcdefghijkl\n"), 0o600)

	g := &gateway{config: &config{KeysDir: filepath.Join(root, "keys"), requestTimeout: time.Second}}
	tr := contract.GatewayTransportPostgres
	q := &request{connection: "fleet-postgres", p: provider{Transport: &tr}, c: connection{Access: &contract.GatewayAccess{
		Grant: "grant:1", Level: contract.GatewayAccessLevelRead, User: "fleet-fleet", Key: "fleet@pds18/id_ed25519",
		Hops: []contract.GatewayHop{{Address: addr, HostKey: hostKey}}, DbUser: new("fleet_fleet_abc123"), Password: new("fleet@pds18/u.pw"),
		Database: new("fleet")}}}
	call := func(tool, sql string) *answer {
		q.tool = tool
		args := map[string]json.RawMessage{}
		if sql != "" {
			args["sql"], _ = json.Marshal(sql)
		}
		return g.serveAccess(nil, context.Background(), q, args, "application/json")
	}

	a := call("sql_query", "SELECT id FROM schema_migration ORDER BY id DESC LIMIT 3")
	var rows sqlReply
	_ = json.Unmarshal(a.body, &rows)
	if a.status != 200 || rows.RowCount != 2 || rows.Rows[0][0] != "b" {
		t.Fatalf("read: %d %s", a.status, a.body)
	}
	if !strings.Contains(a.detail, "schema_migration") || !strings.Contains(a.detail, "rows: 2") {
		t.Fatalf("detail: %q", a.detail)
	}
	if a := call("sql_schema", ""); a.status != 200 || !strings.Contains(string(a.body), "schema_migration") || strings.Contains(string(a.body), "run_token") {
		t.Fatalf("schema: %d %s", a.status, a.body)
	}
	if a := call("sql_query", "SELECT sha256 FROM run_token"); a.err != "query-refused" {
		t.Fatalf("excluded table: %+v", a)
	}
	if a := call("sql_query", "INSERT INTO schema_migration VALUES ('x')"); a.err != "bad-query" {
		t.Fatalf("write statement: %+v", a)
	}
	// A write hidden in a read statement reaches the server and is refused there (read-only transaction, no privilege).
	if a := call("sql_query", "WITH x AS (INSERT INTO schema_migration VALUES ('x') RETURNING id) SELECT * FROM x"); a.err != "query-refused" {
		t.Fatalf("write in WITH: %+v", a)
	}
	if a := call("sql_query", "SELECT pg_sleep(30)"); a.status == 200 {
		t.Fatalf("statement past the call's time ran: %+v", a)
	}
}
