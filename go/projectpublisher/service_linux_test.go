package projectpublisher

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The operator service admits only scoped operations. Its provider path verifies repository scope,
// refreshes its installation identity, and fences delivery against the pull request's exact head.
func TestPublishingAppAPI(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	const branch = "fleet/invented-session"
	head := strings.Repeat("a", 40)
	const other = "other-fleet-test-peer"
	started, proceed := make(chan struct{}), make(chan struct{})
	firstProbe := true
	var mints int
	var operational string
	var author, description, target string
	var ready, queued bool
	var statusWrites int
	permissionsJSON, _ := json.Marshal(permissions)
	repositoryJSON := fmt.Sprintf(`{"id":%d,"full_name":%q}`, RepoID, Repo)

	installation := &installation{app: 123, id: 456, key: key, do: func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != "https" || (r.URL.Host != "api.github.com" && r.URL.Host != "github.com") {
			t.Fatalf("unexpected provider origin %s", r.URL)
		}
		bearer := r.Header.Get("Authorization")
		reply := func(body string) *http.Response {
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body))}
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /app":
			if firstProbe {
				firstProbe = false
				close(started)
				select {
				case <-proceed:
				case <-r.Context().Done():
					return nil, r.Context().Err()
				}
			}
			if strings.Count(bearer, ".") != 2 {
				t.Fatal("App lookup did not use a signed JWT")
			}
			return reply(`{"id":123,"slug":"` + Slug + `","owner":{"login":"taskqorg"}}`), nil
		case "GET /apps/" + Slug:
			if bearer != "" || r.Header.Get("Cookie") != "" {
				t.Fatal("visibility lookup leaked authorization")
			}
			v := reply(privateFixture)
			v.Header = http.Header{"Content-Type": []string{"text/html"}}
			return v, nil
		case "GET " + repoPath + "/installation":
			return reply(fmt.Sprintf(`{"id":456,"app_id":123,"app_slug":%q,"repository_selection":"selected","permissions":%s}`, Slug, permissionsJSON)), nil
		case "POST /app/installations/456/access_tokens":
			mints++
			var input struct {
				RepositoryIDs []int64 `json:"repository_ids"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			value := fmt.Sprintf("invented-installation-token-%d", mints)
			if len(input.RepositoryIDs) > 0 {
				if len(input.RepositoryIDs) != 1 || input.RepositoryIDs[0] != RepoID {
					t.Fatal("operational token requires exact repo ID")
				}
				operational = value
			}
			return reply(fmt.Sprintf(`{"token":%q,"expires_at":%q,"permissions":%s}`,
				value, time.Now().UTC().Add(time.Hour).Format(time.RFC3339), permissionsJSON)), nil
		case "GET /installation/repositories":
			if bearer == "Bearer "+operational {
				t.Fatal("expected proof of the complete installation scope")
			}
			return reply(`{"total_count":1,"repositories":[` + repositoryJSON + `]}`), nil
		case "DELETE /installation/token":
			return reply("{}"), nil
		}
		if bearer != "Bearer "+operational || operational == "" {
			t.Fatal("operation did not use the repo-scoped installation token")
		}
		pr := func() string {
			return fmt.Sprintf(`{"node_id":"PR_fixture","number":42,"html_url":"https://github.com/%s/pull/42","title":"fixture","draft":%t,"state":"open","user":{"login":%q},"head":{"sha":%q,"ref":%q,"repo":%s},"base":{"ref":"main","repo":%s}}`,
				Repo, !ready, author, head, branch, repositoryJSON, repositoryJSON)
		}
		switch r.Method + " " + r.URL.Path {
		case "GET " + repoPath + "/pulls/42":
			return reply(pr()), nil
		case "GET " + repoPath + "/pulls":
			if r.URL.Query().Get("head") != "taskqorg:"+branch {
				t.Fatal("pull request lookup escaped its branch")
			}
			if author == "" {
				return reply("[]"), nil
			}
			return reply("[" + pr() + "]"), nil
		case "POST " + repoPath + "/pulls":
			author = Actor
			return reply(pr()), nil
		case "POST " + repoPath + "/statuses/" + head:
			var input struct {
				Description string `json:"description"`
				Target      string `json:"target_url"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			description, target = input.Description, input.Target
			statusWrites++
			return reply("{}"), nil
		case "GET " + repoPath + "/commits/" + head + "/statuses":
			return reply(fmt.Sprintf(`[{"context":"code-review","state":"success","description":%q,"target_url":%q,"creator":{"login":%q}}]`,
				description, target, Actor)), nil
		case "POST /graphql":
			var req struct {
				Query     string
				Variables map[string]any
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			switch {
			case strings.Contains(req.Query, "markPullRequestReadyForReview"):
				ready = true
				return reply(`{"data":{"markPullRequestReadyForReview":{"pullRequest":{"id":"PR_fixture"}}}}`), nil
			case strings.Contains(req.Query, "dequeuePullRequest"):
				if req.Variables["id"] != "PR_fixture" || !queued {
					t.Fatal("dequeue requires the pull request ID from the scoped lookup")
				}
				queued = false
				return reply(`{"data":{"dequeuePullRequest":{"mergeQueueEntry":{"id":"queue-fixture"}}}}`), nil
			case strings.Contains(req.Query, "enqueuePullRequest"):
				if req.Variables["id"] != "PR_fixture" || req.Variables["head"] != head || !ready {
					t.Fatal("merge queue request lost the exact head or PR grant")
				}
				queued = true
				return reply(`{"data":{"enqueuePullRequest":{"mergeQueueEntry":{"id":"queue-fixture"}}}}`), nil
			}
			entry := "null"
			if queued {
				entry = `{"id":"queue-fixture","state":"QUEUED","position":1}`
			}
			return reply(fmt.Sprintf(`{"data":{"repository":{"pullRequest":{"id":"PR_fixture","headRefOid":%q,"state":"OPEN","isDraft":%t,"isMergeQueueEnabled":true,"mergeStateStatus":"CLEAN","mergeQueueEntry":%s}}}}`, head, !ready, entry)), nil
		default:
			t.Fatalf("unexpected provider operation %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
	}}
	p := publisher{installation: installation, coordinator: 1, executor: 2, tickets: map[string]pushTicket{}}
	serve := func(uid uint32, path string, op Operation) *httptest.ResponseRecorder {
		t.Helper()
		body, _ := json.Marshal(op)
		method := http.MethodPost
		if path == "/readiness" {
			method = http.MethodGet
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(body)))
		w := httptest.NewRecorder()
		p.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), peerKey{}, uid)))
		if strings.Contains(w.Body.String(), "invented-installation-token") {
			t.Fatal("installation token appeared in an IPC reply")
		}
		return w
	}

	probeCtx, cancelProbe := context.WithCancel(context.Background())
	cancelled := make(chan error, 1)
	go func() { _, err := installation.access(probeCtx); cancelled <- err }()
	<-started
	cancelProbe()
	if err := <-cancelled; err == nil || err.Error() != "publisher-installation-verification-pending" {
		t.Fatalf("cancelled probe did not leave shared refresh pending: %v", err)
	}
	close(proceed)

	op := Operation{Action: "publish_draft", Branch: branch, Base: "main", Head: head, Title: "fixture"}
	if w := serve(2, "/operation", op); w.Code != http.StatusForbidden {
		t.Fatal("executor peer could grant publication")
	}
	if w := serve(3, "/readiness", op); w.Code != http.StatusForbidden {
		t.Fatalf("%s could read the installation", other)
	}
	var ticket Reply
	if err := json.Unmarshal(serve(1, "/operation", op).Body.Bytes(), &ticket); err != nil || ticket.Ticket == "" {
		t.Fatalf("draft got no scoped ticket: %v", err)
	}
	approved, ok := p.consume(ticket.Ticket)
	if !ok || approved != op {
		t.Fatal("draft ticket lost its operation binding")
	}
	if _, ok := p.consume(ticket.Ticket); ok {
		t.Fatal("draft ticket could be used twice")
	}
	op.Action = "status"
	w := serve(1, "/operation", op)
	if !strings.Contains(w.Body.String(), `"state":"UNPUBLISHED"`) {
		t.Fatalf("unexpected unpublished status %s", w.Body)
	}
	// Enter the provider stage after the Git pack push. The fixture sends no pack to live GitHub.
	w = httptest.NewRecorder()
	p.run(w, httptest.NewRequest(http.MethodPost, "/push", nil), approved, nil)
	if !strings.Contains(w.Body.String(), `"pullRequestAuthor":"`+Actor+`"`) {
		t.Fatalf("draft author unconfirmed: %s", w.Body)
	}
	op.Action, op.Head, op.Description, op.TargetURL = "record_code_review", strings.Repeat("b", 40), "medium, 0 findings fixed", "https://example.test/review"
	if w := serve(1, "/operation", op); statusWrites != 0 || !strings.Contains(w.Body.String(), "reviewed head") {
		t.Fatal("mismatched head permitted status write")
	}
	op.Head = head
	if w := serve(1, "/operation", op); statusWrites != 1 || !strings.Contains(w.Body.String(), `"readbackVerified":true`) {
		t.Fatalf("review status not read back: %s", w.Body)
	}
	op.Action = "mark_ready"
	if w := serve(1, "/operation", op); !ready || !strings.Contains(w.Body.String(), `"isDraft":false`) {
		t.Fatalf("ready state unconfirmed: %s", w.Body)
	}
	op.Action, op.MergeMethod = "request_merge", "squash"
	if w := serve(1, "/operation", op); !queued || !strings.Contains(w.Body.String(), `"autoMergeEnabled":true`) {
		t.Fatalf("merge queue request unconfirmed: %s", w.Body)
	}
	old := operational
	p.installation.cached.verified = time.Now().Add(-11 * time.Minute)
	op.Action = "status"
	if w := serve(1, "/operation", op); operational == old || mints != 4 || !strings.Contains(w.Body.String(), `"state":"QUEUED"`) {
		t.Fatalf("status after forced rotation: %s", w.Body)
	}
	op.Action, op.Number, op.Branch = "cancel_merge_request", 42, ""
	if w := serve(1, "/operation", op); queued || !strings.Contains(w.Body.String(), `"cancelled":true`) {
		t.Fatalf("operator cancellation failed: %s", w.Body)
	}
}

