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
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// Requirement: an existing grant is adopted only after exact provider and effective Fleet viewer validation;
// refusal and stale or lost refresh replies survive the work pass and a request replay never posts again.
func TestServiceGrantReconcilesRefusalAndStaleReadback(t *testing.T) {
	const (
		account  = "11111111111111111111111111111111"
		appID    = "10000000-0000-4000-8000-000000000001"
		policyID = "10000000-0000-4000-8000-000000000002"
		tokenID  = "10000000-0000-4000-8000-000000000003"
		clientID = "22222222222222222222222222222222.access"
	)
	baseline := time.Now().Add(15 * 24 * time.Hour).UTC().Truncate(time.Second)
	var mu sync.Mutex
	var expiry = baseline
	var posts int
	var mode string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer invented-api-fixture" {
			t.Error("missing configured authority")
			w.WriteHeader(403)
			return
		}
		path, ok := strings.CutPrefix(r.URL.Path, "/accounts/"+account+"/access")
		if !ok {
			t.Errorf("unexpected account path %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		token := doc{"id": tokenID, "name": designVerifierIdentity, "client_id": clientID,
			"duration": "8760h", "expires_at": expiry, "enabled": true, "client_secret_version": 1}
		app := doc{"id": appID, "domain": serviceGrantDomain, "aud": contract.FleetAccessAudience, "type": "self_hosted"}
		var result any
		switch r.Method + " " + path {
		case "GET /service_tokens/" + tokenID:
			result = token
		case "GET /service_tokens":
			result = []doc{token}
		case "GET /apps/" + appID:
			result = app
		case "GET /apps":
			result = []doc{app}
		case "GET /apps/" + appID + "/policies":
			include := []doc{{"service_token": doc{"token_id": tokenID}}}
			if mode == "extra-selector" {
				include = append(include, doc{"everyone": doc{}})
			}
			result = []doc{{"id": policyID, "account_id": account, "name": designVerifierIdentity, "decision": "non_identity", "include": include}}
		case "POST /service_tokens/" + tokenID + "/refresh":
			posts++
			if mode == "refused" {
				w.WriteHeader(403)
				return
			}
			if mode == "success" {
				expiry = expiry.Add(time.Second)
				token["expires_at"] = expiry
				result = token
				break
			}
			// Lost acknowledgement. The read initially sees the previous value.
			w.WriteHeader(502)
			return
		default:
			t.Errorf("unexpected provider call %s %s", r.Method, path)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(doc{"success": true, "result": result})
	}))
	t.Cleanup(provider.Close)

	t.Setenv("FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID", account)
	t.Setenv("FLEET_CLOUDFLARE_API_BASE", provider.URL)
	apiTokenPath := filepath.Join(t.TempDir(), "api-token")
	if err := os.WriteFile(apiTokenPath, []byte("invented-api-fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_CLOUDFLARE_ACCESS_API_TOKEN_FILE", apiTokenPath)
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	var created doc
	e.opInto(t, "project_create", doc{"id": "fleet", "requestId": "service-grant-test", "content": doc{
		"settings":     doc{"id": "fleet", "name": "Fleet", "contextFiles": []string{"rules.md"}},
		"contextFiles": []doc{{"path": "rules.md", "text": "Invented fixture project."}},
	}}, owner, &created)
	run := func() {
		t.Helper()
		if err := kitOf(e.app).passQueue(e.ctx, kitQueueNamed(serviceGrantQueue)); err != nil {
			t.Fatal(err)
		}
	}
	call := func(op string, args doc) contract.ServiceGrantStatusReply {
		t.Helper()
		args["projectId"], args["name"] = "fleet", designVerifierIdentity
		var r contract.ServiceGrantStatusReply
		e.opInto(t, op, args, owner, &r)
		return r
	}
	maintain := func(action, id string) contract.ServiceGrantStatusReply {
		return call("service_grant_maintain", doc{"action": action, "requestId": id})
	}
	read := func() contract.ServiceGrantStatusReply { return call("service_grant_status", doc{}) }
	expectPosts := func(want int) {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		if posts != want {
			t.Fatalf("provider saw %d refreshes, want %d", posts, want)
		}
	}

	enroll := doc{"applicationId": appID, "policyId": policyID, "serviceTokenId": tokenID, "enrollmentReference": "receipt-1"}
	call("service_grant_enroll", enroll)
	run()
	if r := read(); r.Enrollment != "reconciling" || r.ProviderReadiness != "verified" || r.BindingReadiness != "unverified" {
		t.Fatalf("provider ready while machine awaits roster: %+v", r)
	}
	raw, err := os.ReadFile(e.app.peopleFile)
	if err != nil {
		t.Fatal(err)
	}
	var roster []json.RawMessage
	if err := json.Unmarshal(raw, &roster); err != nil {
		t.Fatal(err)
	}
	roster = append(roster, jsonBytes(doc{"email": designVerifierIdentity, "roles": []string{"viewer"}, "accessCommonName": clientID,
		"grants": []doc{{"kind": "project", "id": "fleet"}}}))
	if err := writeFileAtomic(e.app.peopleFile, jsonBytes(roster)); err != nil {
		t.Fatal(err)
	}
	maintain("check", "")
	run()
	if r := read(); r.Enrollment != "adopted" || r.BindingReadiness != "verified" || deref(r.ClientId) != clientID || ms(r.LastConfirmedExpiry) != baseline.UnixMilli() {
		t.Fatalf("adopted grant: %+v", r)
	}
	observe := func() {
		t.Helper()
		headers := e.ownerHeaders()
		headers["X-Fleet-Person"] = clientID
		if code, body := e.callJSON(t, "POST", "/api/ops/design_verifier_observe",
			doc{"kind": "provider", "state": "unknown", "revision": strings.Repeat("a", 40)}, headers); code != 200 {
			t.Fatalf("machine heartbeat: %d %v", code, body)
		}
	}
	if r := read(); r.ServiceReadiness != "unknown" {
		t.Fatalf("configuration was treated as a credential probe: %+v", r)
	}
	observe()
	if r := read(); r.ServiceReadiness != "verified" || r.LastServiceAuthenticatedAt == nil {
		t.Fatalf("authenticated verifier heartbeat is missing: %+v", r)
	}
	expectPosts(0)

	mu.Lock()
	mode = "refused"
	mu.Unlock()
	maintain("renew", "refusal")
	run()
	if r := maintain("renew", "refusal"); r.Renewal == nil || r.Renewal.Result != "refused" ||
		!strings.Contains(deref(r.Renewal.Detail), cfPermServiceTokens) {
		t.Fatalf("refusal replay: %+v", r.Renewal)
	}
	run()
	expectPosts(1)

	mu.Lock()
	mode = "lost-ack"
	mu.Unlock()
	maintain("check", "")
	run()
	if r := read(); r.Renewal == nil || r.Renewal.Result != "awaiting_readback" || !strings.HasPrefix(r.Renewal.RequestId, "automatic:") ||
		ms(r.LastConfirmedExpiry) != baseline.UnixMilli() || r.ProviderReadiness == "verified" {
		t.Fatalf("stale provider lookup was taken as current: %+v, %+v", r, r.Renewal)
	}
	maintain("check", "")
	run()
	expectPosts(2)
	mu.Lock()
	expiry = time.Now().Add(serviceGrantDuration - time.Second).UTC().Truncate(time.Second)
	renewedExpiry := expiry.UnixMilli()
	mu.Unlock()
	maintain("check", "")
	run()
	if r := read(); r.Renewal == nil || r.Renewal.Result != "verified_by_readback" ||
		ms(r.Renewal.VerifiedExpiry) != renewedExpiry || ms(r.LastConfirmedExpiry) != renewedExpiry ||
		deref(r.ServiceTokenId) != tokenID || deref(r.ClientId) != clientID {
		t.Fatalf("recovered renewal: %+v, %+v", r, r.Renewal)
	}
	expectPosts(2)
	mu.Lock()
	mode = "success"
	mu.Unlock()
	maintain("renew", "acknowledged")
	run()
	if r := maintain("renew", "acknowledged"); r.Renewal == nil || r.Renewal.Result != "verified" ||
		ms(r.Renewal.AcknowledgedExpiry) != ms(r.Renewal.VerifiedExpiry) || ms(r.Renewal.VerifiedExpiry) <= renewedExpiry {
		t.Fatalf("manual refresh did not independently verify the acknowledgement: %+v", r.Renewal)
	}
	observe()
	if r := read(); r.ServiceReadiness != "verified" || r.LastServiceAuthenticatedAt == nil {
		t.Fatalf("service was not verified after refresh: %+v", r)
	}

	mu.Lock()
	mode = "extra-selector"
	mu.Unlock()
	maintain("renew", "policy-drift")
	run()
	if r := read(); r.ProviderReadiness != "unverified" || !strings.Contains(deref(r.ProviderProblem), "exact Service Auth") {
		t.Fatalf("changed policy was not rejected: %+v", r)
	}
	expectPosts(3)
}

