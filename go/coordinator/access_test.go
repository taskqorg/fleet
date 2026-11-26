package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	"net"
	osexec "os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"fleet/contract"
	"fleet/coordinator/db"
)

// Requirement: the names Fleet derives for a project are names fleet-provision accepts, so a grant never fails on its
// own user names; risk: a long or odd project id makes a user the script refuses.
func TestAccessNamesMatchRecipes(t *testing.T) {
	script := string(fleetProvisionScript)
	pattern := func(name string) *regexp.Regexp {
		m := regexp.MustCompile(name + `='([^']*)'`).FindStringSubmatch(script)
		if m == nil {
			t.Fatalf("%s not found in fleet-provision.sh", name)
		}
		return regexp.MustCompile(m[1])
	}
	user, dbUser, label := pattern("P_USER"), pattern("P_DBUSER"), pattern("P_LABEL")
	for _, p := range []string{"dam", "cyprus-bz", "LBG2", "a-very-long-project-name-that-goes-on-and-on", "x_y.z"} {
		if u := accessOSUser(p); !user.MatchString(u) {
			t.Errorf("project %s: OS user %s refused by the script", p, u)
		}
		if u := accessDBUser(p, "grant:0123"); !dbUser.MatchString(u) || len(u) > 32 {
			t.Errorf("project %s: database user %s refused by the script or too long for MySQL", p, u)
		}
		if l := accessJumpLabel(p, "pds9"); !label.MatchString(l) {
			t.Errorf("project %s: jump label %s refused by the script", p, l)
		}
	}
	if accessDBUser("dam", "grant:a") == accessDBUser("dam", "grant:b") {
		t.Error("two grants share a database user")
	}
}

// Requirement: a scope the recipes cannot apply is refused when asked, not after the owner approved it.
func TestAccessScopeCheck(t *testing.T) {
	db, star := "revilio", "*"
	for _, c := range []struct {
		kind  string
		scope contract.AccessScope
		ok    bool
	}{
		{"mysql", contract.AccessScope{Database: &db, Tables: []string{"project", "upload"}}, true},
		{"mysql", contract.AccessScope{Database: &db, Tables: []string{star}}, true},
		{"mysql", contract.AccessScope{Database: &db, Tables: []string{star, "upload"}}, false},
		{"mysql", contract.AccessScope{Database: &db, Tables: []string{"up load"}}, false},
		{"mysql", contract.AccessScope{Tables: []string{"upload"}}, false},
		{"files", contract.AccessScope{Paths: []string{"/home/revilio/revilio.com/web/uploads"}}, true},
		{"files", contract.AccessScope{Paths: []string{"/home/../etc"}}, false},
		{"files", contract.AccessScope{Paths: []string{"relative"}}, false},
		{"files", contract.AccessScope{Database: &db, Paths: []string{"/srv"}}, false},
		{"ssh", contract.AccessScope{}, false},
		{"postgres", contract.AccessScope{Database: new("fleet"), Tables: []string{star}, Exclude: []string{"host", "run_token"}}, true},
		{"postgres", contract.AccessScope{Database: new("fleet"), Tables: []string{"task", "session"}}, true},
		{"postgres", contract.AccessScope{Database: new("fleet"), Tables: []string{"task"}, Exclude: []string{"host"}}, false},
		{"postgres", contract.AccessScope{Database: new("fleet"), Tables: []string{star}, Exclude: []string{"Host"}}, false},
		{"postgres", contract.AccessScope{Database: new("fleet"), Tables: []string{"a\"b"}}, false},
		{"postgres", contract.AccessScope{Database: new("Fleet"), Tables: []string{star}}, false},
		{"mysql", contract.AccessScope{Database: &db, Tables: []string{star}, Exclude: []string{"x"}}, false},
		{"postgres", contract.AccessScope{Database: new("fleet"), Tables: []string{star}, Exclude: []string{star}}, false},
		{"postgres", contract.AccessScope{Database: new("fleet"), Tables: []string{star, "task"}}, false},
	} {
		if err := accessScopeCheck(c.kind, &c.scope); (err == nil) != c.ok {
			t.Errorf("%s %+v: err %v, want ok %v", c.kind, c.scope, err, c.ok)
		}
	}
}

