# Gateway

## Overview

`gateway` reads provider API-key files to authenticate outbound provider calls. It also provides the Cloudflare Access forward-auth guard used by the Fleet web origin.

## File Map

- [main.go](main.go): CLI entrypoint (`selftest`, `serve`, `guard`), signal handling, and loopback listener checks.
- [gateway.go](gateway.go): config loading, gateway initialization, HTTP routing (`/healthz`, `/v1/usage`, `/v1/call/...`), typed health/usage reports, and graceful drain loop.
- [snapshot.go](snapshot.go): snapshot schema parsing, file polling and retention, key-value loading and verification, run-token authentication, and usable-key/budget checks.
- [spool.go](spool.go): durable UTC-day JSONL call spool (`fsync` on file and new directory entries), atomic file replacement, and persistent `spool-lost` accounting.
- [access.go](access.go), [sftp.go](sftp.go): project access connections (a provider with `transport`, [project access](../../docs/designs/fleet-project-access.md#how-agents-use-a-grant)): SSH login as the grant's OS user through pinned hops with the key and password files under `<FLEET_SECRET_DIR>/access` (sibling of `keys_dir`), MySQL and PostgreSQL reads in read-only transactions (`sql_query`, `sql_schema`), SFTP folder lists and file downloads (`files_list`, `files_get`), each call's spool `detail`.
- [proxy.go](proxy.go): outbound provider transport, stall/answer timeouts, retry/failover rules, response key redaction, path templating, Anthropic validation/token counting, and single-request file upload/download streaming.
- [upload_resume.go](upload_resume.go): resumable chunked upload staging, commit durability, discard recovery, and disk space reservations.
- [guard.go](guard.go): Caddy forward-auth verifier (`/verify`) for Cloudflare Access RS256 assertions.
- [design_rules_test.go](design_rules_test.go): S10 AST design-rule scan (`TestDesignRules`: `G-MAP`, `G-GO`, `G-PROC`) across non-test, non-generated `go/gateway/*.go` against `designGatewayOwned`.

## Interface

- `fleet-gateway selftest` checks snapshot parsing and date derivation.
- `fleet-gateway serve <config.json>` accepts authenticated `/v1/call/<connection>/<tool>` requests and health/usage routes on a loopback listener. `/healthz` reports the `limits` of each connection that has any and the file calls in flight (`transfers`); `/v1/usage` reports typed per-key provider usage or an explicit `unknown` reason.
- `fleet-gateway guard [127.0.0.1:9471]` serves `/verify` for Caddy forward authentication.
- Gateway snapshots use `contract.GatewaySnapshot`; provider calls produce durable `contract.CallLine` records.

## Invariants

- Listeners must be loopback TCP sockets, including systemd-activated sockets ([main.go](main.go)).
- A snapshot is adopted only when valid; the last valid copy remains usable after an invalid coordinator snapshot ([snapshot.go](snapshot.go)).
- A provider with `keyPath` (Telegram's `/bot`) carries the key in the URL path and sends no key header ([proxy.go](proxy.go) `providerURL`); transport errors, which hold that URL, are never logged or returned.
- Key files are selected by snapshot fingerprint and their values must hash to that fingerprint. Values are not returned to callers and are redacted from provider replies ([snapshot.go](snapshot.go), [proxy.go](proxy.go)).
- A `textReply` tool's 2xx reply that is not JSON reaches the caller as `{"text": ...}` after key redaction (Bugsink's Markdown stacktrace) ([proxy.go](proxy.go)).
- Redirects are not followed. A non-idempotent call with no provider answer is not retried; the outcome is `unknown` when a provider-side charge may have happened ([gateway.go](gateway.go), [proxy.go](proxy.go)).
- Anthropic uses fixed native Messages and Models routes. `anthropicPath` validates the supported request fields and refuses streaming; `anthropicTokens` counts reported tokens without pricing them. Messages 5xx completion stays unknown ([proxy.go](proxy.go), [operation scope](../../docs/designs/fleet-connections.md#anthropic-operations)).
- File tools (`file: upload|download`, [fleet-connections.md](../../docs/designs/fleet-connections.md)): the tool is resolved before the body is read. An upload body is the raw file: it is staged under `state_dir/files`, counted, hashed and scanned (with its name) for every key value the gateway holds and the caller's token, and only a whole file whose sha256 matches `X-Fleet-Sha256` is sent, as one multipart part, with the key. The scan is a tripwire, not data-loss prevention: an encoded or compressed copy passes. A download looks the file up with the key, fetches the reply's relative URL without the key, and streams the bytes unredacted; a short provider body aborts the caller's connection. Stall and answer timeouts come from the connection's `limits` ([proxy.go](proxy.go)).
- The resumable upload protocol stages chunks at confirmed offsets, including across a restart. Whole-file verification precedes a durable commit record and a two-phase spool entry. Available disk space accounts for staged bytes and concurrent chunk reservations. Interrupted chunks can be repeated and a commit with unknown outcome calls for attachment readback ([upload_resume.go](upload_resume.go)).
- A download writes two spool lines with one `callId`: `phase: started` (outcome `unknown`, `download-unfinished`), fsynced before the response headers, and `phase: completed` after the stream. On stop, uploads still staging are refused at once (503 `draining`, or a reset when the client is still sending; nothing reached the provider); posts in flight finish within the unchanged drain ([gateway.go](gateway.go), [proxy.go](proxy.go)).
- The spool acknowledges a call record only after its JSON line is fsynced. Spool-loss accounting is persisted separately when possible ([spool.go](spool.go)).
- The guard accepts only one RS256 Cloudflare Access assertion with the configured issuer, audience, required time claims, and identity. It returns 503 when signing keys cannot be obtained and 403 for invalid assertions ([guard.go](guard.go)).
- Unexpected errors and panics, a handler's panic included (`obs.Handler`), go to Bugsink through [`errtrack`](../errtrack/README.md) with `FLEET_BUGSINK_DSN`, as in the coordinator; goroutines start through `errtrack.Go`. [`design_rules_test.go`](design_rules_test.go) (`TestDesignRules`) mechanically enforces S10 rules (`G-MAP`, `G-GO`, `G-PROC`) across non-test, non-generated gateway code.

## Side effects

- Reads gateway snapshots and key files; retains valid snapshots, disabled-key state, and call spools under the configured state directory ([gateway.go](gateway.go), [snapshot.go](snapshot.go), [spool.go](spool.go), [proxy.go](proxy.go)).
- Stages raw uploads in `state_dir/files/<callId>.part` (0600), removed after the call and, for any left by a crash, at start. Resumable stages and commit records use `state_dir/resumable` and expire after 24 hours ([gateway.go](gateway.go), [proxy.go](proxy.go), [upload_resume.go](upload_resume.go)).
- Sends provider HTTP requests, records usage and key failures, and may disable provider-refused keys ([gateway.go](gateway.go), [proxy.go](proxy.go)).
- Fetches Cloudflare signing certificates and forwards a verified Fleet browser credential and normalized person identity to Caddy ([guard.go](guard.go)).

## Verification

Run from `go/`:

```sh
go test ./gateway
go run ./gateway selftest
```
