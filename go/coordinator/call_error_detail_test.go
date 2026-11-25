package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestCallErrorDetail: a provider's 429 reply keeps its quota and retry details, loses credential-like values, and is
// bounded; text replies and empty ones work.
func TestCallErrorDetail(t *testing.T) {
	reply := `{"error": {"code": 429, "message": "Quota exceeded for metric: generate_content_free_tier_requests, key AIzaSyA1234567890abcdefghijklmnop", "status": "RESOURCE_EXHAUSTED",
	 "details": [{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": [{"quotaMetric": "generativelanguage.googleapis.com/generate_content_free_tier_requests"}]},
	 {"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "41s"}], "link": "https://x.test/?key=secretvalue&a=1"}}`
	got := callErrorDetail(json.RawMessage(reply))
	for _, want := range []string{"RESOURCE_EXHAUSTED", "generate_content_free_tier_requests", `"retryDelay":"41s"`, "[redacted]"} {
		if !strings.Contains(got, want) {
			t.Fatalf("detail lacks %q: %s", want, got)
		}
	}
	for _, leak := range []string{"AIzaSyA1234567890", "secretvalue"} {
		if strings.Contains(got, leak) {
			t.Fatalf("detail keeps %q: %s", leak, got)
		}
	}
	if got := callErrorDetail(json.RawMessage(`"Bearer abcdefghijklmnop rate limited"`)); got != "[redacted] rate limited" {
		t.Fatalf("text reply = %q", got)
	}
	if got := callErrorDetail(nil); got != "" {
		t.Fatalf("empty reply = %q", got)
	}
	long := callErrorDetail(json.RawMessage(`"` + strings.Repeat("é", 3000) + `"`))
	if len(long) > callErrorDetailMax+len("…") || !strings.HasSuffix(long, "…") {
		t.Fatalf("long reply not bounded: %d bytes", len(long))
	}
}
