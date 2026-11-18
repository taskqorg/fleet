package contract

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
)

// Requirement: TGT-3 retries a chunk after a dropped acknowledgement and surfaces an uncertain commit.
// Risk: a dropped response either skips bytes or silently repeats the non-idempotent provider call.
func TestResumableUploadRetriesOnlyStage(t *testing.T) {
	data := bytes.Repeat([]byte("m"), int(UploadChunkBytes)+18)
	var mu sync.Mutex
	var received bytes.Buffer
	var id string
	var starts []int64
	var commits int
	drop := func(w http.ResponseWriter) {
		conn, _, err := http.NewResponseController(w).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}

	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Header.Get(UploadModeHeader) {
		case "status":
			id = r.Header.Get(UploadIDHeader)
		case "chunk":
			if id == "" || r.Header.Get(UploadIDHeader) != id {
				t.Error("chunk lost upload ID")
			}
			offset, _ := strconv.ParseInt(r.Header.Get(UploadOffsetHeader), 10, 64)
			starts = append(starts, offset)
			if offset == int64(received.Len()) {
				if _, err := io.Copy(&received, r.Body); err != nil {
					t.Error(err)
				}
			} else {
				io.Copy(io.Discard, r.Body)
			}
			if len(starts) == 1 {
				drop(w)
				return
			}
		case "commit":
			commits++
			drop(w)
			return
		default:
			t.Error("unexpected upload mode")
			return
		}
		w.Header().Set(UploadOffsetHeader, strconv.Itoa(received.Len()))
		w.WriteHeader(http.StatusNoContent)
	}))
	defer gateway.Close()
	line, legacy := resumableUpload(context.Background(), GatewayTarget{URL: gateway.URL, Token: "test-token"}, "youtrack", "attach", map[string]string{}, Upload{File: bytes.NewReader(data), Size: int64(len(data))})
	if legacy || line.Outcome != CallOutcomeUnknown {
		t.Fatalf("commit got %s, legacy=%v", line.Outcome, legacy)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(starts) != 3 || starts[0] != 0 || starts[1] != 0 || starts[2] != UploadChunkBytes {
		t.Fatalf("chunk offsets %v", starts)
	}
	if commits != 1 || !bytes.Equal(received.Bytes(), data) {
		t.Fatalf("commits %d, bytes %d", commits, received.Len())
	}
}
