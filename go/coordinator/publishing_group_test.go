package main

import (
	"encoding/json"
	"testing"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"
)

// Requirement: configuration.md a host grant "<host>/<group>/*" places publishing for every project of the group;
// risk: a new project of the group needs a per-project host step, or a grant leaks to another host or group.
func TestHostCapsHaveRepoGroupGrant(t *testing.T) {
	caps := []string{contract.PublishingHostCapability("gitlab", "gitlab.ufobe.com", "gradus/*")}
	for slug, want := range map[string]bool{"gradus/dam": true, "gradus/sub/app": true, "other/dam": false, "gradusx/dam": false} {
		ref := contract.RepoRef{Provider: "gitlab", Host: "gitlab.ufobe.com", Slug: slug}
		if got := hostCapsHaveRepo(caps, ref); got != want {
			t.Errorf("%s: got %v, want %v", slug, got, want)
		}
	}
	if hostCapsHaveRepo(caps, contract.RepoRef{Provider: "gitlab", Host: "gitlab.com", Slug: "gradus/dam"}) {
		t.Error("group grant matched another host")
	}
}

// Requirement: resolvePublishingTarget prefers an online eligible host whose latest observation reports a
// verified forge login (ok: true) for the target forge over one with a missing or failed login (FLT-70).
func TestResolvePublishingTargetPrefersVerifiedForgeLogin(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	var ignore map[string]any
	e.opInto(t, "project_create", map[string]any{
		"requestId": "req-create-dam",
		"id":        "dam",
		"content": map[string]any{
			"settings": map[string]any{
				"id":           "dam",
				"name":         "dam",
				"contextFiles": []string{"rules.md"},
				"repos": []map[string]any{{
					"id": "main", "remote": "git@gitlab.ufobe.com:gradus/dam.git", "forge": "gitlab", "defaultBranch": "main",
				}},
				"environments": []map[string]any{
					{"hostId": "owner-mac", "checkoutPath": "/tmp/dam"},
					{"hostId": "pds18-native", "checkoutPath": "/tmp/dam"},
				},
				"execution":   map[string]any{"forge": true},
				"integration": map[string]any{"mode": "pr", "autoMerge": true, "mergeMethod": "squash"},
			},
			"contextFiles": []map[string]any{{"path": "rules.md", "text": "Rules."}},
		},
	}, h, &ignore)

	now := time.Now()
	cap := contract.PublishingHostCapability("gitlab", "gitlab.ufobe.com", "gradus/*")
	putHost := func(id string, logins *[]contract.ForgeLogin) {
		t.Helper()
		payload, _ := json.Marshal(hostPollObservationPayload{
			Capabilities: &[]string{cap},
			ForgeLogins:  logins,
		})
		if err := e.app.store.Write(e.ctx, nil, []string{"host:" + id}, func(q *Request) error {
			return q.db().HostObservationPut(q.ctx(), db.HostObservationPutParams{
				ID:         id,
				Holder:     ptr(testOwnerEmail),
				LastSeenAt: &now,
				Payload:    payload,
			})
		}); err != nil {
			t.Fatal(err)
		}
	}

	// First host in seq order (owner-mac) fails login; second host (pds18-native) has verified ok: true login.
	putHost("owner-mac", &[]contract.ForgeLogin{{Provider: "gitlab", Host: "gitlab.ufobe.com", Ok: false}})
	putHost("pds18-native", &[]contract.ForgeLogin{{Provider: "gitlab", Host: "gitlab.ufobe.com", Ok: true}})

	if err := e.app.store.Write(e.ctx, nil, []string{"project:dam"}, func(q *Request) error {
		q.person = q.peopleSnapshot().person(testOwnerEmail)
		_, target, _, err := resolvePublishingTarget(q, publishingMutationParams{
			action:    "request_merge",
			requestID: "req-verified-1",
			projectID: ptr("dam"),
			number:    ptr(int64(46)),
		})
		if err != nil {
			return err
		}
		if target != "pds18-native" {
			t.Fatalf("targetHost = %q, want pds18-native", target)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Legacy/missing login check on owner-mac (forgeLogins == nil) still loses to verified ok: true on pds18-native.
	putHost("owner-mac", nil)
	if err := e.app.store.Write(e.ctx, nil, []string{"project:dam"}, func(q *Request) error {
		q.person = q.peopleSnapshot().person(testOwnerEmail)
		_, target, _, err := resolvePublishingTarget(q, publishingMutationParams{
			action:    "request_merge",
			requestID: "req-verified-2",
			projectID: ptr("dam"),
			number:    ptr(int64(46)),
		})
		if err != nil {
			return err
		}
		if target != "pds18-native" {
			t.Fatalf("targetHost = %q, want pds18-native over unverified host", target)
		}
		// Explicit hostId overrides ranking.
		_, pinned, _, err := resolvePublishingTarget(q, publishingMutationParams{
			action:    "request_merge",
			requestID: "req-pinned-mac",
			projectID: ptr("dam"),
			number:    ptr(int64(46)),
			hostID:    ptr("owner-mac"),
		})
		if err != nil {
			return err
		}
		if pinned != "owner-mac" {
			t.Fatalf("pinned targetHost = %q, want owner-mac", pinned)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	// Both hosts report ok: true -> owner-mac is tried first, fails, and runPublishingMutation fails over to pds18-native.
	putHost("owner-mac", &[]contract.ForgeLogin{{Provider: "gitlab", Host: "gitlab.ufobe.com", Ok: true}})
	done := make(chan struct{})
	var seenHosts []string
	go func() {
		defer close(done)
		handled := map[string]bool{}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			pubMu.Lock()
			var ids []string
			for id := range pubCmdWaiters {
				if !handled[id] {
					ids = append(ids, id)
				}
			}
			pubMu.Unlock()
			for _, id := range ids {
				var hostID string
				if err := e.ownerStore.pool.QueryRow(e.ctx, `SELECT host_id FROM command WHERE id = $1`, id).Scan(&hostID); err != nil {
					continue
				}
				handled[id] = true
				seenHosts = append(seenHosts, hostID)
				pubMu.Lock()
				if hostID == "owner-mac" {
					pubCmdOutcomes[id] = publishingCmdOutcome{outcome: "failed", errMsg: "project-delivery-unconfirmed: 401"}
				} else {
					pubCmdOutcomes[id] = publishingCmdOutcome{
						outcome: "completed",
						result:  json.RawMessage(`{"requestId":"req-failover","provider":"gitlab","host":"gitlab.ufobe.com","repo":"gradus/dam","branch":"fix/x","kind":"merge_request","number":46,"url":"https://gitlab.ufobe.com/gradus/dam/-/merge_requests/46","requested":true,"autoMergeEnabled":true}`),
					}
				}
				pubMu.Unlock()
				publishingWakeCommand(id)
				if hostID != "owner-mac" {
					return
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	var reply contract.PublishingRequestMergeReply
	e.opInto(t, "publishing_request_merge", map[string]any{
		"requestId": "req-failover",
		"projectId": "dam",
		"number":    46,
	}, h, &reply)
	<-done
	if !reply.Requested || reply.Number != 46 || len(seenHosts) != 2 || seenHosts[0] != "owner-mac" || seenHosts[1] != "pds18-native" {
		t.Fatalf("failover hosts=%v reply=%+v, want [owner-mac pds18-native]", seenHosts, reply)
	}
}
