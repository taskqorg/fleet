package contract

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
)

type uploadTestTransport func(*http.Request) (*http.Response, error)

func (f uploadTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Requirement: TGT-3 resumes even when a stalled chunk times out without an explicit network reset.
// Risk: the per-request stall timer spends all of the reconnect window and the client abandons a recoverable stage.
func TestResumableUploadRetriesAfterWatchdog(t *testing.T) {
	t.Setenv("FLEET_STALL_MS", "120")
	saved := gatewayClient
	t.Cleanup(func() { gatewayClient = saved })
	chunks := 0
	gatewayClient = &http.Client{Transport: uploadTestTransport(func(r *http.Request) (*http.Response, error) {
		h := make(http.Header)
		h.Set(UploadOffsetHeader, "0")
		switch r.Header.Get(UploadModeHeader) {
		case "chunk":
			chunks++
			if chunks == 1 {
				<-r.Context().Done()
				return nil, r.Context().Err()
			}
			h.Set(UploadOffsetHeader, "1")
		case "commit":
			return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader([]byte("{}")))}, nil
		}
		return &http.Response{StatusCode: 204, Header: h, Body: http.NoBody}, nil
	})}
	line, _ := resumableUpload(context.Background(), GatewayTarget{URL: "https://gateway.invalid", Token: "test-token"}, "youtrack", "attach", map[string]string{}, Upload{File: bytes.NewReader([]byte{1}), Size: 1})
	if chunks != 2 || line.Outcome != CallOutcomeOk {
		t.Fatalf("after watchdog got %s with %d chunk attempts", line.Outcome, chunks)
	}
}
