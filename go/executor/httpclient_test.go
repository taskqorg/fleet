package main

import (
	"net/http"
	"testing"
)

// Requirement: a half-dead HTTP/2 connection to the coordinator is detected and closed within seconds.
// Risk: 2026-11-24 owner-mac and windows each lost the coordinator for 16 minutes (every poll timed out on a
// connection TCP had not given up yet); streamClient's clone must keep the same health check.
func TestTransportPingsHTTP2(t *testing.T) {
	stream := streamClient(0, 0).Transport.(*http.Transport)
	for name, tr := range map[string]*http.Transport{"shared": sharedTransport, "stream": stream} {
		if tr.HTTP2 == nil || tr.HTTP2.SendPingTimeout <= 0 || tr.HTTP2.PingTimeout <= 0 {
			t.Fatalf("%s transport has no HTTP/2 ping health check: %+v", name, tr.HTTP2)
		}
	}
}
