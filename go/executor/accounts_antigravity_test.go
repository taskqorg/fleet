package main

import "testing"

// Requirement: agy /usage (agy 1.5.10, live answer of 2026-11-25) becomes limit windows: used percent, period, model
// group and reset; an unstarted window (reset at the epoch) has no reset time; other lines are ignored.
// Risk: the Antigravity quota stays unknown, or remaining is shown as used.
func TestAntigravityUsageWindows(t *testing.T) {
	text := "Gemini Models\tWeekly Limit Remaining\t64%\t2026-12-01T00:44:59Z\n" +
		"Gemini Models\tFive Hour Limit Remaining\t100%\t2026-11-25T11:21:16Z\n" +
		"Claude and GPT models\tWeekly Limit Remaining\t100%\t1970-01-01T00:00:00Z\n" +
		"Some banner line\n"
	w := antigravityUsageWindows(text)
	if len(w) != 3 {
		t.Fatalf("windows: %+v", w)
	}
	if w[0].Period != "weekly" || *w[0].Pct != 36 || *w[0].Model != "gemini-models" || w[0].ResetsAt == nil || *w[0].ResetsAt != 1796085899000 {
		t.Fatalf("gemini weekly: %+v", w[0])
	}
	if w[1].Period != "5h" || *w[1].Pct != 0 {
		t.Fatalf("gemini 5h: %+v", w[1])
	}
	if *w[2].Model != "claude-and-gpt-models" || w[2].ResetsAt != nil {
		t.Fatalf("claude weekly: %+v", w[2])
	}
	if len(antigravityUsageWindows("Remaining credits\t0\n")) != 0 {
		t.Fatal("a credits line is not a window")
	}
}
