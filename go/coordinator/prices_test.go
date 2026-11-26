package main

import (
	"context"
	"fmt"
	"math"
	"os"
	"testing"

	"fleet/coordinator/db"
	"github.com/jackc/pgx/v5"
)

// Requirement: a model tool call (an api step's tool_run) whose line carries the token split and model is priced from
// the price table by model and token kind, as an agent run is (fleet-project-apps.md, Attribution and cost); a call
// without them stays unavailable. Risk: api steps read unavailable although their tokens and prices are known.
func TestPriceModelToolCall(t *testing.T) {
	sockDir, port, _ := ensureTestPostgres(t)
	name := fmt.Sprintf("fleet_go_test_%d_%d", os.Getpid(), testDBSeq.Add(1))
	cloneTestDB(t, name)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, fmt.Sprintf("postgresql://fleet@/%s?host=%s&port=%d", name, sockDir, port))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	for _, sql := range []string{
		"SET fleet.project_ids = '*'",
		`INSERT INTO price_entry(id, subject, unit, price, currency, source, observed_at, effective_from) VALUES
		 ('price:in', 'gpt-x', 'input_tokens', 0.000002, 'USD', 'owner', now(), now() - interval '1 day'),
		 ('price:out', 'gpt-x', 'output_tokens', 0.00001, 'USD', 'owner', now(), now() - interval '1 day'),
		 ('price:cr', 'gpt-x', 'cache_read_tokens', 0.0000005, 'USD', 'owner', now(), now() - interval '1 day')`,
		`INSERT INTO execution(id, kind, state, project_id, started_at, ended_at, data) VALUES
		 ('call:model', 'tool_run', 'succeeded', 'dam', now(), now(), '{"tool":"create_response","connection":"openai-2"}'),
		 ('call:plain', 'tool_run', 'succeeded', 'dam', now(), now(), '{"tool":"create_response","connection":"openai-2"}')`,
	} {
		if _, err := conn.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	q := db.New(conn)
	for _, id := range []string{"call:model", "call:plain"} {
		cost := []byte(`{"unit":"tokens","tokens":1150,"amount":1150,"connection":"openai-2"}`)
		if err := q.ConnectionCostWrite(ctx, db.ConnectionCostWriteParams{Cost: cost, Basis: "measured", ID: id}); err != nil {
			t.Fatal(err)
		}
	}
	cacheRead := int64(100)
	if err := q.ConnTokensWrite(ctx, db.ConnTokensWriteParams{ID: "call:model", Input: 1000, Output: 50, CacheRead: &cacheRead, Model: "gpt-x"}); err != nil {
		t.Fatal(err)
	}
	type spend struct {
		basis    *string
		value    *float64
		unpriced int64
	}
	read := func(id string) spend {
		var s spend
		if err := conn.QueryRow(ctx, `SELECT cost_basis, cost_value, cost_unpriced FROM execution_spend WHERE execution_id = $1`, id).Scan(&s.basis, &s.value, &s.unpriced); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		return s
	}
	s := read("call:model")
	if deref(s.basis) != "estimated" || s.value == nil || math.Abs(*s.value-(1000*0.000002+50*0.00001+100*0.0000005)) > 1e-12 || s.unpriced != 0 {
		t.Fatalf("model call: %v %v %d", deref(s.basis), s.value, s.unpriced)
	}
	if s := read("call:plain"); s.value != nil || s.unpriced != 1 {
		t.Fatalf("call without a model: %v %v %d", deref(s.basis), s.value, s.unpriced)
	}
	if !pricedByTokens("tool_run", ptr("gpt-x"), true) || pricedByTokens("tool_run", nil, true) || pricedByTokens("agent_run", ptr("m"), true) || !pricedByTokens("agent_run", ptr("m"), false) {
		t.Fatal("pricedByTokens disagrees with the SQL by_tokens")
	}
}
