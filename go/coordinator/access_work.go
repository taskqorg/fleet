// The provisioner's queue on the work kit (docs/designs/fleet-standard.md S6, docs/designs/fleet-project-access.md):
// one access_work row per grant with pending work (provision, probe or revoke). The step reads the grant, its server
// and the project's other live grants on that server, leaves the SSH work (accessPlan.apply, access_provision.go) as its
// call and records the outcome: the probe, the credential's fingerprint and the grant's next state. Every recipe is
// idempotent, so a crash between the call and its record only repeats work. Claims run one at a time (Workers 1): a
// project's grants on one server share its OS user, and their recipes must not interleave.
package main

import (
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"fleet/contract"
	"fleet/coordinator/db"
)

func init() {
	kitRegister(&kitQueue{Name: "access-work", Period: 2 * time.Second, Limit: 4, Workers: 1, Due: accessWorkDue, Step: accessWorkStep,
		Retry: kitRetry{Table: "access_work", Key: "grant_id", Guard: `access_work.attempts=$5::int`, Class: func(error) string { return "access_work_failed" }}})
}

func accessWorkDue(q *Request, now time.Time, limit int) ([]*kitClaim, error) {
	rs, err := q.db().AccessWorkDue(q.ctx(), db.AccessWorkDueParams{Now: now, Lim: int32(limit)})
	out := make([]*kitClaim, 0, len(rs))
	for _, r := range rs {
		out = append(out, &kitClaim{Key: r.GrantID, Attempts: int(r.Attempts), Guard: []any{r.Attempts}})
	}
	return out, err
}

func accessWorkStep(q *Request, now time.Time, c *kitClaim) error {
	w, err := q.db().AccessWorkGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	r, err := q.db().AccessGrantGet(q.ctx(), c.Key)
	if errors.Is(err, pgx.ErrNoRows) {
		return q.db().AccessWorkDelete(q.ctx(), db.AccessWorkDeleteParams{GrantID: c.Key, Op: w.Op})
	}
	if err != nil {
		return err
	}
	g := accessGrantOf(r)
	if err := kitLock(q, "project:"+g.ProjectID); err != nil {
		return err
	}
	if c.Result != nil {
		out, _ := c.Result.(accessOutcome)
		if out.Op == "" {
			out.Op = w.Op
		}
		return accessWorkRecord(q, now, g, out.Op, out)
	}
	p, err := accessPlanOf(q, g, w.Op)
	if err != nil {
		return accessWorkRecord(q, now, g, w.Op, accessOutcome{Error: err.Error()})
	}
	c.Call = p.apply
	return nil
}

// accessPlanOf: what accessApply needs: the way to the server and the paths the project's OS user keeps.
func accessPlanOf(q *Request, g accessGrant, op string) (accessPlan, error) {
	hops, _, err := accessHops(q, g.ServerID)
	if err != nil {
		return accessPlan{}, err
	}
	others, err := q.db().AccessGrantLiveForUser(q.ctx(), db.AccessGrantLiveForUserParams{ProjectID: g.ProjectID, ServerID: g.ServerID, ID: g.ID})
	if err != nil {
		return accessPlan{}, err
	}
	p := accessPlan{Op: op, Grant: g, Hops: hops, OSUser: accessOSUser(g.ProjectID), DBUser: accessDBUser(g.ProjectID, g.ID),
		KeyName: accessKeyName(g.ProjectID, g.ServerID), PasswordName: accessPasswordName(g), Others: len(others)}
	for _, o := range others {
		if o.Kind == "files" {
			var pl accessGrantPayload
			lenientDecode(o.Payload, &pl)
			p.Paths = append(p.Paths, accessPathItems(o.Level, pl.Scope)...)
		}
	}
	if op != "revoke" && g.Kind == "files" {
		p.Paths = append(p.Paths, accessPathItems(g.Level, g.Scope)...)
	}
	return p, nil
}

// accessWorkRecord: the outcome of one run. A provision or probe sets the probe and moves the grant to ready or failed;
// a revoke that worked revokes it, one that failed keeps it revoking with its error (access_grant_revoke runs it again).
func accessWorkRecord(q *Request, now time.Time, g accessGrant, op string, out accessOutcome) error {
	res := contract.AccessCheckResult{At: now.UnixMilli(), Ok: out.Error == "", Steps: out.Steps}
	if out.Error != "" {
		res.Error = &out.Error
	}
	note := func(field string, v any) error {
		return q.db().AccessGrantNote(q.ctx(), db.AccessGrantNoteParams{ID: g.ID, Field: field, Value: jsonBytes(v)})
	}
	if err := note("error", out.Error); err != nil {
		return err
	}
	if out.Credential != "" {
		if err := q.db().AccessGrantSetCredential(q.ctx(), db.AccessGrantSetCredentialParams{ID: g.ID, Credential: &out.Credential}); err != nil {
			return err
		}
	}
	var err error
	switch {
	case op != "revoke" && g.State == "revoking": // revoked meanwhile: the revoke that replaced this work decides
		err = note("probe", res)
	case op == "revoke" && res.Ok && g.State == "revoking":
		if err = accessGrantTransition(q, g.ID, "revoked"); err == nil {
			err = accessConnectionDown(q, g)
		}
	case op == "revoke":
	case res.Ok:
		if err = note("probe", res); err == nil {
			err = accessGrantTransition(q, g.ID, "ready")
		}
		if err == nil {
			err = accessConnectionUp(q, g)
		}
	default:
		if err = note("probe", res); err == nil {
			err = accessGrantTransition(q, g.ID, "failed")
		}
	}
	if err != nil {
		return err
	}
	return q.db().AccessWorkDelete(q.ctx(), db.AccessWorkDeleteParams{GrantID: g.ID, Op: op})
}
