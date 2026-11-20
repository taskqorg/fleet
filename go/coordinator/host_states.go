// Host state (docs/designs/fleet-standard.md S5): a registered host is active until hosts_remove tombstones it
// (removed); a removed host stays removed until system_purge deletes its record or hosts_register registers its id
// again. hostTransition is the only writer of
// host.state once the host exists; put of a "host" record (typed_records.go) creates it with its state and moves it
// here.
package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

var hostTransitions = map[string][]string{
	"active":  {"removed"},
	"removed": {"active"},
}

// hostState: the host record's state, locked for this transaction; "" when there is none.
func hostState(q *Request, id string) (string, error) {
	s, err := q.db().HostStateOf(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return s, err
}

// hostTransition moves host record id to state to; the same state is no move, a move the table does not allow a 409.
func hostTransition(q *Request, id, to string) error {
	from, err := hostState(q, id)
	if err != nil {
		return err
	}
	if from == "" {
		return errNotFound
	}
	if from == to {
		return nil
	}
	if !slices.Contains(hostTransitions[from], to) {
		return fail(409, fmt.Sprintf("host %s cannot move from %s to %s", id, from, to))
	}
	return q.db().HostMove(q.ctx(), db.HostMoveParams{ID: id, State: to})
}
