package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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

// Requirement: a granted branch can publish through the fixed operation; risk: credentials leak or request scope widens.
func TestForgePublishDraft(t *testing.T) {
	dir := t.TempDir()
	for name, script := range map[string]string{
		"git": "#!/bin/sh\ncase \"$*\" in *symbolic-ref*) echo fleet/fixture;; *push*|*check-ref-format*) exit 0;; *config*) if [ -f transport-error ]; then exit 2; else exit 1; fi;; *) exit 1;; esac\n",
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
	result, err := forgeCall("publish_draft", M{"title": "fixture", "body": "fixture"}, ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enc(result), "/pull/1") || strings.Contains(enc(result), "invented-token") {
		t.Fatal("invalid or leaking result")
	}
	if _, err := forgeCall("delivery", M{"action": "auto_merge"}, ctx); err == nil || !strings.Contains(err.Error(), "not granted") {
		t.Fatal("auto-merge widened project authority")
	}
	if err := os.WriteFile(filepath.Join(dir, "transport-error"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := forgeCall("publish_draft", M{"title": "fixture", "body": "fixture"}, ctx); err == nil {
		t.Fatal("unknown transport configuration accepted")
	}
	os.Remove(filepath.Join(dir, "transport-error"))
	ctx["branch"] = "fleet/other"
	if _, err = forgeCall("publish_draft", M{"title": "fixture", "body": "fixture"}, ctx); err == nil {
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

func forgeReviewJournal(t *testing.T, responder func(route, body string) string) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "journal.sqlite")
	db, err := openJournal(path)
	if err != nil {
		t.Fatal(err)
	}
	stop, done := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() {
		close(stop)
		<-done
		db.Close()
	})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
				rows, err := db.Query("SELECT id, body FROM delegation_requests WHERE response IS NULL")
				if err != nil {
					continue
				}
				type pending struct{ id, body string }
				var items []pending
				for rows.Next() {
					var p pending
					if rows.Scan(&p.id, &p.body) == nil {
						items = append(items, p)
					}
				}
				rows.Close()
				for _, it := range items {
					var route string
					_ = db.QueryRow("SELECT route FROM outbox WHERE body=? LIMIT 1", it.body).Scan(&route)
					reply := responder(route, it.body)
					_, _ = db.Exec("UPDATE delegation_requests SET response=? WHERE id=?", reply, it.id)
					wake(db, it.id)
				}
			}
		}
	}()
	return path, db
}

// Requirement: T-FLT-27 — project-forge delivery action=code_review posts exact-SHA code-review commit status only after authenticated actor, permission, live-head, and verified Fleet evidence checks, then verifies readback.
// Risk: credentials leak, unverified evidence posts a status, or an unconfirmed GitHub write reports success.
func TestForgeCodeReviewDelivery(t *testing.T) {
	dir := t.TempDir()
	headSHA := strings.Repeat("a", 40)
	var seenRoute, seenBody string
	journalPath, _ := forgeReviewJournal(t, func(route, body string) string {
		seenRoute, seenBody = route, body
		return enc(M{
			"verified":          true,
			"artifactId":        "art-123",
			"artifactSha256":    strings.Repeat("c", 64),
			"executionId":       "exec-1",
			"sessionId":         "sess-1",
			"reviewExecutionId": "exec-rev",
			"reviewSessionId":   "sess-rev",
			"actor":             "execution:exec-1",
			"onBehalfOf":        "owner@trial.test",
			"independent":       true,
			"targetUrl":         "https://fleet.example/results/art-123",
		})
	})

	gitScript := `#!/bin/sh
case "$*" in
  *"check-ref-format"*) exit 0 ;;
  *"symbolic-ref"*) echo "fleet/sess-1" ;;
  *"rev-parse HEAD"*) echo "` + headSHA + `" ;;
  *) exit 1 ;;
esac
`
	ghScript := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "token" ]; then
  echo "secret-forge-token"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  echo '{"number":42,"url":"https://github.com/invented/project/pull/42","isDraft":false,"state":"OPEN","headRefOid":"` + headSHA + `","headRefName":"fleet/sess-1"}'
  exit 0
