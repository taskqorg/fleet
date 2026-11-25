package main

import "testing"

// Requirement: a project tool whose schema takes an object (access_grant_ask's scope) accepts it, and every required
// argument is checked by its declared kind. Risk (2026-11-26, DAM pilot): the check took every non-integer argument
// for a string, so access_grant_ask could never be called.
func TestArgumentsFitObjects(t *testing.T) {
	scope := M{"database": "revilio_db", "tables": []any{"project", "upload"}}
	ask := M{"requestId": "r1", "server": "pds9", "kind": "mysql", "level": "read", "scope": scope, "reason": "inventory"}
	if err := argumentsFit("access_grant_ask", ask); err != nil {
		t.Fatalf("object scope refused: %v", err)
	}
	for name, change := range map[string]func(M){
		"scope as a string": func(m M) { m["scope"] = "revilio_db" },
		"scope missing":     func(m M) { delete(m, "scope") },
		"reason blank":      func(m M) { m["reason"] = " " },
		"unknown field":     func(m M) { m["projectX"] = "x" },
	} {
		m := M{}
		for k, v := range ask {
			m[k] = v
		}
		change(m)
		if err := argumentsFit("access_grant_ask", m); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if err := argumentsFit("connections_key_request", M{"requestId": "r", "connection": "tavily"}); err != nil {
		t.Errorf("string tool refused: %v", err)
	}
	if err := argumentsFit("connections_key_request", M{"requestId": "r", "connection": M{}}); err == nil {
		t.Error("an object for a string argument was accepted")
	}
}
