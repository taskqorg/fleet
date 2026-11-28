package main

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"fleet/contract"
	"fleet/coordinator/db"

	"github.com/jackc/pgx/v5"
)

const (
	serviceGrantQueue       = "service-grant"
	serviceGrantApproval    = "service-grant"
	serviceGrantInterval    = time.Hour
	serviceGrantRenewBefore = 30 * 24 * time.Hour
)

var serviceGrantName = regexp.MustCompile(`^fleet-machine-[a-z0-9][a-z0-9-]{0,47}$`)

func init() {
	op("service_grant_ask", serviceGrantAsk)
	op("service_grant_enroll", serviceGrantEnroll)
	op("service_grant_status", serviceGrantStatus)
	op("service_grant_maintain", serviceGrantMaintain)
	addProjectOps("service_grant_ask", "service_grant_status")
	kitRegister(&kitQueue{Name: serviceGrantQueue, Period: 10 * time.Second, Limit: 1, Due: serviceGrantDue, Step: serviceGrantStep,
		Retry: kitRetry{Table: "service_grant", Key: "name", Guard: `service_grant.check_sequence=$5::bigint`,
			Class: func(error) string { return "Fleet could not save reconciliation. The worker will retry." }}})
}

func serviceGrantScopeValid(project, name string) bool {
	if name == designVerifierIdentity {
		return project == contract.ProductionBrowserProject
	}
	return idPattern.MatchString(project) && serviceGrantName.MatchString(name)
}

func serviceGrantHumanOwner(q *Request) error {
	if q.agent != "" || q.person == nil || !q.person.Owner || q.person.machine() {
		return fail(403, "Only the Fleet owner may manage Cloudflare service grants")
	}
	return nil
}

func serviceGrantRead(q *Request, project, name string) (db.ServiceGrant, error) {
	if !serviceGrantScopeValid(project, name) {
		return db.ServiceGrant{}, fail(400, "Expected a supported service grant name and one project")
	}
	g, err := q.db().ServiceGrantGet(q.ctx(), db.ServiceGrantGetParams{Name: name, ProjectID: project})
	if errors.Is(err, pgx.ErrNoRows) {
		err = fail(404, "Unknown service grant")
	}
	return g, err
}

func serviceGrantStatus(q *Request, r *contract.ServiceGrantStatusRequest) (contract.ServiceGrantStatusReply, error) {
	g, err := serviceGrantRead(q, r.ProjectId, r.Name)
	if err != nil {
		return contract.ServiceGrantStatusReply{}, err
	}
	return serviceGrantView(q, g, r.RequestId)
}

func serviceGrantView(q *Request, g db.ServiceGrant, requestID *string) (contract.ServiceGrantStatusReply, error) {
	now := time.Now()
	out := serviceGrantViewOf(g, now)
	if g.Name == designVerifierIdentity && g.ClientID != nil {
		if err := serviceGrantVerifierReadiness(q, g, now, &out); err != nil {
			return out, err
		}
	}
	var a db.ServiceGrantRenewal
	var err error
	if requestID == nil {
		a, err = q.db().ServiceGrantLatestRenewal(q.ctx(), g.Name)
		if errors.Is(err, pgx.ErrNoRows) {
			return out, nil
		}
	} else {
		a, err = q.db().ServiceGrantGetRenewal(q.ctx(), db.ServiceGrantGetRenewalParams{GrantName: g.Name, RequestID: *requestID})
		if errors.Is(err, pgx.ErrNoRows) {
			return out, fail(404, "Unknown service grant renewal request")
		}
	}
	if err != nil {
		return out, err
	}
	result := deref(a.Result)
	if result == "" {
		result = "queued"
		if a.DispatchedAt != nil {
			result = "awaiting_readback"
		}
	}
	out.Renewal = &contract.ServiceGrantRenewal{RequestId: a.RequestID, RequestedBy: a.RequestedBy,
		RequestedAt: a.RequestedAt.UnixMilli(), Result: result, BaselineExpiry: msOfTime(a.BaselineExpiresAt),
		AcknowledgedExpiry: msOfTime(a.AcknowledgedExpiry), VerifiedExpiry: msOfTime(a.VerifiedExpiry),
		DispatchedAt: msOfTime(a.DispatchedAt), CompletedAt: msOfTime(a.CompletedAt), Detail: a.Detail}
	return out, nil
}