fi
if [ "$1" = "api" ]; then
  case "$*" in
    *" user"*)
      echo '{"login":"fleet-bot"}'
      exit 0
      ;;
    *" repos/invented/project/statuses/` + headSHA + `"*)
      echo "$*" >> "` + filepath.Join(dir, "status-writes.log") + `"
      echo '{"state":"success","context":"code-review"}'
      exit 0
      ;;
    *" repos/invented/project/commits/` + headSHA + `/status"*)
      echo '{"sha":"` + headSHA + `","state":"success","statuses":[{"context":"code-review","state":"success","description":"medium, 0 findings fixed","target_url":"https://fleet.example/results/art-123","updated_at":"2026-11-23T18:00:00Z","creator":{"login":"fleet-bot"}}]}'
      exit 0
      ;;
    *" repos/invented/project"*)
      echo '{"full_name":"invented/project","permissions":{"push":true}}'
      exit 0
      ;;
  esac
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(gitScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(ghScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("github.com:\n  user: fleet-bot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	ctx := M{
		"repo":           "invented/project",
		"repositories":   "invented/project",
		"branch":         "fleet/sess-1",
		"prefix":         "fleet",
		"base":           "main",
		"store":          dir,
		"workdir":        dir,
		"home":           dir,
		"credentialHome": dir,
		"hostId":         "host-1",
		"commandId":      "cmd-1",
		"executionId":    "exec-1",
		"projectId":      "proj-1",
		"journal":        journalPath,
	}
	res, err := forgeCall("delivery", M{
		"action":      "code_review",
		"head_sha":    headSHA,
		"artifact_id": "art-123",
		"description": "medium, 0 findings fixed",
	}, ctx)
	if err != nil {
		t.Fatalf("code_review failed: %v", err)
	}
	encoded := enc(res)
	if strings.Contains(encoded, "secret-forge-token") {
		t.Fatal("token leaked in tool result")
	}
	payloadText := ""
	if items := jArr(res, "content"); len(items) > 0 {
		payloadText, _ = jAt(items[0], "text").(string)
	}
	for _, want := range []string{`"readbackVerified":true`, `"independentReview":true`, `"githubActor":"fleet-bot"`, `"fleetActor":"execution:exec-1"`, `"onBehalfOf":"owner@trial.test"`, `"headSha":"` + headSHA + `"`} {
		if !strings.Contains(payloadText, want) {
			t.Fatalf("result missing %s: %s", want, payloadText)
		}
	}
	if seenRoute != "/api/hosts/forge/review" || !strings.Contains(seenBody, headSHA) || !strings.Contains(seenBody, "art-123") {
		t.Fatalf("unexpected coordinator outbox request: route=%s body=%s", seenRoute, seenBody)
	}
	writes, err := os.ReadFile(filepath.Join(dir, "status-writes.log"))
	if err != nil || !strings.Contains(string(writes), "repos/invented/project/statuses/"+headSHA) || !strings.Contains(string(writes), "target_url=https://fleet.example/results/art-123") {
		t.Fatalf("expected one status write on exact SHA, got err=%v writes=%q", err, string(writes))
	}
}

// Requirement: T-FLT-27 — code_review refuses unrelated/changed SHAs, missing/rejected evidence, read-only contexts, missing permissions, and failed readback without mutating GitHub status.
// Risk: stale review status lands on a moved PR head, ungranted repo, or unverified commit.
func TestForgeCodeReviewRejections(t *testing.T) {
	headA := strings.Repeat("a", 40)
	headB := strings.Repeat("b", 40)

	runCase := func(t *testing.T, name string, mutateEnv func(dir string), ctxMod func(ctx M), args M, responder func(route, body string) string, wantErr string) {
		t.Helper()
		dir := t.TempDir()
		rejectMode := false
		if responder == nil {
			responder = func(route, body string) string {
				if rejectMode {
					return `{"error":"Coordinator rejected delegation","message":"review evidence rejected"}`
				}
				return enc(M{"verified": true, "artifactId": "art-1", "artifactSha256": strings.Repeat("c", 64), "executionId": "exec-1", "sessionId": "sess-1", "actor": "execution:exec-1", "independent": true})
			}
		}
		journalPath, _ := forgeReviewJournal(t, responder)

		gitScript := `#!/bin/sh
case "$*" in
  *"check-ref-format"*) exit 0 ;;
  *"symbolic-ref"*) [ -f "` + dir + `/bad-branch" ] && echo "fleet/other" || echo "fleet/sess-1" ;;
  *"rev-parse HEAD"*) [ -f "` + dir + `/local-moved" ] && echo "` + headB + `" || echo "` + headA + `" ;;
  *) exit 1 ;;
esac
`
		ghScript := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "token" ]; then
  echo "secret-forge-token"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  if [ -f "` + dir + `/pr-moved-on-second" ]; then
    if [ -f "` + dir + `/pr-view-seen" ]; then
      echo '{"number":42,"url":"https://github.com/invented/project/pull/42","isDraft":false,"state":"OPEN","headRefOid":"` + headB + `","headRefName":"fleet/sess-1"}'
      exit 0
    fi
    : > "` + dir + `/pr-view-seen"
  fi
  if [ -f "` + dir + `/pr-moved" ]; then
    echo '{"number":42,"url":"https://github.com/invented/project/pull/42","isDraft":false,"state":"OPEN","headRefOid":"` + headB + `","headRefName":"fleet/sess-1"}'
    exit 0
  fi
  if [ -f "` + dir + `/pr-closed" ]; then
    echo '{"number":42,"url":"https://github.com/invented/project/pull/42","isDraft":false,"state":"CLOSED","headRefOid":"` + headA + `","headRefName":"fleet/sess-1"}'
    exit 0
  fi
  echo '{"number":42,"url":"https://github.com/invented/project/pull/42","isDraft":false,"state":"OPEN","headRefOid":"` + headA + `","headRefName":"fleet/sess-1"}'
  exit 0
fi
if [ "$1" = "api" ]; then
  case "$*" in
    *" user"*)
      [ -f "` + dir + `/no-user" ] && exit 1
      echo '{"login":"fleet-bot"}'
      exit 0
      ;;
    *" repos/invented/project/statuses/"*)
      echo "$*" >> "` + filepath.Join(dir, "status-writes.log") + `"
      echo '{"state":"success"}'
      exit 0
      ;;
    *" repos/invented/project/commits/"*"/status"*)
      if [ -f "` + dir + `/bad-readback" ]; then
        echo '{"sha":"` + headA + `","state":"pending","statuses":[]}'
        exit 0
      fi
      echo '{"sha":"` + headA + `","state":"success","statuses":[{"context":"code-review","state":"success","description":"medium, 0 findings fixed"}]}'
      exit 0
      ;;
    *" repos/invented/project"*)
      [ -f "` + dir + `/no-push" ] && echo '{"full_name":"invented/project","permissions":{"push":false}}' && exit 0
      echo '{"full_name":"invented/project","permissions":{"push":true}}'
      exit 0
      ;;
  esac
