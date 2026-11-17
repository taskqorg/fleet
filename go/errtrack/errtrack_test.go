package errtrack

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"fleet/obs"
)

// Requirement: fleet-error-tracking.md, server errors: without a DSN nothing is sent; with one, a failed pass and a
// panicking pass reach Bugsink as events with the service, the Fleet version as release and the span name, and the
// panic is re-raised. Risk: tests and development send events somewhere, or a service's failures never reach Bugsink.
func TestStart(t *testing.T) {
	if err := Start("", "coordinator", "h1", "1.0.0"); err != nil || Enabled() {
		t.Fatalf("no DSN: %v %v", err, Enabled())
	}
	Capture(errors.New("ignored"), nil) // a no-op without a client

	var mu sync.Mutex
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, r.URL.Path+"\n"+string(b))
		mu.Unlock()
	}))
	defer srv.Close()
	dsn := strings.Replace(srv.URL, "http://", "http://publickey@", 1) + "/42"
	if err := Start(dsn, "coordinator", "h1", "1.261117.3"); err != nil || !Enabled() {
		t.Fatalf("start: %v", err)
	}
	obs.Pass(context.Background(), "failing", func(context.Context) (int, error) { return 0, errors.New("pass broke") })
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic was swallowed")
			}
		}()
		obs.Pass(context.Background(), "panicking", func(context.Context) (int, error) { panic("pass panicked") })
	}()
	Flush(2 * time.Second)
	mu.Lock()
	defer mu.Unlock()
	all := strings.Join(bodies, "\n")
	for _, want := range []string{"/api/42/envelope/", "pass broke", "pass panicked", `"release":"1.261117.3"`, `"service":"coordinator"`, `"span":"pass.failing"`, `"span":"pass.panicking"`, `"server_name":"h1"`} {
		if !strings.Contains(all, want) {
			t.Fatalf("missing %s in %d envelopes:\n%s", want, len(bodies), all)
		}
	}
}