// Requirement: the card states the exact grant: the OS user, the database user and statement, the paths, and that
// write is refused for read.
func TestAccessRecipeText(t *testing.T) {
	db := "revilio"
	g := accessGrant{}
	g.ID, g.ProjectID, g.ServerID, g.Kind, g.Level = "grant:1", "dam", "pds9", "mysql", "read"
	g.Scope = contract.AccessScope{Database: &db, Tables: []string{"project", "upload"}}
	text := strings.Join(accessRecipe(g, "jump"), "\n")
	for _, want := range []string{"On jump: user fleet-jump", "OS user fleet-dam", "GRANT SELECT ON revilio.project, revilio.upload", accessDBUser("dam", "grant:1"), "writing is refused"} {
		if !strings.Contains(text, want) {
			t.Errorf("recipe lacks %q:\n%s", want, text)
		}
	}
	g.ProjectID, g.ServerID, g.Kind = "fleet", "pds18", "postgres"
	g.Scope = contract.AccessScope{Database: new("fleet"), Tables: []string{"*"}, Exclude: []string{"host", "run_token"}}
	text = strings.Join(accessRecipe(g, ""), "\n")
	for _, want := range []string{"127.0.0.1:5432", "GRANT SELECT ON every table of fleet.public that exists at provisioning except host, run_token", "read-only by default", "writing is refused"} {
		if !strings.Contains(text, want) {
			t.Errorf("postgres recipe lacks %q:\n%s", want, text)
		}
	}
	if got := accessPGTables(g.Scope); got != "*,-host,-run_token" {
		t.Errorf("pg tables argument %q", got)
	}
	// Go's regexp caps nested repeats: the count limit is fleet-provision's own self-test.
	if m := regexp.MustCompile(`P_PGTABLES='([^']*)'`).FindStringSubmatch(string(fleetProvisionScript)); m == nil || !regexp.MustCompile(strings.ReplaceAll(m[1], "{0,199}", "*")).MatchString(accessPGTables(g.Scope)) {
		t.Error("fleet-provision refuses the pg tables argument Fleet derives")
	}
}

// Requirement: a personal token reaches an access connection only when its minting person sees the connection's
// project; an agent or a person without the project does not.
func TestAccessTokenAllowed(t *testing.T) {
	c := apiConnection{Name: "fleet-postgres", Provider: "project-postgres", Access: connAccess{Projects: []string{"fleet"}}}
	name := "mac-claude"
	run := mintRun{Personal: &name}
	owner := &Person{Email: "o@x", Owner: true}
	other := &Person{Email: "p@x"}
	other.Grants = append(other.Grants, struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{"project", "dam"})
	if !accessTokenAllowed(&Request{person: owner}, c, run) {
		t.Error("the owner's personal token was refused")
	}
	if accessTokenAllowed(&Request{person: other}, c, run) {
		t.Error("a person without project fleet got its database")
	}
	if accessTokenAllowed(&Request{person: owner, agent: "execution:1"}, c, run) || accessTokenAllowed(&Request{person: owner}, c, mintRun{}) {
		t.Error("an agent or a run token passed the personal-token rule")
	}
	if accessTokenAllowed(&Request{person: owner}, apiConnection{Provider: "tavily", Access: c.Access}, run) {
		t.Error("the rule reached a non-access connection")
	}
}

// Requirement: fleet-provision's own argument checks pass (bash, the server's shell).
func TestFleetProvisionSelfTest(t *testing.T) {
	if _, err := osexec.LookPath("bash"); err != nil {
		t.Skip("no bash")
	}
	out, err := osexec.Command("bash", "fleet-provision.sh", "--self-test").CombinedOutput()
	if err != nil {
		t.Fatalf("self-test: %v\n%s", err, out)
	}
}