fi
exit 1
`
		if err := os.WriteFile(filepath.Join(dir, "git"), []byte(gitScript), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(ghScript), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("github.com:\n  user: fleet-bot\n"), 0600); err != nil {
			t.Fatal(err)
		}
		if mutateEnv != nil {
			mutateEnv(dir)
		}
		t.Setenv("PATH", dir)

		ctx := M{
			"repo":           "invented/project",
			"repositories":   "invented/project",
			"branch":         "fleet/sess-1",
			"prefix":         "fleet",
			"base":           "main",
			"store":          dir,
			"workdir":        dir,
			"home":           dir,
			"credentialHome": dir,
			"hostId":         "host-1",
			"commandId":      "cmd-1",
			"executionId":    "exec-1",
			"projectId":      "proj-1",
			"journal":        journalPath,
		}
		if ctxMod != nil {
			ctxMod(ctx)
		}
		if args == nil {
			args = M{"action": "code_review", "head_sha": headA, "artifact_id": "art-1", "description": "medium, 0 findings fixed"}
		}
		_, err := forgeCall("delivery", args, ctx)
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("%s: got err=%v, want substring %q", name, err, wantErr)
		}
		if name != "readback-missing-status" {
			if _, statErr := os.Stat(filepath.Join(dir, "status-writes.log")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s: unexpected GitHub status mutation occurred", name)
			}
		}
	}

	t.Run("short-sha", func(t *testing.T) {
		runCase(t, "short-sha", nil, nil, M{"action": "code_review", "head_sha": headA[:12], "artifact_id": "art-1", "description": "medium, 0 findings fixed"}, nil, "invalid head_sha")
	})
	t.Run("missing-artifact", func(t *testing.T) {
		runCase(t, "missing-artifact", nil, nil, M{"action": "code_review", "head_sha": headA, "artifact_id": "", "description": "medium, 0 findings fixed"}, nil, "invalid artifact_id")
	})
	t.Run("multiline-description", func(t *testing.T) {
		runCase(t, "multiline-description", nil, nil, M{"action": "code_review", "head_sha": headA, "artifact_id": "art-1", "description": "ok\nbad"}, nil, "invalid description")
	})
	t.Run("read-only-context", func(t *testing.T) {
		runCase(t, "read-only-context", nil, func(c M) { c["readOnly"] = true }, nil, nil, "read-only")
	})
	t.Run("missing-journal-context", func(t *testing.T) {
		runCase(t, "missing-journal-context", nil, func(c M) { delete(c, "journal") }, nil, nil, "verification context unavailable")
	})
	t.Run("local-head-moved", func(t *testing.T) {
		runCase(t, "local-head-moved", func(d string) { os.WriteFile(filepath.Join(d, "local-moved"), nil, 0600) }, nil, nil, nil, "local branch head")
	})
	t.Run("pr-head-moved", func(t *testing.T) {
		runCase(t, "pr-head-moved", func(d string) { os.WriteFile(filepath.Join(d, "pr-moved"), nil, 0600) }, nil, nil, nil, "pull request head changed")
	})
	t.Run("pr-head-moved-after-verify", func(t *testing.T) {
		runCase(t, "pr-head-moved-after-verify", func(d string) { os.WriteFile(filepath.Join(d, "pr-moved-on-second"), nil, 0600) }, nil, nil, nil, "pull request head changed")
	})
	t.Run("pr-closed", func(t *testing.T) {
		runCase(t, "pr-closed", func(d string) { os.WriteFile(filepath.Join(d, "pr-closed"), nil, 0600) }, nil, nil, nil, "not open")
	})
	t.Run("no-github-actor", func(t *testing.T) {
		runCase(t, "no-github-actor", func(d string) { os.WriteFile(filepath.Join(d, "no-user"), nil, 0600) }, nil, nil, nil, "forge-actor-auth-unready")
	})
	t.Run("no-push-permission", func(t *testing.T) {
		runCase(t, "no-push-permission", func(d string) { os.WriteFile(filepath.Join(d, "no-push"), nil, 0600) }, nil, nil, nil, "forge-repository-push-unready")
	})
	t.Run("coordinator-rejects-evidence", func(t *testing.T) {
		runCase(t, "coordinator-rejects-evidence", nil, nil, nil, func(route, body string) string {
			return `{"error":"Coordinator rejected delegation","statusCode":409,"message":"Review artifact does not reference exact commit"}`
		}, "does not reference exact commit")
	})
	t.Run("readback-missing-status", func(t *testing.T) {
		runCase(t, "readback-missing-status", func(d string) { os.WriteFile(filepath.Join(d, "bad-readback"), nil, 0600) }, nil, nil, nil, "readback-unconfirmed")
	})
}

// Requirement: T-FLT-34 — project-forge delivery marks draft PRs ready idempotently, enables auto-merge without conflicting --<method> flags on merge-queue branches, reports mergeQueueEntry in status, and scrubs token-like patterns from failure stderr.
// Risk: coding-worker runs stall at draft PR when mergeMethod differs from a repository's merge_queue ruleset, or leak credentials in error output.
func TestForgeDeliveryMergeQueueAndReady(t *testing.T) {
	dir := t.TempDir()
	mergeLog := filepath.Join(dir, "merge-calls.log")
	readyLog := filepath.Join(dir, "ready-calls.log")

	gitScript := `#!/bin/sh
