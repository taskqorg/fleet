# contracts

JSON Schema contracts shared by the coordinator, the app and the executor.

* `host-protocol.schema.json`: the host protocol (`/api/hosts/poll` request and reply, `/api/hosts/receipts` status
  receipts, `/api/hosts/events` batches) and event payloads. It is the source of the types on all three sides
  (docs/designs/fleet-stack.md, Contracts); `go/coordinator/schema.sql` stays the source of table rows only.
* `lab-config.json`: JSON Schema for agent configurations in laboratory configurations (copied as an asset; not generated).

## Generated types

* TypeScript: `contracts/gen-ts.mjs` writes `contracts/generated/<name>.ts`. `pnpm run typecheck` and `pnpm run build`
  run it first; `pnpm run contracts` runs it alone (for an editor). The output is not committed.
* Go: `go/gen` writes the same types into the shared package `go/contract` on `go generate` (`go/contract/host_protocol_gen.go`).
  The file is committed; CI fails when `go generate` changes it. The executor uses it for the poll reply, status receipts, event batches and the data of its own event types (`FleetEventType`); native harness records stay raw JSON (`HostEvent.data`).

Every `*.schema.json` file here is generated; each `$defs` entry becomes one type with the same name.

## Supported subset

Both generators accept only these keywords and fail on any other, so a schema never yields a silently weaker type:
`type` (one type, optionally with `"null"`), `properties`, `required`, `additionalProperties`, `items`, `enum` (strings),
`$ref` (`#/$defs/<Name>`), `minimum`, `$comment`.

* An object with properties, and an enum, must be a `$defs` entry (Go needs its name).
* `{}` is any JSON value: `unknown` in TypeScript, `json.RawMessage` in Go. Use it only for native records Fleet
  passes on without reading.
* `"additionalProperties": true` marks an open object: Go and TypeScript name only the listed fields;
  TypeScript names only the listed fields, so code reads a field only after the schema names it.
* A property not in `required` is optional (`?` in TypeScript, a pointer with `omitempty` in Go). A required
  property with `"null"` in its type is always written, as `null` when absent.
