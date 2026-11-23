package main

import (
	"context"
	"database/sql"
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
