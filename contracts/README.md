# contracts

JSON Schema contracts shared by the coordinator, the app and the executor.

* `host-protocol.schema.json`: the host protocol (`/api/hosts/poll` request and reply, `/api/hosts/receipts` status
  receipts, `/api/hosts/events` batches) and event payloads. It is the source of the types on all three sides
  (docs/designs/fleet-stack.md, Contracts); `packages/coordinator/db/schema.sql` stays the source of table rows only.
* `lab-config.json`: JSON Schema for agent configurations in laboratory configurations (copied as an asset; not generated).

## Generated types

* TypeScript: `contracts/gen-ts.mjs` writes `contracts/generated/<name>.ts`. `pnpm run typecheck` and `pnpm run build`
  run it first; `pnpm run contracts` runs it alone (for an editor). The output is not committed.
* Rust: `crates/executor/build.rs` writes the same types to Cargo's `OUT_DIR` on every `cargo build`;
  `crates/executor/src/contracts.rs` includes them. Only cargo is needed.

Every `*.schema.json` file here is generated; each `$defs` entry becomes one type with the same name.

## Supported subset

Both generators accept only these keywords and fail on any other, so a schema never yields a silently weaker type:
`type` (one type, optionally with `"null"`), `properties`, `required`, `additionalProperties`, `items`, `enum` (strings),
`$ref` (`#/$defs/<Name>`), `minimum` (`0` makes a Rust `u64`), `$comment`.

* An object with properties, and an enum, must be a `$defs` entry (Rust needs its name).
* `{}` is any JSON value: `unknown` in TypeScript, `serde_json::Value` in Rust. Use it only for native records Fleet
  passes on without reading.
* `"additionalProperties": true` marks an open object: Rust keeps the unnamed fields (`rest`) and writes them back;
  TypeScript names only the listed fields, so code reads a field only after the schema names it.
* A property not in `required` is optional (`?` in TypeScript, `Option` omitted when `None` in Rust). A required
  property with `"null"` in its type is always written, as `null` when absent.
