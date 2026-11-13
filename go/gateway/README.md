# Gateway

## Overview

`gateway` is the only runtime process that reads provider API-key values. It also provides the Cloudflare Access forward-auth guard used by the Fleet web origin.

## Interface

- `fleet-gateway selftest` checks snapshot parsing and date derivation.
- `fleet-gateway serve <config.json>` accepts authenticated `/v1/call/<connection>/<tool>` requests and health/usage routes on a loopback listener.
- `fleet-gateway guard [127.0.0.1:9471]` serves `/verify` for Caddy forward authentication.
- Gateway snapshots use `contract.GatewaySnapshot`; provider calls produce durable `contract.CallLine` records.

## Invariants

- Listeners must be loopback TCP sockets, including systemd-activated sockets ([main.go](main.go)).
- A snapshot is adopted only when valid; the last valid copy remains usable after an invalid coordinator snapshot ([gateway.go](gateway.go)).
- Key files are selected by snapshot fingerprint and their values must hash to that fingerprint. Values are not returned to callers and are redacted from provider replies ([gateway.go](gateway.go)).
- Redirects are not followed. A non-idempotent call with no provider answer is not retried; the outcome is `unknown` when a provider-side charge may have happened ([gateway.go](gateway.go)).
- The spool acknowledges a call record only after its JSON line is fsynced. Spool-loss accounting is persisted separately when possible ([gateway.go](gateway.go)).
- The guard accepts only one RS256 Cloudflare Access assertion with the configured issuer, audience, required time claims, and identity. It returns 503 when signing keys cannot be obtained and 403 for invalid assertions ([guard.go](guard.go)).

## Side effects

- Reads gateway snapshots and key files; retains valid snapshots, disabled-key state, and call spools under the configured state directory ([gateway.go](gateway.go)).
- Sends provider HTTP requests, records usage and key failures, and may disable provider-refused keys ([gateway.go](gateway.go)).
- Fetches Cloudflare signing certificates and forwards a verified Fleet browser credential and normalized person identity to Caddy ([guard.go](guard.go)).

## Verification

Run from `go/`:

```sh
go test ./gateway
go run ./gateway selftest
```