// fakeSSH: an SSH server on loopback for one user key: exec answers "ran <command>"; the sftp subsystem answers a
// version packet; a direct-tcpip channel to anything answers a MySQL-like greeting.
func fakeSSH(t *testing.T, user ssh.PublicKey) (addr string, hostKey ssh.PublicKey) {
	t.Helper()
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ssh.NewSignerFromKey(priv)
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, k ssh.PublicKey) (*ssh.Permissions, error) {
		if string(k.Marshal()) == string(user.Marshal()) {
			return nil, nil
		}
		return nil, io.EOF
	}}
	cfg.AddHostKey(signer)
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
					switch nc.ChannelType() {
					case "direct-tcpip":
						ch, _, _ := nc.Accept()
						if p := nc.ExtraData(); len(p) > 8 && strings.Contains(string(p), "127.0.0.1") && binary.BigEndian.Uint32(p[4+binary.BigEndian.Uint32(p):]) == 5432 {
							req := make([]byte, 8) // PostgreSQL: the SSL request, answered N
							_, _ = io.ReadFull(ch, req)
							_, _ = ch.Write([]byte("N"))
							ch.Close()
							continue
						}
						greeting := append([]byte{10}, []byte("8.0.40-fake\x00")...)
						head := make([]byte, 4)
						binary.LittleEndian.PutUint32(head, uint32(len(greeting)))
						head[3] = 0
						_, _ = ch.Write(append(head, greeting...))
						ch.Close()
					case "session":
						ch, in, _ := nc.Accept()
						go func() {
							for r := range in {
								switch r.Type {
								case "exec":
									_ = r.Reply(true, nil)
									cmd := string(r.Payload[4:])
									_, _ = ch.Write([]byte("ran " + cmd + "\n"))
									_ = ch.CloseWrite()
									_, _ = ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
									ch.Close()
								case "subsystem":
									_ = r.Reply(true, nil)
									buf := make([]byte, 9)
									_, _ = io.ReadFull(ch, buf)
									_, _ = ch.Write([]byte{0, 0, 0, 5, 2, 0, 0, 0, 3})
								default:
									_ = r.Reply(false, nil)
								}
							}
						}()
					default:
						_ = nc.Reject(ssh.UnknownChannelType, "no")
					}
				}
			}()
		}
	}()
	return l.Addr().String(), signer.PublicKey()
}

// Requirement: the provisioner learns a server's host key once and then refuses any other key; recipes run as the
// forced command reads them; the probes recognize an SFTP login and a forwarded MySQL greeting.
func TestAccessSSH(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	client, _ := ssh.NewSignerFromKey(priv)
	addr, hostKey := fakeSSH(t, client.PublicKey())
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var learned string
	if _, err := accessDial(ctx, []accessHop{{ID: "fake", Address: addr}}, "root", client, &learned); err != errHostKeyLearned {
		t.Fatalf("learn: %v", err)
	}
	if want := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(hostKey))); learned != want {
		t.Fatalf("learned %q, want %q", learned, want)
	}
	if _, err := accessDial(ctx, []accessHop{{ID: "fake", Address: addr}}, "root", client, nil); err == nil {
		t.Fatal("a server without a pinned key was trusted")
	}
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	otherSigner, _ := ssh.NewSignerFromKey(other)
	wrong := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(otherSigner.PublicKey())))
	if _, err := accessDial(ctx, []accessHop{{ID: "fake", Address: addr, HostKey: wrong}}, "root", client, nil); err == nil || !strings.Contains(err.Error(), "pinned") {
		t.Fatalf("a changed host key was accepted: %v", err)
	}
	c, err := accessDial(ctx, []accessHop{{ID: "fake", Address: addr, HostKey: learned}}, "root", client, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	out, err := accessRun(c, []string{"probe-files", "fleet-dam", "ro:/srv/x"}, "")
	if err != nil || out != "ran probe-files fleet-dam ro:/srv/x" {
		t.Fatalf("run: %q %v", out, err)
	}
	if err := accessProbeSFTP(c); err != nil {
		t.Fatalf("sftp probe: %v", err)
	}
	if v, err := accessProbeForward(c, "127.0.0.1:3306"); err != nil || v != "8.0.40-fake" {
		t.Fatalf("forward probe: %q %v", v, err)
	}
	if err := accessProbePostgres(c, "127.0.0.1:5432"); err != nil {
		t.Fatalf("postgres probe: %v", err)
	}
	if err := accessProbePostgres(c, "127.0.0.1:3306"); err == nil {
		t.Fatal("a MySQL greeting passed the postgres probe")
	}
}

