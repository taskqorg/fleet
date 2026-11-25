package main

import (
	"strings"
	"testing"

	"fleet/contract"
)

// Requirement: db_explain shows a view's statements as the app runs them, under RLS, with their plans, for full
// access only and for view operations only. Risk: a plan is taken as postgres (RLS skipped, plans 3-5x optimistic), a
// caller explains arbitrary SQL or a write, or a failed EXPLAIN aborts the rest.
func TestDbExplain(t *testing.T) {
	e := openTestCoordinator(t, false)
	var got contract.DbExplainReply
	e.opInto(t, "db_explain", map[string]any{"view": "home_view"}, e.ownerHeaders(), &got)
	if got.View != "home_view" || len(got.Statements) < 3 {
		t.Fatalf("home_view statements: %+v", got)
	}
	explained := 0
	for _, s := range got.Statements {
		if s.Calls < 1 || s.Sql == "" {
			t.Fatalf("statement: %+v", s)
		}
		if s.Error == nil && strings.Contains(s.Plan, "Execution Time") {
			explained++
		}
	}
	if explained < 3 {
		t.Fatalf("plans with ANALYZE: %d of %d", explained, len(got.Statements))
	}
	for _, view := range []string{"questions_list", "db_explain", "message_send", "nope"} {
		if code, _ := e.callJSON(t, "POST", "/api/ops/db_explain", map[string]any{"view": view}, e.ownerHeaders()); code != 400 {
			t.Fatalf("%s: %d, want 400", view, code)
		}
	}
}
