package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"fleet/obs"
)

// Requirement: absence-pass measures time without a completed pass and links the last trace.
// Risk: slow but continuous single-flight work opens periodic false absence findings.
func TestPassAbsenceUsesLastCompletion(t *testing.T) {
	const trace = "12345678901234567890123456789012"
	var age atomic.Int32
	age.Store(261)
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		if strings.Contains(string(query), "name LIKE 'pass.%'") {
			fmt.Fprintf(w, "{\"Name\":\"pass.placement-fixture\",\"Age\":%d,\"Duration\":104,\"TraceID\":%q}\n", age.Load(), trace)
		}
	}))
	t.Cleanup(ch.Close)
	oldStore := spanStore
	spanStore = &obs.ClickHouse{URL: ch.URL}
	t.Cleanup(func() { spanStore = oldStore })
	passIntervals.Store("placement-fixture", time.Minute)
	t.Cleanup(func() { passIntervals.Delete("placement-fixture") })

	was := processStarted
	processStarted = nowMs() - time.Hour.Milliseconds()
	t.Cleanup(func() { processStarted = was })
	findings, err := spanRules(context.Background())
	if err != nil || len(findings) != 0 {
		t.Fatalf("157 seconds since completion: findings=%+v err=%v", findings, err)
	}
	age.Store(290)
	findings, err = spanRules(context.Background())
	if err != nil || len(findings) != 1 || findings[0].Value != 186 || len(findings[0].Exemplars) != 1 || findings[0].Exemplars[0] != trace {
		t.Fatalf("186 seconds since completion: findings=%+v err=%v", findings, err)
	}
	// A coordinator that started 30 s ago (a release) has had no time to run the pass: no finding.
	processStarted = nowMs() - 30_000
	if findings, err = spanRules(context.Background()); err != nil || len(findings) != 0 {
		t.Fatalf("30 s after a restart: findings=%+v err=%v", findings, err)
	}
}

// Requirement: host-saturation names the latest beat's swap and its top three consumers from fleet.top
// (docs/designs/fleet-host-cleanup.md#6-host-saturation), one finding per host.
// Risk: the finding says only "saturated" and the owner hunts for what holds the memory by hand.
func TestHostSaturationNamesSwapAndTopConsumers(t *testing.T) {
	ch := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query, _ := io.ReadAll(r.Body)
		if strings.Contains(string(query), "AS Hot") {
			fmt.Fprintf(w, "{\"HostID\":\"owner-mac\",\"Beats\":5,\"Hot\":5,\"MaxLoad\":2,\"Exemplar\":\"t1\",\"SwapUsed\":%d,\"SwapTotal\":%d,\"Top\":%q}\n",
				37<<30, 38<<30, "com.apple.Virtualization.VirtualMachine:20480,postgres:2048*,node:1024,go:512")
		}
	}))
	t.Cleanup(ch.Close)
	oldStore := spanStore
	spanStore = &obs.ClickHouse{URL: ch.URL}
	t.Cleanup(func() { spanStore = oldStore })
	findings, err := spanRules(context.Background())
	if err != nil || len(findings) != 1 || findings[0].Rule != "host-saturation" || findings[0].Text == nil {
		t.Fatalf("findings=%+v err=%v", findings, err)
	}
	want := "Host owner-mac is saturated: CPU load above its cores, swap above 80% or memory below 5% for 5 heartbeats; swap 37/38 GB; top: com.apple.Virtualization.VirtualMachine 20 GB, postgres 2 GB (fleet), node 1 GB."
	if *findings[0].Text != want {
		t.Fatalf("text = %q, want %q", *findings[0].Text, want)
	}
}
