package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"fleet/contract"
	"fleet/projectpublisher"
)

// Requirement: host publication resolves the original managed workspace from the real executor journal.
// Risk: retained executions cannot publish, or a conflicting command redirects publication to another checkout.
func TestPublishingSourceWorktree(t *testing.T) {
	e, root := scriptExecutor(t)
	insert := func(id, execution, session, kind, key string) {
		t.Helper()
		p := &contract.StartPayload{WorkspaceKey: optStr(key)}
		var c contract.Command
		switch kind {
		case "start":
			c.Start = &contract.StartCommand{Id: id, ExecutionId: execution, SessionId: &session, Kind: kind, Payload: p}
		case "input":
			c.Input = &contract.InputCommand{Id: id, ExecutionId: execution, SessionId: &session, Kind: kind, Payload: p}
		case "stop":
			c.Stop = &contract.StopCommand{Id: id, ExecutionId: execution, SessionId: &session, Kind: kind}
		default:
			t.Fatalf("unexpected command kind %s", kind)
		}
		if _, err := e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,1)", id, enc(c)); err != nil {
			t.Fatal(err)
		}
	}
	workspace := func(session, key string) string {
		t.Helper()
		dir := filepath.Join(e.commandDir(session, key), "worktree")
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".git"), []byte("gitdir: /invented/metadata\n"), 0600); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	legacy := workspace("session-legacy", "")
	insert("start-legacy", "execution-legacy", "session-legacy", "start", "")
	got, err := e.publishingSourceWorktree("execution-legacy")
	if err != nil || got != legacy {
		t.Fatalf("legacy gitfile: got %q, %v", got, err)
	}
	nested := workspace("session-transition", "project-v2")
	insert("start-transition", "execution-transition", "session-transition", "start", "project-v2")
	insert("input-transition", "execution-transition", "session-transition", "input", "project-v2")
	got, err = e.publishingSourceWorktree("execution-transition")
	if err != nil || got != nested {
		t.Fatalf("transition: got %q, %v", got, err)
	}
	insert("input-only", "execution-input-only", "session-transition", "input", "project-v2")
	got, err = e.publishingSourceWorktree("execution-input-only")
	if err != nil || got != nested {
		t.Fatalf("input continuation: got %q, %v", got, err)
	}
	insert("unrelated-stop", "execution-legacy", "elsewhere", "stop", "")
	got, err = e.publishingSourceWorktree("execution-legacy")
	if err != nil || got != legacy {
		t.Fatalf("stop must not redirect: %q, %v", got, err)
	}
	insert("conflict", "execution-legacy", "session-transition", "input", "")
	if _, err = e.publishingSourceWorktree("execution-legacy"); err == nil {
		t.Fatal("ambiguous execution accepted")
	}
	insert("unsafe", "execution-unsafe", "../outside", "start", "")
	if _, err = e.publishingSourceWorktree("execution-unsafe"); err == nil {
		t.Fatal("unsafe session accepted")
	}
	if _, err := e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,1)", "bad-key", `{"kind":"start","sessionId":"session-legacy","executionId":"execution-bad-key","payload":{"workspaceKey":7}}`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.publishingSourceWorktree("execution-bad-key"); err == nil {
		t.Fatal("non-string workspace key accepted")
	}
	insert("missing", "execution-missing", "session-missing", "start", "")
	if _, err = e.publishingSourceWorktree("execution-missing"); err == nil {
		t.Fatal("missing worktree accepted")
	}
	if _, err = e.publishingSourceWorktree("execution-unknown"); err == nil {
		t.Fatal("unknown execution accepted")
	}
	if _, err := e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,1)", "bad-payload", `{"kind":"start","sessionId":"session-legacy","executionId":"bad-payload","payload":{"branch":"yes"}}`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.publishingSourceWorktree("bad-payload"); err == nil {
		t.Fatal("invalid typed start payload accepted")
	}
	outside := t.TempDir()
	for _, dir := range []string{outside, filepath.Join(outside, "worktree")} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(root, "sessions", "session-link")
	if err := os.MkdirAll(filepath.Dir(link), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	inside := e.commandDir("session-worktree-link", "")
	if err := os.MkdirAll(inside, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(inside, "worktree")); err != nil {
		t.Fatal(err)
	}
	insert("worktree-symlink", "execution-worktree-link", "session-worktree-link", "start", "")
	if _, err = e.publishingSourceWorktree("execution-worktree-link"); err == nil {
		t.Fatal("symlink worktree accepted")
	}
	insert("symlink", "execution-link", "session-link", "start", "")
	if _, err = e.publishingSourceWorktree("execution-link"); err == nil {
		t.Fatal("symlink session accepted")
	}
}

