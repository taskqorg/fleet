package main

import (
	"strings"
	"testing"
	"time"

	"fleet/coordinator/db"
)

// A new work item's room history keeps the newest messages within its bound, oldest first, and is empty without any.
func TestRoomHistoryText(t *testing.T) {
	if roomHistoryText("Billing", nil) != "" {
		t.Fatal("no messages: no handoff")
	}
	at := time.Date(2026, 11, 4, 12, 33, 0, 0, time.UTC)
	rows := []db.TelegramRoomHistoryRow{}
	for i := 0; i < 20; i++ {
		rows = append(rows, db.TelegramRoomHistoryRow{At: at.Add(time.Duration(i) * time.Minute), Author: "Alex K", Text: strings.Repeat("x", 2000) + string(rune('a'+i))})
	}
	got := roomHistoryText("Billing", rows)
	if !strings.Contains(got, "«Billing»") || !strings.Contains(got, "untrusted context") {
		t.Fatalf("header: %.200s", got)
	}
	if !strings.Contains(got, "xt\n") && !strings.HasSuffix(got, "xt") {
		t.Fatal("the newest message is kept")
	}
	if strings.Contains(got, "xa\n") || strings.HasSuffix(got, "xa") {
		t.Fatal("the oldest message falls out of the bound")
	}
	if strings.Index(got, "12:52 UTC") < strings.Index(got, "12:51 UTC") {
		t.Fatal("oldest first")
	}
	if n := len([]rune(got)); n > roomHistoryRunes+400 {
		t.Fatalf("bound: %d runes", n)
	}
}
