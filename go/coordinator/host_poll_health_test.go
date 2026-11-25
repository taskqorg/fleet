package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPollCommandRefusal(t *testing.T) {
	for _, c := range []struct {
		err     error
		reason  string
		outcome pollCommandOutcome
	}{
		{fail(403, "Pipeline connection or tool is no longer granted"), "refused: Pipeline connection or tool is no longer granted", pollStopsCommand},
		{fail(404, "Not found"), "handoff failed: Not found", pollStopsCommand},
		{fail(409, "Pipeline plan changed before this transition"), "", pollRetriesCommand},
		{fail(500, "Delegation of a child execution not found"), "", pollRetriesCommand},
		{fail(503, "store unreachable"), "", pollRetriesCommand},
		{fail(429, "busy"), "", pollRetriesCommand},
		{fmt.Errorf("read: connection reset"), "", pollRetriesCommand},
		{&pgconn.PgError{Code: "23503"}, "", pollRetriesCommand},
		{&pgconn.PgError{Code: "42703"}, "", pollRetriesCommand},
		{&pgconn.PgError{Code: "23505"}, "", pollRetriesCommand},
		{&pgconn.PgError{Code: "40001"}, "", pollFails},
		{&pgconn.PgError{Code: "40P01"}, "", pollFails},
		{&pgconn.PgError{Code: "55P03"}, "", pollRetriesCommand},
		{&pgconn.PgError{Code: "22P02"}, "", pollRetriesCommand},
		{&pgconn.PgError{Code: "42501"}, "", pollRetriesCommand}, // in the unscoped poll, a missing grant is the coordinator's bug
		{fail(422, "Invalid stored input"), "", pollRetriesCommand},
		{fmt.Errorf("wrapped: %w", context.Canceled), "", pollFails},
	} {
		if reason, outcome := pollCommandRefusal(c.err); reason != c.reason || outcome != c.outcome {
			t.Errorf("%v: %q %v, want %q %v", c.err, reason, outcome, c.reason, c.outcome)
		}
	}
}

// Requirement: a waiting command is reported once per error class, its entry goes when it prepares or stops, and the
// map is bounded. Risk: error text with ids or times grows the dedup map without bound and floods Bugsink.
func TestCommandWaitsBoundAndCleanup(t *testing.T) {
	w := &commandWaits{m: map[string]string{}}
	a, b := fail(409, "Pipeline plan changed at revision 7"), fail(409, "Pipeline plan changed at revision 8")
	if !w.note("c1", errorClass(a)) || w.note("c1", errorClass(b)) {
		t.Fatal("the same status reported twice for varying text, or a new wait not reported")
	}
	if !w.note("c1", errorClass(&pgconn.PgError{Code: "55P03", Message: "lock at 12:00"})) {
		t.Fatal("a new error class was not reported")
	}
	w.forget("c1")
	if w.size() != 0 || !w.note("c1", errorClass(a)) {
		t.Fatal("a prepared command's entry stayed")
	}
	for i := 0; i < commandWaitsCap*2; i++ {
		w.note(fmt.Sprint("x", i), errorClass(a))
	}
	if n := w.size(); n > commandWaitsCap {
		t.Fatalf("map holds %d entries, cap %d", n, commandWaitsCap)
	}
}

func TestPollFailureOf(t *testing.T) {
	f, ok := pollFailureOf(&HTTPError{Code: 409, Msg: "Unsupported", Extra: doc{"reason": "pipeline_format_unsupported"}})
	if !ok || f.status != 409 || f.reason != "pipeline_format_unsupported" || !internalPollError(f) {
		t.Fatalf("409 %+v %v", f, ok)
	}
	if f, _ := pollFailureOf(&HTTPError{Code: 409, Msg: "x", Extra: doc{"reason": "Free Text <b>"}}); f.reason != "http_409" {
		t.Fatalf("free-text reason became %q", f.reason)
	}
	if f, ok := pollFailureOf(fail(400, "Invalid body")); !ok || f.reason != "http_400" || internalPollError(f) {
		t.Fatalf("400 %+v %v: recorded, but a client error, not for Bugsink", f, ok)
	}
	if _, ok := pollFailureOf(&HTTPError{Code: 410, Msg: "host-removed"}); ok {
		t.Fatal("a removed host recorded a poll failure")
	}
	if f, _ := pollFailureOf(fmt.Errorf("%s", strings.Repeat("é", 400))); f.status != 500 || f.reason != "internal" || len([]rune(f.message)) != hostPollMessageRunes || internalPollError(f) {
		t.Fatalf("500 %+v", f)
	}
}
