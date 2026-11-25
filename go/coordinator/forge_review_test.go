package main

import (
	"strings"
	"testing"
)

// Requirement: T-FLT-27 — forge review validation refuses malformed repository slugs, branches, descriptions, and GitHub actor names.
// Risk: path injection, newline injection, or invalid GitHub status payloads bypass coordinator validation.
func TestForgeReviewInputValidation(t *testing.T) {
	for _, badRepo := range []string{"", "owner", "owner/repo/extra", "../repo", "owner/.."} {
		if validForgeRepoSlug(badRepo) {
			t.Fatalf("accepted bad repo %q", badRepo)
		}
	}
	if !validForgeRepoSlug("taskqorg/fleet") {
		t.Fatal("refused valid repo slug")
	}
	for _, badBranch := range []string{"", "/fleet/a", "fleet/a/", "fleet//a", "fleet/../a", "fleet/a b", "fleet/a\nb", "fleet/a:b"} {
		if validForgeBranch(badBranch) {
			t.Fatalf("accepted bad branch %q", badBranch)
		}
	}
	if !validForgeBranch("fleet/session-123") {
		t.Fatal("refused valid branch")
	}
	for _, badDesc := range []string{"", "   ", "line1\nline2", strings.Repeat("a", 141)} {
		if validForgeDescription(badDesc) {
			t.Fatalf("accepted bad description %q", badDesc)
		}
	}
	if !validForgeDescription("medium, 0 findings fixed") {
		t.Fatal("refused valid description")
	}
	for _, badActor := range []string{"", "a b", "a/b", "a\nb"} {
		if validForgeGithubActor(badActor) {
			t.Fatalf("accepted bad github actor %q", badActor)
		}
	}
	if !validForgeGithubActor("fleet-bot") {
		t.Fatal("refused valid github actor")
	}
}
