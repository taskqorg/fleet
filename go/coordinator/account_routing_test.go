package main

import (
	"fleet/contract"
	"fmt"
	"testing"
)

// Requirement: account_hosts and inspection dispatch use one eligible current route.
// Risk: a stale compatibility inspector masks a native host, fallback bypasses provider policy,
// or an observation from an old holder authenticates a different host owner.
func TestAccountInspectionRouting(t *testing.T) {
	env := openTestCoordinator(t, false)
	owner := env.ownerHeaders()
	code, a := env.callJSON(t, "POST", "/api/ops/account_create", doc{"name": "routing-fixture", "harness": "codex"}, owner)
	if code != 200 {
		t.Fatal(code, a)
	}
	account := astr(a["id"])
	cases := []struct {
		name                                                                            string
		nativeFresh, inspectorFresh, legacy, nativeDenied, inspectorDenied, wrongHolder bool
		wantInspector, connectivity                                                     string
		wantCode                                                                        int
	}{
		{name: "fresh-native-stale-inspector", nativeFresh: true, connectivity: "online", wantCode: 200},
		{name: "offline-native-fallback", inspectorFresh: true, wantInspector: "inspector", connectivity: "online", wantCode: 200},
		{name: "both-offline", connectivity: "offline", wantCode: 409},
		{name: "legacy-inspector", legacy: true, inspectorFresh: true, wantInspector: "inspector", connectivity: "online", wantCode: 200},
		{name: "native-provider-denied", nativeFresh: true, inspectorFresh: true, nativeDenied: true, connectivity: "online", wantCode: 409},
		{name: "fallback-provider-denied", inspectorFresh: true, inspectorDenied: true, connectivity: "offline", wantCode: 409},
		{name: "holder-observation-mismatch", nativeFresh: true, wrongHolder: true, connectivity: "unknown", wantCode: 409},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := fmt.Sprintf("routing-%d", i)
			inspector := target + "-inspector"
			reg := func(id string, roles []string, denied bool) string {
				t.Helper()
				b := doc{"id": id, "roles": roles}
				if denied {
					b["harnesses"] = doc{"codex": doc{"allowed": false}}
				}
				c, r := env.callJSON(t, "POST", "/api/ops/hosts_register", b, owner)
				if c != 200 {
					t.Fatal(c, r)
				}
				return astr(r["secret"])
			}
			roles := []string{}
			if tc.legacy {
				roles = []string{"publication"}
			}
			targetSecret := reg(target, roles, tc.nativeDenied)
			inspectorSecret := reg(inspector, []string{"inspector-for:" + target}, tc.inspectorDenied)
			if err := runTx(env.ctx, env.app, nil, true, "", func(q *Request) error {
				for id, fresh := range map[string]bool{target: tc.nativeFresh, inspector: tc.inspectorFresh} {
					at := nowMs() - hostStaleMs - 1000
					if fresh {
						at = nowMs()
					}
					holder := testOwnerEmail
					if id == target && tc.wrongHolder {
						holder = "outsider@trial.test"
					}
					if err := put(q, "host-observation", doc{"id": id, "holder": holder, "platform": "darwin", "lastSeenAt": at, "capabilities": []string{"codex"}}); err != nil {
						return err
					}
				}
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			c, hs := env.callJSON(t, "POST", "/api/ops/account_hosts", doc{"accountId": account}, owner)
			if c != 200 {
				t.Fatal(c, hs)
			}
			var host doc
			for _, v := range hs["hosts"].([]any) {
				h := mapOf(v)
				if h["id"] == target {
					host = h
				}
			}
			if host == nil || host["connectivity"] != tc.connectivity {
				t.Fatalf("host projection %v", host)
			}
			chosen := target
			if tc.wantInspector != "" {
				chosen = inspector
				if host["inspector"] != inspector {
					t.Fatalf("missing fallback %v", host)
				}
			} else if host["inspector"] != nil {
				t.Fatalf("stale/ineligible inspector retained %v", host)
			}
			c, r := env.callJSON(t, "POST", "/api/ops/account_discover", doc{"accountId": account, "hostId": target}, owner)
			if c != tc.wantCode {
				t.Fatalf("discover %d %v", c, r)
			}
			if c != 200 {
				return
			}
			bad, _ := env.callJSON(t, "POST", "/api/ops/account_discover", doc{"accountId": account, "hostId": target, "harness": "claude"}, owner)
			if bad != 400 {
				t.Fatalf("caller overrode account provider: %d", bad)
			}
			secret := targetSecret
			if chosen == inspector {
				secret = inspectorSecret
			}
			poll := contract.PollRequest{HostId: chosen, Platform: contract.Platform("darwin"), Capabilities: []string{"codex"}, Stores: []contract.StoreReport{}, Problems: []contract.EdgeProblem{}}
			c, p := env.callJSON(t, "POST", "/api/hosts/poll", poll, map[string]string{"Authorization": "Bearer " + secret})
			if c != 200 {
				t.Fatal(c, p)
			}
			var command doc
			for _, v := range p["commands"].([]any) {
				x := mapOf(v)
				if x["executionId"] == r["executionId"] {
					command = x
				}
			}
			if command == nil || command["hostId"] != chosen || mapOf(command["payload"])["nativeHostId"] != target || mapOf(command["payload"])["harness"] != "codex" {
				t.Fatalf("route mismatch %v -> %v", host, command)
			}
			reply := doc{"hostId": chosen, "commandId": command["id"], "executionId": command["executionId"], "accountCatalogue": doc{"accountId": "wrong-account", "bindings": []any{}}}
			c, _ = env.callJSON(t, "POST", "/api/hosts/accounts/report", reply, map[string]string{"Authorization": "Bearer " + secret})
			if c != 403 {
				t.Fatalf("foreign account accepted: %d", c)
			}
			outsider := env.ownerHeaders()
			outsider["X-Fleet-Person"] = "outsider@trial.test"
			c, _ = env.callJSON(t, "POST", "/api/ops/account_discover", doc{"accountId": account, "hostId": target}, outsider)
			if c != 403 && c != 404 {
				t.Fatalf("non-holder discovered account: %d", c)
			}
		})
	}
}