case "$*" in
  *"check-ref-format"*) exit 0 ;;
  *"symbolic-ref"*) echo "fleet/sess-34" ;;
  *) exit 1 ;;
esac
`
	ghScript := `#!/bin/sh
if [ "$1" = "auth" ] && [ "$2" = "token" ]; then
  echo "ghp_secret_forge_token_value"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "view" ]; then
  draft=false
  if [ -f "` + dir + `/is-draft" ] && [ ! -f "` + dir + `/ready-done" ]; then
    draft=true
  fi
  echo '{"number":99,"url":"https://github.com/invented/project/pull/99","isDraft":'"$draft"',"state":"OPEN","baseRefName":"main","headRefOid":"` + strings.Repeat("a", 40) + `","headRefName":"fleet/sess-34","mergeable":"MERGEABLE","mergeStateStatus":"CLEAN","autoMergeRequest":null}'
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "ready" ]; then
  echo "$*" >> "` + readyLog + `"
  : > "` + dir + `/ready-done"
  exit 0
fi
if [ "$1" = "pr" ] && [ "$2" = "merge" ]; then
  if [ -f "` + dir + `/fail-merge-with-secret" ]; then
    echo "GraphQL: ghp_secret_forge_token_value leaked" >&2
    echo "Pull request cannot be merged cleanly" >&2
    exit 1
  fi
  echo "$*" >> "` + mergeLog + `"
  exit 0
