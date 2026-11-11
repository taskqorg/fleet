package main

import (
	"encoding/json"
	"testing"

	"fleet/contract"
)

// Requirement: an agent acts only on the execution and session the tool path binds (agent.go bound). Risk: a body key that
// differs only in case ("executionid") overrides the bound executionId, since encoding/json matches names case-insensitively.
func TestTypedOpRefusesCaseVariantKeys(t *testing.T) {
	var got string
	h := typedOp(func(q *Request, r *contract.ExecutionStatusRequest) (string, error) {
		got = r.ExecutionId
		return "", nil
	})
	if _, err := h(&Request{body: json.RawMessage(`{"executionId":"mine","executionid":"other"}`)}); err == nil {
		t.Fatalf("case variant accepted, handler saw %q", got)
	}
	if _, err := h(&Request{body: json.RawMessage(`{"executionId":"mine","extra":1}`)}); err != nil || got != "mine" {
		t.Fatalf("exact body: err %v, saw %q", err, got)
	}
}

// Requirement N5 (the request struct is the contract). Risk: a required number left out decodes as 0, so budget_set
// without a limit created a stop budget of 0 that cancelled all of a person's work.
func TestTypedOpRefusesMissingRequiredField(t *testing.T) {
	h := typedOp(func(q *Request, r *contract.BudgetSetRequest) (string, error) { return "", nil })
	if _, err := h(&Request{body: json.RawMessage(`{"scope":"person","scopeId":"a@b","unit":"wall_ms","action":"stop"}`)}); err == nil {
		t.Fatal("missing limit accepted")
	}
	if _, err := h(&Request{body: json.RawMessage(`{"scope":"person","scopeId":"a@b","unit":"wall_ms","limit":0}`)}); err != nil {
		t.Fatalf("limit 0 refused: %v", err)
	}
}
