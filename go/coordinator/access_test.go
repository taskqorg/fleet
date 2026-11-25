package main

import "testing"

// Requirement (System › Access, approved 2026-11-25): access_status lists every subscription and key with its state in
// plain words and the next step; access_onboard creates a missing connection and counts a monthly limit; account_delete
// deletes only a subscription nothing uses. Risk: an owner sees "fine" for a key that has none, or loses an account in use.
func TestAccessStatusOnboardDelete(t *testing.T) {
	env := openTestCoordinator(t, false)
	owner := env.ownerHeaders()
	call := func(op string, body doc, want int) doc {
		t.Helper()
		code, r := env.callJSON(t, "POST", "/api/ops/"+op, body, owner)
		if code != want {
			t.Fatalf("%s: %d %v", op, code, r)
		}
		return r
	}
	item := func(s doc, id string) doc {
		t.Helper()
		for _, x := range s["items"].([]any) {
			if it := x.(doc); it["id"] == id {
				return it
			}
		}
		t.Fatalf("no item %s in %v", id, s)
		return nil
	}
	a := call("account_create", doc{"name": "empty-codex", "harness": "codex"}, 200)
	id := astr(a["id"])

	s := call("access_status", doc{}, 200)
	if it := item(s, "account:"+id); it["state"] != "needs-login" || it["group"] != "subscription" || it["kind"] != "ChatGPT login" {
		t.Fatal(it)
	}
	if it := item(s, "provider:openai"); it["state"] != "not-added" || it["next"].(doc)["action"] != "add-connection" {
		t.Fatal(it)
	}

	o := call("access_onboard", doc{"kind": "key", "provider": "openai", "connection": "openai-work"}, 200)
	if o["created"] != true || o["connection"] != "openai-work" || len(o["steps"].([]any)) < 2 {
		t.Fatal(o)
	}
	s = call("access_status", doc{}, 200)
	if it := item(s, "connection:openai-work"); it["state"] != "needs-key" || it["where"] != "all projects" {
		t.Fatal(it)
	}
	l := call("access_onboard", doc{"kind": "limit", "connection": "openai-work", "limit": doc{"amount": 200, "unit": "calls"}}, 200)
	again := call("access_onboard", doc{"kind": "limit", "connection": "openai-work", "limit": doc{"amount": 300, "unit": "calls"}}, 200)
	if l["budgetId"] == nil || again["budgetId"] != l["budgetId"] {
		t.Fatal("a second limit must replace the first", l, again)
	}
	s = call("access_status", doc{}, 200)
	if lim := item(s, "connection:openai-work")["limit"].(doc); lim["budget"].(doc)["limit"] != float64(300) {
		t.Fatal(lim)
	}

	if p := call("account_delete", doc{"accountId": id, "preview": true}, 200); p["deleted"] != false || p["refused"] != nil {
		t.Fatal(p)
	}
	if d := call("account_delete", doc{"accountId": id}, 200); d["deleted"] != true {
		t.Fatal(d)
	}
	call("account_delete", doc{"accountId": id}, 404)
}