fi
if [ "$1" = "api" ]; then
  case "$*" in
    *"repos/invented/project/rules/branches/main"*)
      if [ -f "` + dir + `/no-queue" ]; then
        echo '[{"type":"pull_request"}]'
      else
        echo '[{"type":"pull_request"},{"type":"merge_queue"}]'
      fi
      exit 0
      ;;
    *"graphql"*)
      echo '{"data":{"repository":{"pullRequest":{"mergeQueueEntry":{"state":"QUEUED","position":1}}}}}'
      exit 0
      ;;
  esac
fi
exit 1
`
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte(gitScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(ghScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "hosts.yml"), []byte("github.com:\n  user: fleet-bot\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "is-draft"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	ctx := M{
		"repo":           "invented/project",
		"repositories":   "invented/project",
		"branch":         "fleet/sess-34",
		"prefix":         "fleet",
		"base":           "main",
		"autoMerge":      true,
		"mergeMethod":    "squash",
		"store":          dir,
		"workdir":        dir,
		"home":           dir,
		"credentialHome": dir,
	}

	// Draft PR must refuse auto_merge until marked ready.
	if _, err := forgeCall("delivery", M{"action": "auto_merge"}, ctx); err == nil || !strings.Contains(err.Error(), "still a draft") {
		t.Fatalf("expected draft refusal, got %v", err)
	}

	textOf := func(v any) string {
		if items := jArr(v, "content"); len(items) > 0 {
			s, _ := jAt(items[0], "text").(string)
			return s
		}
		return enc(v)
	}

	// Mark ready transitions draft -> ready, and second ready is idempotent without invoking gh pr ready again.
	res, err := forgeCall("delivery", M{"action": "ready"}, ctx)
	if err != nil {
		t.Fatalf("ready failed: %v", err)
	}
	if !strings.Contains(textOf(res), `"requested":true`) {
		t.Fatalf("unexpected ready reply: %v", res)
	}
	res, err = forgeCall("delivery", M{"action": "ready"}, ctx)
	if err != nil {
		t.Fatalf("second ready failed: %v", err)
	}
	if !strings.Contains(textOf(res), `"alreadyReady":true`) {
		t.Fatalf("expected alreadyReady on second ready, got %v", res)
	}
	readyBytes, err := os.ReadFile(readyLog)
	if err != nil || strings.Count(strings.TrimSpace(string(readyBytes)), "\n") != 0 {
		t.Fatalf("expected exactly 1 gh pr ready call, got %q (err=%v)", string(readyBytes), err)
	}

	// Merge-queue branch must omit --squash even when project mergeMethod is squash.
	if _, err := forgeCall("delivery", M{"action": "auto_merge"}, ctx); err != nil {
		t.Fatalf("auto_merge on merge_queue branch failed: %v", err)
	}
	// Non-queue branch keeps --squash.
	if err := os.WriteFile(filepath.Join(dir, "no-queue"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := forgeCall("delivery", M{"action": "auto_merge"}, ctx); err != nil {
		t.Fatalf("auto_merge on non-queue branch failed: %v", err)
	}
	mergeCalls := strings.Split(strings.TrimSpace(func() string { b, _ := os.ReadFile(mergeLog); return string(b) }()), "\n")
	if len(mergeCalls) != 2 {
		t.Fatalf("expected 2 merge calls, got %v", mergeCalls)
	}
	if strings.Contains(mergeCalls[0], "--squash") || !strings.Contains(mergeCalls[0], "--auto") {
		t.Fatalf("merge-queue call should omit --squash: %q", mergeCalls[0])
	}
	if !strings.Contains(mergeCalls[1], "--squash") || !strings.Contains(mergeCalls[1], "--auto") {
		t.Fatalf("non-queue call should pass --squash: %q", mergeCalls[1])
	}

	// Status includes mergeQueueEntry when open and non-draft.
	stRes, err := forgeCall("delivery", M{"action": "status"}, ctx)
	if err != nil {
		t.Fatalf("status failed: %v", err)
	}
	if !strings.Contains(enc(stRes), "QUEUED") {
		t.Fatalf("expected mergeQueueEntry in status, got %v", stRes)
	}

	// Failure stderr scrubs token prefixes while keeping actionable reason.
	if err := os.WriteFile(filepath.Join(dir, "fail-merge-with-secret"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = forgeCall("delivery", M{"action": "auto_merge"}, ctx)
	if err == nil || strings.Contains(err.Error(), "ghp_secret") || !strings.Contains(err.Error(), "Pull request cannot be merged cleanly") {
		t.Fatalf("expected sanitized merge error without secret, got %v", err)
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

// Requirement: T-FLT-29 — project-publishing supports full GitLab MR parity (including self-hosted GitLab): capabilities, publish_draft, record_code_review, mark_ready, request_merge, cancel_merge_request, expectedHeadSha fencing, and token scrubbing.
// Risk: self-hosted GitLab MRs stall, leak glpat-* tokens, bypass expectedHeadSha fencing, or skip code-review readback verification.
func TestPublishingGitLabSelfHostedParity(t *testing.T) {
	dir := t.TempDir()
	headA := strings.Repeat("a", 40)
	headB := strings.Repeat("b", 40)
	var seenRoute string
	journalPath, _ := forgeReviewJournal(t, func(route, body string) string {
		seenRoute = route
		return enc(M{
			"verified":       true,
			"artifactId":     "art-gl-1",
			"artifactSha256": strings.Repeat("d", 64),
			"executionId":    "exec-gl-1",
			"sessionId":      "sess-gl-1",
			"actor":          "execution:exec-gl-1",
			"independent":    true,
			"provider":       "gitlab",
			"host":           "gitlab.ufobe.com",
			"providerActor":  "gradus-bot",
		})
	})

	glabDir := filepath.Join(dir, "glab")
	if err := os.MkdirAll(glabDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(glabDir, "config.yml"), []byte("hosts:\n  gitlab.ufobe.com:\n    token: glpat-secret-test-value\n"), 0600); err != nil {
		t.Fatal(err)
	}

	gitScript := `#!/bin/sh
