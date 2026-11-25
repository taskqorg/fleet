package main

import (
	"encoding/json"
	"testing"
)

func TestProviderErrorDetail(t *testing.T) {
	for _, c := range []struct{ body, want string }{
		{`{"error":{"code":429,"message":"You exceeded your current quota.","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_requests","quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`,
			"RESOURCE_EXHAUSTED; You exceeded your current quota.; quota GenerateRequestsPerDayPerProjectPerModel-FreeTier"},
		{`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`, "rate_limit_error; Rate limited"},
		{`{"error":"no-usable-key","message":"every key is disabled"}`, "no-usable-key; every key is disabled"},
		{`{"message":"Bad credentials"}`, "Bad credentials"},
		{`{"text":"plain"}`, ""},
		{`not json`, ""},
	} {
		if got := providerErrorDetail(json.RawMessage(c.body)); got != c.want {
			t.Errorf("%s: got %q, want %q", c.body, got, c.want)
		}
	}
}
