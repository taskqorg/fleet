package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// heldPollFake is a fake coordinator that holds ?wait= polls until release (or the client's cancel) and answers
// every other poll at once. Cleanup releases any held poll.
type heldPollFake struct {
	held     atomic.Int32
	canceled chan struct{}
	release  func()
}

func newHeldPoll(t *testing.T, e *executor) *heldPollFake {
	f := &heldPollFake{canceled: make(chan struct{}, 1)}
	gate := make(chan struct{})
	var once sync.Once
	f.release = func() { once.Do(func() { close(gate) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != pollRoute {
			w.WriteHeader(404)
			return
		}
		io.Copy(io.Discard, r.Body) // the server notices a client cancel only after the body is read
		if strings.Contains(r.URL.RawQuery, "wait=") {
			f.held.Add(1)
			select {
			case <-gate:
			case <-r.Context().Done():
				select {
				case f.canceled <- struct{}{}:
				default:
				}
				return
			}
		}
		json.NewEncoder(w).Encode(M{"commands": []any{}})
	}))
	t.Cleanup(server.Close)
	t.Cleanup(f.release) // runs before server.Close
	e.url, e.token = server.URL, "fixture"
	return f
}

// waitHeld returns once the exchange's poll is held by the fake coordinator.
func (f *heldPollFake) waitHeld(t *testing.T) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); f.held.Load() == 0 && time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
	}
	if n := f.held.Load(); n != 1 {
		t.Fatalf("held polls = %d", n)
	}
}

func (f *heldPollFake) waitCanceled(t *testing.T) {
	t.Helper()
	select {
	case <-f.canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("held poll not canceled")
	}
}

func waitReceipt(e *executor, cid, want string, within time.Duration) bool {
	for end := time.Now().Add(within); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		if lastReceipt(e, cid) == want {
			return true
		}
	}
	return false
}

// runScript starts a script execution and stops its process at cleanup.
func runScript(t *testing.T, e *executor, id string, payload M) {
	t.Helper()
	if err := e.run(M{"id": id, "executionId": id, "sessionId": id, "kind": "script", "payload": payload}); err != nil {
		t.Fatal(err)
	}
	l := e.live[id]
	t.Cleanup(func() { l.cleanup(); <-l.exited })
}

// Requirement: a held long poll does not stop the 100 ms process loop (executor README, Side effects).
// Risk: a process that exits, or passes its deadline, while the coordinator holds the poll for up to 25 s is
// observed only when the poll returns, so every terminal receipt waits for the next command or the 25 s answer.
func TestHeldPollObservesExitAndDeadline(t *testing.T) {
	e, _ := scriptExecutor(t)
	f := newHeldPoll(t, e)
	runScript(t, e, "quick", M{"command": `printf saved > "$FLEET_OUTPUT_DIR/result.txt"`})
	runScript(t, e, "late", M{"timeout_ms": 200, "command": "sleep 30"})
	returned := make(chan error, 1)
	go func() { returned <- e.exchange() }()
	f.waitHeld(t)
	began := time.Now()
	exited := waitReceipt(e, "quick", "completed", 8*time.Second)
	exitedAfter := time.Since(began)
	timedOut := waitReceipt(e, "late", "failed", 8*time.Second)
	select {
	case err := <-returned:
		t.Fatalf("exchange returned before the held poll was answered: %v", err)
	default:
	}
	f.release()
	if err := <-returned; err != nil {
		t.Fatal(err)
	}
	if !exited || !timedOut {
		t.Fatalf("while the poll was held: exit receipt %v, deadline receipt %v", exited, timedOut)
	}
	if exitedAfter > 5*time.Second { // ticks plus process start on a loaded runner; the old wait was longPollWait
		t.Fatalf("exit receipt after %v", exitedAfter)
	}
	if n := f.held.Load(); n != 1 {
		t.Fatalf("held polls = %d, want the one exchange poll", n)
	}
	if len(e.live) != 0 {
		t.Fatalf("live after exchange = %v", len(e.live))
	}
	t.Logf("exit receipt %v after the held poll began", exitedAfter.Round(time.Millisecond))
}

// Requirement: a process-loop failure during the held poll ends exchange at once as a fatal tick failure, cancels
// the poll and leaves no poll goroutine.
// Risk: the error waits for the coordinator's answer, or the abandoned request keeps the host's poll slot.
func TestHeldPollTickFailureCancelsPoll(t *testing.T) {
	e, _ := scriptExecutor(t)
	f := newHeldPoll(t, e)
	runScript(t, e, "busy", M{"command": "sleep 30"})
	returned := make(chan error, 1)
	go func() { returned <- e.exchange() }()
	f.waitHeld(t)
	e.db.Close() // the next tick's journal read fails
	select {
	case err := <-returned:
		var failed tickFailed
		if !errors.As(err, &failed) || !strings.Contains(failed.Unwrap().Error(), "closed") {
			t.Fatalf("exchange error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("tick failure waited for the held poll")
	}
	f.waitCanceled(t)
}

// Requirement: shutdown cancels the held poll; the loop is not left ticking until the coordinator answers.
// Risk: exchange returns but the abandoned request keeps the host's poll slot.
func TestHeldPollStopsOnShutdown(t *testing.T) {
	e, _ := scriptExecutor(t)
	f := newHeldPoll(t, e)
	stopCtx, cancelStop := context.WithCancel(context.Background())
	e.stopCtx = stopCtx
	returned := make(chan error, 1)
	go func() { returned <- e.exchange() }()
	f.waitHeld(t)
	cancelStop()
	select {
	case err := <-returned:
		if err == nil || errors.Is(err, errHostRemoved) {
			t.Fatalf("exchange error after shutdown = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown waited for the held poll")
	}
	f.waitCanceled(t)
}
