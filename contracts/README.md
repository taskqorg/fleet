# contracts

JSON Schema contracts shared by the coordinator, the app, the executor and the gateway.

* `host-protocol.schema.json`: the host protocol (`/api/hosts/poll` request and reply, `/api/hosts/receipts` status
  receipts, one route per other receipt envelope: `/api/hosts/project-operations` `ProjectReceipt`,
  `/api/hosts/delegations` `ChildReceipt`, `/api/hosts/accounts/report` `AccountReceipt`; `/api/hosts/events` batches; the artifact
  transfer requests `Artifact*Request` and `HostArtifact*Request` of `/api/hosts/artifacts/*`, `StoresRequest`, `AgentToolsRequest`) and
  event payloads. Replies of the artifact routes are not described yet. It is the source of the types on all three sides
  (docs/designs/fleet-contracts.md); `go/coordinator/schema.sql` stays the source of table rows only.
* `gateway.schema.json`: the fleet-gateway seam (docs/designs/fleet-connections.md section 3): the snapshot the
  coordinator writes and the gateway reads, and the call line the gateway spools and the coordinator ingests.
* `ops.schema.json`: the coordinator operations (`POST /api/ops/<name>`, and the agents' MCP tools of the same names).
  `operations` lists every operation (`read`, `long`, `errors`: the statuses its handler returns besides 401, 403 and 500);
  each name `<name>` has `$defs` `<Name>Request` and `<Name>Reply`, and a failed call answers `OpError`. `planned` lists operations specified contract first and not built yet
  (their `$defs` exist; building one moves its name to `operations`). TypeScript gets
  `Ops` (name to request and reply, used by `op()` in `app/src/api.ts`); Go gets `contract.Operations`, which
  `go/coordinator/ops_contract_test.go` compares with the registry. A handler registered through `typedOp` (`go/coordinator/registry.go`)
  decodes the body into `<Name>Request` and returns `<Name>Reply`; operations move to it domain by domain.
* `profile.schema.json`: the agent profile (`config/agents/*.json`, the start command's `agentConfig`). Its validator
  `contract.ParseProfile` (`go/contract/profile.go`) is the one the coordinator and the executor run; a Go test checks every shipped profile.

* `lab-export.schema.json`: a project's export to Lab, version 1 (docs/designs/fleet-lab.md, Project data in examples);
  read by `scripts/lab-export.ts`; no Go code reads it, so it has no `go generate` line (TypeScript types only).

## Generated types

* TypeScript: `contracts/gen-ts.mjs` writes `contracts/generated/<name>.ts`. `pnpm run typecheck` and `pnpm run build`
  run it first; `pnpm run contracts` runs it alone (for an editor). The output is not committed.
* Go: `go/gen` writes the same types into the shared package `go/contract` on `go generate` (`go/contract/<name>_gen.go`).
  The files are committed; CI fails when `go generate` changes them. The executor uses them for the poll reply, status receipts, event batches and the data of its own event types (`FleetEventType`); native harness records stay raw JSON (`HostEvent.data`).

Every `*.schema.json` file here is generated; each `$defs` entry becomes one type with the same name.

## Supported subset

Both generators accept only these keywords and fail on any other, so a schema never yields a silently weaker type:
`type` (one type, optionally with `"null"`), `properties`, `required`, `additionalProperties`, `items`, `enum` (strings),
`$ref` (`#/$defs/<Name>`, or `<file>.schema.json#/$defs/<Name>` for a type of another contract file: an import in TypeScript, the same package in Go), `minimum`, `$comment`, `const` (a string: a literal type in TypeScript, `string` in Go) and
`oneOf` (only as a `$defs` entry whose members are all `$ref`: a union in TypeScript, `json.RawMessage` in Go, where
each member is its own struct; give each member a `const` discriminator such as `kind`).
An object without properties whose `additionalProperties` is a schema is a map (`map[string]T` in Go, `Record<string, T>` in TypeScript).

* An object with properties, and an enum, must be a `$defs` entry (Go needs its name).
* `{}` is any JSON value: `unknown` in TypeScript, `json.RawMessage` in Go. Use it only for native records Fleet
  passes on without reading.
* `"additionalProperties": true` marks an open object: Go and TypeScript name only the listed fields;
  TypeScript names only the listed fields, so code reads a field only after the schema names it.
* A property not in `required` is optional (`?` in TypeScript, a pointer with `omitempty` in Go). A required
  property with `"null"` in its type is always written, as `null` when absent.
