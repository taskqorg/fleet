# Error tracking

## Overview

`errtrack` sends a Fleet service's unexpected errors and panics to Bugsink, Fleet's error tracker, in the Sentry protocol through `sentry-go` ([error tracking](../../docs/designs/fleet-error-tracking.md#3-server-errors)).

## Interface

- `Start(dsn, service, host, release)` initialises the client and sets the `obs` fail hook, so a panic in `obs.Handler` or `obs.Pass` and a failed `obs.Pass` become events tagged with the span name. `Init` initialises without the hook.
- `Go(name, fn)`: the one way a service starts a goroutine; a panic in it is sent (tag `span`) and raised again, so the process still ends and its service manager restarts it.
- `DSN()`: `FLEET_BUGSINK_DSN` when set (empty turns tracking off), else `bugsinkDsn` of `config/executor-defaults.json` next to the running binary; the executor, gateway and fleet-update read their DSN through it.
- `Capture(err, tags)` sends one error; `Panic(p, tags)` sends a recovered panic value and waits up to 2 s for delivery; `Flush(d)` waits for queued events; `Enabled` reports whether a client runs.

## Invariants

- An empty DSN leaves every call a no-op: tests and development send nothing.
- Events carry `release` (the Fleet version), `environment` production, `server_name` (the host id) and tag `service`; no personal data (`SendDefaultPII` off). A cancelled context is not an error.

## Side Effects

- With a DSN, events are sent over HTTPS to the DSN's host; `Panic` blocks its caller up to 2 s.

## Verification

Run from `go/`: `go test ./errtrack`
