package main

import (
	"encoding/json"
	"errors"
	"slices"

	"github.com/jackc/pgx/v5"

	"fleet/coordinator/db"
)

// The executor already journals this identity before sending. Commit its acknowledgment with the mutation.
func agentReceipt(q *Request, key *string, execution, operation string, payload any, run func() (any, error)) (any, error) {
	if key == nil || *key == "" {
		return run()
	} // Older hosts did not send identities.
	digest := frameHash(payload)
	prior, err := q.db().ReceiptOf(q.ctx(), *key)
	if err == nil {
		if prior.ExecutionID != execution || prior.DirectingEmail != q.person.Email || prior.Operation != operation || prior.PayloadSha != digest {
			return nil, fail(409, "Agent tool request identity conflict")
		}
		var previous Person
		if err := json.Unmarshal(prior.Authority, &previous); err != nil {
			return nil, err
		}
		if !receiptAuthority(q.person, &previous) {
			return nil, fail(403, "Request already committed; current authority no longer permits its acknowledgment. Do not repeat the mutation")
		}
		return json.RawMessage(prior.Result), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out, err := run()
	if err != nil {
		return nil, err
	}
	return out, q.db().ReceiptInsert(q.ctx(), db.ReceiptInsertParams{ID: *key, ExecutionID: execution, DirectingEmail: q.person.Email, Operation: operation,
		PayloadSha: digest, Result: jsonBytes(out), Authority: jsonBytes(q.person)})
}

// Withhold old data after a revocation, even for an operation whose target is implicit in its body.
func receiptAuthority(current, previous *Person) bool {
	if fullAccess(current) {
		return true
	}
	if fullAccess(previous) {
		return false
	}
	for _, role := range previous.Roles {
		if !slices.Contains(current.Roles, role) {
			return false
		}
	}
	for _, grant := range previous.Grants {
		if !slices.Contains(current.Grants, grant) {
			return false
		}
	}
	return true
}
