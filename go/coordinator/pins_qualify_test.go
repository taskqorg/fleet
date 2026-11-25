package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"fleet/harnessrelease"
)

// TestPinsHostVariants: one trial per changed CLI, host with a ready binding and configured model; unchanged CLIs,
// unready bindings and unknown platforms run nothing; WSL (win32) runs the linux pin.
func TestPinsHostVariants(t *testing.T) {
	asset := func(d string) harnessrelease.Asset { return harnessrelease.Asset{Version: "1.0.0", SHA256: d} }
	current := pinsCandidate{"linux-amd64": {"codex": asset("a"), "claude": asset("c")}, "darwin-arm64": {"codex": asset("a")}}
	candidate := pinsCandidate{"linux-amd64": {"codex": asset("b"), "claude": asset("c")}, "darwin-arm64": {"codex": asset("b")}}
	binding := func(host, platform, readiness string) json.RawMessage {
		return jsonBytes(map[string]string{"hostId": host, "platform": platform, "readiness": readiness})
	}
	accounts := []accountRec{
		{ID: "x", Harness: "codex", accountPayload: accountPayload{Bindings: []json.RawMessage{binding("pds18", "linux", "ready"), binding("mac", "darwin", "ready"), binding("off", "linux", "needs-login")}}},
		{ID: "y", Harness: "codex", accountPayload: accountPayload{Bindings: []json.RawMessage{binding("pds18", "linux", "ready"), binding("win", "win32", "ready")}}},
		{ID: "z", Harness: "claude", accountPayload: accountPayload{Bindings: []json.RawMessage{binding("pds18", "linux", "ready")}}},
	}
	vs := pinsHostVariants(accounts, current, candidate, map[string][]string{"codex": {"m1", "m2"}, "claude": {"s"}})
	labels := []string{}
	for _, v := range vs {
		labels = append(labels, v.label())
	}
	want := []string{"mac codex m1", "mac codex m2", "pds18 codex m1", "pds18 codex m2", "win codex m1", "win codex m2"}
	if string(jsonBytes(labels)) != string(jsonBytes(want)) || vs[2].Account != "x" || vs[2].Asset.SHA256 != "b" {
		t.Fatalf("%v %+v", labels, vs)
	}
}

// TestPinsTrialOutcome: done when every variant has a terminal trial; passed only when each succeeded and its check passed.
func TestPinsTrialOutcome(t *testing.T) {
	v := func(s string) *string { return &s }
	runs := []*labRun{{ID: "1", Variant: v("a"), State: "succeeded"}, {ID: "2", Variant: v("b"), State: "succeeded"}}
	pass := []labScore{{Form: "pass_fail", Value: json.RawMessage("true"), TrialIDs: []string{"1"}}, {Form: "pass_fail", Value: json.RawMessage("true"), TrialIDs: []string{"2"}}}
	if st := pinsTrialOutcome([]string{"a", "b"}, runs, pass); !st.Done || !st.Passed {
		t.Fatalf("%+v", st)
	}
	if st := pinsTrialOutcome([]string{"a", "b", "c"}, runs, pass); st.Done || st.Passed {
		t.Fatalf("missing trial: %+v", st)
	}
	if st := pinsTrialOutcome([]string{"a", "b"}, runs, pass[:1]); !st.Done || st.Passed || len(st.Failures) != 1 {
		t.Fatalf("failed check: %+v", st)
	}
	runs[0].State = "failed"
	if st := pinsTrialOutcome([]string{"a", "b"}, runs, pass); !st.Done || st.Passed {
		t.Fatalf("failed run: %+v", st)
	}
}

// TestPinsLiveVariants: trials go to hosts that polled lately, whatever their update state; an older executor makes
// the qualification wait while its update is on its way and is skipped by name when its update is held or refused.
func TestPinsLiveVariants(t *testing.T) {
	vs := []pinsVariant{{Host: "a"}, {Host: "b"}, {Host: "gone"}}
	hosts := map[string]pinsHost{"a": {version: "1.261125.227", live: true}, "b": {version: "1.261125.230", live: true}}
	if got, skipped, ready := pinsLiveVariants(vs, hosts, "1.261125.227"); !ready || len(got) != 2 || len(skipped) != 1 {
		t.Fatalf("%v %v %v", got, skipped, ready)
	}
	hosts["b"] = pinsHost{version: "1.261125.155", live: true}
	if _, _, ready := pinsLiveVariants(vs, hosts, "1.261125.227"); ready {
		t.Fatal("an updating older executor would run its own CLI")
	}
	hosts["b"] = pinsHost{version: "1.261125.155", live: true, stuck: true}
	if got, skipped, ready := pinsLiveVariants(vs, hosts, "1.261125.227"); !ready || len(got) != 1 || len(skipped) != 2 {
		t.Fatalf("held host: %v %v %v", got, skipped, ready)
	}
}

// TestPinsCandidateOf: the pins read raw (keyed) and as the keyless contents object with base64 both parse; an empty
// file is an error, never an empty candidate that would run no trial.
func TestPinsCandidateOf(t *testing.T) {
	raw := `{"enabled":true,"targets":{"linux-amd64":{"codex":{"version":"0.172.0","url":"https://github.com/openai/codex/releases/download/rust-v0.172.0/c.tar.gz","sha256":"` + strings.Repeat("a", 64) + `","entry":"bin/codex"}}}}`
	for name, b := range map[string]string{
		"raw":      raw,
		"contents": `{"encoding":"base64","content":"` + base64.StdEncoding.EncodeToString([]byte(raw))[:40] + `\n` + base64.StdEncoding.EncodeToString([]byte(raw))[40:] + `"}`,
	} {
		c, err := pinsCandidateOf([]byte(b), "x")
		if err != nil || c["linux-amd64"]["codex"].Version != "0.172.0" {
			t.Fatalf("%s: %v %v", name, c, err)
		}
	}
	if _, err := pinsCandidateOf([]byte(`{"targets":{}}`), "x"); err == nil {
		t.Fatal("empty pins parsed")
	}
}

// TestPinsNoTrialNoPass: an experiment without trials is never done and never passed.
func TestPinsNoTrialNoPass(t *testing.T) {
	if st := pinsTrialOutcome(nil, nil, nil); st.Done || st.Passed {
		t.Fatalf("%+v", st)
	}
}

// TestPinsReview: a CLI proposal may change only config/harness-pins.json.
func TestPinsReview(t *testing.T) {
	if f := pinsReview([]pinsFile{{"config/harness-pins.json", "modified"}}, "cli"); len(f) != 0 {
		t.Fatal(f)
	}
	for name, files := range map[string][]pinsFile{
		"extra file": {{"config/harness-pins.json", "modified"}, {"go/x.go", "added"}},
		"other kind": {{"config/tool-pins.json", "modified"}},
		"removed":    {{"config/harness-pins.json", "removed"}},
		"empty":      nil,
	} {
		if f := pinsReview(files, "cli"); len(f) == 0 {
			t.Errorf("%s: no finding", name)
		}
	}
}
