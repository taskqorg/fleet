package main

import (
	"testing"
)

// Requirement: a resumed Codex thread receives Fleet's developer instructions once; the same text is not appended
// again on the next resume, changed text is.
// Risk: 2026-11-25 HAL0 (C-FLT-592) appended the same ~46 KB instructions on every 5-minute wake; each copy stayed in
// the native history, so context grew ~10k tokens per turn to ~400k tokens per "no changes" reply.
func TestCodexResumeInjectsInstructionsOnce(t *testing.T) {
	marks := t.TempDir()
	open := func(native, text string) (first string, s *codexSession) {
		t.Helper()
		s = &codexSession{profile: M{"agentConfig": M{"instructions": text}, "text": "task"}, root: "/work", resume: native, marks: marks}
		if _, _, _, err := s.Receive(M{"id": 1, "result": M{}}); err != nil {
			t.Fatal(err)
		}
		thread := native
		if thread == "" {
			thread = "th-1"
		}
		frames, _, _, err := s.Receive(M{"id": 2, "result": M{"thread": M{"id": thread}}})
		if err != nil || len(frames) != 1 {
			t.Fatal(frames, err)
		}
		if m, _ := frames[0]["method"].(string); m == "thread/inject_items" {
			if _, _, _, err := s.Receive(M{"id": 4, "result": M{}}); err != nil {
				t.Fatal(err)
			}
		}
		m, _ := frames[0]["method"].(string)
		return m, s
	}
	if m, _ := open("", "rules A"); m != "turn/start" {
		t.Fatalf("new thread first frame = %s, want turn/start", m)
	}
	if m, _ := open("th-1", "rules A"); m != "turn/start" {
		t.Fatalf("resume with the same instructions sends %s, want turn/start without a second copy", m)
	}
	if m, _ := open("th-1", "rules B"); m != "thread/inject_items" {
		t.Fatalf("resume with changed instructions sends %s, want thread/inject_items", m)
	}
	if m, _ := open("th-1", "rules B"); m != "turn/start" {
		t.Fatalf("resume after the injection sends %s, want turn/start", m)
	}
	// A thread with no mark (from before marks, or another executor root) still gets the instructions.
	if m, _ := open("th-unknown", "rules B"); m != "thread/inject_items" {
		t.Fatalf("resume of an unmarked thread sends %s, want thread/inject_items", m)
	}
}
