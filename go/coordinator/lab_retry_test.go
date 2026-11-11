package main

import "testing"

// Requirement: canonical command-bound absence, not a prose claim, authorizes suffix recovery.
// Risk: unrelated/remapped proof or an unresolved launch admits duplicate effects.
func TestSuffixProofRejectsBrokenBindings(t *testing.T) {
	execution := doc{"id": "script", "sessionId": "session", "hostId": "host", "pipelineStepId": "source:step:1", "actor": "program-worker"}
	command := doc{"id": "command", "executionId": "script", "sessionId": "session", "hostId": "host", "kind": "script", "status": "unknown"}
	event := doc{"id": "event:script:command-17", "seq": float64(17), "executionId": "script", "sessionId": "session", "type": "process.reconciled", "data": doc{"ownedProcesses": float64(1), "resolved": true, "survived": false, "launchWindowUnresolved": false}}
	if !suffixProof(execution, command, event, "source:step:1") {
		t.Fatal("canonical resolved proof rejected")
	}
	for _, tc := range []struct {
		name   string
		mutate func(doc, doc, doc)
	}{
		{"wrong host", func(e, c, v doc) { c["hostId"] = "other" }},
		{"wrong execution", func(e, c, v doc) { v["executionId"] = "other" }},
		{"wrong session", func(e, c, v doc) { v["sessionId"] = "other" }},
		{"wrong canonical id", func(e, c, v doc) { v["id"] = "event:script:other-17" }},
		{"wrong sequence", func(e, c, v doc) { v["seq"] = float64(18) }},
		{"no unknown receipt", func(e, c, v doc) { c["status"] = "started" }},
		{"native", func(e, c, v doc) { c["kind"] = "start" }},
		{"zero owned", func(e, c, v doc) { mapOf(v["data"])["ownedProcesses"] = float64(0) }},
		{"unresolved", func(e, c, v doc) { mapOf(v["data"])["resolved"] = false }},
		{"surviving", func(e, c, v doc) { mapOf(v["data"])["survived"] = true }},
		{"launch window", func(e, c, v doc) { mapOf(v["data"])["launchWindowUnresolved"] = true }},
		{"missing field", func(e, c, v doc) { delete(mapOf(v["data"]), "survived") }},
		{"wrong type", func(e, c, v doc) { mapOf(v["data"])["resolved"] = "true" }},
		{"transport remap", func(e, c, v doc) { mapOf(v["data"])["fleetNormalized"] = doc{"deltaOf": "other"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var e, c, v doc
			jsonRoundTrip(execution, &e)
			jsonRoundTrip(command, &c)
			jsonRoundTrip(event, &v)
			tc.mutate(e, c, v)
			if suffixProof(e, c, v, "source:step:1") {
				t.Fatal("invalid proof accepted")
			}
		})
	}
}

// Requirement: no additional launch-capable command, including late replay with a new ID, may be pending.
// Risk: canonical proof for one command masks another live launch.
func TestSuffixAdditionalLaunch(t *testing.T) {
	original := doc{"id": "original", "kind": "script", "status": "unknown"}
	if _, err := suffixCommand([]doc{original, doc{"kind": "stop"}}); err != nil {
		t.Fatal(err)
	}
	for _, other := range []doc{{"id": "late", "kind": "script", "status": "pending"}, {"id": "late", "kind": "start", "status": "accepted"}, {"id": "late", "kind": "input", "status": "started"}} {
		if _, err := suffixCommand([]doc{original, other}); err == nil {
			t.Fatal("second launch allowed", other)
		}
	}
}

// Requirement: the immutable output belongs to the exact original pipeline step in the same project.
// Risk: a corrupt internal pointer substitutes another readable project's bytes.
func TestPipelineOutputRejectsForeignIdentity(t *testing.T) {
	project, owner := "project", "run:step:0"
	artifact := Artifact{ProjectID: &project, ExecutionID: &owner, Name: "output.json", State: "stored", ContentHash: sha256hex([]byte("bytes"))}
	if !pipelineOutputIdentity(&artifact, project, owner) {
		t.Fatal("own immutable output rejected")
	}
	foreignProject := "other"
	foreignOwner := "other:step:0"
	for _, change := range []func(*Artifact){func(a *Artifact) { a.ProjectID = &foreignProject }, func(a *Artifact) { a.ExecutionID = &foreignOwner }, func(a *Artifact) { a.State = "pending" }, func(a *Artifact) { a.Name = "other.json" }, func(a *Artifact) { a.ContentHash = "invalid" }} {
		a := artifact
		change(&a)
		if pipelineOutputIdentity(&a, project, owner) {
			t.Fatal("foreign or unavailable output accepted")
		}
	}
}
