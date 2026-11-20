# mcpserve

## Overview

The one MCP tools server over Fleet operations ([Fleet standard](../../docs/designs/fleet-standard.md) S8). The coordinator's `/api/tools`, `/api/project-tools` and `/api/hosts/agent-tools` serve through it.

## Interfaces and boundaries

- `Server{Name, Tools, Call}.Handle(body)`: answers one JSON-RPC request. `initialize` names the server; `tools/list` publishes `Tools`, each with the `description` and `inputSchema` of its `contract.Operations` entry; `tools/call` hands the name and arguments to `Call` and wraps its reply as one JSON text content. Any other method returns `ErrUnknownMethod`.
- `Parse(body)`: the JSON-RPC request, for a caller that binds a call before handling it.
- `List(names)`, `Result(id, out)`: the `tools/list` entries and the `tools/call` result, for a caller that answers outside `Handle`.

## Invariants

- Tool descriptions and input schemas come only from `contracts/ops-*.schema.json`; a server chooses which operations it publishes and accepts, never how they are described.
- `Call` decides which names it accepts and returns its own error; `Handle` adds no authorization of its own.

## Side Effects

- None; `Call` runs the operation.

## Verification

```sh
go build ./mcpserve
```
