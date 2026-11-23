package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
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

// Requirement: T-FLT-27 — coordinator /api/hosts/forge/review enforces host/project/profile/SHA/evidence/permission checks and records an audit_log entry.
// Risk: a managed execution posts code-review status for an unrelated project, wrong SHA, read-only session, or fabricated evidence.
func TestForgeReviewRoute(t *testing.T) {
	env := openTestCoordinator(t, true)
	oldLocations, oldSecrets, oldEndpointHosts := defaultLocations, storeSecretsDir, storeEndpointHosts
	defaultLocations = []string{"forge-host"}
	storeSecretsDir = t.TempDir()
	storeEndpointHosts = []string{"127.0.0.1"}
	t.Cleanup(func() {
		defaultLocations, storeSecretsDir, storeEndpointHosts = oldLocations, oldSecrets, oldEndpointHosts
	})

	var mu sync.Mutex
	objects := map[string][]byte{}
	uploads := map[string]map[int][]byte{}
	uploadSeq := 0
	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		q := r.URL.Query()
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		defer mu.Unlock()
		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			uploadSeq++
			uid := fmt.Sprintf("up-%d", uploadSeq)
			uploads[uid] = map[int][]byte{}
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>`+uid+`</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && q.Has("partNumber"):
			uid := q.Get("uploadId")
			pn, _ := strconv.Atoi(q.Get("partNumber"))
			if uploads[uid] == nil {
				uploads[uid] = map[int][]byte{}
			}
			uploads[uid][pn] = body
			w.Header().Set("ETag", `"etag-1"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && q.Has("uploadId"):
			uid := q.Get("uploadId")
			parts := uploads[uid]
			nums := make([]int, 0, len(parts))
			for n := range parts {
				nums = append(nums, n)
			}
			sort.Ints(nums)
			var buf bytes.Buffer
			for _, n := range nums {
				buf.Write(parts[n])
			}
			objects[key] = buf.Bytes()
			delete(uploads, uid)
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult/>`)
		case r.Method == http.MethodDelete && q.Has("uploadId"):
			delete(uploads, q.Get("uploadId"))
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPut:
			if src := r.Header.Get("x-amz-copy-source"); src != "" {
				unescaped, _ := url.PathUnescape(strings.TrimPrefix(src, "/"))
				objects[key] = append([]byte(nil), objects[unescaped]...)
				w.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(w, `<CopyObjectResult/>`)
				return
			}
			objects[key] = body
			w.Header().Set("ETag", `"etag-1"`)
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodDelete:
			delete(objects, key)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet, r.Method == http.MethodHead:
			data, ok := objects[key]
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Length", strconv.Itoa(len(data)))
			w.WriteHeader(http.StatusOK)
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer fakeS3.Close()

	hostID := "forge-host"
	hostSecret := env.registerHost(t, hostID)
	hostHeaders := map[string]string{"Authorization": "Bearer " + hostSecret}

	code, body := env.callJSON(t, "POST", "/api/hosts/stores", doc{
		"store": doc{
			"id":              "forge-store",
			"bucket":          "fleet",
			"endpoint":        fakeS3.URL,
			"accessKeyId":     "k",
			"secretAccessKey": "s",
		},
	}, hostHeaders)
	if code != 200 {
		t.Fatalf("stores = %d %v", code, body)
	}

	// Register project with execution.forge=true and coding-worker profile.
	projectID := "forge-proj"
	otherProjectID := "other-proj"
	for _, pid := range []string{projectID, otherProjectID} {
		code, body = env.callJSON(t, "POST", "/api/ops/project_create", doc{
			"id":        pid,
			"requestId": "req-" + pid,
			"content": doc{
				"settings": doc{
					"id":           pid,
					"name":         pid,
					"contextFiles": []string{},
					"repos":        []doc{{"id": "main", "remote": "https://github.com/invented/" + pid + ".git", "forge": "github", "defaultBranch": "main"}},
					"environments": []doc{{"hostId": hostID, "checkoutPath": "/tmp/" + pid}},
					"roles":        doc{"worker": "coding-worker", "reviewer": "codex-researcher"},
					"execution":    doc{"tools": []string{}, "forge": true, "profiles": []string{"coding-worker"}},
				},
				"contextFiles": []doc{},
			},
		}, env.ownerHeaders())
		if code != 200 {
			t.Fatalf("project_create(%s) = %d %v", pid, code, body)
		}
	}

	// Poll host with forge capability for invented/forge-proj only.
	code, body = env.callJSON(t, "POST", "/api/hosts/poll", doc{
		"platform":     "darwin",
		"capabilities": []string{"codex", "claude", "start-access-v1", "forge:invented/" + projectID},
		"stores":       []doc{},
		"problems":     []doc{},
	}, hostHeaders)
	if code != 200 {
		t.Fatalf("poll = %d %v", code, body)
	}

	// Create tasks for task-scoping check.
	code, task1 := env.callJSON(t, "POST", "/api/ops/task_create", doc{
		"projectId": projectID, "title": "Task 1", "expectedOutcome": "T1", "requestId": "task-1",
	}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("task_create(1) = %d %v", code, task1)
	}
	task1ID := task1["id"].(string)
	code, task2 := env.callJSON(t, "POST", "/api/ops/task_create", doc{
		"projectId": projectID, "title": "Task 2", "expectedOutcome": "T2", "requestId": "task-2",
	}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("task_create(2) = %d %v", code, task2)
	}
	task2ID := task2["id"].(string)

	// Start worker session, reviewer session, read-only session, and other-project session.
	startSession := func(pid, role, taskID, reqID string, access *string) (string, string) {
		req := doc{"projectId": pid, "role": role, "requestId": reqID, "text": "fixture " + reqID, "hostId": hostID}
		if taskID != "" {
			req["taskId"] = taskID
		}
		if access != nil {
			req["access"] = *access
			if *access == "read" {
				req["branch"] = false
			}
		}
		c, b := env.callJSON(t, "POST", "/api/ops/session_start", req, env.ownerHeaders())
		if c != 200 {
			t.Fatalf("session_start(%s) = %d %v", reqID, c, b)
		}
		return b["sessionId"].(string), b["executionId"].(string)
	}
	workerSession, workerExec := startSession(projectID, "worker", task1ID, "start-worker", nil)
	_, revExec := startSession(projectID, "reviewer", task1ID, "start-reviewer", nil)
	_, otherTaskRevExec := startSession(projectID, "reviewer", task2ID, "start-other-task-rev", nil)
	_, otherProjExec := startSession(otherProjectID, "reviewer", "", "start-other-proj", nil)
	roAccess := "read"
	_, roExec := startSession(projectID, "worker", task1ID, "start-ro", &roAccess)

	// Poll to get commands for all 5 executions and mark them accepted/started.
	cmdByExec := map[string]string{}
	needExecs := []string{workerExec, revExec, otherTaskRevExec, otherProjExec, roExec}
	for attempt := 0; attempt < 10 && len(cmdByExec) < len(needExecs); attempt++ {
		code, pollBody := env.callJSON(t, "POST", "/api/hosts/poll?wait=1000", doc{
			"platform":     "darwin",
			"capabilities": []string{"codex", "claude", "start-access-v1", "forge:invented/" + projectID, "forge:invented/" + otherProjectID},
			"stores":       []doc{},
			"problems":     []doc{},
		}, hostHeaders)
		if code != 200 {
			t.Fatalf("poll commands = %d %v", code, pollBody)
		}
		if cmds, ok := pollBody["commands"].([]any); ok {
			for _, raw := range cmds {
				m := raw.(map[string]any)
				exID := m["executionId"].(string)
				cmdID := m["id"].(string)
				cmdByExec[exID] = cmdID
				for _, st := range []string{"accepted", "started"} {
					rc, rb := env.callJSON(t, "POST", "/api/hosts/receipts", doc{
						"executionId": exID, "commandId": cmdID, "status": st,
					}, hostHeaders)
					if rc != 200 {
						t.Fatalf("receipt(%s,%s) = %d %v", exID, st, rc, rb)
					}
				}
			}
		}
	}
	for _, ex := range needExecs {
		if cmdByExec[ex] == "" {
			t.Fatalf("missing command for execution %s (got %v)", ex, cmdByExec)
		}
	}

	// Re-poll with ONLY forge:invented/forge-proj so capability checks for other-proj fail as expected.
	_, _ = env.callJSON(t, "POST", "/api/hosts/poll", doc{
		"platform":     "darwin",
		"capabilities": []string{"codex", "claude", "start-access-v1", "forge:invented/" + projectID},
		"stores":       []doc{},
		"problems":     []doc{},
	}, hostHeaders)

	// Upload a stored review artifact containing headSHA1 for task1, wrong-SHA artifact, and other-task/other-proj artifacts.
	headSHA1 := strings.Repeat("a", 40)
	headSHA2 := strings.Repeat("b", 40)
	storeArtifact := func(execID, name string, content []byte) string {
		sum := sha256.Sum256(content)
		digest := hex.EncodeToString(sum[:])
		rc, r := env.callJSON(t, "POST", "/api/hosts/artifacts/reserve", doc{
			"executionId": execID, "name": name, "mediaType": "text/markdown", "size": len(content), "sha256": digest,
		}, hostHeaders)
		if rc != 200 {
			t.Fatalf("reserve(%s) = %d %v", name, rc, r)
		}
		artID := r["artifactId"].(string)
		pc, p := env.callJSON(t, "POST", "/api/hosts/artifacts/parts", doc{"artifactId": artID, "partNumbers": []int{1}}, hostHeaders)
		if pc != 200 {
			t.Fatalf("parts = %d %v", pc, p)
		}
		partURL := p["parts"].([]any)[0].(map[string]any)["url"].(string)
		putReq, _ := http.NewRequest(http.MethodPut, partURL, strings.NewReader(string(content)))
		putResp, err := http.DefaultClient.Do(putReq)
		if err != nil || putResp.StatusCode != 200 {
			t.Fatalf("PUT part failed: %v %v", err, putResp)
		}
		putResp.Body.Close()
		cc, cb := env.callJSON(t, "POST", "/api/hosts/artifacts/commit", doc{
			"artifactId": artID, "parts": []doc{{"partNumber": 1, "etag": `"etag-1"`}},
		}, hostHeaders)
		if cc != 200 {
			t.Fatalf("commit = %d %v", cc, cb)
		}
		vc, vb := env.callJSON(t, "POST", "/api/hosts/artifacts/verified", doc{
			"artifactId": artID, "storeId": "forge-store", "sha256": digest,
		}, hostHeaders)
		if vc != 200 || vb["state"] != "stored" {
			t.Fatalf("verified = %d %v", vc, vb)
		}
		return artID
	}

	validArt := storeArtifact(revExec, "review-1.md", []byte("# Review\nCommit "+headSHA1+" passed with 0 blockers.\n"))
	wrongSHAArt := storeArtifact(revExec, "review-old.md", []byte("# Review\nCommit "+headSHA2+" passed.\n"))
	otherTaskArt := storeArtifact(otherTaskRevExec, "review-t2.md", []byte("# Review\nCommit "+headSHA1+" passed.\n"))
	otherProjArt := storeArtifact(otherProjExec, "review-other.md", []byte("# Review\nCommit "+headSHA1+" passed.\n"))

	// Pending (unverified) artifact must be rejected.
	pendingBytes := []byte("Commit " + headSHA1)
	pendingSum := sha256.Sum256(pendingBytes)
	pc, pb := env.callJSON(t, "POST", "/api/hosts/artifacts/reserve", doc{
		"executionId": revExec, "name": "pending.md", "mediaType": "text/markdown", "size": len(pendingBytes), "sha256": hex.EncodeToString(pendingSum[:]),
	}, hostHeaders)
	if pc != 200 {
		t.Fatalf("reserve pending = %d %v", pc, pb)
	}
	pendingArt := pb["artifactId"].(string)

	callReview := func(execID, repo, branch, sha, artID string) (int, map[string]any) {
		return env.callJSON(t, "POST", "/api/hosts/forge/review", doc{
			"hostId":       hostID,
			"commandId":    cmdByExec[execID],
			"executionId":  execID,
			"agentToolKey": "k-" + execID + "-" + artID,
			"repo":         repo,
			"branch":       branch,
			"headSha":      sha,
			"artifactId":   artID,
			"description":  "medium, 0 findings fixed",
			"githubActor":  "fleet-bot",
		}, hostHeaders)
	}

	// Negative tests:
	for _, tc := range []struct {
		name                             string
		execID, repo, branch, sha, artID string
		wantCode                         int
	}{
		{"wrong-sha-in-artifact", workerExec, "invented/" + projectID, "fleet/" + workerSession, headSHA1, wrongSHAArt, 409},
		{"short-sha", workerExec, "invented/" + projectID, "fleet/" + workerSession, headSHA1[:12], validArt, 400},
		{"pending-artifact", workerExec, "invented/" + projectID, "fleet/" + workerSession, headSHA1, pendingArt, 409},
		{"other-task-artifact", workerExec, "invented/" + projectID, "fleet/" + workerSession, headSHA1, otherTaskArt, 403},
		{"other-project-artifact", workerExec, "invented/" + projectID, "fleet/" + workerSession, headSHA1, otherProjArt, 403},
		{"ungranted-repo", workerExec, "invented/" + otherProjectID, "fleet/" + workerSession, headSHA1, validArt, 403},
		{"bad-branch-prefix", workerExec, "invented/" + projectID, "main", headSHA1, validArt, 403},
		{"read-only-session", roExec, "invented/" + projectID, "fleet/ro", headSHA1, validArt, 403},
		{"non-worker-profile-excluded", revExec, "invented/" + projectID, "fleet/rev", headSHA1, validArt, 403},
	} {
		rc, rb := callReview(tc.execID, tc.repo, tc.branch, tc.sha, tc.artID)
		if rc != tc.wantCode {
			t.Fatalf("%s: got status %d (%v), want %d", tc.name, rc, rb, tc.wantCode)
		}
	}

	// Positive path: valid worker execution, valid independent stored artifact naming exact SHA.
	rc, rb := callReview(workerExec, "invented/"+projectID, "fleet/"+workerSession, headSHA1, validArt)
	if rc != 200 {
		t.Fatalf("valid forge review = %d %v", rc, rb)
	}
	if rb["verified"] != true || rb["independent"] != true || rb["artifactId"] != validArt {
		t.Fatalf("unexpected reply: %v", rb)
	}

	// Confirm audit_log row was persisted.
	var auditCount int
	if err := env.ownerStore.pool.QueryRow(env.ctx, "SELECT count(*) FROM audit_log WHERE target=$1 AND field='code-review'", "forge:invented/"+projectID+":"+headSHA1).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("audit_log count = %d, want 1", auditCount)
	}
}
