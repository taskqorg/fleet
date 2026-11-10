package main

import (
	"encoding/json"
	"testing"
)

// Requirement: grant {kind: "*"} is owner-equivalent (site-agents, fleet-people-access §1) without being the owner.
func TestStarGrantIsOwnerEquivalent(t *testing.T) {
	var p Person
	json.Unmarshal([]byte(`{"email":"site-agents","roles":["operator","architect"],"accessCommonName":"x.access","grants":[{"kind":"*","id":"*"}]}`), &p)
	if !fullAccess(&p) || scopeOf(&p) != nil || !mayAccessProjectID(&p, "any") || p.Owner {
		t.Fatal("star grant must be full access, '*' scope, any project, not owner")
	}
	op := Person{Email: "op", Roles: []string{"operator"}}
	if fullAccess(&op) || scopeOf(&op) == nil {
		t.Fatal("operator without star grant must stay scoped")
	}
}