// Requirement: every recipe the provisioner names is one fleet-provision dispatches.
func TestAccessRecipesKnown(t *testing.T) {
	script := string(fleetProvisionScript)
	for _, r := range []string{"version", "os-user", "revoke-os-user", "mysql-user", "revoke-mysql", "probe-mysql", "probe-files", "jump-allow", "jump-deny", "mysql-databases", "pg-databases", "pg-user", "probe-pg", "revoke-pg"} {
		if !strings.Contains(script, "\n\t"+r+") ") {
			t.Errorf("fleet-provision does not dispatch %s", r)
		}
	}
	if accessEnrollCommand("KEY") == "" || fleetProvisionVersion() == "0" {
		t.Error("enroll command or version missing")
	}
}

// Risk: a Tavily key out of plan credits (432) reads as a bad key to replace, or stays failing after a top-up.
func TestAccessCreditLimitAsksForTopUp(t *testing.T) {
	c := apiConnection{Name: "tavily", Provider: "tavily"}
	keys := []connKey{{Fingerprint: "k1", State: "active", Problem: &keyProblem{Status: 432, Message: "plan limit", At: 2000}}}
	it := accessConnection(c, keys, db.AccessLastCallsRow{OkStartedAt: 1000}, nil, nil)
	if it.State != contract.AccessStateFailing || it.Next == nil || it.Next.Action != contract.AccessActionBilling || !strings.Contains(it.StateText, "credits") {
		t.Fatalf("credit limit: %+v %+v", it.State, it.Next)
	}
	it = accessConnection(c, keys, db.AccessLastCallsRow{OkStartedAt: 3000}, nil, nil)
	if it.State == contract.AccessStateFailing {
		t.Fatalf("a later success still fails: %s", it.StateText)
	}
}

// Requirement: a project access connection reads its grant, never "Needs a key": ready works, a failed last call or a
// failed grant fails, a revoked grant takes no calls, and no grant at all says so.
func TestAccessGrantItem(t *testing.T) {
	c := apiConnection{Name: "fleet-postgres", Provider: "project-postgres", Access: connAccess{Projects: []string{"fleet"}}}
	g := accessGrant{}
	g.ServerID, g.Level, g.State = "pds18", "read", "ready"
	it := accessGrantItem(accessConnection(c, nil, db.AccessLastCallsRow{}, nil, nil), g, true)
	if it.State != contract.AccessStateWorks || it.Kind != "grant" || it.Keys != nil || it.Next != nil || !strings.Contains(it.StateText, "pds18") {
		t.Fatalf("ready: %+v", it)
	}
	failed := accessConnection(c, nil, db.AccessLastCallsRow{LastID: "call:1", LastState: "failed", LastStartedAt: 2}, nil, nil)
	if it := accessGrantItem(failed, g, true); it.State != contract.AccessStateFailing {
		t.Fatalf("failed call: %+v", it)
	}
	g.State = "revoked"
	if it := accessGrantItem(accessConnection(c, nil, db.AccessLastCallsRow{}, nil, nil), g, true); it.State != contract.AccessStatePaused {
		t.Fatalf("revoked: %+v", it)
	}
	if it := accessGrantItem(accessConnection(c, nil, db.AccessLastCallsRow{}, nil, nil), accessGrant{}, false); it.State != contract.AccessStateFailing || it.Next != nil {
		t.Fatalf("no grant: %+v", it)
	}
}
