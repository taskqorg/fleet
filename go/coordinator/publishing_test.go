package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"fleet/contract"
)

// Requirement: T-FLT-29 — coordinator publishing contract supports GitHub PR and GitLab MR (including self-hosted GitLab) capability discovery, live host records, and authority-checked mutations.
// Risk: GitLab projects lack publishing capabilities, host records fail to surface in publishing_status, or mutating ops bypass forge/autoMerge authority or idempotency.
func TestPublishingCapabilitiesStatusAndMutations(t *testing.T) {
	pubMu.Lock()
	pubRecords = map[string]contract.PublishingRecord{}
	pubCmdOutcomes = map[string]publishingCmdOutcome{}
	pubCmdWaiters = map[string]chan struct{}{}
	pubIdempotent = map[string]json.RawMessage{}
	pubMu.Unlock()

	env := openTestCoordinator(t, true)
	oldLocations, oldSecrets, oldEndpointHosts := defaultLocations, storeSecretsDir, storeEndpointHosts
	defaultLocations = []string{"gl-host-1"}
	storeSecretsDir = t.TempDir()
	storeEndpointHosts = []string{"127.0.0.1"}
	t.Cleanup(func() {
		defaultLocations, storeSecretsDir, storeEndpointHosts = oldLocations, oldSecrets, oldEndpointHosts
	})

	var fakeS3Mu sync.Mutex
	fakeS3Data := map[string][]byte{}
	fakeS3Uploads := map[string]map[string][]byte{}
	fakeS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		q := r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		fakeS3Mu.Lock()
		defer fakeS3Mu.Unlock()
		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			uid := "up-gl-1"
			fakeS3Uploads[uid] = map[string][]byte{}
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>`+uid+`</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && q.Has("partNumber"):
			uid := q.Get("uploadId")
			if fakeS3Uploads[uid] == nil {
				fakeS3Uploads[uid] = map[string][]byte{}
			}
			fakeS3Uploads[uid][q.Get("partNumber")] = b
			w.Header().Set("ETag", `"etag-gl-1"`)
			w.WriteHeader(200)
		case r.Method == http.MethodPost && q.Has("uploadId"):
			uid := q.Get("uploadId")
			fakeS3Data[key] = fakeS3Uploads[uid]["1"]
			delete(fakeS3Uploads, uid)
			w.WriteHeader(200)
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult/>`)
		case r.Method == http.MethodPut:
			if src := r.Header.Get("x-amz-copy-source"); src != "" {
				unescaped, _ := url.PathUnescape(strings.TrimPrefix(src, "/"))
				fakeS3Data[key] = append([]byte(nil), fakeS3Data[unescaped]...)
				w.WriteHeader(200)
				_, _ = io.WriteString(w, `<CopyObjectResult/>`)
				return
			}
			fakeS3Data[key] = b
			w.Header().Set("ETag", `"etag-gl-1"`)
			w.WriteHeader(200)
		case r.Method == http.MethodGet || r.Method == http.MethodHead:
			data, ok := fakeS3Data[key]
			if !ok {
				w.WriteHeader(404)
				return
			}
			w.WriteHeader(200)
			if r.Method == http.MethodGet {
				_, _ = w.Write(data)
			}
		default:
			w.WriteHeader(200)
		}
	}))
	defer fakeS3.Close()

	hostID := "gl-host-1"
	hostSecret := env.registerHost(t, hostID)
	hostHeaders := map[string]string{"Authorization": "Bearer " + hostSecret}
	code, body := env.callJSON(t, "POST", "/api/hosts/stores", doc{
		"store": doc{
			"id":              "gl-store",
			"location":        hostID,
			"region":          "us-east-1",
			"bucket":          "fleet",
			"endpoint":        fakeS3.URL,
			"accessKeyId":     "k",
			"secretAccessKey": "s",
		},
	}, hostHeaders)
	if code != 200 {
		t.Fatalf("stores = %d %v", code, body)
	}

	// Self-hosted GitLab project with SSH remote and nested subgroup + GitHub project without autoMerge.
	glProj := "cs-gl"
	ghProj := "gh-no-merge"
	code, body = env.callJSON(t, "POST", "/api/ops/project_create", doc{
		"id": glProj, "requestId": "req-gl-proj",
		"content": doc{
			"settings": doc{
				"id":           glProj,
				"name":         "Cyprusbz GitLab",
				"contextFiles": []string{},
				"repos": []doc{{
					"id":            "main",
					"remote":        "git@gitlab.ufobe.com:gradus/sub/cyprusbz.git",
					"forge":         "gitlab",
					"host":          "gitlab.ufobe.com",
					"defaultBranch": "main",
					"connection":    "cs-gitlab",
				}},
				"environments": []doc{{"hostId": hostID, "checkoutPath": "/tmp/" + glProj, "repo": "main"}},
				"integration":  doc{"mode": "pr", "autoMerge": true, "mergeMethod": "squash"},
				"roles":        doc{"worker": "coding-worker", "reviewer": "codex-researcher"},
				"execution":    doc{"tools": []string{}, "forge": true, "profiles": []string{"coding-worker"}},
			},
			"contextFiles": []doc{},
		},
	}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("project_create(gl) = %d %v", code, body)
	}

	code, body = env.callJSON(t, "POST", "/api/ops/project_create", doc{
		"id": ghProj, "requestId": "req-gh-proj",
		"content": doc{
			"settings": doc{
				"id":           ghProj,
				"name":         "GitHub Draft Only",
				"contextFiles": []string{},
				"repos":        []doc{{"id": "main", "remote": "https://github.com/taskqorg/draft-only.git", "forge": "github", "defaultBranch": "main"}},
				"environments": []doc{{"hostId": hostID, "checkoutPath": "/tmp/" + ghProj, "repo": "main"}},
				"integration":  doc{"mode": "pr", "autoMerge": false, "mergeMethod": "squash"},
				"roles":        doc{"worker": "coding-worker", "reviewer": "codex-researcher"},
				"execution":    doc{"tools": []string{}, "forge": true, "profiles": []string{"coding-worker"}},
			},
			"contextFiles": []doc{},
		},
	}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("project_create(gh) = %d %v", code, body)
	}

	// Host polls with both unified GitLab and GitHub publishing capabilities.
	glCap := contract.PublishingHostCapability("gitlab", "gitlab.ufobe.com", "gradus/sub/cyprusbz")
	ghCap := contract.PublishingHostCapability("github", "github.com", "taskqorg/draft-only")
	code, body = env.callJSON(t, "POST", "/api/hosts/poll", doc{
		"platform":     "linux",
		"capabilities": []string{"codex", "claude", "start-access-v1", glCap, ghCap},
		"stores":       []doc{},
		"problems":     []doc{},
	}, hostHeaders)
	if code != 200 {
		t.Fatalf("poll = %d %v", code, body)
	}

	// Verify publishing_capabilities parity for both self-hosted GitLab and GitHub.
	code, capBody := env.callJSON(t, "POST", "/api/ops/publishing_capabilities", doc{"projectId": glProj}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("publishing_capabilities(gl) = %d %v", code, capBody)
	}
	glCaps, _ := capBody["repos"].([]any)
	if len(glCaps) != 1 {
		t.Fatalf("expected 1 gitlab capability entry, got %v", capBody)
	}
	c0 := glCaps[0].(map[string]any)
	if c0["provider"] != "gitlab" || c0["host"] != "gitlab.ufobe.com" || c0["repo"] != "gradus/sub/cyprusbz" || c0["publicationSupported"] != true || c0["autoMergeGranted"] != true {
		t.Fatalf("unexpected gitlab capability: %v", c0)
	}

	// Verify request_merge is refused on GitHub project where integration.autoMerge=false.
	code, refuseBody := env.callJSON(t, "POST", "/api/ops/publishing_request_merge", doc{
		"projectId": ghProj, "requestId": "req-refuse-merge", "number": 42,
	}, env.ownerHeaders())
	if code != 403 {
		t.Fatalf("expected 403 when autoMerge=false, got %d %v", code, refuseBody)
	}

	// Verify forgeReviewRoute accepts self-hosted GitLab nested subgroup review attestation.
	code, revSess := env.callJSON(t, "POST", "/api/ops/session_start", doc{
		"projectId": glProj, "role": "reviewer", "requestId": "req-gl-rev-start", "text": "review", "hostId": hostID,
	}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("session_start(rev) = %d %v", code, revSess)
	}
	code, workSess := env.callJSON(t, "POST", "/api/ops/session_start", doc{
		"projectId": glProj, "role": "worker", "requestId": "req-gl-work-start", "text": "work", "hostId": hostID,
	}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("session_start(work) = %d %v", code, workSess)
	}
	revExecID := revSess["executionId"].(string)
	workExecID := workSess["executionId"].(string)
	workSessID := workSess["sessionId"].(string)

	// Poll host until dispatch assigns both reviewer and worker executions, and mark them accepted/started.
	cmdByExec := map[string]string{}
	for attempt := 0; attempt < 10 && len(cmdByExec) < 2; attempt++ {
		code, pollCmds := env.callJSON(t, "POST", "/api/hosts/poll?wait=1000", doc{
			"platform":     "linux",
			"capabilities": []string{"codex", "claude", "start-access-v1", glCap, ghCap},
			"stores":       []doc{},
			"problems":     []doc{},
		}, hostHeaders)
		if code != 200 {
			t.Fatalf("poll cmds = %d %v", code, pollCmds)
		}
		if cmds, ok := pollCmds["commands"].([]any); ok {
			for _, raw := range cmds {
				m := raw.(map[string]any)
				if m["kind"] == "start" {
					cid, eid := m["id"].(string), m["executionId"].(string)
					if _, seen := cmdByExec[eid]; !seen {
						cmdByExec[eid] = cid
						for _, st := range []string{"accepted", "started"} {
							c, rb := env.callJSON(t, "POST", "/api/hosts/receipts", doc{"commandId": cid, "executionId": eid, "hostId": hostID, "status": st}, hostHeaders)
							if c != 200 {
								t.Fatalf("receipt(%s) = %d %v", st, c, rb)
							}
						}
					}
				}
			}
		}
	}
	workCmdID := cmdByExec[workExecID]
	if workCmdID == "" || cmdByExec[revExecID] == "" {
		t.Fatalf("missing dispatched commands: %v", cmdByExec)
	}

	headSHA := strings.Repeat("c", 40)
	revBodyText := "Reviewed " + headSHA + " CLEAN\n"
	revSum := sha256.Sum256([]byte(revBodyText))
	revHash := hex.EncodeToString(revSum[:])
	code, res := env.callJSON(t, "POST", "/api/hosts/artifacts/reserve", doc{
		"executionId": revExecID, "name": "code-review.md", "mediaType": "text/markdown",
		"size": len(revBodyText), "sha256": revHash,
	}, hostHeaders)
	if code != 200 {
		t.Fatalf("reserve = %d %v", code, res)
	}
	artID := res["artifactId"].(string)
	code, partsResp := env.callJSON(t, "POST", "/api/hosts/artifacts/parts", doc{"artifactId": artID, "partNumbers": []int{1}}, hostHeaders)
	if code != 200 {
		t.Fatalf("parts = %d %v", code, partsResp)
	}
	partURL := partsResp["parts"].([]any)[0].(map[string]any)["url"].(string)
	reqPut, _ := http.NewRequest(http.MethodPut, partURL, strings.NewReader(revBodyText))
	respPut, err := http.DefaultClient.Do(reqPut)
	if err != nil || respPut.StatusCode != 200 {
		t.Fatalf("s3 put failed: %v", err)
	}
	_ = respPut.Body.Close()
	if c, b := env.callJSON(t, "POST", "/api/hosts/artifacts/commit", doc{"artifactId": artID, "parts": []doc{{"partNumber": 1, "etag": `"etag-gl-1"`}}}, hostHeaders); c != 200 {
		t.Fatalf("commit = %d %v", c, b)
	}
	if c, b := env.callJSON(t, "POST", "/api/hosts/artifacts/verified", doc{"artifactId": artID, "storeId": "gl-store", "sha256": revHash}, hostHeaders); c != 200 {
		t.Fatalf("verified = %d %v", c, b)
	}

	// Host posts GitLab code-review verification via /api/hosts/forge/review.
	code, forgeRev := env.callJSON(t, "POST", "/api/hosts/forge/review", doc{
		"hostId": hostID, "executionId": workExecID, "commandId": workCmdID, "agentToolKey": "k-gl",
		"provider": "gitlab", "host": "gitlab.ufobe.com",
		"repo": "gradus/sub/cyprusbz", "branch": "fleet/" + workSessID,
		"headSha": headSHA, "artifactId": artID,
		"description": "medium, 0 findings fixed", "githubActor": "gradus-bot", "providerActor": "gradus-bot",
	}, hostHeaders)
	if code != 200 || forgeRev["verified"] != true || forgeRev["provider"] != "gitlab" || forgeRev["host"] != "gitlab.ufobe.com" {
		t.Fatalf("forge_review(gitlab) = %d %v", code, forgeRev)
	}

	// Host reports a live self-hosted GitLab MR observation via /api/hosts/publishing/record and it surfaces in publishing_status.
	code, recReply := env.callJSON(t, "POST", "/api/hosts/publishing/record", doc{
		"hostId": hostID, "executionId": workExecID, "action": "publish_draft", "requestId": "req-gl-pub-1",
		"projectId": glProj, "repoId": "main", "provider": "gitlab", "host": "gitlab.ufobe.com",
		"repo": "gradus/sub/cyprusbz", "kind": "merge_request", "number": 77,
		"title": "Draft: GitLab parity", "url": "https://gitlab.ufobe.com/gradus/sub/cyprusbz/-/merge_requests/77",
		"branch": "fleet/" + workSessID, "baseBranch": "main", "headSha": headSHA,
		"isDraft": true, "state": "OPEN", "autoMergeEnabled": false, "mergeMethod": "squash",
		"sourceExecutionId": workExecID,
	}, hostHeaders)
	if code != 200 || recReply["ok"] != true {
		t.Fatalf("publishing/record = %d %v", code, recReply)
	}

	code, statusBody := env.callJSON(t, "POST", "/api/ops/publishing_status", doc{"projectId": glProj}, env.ownerHeaders())
	if code != 200 {
		t.Fatalf("publishing_status = %d %v", code, statusBody)
	}
	items := statusBody["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 GitLab MR item in publishing_status, got %v", statusBody)
	}
	it := items[0].(map[string]any)
	if it["provider"] != "gitlab" || it["host"] != "gitlab.ufobe.com" || it["kind"] != "merge_request" || int(it["number"].(float64)) != 77 || it["headSha"] != headSHA {
		t.Fatalf("unexpected publishing_status item: %v", it)
	}

	// Verify dispatch capability check accepts GitLab publishing capability when forge=true.
	if !hostHasProjectPublishingCapability([]string{glCap}, runSettings{
		Repos: []projectRepo{{ID: "main", Remote: "git@gitlab.ufobe.com:gradus/sub/cyprusbz.git", Forge: "gitlab", Host: "gitlab.ufobe.com"}},
	}, hostID) {
		t.Fatal("expected dispatch publishing capability check to accept self-hosted GitLab repo")
	}

	// End-to-end publishing_mark_ready command dispatch -> host poll -> RoutePublishingRecord -> idempotent replay.
	readyDone := make(chan struct{})
	var readyCode int
	var readyResp map[string]any
	go func() {
		defer close(readyDone)
		readyCode, readyResp = env.callJSON(t, "POST", "/api/ops/publishing_mark_ready", doc{
			"projectId": glProj, "repoId": "main", "number": 77, "requestId": "req-gl-ready-1", "expectedHeadSha": headSHA,
		}, env.ownerHeaders())
	}()

	var pubCmdID, pubExecID string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-readyDone:
			t.Fatalf("publishing_mark_ready exited before host command observed: code=%d resp=%v", readyCode, readyResp)
		default:
		}
		_, pBody := env.callJSON(t, "POST", "/api/hosts/poll?wait=1000", doc{
			"platform":     "linux",
			"capabilities": []string{"codex", "claude", "start-access-v1", glCap, ghCap},
		}, hostHeaders)
		if cmds, ok := pBody["commands"].([]any); ok {
			for _, raw := range cmds {
				m := raw.(map[string]any)
				if m["kind"] == "publishing" {
					pubCmdID, pubExecID = m["id"].(string), m["executionId"].(string)
					break
				}
			}
		}
		if pubCmdID != "" {
			break
		}
	}
	if pubCmdID == "" {
		t.Fatal("timed out waiting for publishing command on host poll")
	}

	readyPayload := doc{
		"requestId": "req-gl-ready-1", "provider": "gitlab", "host": "gitlab.ufobe.com",
		"repo": "gradus/sub/cyprusbz", "branch": "fleet/" + workSessID, "kind": "merge_request",
		"number": 77, "url": "https://gitlab.ufobe.com/gradus/sub/cyprusbz/-/merge_requests/77",
		"headSha": headSHA, "requested": true, "alreadyReady": false, "isDraft": false,
	}
	cRec, _ := env.callJSON(t, "POST", "/api/hosts/publishing/record", doc{
		"hostId": hostID, "commandId": pubCmdID, "executionId": pubExecID, "action": "mark_ready", "requestId": "req-gl-ready-1",
		"projectId": glProj, "repoId": "main", "provider": "gitlab", "host": "gitlab.ufobe.com",
		"repo": "gradus/sub/cyprusbz", "kind": "merge_request", "number": 77,
		"title": "GitLab parity", "url": "https://gitlab.ufobe.com/gradus/sub/cyprusbz/-/merge_requests/77",
		"branch": "fleet/" + workSessID, "baseBranch": "main", "headSha": headSHA,
		"isDraft": false, "state": "OPEN", "autoMergeEnabled": false, "result": readyPayload,
	}, hostHeaders)
	if cRec != 200 {
		t.Fatalf("publishing/record(mark_ready) = %d", cRec)
	}
	_, _ = env.callJSON(t, "POST", "/api/hosts/receipts", doc{"commandId": pubCmdID, "executionId": pubExecID, "hostId": hostID, "status": "completed"}, hostHeaders)

	<-readyDone
	if readyCode != 200 || readyResp["provider"] != "gitlab" || readyResp["isDraft"] != false {
		t.Fatalf("publishing_mark_ready = %d %v", readyCode, readyResp)
	}
	// Duplicate requestId returns idempotently.
	replayCode, replayResp := env.callJSON(t, "POST", "/api/ops/publishing_mark_ready", doc{
		"projectId": glProj, "repoId": "main", "number": 77, "requestId": "req-gl-ready-1", "expectedHeadSha": headSHA,
	}, env.ownerHeaders())
	if replayCode != 200 || replayResp["url"] != readyResp["url"] {
		t.Fatalf("idempotent replay failed: %d %v", replayCode, replayResp)
	}
}
