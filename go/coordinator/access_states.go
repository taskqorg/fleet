// Project access grant state (docs/designs/fleet-standard.md S5, docs/designs/fleet-project-access.md#grant-states):
// accessGrantTransitions is the table, accessGrantInsert creates a grant asked and accessGrantTransition is the only
// other writer of access_grant.state. Every move is an audit row on the grant.
package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

var accessGrantTransitions = map[string][]string{
	"asked":        {"provisioning", "declined", "withdrawn"},
	"provisioning": {"ready", "failed", "revoking"},
	"ready":        {"provisioning", "failed", "revoking"},
	"failed":       {"provisioning", "revoking"},
	"revoking":     {"revoked"},
	"declined":     nil,
	"withdrawn":    nil,
	"revoked":      nil,
}

// accessGrantInsert creates grant p in state asked.
func accessGrantInsert(q *Request, p db.AccessGrantInsertParams) error {
	if err := q.db().AccessGrantInsert(q.ctx(), p); err != nil {
		return err
	}
	return audit(q, "access-grant:"+p.ID, "state", nil, "asked", auditExtra{ProjectID: &p.ProjectID})
}

// accessGrantTransition moves grant id to state to, locking its row; the same state is no move, a move the table does
// not allow is 409.
func accessGrantTransition(q *Request, id, to string) error {
	from, err := q.db().AccessGrantLockState(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if from == to {
		return nil
	}
	if !slices.Contains(accessGrantTransitions[from], to) {
		return fail(409, fmt.Sprintf("grant %s cannot move from %s to %s", id, from, to))
	}
	if err := q.db().AccessGrantMove(q.ctx(), db.AccessGrantMoveParams{ID: id, State: to}); err != nil {
		return err
	}
	return audit(q, "access-grant:"+id, "state", from, to, auditExtra{})
}
