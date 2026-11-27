package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fleet/contract"
)

// Production Access is available only to the declared owner-held hosts. A missing
// credential stays visible through host observation before any execution starts.
func TestProjectBrowserAccessAndObservation(t *testing.T) {
	e := openTestCoordinator(t, false)
	var created map[string]any
	e.opInto(t, "project_create", doc{"requestId": "browser-access-test", "id": "fleet", "content": doc{
		"settings": doc{"id": "fleet", "name": "Fleet", "contextFiles": []string{"rules.md"}, "environments": []doc{{"hostId": "browser-host", "checkoutPath": "/invented/fleet"}, {"hostId": "member-host", "checkoutPath": "/invented/fleet"}},
			"execution": doc{"tools": []string{"browser"}}},
		"contextFiles": []doc{{"path": "rules.md", "text": "Fixture project rules."}},
	}}, e.ownerHeaders(), &created)
	auth := func(id string) map[string]string {
		return map[string]string{"Authorization": "Bearer " + e.registerHost(t, id)}
	}
	granted, ungranted := auth("browser-host"), auth("other-host")
	var member contract.HostWriteReply
	e.opInto(t, "hosts_register", doc{"id": "member-host", "holder": "agents@trial.test"}, e.ownerHeaders(), &member)
	notOwner := map[string]string{"Authorization": "Bearer " + deref(member.Secret)}
	root := t.TempDir()
	t.Setenv("FLEET_SECRET_DIR", root)
	binding := doc{"projectId": "fleet"}
	access := contract.RouteBrowserAccess.Path
	if code, _ := e.callJSON(t, "POST", access, binding, e.ownerHeaders()); code != http.StatusUnauthorized {
		t.Fatalf("person route response = %d", code)
	}
	if code, response := e.callJSON(t, "POST", access, binding, granted); code != http.StatusServiceUnavailable {
		t.Fatalf("missing credential response = %d %v", code, response)
	}
	dir := filepath.Join(root, "project-browser")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "site-agents.json"), []byte(`{"clientId":"invented-id","clientSecret":"invented-secret"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, headers := range []map[string]string{ungranted, notOwner} {
		if code, response := e.callJSON(t, "POST", access, binding, headers); code != http.StatusOK || response["clientId"] != "" || response["clientSecret"] != "" {
			t.Fatalf("ungranted host got a credential or status %d", code)
		}
	}
	if code, response := e.callJSON(t, "POST", access, binding, granted); code != http.StatusOK || response["clientId"] != "invented-id" || response["clientSecret"] != "invented-secret" {
		t.Fatalf("granted host did not receive the fixture credential, status %d", code)
	}

	e.opInto(t, "project_create", doc{"requestId": "generic-browser-test", "id": "dam", "content": doc{
		"settings": doc{"id": "dam", "name": "DAM", "contextFiles": []string{"rules.md"},
			"environments": []doc{{"hostId": "browser-host", "checkoutPath": "/invented/dam"}, {"hostId": "member-host", "checkoutPath": "/invented/dam"}},
			"execution":    doc{"tools": []string{"browser"}}},
		"contextFiles": []doc{{"path": "rules.md", "text": "Fixture project rules."}},
	}}, e.ownerHeaders(), &created)
	dam := doc{"projectId": "dam"}
	if code, response := e.callJSON(t, "POST", access, dam, granted); code != http.StatusOK || response["production"] != nil {
		t.Fatalf("unconfigured project got authority, status %d", code)
	}
	generic := filepath.Join(dir, "dam.binding.json")
	credential := filepath.Join(dir, "dam.env")
	if err := os.WriteFile(generic, []byte(`{"origin":"https://revilio.com","accessFile":"dam.env","headers":{"X-DAM-Verification-Token":"DAM_VERIFICATION_TOKEN"},"readinessProbe":{"path":"/ready","method":"GET","status":200}}`), 0600); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.callJSON(t, "POST", access, dam, granted); code != http.StatusServiceUnavailable {
		t.Fatalf("configured project with missing credentials got status %d", code)
	}
	if err := os.WriteFile(credential, []byte("DAM_VERIFICATION_TOKEN=invented-dam-authority\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, headers := range []map[string]string{ungranted, notOwner} {
		if code, response := e.callJSON(t, "POST", access, dam, headers); code != http.StatusOK || response["production"] != nil || response["clientSecret"] != "" {
			t.Fatalf("ungranted host got production authority or status %d", code)
		}
	}
	code, response := e.callJSON(t, "POST", access, dam, granted)
	genericReply, ok := response["production"].(map[string]any)
	if code != http.StatusOK || !ok || genericReply["origin"] != "https://revilio.com" || response["clientSecret"] != "" {
		t.Fatalf("granted host did not get generic authority, status %d", code)
	}
	headers, ok := genericReply["headers"].([]any)
	if !ok || len(headers) != 1 {
		t.Fatal("managed production reply did not include its configured header")
	}
	header, ok := headers[0].(map[string]any)
	if !ok || header["name"] != "X-DAM-Verification-Token" || header["value"] != "invented-dam-authority" {
		t.Fatal("managed header did not resolve the configured env value")
	}
	rotated := filepath.Join(dir, "dam-rotated.env")
	if err := os.Rename(credential, rotated); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(rotated, credential); err != nil {
		t.Fatal(err)
	}
	if code, _ := e.callJSON(t, "POST", access, dam, granted); code != http.StatusServiceUnavailable {
		t.Fatalf("managed grant accepted symlink credential, status %d", code)
	}

	const pending = "Waiting for a production browser readiness check"
	const failed = "Production browser failed the site-agents identity check"
	poll := func(id string, headers map[string]string, ready *bool, problems []doc, reason string) {
		t.Helper()
		body := doc{"hostId": id, "platform": "linux", "capabilities": []string{}, "stores": []any{}, "problems": problems, "gitConnections": []string{}}
		if ready != nil {
			body["browserProductionReady"] = *ready
		}
		if code, _ := e.callJSON(t, "POST", contract.RoutePoll.Path, body, headers); code != http.StatusOK {
			t.Fatalf("poll response = %d", code)
		}
		var info contract.HostDiagnoseReply
		e.opInto(t, "host_diagnose", doc{"host": id, "wait": 0}, e.ownerHeaders(), &info)
		if got := strings.Join(info.Problems, " "); got != reason {
			t.Fatalf("production readiness problem = %q, want %q", got, reason)
		}
	}
	p := doc{"kind": "browser-production-unready", "route": "", "status": 0, "location": nil, "detail": failed}
	poll("browser-host", granted, nil, nil, pending)
	poll("other-host", ungranted, nil, nil, "")
	poll("other-host", ungranted, ptr(false), []doc{p}, "")
	poll("browser-host", granted, ptr(false), []doc{p}, failed)
	poll("browser-host", granted, ptr(true), nil, "")
}
