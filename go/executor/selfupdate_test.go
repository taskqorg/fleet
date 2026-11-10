package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPollSignal(t *testing.T) {
	for _, c := range []struct {
		a    answer
		ok   bool
		perr string
	}{
		{answer{kind: answerOK, status: 200}, true, ""},
		{answer{kind: answerRetry}, false, "network"},
		{answer{kind: answerEdge, status: 302}, false, "network"},
		{answer{kind: answerRetry, status: 503}, false, "http-503"},
		{answer{kind: answerEdge, status: 401}, false, "http-401"},
	} {
		if ok, perr := pollSignal(c.a); ok != c.ok || perr != c.perr {
			t.Errorf("%+v: %v %q", c.a, ok, perr)
		}
	}
}

func TestUpdateSignalWrittenOnChange(t *testing.T) {
	root := t.TempDir()
	lastSignal = nil
	t.Cleanup(func() { lastSignal = nil })
	file := filepath.Join(root, "update", "update.json")
	target := "1.261112.0"
	s := updateSignal{Target: &target, AutoUpdate: "on", Running: "1.261111.0", PollOk: true}
	if err := writeUpdateSignal(root, s); err != nil {
		t.Fatal(err)
	}
	os.Remove(file)
	same := "1.261112.0"
	s.Target = &same
	writeUpdateSignal(root, s)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("rewritten without a change")
	}
	s.PollOk, s.PollError = false, "network"
	writeUpdateSignal(root, s)
	if _, err := os.Stat(file); err != nil {
		t.Fatal("not written on change")
	}
}

func TestLastUpdate(t *testing.T) {
	root := t.TempDir()
	if lastUpdate(root) != nil {
		t.Fatal("no state file")
	}
	os.MkdirAll(filepath.Join(root, "update"), 0o700)
	os.WriteFile(filepath.Join(root, "update", "update-state.json"), []byte(`{"last":{"version":"1.261111.0","state":"ok","at":"x"}}`), 0o600)
	if m, ok := lastUpdate(root).(M); !ok || m["state"] != "ok" {
		t.Fatalf("%v", lastUpdate(root))
	}
}
