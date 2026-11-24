package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

func pricingFixtures(t *testing.T) map[string]string {
	t.Helper()
	fixtures := map[string]string{}
	for i, name := range []string{"openai", "anthropic", "gemini"} {
		page, err := os.ReadFile("testdata/provider-pricing/" + name + ".md")
		if err != nil {
			t.Fatal(err)
		}
		fixtures[providerPriceSources[i].document] = string(page)
	}
	return fixtures
}

// Requirement: FLT-27 only imports standard, representable token tariffs for exact IDs.
// Risk: source drift, repeated model rows, capacity tiers, different modalities, and date gaps masquerade as valid rates.
func TestProviderPriceFormats(t *testing.T) {
	fixtures := pricingFixtures(t)
	now := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	openai := fixtures[providerPriceSources[0].document]
	gemini := fixtures[providerPriceSources[2].document]
	for _, tc := range []struct {
		name, page, gap string
		provider, count int
		now             time.Time
	}{
		{"OpenAI Standard only", openai, "Long-context", 0, 6, now},
		{"OpenAI changed table heading", strings.Replace(openai, "### Standard pricing data", "### Price list", 1), "changed", 0, 0, now},
		{"OpenAI duplicate model", strings.Replace(openai, "| gpt-6-astra |", "| gpt-6.2-sol |", 1), "gpt-6.2-sol: exact Standard row missing, ambiguous", 0, 3, now},
		{"Anthropic unit changes", strings.Replace(fixtures[providerPriceSources[1].document], "$0.10 / MTok", "$0.10 / KTok", 1), "claude-sonnet-5-5 cache_read_tokens", 1, 5, now},
		{"Gemini uses paid Standard", gemini, "token-hour", 2, 6, now},
		{"Gemini rejects modality rate", strings.Replace(gemini, "$0.75 through", "$0.75 (text) through", 1), "input_tokens", 2, 4, now},
		{"Gemini rejects date gap", strings.Replace(gemini, "$1.50 starting January 1", "$1.50 starting January 2", 1), "input_tokens", 2, 4, now},
		{"Gemini at effective boundary", gemini, "token-hour", 2, 3, time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rates, gaps := providerPriceSources[tc.provider].parse(tc.page, tc.now)
			if len(rates) != tc.count || !strings.Contains(strings.Join(gaps, "; "), tc.gap) {
				t.Fatalf("rates=%+v, gaps=%v", rates, gaps)
			}
		})
	}
}

