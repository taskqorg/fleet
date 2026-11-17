// Package errtrack sends a Fleet service's unexpected errors and panics to Bugsink, Fleet's error tracker
// (docs/designs/fleet-error-tracking.md), with the Sentry protocol through sentry-go. Without a DSN every call is a
// no-op, so tests and development need nothing. Events carry the service, the host and the Fleet version as release;
// no personal data is sent (SendDefaultPII stays off).
package errtrack

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"fleet/obs"

	"github.com/getsentry/sentry-go"
)

var enabled atomic.Bool

// Init starts the client for dsn; an empty dsn leaves errtrack off. service is the component (coordinator, executor),
// host the Fleet host id, release the Fleet version.
func Init(dsn, service, host, release string) error {
	if dsn == "" {
		return nil
	}
	err := sentry.Init(sentry.ClientOptions{
		Dsn: dsn, Release: release, Environment: "production", ServerName: host,
		AttachStacktrace: true, SendDefaultPII: false, SampleRate: 1,
	})
	if err != nil {
		return fmt.Errorf("errtrack: %w", err)
	}
	sentry.ConfigureScope(func(s *sentry.Scope) { s.SetTag("service", service) })
	enabled.Store(true)
	return nil
}

// Start is Init plus the obs hook: a panic in an HTTP handler or a pass, and a failed pass, become events tagged with
// the span name. A failed Init is returned and leaves errtrack off.
func Start(dsn, service, host, release string) error {
	if err := Init(dsn, service, host, release); err != nil || !Enabled() {
		return err
	}
	obs.SetFailHook(func(name string, p any, err error) {
		if p != nil {
			Panic(p, map[string]string{"span": name})
		} else {
			Capture(err, map[string]string{"span": name})
		}
	})
	return nil
}

// Enabled reports whether events are sent.
func Enabled() bool { return enabled.Load() }

// Capture sends err with tags (the route, the pass); context.Canceled is the caller leaving, not an error.
func Capture(err error, tags map[string]string) {
	if !enabled.Load() || err == nil || errors.Is(err, context.Canceled) {
		return
	}
	sentry.WithScope(func(s *sentry.Scope) {
		for k, v := range tags {
			s.SetTag(k, v)
		}
		sentry.CaptureException(err)
	})
}

// Panic sends a recovered panic value with tags and waits briefly for delivery, since the process may end with it.
// Call it from the deferred recover; the caller re-panics as before.
func Panic(p any, tags map[string]string) {
	if !enabled.Load() || p == nil {
		return
	}
	sentry.WithScope(func(s *sentry.Scope) {
		for k, v := range tags {
			s.SetTag(k, v)
		}
		sentry.CurrentHub().RecoverWithContext(context.Background(), p)
	})
	sentry.Flush(2 * time.Second)
}

// Flush waits up to d for queued events (before exit).
func Flush(d time.Duration) {
	if enabled.Load() {
		sentry.Flush(d)
	}
}
