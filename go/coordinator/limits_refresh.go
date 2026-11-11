package main

// subscription_limits_refresh (docs/designs/fleet-people-access.md section 8 "Read without a run").

import "fleet/contract"

func init() {
	operation("subscription_limits_refresh", false, typedOp(subscriptionLimitsRefresh))
}

// subscriptionLimitsRefresh (section 8 "Read without a run"): an account-inspect command in mode limits to one host
// with a ready binding of each subscription; the host's vendor CLI answers with a probe limit_reading on that
// execution. The holder or the owner may ask; Fleet sets no refresh interval.
func subscriptionLimitsRefresh(q *Request, req *contract.SubscriptionLimitsRefreshRequest) (contract.SubscriptionLimitsRefreshReply, error) {
	reply := contract.SubscriptionLimitsRefreshReply{Sent: []contract.LimitRefreshSent{}, Skipped: []contract.LimitRefreshSkipped{}}
	accounts, err := all(q, "managed-account", nil)
	if err != nil {
		return reply, err
	}
	mine := func(a doc) bool { return q.person == nil || fullAccess(q.person) || q.person.Email == a["holder"] }
	var targets []doc
	for _, a := range accounts {
		if req.SubscriptionId != nil && a["id"] != *req.SubscriptionId {
			continue
		}
		if !mine(a) {
			if req.SubscriptionId != nil {
				return reply, fail(403, "Only the holder or the owner refreshes this subscription")
			}
			continue
		}
		targets = append(targets, a)
	}
	if req.SubscriptionId != nil && len(targets) == 0 {
		return reply, fail(404, "Unknown subscription")
	}
	obs, err := all(q, "host-observation", nil)
	if err != nil {
		return reply, err
	}
	seen := map[string]float64{}
	for _, o := range obs {
		seen[astr(o["id"])] = num(o["lastSeenAt"])
	}
	for _, a := range targets {
		id := astr(a["id"])
		var host, binding string
		for _, b := range bindingsOf(a) {
			h := astr(b["hostId"])
			if b["readiness"] == "ready" && float64(nowMs())-seen[h] < 30000 {
				host, binding = h, astr(b["binding"])
				break
			}
		}
		if host == "" {
			reply.Skipped = append(reply.Skipped, contract.LimitRefreshSkipped{SubscriptionId: id, Reason: "no online host with a ready binding"})
			continue
		}
		sid, eid, mode := newID(), newID(), "limits"
		if err := put(q, "session", doc{"id": sid, "createdAt": nowMs(), "actor": "program-worker"}); err != nil {
			return reply, err
		}
		if err := put(q, "execution", doc{"id": eid, "executionId": eid, "sessionId": sid, "hostId": host, "state": "preparing", "createdAt": nowMs(), "actor": "program-worker", "accountId": id}); err != nil {
			return reply, err
		}
		if err := put(q, "command", doc{"id": newID(), "executionId": eid, "sessionId": sid, "hostId": host, "kind": string(contract.CommandKindAccountInspect), "status": "pending",
			"payload": contract.AccountInspectPayload{AccountId: id, Harness: astr(a["harness"]), Binding: &binding, NativeHostId: &host, Mode: &mode}}); err != nil {
			return reply, err
		}
		reply.Sent = append(reply.Sent, contract.LimitRefreshSent{SubscriptionId: id, HostId: host, Binding: binding, ExecutionId: eid})
	}
	return reply, nil
}
