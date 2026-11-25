package contract

import (
	"os"
	"testing"
)

// TestProfileHarnessCandidate: a qualification run's profile (a shipped profile with the Lab candidate override) parses.
func TestProfileHarnessCandidate(t *testing.T) {
	raw, err := os.ReadFile("../../config/agents/pins-qualify-codex.json")
	if err != nil {
		t.Fatal(err)
	}
	withCandidate := append(raw[:len(raw)-2], []byte(`,"harnessCandidate":{"name":"codex","asset":{"version":"0.172.0","url":"https://github.com/openai/codex/releases/download/rust-v0.172.0/c.tar.gz","sha256":"aa","entry":"bin/codex"}}}`)...)
	p, err := ParseProfile(withCandidate)
	if err != nil {
		t.Fatalf("%v\n%s", err, withCandidate)
	}
	if p.HarnessCandidate == nil || p.HarnessCandidate.Asset.Version != "0.172.0" {
		t.Fatalf("%+v", p.HarnessCandidate)
	}
}
