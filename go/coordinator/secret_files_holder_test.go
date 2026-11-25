package main

import "testing"

func TestSecretFilesHolder(t *testing.T) {
	agents := &Person{Email: "site-agents"}
	agents.Grants = append(agents.Grants, struct {
		Kind string `json:"kind"`
		ID   string `json:"id"`
	}{"*", ""})
	member := &Person{Email: "m@example.com"}
	q := &Request{peopleView: &peopleSnapshot{byEmail: map[string]*Person{"site-agents": agents, "m@example.com": member}}}
	for holder, want := range map[string]bool{"o@example.com": true, "site-agents": true, "m@example.com": false, "gone@example.com": false} {
		if got := secretFilesHolder(q, holder, "o@example.com"); got != want {
			t.Errorf("holder %s: got %v, want %v", holder, got, want)
		}
	}
}