// A design verifier heartbeat proves that the dedicated unit authenticated to Fleet. A heartbeat
// can prove Access even when that unit reports an unavailable GitHub provider.
func serviceGrantVerifierReadiness(q *Request, g db.ServiceGrant, now time.Time, out *contract.ServiceGrantStatusReply) error {
	rows, err := q.db().DesignVerifierLatest(q.ctx())
	if err != nil {
		return err
	}
	var last time.Time
	for _, row := range rows {
		if (row.Field == "provider-ready" || row.Field == "provider-unknown") && row.At.After(last) {
			last = row.At
		}
	}
	if last.IsZero() {
		return nil
	}
	out.LastServiceAuthenticatedAt = msOfTime(&last)
	configured := q.peopleSnapshot().person(designVerifierIdentity)
	if out.BindingReadiness == "verified" && out.ProviderReadiness == "verified" &&
		designVerifierPerson(configured) && configured.AccessCommonName == *g.ClientID &&
		!last.Before(g.ExpiresAt.Add(-serviceGrantDuration)) && !last.After(now) && now.Sub(last) <= designVerifierObservationMaxAge {
		out.ServiceReadiness = "verified"
	}
	return nil
}

func serviceGrantViewOf(g db.ServiceGrant, now time.Time) contract.ServiceGrantStatusReply {
	out := contract.ServiceGrantStatusReply{
		ProjectId: g.ProjectID, Name: g.Name, Role: "viewer", Application: serviceGrantDomain, Audience: contract.FleetAccessAudience,
		DurationHours: int64(serviceGrantDuration / time.Hour), Enrollment: "awaiting_approval", Paused: g.Paused,
		ProviderReadiness: "unknown", BindingReadiness: "unknown", ServiceReadiness: "unknown", QuestionId: g.QuestionID,
		AccountId: g.AccountID, ApplicationId: g.ApplicationID, PolicyId: g.PolicyID,
		ServiceTokenId: g.TokenID, ClientId: g.ClientID, EnrollmentReference: g.EnrollmentReference,
		LastCheckedAt: msOfTime(g.CheckedAt), LastConfirmedExpiry: msOfTime(g.ExpiresAt), ProviderLastSeenAt: msOfTime(g.ProviderLastSeenAt),
		ProviderProblem: nonEmpty(g.ProviderCheck), BindingProblem: nonEmpty(g.BindingCheck), ReconcileError: g.LastError,
	}
	switch {
	case g.DeclinedAt != nil:
		out.Enrollment = "declined"
	case g.AdoptedAt != nil:
		out.Enrollment = "adopted"
	case g.TokenID != nil:
		out.Enrollment = "reconciling"
	case g.ApprovedAt != nil:
		out.Enrollment = "awaiting_operator_enrollment"
	}
	if g.TokenID == nil {
		return out
	}
	out.NextCheckAt = msOfTime(&g.NextAttemptAt)
	if g.CheckedAt == nil || g.CheckStartedAt != nil || g.LastError != nil || now.Sub(*g.CheckedAt) > 2*serviceGrantInterval {
		return out
	}
	out.ProviderReadiness = serviceGrantReadiness(g.ProviderCheck)
	out.BindingReadiness = serviceGrantReadiness(g.BindingCheck)
	if out.ProviderReadiness == "verified" && (g.ExpiresAt == nil || !g.ExpiresAt.After(now)) {
		out.ProviderReadiness = "expired"
	}
	return out
}

func serviceGrantReadiness(problem *string) string {
	if problem == nil {
		return "unknown"
	}
	if *problem != "" {
		return "unverified"
	}
	return "verified"
}

