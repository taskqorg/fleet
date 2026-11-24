package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"fleet/contract"
)

func TestCloudflareAccessHTTPReadback(t *testing.T) {
	const token = "invented-cloudflare-readback-token"
	const groupID = "11111111-2222-3333-4444-555555555555"
	var reads, writes atomic.Int32
	var unavailable atomic.Bool
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Error("incorrect Cloudflare authorization header")
		}
		if r.Method == http.MethodPut {
			writes.Add(1)
			var group accessGroupRules
			if err := json.NewDecoder(r.Body).Decode(&group); err != nil || !group.includes("human@trial.test") {
				t.Errorf("admission was absent from group update: %v", err)
			}
		} else {
			reads.Add(1)
		}
		if unavailable.Load() {
			http.Error(w, token, http.StatusUnauthorized)
			return
		}
		// A successful response without an applied update must remain unconfirmed.
		_, _ = w.Write([]byte(`{"success":true,"result":{"id":"` + groupID + `","name":"fleet-people","include":[]}}`))
	}))
	defer edge.Close()
	access := &peopleAccess{endpoint: edge.URL, groupID: groupID, token: token, client: edge.Client()}
	if err := access.sync(context.Background(), "human@trial.test", true); err == nil || !strings.Contains(err.Error(), "differs") {
		t.Fatalf("unconfirmed admission returned %v", err)
	}
	if reads.Load() != 2 || writes.Load() != 1 {
		t.Fatalf("expected read, update, readback, got %d reads and %d writes", reads.Load(), writes.Load())
	}
	unavailable.Store(true)
	if err := access.sync(context.Background(), "human@trial.test", true); err == nil || err.Error() != "Cloudflare Access returned HTTP 401" {
		t.Fatalf("expected only the sanitized HTTP status")
	}
	if writes.Load() != 1 {
		t.Fatal("failed observation attempted a write")
	}
}

func TestPeopleCloudflareAccess(t *testing.T) {
	e := openTestCoordinator(t, false)
	const owner = "owner@trial.test"
	const email = "new-human@trial.test"
	const token = "invented-cloudflare-test-token"
	const groupID = "11111111-2222-3333-4444-555555555555"
	var mu sync.Mutex
	group := accessGroupRules{
		Name:      "fleet-people",
		Include:   []json.RawMessage{json.RawMessage(`{"email":{"email":"owner@trial.test"}}`), json.RawMessage(`{"service_token":{"token_id":"invented-service-id"}}`)},
		Exclude:   json.RawMessage(`[{"email":{"email":"excluded@trial.test"}}]`),
		Require:   json.RawMessage(`[{"login_method":{"id":"invented-login-id"}}]`),
		IsDefault: json.RawMessage(`false`),
	}
	putCount := 0
	failWrite, failRead := false, false
	edge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Path != "/access/groups/"+groupID {
			t.Error("incorrect Cloudflare credential or target")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if failRead {
				http.Error(w, token, http.StatusServiceUnavailable)
				return
			}
		case http.MethodPut:
			if failWrite {
				http.Error(w, token, http.StatusBadGateway)
				return
			}
			var next accessGroupRules
			if err := json.NewDecoder(r.Body).Decode(&next); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if string(next.Exclude) != string(group.Exclude) || string(next.Require) != string(group.Require) ||
				string(next.IsDefault) != string(group.IsDefault) || len(next.Include) < 2 ||
				string(next.Include[1]) != string(group.Include[1]) || !next.includes(owner) {
				t.Error("sync changed unrelated Cloudflare rules")
			}
			group = next
			putCount++
		default:
			t.Errorf("unexpected Cloudflare method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "result": struct {
			accessGroupRules
			ID string `json:"id"`
		}{group, groupID}})
	}))
	defer edge.Close()
	e.app.peopleAccess = &peopleAccess{endpoint: edge.URL + "/access/groups/" + groupID, groupID: groupID, token: token, client: edge.Client()}

	call := func(person, op string, body any, want int) map[string]any {
		t.Helper()
		status, out := e.callJSON(t, http.MethodPost, "/api/ops/"+op, body, map[string]string{
			"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": person,
		})
		if status != want {
			t.Fatalf("%s: got %d, want %d: %v", op, status, want, out)
		}
		if strings.Contains(string(jsonBytes(out)), token) {
			t.Fatal("Cloudflare response exposed the API token")
		}
		return out
	}
	empty := struct{}{}
	sync := func(version string, want int) map[string]any {
		return call(owner, "people_access_sync", contract.PeopleAccessSyncRequest{Email: email, BaseVersion: version}, want)
	}
	observe := func(state string, admitted bool) {
		t.Helper()
		out := call(owner, "people_access_status", empty, 200)
		if out["state"] != state || strings.Contains(string(jsonBytes(out["admitted"])), email) != admitted {
			t.Fatalf("unexpected Cloudflare admission: %v", out)
		}
	}
	update := contract.PeopleUpdateRequest{Email: " New-Human@trial.test ", Name: ptr("New human"), Roles: []string{"operator"}}
	updateState := func(want string) {
		t.Helper()
		out := call(owner, "people_update", update, 200)
		if result, ok := out["cloudflareSync"].(map[string]any); !ok || result["state"] != want {
			t.Fatalf("expected Cloudflare sync state %s: %v", want, out)
		}
	}
	updateState("synced")
	observe("active", true)
	call(email, "people_access_status", empty, 403)
	call(email, "people_access_sync", contract.PeopleAccessSyncRequest{Email: email}, 403)
	update.BaseVersion = peopleRowVersion(e.app.peopleSnapshot().person(email))
	if result := sync(update.BaseVersion, 200); result["state"] != "synced" {
		t.Fatalf("idempotent retry: %v", result)
	}

	// An edit repairs an email removed outside Fleet.
	mu.Lock()
	if putCount != 1 {
		t.Errorf("idempotent retry made %d group writes", putCount)
	}
	group.Include = group.Include[:2]
	mu.Unlock()
	update.Name = ptr("Edited human")
	updateState("synced")
	observe("active", true)
	update.BaseVersion = peopleRowVersion(e.app.peopleSnapshot().person(email))
	sync("", 409)

	mu.Lock()
	group.Include = append(group.Include, json.RawMessage(`{"email":{"email":"NEW-HUMAN@trial.test"}}`))
	failWrite = true
	mu.Unlock()
	update.Revoke = ptr(true)
	updateState("error")
	call(email, "me", empty, 403)
	observe("active", true)
	mu.Lock()
	failWrite = false
	failRead = true
	mu.Unlock()
	observe("error", false)
	mu.Lock()
	failRead = false
	mu.Unlock()
	if result := sync("", 200); result["state"] != "synced" {
		t.Fatalf("revocation retry: %v", result)
	}
	observe("active", false)
	changes := string(jsonBytes(call(owner, "setting_changes", map[string]string{"target": "people"}, 200)))
	for _, detail := range []string{`"field":"cloudflare-access"`, `"actor":"owner@trial.test"`, `"state":"requested"`, `"state":"error"`, `"state":"synced"`, `"admit":false`} {
		if !strings.Contains(changes, detail) {
			t.Errorf("Cloudflare audit missing %s", detail)
		}
	}
}

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
	if result := call(owner, "people_update", add, 200)["cloudflareSync"].(map[string]any); result["state"] != "manual" {
		t.Fatalf("unconfigured sync: %v", result)
	}
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
