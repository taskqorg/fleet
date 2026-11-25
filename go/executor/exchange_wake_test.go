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
