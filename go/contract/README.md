# Contract

## Overview

`contract` provides the generated JSON-schema types used by the coordinator, executor, and gateway, plus the cross-schema validation that code generation cannot express.

## Interface

- Generated `*_gen.go` files define request, reply, event, profile, gateway, host-protocol, Lab, and operation-domain types. Every generated struct exposes `Validate`; generated enums expose `Valid`.
- `Operations` registers every generated `ops-*.schema.json` operation with its read/long flags, permitted status codes, and input schema ([contract.go](contract.go)).
- `ParseProfile` and `ParseAntigravitySettings` strictly decode supported agent profiles and settings.
- `ParseContextSources` and `ContextSources` validate immutable repository-rule references.
- `GatewayCall` makes one JSON gateway call and returns a `CallLine`, including `ok`, `error`, or `unknown`; it does not return a Go error. `GatewayTarget` names the gateway URL, bearer, extra headers and the tool's `idempotent` flag.
- `GatewayUpload` sends an opened file's raw bytes to a `file: upload` tool; `GatewayDownload` writes a `file: download` tool's stream into a local folder and `SafeName` makes the provider's file name one path element ([gateway_file.go](gateway_file.go), [fleet-connections.md §9](../../docs/designs/fleet-connections.md#9-files)).

## Invariants

- `strict` rejects unknown JSON fields and null except for optional strings; `Check` preserves nested field paths in validation errors ([profile.go](profile.go)).
- Context sources require unique repository IDs, a full 40-hex revision, nonempty core files, safe unique relative paths, and SHA-256 digests. They cannot name commands, credentials, or caller-selected host paths ([context_sources.go](context_sources.go)).
- Profile validation is shared: a profile accepted by the coordinator is checked again before the executor spawns it. Harness-specific grants and mapped Antigravity settings are restricted ([profile.go](profile.go)).
- Gateway calls have no total deadline: `FLEET_STALL_MS` (default 60 s) bounds each wait for progress and a JSON call's header wait, `FLEET_ANSWER_MS` (default 180 s) the header wait after a file request's last byte. They distinguish failures that leave the provider-side outcome unknown from failures known not to have sent a request: the gateway's `x-fleet-outcome` decides; a 5xx without `x-fleet-call-id` (the edge) is `edge-<status>`, `unknown` unless the tool is idempotent; a cut before an upload's last byte is `upload-interrupted` ([gateway_call.go](gateway_call.go)).
- A download is written inside an `os.Root` of the trusted root: each folder below it is created 0700 or refused when it is a symlink, the file goes to `<name>.<random>.part` (`O_EXCL`, 0600), is counted against `Content-Length`, hashed, fsynced and renamed in the same folder; any failure removes the part file ([gateway_file.go](gateway_file.go)).

## Side effects

- `GatewayCall` sends an authenticated HTTP POST to `fleet-gateway`; its provider output is retained in the returned call line ([gateway_call.go](gateway_call.go)).
- `GatewayUpload` reads the caller's open file; `GatewayDownload` creates folders and writes files under the caller's root ([gateway_file.go](gateway_file.go)).
- Package initialization populates the process-global `Operations` map and panics if two generated schemas register one operation name ([contract.go](contract.go)).

## Verification

Run from `go/`:

```sh
go generate ./contract
go test ./contract
```
