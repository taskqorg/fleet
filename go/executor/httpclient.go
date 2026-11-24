package main

import (
	"net"
	"net/http"
	"time"
)

// httpClient is the one way the executor makes an HTTP client (docs/designs/fleet-standard.md S7). Redirects are never
// followed: requests carry the host token and the Access service token headers, which a redirect would hand on.
// timeout bounds a whole exchange (0: none). Clients share one pooled transport that keeps an idle connection per
// concurrent outbox lane (the default keeps 2 per host), so keep-alive avoids a TLS handshake per row.
func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, CheckRedirect: noRedirect, Transport: sharedTransport}
}

// streamClient: a client for long bodies (32 MiB store parts over a slow tunnel) with no whole-exchange bound, only
// a dial and a response-header bound.
func streamClient(dial, header time.Duration) *http.Client {
	t := sharedTransport.Clone()
	t.DialContext = (&net.Dialer{Timeout: dial, KeepAlive: 30 * time.Second}).DialContext
	t.ResponseHeaderTimeout = header
	return &http.Client{CheckRedirect: noRedirect, Transport: t}
}

var sharedTransport = func() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.MaxIdleConnsPerHost = 100
	// Every request to the coordinator shares one HTTP/2 connection through the edge. When that connection goes
	// half-dead (no reset arrives, e.g. after a network change), each request only times out its own stream and the next
	// one reuses the dead connection until TCP gives up, about 15 minutes: on 2026-11-24 owner-mac and windows each
	// lost the coordinator for 16 minutes. A ping after 15 s of silence, answered within 10 s, closes such a connection.
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: 15 * time.Second, PingTimeout: 10 * time.Second}
	return t
}()

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