func serviceGrantAsk(q *Request, r *contract.ServiceGrantAskRequest) (contract.ServiceGrantAskReply, error) {
	var out contract.ServiceGrantAskReply
	if !serviceGrantScopeValid(r.ProjectId, r.Name) || r.Name == designVerifierIdentity ||
		!serviceGrantReceipt.MatchString(r.RequestId) || strings.TrimSpace(r.Reason) == "" || len(r.Reason) > 500 {
		return out, fail(400, "Expected a fleet-machine- name, one project and a reason")
	}
	if isViewer(q.person) {
		return out, fail(403, "Project viewers may only read")
	}
	if _, err := projectSettingsByID(q, r.ProjectId); err != nil {
		return out, err
	}
	existing, err := q.db().ServiceGrantLock(q.ctx(), r.Name)
	switch {
	case err == nil && existing.ProjectID == r.ProjectId && existing.RequestID == r.RequestId:
		return serviceGrantView(q, existing, nil)
	case err == nil:
		return out, fail(409, "The machine name already has a Fleet grant request")
	case !errors.Is(err, pgx.ErrNoRows):
		return out, err
	}

	scope := approvalScope{RequestID: "service-grant:" + r.Name, Action: serviceGrantApproval, Risk: "radical",
		What:  fmt.Sprintf("One-year service token %s for the Fleet viewer in project %s. %s", r.Name, r.ProjectId, strings.TrimSpace(r.Reason)),
		Where: serviceGrantDomain, Rollback: "Pause Fleet renewal and have the trusted operator revoke the exact provider binding when access must end."}
	ask := questionAsk{Kind: "operator_decision", ProjectID: r.ProjectId,
		Text:    approvalText(scope, approverNames(approvers(q, r.ProjectId, serviceGrantApproval))),
		AskedBy: ptr(actor(q, "operator")), RequestID: &scope.RequestID, DirectToOwner: true,
		Options: jsonBytes([]string{approvalYes, approvalNo}), Data: jsonBytes(struct {
			Approval approvalScope `json:"approval"`
		}{scope})}
	if eid, ok := strings.CutPrefix(q.agent, "execution:"); ok {
		sid, err := q.db().ConnExecutionSession(q.ctx(), eid)
		if err != nil {
			return out, err
		}
		ask.ExecutionID, ask.SessionID = &eid, sid
	}
	qid, err := askQuestionAs(q, ask)
	if err != nil {
		return out, err
	}
	n, err := q.db().ServiceGrantAsk(q.ctx(), db.ServiceGrantAskParams{Name: r.Name, ProjectID: r.ProjectId,
		RequestID: r.RequestId, QuestionID: &qid, AskedBy: *ask.AskedBy})
	if err != nil {
		return out, err
	}
	if n != 1 {
		return out, fail(409, "The machine name already has a Fleet grant request")
	}
	g, err := serviceGrantRead(q, r.ProjectId, r.Name)
	if err != nil {
		return out, err
	}
	return serviceGrantView(q, g, nil)
}

func serviceGrantQuestionEnded(q *Request, id string, ev questionEvent) error {
	if ev.To != "answered" && ev.To != "withdrawn" {
		return nil
	}
	return q.db().ServiceGrantDecide(q.ctx(), db.ServiceGrantDecideParams{QuestionID: &id,
		Approved: ev.To == "answered" && deref(ev.Answer) == approvalYes})
}

