package main

import (
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"slices"
)

// The executor already journals this identity before sending. Commit its acknowledgment with the mutation.
func agentReceipt(q *Request, key *string, execution, operation string, payload any, run func() (any, error)) (any, error) {
	if key == nil || *key == "" {
		return run()
	} // Older hosts did not send identities.
	digest := frameHash(payload)
	var priorExecution, priorDirector, priorOperation, priorDigest string
	var result, authority json.RawMessage
	err := q.tx.QueryRow(q.ctx(), "SELECT execution_id,directing_email,operation,payload_sha,result,authority FROM agent_tool_receipt WHERE id=$1", *key).Scan(&priorExecution, &priorDirector, &priorOperation, &priorDigest, &result, &authority)
	if err == nil {
		if priorExecution != execution || priorDirector != q.person.Email || priorOperation != operation || priorDigest != digest {
			return nil, fail(409, "Agent tool request identity conflict")
		}
		var previous Person
		if err := json.Unmarshal(authority, &previous); err != nil {
			return nil, err
		}
		if !receiptAuthority(q.person, &previous) {
			return nil, fail(403, "Request already committed; current authority no longer permits its acknowledgment. Do not repeat the mutation")
		}
		return result, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out, err := run()
	if err != nil {
		return nil, err
	}
	err = exec(q, "INSERT INTO agent_tool_receipt VALUES($1,$2,$3,$4,$5,$6,$7)", *key, execution, q.person.Email, operation, digest, jsonBytes(out), jsonBytes(q.person))
	return out, err
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
