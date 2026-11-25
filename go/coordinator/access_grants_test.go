package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: access_grants (docs/designs/fleet-project-access.md) shows a person only the grants of their projects,
// also without projectId (the access_grant row policy); answers the enroll command for a server to enroll to a
// full-access person only; and every grant or server write is a change (kinds access-grant, access-server) that reaches
// the people who see it (a grant's project; a server, everyone), so the Access screens reread at once. Risk: a grant leaks to another project's people, the ~18 KB root command reaches a
// person who may not enroll servers, or an approval answered in Needs you never reaches an open Access facet.
func TestAccessGrantsScopeEnrollAndChanges(t *testing.T) {
	e := openTestCoordinator(t, false)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := e.ownerStore.pool.Exec(e.ctx, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	t.Setenv("FLEET_SECRET_DIR", t.TempDir())
	if _, err := accessKey("provisioner"); err != nil { // access_server_add makes it; the read only loads it
		t.Fatal(err)
	}
	exec(`INSERT INTO access_server (id, address, host_key, enrolled_at, payload) VALUES ('jump', '10.0.0.1', 'ssh-ed25519 AAAA', now(), $1)`,
		`{"checked": {"at": 1, "ok": true, "version": "`+fleetProvisionVersion()+`"}}`)
	exec(`INSERT INTO access_server (id, address, via) VALUES ('pds9', '10.0.0.9', 'jump')`)
	for _, g := range [][2]string{{"ag-1", "desiign"}, {"ag-2", "hello-world"}} {
		exec(`INSERT INTO access_grant (id, project_id, server_id, kind, level, request_id, asked_by, payload)
		 VALUES ($1, $2, 'pds9', 'mysql', 'read', $1, 'agent:worker', '{"scope": {"database": "d", "tables": ["t"]}, "reason": "r"}')`, g[0], g[1])
	}
	as := func(email string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": email}
	}
	read := func(email string) contract.AccessGrantsReply {
		t.Helper()
		var r contract.AccessGrantsReply
		e.opInto(t, "access_grants", doc{}, as(email), &r)
		return r
	}
	enroll := func(r contract.AccessGrantsReply, id string) string {
		for _, s := range r.Servers {
			if s.Id == id {
				return deref(s.Enroll)
			}
		}
		t.Fatalf("no server %s in %+v", id, r.Servers)
		return ""
	}

	member := read("member@trial.test")
	if len(member.Grants) != 1 || member.Grants[0].Id != "ag-1" {
		t.Fatalf("member sees grants %+v, want only ag-1 (project desiign)", member.Grants)
	}
	if got := enroll(member, "pds9"); got != "" {
		t.Fatalf("member gets the enroll command (%d bytes)", len(got))
	}
	for _, email := range []string{testOwnerEmail, "agents@trial.test"} {
		r := read(email)
		if len(r.Grants) != 2 {
			t.Fatalf("%s sees %d grants, want 2", email, len(r.Grants))
		}
		if got := enroll(r, "pds9"); !strings.HasPrefix(got, "sudo sh -c ") || !strings.Contains(got, "fleet-provision install ") {
			t.Fatalf("%s: enroll for pds9 = %.80q", email, got)
		}
		if got := enroll(r, "jump"); got != "" {
			t.Fatalf("%s: an enrolled server carries the enroll command", email)
		}
	}

	// The member's change feed: the head first, then what the next writes bring.
	var head contract.ChangesSinceReply
	e.opInto(t, "changes_since", doc{}, as("member@trial.test"), &head)
	exec(`UPDATE access_grant SET state = 'provisioning' WHERE id = 'ag-1'`)
	exec(`UPDATE access_grant SET state = 'provisioning' WHERE id = 'ag-2'`)
	exec(`UPDATE access_server SET enrolled_at = now() WHERE id = 'pds9'`)
	var feed contract.ChangesSinceReply
	e.opInto(t, "changes_since", doc{"cursor": head.Cursor}, as("member@trial.test"), &feed)
	var seen []string
	for _, c := range feed.Changes {
		seen = append(seen, string(c.Kind)+":"+c.Id)
	}
	if got := strings.Join(seen, ","); got != "access-grant:ag-1,access-server:pds9" {
		t.Fatalf("member's feed %s, want its own project's grant and the server (a server belongs to no project)", got)
	}
	var kinds []string
	rows, err := e.ownerStore.pool.Query(e.ctx, `SELECT kind || ':' || id || ':' || coalesce(project_id, '') FROM change WHERE kind IN ('access-grant', 'access-server') ORDER BY seq`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			t.Fatal(err)
		}
		kinds = append(kinds, k)
	}
	rows.Close()
	want := "access-server:jump:,access-server:pds9:,access-grant:ag-1:desiign,access-grant:ag-2:hello-world,access-grant:ag-1:desiign,access-grant:ag-2:hello-world,access-server:pds9:"
	if got := strings.Join(kinds, ","); got != want {
		t.Fatalf("changes %s, want %s", got, want)
	}
	if got := contract.Touches("access-grant", "ag-1"); len(got) != 1 || got[0] != "access_grants" {
		t.Fatalf("an access-grant change touches %v, want access_grants", got)
	}
}