func serviceGrantEnroll(q *Request, r *contract.ServiceGrantEnrollRequest) (contract.ServiceGrantEnrollReply, error) {
	var out contract.ServiceGrantEnrollReply
	if err := serviceGrantHumanOwner(q); err != nil {
		return out, err
	}
	if !serviceGrantScopeValid(r.ProjectId, r.Name) || !serviceGrantUUID.MatchString(r.ApplicationId) ||
		!serviceGrantUUID.MatchString(r.PolicyId) || !serviceGrantUUID.MatchString(r.ServiceTokenId) ||
		!serviceGrantReceipt.MatchString(r.EnrollmentReference) {
		return out, fail(400, "Expected an eligible grant, three Cloudflare UUIDs and a nonsecret receipt reference")
	}
	if _, err := projectSettingsByID(q, r.ProjectId); err != nil {
		return out, err
	}
	account := strings.ToLower(strings.TrimSpace(os.Getenv("FLEET_CLOUDFLARE_ACCESS_ACCOUNT_ID")))
	if !cloudflareAccountID.MatchString(account) {
		return out, fail(409, "Fleet's Cloudflare account is unconfigured")
	}
	d := q.db()
	if r.Name == designVerifierIdentity {
		if err := d.ServiceGrantBootstrap(q.ctx(), db.ServiceGrantBootstrapParams{Name: r.Name, ProjectID: r.ProjectId,
			RequestID: r.EnrollmentReference, AskedBy: q.person.Email}); err != nil {
			return out, err
		}
	}
	g, err := d.ServiceGrantLock(q.ctx(), r.Name)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && g.ProjectID != r.ProjectId {
		return out, fail(404, "Request approval for this machine and project first")
	}
	if err != nil {
		return out, err
	}
	if g.ApprovedAt == nil {
		return out, fail(409, "The owner has not approved this machine grant")
	}
	if g.TokenID != nil {
		if deref(g.AccountID) == account && deref(g.ApplicationID) == r.ApplicationId && deref(g.PolicyID) == r.PolicyId &&
			*g.TokenID == r.ServiceTokenId && deref(g.EnrollmentReference) == r.EnrollmentReference {
			return serviceGrantView(q, g, nil)
		}
		return out, fail(409, "Enrollment already pins this grant to its existing provider objects")
	}
	err = d.ServiceGrantEnroll(q.ctx(), db.ServiceGrantEnrollParams{Name: r.Name, AccountID: &account, ApplicationID: &r.ApplicationId,
		TokenID: &r.ServiceTokenId, PolicyID: &r.PolicyId, EnrollmentReference: &r.EnrollmentReference})
	if pgCode(err) == "23505" {
		return out, fail(409, "A service grant already uses that policy or token")
	}
	if err != nil {
		return out, err
	}
	if err := audit(q, "service-grant:"+g.Name, "enrolled", nil, r.EnrollmentReference, auditExtra{ProjectID: &g.ProjectID}); err != nil {
		return out, err
	}
	if err := serviceGrantQueueNow(q, g.Name); err != nil {
		return out, err
	}
	g, err = d.ServiceGrantLock(q.ctx(), r.Name)
	if err != nil {
		return out, err
	}
	return serviceGrantView(q, g, nil)
}