// Credential mode is evaluated only after the exact read-only systemd directory is verified.
func TestPublisherCredentialMode(t *testing.T) {
	t.Setenv("CREDENTIALS_DIRECTORY", t.TempDir())
	if _, err := readCredential(); err == nil || err.Error() != "publisher systemd credential directory invalid" {
		t.Fatal("a caller-selected PEM directory was accepted")
	}
	const service = 61100
	for _, tc := range []struct {
		mode         uint32
		owner, group uint32
		links        uint64
		want         bool
	}{
		{unix.S_IFREG | 0440, 0, 0, 1, true},
		{unix.S_IFREG | 0400, service, service, 1, true},
		{unix.S_IFREG | 0440, 0, service, 1, false},
		{unix.S_IFREG | 0444, 0, 0, 1, false},
		{unix.S_IFREG | 0640, 0, 0, 1, false},
		{unix.S_IFREG | 0440, 0, 0, 2, false},
		{unix.S_IFLNK | 0440, 0, 0, 1, false},
	} {
		st := unix.Stat_t{Mode: tc.mode, Uid: tc.owner, Gid: tc.group}
		st.Nlink = 1
		if tc.links == 2 {
			st.Nlink = 2
		}
		if got := validCredentialMode(st, service); got != tc.want {
			t.Errorf("mode %o uid %d gid %d links %d = %t", tc.mode, tc.owner, tc.group, tc.links, got)
		}
	}
}

// The receiver sees an object stream. Candidate configuration and transport settings need not be trusted.
func TestPublisherExactHeadObjectImport(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	dir := t.TempDir()
	run := func(stdin io.Reader, args ...string) []byte {
		t.Helper()
		b, err := git(ctx, dir, stdin, "", args...)
		if err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
		return b
	}
	run(nil, "init", "--template=", "--object-format=sha1", ".")
	run(nil, "-c", "user.name=Invented", "-c", "user.email=invented@example.test", "commit", "--allow-empty", "-m", "fixture")
	head := strings.TrimSpace(string(run(nil, "rev-parse", "HEAD")))
	pack := run(strings.NewReader(head+"\n"), "pack-objects", "--stdout", "--revs")
	if err := importObjects(ctx, t.TempDir(), bytes.NewReader(pack), head); err != nil {
		t.Fatalf("authorized pack failed: %v", err)
	}
	if err := importObjects(ctx, t.TempDir(), bytes.NewReader(pack), strings.Repeat("a", 40)); err == nil || err.Error() != "publisher-object-head-mismatch" {
		t.Fatalf("different authorized head was accepted: %v", err)
	}
}
