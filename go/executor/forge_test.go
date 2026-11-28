package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"fleet/contract"
	"fleet/projectpublisher"
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
	// A group grant covers today's and future projects of the group, on its host only.
	if !contract.ForgeRepoGranted("gitlab.ufobe.com/gradus/*", ref) {
		t.Fatal("group grant did not cover a project of the group")
	}
	if contract.ForgeRepoGranted("gitlab.other.com/gradus/*", ref) || contract.ForgeRepoGranted("gitlab.ufobe.com/grad/*", ref) {
		t.Fatal("group grant on wrong host or group accepted")
	}
	if _, _, ok := contract.ParseForgeRepoEntry("gitlab.ufobe.com/*"); ok {
		t.Fatal("host-wide grant accepted")
	}
}

// Requirement: P-71 reports provider-confirmed absence through both status tools.
// Risk: empty branches look like transport failures, or a failed lookup looks unpublished.
func TestPublishingBranchStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	dir := t.TempDir()
	t.Setenv("PATH", dir)
	for _, file := range []string{"hosts.yml", "config.yml"} {
		if err := os.WriteFile(filepath.Join(dir, file), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const branch = "fleet/fixture"
	sha := strings.Repeat("a", 40)
	moved := strings.Repeat("b", 40)
	const noPR = "echo 'no pull requests found for branch \"fleet/fixture\"' >&2; exit 1"
	const denied = "echo 'HTTP 403' >&2; exit 1"
	for _, provider := range []struct {
		name, host, cli, endpoint, absentLookup, existing, state string
	}{
		{"github", "github.com", "gh", "repos/invented/project/pulls?state=all&head=invented%3Afleet%2Ffixture", noPR,
			fmt.Sprintf(`{"number":17,"url":"https://github.com/invented/project/pull/17","state":"MERGED","headRefName":%q,"headRefOid":%q,"statusCheckRollup":[]}`, branch, sha), "MERGED"},
		{"gitlab", "gitlab.example.test", "glab", "projects/invented%2Fproject/merge_requests?state=opened&source_branch=fleet%2Ffixture", ":",
			fmt.Sprintf(`[{"iid":17,"web_url":"https://gitlab.example.test/invented/project/-/merge_requests/17","state":"opened","source_branch":%q,"sha":%q,"head_pipeline":{"id":42,"status":"success"}}]`, branch, sha), "OPEN"},
	} {
		t.Run(provider.name, func(t *testing.T) {
			ctx := M{"provider": provider.name, "host": provider.host, "repo": "invented/project",
				"repositories": provider.host + "/invented/project", "branch": branch, "store": dir, "skipWorktreeHead": true}
			const empty = "echo '[]'"
			const invalid = "echo 'null'"
			const readError = "project-delivery-unconfirmed: HTTP 403"
			existing := "echo '" + provider.existing + "'"
			type statusCase struct {
				name, prView, list, allList, state, wantError string
			}
			cases := []statusCase{
				{"unpublished", provider.absentLookup, empty, empty, "UNPUBLISHED", ""},
				{"existing", existing, existing, "exit 2", provider.state, ""},
				{"provider error", denied, denied, "exit 2", "", readError},
				{"malformed list", provider.absentLookup, invalid, "exit 2", "", "invalid forge response"},
			}
			if provider.name == "github" {
				cases = append(cases,
					statusCase{"failed confirmation", noPR, denied, "exit 2", "", readError},
					statusCase{"disagreement", noPR, "echo '[{}]'", "exit 2", "", "project-status-read-failed"},
					statusCase{"malformed view", "echo '{}'", empty, "exit 2", "", "invalid forge response"})
			} else {
				cases = append(cases,
					statusCase{"failed confirmation", ":", empty, denied, "", readError},
					statusCase{"malformed confirmation", ":", empty, invalid, "", "invalid forge response"},
					statusCase{"invalid merge request", ":", "echo '[null]'", "exit 2", "", "invalid forge response"})
				for _, state := range []string{"merged", "closed", "locked"} {
					allList := strings.Replace(existing, `"state":"opened"`, `"state":"`+state+`"`, 1)
					cases = append(cases, statusCase{state, ":", empty, allList, strings.ToUpper(state), ""})
				}
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					script := fmt.Sprintf(`#!/bin/sh
if [ "$1" = pr ] && [ "$2" = view ] && [ "$3" = %q ]; then
  %s
  exit 0
fi
case "$4" in
  %q) %s ;;
  %q) %s ;;
  */statuses) echo '[]' ;;
  *) exit 2 ;;
esac
`, branch, tc.prView, provider.endpoint, tc.list, strings.Replace(provider.endpoint, "state=opened", "state=all", 1), tc.allList)
					if err := os.WriteFile(filepath.Join(dir, provider.cli), []byte(script), 0o755); err != nil {
						t.Fatal(err)
					}
					for _, call := range []struct {
						name string
						run  func(string, any, any) (any, error)
						args M
					}{
						{"status", publishingCall, M{"expectedHeadSha": sha}},
						{"delivery", forgeCall, M{"action": "status"}},
					} {
						reply, err := call.run(call.name, call.args, ctx)
						if tc.wantError != "" {
							if err == nil || !strings.HasPrefix(err.Error(), tc.wantError) {
								t.Fatalf("%s: wanted %q, got %v, %v", call.name, tc.wantError, reply, err)
							}
							continue
						}
						if err != nil {
							t.Fatalf("%s: %v", call.name, err)
						}
						content := reply.(M)["content"].([]any)[0].(M)["text"].(string)
						got := decM(content)
						if got["state"] != tc.state || got["provider"] != provider.name || got["host"] != provider.host || got["repo"] != ctx["repo"] {
							t.Fatalf("%s: unexpected status %s", call.name, content)
						}
						published := tc.state != "UNPUBLISHED"
						if !published && (got["published"] != false || got["branch"] != branch || got["headRefOid"] != nil || got["number"] != nil) {
							t.Fatalf("%s: unexpected unpublished status %s", call.name, content)
						}
						if published && (got["headRefOid"] != sha || got["number"] != json.Number("17") || got["statusCheckRollup"] == nil) {
							t.Fatalf("%s: missing existing facts in %s", call.name, content)
						}
						if call.name == "status" && (got["expectedHeadSha"] != sha || got["headMatchesExpected"] != published) {
							t.Fatalf("%s: unexpected head comparison %s", call.name, content)
						}
					}
					if tc.state != "" && tc.state != "UNPUBLISHED" {
						got, err := executePublishingAction(typed[forgeContext](ctx), "status", publishingArgs{ExpectedHeadSha: &moved})
						if err != nil || got["headMatchesExpected"] != false {
							t.Fatalf("moved head matched: %v, %v", got, err)
						}
					}
				})
			}
		})
	}
}

