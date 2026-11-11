package contract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R1, R3; risk: role-specific exclusion or arbitrary host commands enter a named capability grant.
func TestSerenaGrantAcrossHarnesses(t *testing.T) {
	for _, fields := range []string{`"effort":"low"`, `"harness":"claude","instructionRole":"system","tools":["Read"]`, `"harness":"antigravity","instructionRole":"user"`} {
		raw := `{"id":"pm-or-worker","model":"m","instructions":"x","mcpServers":[{"id":"serena","data":{"language":"go"}}],` + fields + `}`
		if _, err := ParseProfile([]byte(raw)); err != nil {
			t.Fatal(raw, err)
		}
	}
	for _, data := range []string{`{}`, `{"language":"go","command":"arbitrary"}`, `{"language":"python"}`} {
		if _, err := ParseProfile([]byte(`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[{"id":"serena","data":` + data + `}]}`)); err == nil {
			t.Fatal("accepted", data)
		}
	}
}

// Requirement: one profile validator for coordinator and executor (review C8); every shipped profile passes it and
// profiles outside the supported shapes are refused. Risk: a profile the coordinator lists and dispatches (gitNetwork on
// a Claude profile) fails on the host, or an unmapped grant silently widens what the agent may do.
func TestParseProfile(t *testing.T) {
	files, _ := filepath.Glob("../../config/agents/*.json")
	if len(files) == 0 {
		t.Fatal("no profiles found")
	}
	for _, f := range files {
		if strings.HasSuffix(f, "-data.json") { // design-rules data, not a profile
			continue
		}
		raw, _ := os.ReadFile(f)
		if _, err := ParseProfile(raw); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
	for _, good := range []string{
		`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[],"gitNetwork":true}`,
		`{"id":"p","harness":"claude","model":"m","effort":null,"instructionRole":"system","instructions":"x","mcpServers":[],"tools":["Read","Bash"],"gitNetwork":true}`,
	} {
		if _, err := ParseProfile([]byte(good)); err != nil {
			t.Error(good, err)
		}
	}
	for _, bad := range []string{
		`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[],"tools":[]}`,
		`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[],"unknown":1}`,
		`{"id":"p","harness":"claude","model":"m","instructionRole":"system","instructions":"x","mcpServers":[],"tools":["WebFetch"]}`,
		`{"id":"p","harness":"antigravity","model":"m","instructionRole":"user","instructions":"x","mcpServers":[],"settings":{"mode":"yolo"}}`,
		`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[{"id":"other","data":{}}]}`,
		`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[{"id":"evil","data":{}}],"mcpservers":[]}`, // case variant
		`{"id":"p","model":"m","effort":"low","mcpServers":[]}`,                                                           // no instructions
		`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[],"skills":["-rf"]}`,
		`{"id":"p","model":"m","effort":"low","instructions":"x","mcpServers":[],"delegationProfiles":null}`,
	} {
		if _, err := ParseProfile([]byte(bad)); err == nil {
			t.Error("accepted", bad)
		}
	}
}