// Requirement: host jobs use the retained checkout and check both its branch and its head
// before requesting a review status or a push ticket. The provider in this test is local.
func TestPublishingHostJobFromLinkedWorktree(t *testing.T) {
	t.Setenv("FLEET_FORGE_REPOSITORIES", projectpublisher.Repo)
	e, _ := scriptExecutor(t)
	e.host = projectpublisher.Executor
	const session = "source-session"
	const source = "source-execution"
	const branch = "fleet/source-session"
	git := func(dir string, args ...string) string {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "-c", "core.hooksPath=/dev/null"}, args...)...)
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	repo := t.TempDir()
	git(repo, "init", "-q")
	git(repo, "commit", "--allow-empty", "-qm", "fixture")
	root := e.commandDir(session, "")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "worktree")
	git(repo, "worktree", "add", "-qb", branch, worktree)
	head := git(worktree, "rev-parse", "HEAD")
	start := contract.StartCommand{Id: "start", ExecutionId: source, SessionId: optStr(session), Kind: "start", Payload: &contract.StartPayload{Branch: new(true)}}
	if _, err := e.db.Exec("INSERT INTO commands(id,body,terminal) VALUES(?,?,1)", start.Id, enc(start)); err != nil {
		t.Fatal(err)
	}

	for _, action := range []string{"publish_draft", "record_code_review"} {
		t.Run(action, func(t *testing.T) {
			calls := make(chan contract.PublishingAppRequest, 3)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != contract.RoutePublishingApp.Path {
					http.NotFound(w, r)
					return
				}
				var request contract.PublishingAppRequest
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					http.Error(w, "request", 400)
					return
				}
				calls <- request
				w.Header().Set("Content-Type", "application/json")
				if action == "publish_draft" {
					// Stop at issuer admission. This fixture has no real signing key or push ticket.
					fmt.Fprint(w, `{"error":"test issuer withheld push ticket"}`)
				} else {
					fmt.Fprint(w, `{"result":{"number":27,"state":"OPEN","headRefName":"fleet/source-session"}}`)
				}
			}))
			defer srv.Close()
			e.url = srv.URL
			cases := []struct{ name, branch, head, failure string }{
				{"matching", branch, head, ""},
				{"other-branch", "fleet/another-source", head, "execution branch changed"},
				{"other-head", branch, strings.Repeat("1", 40), "local branch head does not match reviewed head_sha"},
			}
			for _, test := range cases {
				id := action + "-" + test.name
				p := contract.PublishingCommandPayload{Action: action, ProjectId: "fleet", Provider: "github", Host: "github.com", Repo: projectpublisher.Repo,
					RequestId: id, BaseBranch: "main", Prefix: optStr("fleet"), Branch: &test.branch, ExpectedHeadSha: &test.head, SourceExecutionId: optStr(source),
					Title: optStr("Fixture"), Body: optStr("Review fixture"), ArtifactId: optStr("review-fixture"), Description: optStr("Independent fixture review"), MergeMethod: "squash"}
				if err := e.run(M{"id": id, "executionId": "job-" + id, "kind": "publishing", "payload": p}); err != nil {
					t.Fatal(err)
				}
				var body string
				if err := e.db.QueryRow("SELECT body FROM outbox WHERE route=? AND json_extract(body,'$.commandId')=?", contract.RoutePublishingRecord.Path, id).Scan(&body); err != nil {
					t.Fatal(err)
				}
				var record contract.PublishingHostRecord
				if err := json.Unmarshal([]byte(body), &record); err != nil {
					t.Fatal(err)
				}
				failure := test.failure
				if test.name == "matching" {
					if len(calls) != 1 {
						t.Fatalf("%s did not reach the issuer", id)
					}
					req := <-calls
					if req.Action != action || req.CommandId != id || str0(req.ExpectedHeadSha) != head {
						t.Fatalf("issuer saw wrong source: %+v", req)
					}
					if action == "publish_draft" {
						failure = "test issuer withheld push ticket"
					} else if record.Number != 27 {
						t.Fatalf("unexpected review record: %+v", record)
					}
				}
				if str0(record.Error) != failure || len(calls) != 0 {
					t.Fatalf("%s: expected %q, got %+v with %d unexpected calls", id, failure, record, len(calls))
				}
			}
		})
	}
}
