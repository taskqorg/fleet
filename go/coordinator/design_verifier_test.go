package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fleet/contract"
)

// T-FLT-181: registration and a live App observation are separate. Other callers cannot forge a receipt.
func TestDesignVerifierStatusAuthority(t *testing.T) {
	e := openTestCoordinator(t, false)
	const alias = "invented-verifier-client-id"
	raw, err := os.ReadFile(e.app.peopleFile)
	if err != nil {
		t.Fatal(err)
	}
	var roster []json.RawMessage
	if err := json.Unmarshal(raw, &roster); err != nil {
		t.Fatal(err)
	}
	roster = append(roster, json.RawMessage(`{"email":"fleet-design-verifier","roles":["viewer"],"accessCommonName":"`+alias+`","grants":[{"kind":"project","id":"fleet"}]}`))
	if err := os.WriteFile(e.app.peopleFile, jsonBytes(roster), 0600); err != nil {
		t.Fatal(err)
	}
	var status contract.DesignVerifierStatusReply
	e.opInto(t, "design_verifier_status", doc{}, e.ownerHeaders(), &status)
	if !status.FleetIdentityConfigured || status.AppId != 5962560 || status.Provider != "unknown" || status.LastReady != nil {
		t.Fatalf("registration read as live readiness: %+v", status)
	}
	observation := contract.DesignVerifierObservation{Kind: "provider", State: "ready", Revision: strings.Repeat("a", 40),
		KeyFingerprint:  ptr("SHA256:" + base64.StdEncoding.EncodeToString(make([]byte, 32))),
		AuthenticatedAt: ptr(nowMs()), ExpiresAt: ptr(nowMs() + 60_000)}
	const path = "/api/ops/design_verifier_observe"
	if code, _ := e.callJSON(t, "POST", path, observation, e.ownerHeaders()); code != 403 {
		t.Fatalf("human impersonated the verifier: %d", code)
	}
	headers := e.ownerHeaders()
	headers["X-Fleet-Person"] = designVerifierIdentity
	if code, _ := e.callJSON(t, "POST", path, observation, headers); code != 403 {
		t.Fatalf("bare identity bypassed the Access alias: %d", code)
	}
	headers["X-Fleet-Person"] = alias
	if code, body := e.callJSON(t, "POST", path, observation, headers); code != 200 {
		t.Fatalf("protected observation returned %d: %v", code, body)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/project_list", doc{}, headers); code != 403 {
		t.Fatalf("verifier got an unrelated read: %d", code)
	}
	e.opInto(t, "design_verifier_status", doc{}, e.ownerHeaders(), &status)
	if status.Provider != "ready" || status.LastReady == nil || status.LatestPR != nil || status.LatestMergeGroup != nil {
		t.Fatalf("unexpected readiness or invented check: %+v", status)
	}
	if code, body := e.callJSON(t, "POST", path, doc{"kind": "provider", "state": "unknown", "revision": observation.Revision}, headers); code != 200 {
		t.Fatalf("unknown provider observation returned %d: %v", code, body)
	}
	e.opInto(t, "design_verifier_status", doc{}, e.ownerHeaders(), &status)
	if status.Provider != "unknown" || status.LastReady == nil || status.LastReady.Observation.Revision != observation.Revision {
		t.Fatalf("lost retained readiness after provider failure: %+v", status)
	}
}
