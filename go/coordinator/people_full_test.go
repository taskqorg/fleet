package main

import (
	"encoding/json"
	"testing"
)

// Requirement: grant {kind: "*"} is owner-equivalent (site-agents, fleet-people-access §1) without being the owner.
func TestStarGrantIsOwnerEquivalent(t *testing.T) {
	var p Person
	if err := json.Unmarshal([]byte(`{"email":"site-agents","roles":["operator","architect"],"accessCommonName":"x.access","grants":[{"kind":"*","id":"*"}]}`), &p); err != nil {
		t.Fatal(err)
	}
	if !fullAccess(&p) || scopeOf(&p) != nil || !mayAccessProjectID(&p, "any") || p.Owner {
		t.Fatal("star grant must be full access, '*' scope, any project, not owner")
	}
	op := Person{Email: "op", Roles: []string{"operator"}}
	if fullAccess(&op) || scopeOf(&op) == nil {
		t.Fatal("operator without star grant must stay scoped")
	}
	if connOwner(&Request{person: &p}) != nil || connOwner(&Request{person: &op}) == nil {
		t.Fatal("star grant handles keys and personal tokens; a scoped operator does not")
	}
}
