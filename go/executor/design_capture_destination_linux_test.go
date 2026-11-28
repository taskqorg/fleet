//go:build linux

package main

import (
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fleet/contract"
)

func TestDesignCaptureAdvertisesOnlyTheResolvedSessionDestination(t *testing.T) {
	e := &executor{root: t.TempDir(), sessions: t.TempDir(), db: journal(t), host: "separate-linux-designer"}
	if !captureDestinationReady(e.root) {
		t.Fatal("the data filesystem must support the real publish preflight for this regression")
	}

	prepare := func(legacy bool) (execution, session, input string) {
		t.Helper()
		execution, session, command := newID(), newID(), newID()
		key := "fixture workspace"
		legacyDir := filepath.Join(e.root, "sessions", session)
		if legacy {
			if err := os.MkdirAll(legacyDir, 0700); err != nil {
				t.Fatal(err)
			}
		}
		dir := e.commandDir(session, key)
		input = filepath.Join(dir, "input")
		if err := os.MkdirAll(input, 0700); err != nil {
			t.Fatal(err)
		}
		raw := enc(M{"kind": "start", "id": command, "executionId": execution, "sessionId": session,
			"payload": M{"projectId": "fleet", "agentConfig": M{"id": "codex-designer"}, "workspaceKey": key}})
		if _, err := e.db.Exec("INSERT INTO commands(id,body) VALUES(?,?)", command, raw); err != nil {
			t.Fatal(err)
		}
		workers := workerDirFor(e.root, execution)
		if err := os.MkdirAll(workers, 0700); err != nil {
			t.Fatal(err)
		}
		spec := workerSpec{Kind: "harness", Execution: execution, CommandId: command, Session: session,
			DataDir: e.root, Dir: dir, InputDir: input}
		if err := os.WriteFile(filepath.Join(workers, "spec.json"), []byte(enc(spec)), 0600); err != nil {
			t.Fatal(err)
		}
		if !legacy {
			// A directory discovered after launch changes the legacy resolver's
			// answer, but it must not change the actual artifact_get destination.
			if err := os.MkdirAll(legacyDir, 0700); err != nil {
				t.Fatal(err)
			}
			if filepath.Join(e.commandDir(session, key), "input") == input {
				t.Fatal("fixture did not change the computed session path")
			}
		}
		return execution, session, input
	}
	newExec, newSession, newInput := prepare(false)
	oldExec, oldSession, oldInput := prepare(true)
	for _, unavailable := range []string{newInput, oldInput} {
		var visited []string
		got := e.captureStageCapabilities([]string{newExec, oldExec}, func(dir string) bool {
			visited = append(visited, dir)
			// Model a filesystem where link publication fails on one session
			// volume, even though the data root and other destination support it.
			return dir != unavailable && captureDestinationReady(dir)
		})
		if !slices.Equal(visited, []string{newInput, oldInput}) {
			t.Fatalf("capture checked the wrong destinations: %v", visited)
		}
		wanted := oldExec
		if unavailable == oldInput {
			wanted = newExec
		}
		if !slices.Equal(got, []string{contract.DesignCaptureStageCapability(wanted)}) {
			t.Fatalf("unsupported execution advertised staging: %v", got)
		}
	}
	if e.captureStageInput(newExec, newSession) != newInput || e.captureStageInput(oldExec, oldSession) != oldInput ||
		e.captureStageInput(newExec, oldSession) != "" {
		t.Fatal("the local render handoff did not bind the exact originating session and input")
	}
	for _, path := range []string{e.root, newInput, oldInput} {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if strings.HasPrefix(entry.Name(), ".fleet-capture-preflight-") {
				t.Fatalf("publication preflight left an entry in %s", path)
			}
		}
	}
}

func TestDesignCaptureDowngradedDestinationNeverSendsPermit(t *testing.T) {
	dest := filepath.Join(t.TempDir(), "input")
	if err := os.Mkdir(dest, 0700); err != nil {
		t.Fatal(err)
	}
	if !captureDestinationReady(dest) {
		t.Fatal("initial exact destination preflight failed")
	}
	client, broker := net.Pipe()
	defer broker.Close()
	done := make(chan error, 1)
	go func() {
		_ = broker.SetDeadline(time.Now().Add(2 * time.Second))
		if _, err := io.WriteString(broker, "FDCA"); err != nil {
			done <- err
			return
		}
		b := make([]byte, 1)
		n, err := broker.Read(b)
		if n != 0 {
			done <- errors.New("broker received permit framing after destination lost readiness")
			return
		}
		if !errors.Is(err, io.EOF) {
			done <- err
			return
		}
		done <- nil
	}()
	// The origin passed preflight earlier. At admission, its mount has been
	// replaced by a destination that the guarded publication path rejects.
	if err := os.Remove(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/proc", dest); err != nil {
		t.Fatal(err)
	}
	dial := func() (net.Conn, error) { return client, nil }
	deadline := time.Now().Add(time.Second)
	_, _, _, err := awaitCaptureBroker(dial, t.TempDir(), []byte("unspent fixture permit"), deadline, deadline, time.Millisecond,
		func() bool {
			return (captureUploadAuthority{OriginHost: contract.DesignCaptureHost, StageInput: dest}).redemptionReady(captureDestinationReady)
		})
	if !errors.Is(err, errCaptureStageUnavailable) {
		t.Fatalf("admission ignored destination downgrade: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
