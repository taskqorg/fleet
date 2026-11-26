package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: connecting a local agent mints <machine>-<harness> with every connection the person may use and answers
// the command with the new token once; connecting again refreshes without a value; the report shows it unused until
// a call, connected after one, needing a refresh when a connection is added, and disconnected once revoked.
func TestLocalAgentsFlow(t *testing.T) {
	e := openTestCoordinator(t, false)
	h := e.ownerHeaders()
	if _, err := e.ownerStore.pool.Exec(e.ctx, `INSERT INTO host (id, host_id, holder) VALUES ('la-mac', 'la-mac', $1)`, testOwnerEmail); err != nil {
		t.Fatal(err)
	}
	var ignore map[string]any
	e.opInto(t, "connections_create", map[string]any{"name": "github", "provider": "github"}, h, &ignore)
	var first contract.LocalAgentConnectReply
	e.opInto(t, "local_agent_connect", map[string]any{"machine": "la-mac", "harness": "codex"}, h, &first)
	if first.Name != "la-mac-codex" || first.NewToken == nil || !*first.NewToken || !strings.Contains(first.Command, "| ~/releases/current/fleet-executor local-agent codex la-mac-codex") {
		t.Fatalf("connect: %+v", first)
	}
	report := func() contract.LocalAgent {
		t.Helper()
		var r contract.LocalAgentsReply
		e.opInto(t, "local_agents", map[string]any{}, h, &r)
		if len(r.Agents) != 1 {
			t.Fatalf("agents: %+v", r.Agents)
		}
		return r.Agents[0]
	}
	if a := report(); a.State != contract.LocalAgentStateUnused || len(a.Connections) != 1 || a.Connections[0].Name != "github" {
		t.Fatalf("new agent: %+v", a)
	}
	if err := e.app.store.Write(e.ctx, nil, nil, func(q *Request) error {
		tok, err := localAgentLive(q, "la-mac-codex")
		if err != nil || tok == nil {
			t.Fatalf("token: %v", err)
		}
		return tokenCallNote(q, tok.ID, contract.CallLine{Connection: "github", Tool: "repo_get", Outcome: contract.CallOutcomeOk, StartedAt: 5000})
	}); err != nil {
		t.Fatal(err)
	}
	if a := report(); a.State != contract.LocalAgentStateConnected || a.LastOkAt == nil || *a.LastOkAt != 5000 || a.Connections[0].OkTool == nil {
		t.Fatalf("after a call: %+v", a)
	}
	e.opInto(t, "connections_create", map[string]any{"name": "openai", "provider": "openai"}, h, &ignore)
	if a := report(); a.State != contract.LocalAgentStateRefresh || len(a.Missing) != 1 || a.Missing[0] != "openai" {
		t.Fatalf("new connection: %+v", a)
	}
	var again contract.LocalAgentConnectReply
	e.opInto(t, "local_agent_connect", map[string]any{"machine": "la-mac", "harness": "codex"}, h, &again)
	if again.NewToken != nil && *again.NewToken || strings.Contains(again.Command, "printf") || len(again.Connections) != 2 {
		t.Fatalf("refresh: %+v", again)
	}
	if a := report(); a.State != contract.LocalAgentStateConnected {
		t.Fatalf("after refresh: %+v", a)
	}
	e.opInto(t, "local_agent_disconnect", map[string]any{"machine": "la-mac", "harness": "codex"}, h, &ignore)
	if a := report(); a.State != contract.LocalAgentStateDisconnected {
		t.Fatalf("after disconnect: %+v", a)
	}
	if code, _ := e.callJSON(t, "POST", "/api/ops/local_agent_connect", map[string]any{"machine": "nowhere", "harness": "codex"}, h); code != 404 {
		t.Fatalf("unknown machine: %d", code)
	}
}
