# Contract

## Overview

`contract` provides the generated JSON-schema types used by the coordinator, executor, and gateway, plus the cross-schema validation that code generation cannot express.

## Interface

- Generated `*_gen.go` files define request, reply, event, profile, gateway, host-protocol, Lab, and operation-domain types. Every generated struct exposes `Validate`; generated enums expose `Valid`.
- `Operations` registers every generated `ops-*.schema.json` operation with its read/long flags, permitted status codes, and input schema ([contract.go](contract.go)).
- `ParseProfile` and `ParseAntigravitySettings` strictly decode supported agent profiles and settings.
- `ParseContextSources` and `ContextSources` validate immutable repository-rule references.
- `GatewayCall` makes one gateway call and returns a `CallLine`, including `ok`, `error`, or `unknown`; it does not return a Go error.

## Invariants

- `strict` rejects unknown JSON fields and null except for optional strings; `Check` preserves nested field paths in validation errors ([profile.go](profile.go)).
- Context sources require unique repository IDs, a full 40-hex revision, nonempty core files, safe unique relative paths, and SHA-256 digests. They cannot name commands, credentials, or caller-selected host paths ([context_sources.go](context_sources.go)).
- Profile validation is shared: a profile accepted by the coordinator is checked again before the executor spawns it. Harness-specific grants and mapped Antigravity settings are restricted ([profile.go](profile.go)).
- `GatewayCall` uses a 60-second request context. It distinguishes failures that leave provider-side outcome unknown from failures known not to have sent a request ([gateway_call.go](gateway_call.go)).

## Side effects

- `GatewayCall` sends an authenticated HTTP POST to `fleet-gateway`; its provider output is retained in the returned call line ([gateway_call.go](gateway_call.go)).
- Package initialization populates the process-global `Operations` map and panics if two generated schemas register one operation name ([contract.go](contract.go)).

## Verification

Run from `go/`:

```sh
go generate ./contract
go test ./contract
```
