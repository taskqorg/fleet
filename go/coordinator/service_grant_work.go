package main

import (
	"context"
	"errors"
	"time"

	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

const serviceGrantReadbackInterval = time.Minute

type serviceGrantWorkResult struct {
	sequence    int64
	observation *serviceGrantObservation
	refresh     *serviceGrantRefresh
}

func serviceGrantDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rows, err := q.db().ServiceGrantDue(q.ctx(), db.ServiceGrantDueParams{Due: tstz(now), Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rows))
	for _, g := range rows {
		out = append(out, &kitClaim{Key: g.Name, Attempts: int(g.Attempts), Guard: []any{g.CheckSequence}})
	}
	return out, err
}

func serviceGrantStep(q *Request, _ time.Time, c *kitClaim) error {
	g, err := q.db().ServiceGrantLock(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return errKitSkip
	}
	if err != nil {
		return err
	}
	now := time.Now()
	if c.Result == nil {
		if g.TokenID == nil || g.ApprovedAt == nil || g.CheckSequence != c.Guard[0] || g.NextAttemptAt.After(now) {
			return errKitSkip
		}
		if err := q.db().ServiceGrantBeginCheck(q.ctx(), db.ServiceGrantBeginCheckParams{Name: c.Key, CheckStartedAt: &now}); err != nil {
			return err
		}
		g.CheckSequence++
		return serviceGrantObserveCall(q, g, now, c)
	}

	result := c.Result.(serviceGrantWorkResult)
	if g.CheckSequence != result.sequence || g.CheckStartedAt == nil || !g.NextAttemptAt.After(now) {
		return errKitSkip
	}
	c.Guard = []any{result.sequence}
	attempt, active, err := serviceGrantActive(q, g.Name)
	if err != nil {
		return err
	}
	if result.refresh != nil {
		return serviceGrantRecordPost(q, g, attempt, now, *result.refresh, c)
	}
	return serviceGrantRecordObservation(q, g, attempt, active, now, *result.observation, c)
}

func serviceGrantObserveCall(q *Request, g db.ServiceGrant, now time.Time, c *kitClaim) error {
	if err := kitSchedule(q, serviceGrantQueue, g.Name, now.Add(serviceGrantLease)); err != nil {
		return err
	}
	app := q.app
	c.Call = func(parent context.Context) any {
		ctx, cancel := context.WithDeadline(parent, now.Add(serviceGrantLease/2))
		defer cancel()
		peopleErr := app.refreshPeople(ctx)
		cf, err := cloudflareAccountFromEnv()
		var observation serviceGrantObservation
		if err != nil {
			observation.provider = "Fleet's protected Cloudflare account settings are unavailable"
		} else {
			observation = serviceGrantObserveProvider(ctx, cf, g)
		}
		if peopleErr != nil {
			observation.binding = "Fleet could not refresh its public machine binding"
		}
		return serviceGrantWorkResult{sequence: g.CheckSequence, observation: &observation}
	}
	return nil
}