// Requirement: the GitLab token comes from `glab config get token --host`; risk: glab 1.112 reads -h as --help, so the
// usage text became the token and every host lost GitLab publishing (2026-11-26).
func TestGlabCredentialUsesHostFlag(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do [ \"$a\" = -h ] && { echo 'USAGE glab config get <key> [--flags]'; exit 0; }; done\n" +
		"[ \"$5\" = gitlab.example.com ] && [ \"$4\" = --host ] && echo glpat-fixture\n"
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if got := forgeCredential(forgeContext{Provider: "gitlab", Host: "gitlab.example.com", Store: t.TempDir()}, "gitlab", "gitlab.example.com"); got != "glpat-fixture" {
		t.Fatalf("token %q", got)
	}
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte("#!/bin/sh\necho 'USAGE glab config get <key>'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := forgeCredential(forgeContext{Provider: "gitlab", Host: "gitlab.example.com", Store: t.TempDir()}, "gitlab", "gitlab.example.com"); got != "" {
		t.Fatalf("usage text taken as token %q", got)
	}
}

// Requirement: gitlabAdapter.requestMerge omits merge_when_pipeline_succeeds when the head pipeline is already
// terminal or absent, and sends it only when the pipeline is actively running/pending. Risk: GitLab rejects
// merging a green MR with 405/422 when merge_when_pipeline_succeeds=true is hardcoded (FLT-70).
func TestGitlabRequestMergePipelineStatus(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	logFile := filepath.Join(bin, "glab.args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"" + logFile + "\"\necho '{\"iid\":46,\"state\":\"merged\"}'\n"
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)

	ctx := forgeContext{
		Provider: "gitlab",
		Host:     "gitlab.ufobe.com",
		Repo:     "gradus/dam",
		Store:    t.TempDir(),
	}
	a := gitlabAdapter{host: "gitlab.ufobe.com"}

	for _, status := range []string{"success", "", "manual", "skipped"} {
		pr := pubView{Number: 46, HeadRefOid: "deadbeef", ChecksState: status}
		if err := a.requestMerge(ctx, pr, "merge"); err != nil {
			t.Fatalf("status=%q: %v", status, err)
		}
		got, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		line := string(got)
		if strings.Contains(line, "merge_when_pipeline_succeeds") {
			t.Fatalf("status=%q unexpectedly set merge_when_pipeline_succeeds: %s", status, line)
		}
		if !strings.Contains(line, "squash=false") || !strings.Contains(line, "sha=deadbeef") {
			t.Fatalf("status=%q missing merge flags: %s", status, line)
		}
	}

	for _, status := range []string{"running", "pending", "created", "preparing"} {
		pr := pubView{Number: 46, HeadRefOid: "deadbeef", ChecksState: status}
		if err := a.requestMerge(ctx, pr, "squash"); err != nil {
			t.Fatalf("status=%q: %v", status, err)
		}
		got, err := os.ReadFile(logFile)
		if err != nil {
			t.Fatal(err)
		}
		line := string(got)
		if !strings.Contains(line, "merge_when_pipeline_succeeds=true") || !strings.Contains(line, "squash=true") {
			t.Fatalf("status=%q expected merge_when_pipeline_succeeds=true and squash=true, got: %s", status, line)
		}
	}
}

// Requirement: forgeSanitizeErr skips glab deprecation warnings (and forgeCommand uses GLAB_NO_PROMPT)
// so warning lines cannot hide real API errors, and forgeLoginCheck rejects read-only GitLab PATs (FLT-70).
func TestGlabErrorSanitizeAndLoginScopeCheck(t *testing.T) {
	if err := forgeSanitizeErr(
		"project-delivery-unconfirmed",
		"DEPRECATION WARNING: The environment variable NO_PROMPT has been deprecated.\nglab: 403 Forbidden (HTTP 403)\n",
		errors.New("exit status 1"),
	); err == nil || strings.Contains(err.Error(), "DEPRECATION WARNING") || !strings.Contains(err.Error(), "403 Forbidden") {
		t.Fatalf("got %v, want real error line instead of deprecation warning", err)
	}

	if runtime.GOOS == "windows" {
		t.Skip("shell fixture")
	}
	bin := t.TempDir()
	store := t.TempDir()
	glabDir := filepath.Join(store, "glab")
	if err := os.MkdirAll(glabDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(glabDir, "config.yml"), []byte("hosts:\n  gitlab.ufobe.com:\n    token: glpat-test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FLEET_FORGE_CONFIG_DIR", store)
	t.Setenv("PATH", bin)

	// read_api-only token answers `user` but lacks `api` scope in `personal_access_tokens/self`.
	readOnlyScript := "#!/bin/sh\n" +
		"if [ -n \"$NO_PROMPT\" ] || [ \"$GLAB_NO_PROMPT\" != 1 ]; then exit 2; fi\n" +
		"case \"$*\" in\n" +
		"  *\"config get token\"*) echo glpat-test;;\n" +
		"  *\" user\"*) echo '{\"username\":\"fleet-gradus\"}';;\n" +
		"  *\"personal_access_tokens/self\"*) echo '{\"active\":true,\"scopes\":[\"read_api\"]}';;\n" +
		"  *) exit 1;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(readOnlyScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if forgeLoginCheck("gitlab", "gitlab.ufobe.com") {
		t.Fatal("forgeLoginCheck returned true for read_api-only token")
	}

	// api-scoped active token succeeds and passes GLAB_NO_PROMPT=1 (without deprecated NO_PROMPT).
	apiScript := strings.ReplaceAll(readOnlyScript, `"read_api"`, `"api","read_repository"`)
	if err := os.WriteFile(filepath.Join(bin, "glab"), []byte(apiScript), 0o755); err != nil {
		t.Fatal(err)
	}
	if !forgeLoginCheck("gitlab", "gitlab.ufobe.com") {
		t.Fatal("forgeLoginCheck returned false for active api-scoped token")
	}
}

// App operations use the same publishing result journal as the legacy host jobs. A branch-only operation must
// retain the provider's PR number so the coordinator can attach the observation to the request and task.
func TestPublishingAppHostJobRecord(t *testing.T) {
	t.Setenv("FLEET_FORGE_REPOSITORIES", projectpublisher.Repo)
	e, _ := scriptExecutor(t)
	e.host = projectpublisher.Executor
	const branch = "fleet/invented-host-job"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != contract.RoutePublishingApp.Path {
			t.Errorf("unexpected broker request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var req contract.PublishingAppRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Action != "mark_ready" || req.CommandId != "app-job" {
			t.Errorf("invalid App host request: %+v %v", req, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"result":{"number":42,"url":"https://github.com/taskqorg/fleet/pull/42","branch":"fleet/invented-host-job","isDraft":false}}`)
	}))
	defer server.Close()
	e.url = server.URL
	p := contract.PublishingCommandPayload{Action: "mark_ready", ProjectId: "fleet", Provider: "github", Host: "github.com", Repo: projectpublisher.Repo,
		RequestId: "invented-request", BaseBranch: "main", Prefix: optStr("fleet"), Branch: optStr(branch), MergeMethod: "squash"}
	if err := e.run(M{"id": "app-job", "executionId": "app-host-execution", "kind": "publishing", "payload": p}); err != nil {
		t.Fatal(err)
	}
	var encoded string
	if err := e.db.QueryRow("SELECT body FROM outbox WHERE route=?", contract.RoutePublishingRecord.Path).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var rec contract.PublishingHostRecord
	if err := json.Unmarshal([]byte(encoded), &rec); err != nil || rec.Number != 42 || rec.Branch != branch || rec.Error != nil {
		t.Fatalf("publishing observation lost the App request number: %+v, %v", rec, err)
	}
}

// The normal project-publishing tools receive the App result through the executor journal. The result
// is an object carried as json.RawMessage and must survive delivery and the journal wait intact.
func TestPublishingAppMCPJournalResult(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "journal.sqlite")
	db, err := openJournal(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != contract.RoutePublishingApp.Path {
			t.Errorf("unexpected publisher journal request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintln(w, `{"result":{"state":"OPEN","number":42,"isDraft":false}}`)
	}))
	defer server.Close()
	ctx := forgeContext{Journal: journalPath, ExecutionId: "app-source-run", CommandId: "app-start-command", HostId: projectpublisher.Executor}
	req := contract.PublishingAppRequest{HostId: ctx.HostId, ExecutionId: ctx.ExecutionId, CommandId: ctx.CommandId,
		AgentToolKey: "invented-app-status-request", Action: "status"}
	if err := journalOnce(db, req.AgentToolKey, enc(req), contract.RoutePublishingApp.Path, ctx.ExecutionId); err != nil {
		t.Fatal(err)
	}
	if err := flush(db, server.URL, "invented-token", ctx.HostId); err != nil {
		t.Fatal(err)
	}
	got, err := appJournalRequest(ctx, req)
	pr := typedRaw[forgePRView](got.Result)
	if err != nil || pr.Number != 42 || pr.State != "OPEN" || pr.IsDraft {
		t.Fatalf("MCP journal dropped provider status: result=%s err=%v", got.Result, err)
	}
}
