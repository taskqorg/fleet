package main

import (
	"strings"
	"testing"
)

func TestCapabilityInstruction(t *testing.T) {
	dir := &Person{Email: "agents@example.com"}
	dir.Grants = append(dir.Grants, struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{"project", "fleet"})
	ops := []agentOperation{{Name: "project_create"}, {Name: "project_readiness"}, {Name: "ask"}, {Name: "task_page"}}
	conns := []connSpec{{Name: "youtrack", Tools: []toolSpec{{Name: "issues_search"}}}}
	got := capabilityInstruction(dir, ops, conns, map[string][]string{"pds18-native": {}, "owner-mac": {"pipe-read", "cs-gitlab"}})
	for _, want := range []string{
		"You act for agents@example.com with access to projects fleet.",
		"Fleet operations (4 in your tool list), among them projects: project_create, project_readiness; people: ask.",
		"API connections of this turn: youtrack (issues_search).",
		"Git connections (deploy keys) on hosts: owner-mac: cs-gitlab, pipe-read; pds18-native: none.",
		"says nothing about GitLab",
		"names what you tried",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if line := strings.Split(got, "\n")[2]; strings.Contains(line, "project_list") || strings.Contains(line, "task_page") {
		t.Errorf("names an operation the tool list does not carry:\n%s", got)
	}
	for _, n := range personOnlyOperations {
		if !strings.Contains(got, n) {
			t.Errorf("person-only %s not named", n)
		}
	}
	if !strings.Contains(capabilityInstruction(&Person{Email: "o@example.com", Owner: true}, nil, nil, nil), "full access") {
		t.Error("owner is not full access")
	}
}
