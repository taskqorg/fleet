package main

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// Requirement: fleet-telemetry.md §1 "Client disconnect is cancelled": a request whose client left answers 499, not
// 500, so it raises no error report and no error-spike.
// Risk: 2026-11-25 a coordinator restart's reconnects aborted definition_list calls; each became a 500 and a Bugsink
// event, and 25 of 48 calls in five minutes opened a false error-spike question.
func TestClientGoneIsNotServerError(t *testing.T) {
	env := openTestCoordinator(t, false)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("POST", "/api/ops/definition_list", strings.NewReader(`{"kind":"agent"}`)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range env.ownerHeaders() {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	env.app.routes().ServeHTTP(rec, req)
	if rec.Code != 499 {
		t.Fatalf("cancelled request = %d %s, want 499", rec.Code, rec.Body.String())
	}
}
