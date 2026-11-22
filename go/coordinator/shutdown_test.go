package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// Requirement: coordinator shutdown cancels background workers, closes long-lived stream/poll waits, lets short
// in-flight HTTP requests finish, and drains within a bounded timeout well under systemd's 30 s limit.
// Risk: uncancelled background loops or held SSE/long-poll/dispatch waits stall Shutdown and block store.Close until SIGKILL.
func TestShutdownDrainsQuickly(t *testing.T) {
	a := &App{}
	a.ensureRuntime()

	bgCtx, bgCancel := context.WithCancel(context.Background())
	var bg bgTracker
	bgCtx = withBackgroundTracker(bgCtx, &bg)
	restoreCoalesced := setCoalescedScope(bgCtx, &bg)
	defer restoreCoalesced()

	bctx, bcancel := context.WithCancel(bgCtx)
	a.stream = &Broadcast{
		subs:     map[chan struct{}]bool{},
		ctx:      bctx,
		cancel:   bcancel,
		done:     make(chan struct{}),
		feedWake: make(chan struct{}, 1),
	}
	close(a.stream.done)

	var bgExited atomic.Int32
	everyWake(bgCtx, "test-pass", time.Hour, nil, false, func(context.Context, bool) error { return nil })
	untilDone(bgCtx, "test-init", time.Hour, func(ctx context.Context) error {
		<-ctx.Done()
		bgExited.Add(1)
		return ctx.Err()
	})
	triggered := make(chan struct{})
	runCoalesced := coalesced("test-coalesced", func(ctx context.Context, _ struct{}) error {
		close(triggered)
		<-ctx.Done()
		bgExited.Add(1)
		return ctx.Err()
	})
	runCoalesced(struct{}{})
	<-triggered

	// Simulate an active stream subscriber and an active dispatch waiter.
	streamSub := a.stream.join()
	a.dispatchLoopStarted.Store(true)
	dispatchWaitDone := make(chan error, 1)
	go func() {
		dispatchWaitDone <- a.waitDispatchCaughtUp(context.Background(), 1)
	}()

	reqCtx, reqCancel := context.WithCancel(context.Background())
	defer reqCancel()

	inFlightStarted := make(chan struct{})
	inFlightStatus := make(chan int, 1)
	longPollStarted := make(chan struct{})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /inflight", func(w http.ResponseWriter, r *http.Request) {
		close(inFlightStarted)
		select {
		case <-time.After(40 * time.Millisecond):
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	mux.HandleFunc("GET /poll", func(w http.ResponseWriter, r *http.Request) {
		close(longPollStarted)
		select {
		case <-a.stoppedCh():
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:     mux,
		BaseContext: func(net.Listener) context.Context { return reqCtx },
	}
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(ln)
	}()

	client := &http.Client{Timeout: 5 * time.Second}
	defer client.CloseIdleConnections()

	go func() {
		resp, err := client.Get("http://" + ln.Addr().String() + "/inflight")
		if err != nil {
			inFlightStatus <- 0
			return
		}
		resp.Body.Close()
		inFlightStatus <- resp.StatusCode
	}()
	longPollStatus := make(chan int, 1)
	go func() {
		resp, err := client.Get("http://" + ln.Addr().String() + "/poll")
		if err != nil {
			longPollStatus <- 0
			return
		}
		resp.Body.Close()
		longPollStatus <- resp.StatusCode
	}()

	<-inFlightStarted
	<-longPollStarted

	start := time.Now()
	a.shutdown(srv, bgCancel, &bg, reqCancel, shutdownTimeouts{
		http:       2 * time.Second,
		background: 2 * time.Second,
		store:      time.Second,
	})
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Fatalf("shutdown took %v, want < 1s", elapsed)
	}
	if code := <-inFlightStatus; code != http.StatusOK {
		t.Fatalf("in-flight request got status %d, want 200", code)
	}
	if code := <-longPollStatus; code != http.StatusOK {
		t.Fatalf("long-poll request got status %d, want 200", code)
	}
	if err := <-dispatchWaitDone; err != nil {
		t.Fatalf("waitDispatchCaughtUp returned %v, want nil on shutdown", err)
	}
	select {
	case _, ok := <-streamSub:
		if ok {
			t.Fatal("stream subscription channel received value instead of closing")
		}
	default:
		t.Fatal("stream subscription channel was not closed on shutdown")
	}
	// Late stream joins after close must return an already-closed channel.
	select {
	case _, ok := <-a.stream.join():
		if ok {
			t.Fatal("late stream join channel was not closed")
		}
	default:
		t.Fatal("late stream join channel blocked after close")
	}
	if got := bgExited.Load(); got != 2 {
		t.Fatalf("background workers exited = %d, want 2", got)
	}
	if !waitBackground(&bg, 100*time.Millisecond) {
		t.Fatal("background tracker did not drain")
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		t.Fatalf("Serve returned %v, want http.ErrServerClosed", err)
	}
}