func serviceGrantRecordObservation(q *Request, g db.ServiceGrant, a db.ServiceGrantRenewal, active bool, now time.Time, obs serviceGrantObservation, c *kitClaim) error {
	d := q.db()
	clientID := firstString(obs.token.ClientID, deref(g.ClientID))
	var binding *string
	if obs.binding != "" {
		binding = &obs.binding
	} else if clientID != "" {
		problem, err := serviceGrantBinding(q, g, clientID)
		if err != nil {
			return err
		}
		binding = &problem
	}
	providerVerified := obs.provider == ""
	var renewalResult string
	if providerVerified && active && a.DispatchedAt != nil {
		renewalResult, obs.provider = serviceGrantRenewalResult(a, obs.token)
		providerVerified = obs.provider == ""
	}
	adopted := providerVerified && binding != nil && *binding == ""
	observed := db.ServiceGrantObserveParams{Name: g.Name, CheckedAt: &now, ProviderCheck: &obs.provider, BindingCheck: binding, Adopted: adopted}
	if providerVerified {
		observed.ClientID, observed.ExpiresAt, observed.ProviderLastSeenAt = &obs.token.ClientID, tstz(obs.token.ExpiresAt), tsOf(obs.token.LastSeenAt)
	}
	if err := d.ServiceGrantObserve(q.ctx(), observed); err != nil {
		return err
	}

	if active && a.DispatchedAt != nil {
		return serviceGrantReadback(q, g, a, now, obs, adopted, binding, renewalResult)
	}
	expiry := g.ExpiresAt
	if providerVerified {
		expiry = &obs.token.ExpiresAt
	}
	if !active && !g.Paused && g.AdoptedAt != nil && expiry != nil &&
		expiry.After(now) && !expiry.After(now.Add(serviceGrantRenewBefore)) {
		a = db.ServiceGrantRenewal{GrantName: g.Name, RequestID: "automatic:" + newID()}
		if err := d.ServiceGrantRequestRenewal(q.ctx(), db.ServiceGrantRequestRenewalParams{
			GrantName: g.Name, RequestID: a.RequestID, RequestedBy: "fleet"}); err != nil {
			return err
		}
		active = true
	}
	if !active || g.Paused {
		return serviceGrantFinishCheck(q, g.Name, now.Add(serviceGrantInterval))
	}
	if !adopted || !obs.token.ExpiresAt.After(now) {
		detail := firstString(obs.provider, deref(binding), "The token has expired. The owner must review recovery.")
		if err := serviceGrantAckRenewal(q, a, nil, detail); err != nil {
			return err
		}
		return serviceGrantFinishCheck(q, g.Name, now.Add(serviceGrantInterval))
	}
	if err := d.ServiceGrantDispatchRenewal(q.ctx(), db.ServiceGrantDispatchRenewalParams{
		GrantName: g.Name, RequestID: a.RequestID, DispatchedAt: &now, BaselineExpiresAt: &obs.token.ExpiresAt,
		BaselineSecretVersion: obs.token.SecretVersion}); err != nil {
		return err
	}
	if err := kitSchedule(q, serviceGrantQueue, g.Name, now.Add(serviceGrantLease)); err != nil {
		return err
	}
	c.Call = func(parent context.Context) any {
		ctx, cancel := context.WithDeadline(parent, now.Add(serviceGrantLease/2))
		defer cancel()
		result := serviceGrantRefreshToken(ctx, obs.cf, obs.token)
		return serviceGrantWorkResult{sequence: g.CheckSequence, refresh: &result}
	}
	return nil
}

func serviceGrantRecordPost(q *Request, g db.ServiceGrant, a db.ServiceGrantRenewal, now time.Time, post serviceGrantRefresh, c *kitClaim) error {
	if a.DispatchedAt == nil {
		return errKitSkip
	}
	if post.refused {
		if err := serviceGrantFinishRenewal(q, g, a, "refused", post.detail, nil); err != nil {
			return err
		}
		return serviceGrantFinishCheck(q, g.Name, now.Add(serviceGrantInterval))
	}
	if err := serviceGrantAckRenewal(q, a, post.expires, post.detail); err != nil {
		return err
	}
	// The response alone never proves renewal. A lost result or process restart also enters readback here.
	return serviceGrantObserveCall(q, g, now, c)
}

func serviceGrantReadback(q *Request, g db.ServiceGrant, a db.ServiceGrantRenewal, now time.Time, obs serviceGrantObservation, adopted bool, binding *string, result string) error {
	if adopted {
		if err := serviceGrantFinishRenewal(q, g, a, result, "", &obs.token.ExpiresAt); err != nil {
			return err
		}
		return serviceGrantFinishCheck(q, g.Name, now.Add(serviceGrantInterval))
	}
	detail := firstString(obs.provider, deref(binding), "The Fleet binding is unverified")
	if err := serviceGrantAckRenewal(q, a, a.AcknowledgedExpiry, detail); err != nil {
		return err
	}
	delay := serviceGrantReadbackInterval
	if now.Sub(*a.DispatchedAt) > 10*serviceGrantReadbackInterval {
		delay = serviceGrantInterval
	}
	return serviceGrantFinishCheck(q, g.Name, now.Add(delay))
}

func serviceGrantAckRenewal(q *Request, a db.ServiceGrantRenewal, expiry *time.Time, detail string) error {
	return q.db().ServiceGrantAckRenewal(q.ctx(), db.ServiceGrantAckRenewalParams{GrantName: a.GrantName, RequestID: a.RequestID,
		AcknowledgedExpiry: expiry, Detail: strPtr(detail)})
}

func serviceGrantFinishRenewal(q *Request, g db.ServiceGrant, a db.ServiceGrantRenewal, result, detail string, expiry *time.Time) error {
	if err := q.db().ServiceGrantFinishRenewal(q.ctx(), db.ServiceGrantFinishRenewalParams{GrantName: a.GrantName, RequestID: a.RequestID,
		Result: &result, Detail: strPtr(detail), VerifiedExpiry: expiry}); err != nil {
		return err
	}
	return audit(q, "service-grant:"+g.Name, "renewal", a.RequestID, result, auditExtra{ProjectID: &g.ProjectID, Reason: detail})
}

func serviceGrantFinishCheck(q *Request, name string, next time.Time) error {
	if err := q.db().ServiceGrantFinishCheck(q.ctx(), name); err != nil {
		return err
	}
	return kitSchedule(q, serviceGrantQueue, name, next)
}
