package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestAcpModelList(t *testing.T) {
	r := typed[acpResult](j(`{"sessionId":"s","models":{"availableModels":[{"modelId":"gemini-3.5-pro"},{"modelId":"gemini-3.5-flash"}]},` +
		`"configOptions":[{"id":"mode","category":"mode","type":"select","currentValue":"plan","options":[{"value":"plan"}]},` +
		`{"id":"model","category":"model","type":"select","currentValue":"gemini-3.5-pro","options":[{"value":"gemini-3.5-pro"},{"options":[{"value":"claude-sonnet-5"}]}]}]}`))
	got := modelNames(acpModelList(r))
	want := []string{"gemini-3.5-pro", "gemini-3.5-flash", "claude-sonnet-5"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	if modelNames(acpModelList(typed[acpResult](j(`{"sessionId":"s"}`)))) != nil {
		t.Fatal("a session/new answer without models must read not probed")
	}
}

// A fake ACP peer: answers initialize and session/new with a model list; the probe reads it without sending a prompt.
func TestProbeModelsAntigravity(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "native"), 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "agy")
	script := `#!/bin/sh
read a
echo '{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1}}'
read b
case "$b" in *session/new*) ;; *) exit 1;; esac
case "$*" in *"--gemini-dir ` + dir + `/native"*) ;; *) exit 1;; esac
echo '{"jsonrpc":"2.0","id":2,"result":{"sessionId":"s","models":{"availableModels":[{"modelId":"gemini-3.5-pro"}]}}}'
read c
`
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	x := registryEntry{Harness: "antigravity", Home: dir, NativeHome: filepath.Join(dir, "native"), Executable: exe, Readiness: "ready"}
	if err := runnable(x); err != nil {
		t.Fatal(err)
	}
	if got := probeModels(x); !slices.Equal(got, []string{"gemini-3.5-pro"}) {
		t.Fatalf("got %v", got)
	}
}

// Requirement (System › Access, Sign in): a login started on the host reports the CLI's sign-in link and device code
// while the CLI keeps waiting for the owner. Risk: the owner sees nothing to open, or the command waits for the sign-in.
func TestRunLoginReportsLinkAndCode(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(dir, "codex")
	script := "#!/bin/sh\nprintf '\\033[1mFollow these steps\\033[0m\\nOpen https://auth.openai.com/codex/device\\nEnter this code: ABCD-12345\\n'\nsleep 5\n"
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	x := registryEntry{Harness: "codex", Home: dir, NativeHome: dir, Executable: exe, Readiness: "ready"}
	start := time.Now()
	p := runLogin(x, []string{"login", "--device-auth"})
	if p.State != "waiting" || p.Url == nil || *p.Url != "https://auth.openai.com/codex/device" || p.Code == nil || *p.Code != "ABCD-12345" {
		t.Fatalf("%+v", p)
	}
	if time.Since(start) > 4*time.Second {
		t.Fatal("the command waited for the sign-in")
	}
	if p.Text == nil || strings.Contains(*p.Text, "\x1b") {
		t.Fatal("terminal escapes kept", p.Text)
	}
}