case "$*" in
  *"check-ref-format"*) exit 0 ;;
  *"symbolic-ref"*) echo "fleet-test/sess-gl-1" ;;
  *"rev-parse HEAD"*) echo "` + headA + `" ;;
  *"config --get-regexp"*) exit 1 ;;
  *"push"*) echo "$*" >> "` + filepath.Join(dir, "git-push.log") + `"; exit 0 ;;
  *) exit 1 ;;
esac
`
	glabScript := `#!/bin/sh
if [ "$1" = "config" ] && [ "$2" = "get" ] && [ "$3" = "token" ]; then
  echo "glpat-secret-test-value"
  exit 0
fi
if [ "$1" = "api" ]; then
  echo "$*" >> "` + filepath.Join(dir, "glab-api.log") + `"
  case "$*" in
    *" user"*)
      echo '{"username":"gradus-bot"}'
      exit 0
      ;;
    *" projects/gradus%2Fcyprusbz/merge_requests?state=opened"*)
      if [ ! -f "` + dir + `/mr-created" ]; then
        echo '[]'
        exit 0
      fi
      draft=true
      title="Draft: Add GitLab publishing"
      mwps=false
      if [ -f "` + dir + `/mr-ready" ]; then
        draft=false
        title="Add GitLab publishing"
      fi
      if [ -f "` + dir + `/mr-automerge" ]; then
        mwps=true
      fi
      echo '[{"iid":17,"web_url":"https://gitlab.ufobe.com/gradus/cyprusbz/-/merge_requests/17","title":"'"$title"'","draft":'"$draft"',"work_in_progress":false,"state":"opened","target_branch":"main","source_branch":"fleet-test/sess-gl-1","sha":"` + headA + `","merge_status":"can_be_merged","detailed_merge_status":"mergeable","merge_when_pipeline_succeeds":'"$mwps"',"head_pipeline":{"id":501,"status":"running","web_url":"https://gitlab.ufobe.com/gradus/cyprusbz/-/pipelines/501","sha":"` + headA + `"}}]'
      exit 0
      ;;
    *"--method POST projects/gradus%2Fcyprusbz/merge_requests/17/cancel_merge_when_pipeline_succeeds"*)
      rm -f "` + dir + `/mr-automerge"
      echo '{"iid":17,"merge_when_pipeline_succeeds":false}'
      exit 0
      ;;
    *"--method PUT projects/gradus%2Fcyprusbz/merge_requests/17/merge"*)
      if [ -f "` + dir + `/fail-merge-secret" ]; then
        echo "405 Method Not Allowed private-token glpat-secret-test-value" >&2
        echo "MR pipeline still required" >&2
        exit 1
      fi
      : > "` + dir + `/mr-automerge"
      echo '{"iid":17,"merge_when_pipeline_succeeds":true}'
      exit 0
      ;;
    *"--method PUT projects/gradus%2Fcyprusbz/merge_requests/17"*)
      : > "` + dir + `/mr-ready"
      echo '{"iid":17,"draft":false,"title":"Add GitLab publishing"}'
      exit 0
      ;;
    *"--method POST projects/gradus%2Fcyprusbz/merge_requests "*)
      : > "` + dir + `/mr-created"
      echo '{"iid":17,"web_url":"https://gitlab.ufobe.com/gradus/cyprusbz/-/merge_requests/17","title":"Draft: Add GitLab publishing","draft":true,"work_in_progress":false,"state":"opened","target_branch":"main","source_branch":"fleet-test/sess-gl-1","sha":"` + headA + `"}'
      exit 0
      ;;
    *"--method POST projects/gradus%2Fcyprusbz/statuses/` + headA + `"*)
      echo '{"name":"code-review","status":"success"}'
      exit 0
      ;;
    *"projects/gradus%2Fcyprusbz/repository/commits/` + headA + `/statuses"*)
      echo '[{"name":"code-review","status":"success","description":"medium, 0 findings fixed","updated_at":"2026-11-24T10:00:00Z","author":{"username":"gradus-bot"}}]'
      exit 0
      ;;
    *"projects/gradus%2Fcyprusbz"*)
      echo '{"path_with_namespace":"gradus/cyprusbz","permissions":{"project_access":{"access_level":40}}}'
      exit 0
      ;;
  esac
