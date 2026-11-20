package main

// subscription_limits_refresh (docs/designs/fleet-people-access.md section 8 "Read without a run").

import (
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	op("subscription_limits_refresh", subscriptionLimitsRefresh)
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
			continue // another person's subscription is not found, the same as an unknown id
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
		hid := astr(o["id"])
		seen[hid] = q.app.effectiveHostSeen(hid, num(o["lastSeenAt"]))
	}
	for _, a := range targets {
		id := astr(a["id"])
		// A read already on its way is the answer to a repeated refresh.
		inflight, err := q.db().InflightLimitRead(q.ctx(), db.InflightLimitReadParams{Kind: string(contract.CommandKindAccountInspect), AccountID: id})
		if err == nil {
			var p struct {
				Payload struct {
					Binding string `json:"binding"`
				} `json:"payload"`
			}
			_ = json.Unmarshal(inflight.Payload, &p)
			reply.Sent = append(reply.Sent, contract.LimitRefreshSent{SubscriptionId: id, HostId: deref(inflight.HostID), Binding: p.Payload.Binding, ExecutionId: deref(inflight.ExecutionID)})
			continue
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return reply, err
		}
		var host, binding, reason string
		reason = "no binding on any host; connect the subscription first"
		for _, b := range bindingsOf(a) {
			h := astr(b["hostId"])
			allowed, err := harnessAllowed(q, h, astr(a["harness"]))
			switch {
			case err != nil:
				return reply, err
			case b["readiness"] != "ready":
				reason = "binding " + astr(b["binding"]) + " on " + h + " is not ready; login needed"
				continue
			case !allowed:
				reason = astr(a["harness"]) + " is not allowed on host " + h
				continue
			case float64(nowMs())-seen[h] >= onlineMs:
				reason = "host " + h + " is offline"
				continue
			}
			host, binding = h, astr(b["binding"])
			break
		}
		if host == "" {
			reply.Skipped = append(reply.Skipped, contract.LimitRefreshSkipped{SubscriptionId: id, Reason: reason})
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
