package main

import (
	"os"
	"strings"
	"testing"
	"time"
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