func TestServiceGrantReadinessExpiresWithObservation(t *testing.T) {
	now := time.Now()
	row := db.ServiceGrant{TokenID: ptr("invented"), CheckedAt: ptr(now.Add(-3 * time.Hour)), ProviderCheck: ptr(""),
		BindingCheck: ptr(""), ExpiresAt: ptr(now.Add(time.Hour))}
	status := serviceGrantViewOf(row, now)
	if status.ProviderReadiness != "unknown" || status.BindingReadiness != "unknown" || status.LastConfirmedExpiry == nil {
		t.Fatalf("stale observation: %+v", status)
	}
}

// Requirement: project requests can record a specific future grant, but only the Fleet owner can approve it.
func TestServiceGrantApprovalScope(t *testing.T) {
	e := openTestCoordinator(t, false)
	owner := e.ownerHeaders()
	as := func(email string) map[string]string {
		h := e.ownerHeaders()
		h["X-Fleet-Person"] = email
		return h
	}
	member := as("member@trial.test")
	ask := doc{"projectId": "desiign", "name": "fleet-machine-test", "requestId": "scope-1", "reason": "Read verification evidence"}
	var grant contract.ServiceGrantStatusReply
	e.opInto(t, "service_grant_ask", ask, member, &grant)
	if grant.Enrollment != "awaiting_approval" || grant.QuestionId == nil ||
		grant.ProjectId != "desiign" || grant.DurationHours != 8760 || grant.Role != "viewer" {
		t.Fatalf("the future grant scope was changed: %+v", grant)
	}
	id := *grant.QuestionId
	e.opInto(t, "service_grant_ask", ask, member, &grant)
	if deref(grant.QuestionId) != id {
		t.Fatalf("replayed request did not retain the same question: %+v", grant)
	}
	decision := doc{"questionId": id, "answer": approvalYes}
	if code, _ := e.callJSON(t, "POST", "/api/ops/question_answer", decision, as("agents@trial.test")); code != 403 {
		t.Fatalf("full-access non-owner could approve an enrollment: %d", code)
	}
	var answered doc
	e.opInto(t, "question_answer", decision, owner, &answered)
	lookup := doc{"projectId": "desiign", "name": "fleet-machine-test"}
	e.opInto(t, "service_grant_status", lookup, member, &grant)
	if grant.Enrollment != "awaiting_operator_enrollment" || grant.ServiceTokenId != nil {
		t.Fatalf("approval changed provider state: %+v", grant)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/service_grant_status", lookup, as("outsider@trial.test")); code != 404 {
		t.Fatalf("ungranted project could read a grant: %d", code)
	}
}
