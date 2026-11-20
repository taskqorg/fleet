package main

// subscription_limits_refresh (docs/designs/fleet-people-access.md section 8 "Read without a run").

import (
	"encoding/json"
	"errors"
	"math"

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
	accounts, err := accountsAll(q)
	if err != nil {
		return reply, err
	}
	mine := func(a account) bool {
		return q.person == nil || fullAccess(q.person) || a.Holder != nil && q.person.Email == *a.Holder
	}
	var targets []account
	for _, a := range accounts {
		if req.SubscriptionId != nil && a.ID != *req.SubscriptionId {
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
	obs, err := q.db().AccountHostSeen(q.ctx())
	if err != nil {
		return reply, err
	}
	seen := map[string]float64{}
	for _, o := range obs {
		var at float64
		if o.LastSeenAt != nil {
			at = math.Round(float64(o.LastSeenAt.UnixMicro()) / 1000)
		}
		seen[o.ID] = q.app.effectiveHostSeen(o.ID, at)
	}
	for _, a := range targets {
		id, harness := a.ID, deref(a.Harness)
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
		for _, b := range a.Bindings {
			h := deref(b.HostID)
			allowed, err := harnessAllowed(q, h, harness)
			switch {
			case err != nil:
				return reply, err
			case deref(b.Readiness) != "ready":
				reason = "binding " + deref(b.Binding) + " on " + h + " is not ready; login needed"
				continue
			case !allowed:
				reason = harness + " is not allowed on host " + h
				continue
			case float64(nowMs())-seen[h] >= onlineMs:
				reason = "host " + h + " is offline"
				continue
			}
			host, binding = h, deref(b.Binding)
			break
		}
		if host == "" {
			reply.Skipped = append(reply.Skipped, contract.LimitRefreshSkipped{SubscriptionId: id, Reason: reason})
			continue
		}
		mode := "limits"
		eid, err := accountInspectionJob(q, host, contract.AccountInspectPayload{AccountId: id, Harness: harness, Binding: &binding, NativeHostId: &host, Mode: &mode})
		if err != nil {
			return reply, err
		}
		reply.Sent = append(reply.Sent, contract.LimitRefreshSent{SubscriptionId: id, HostId: host, Binding: binding, ExecutionId: eid})
	}
	return reply, nil
}