// Requirement: FLT-27 appends only verified prices, persists cost provenance and audits, and leaves unpriced
// work unavailable. Risk: dry-run writes, stale execution plans, duplicate retries, effective-date bleed into older
// usage, or blending subscription equivalents with measured charges.
func TestProviderPriceRefreshAndSpend(t *testing.T) {
	e := openTestCoordinator(t, false)
	fixtures := pricingFixtures(t)
	now := time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	unavailable := ""
	fetch := func(_ context.Context, url string) (string, error) {
		if url == unavailable {
			return "", fmt.Errorf("HTTP 503")
		}
		return fixtures[url], nil
	}
	owner := testOwnerEmail
	ownerHeaders := e.ownerHeaders()
	request := func(email string) *Request {
		return &Request{app: e.app, person: &Person{Email: email}, agent: "execution:hal0-fixture"}
	}
	denied := map[string]string{"Authorization": "Bearer " + e.browserSecret, "X-Fleet-Person": "member@trial.test"}
	if code, result := e.callJSON(t, "POST", "/api/ops/price_refresh", doc{}, denied); code != 403 {
		t.Fatalf("member can access pricing refresh: %d %v", code, result)
	}
	old := now.AddDate(0, 0, -1)
	_, err := e.ownerStore.pool.Exec(e.ctx, `
		INSERT INTO price_entry(id, subject, unit, price, currency, source, observed_at, effective_from)
		VALUES ('legacy-input', 'gpt-6.2-sol', 'input_tokens', 0.000003, 'USD', 'owner', $1, $1)`, old)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`INSERT INTO execution(id,kind,state,model,started_at,ended_at,data) VALUES
		('gpt-sub','agent_run','succeeded','gpt-6.2-sol',$1,$1,'{}'),
		('claude-sub','agent_run','succeeded','claude-sonnet-5-5',$1,$1,'{}'),
		('missing','agent_run','succeeded','example-unpriced-model',$1,$1,'{}'),
		('charge','tool_run','succeeded',NULL,$1,$1,'{}')`,
		`INSERT INTO execution_totals (execution_id, model, tokens_cache_read, tokens_cache_read_basis, billing_basis, connection_cost, connection_cost_basis) VALUES
		('gpt-sub','gpt-6.2-sol',10000000,'measured','subscription',NULL,'unavailable'),
		('claude-sub','claude-sonnet-5-5',20000000,'measured','subscription',NULL,'unavailable'),
		('missing','example-unpriced-model',10000000,'measured','subscription',NULL,'unavailable'),
		('charge',NULL,NULL,'unavailable','api','{"unit":"call","amount":6,"currency":"USD"}','measured')`,
	} {
		args := []any{}
		if strings.Contains(stmt, "$1") {
			args = append(args, old)
		}
		if _, err := e.ownerStore.pool.Exec(e.ctx, stmt, args...); err != nil {
			t.Fatal(err)
		}
	}
	read := func(op string, body any) map[string]any {
		t.Helper()
		code, value := e.callJSON(t, "POST", "/api/ops/"+op, body, ownerHeaders)
		if code != 200 {
			t.Fatalf("%s: %d %v", op, code, value)
		}
		return value
	}
	check := func(planID *string) (contract.PriceRefreshReply, error) {
		return priceRefreshWith(request(owner), &contract.PriceRefreshRequest{PlanId: planID}, fetch, func() time.Time { return now })
	}
	spend := func() (map[string]any, map[contract.ResourceBasis]contract.CostFigure) {
		t.Helper()
		wire := read("spend_summary", doc{"window": doc{"from": "2026-11-23", "to": "2026-11-25"}, "by": "model"})
		payload, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		var result contract.SpendSummaryReply
		if err = json.Unmarshal(payload, &result); err != nil {
			t.Fatal(err)
		}
		costs := map[contract.ResourceBasis]contract.CostFigure{}
		for _, cost := range result.Totals.Cost {
			costs[cost.Basis] = cost
		}
		return wire, costs
	}
	if _, before := spend(); before[contract.ResourceBasisSubscription].Value != nil {
		t.Fatalf("cache reads were priced before import: %v", before)
	}

	preview, err := check(nil)
	if err != nil || len(preview.Providers) != 3 || len(preview.Appended) != 0 {
		t.Fatalf("preview=%+v, err=%v", preview, err)
	}
	if saved := read("prices_list", doc{})["prices"].([]any); len(saved) != 1 {
		t.Fatalf("dry run mutated catalog: %v", saved)
	}
	observation := preview.Providers[0].Changes[0]
	if observation.Previous == nil || observation.Previous.Id != "legacy-input" || observation.Price != 0.000002 {
		t.Fatalf("wrong correction: %+v", observation)
	}
	unavailable = providerPriceSources[1].document
	if _, err := check(&preview.PlanId); err == nil {
		t.Fatal("applied a different source snapshot under the old plan")
	}
	partial, err := check(nil)
	if err != nil || len(partial.Providers[1].Changes) != 0 || !strings.Contains(strings.Join(partial.Providers[1].Gaps, " "), "HTTP 503") {
		t.Fatalf("source failure was not isolated: %+v, %v", partial, err)
	}
	unavailable = ""
	written, err := check(&preview.PlanId)
	if err != nil || len(written.Appended) != 18 {
		t.Fatalf("apply=%+v, err=%v", written, err)
	}
	for _, entry := range written.Appended {
		history := read("setting_changes", doc{"target": "price:" + entry.Id})["changes"].([]any)
		if len(history) != 1 {
			t.Fatalf("%s audit entries: %v", entry.Id, history)
		}
		record := history[0].(map[string]any)
		if record["actor"] != "execution:hal0-fixture" || record["onBehalfOf"] != owner ||
			record["value"].(map[string]any)["id"] != entry.Id {
			t.Fatalf("missing audit provenance: %v", record)
		}
	}
	if _, err := check(&preview.PlanId); err == nil {
		t.Fatal("stale preview was accepted after append")
	}
	saved := read("prices_list", doc{})["prices"].([]any)
	if len(saved) != len(written.Appended)+1 {
		t.Fatalf("expected all appends and retained original: %v", saved)
	}
	now = now.Add(24 * time.Hour)
	rerun, err := check(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range rerun.Providers {
		if len(provider.Changes) != 0 || provider.Unchanged != 6 {
			t.Fatalf("unchanged rerun: %+v", provider)
		}
	}
	if result, err := check(&rerun.PlanId); err != nil || len(result.Appended) != 0 {
		t.Fatalf("unchanged apply: %+v, %v", result, err)
	}
	now = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	atDate, err := check(nil)
	if err != nil || atDate.Providers[2].Unchanged != 3 || len(atDate.Providers[2].Changes) != 0 {
		t.Fatalf("Gemini future date did not activate: %+v, %v", atDate, err)
	}

	wire, figures := spend()
	sub, billed, missing := figures[contract.ResourceBasisSubscription], figures[contract.ResourceBasisMeasured], figures[contract.ResourceBasisUnavailable]
	if sub.Value == nil || math.Abs(*sub.Value-3) > 1e-9 || len(sub.Prices) != 2 || billed.Value == nil || *billed.Value != 6 ||
		missing.Basis != contract.ResourceBasisUnavailable || missing.Value != nil || missing.Currency != nil || missing.Usages == nil || *missing.Usages != 1 {
		t.Fatalf("Spend bases or missing value incorrect: %+v", figures)
	}
	data, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("Spend response: %s", data)
}
