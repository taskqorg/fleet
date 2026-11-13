# Go services

## Overview

This module builds Fleet's coordinator, executor, connection gateway, updater, shared contract types, and observability support. It is one Go module; generated contract files are committed outputs of the schemas in `../contracts/`.

## Interfaces and boundaries

- [contract](contract/README.md): typed JSON contracts shared by the coordinator, executor, and gateway.
- [coordinator](coordinator/README.md): HTTP control-plane service and durable Fleet state.
- [executor](executor/README.md): host-side command runner, native harness adapter, and local journal.
- [gateway](gateway/README.md): loopback-only provider-key gateway and Cloudflare Access guard.
- [gen](gen/README.md): contract-schema-to-Go generator.
- [obs](obs/README.md): trace spans, HTTP instrumentation, and ClickHouse sink.
- [update](update/README.md): verified native and UI release installer.

## Invariants

- `contract/*_gen.go` is generated from `contracts/*.schema.json`; regenerate it with `go generate ./contract`, never edit it by hand ([contract/contract.go](contract/contract.go)).
- The module builds with `CGO_ENABLED=0`; the project contract requires a stop-and-ask before introducing C dependencies ([fleet.md](../fleet.md#stack)).
- Dependency resolution is read-only (`-mod=readonly`) and all binaries use the single `go.mod` and `go.sum`.

## Side Effects

- The module's binaries run services, execute host work, route provider calls, and install releases; their package contracts name the narrower mutation boundaries.

## Verification

Run from this directory:

```sh
go build ./contract ./gen ./obs
CGO_ENABLED=0 go build -o bin/fleet-coordinator ./coordinator
CGO_ENABLED=0 go build -o bin/fleet-executor ./executor
CGO_ENABLED=0 go build -o bin/fleet-gateway ./gateway
CGO_ENABLED=0 go build -o bin/fleet-update ./update
```

The full fallback gate is CI-only; see [Fleet verification](../fleet.md#verification).
