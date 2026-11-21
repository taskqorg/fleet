package main

import (
	"net/http"
	"time"
)

// httpClient is the sole HTTP client builder for fleet-update (docs/designs/fleet-standard.md S10).
// Redirects remain enabled because GitHub Releases redirects release tarball and latest.json downloads.
func httpClient(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout}
}
