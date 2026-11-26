package main

// The coordinator reads at most 1 MiB of a request body (readBody); an event batch over it was answered 400 "Invalid
// JSON body" and set aside, so single large events (a thread.started with a long context handoff, an item.completed
// with a long tool output) were lost (2026-11-25, owner-mac and windows). Batches stay under maxRequestBytes, and an
// event over it has its longest strings cut, the cut recorded in the event's data (fleetTruncated).

import "fmt"

const maxRequestBytes = 1<<20 - 64<<10

// shrinkEvents cuts the longest strings of each event in a batch until the batch encodes under limit.
func shrinkEvents(value M, limit int) {
	events, _ := value["events"].([]any)
	if len(events) == 0 || len(enc(value)) <= limit {
		return
	}
	per := max(limit/len(events), 4<<10)
	for _, e := range events {
		ev, ok := e.(map[string]any)
		if !ok {
			continue
		}
		before := len(enc(ev))
		if before <= per {
			continue
		}
		for keep := 64 << 10; keep >= 256 && len(enc(ev)) > per; keep /= 2 {
			cutStrings(ev, keep)
		}
		if data, ok := ev["data"].(map[string]any); ok {
			data["fleetTruncated"] = M{"bytes": before, "kept": len(enc(ev))}
		}
	}
}

// cutStrings shortens every string longer than keep bytes in v, in place, to its head and a marker.
func cutStrings(v any, keep int) {
	switch x := v.(type) {
	case map[string]any:
		for k, y := range x {
			if s, ok := y.(string); ok && len(s) > keep {
				x[k] = cut(s, keep)
			} else {
				cutStrings(y, keep)
			}
		}
	case []any:
		for i, y := range x {
			if s, ok := y.(string); ok && len(s) > keep {
				x[i] = cut(s, keep)
			} else {
				cutStrings(y, keep)
			}
		}
	}
}

func cut(s string, keep int) string {
	head := s[:keep]
	for len(head) > 0 && head[len(head)-1]&0xC0 == 0x80 { // never split a UTF-8 sequence
		head = head[:len(head)-1]
	}
	if len(head) > 0 && head[len(head)-1] >= 0xC0 {
		head = head[:len(head)-1]
	}
	return head + fmt.Sprintf("\n[Fleet cut %d bytes: event over the request cap]", len(s)-len(head))
}
