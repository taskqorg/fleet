# Contract generator

## Overview

`gen` converts one supported Fleet JSON Schema into Go declarations for the `contract` package.

## Interface

```sh
go run ./gen <schema.json> <package> <out.go>
```

`go/contract/contract.go` declares the repository's `go:generate` invocations for host protocol, gateway, profile, Lab registry, and operation schemas.

## Invariants

- The generator rejects schema keywords outside its supported subset instead of producing weaker types ([main.go](main.go)).
- It maps `$defs` to exported Go declarations, validates enum values and required fields, resolves canonical request references for tool discovery, and formats output with `go/format` ([main.go](main.go)).
- Generated files are committed outputs. Resolve generated-file conflicts by regenerating them, not by manual edits.

## Side effects

- Reads a schema and any referenced local schema files, then writes the requested Go output file ([main.go](main.go)).

## Verification

Run from `go/`:

```sh
go generate ./contract
git diff --exit-code -- contract
```
