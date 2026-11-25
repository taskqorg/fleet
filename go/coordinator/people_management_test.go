package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleet/contract"
)

func TestHumanGrantAndPromotionAuthority(t *testing.T) {
	const email = "architect@trial.test"
	const machineAlias = "invented-machine-client-id"
	raw := []byte(`[
		{"email":"owner@trial.test","roles":["architect"],"owner":true},
		{"email":"machine-test","roles":["operator"],"AccessCommonName":"invented-machine-client-id","grants":[{"kind":"*"}]}
	]`)
	base := &contract.PeopleUpdateRequest{Email: email, Roles: []string{"architect"}, Projects: []string{"*"}, PersonalLab: ptr(true)}
	next, err := updateHuman(nil, raw, email, base)
	if err != nil {
		t.Fatal(err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(next, &rows); err != nil || len(rows) != 3 {
		t.Fatalf("unexpected roster: %s, %v", next, err)
	}
	p, err := personRow(rows[2])
	if err != nil {
		t.Fatal(err)
	}
	if fullAccess(p) || !hasPersonalLab(p) || !mayAccessProjectID(p, "future-project") || scopeOf(p).ProjectIDs != "*" {
		t.Fatalf("wrong architect authority: %+v", p)
	}
	if err := labPromotionAuthority(&Request{person: p}); err != nil {
		t.Fatal(err)
	}
	machine, err := personRow(rows[1])
	if err != nil || machine.AccessCommonName != machineAlias {
		t.Fatalf("the machine mapping changed: %v", err)
	}
	if err := labPromotionAuthority(&Request{person: machine}); err == nil {
		t.Fatal("full-access grant substituted for the architect role")
	}
	if strings.Contains(string(jsonBytes(redactPeople(next))), machineAlias) {
		t.Fatal("redaction missed the case-insensitive machine mapping")
	}
	base.Roles = []string{"operator"}
	if _, err := updateHuman(nil, raw, email, base); err == nil {
		t.Fatal("accepted the wildcard project grant for an operator")
	}
}

// Requirement FLT-29: the coordinator uses a private host token file. Risk: a partial or
// insecure configuration appears to be enabled or prints the secret in its diagnostic.
func TestPeopleCloudflareConfig(t *testing.T) {
	const token = "invented-token"
	t.Setenv("FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID", "")
	t.Setenv("FLEET_CLOUDFLARE_ACCESS_GROUP_ID", "")
	t.Setenv("FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE", "")
	if client, err := cloudflarePeopleFromEnv(); client != nil || err != nil {
		t.Fatal("unset configuration should allow manual management")
	}
	t.Setenv("FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID", "00000000000000000000000000000001")
	if client, err := cloudflarePeopleFromEnv(); client != nil || err == nil {
		t.Fatal("partial configuration should return an error")
	}
	t.Setenv("FLEET_CLOUDFLARE_ACCESS_GROUP_ID", "00000000-0000-0000-0000-000000000001")
	file := filepath.Join(t.TempDir(), "access-token")
	t.Setenv("FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE", file)
	if err := os.WriteFile(file, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	client, err := cloudflarePeopleFromEnv()
	if err != nil || client == nil || client.token != token ||
		client.url != "https://api.cloudflare.com/client/v4/accounts/00000000000000000000000000000001/access/groups/00000000-0000-0000-0000-000000000001" {
		t.Fatal("private token file or account/group endpoint was not loaded")
	}
	if err := os.Chmod(file, 0o644); err != nil {
		t.Fatal(err)
	}
	if client, err := cloudflarePeopleFromEnv(); client != nil || err == nil || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), file) {
		t.Fatal("insecure token file was accepted or a private value was exposed")
	}
}