fi
exit 1
`
	binDir := filepath.Join(dir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "git"), []byte(gitScript), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "glab"), []byte(glabScript), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Setenv("FLEET_FORGE_CONFIG_DIR", dir)
	t.Setenv("FLEET_FORGE_REPOSITORIES", "gitlab.ufobe.com/gradus/cyprusbz")

	caps := capabilities()
	if !slices.Contains(caps, contract.PublishingHostCapability("gitlab", "gitlab.ufobe.com", "gradus/cyprusbz")) {
		t.Fatalf("expected gitlab publishing capability in %v", caps)
	}

	ctx := M{
		"provider":       "gitlab",
		"host":           "gitlab.ufobe.com",
		"repo":           "gradus/cyprusbz",
		"remote":         "https://gitlab.ufobe.com/gradus/cyprusbz.git",
		"repositories":   "gitlab.ufobe.com/gradus/cyprusbz",
		"branch":         "fleet-test/sess-gl-1",
		"prefix":         "fleet-test",
		"base":           "main",
		"autoMerge":      true,
		"mergeMethod":    "squash",
		"store":          dir,
		"glabStore":      glabDir,
		"workdir":        dir,
		"home":           dir,
		"credentialHome": dir,
		"hostId":         "host-1",
		"commandId":      "cmd-gl-1",
		"executionId":    "exec-gl-1",
		"projectId":      "cs",
		"journal":        journalPath,
	}
	textOf := func(v any) string {
		if items := jArr(v, "content"); len(items) > 0 {
			s, _ := jAt(items[0], "text").(string)
			return s
		}
		return enc(v)
	}

	capRes, err := publishingCall("capabilities", M{}, ctx)
	if err != nil || !strings.Contains(textOf(capRes), `"provider":"gitlab"`) || !strings.Contains(textOf(capRes), `"kind":"merge_request"`) {
		t.Fatalf("capabilities failed: err=%v res=%v", err, capRes)
	}
	// expectedHeadSha mismatch fences publish_draft before push.
	if _, err := publishingCall("publish_draft", M{"requestId": "req-1", "title": "Add GitLab publishing", "body": "desc", "expectedHeadSha": headB}, ctx); err == nil {
		t.Fatal("expected head mismatch error on publish_draft")
	}
	pubRes, err := publishingCall("publish_draft", M{"requestId": "req-1", "title": "Add GitLab publishing", "body": "desc", "expectedHeadSha": headA, "sourceExecutionId": "exec-gl-1"}, ctx)
	if err != nil || !strings.Contains(textOf(pubRes), `"kind":"merge_request"`) || !strings.Contains(textOf(pubRes), `merge_requests/17`) {
		t.Fatalf("publish_draft failed: err=%v res=%v", err, pubRes)
	}
	if strings.Contains(enc(pubRes), "glpat-secret") {
		t.Fatal("gitlab token leaked in publish_draft")
	}

	revRes, err := publishingCall("record_code_review", M{"requestId": "req-rev", "expectedHeadSha": headA, "artifactId": "art-gl-1", "description": "medium, 0 findings fixed", "sourceExecutionId": "exec-gl-1"}, ctx)
	if err != nil || !strings.Contains(textOf(revRes), `"readbackVerified":true`) || !strings.Contains(textOf(revRes), `"providerActor":"gradus-bot"`) {
		t.Fatalf("record_code_review failed: err=%v res=%v", err, revRes)
	}
	if seenRoute != "/api/hosts/forge/review" {
		t.Fatalf("unexpected review route: %s", seenRoute)
	}

	// Draft MR refuses request_merge before mark_ready.
	if _, err := publishingCall("request_merge", M{"requestId": "req-m0", "expectedHeadSha": headA}, ctx); err == nil || !strings.Contains(err.Error(), "still a draft") {
		t.Fatalf("expected draft refusal, got %v", err)
	}
	// Stale expectedHeadSha fences mark_ready.
	if _, err := publishingCall("mark_ready", M{"requestId": "req-r0", "expectedHeadSha": headB}, ctx); err == nil || !strings.Contains(err.Error(), "head changed") {
		t.Fatalf("expected head-change fence on mark_ready, got %v", err)
	}
	readyRes, err := publishingCall("mark_ready", M{"requestId": "req-r1", "expectedHeadSha": headA}, ctx)
	if err != nil || !strings.Contains(textOf(readyRes), `"alreadyReady":false`) {
		t.Fatalf("mark_ready failed: err=%v res=%v", err, readyRes)
	}
	mergeRes, err := publishingCall("request_merge", M{"requestId": "req-m1", "expectedHeadSha": headA, "mergeMethod": "squash"}, ctx)
	if err != nil || !strings.Contains(textOf(mergeRes), `"autoMergeEnabled":true`) {
		t.Fatalf("request_merge failed: err=%v res=%v", err, mergeRes)
	}
	cancelRes, err := publishingCall("cancel_merge_request", M{"requestId": "req-c1", "expectedHeadSha": headA}, ctx)
	if err != nil || !strings.Contains(textOf(cancelRes), `"cancelled":true`) {
		t.Fatalf("cancel_merge_request failed: err=%v res=%v", err, cancelRes)
	}
	// Second cancel is idempotent with alreadyInactive:true.
	cancelRes2, err := publishingCall("cancel_merge_request", M{"requestId": "req-c2", "expectedHeadSha": headA}, ctx)
	if err != nil || !strings.Contains(textOf(cancelRes2), `"alreadyInactive":true`) {
		t.Fatalf("second cancel_merge_request failed: err=%v res=%v", err, cancelRes2)
	}
	// Token scrubbing on GitLab failure stderr.
	if err := os.WriteFile(filepath.Join(dir, "fail-merge-secret"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	_, err = publishingCall("request_merge", M{"requestId": "req-m2", "expectedHeadSha": headA}, ctx)
	if err == nil || strings.Contains(err.Error(), "glpat-secret") || !strings.Contains(err.Error(), "MR pipeline still required") {
		t.Fatalf("expected sanitized GitLab error without token, got %v", err)
	}
}
