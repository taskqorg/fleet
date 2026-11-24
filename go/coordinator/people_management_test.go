package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// Requirement FLT-29: Humans syncs direct Cloudflare email rules. Risk: a failed edge change
// outlives the Fleet roster write and a retry overwrites other rules or re-admits a revoked human.
func TestPeopleCloudflareAdmissionAndRevocation(t *testing.T) {
	e := openTestCoordinator(t, false)
	const (
		owner   = "owner@trial.test"
		member  = "member@trial.test"
		groupID = "00000000-0000-0000-0000-000000000001"
		token   = "invented-cloudflare-secret"
		path    = "/client/v4/accounts/00000000000000000000000000000001/access/groups/" + groupID
	)
	otherRules := []json.RawMessage{
		json.RawMessage(`{"email":{"email":"owner@trial.test"}}`),
		json.RawMessage(`{"service_token":{"token_id":"invented-service-token-id"}}`),
	}
	excludeRules := []json.RawMessage{json.RawMessage(`{"email":{"email":"blocked@trial.test"}}`)}
	requireRules := []json.RawMessage{json.RawMessage(`{"geo":{"country_code":"US"}}`)}
	group := accessGroup{ID: groupID, Name: "fleet-people", Include: otherRules, Exclude: excludeRules, Require: requireRules, IsDefault: ptr(false)}
	var mu sync.Mutex
	var calls, puts int
	var refusePut, ignorePut bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		if r.URL.Path != path || r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("Cloudflare endpoint or credential did not match the fixture")
			http.Error(w, "wrong endpoint or credential", http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodPut:
			puts++
			if refusePut {
				http.Error(w, token, http.StatusForbidden)
				return
			}
			var next accessGroup
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				t.Error(err)
				http.Error(w, "bad group update", http.StatusBadRequest)
				return
			}
			if r.Header.Get("Content-Type") != "application/json" || next.ID != "" || next.Name != group.Name ||
				next.IsDefault == nil || *next.IsDefault || string(jsonBytes(next.Exclude)) != string(jsonBytes(excludeRules)) ||
				string(jsonBytes(next.Require)) != string(jsonBytes(requireRules)) ||
				len(next.Include) < len(otherRules) || string(jsonBytes(next.Include[:len(otherRules)])) != string(jsonBytes(otherRules)) {
				t.Errorf("group update did not preserve writable settings: %+v", next)
				http.Error(w, "rules changed", http.StatusBadRequest)
				return
			}
			if !ignorePut {
				group.Include = next.Include
			}
		case http.MethodGet:
		default:
			t.Errorf("unexpected Cloudflare method %s", r.Method)
			http.Error(w, "bad method", http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": group})
	}))
	defer server.Close()
	e.app.peopleCloudflare = &peopleCloudflare{client: server.Client(), url: server.URL + path, groupID: groupID, token: token}
	if err := os.WriteFile(e.app.peopleFile, []byte(`[
		{"email":"owner@trial.test","roles":["architect"],"owner":true},
		{"email":"machine@trial.test","roles":["operator"],"accessCommonName":"machine-test","grants":[{"kind":"*","id":"*"}]}
	]`), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(as, name string, req any, want int) map[string]any {
		t.Helper()
		status, reply := e.callJSON(t, http.MethodPost, "/api/ops/"+name, req, map[string]string{
			"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": as,
		})
		if status != want || strings.Contains(string(jsonBytes(reply)), token) {
			t.Fatalf("%s: status %d, want %d, or credential leaked", name, status, want)
		}
		return reply
	}
	assertStatus := func(row map[string]any, want string) {
		t.Helper()
		if state, ok := row["cloudflare"].(map[string]any); !ok || state["status"] != want {
			t.Fatalf("Cloudflare state = %v, want %s", row["cloudflare"], want)
		}
	}
	roster := func() ([]any, []any) {
		reply := call(owner, "people_list", map[string]bool{"manage": true}, 200)
		if reply["cloudflareMode"] != "active" {
			t.Fatalf("wrong mode: %v", reply)
		}
		removals, _ := reply["cloudflareRemovals"].([]any)
		return reply["roster"].([]any), removals
	}
	find := func(rows []any, email string) map[string]any {
		for _, raw := range rows {
			if row := raw.(map[string]any); row["email"] == email {
				return row
			}
		}
		return nil
	}
	call(owner, "people_list", struct{}{}, 200)
	call("machine-test", "people_list", map[string]bool{"manage": true}, 403)
	call("machine-test", "people_access_sync", map[string]string{"email": member}, 403)
	call(owner, "people_access_sync", map[string]string{"email": owner}, 403)
	call(owner, "people_access_sync", map[string]string{"email": "machine@trial.test"}, 403)
	call(owner, "people_access_sync", map[string]string{"email": "absent@trial.test"}, 404)
	mu.Lock()
	if calls != 0 {
		t.Error("public roster or denied operations contacted Cloudflare")
	}
	ignorePut = true
	mu.Unlock()

	update := map[string]any{"email": " Member@trial.test ", "baseVersion": "", "roles": []string{"operator"}}
	// A PUT success without matching readback cannot be reported as synchronized.
	assertStatus(call(owner, "people_update", update, 200), "pending")
	call(member, "me", struct{}{}, 200)
	rows, _ := roster()
	assertStatus(find(rows, member), "missing")
	assertStatus(find(rows, owner), "present")
	if find(rows, "machine@trial.test")["cloudflare"] != nil {
		t.Fatal("machine identity reported as a human email rule")
	}
	mu.Lock()
	ignorePut = false
	mu.Unlock()
	retry := map[string]string{"email": member}
	assertStatus(call(owner, "people_access_sync", retry, 200), "present")
	rows, _ = roster()
	assertStatus(find(rows, member), "present")
	update["baseVersion"] = find(rows, member)["version"]
	update["name"] = "New name"
	assertStatus(call(owner, "people_update", update, 200), "present")
	mu.Lock()
	if puts != 2 {
		t.Errorf("editing an already admitted human made a redundant write: %d PUTs", puts)
	}
	group.Include = append(group.Include, json.RawMessage(`{"email":{"email":"MEMBER@TRIAL.TEST"}}`))
	refusePut = true
	mu.Unlock()

	rows, _ = roster()
	revoke := map[string]any{"email": member, "baseVersion": find(rows, member)["version"], "revoke": true}
	assertStatus(call(owner, "people_update", revoke, 200), "pending")
	call(member, "me", struct{}{}, 403)
	rows, removals := roster()
	if find(rows, member) != nil {
		t.Fatal("revoked person still appears in the Fleet roster")
	}
	removal := find(removals, member)
	assertStatus(removal, "removal_required")
	if removal["cloudflare"].(map[string]any)["message"] != "Cloudflare Access returned HTTP 403" {
		t.Fatalf("failed Cloudflare removal lost its error: %v", removal)
	}
	mu.Lock()
	group.Name = "unrelated-group"
	refusePut = false
	mu.Unlock()
	rows, removals = roster()
	assertStatus(find(rows, owner), "unknown")
	assertStatus(find(removals, member), "pending")
	assertStatus(call(owner, "people_access_sync", retry, 200), "pending")
	mu.Lock()
	if puts != 3 {
		t.Errorf("sync attempted to update a group with the wrong name: %d PUTs", puts)
	}
	group.Name = "fleet-people"
	mu.Unlock()
	assertStatus(call(owner, "people_access_sync", retry, 200), "removed")
	_, removals = roster()
	if len(removals) != 0 {
		t.Fatalf("confirmed removal still shown: %v", removals)
	}
	mu.Lock()
	if len(group.Include) != len(otherRules) {
		t.Error("duplicate case-insensitive email rules were left in Cloudflare")
	}
	mu.Unlock()
	changes := call(owner, "setting_changes", map[string]string{"target": "people"}, 200)["changes"].([]any)
	latest := changes[0].(map[string]any)
	if latest["actor"] != owner || latest["field"] != cloudflareAuditPrefix+member ||
		latest["value"].(map[string]any)["status"] != "removed" {
		t.Fatalf("Cloudflare removal missing from the People audit: %v", latest)
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
