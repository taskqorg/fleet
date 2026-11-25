package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fleet/contract"
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
	_, err := forgeCall("publish_draft", M{"title": "diagnostic", "body": "fixture"}, M{"repo": "invented/project", "branch": "fleet/fixture", "prefix": "fleet", "repositories": "other/project"})
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

// requirement: push credentials and leased updates stay bound to the granted transport.
// risk: a URL named as a local remote redirects the push.
func TestForgeRejectsURLNamedRemote(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init"},
		{"config", "remote.https://github.com/invented/project.git.url", "https://example.test/other.git"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := checkCleanTransportConfig(forgeContext{Workdir: dir, Home: dir}); err == nil {
		t.Fatal("accepted local remote override for a granted URL")
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

// Requirement: T-FLT-34 — coding-worker profile ships the code-review skill and injects it into Codex developer_instructions.
// Risk: managed coding-worker runs lack /code-review or try running sandboxed nested `codex review` and stall before posting status.
func TestCodingWorkerIncludesCodeReviewSkill(t *testing.T) {
	profileBytes, err := os.ReadFile(filepath.Join("..", "..", "config", "agents", "coding-worker.json"))
	if err != nil {
		t.Fatal(err)
	}
	skillBytes, err := os.ReadFile(filepath.Join("..", "..", "config", "skills", "code-review", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	var raw M
	if err := json.Unmarshal(profileBytes, &raw); err != nil {
		t.Fatal(err)
	}
	skills := []any{M{"id": "code-review", "text": string(skillBytes)}}
	ev, _, err := profilePrepare(raw, "acc-1", skills)
	if err != nil {
		t.Fatal(err)
	}
	applied := typed[struct {
		Applied struct {
			Instructions string `json:"instructions"`
		} `json:"applied"`
	}](ev).Applied.Instructions
	if !strings.Contains(applied, "# Skill: code-review") || !strings.Contains(applied, "origin/<base>...HEAD") || !strings.Contains(applied, "artifact_save") {
		t.Fatalf("coding-worker developer_instructions missing code-review skill content: %s", applied)
	}
	if !strings.Contains(string(profileBytes), `"code-review"`) {
		t.Fatalf("coding-worker.json missing code-review in skills")
	}
}

// Requirement: T-FLT-29 — ParseRepoRemote and ParseForgeRepoEntry detect GitHub and self-hosted GitLab (including nested subgroups) while rejecting mismatched or malformed hosts.
// Risk: an untrusted remote or mismatched host hint redirects credentials or rejects valid self-hosted GitLab repositories.
func TestPublishingProviderDetectionAndBoundary(t *testing.T) {
	ref, ok := contract.ParseRepoRemote("git@gitlab.ufobe.com:gradus/cyprusbz.git", "gitlab", "")
	if !ok || ref.Provider != "gitlab" || ref.Host != "gitlab.ufobe.com" || ref.Slug != "gradus/cyprusbz" {
		t.Fatalf("unexpected self-hosted gitlab ref: %+v ok=%v", ref, ok)
	}
	subRef, ok := contract.ParseRepoRemote("https://gitlab.example.com/org/team/app.git", "", "")
	if !ok || subRef.Provider != "gitlab" || subRef.Host != "gitlab.example.com" || subRef.Slug != "org/team/app" {
		t.Fatalf("unexpected subgroup gitlab ref: %+v ok=%v", subRef, ok)
	}
	// Explicit forge: gitlab works even when hostname does not start with "gitlab."
	customRef, ok := contract.ParseRepoRemote("https://code.internal.corp/platform/svc.git", "gitlab", "code.internal.corp")
	if !ok || customRef.Provider != "gitlab" || customRef.Host != "code.internal.corp" || customRef.Slug != "platform/svc" {
		t.Fatalf("unexpected explicit gitlab host ref: %+v ok=%v", customRef, ok)
	}
	if _, ok := contract.ParseRepoRemote("https://code.internal.corp/platform/svc.git", "gitlab", "other.corp"); ok {
		t.Fatal("mismatched host hint accepted")
	}
	if _, ok := contract.ParseRepoRemote("https://code.internal.corp/platform/svc.git", "", ""); ok {
		t.Fatal("ambiguous non-github/non-gitlab host accepted without explicit forge")
	}
	if !contract.ForgeRepoGranted("gitlab.ufobe.com/gradus/cyprusbz,taskqorg/fleet", ref) {
		t.Fatal("expected granted self-hosted gitlab repo")
	}
	if contract.ForgeRepoGranted("gitlab.other.com/gradus/cyprusbz", ref) {
		t.Fatal("grant on wrong host accepted")
	}
}
