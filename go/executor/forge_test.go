package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Requirement: publication stays in the configured GitHub repository; risk: an input URL redirects credentials.
func TestForgeRepositoryBoundary(t *testing.T) {
	for _, remote := range []string{"https://evil.test/o/r", "https://github.com@evil.test/o/r", "https://github.com/o/r?redirect=x", "https://github.com/o/r/extra", "file:///tmp/repo", "https://github.com/../repo", "https://user@github.com/o/r"} {
		if forgeRepo(remote) != "" {
			t.Fatalf("accepted %s", remote)
		}
	}
	if forgeRepo("https://github.com/invented/project.git") != "invented/project" {
		t.Fatal("valid project refused")
	}
	_, err := forge(M{"method": "tools/call", "params": M{"name": "publish_draft", "arguments": M{"title": "diagnostic", "body": "fixture"}}}, M{"repo": "invented/project", "branch": "fleet/fixture", "prefix": "fleet", "repositories": "other/project"})
	if err == nil {
		t.Fatal("ungranted repository accepted")
	}
}

// Requirement: missing managed tools prevent launch; risk: service PATH is mistaken for usable managed execution.
func TestCodingToolMissing(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if codingTool("node", t.TempDir()) == nil {
		t.Fatal("missing node accepted")
	}
	if codingTool("arbitrary-shell", t.TempDir()) == nil {
		t.Fatal("unknown tool accepted")
	}
}

// Requirement: a granted branch can publish through the fixed operation; risk: credentials leak or request scope widens.
func TestForgePublishDraft(t *testing.T) {
	dir := t.TempDir()
	for name, script := range map[string]string{
		"git": "#!/bin/sh\ncase \"$*\" in *symbolic-ref*) echo fleet/fixture;; *push*|*check-ref-format*) exit 0;; *) exit 1;; esac\n",
		"gh":  "#!/bin/sh\ncase \"$*\" in *'auth token'*) echo invented-token;; *'--method POST'*) echo '{\"html_url\":\"https://github.com/invented/project/pull/1\",\"number\":1}';; *api*) echo '[]';; *) exit 1;; esac\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("invented fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	ctx := M{"repo": "invented/project", "repositories": "invented/project", "branch": "fleet/fixture", "prefix": "fleet", "base": "main", "store": dir, "workdir": dir, "home": dir, "credentialHome": dir}
	result, err := forge(M{"method": "tools/call", "params": M{"name": "publish_draft", "arguments": M{"title": "fixture", "body": "fixture"}}}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enc(result), "/pull/1") || strings.Contains(enc(result), "invented-token") {
		t.Fatal("invalid or leaking result")
	}
	if _, err := forge(M{"method": "tools/call", "params": M{"name": "delivery", "arguments": M{"action": "auto_merge"}}}, ctx); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatal("auto-merge widened project authority")
	}
	ctx["branch"] = "fleet/other"
	if _, err = forge(M{"method": "tools/call", "params": M{"name": "publish_draft", "arguments": M{"title": "fixture", "body": "fixture"}}}, ctx); err == nil {
		t.Fatal("changed branch accepted")
	}
}

// Requirement: readiness must release a hung external command; risk: a probe blocks dispatch forever.
func TestCodingProbeHung(t *testing.T) {
	c := exec.Command("/bin/sh", "-c", "sleep 30 & wait")
	started := time.Now()
	if _, err := boundedOutput(c, 20*time.Millisecond); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline, got %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("hung probe did not release")
	}
}