var serviceGrantReceipt = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._:/-]{0,199}$`)

func serviceGrantMaintain(q *Request, r *contract.ServiceGrantMaintainRequest) (contract.ServiceGrantMaintainReply, error) {
	var out contract.ServiceGrantMaintainReply
	if err := serviceGrantHumanOwner(q); err != nil {
		return out, err
	}
	g, err := q.db().ServiceGrantLock(q.ctx(), r.Name)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && g.ProjectID != r.ProjectId {
		return out, fail(404, "Unknown service grant")
	}
	if err != nil {
		return out, err
	}
	if g.TokenID == nil {
		return out, fail(409, "Enroll the approved provider objects first")
	}
	d := q.db()
	act := string(r.Action)
	now := time.Now()
	var attemptID *string
	switch act {
	case "renew":
		attemptID = r.RequestId
		if !serviceGrantReceipt.MatchString(deref(attemptID)) || strings.HasPrefix(*attemptID, "automatic:") {
			return out, fail(400, "Expected a stable manual renew requestId")
		}
		if _, err := d.ServiceGrantGetRenewal(q.ctx(), db.ServiceGrantGetRenewalParams{GrantName: g.Name, RequestID: *attemptID}); err == nil {
			return serviceGrantView(q, g, attemptID)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if g.Paused || g.AdoptedAt == nil {
			return out, fail(409, "The grant must be adopted and active to queue renewal")
		}
		if g.ExpiresAt != nil && !g.ExpiresAt.After(now) {
			return out, fail(409, "The last confirmed token has expired. Check the grant and review recovery.")
		}
		if _, found, err := serviceGrantActive(q, g.Name); err != nil {
			return out, err
		} else if found {
			return out, fail(409, "Another renewal is pending. Read its retained request before retrying.")
		}
		if err := d.ServiceGrantRequestRenewal(q.ctx(), db.ServiceGrantRequestRenewalParams{
			GrantName: g.Name, RequestID: *attemptID, RequestedBy: q.person.Email}); err != nil {
			return out, err
		}
	case "pause", "resume":
		if err := d.ServiceGrantPause(q.ctx(), db.ServiceGrantPauseParams{Name: g.Name, Paused: act == "pause"}); err != nil {
			return out, err
		}
		if g.Paused != (act == "pause") {
			if err := audit(q, "service-grant:"+g.Name, "renewalPaused", g.Paused, !g.Paused, auditExtra{ProjectID: &g.ProjectID}); err != nil {
				return out, err
			}
		}
	case "close_unconfirmed":
		attemptID = r.RequestId
		if !serviceGrantReceipt.MatchString(deref(attemptID)) {
			return out, fail(400, "Name the exact pending renewal request")
		}
		closed, err := d.ServiceGrantGetRenewal(q.ctx(), db.ServiceGrantGetRenewalParams{GrantName: g.Name, RequestID: *attemptID})
		if err == nil && deref(closed.Result) == "closed_by_owner" {
			return serviceGrantView(q, g, attemptID)
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if !g.Paused || (g.CheckStartedAt != nil && g.NextAttemptAt.After(now)) {
			return out, fail(409, "Pause the grant and wait for in-flight reconciliation before closing an attempt")
		}
		a, found, err := serviceGrantActive(q, g.Name)
		if err != nil {
			return out, err
		}
		if !found || a.RequestID != deref(r.RequestId) {
			return out, fail(409, "Name the exact pending renewal request")
		}
		if err := serviceGrantFinishRenewal(q, g, a, "closed_by_owner", "Owner closed the pending request after checking provider activity", nil); err != nil {
			return out, err
		}
	case "check":
	default:
		return out, fail(400, "Unknown maintenance action")
	}
	if g.CheckStartedAt == nil || !g.NextAttemptAt.After(now) {
		if err := serviceGrantQueueNow(q, g.Name); err != nil {
			return out, err
		}
	}
	g, err = d.ServiceGrantLock(q.ctx(), g.Name)
	if err != nil {
		return out, err
	}
	return serviceGrantView(q, g, attemptID)
}

func serviceGrantQueueNow(q *Request, name string) error {
	if err := kitSchedule(q, serviceGrantQueue, name, time.Now()); err != nil {
		return err
	}
	return kitNotify(q, serviceGrantQueue)
}

func serviceGrantActive(q *Request, name string) (db.ServiceGrantRenewal, bool, error) {
	a, err := q.db().ServiceGrantActiveRenewal(q.ctx(), name)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, false, nil
	}
	return a, err == nil, err
}

func serviceGrantBinding(q *Request, g db.ServiceGrant, clientID string) (string, error) {
	p := q.app.peopleSnapshot().person(g.Name)
	if p == nil {
		return "The operator has not registered the machine viewer in Fleet", nil
	}
	if p.AccessCommonName != clientID {
		return "Fleet's registered machine common name differs from Cloudflare", nil
	}
	// This runs in the worker's full database scope, so any initial membership in another project is visible.
	p, err := resolveAccessTx(q.ctx(), q.tx, g.Name)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "The machine viewer is missing from Fleet's effective roster", nil
		}
		return "", err
	}
	if p.Owner || !slices.Equal(p.Roles, []string{"viewer"}) ||
		!slices.Equal(p.Grants, []personGrant{{Kind: "project", ID: g.ProjectID}}) {
		return "Fleet's effective machine access differs from the approved one-project viewer", nil
	}
	if _, err := projectSettingsByID(q, g.ProjectID); err != nil {
		var httpErr *HTTPError
		if errors.As(err, &httpErr) && httpErr.Code == 404 {
			return "The approved Fleet project no longer exists", nil
		}
		return "", err
	}
	return "", nil
}
