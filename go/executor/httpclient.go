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
	return t
}()

func noRedirect(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
