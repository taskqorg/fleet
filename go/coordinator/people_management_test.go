package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"fleet/contract"
)

func TestPeopleManagementAndPersonalLab(t *testing.T) {
	e := openTestCoordinator(t, false)
	const owner = "owner@trial.test"
	const member = "member@trial.test"
	const architect = "architect@trial.test" // invented fixture for Simon's granted role and scope
	const machine = "machine-test"
	const machineMapping = "invented-machine-client-id"
	if err := os.WriteFile(e.app.peopleFile, []byte(`[
		{"email":"owner@trial.test","roles":["operator","architect"],"owner":true},
		{"email":"member@trial.test","roles":["operator"],"grants":[{"kind":"project","id":"desiign"}]},
		{"email":"machine-test","roles":["operator"],"accessCommonName":"invented-machine-client-id","grants":[{"kind":"*","id":"*"}]}
	]`), 0o600); err != nil {
		t.Fatal(err)
	}

	call := func(actor, op string, body any, want int) map[string]any {
		t.Helper()
		status, out := e.callJSON(t, http.MethodPost, "/api/ops/"+op, body, map[string]string{
			"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": actor,
		})
		if status != want {
			t.Fatalf("%s %s: got %d, want %d: %v", actor, op, status, want, out)
		}
		return out
	}
	roster := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, r := range call(owner, "people_list", map[string]bool{"manage": true}, 200)["roster"].([]any) {
			row := r.(map[string]any)
			out[row["email"].(string)] = row
		}
		return out
	}
	before := roster()
	if before[machine]["machine"] != true || before[owner]["owner"] != true {
		t.Fatalf("wrong identity kinds: %v", before)
	}
	add := map[string]any{"email": architect, "baseVersion": "", "name": "Simon", "roles": []string{"architect"},
		"projects": []string{"*"}, "personalLab": true}
	call(member, "people_update", add, 403)
	call(machineMapping, "people_update", add, 403)
	call(machineMapping, "people_list", map[string]bool{"manage": true}, 403)
	call(owner, "people_update", add, 200)
	call(owner, "people_update", add, 409)

	after := roster()
	if after[architect]["owner"] != false || after[architect]["personalLab"] != true ||
		after[member]["version"] != before[member]["version"] || after[machine]["version"] != before[machine]["version"] {
		t.Fatalf("wrong roster after addition: %v", after)
	}
	me := call(architect, "me", struct{}{}, 200)
	if me["owner"] != false || me["personalLab"] != true {
		t.Fatalf("wrong architect identity: %v", me)
	}
	all := call(owner, "project_list", struct{}{}, 200)["projects"].([]any)
	got := call(architect, "project_list", struct{}{}, 200)["projects"].([]any)
	if len(got) != len(all) || len(all) < 2 {
		t.Fatalf("architect project scope: %d; owner: %d", len(got), len(all))
	}
	call(architect, "people_list", map[string]bool{"manage": true}, 403)
	changes := call(owner, "setting_changes", map[string]string{"target": "people"}, 200)
	if raw, _ := json.Marshal(changes); strings.Contains(string(raw), machineMapping) || !strings.Contains(string(raw), owner) {
		t.Fatal("people audit exposed the machine mapping or omitted the owner")
	}

	update := map[string]any{"email": member, "name": "Member", "baseVersion": after[member]["version"],
		"roles": []string{"operator"}, "projects": []string{"desiign"}, "personalLab": true}
	call(owner, "people_update", update, 200)
	draft := map[string]any{"id": "test-private-lab-draft", "project": "desiign", "pipeline": "coding-task", "variant": "Reviewed variant"}
	call(member, "lab_personal_save", draft, 200)
	list := func(actor string) []any { return call(actor, "lab_personal_list", struct{}{}, 200)["drafts"].([]any) }
	if len(list(member)) != 1 || len(list(architect)) != 0 || len(list(owner)) != 0 {
		t.Fatal("draft visible outside its author")
	}
	call(member, "lab_personal_promote", map[string]any{"id": draft["id"], "pipelineRevision": 1, "confirm": true}, 403)
	call(machineMapping, "lab_personal_promote", map[string]any{"id": draft["id"], "pipelineRevision": 1, "confirm": true}, 403)
	call(member, "lab_promote", map[string]string{"experimentId": "unknown", "variant": "unknown"}, 403)
	draft["submit"] = true
	submitted := call(member, "lab_personal_save", draft, 200)
	if len(list(architect)) != 1 {
		t.Fatal("architect did not see submitted draft")
	}
	promote := map[string]any{"id": draft["id"], "pipelineRevision": submitted["pipelineRevision"], "confirm": false}
	call(architect, "lab_personal_promote", promote, 400)
	promote["confirm"] = true
	call(member, "lab_personal_promote", promote, 403)
	promoted := call(architect, "lab_personal_promote", promote, 200)
	replayed := call(architect, "lab_personal_promote", promote, 200)
	if promoted["method"] == "" || replayed["method"] != promoted["method"] {
		t.Fatalf("wrong promotion: %v then %v", promoted, replayed)
	}
	if method := list(member)[0].(map[string]any)["promotedMethod"]; method != promoted["method"] {
		t.Fatalf("author could not read back the result: %v", method)
	}
	catalogue := call(architect, "catalogue", map[string]string{"projectId": "desiign"}, 200)
	catalogueRaw, _ := json.Marshal(catalogue)
	if strings.Contains(string(catalogueRaw), draft["id"].(string)) {
		t.Fatal("shared catalogue exposes a private draft ID")
	}

	call(owner, "people_update", map[string]any{"email": member, "baseVersion": roster()[member]["version"], "revoke": true}, 200)
	call(member, "me", struct{}{}, 403)
	call(member, "lab_personal_list", struct{}{}, 403)
	if len(list(architect)) != 0 {
		t.Fatal("revoked person's draft still appears in architect review")
	}
}

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
