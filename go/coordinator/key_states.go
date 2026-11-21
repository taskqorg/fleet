// Connection key state (docs/designs/fleet-standard.md S5, docs/designs/fleet-connections.md): a key is active,
// disabled (the provider refused it, or a holder turned it off) or retired (its value is gone; terminal).
// connectionKeyTransition is the only writer of connection_key.state: connKeyPut (connections.go) creates it active
// and moves it here to the state it names.
package main

import (
	"errors"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

var connectionKeyTransitions = map[string][]string{
	"active":   {"disabled", "retired"},
	"disabled": {"active", "retired"},
	"retired":  nil,
}

// connectionKeyState: the key's state, locked for this transaction; "" when there is none.
func connectionKeyState(q *Request, id string) (string, error) {
	s, err := q.db().ConnectionKeyStateOf(q.ctx(), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return s, err
}

// connectionKeyTransition moves key id to state to; the same state is no move, a move the table does not allow a 409
// (a retired key stays retired).
func connectionKeyTransition(q *Request, id, to string) error {
	from, err := connectionKeyState(q, id)
	if err != nil {
		return err
	}
	if from == "" {
		return errNotFound
	}
	if from == to {
		return nil
	}
	if !slices.Contains(connectionKeyTransitions[from], to) {
		return fail(409, fmt.Sprintf("key %s cannot move from %s to %s", id, from, to))
	}
	return q.db().ConnectionKeyMove(q.ctx(), db.ConnectionKeyMoveParams{ID: id, State: to})
}
