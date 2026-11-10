package main

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Requirement: the change feed observes failed writes and bounds a blocked write, while an idle stream stays open (SL8).
// Risk: a connected client that stops reading pins the stream handler and its subscription forever.
func TestSSEBlockedWriteEnds(t *testing.T) {
	defer func(d time.Duration) { streamWriteWait = d }(streamWriteWait)
	streamWriteWait = 200 * time.Millisecond
	ended := make(chan error, 1)
	bursts := 0 // successful bursts after the idle wait
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := newSSE(w)
		sw.send("ready", map[string]int64{"cursor": 0}, -1)
		if err := sw.flush(); err != nil {
			ended <- err
			return
		}
		time.Sleep(3 * streamWriteWait) // idle longer than one write bound: the stream must still be open
		big := strings.Repeat("x", 1<<16)
		for {
			sw.begin()
			sw.send("", big, 1)
			if err := sw.flush(); err != nil {
				ended <- err
				return
			}
			bursts++
		}
	}))
	defer srv.Close()
	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.Write([]byte("GET / HTTP/1.1\r\nHost: x\r\n\r\n"))
	br := bufio.NewReader(conn)
	for line := ""; !strings.HasPrefix(line, "data: "); {
		if line, err = br.ReadString('\n'); err != nil {
			t.Fatal(err)
		}
	}
	select { // the client now never reads again
	case err := <-ended:
		if err == nil || bursts == 0 {
			t.Fatalf("handler ended with %v after %d bursts; want a write error after the idle stream wrote again", err, bursts)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("a write to a client that stopped reading never ended")
	}
}
