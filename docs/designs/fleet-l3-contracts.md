# L3 stage 1: one schema for event payloads and the host protocol

Phase design for [fleet-stack.md](./fleet-stack.md) L3, stage 1 (Detailed Implementation steps 9-11, open question 10).
Owner decisions already taken there are not repeated: JSON Schema in `contracts/`, `schema.sql` for rows only, generated
types for the coordinator, the app and the executor, generated at build time and not committed. This document covers
only the choices that had more than one defensible answer.

## Reversal check (done first)

The transcript event union (`item.completed` with `data.item`, `turn.completed`, `outbox.failed`) was generated with
the off-the-shelf generators `json-schema-to-typescript` 15.0.4 and `cargo-typify` 0.6.1, run from a scratch directory
(not added to the repo). Both sides came out typed: a TypeScript union of interfaces with literal `type` fields, and a
Rust `#[serde(untagged)] enum` of structs with typed `data` and `item` fields
(`evidence/l3/reversal-check.{schema.json,ts,rs}`). One finding: with `"const"` typify types the discriminator as
`serde_json::Value`; with a one-value string `enum` it gives a unit enum. The format stays JSON Schema.

## 1. Generators: two small in-repo generators, not the off-the-shelf ones

| Option | For | Against |
| --- | --- | --- |
| `json-schema-to-typescript` + `typify` | Full JSON Schema; maintained | 15 npm modules (`npm ls --all`, includes prettier and lodash) and 30 crates (`cargo tree` of typify 0.6.1, includes syn, schemars, regress) added to the supply chain (goal 3); typify is 0.x |
| **In-repo `contracts/gen-ts.mjs` (Node) + `crates/executor/build.rs` (Rust)** | No new third-party module; `cargo build` alone still works (open question 10); fails on any keyword outside a small subset | Two generators to keep in step; only the subset in `contracts/README.md` |

Chosen: in-repo. The subset covers every shape in stage 1; a schema that needs more fails loudly in both generators,
which is the point at which an off-the-shelf generator can be reconsidered. Keeping the two in step is checked by the
planted-rename evidence (a renamed field fails the build on every side that uses it).

## 2. Event data: one open object with named fields, not a union by event type

The wire `type` of a harness event is the native record's own type (`assistant`, `result`, `protocol.write`, ...), so a
closed union by `type` cannot cover harness events. A union with a catch-all `type: string` member does not narrow in
TypeScript (`ev.type === 'item.completed'` keeps the catch-all), so readers would still need casts.

Chosen: `EventData` is an open object whose named fields are the ones Fleet code reads across the seam (`item`,
`fleetNormalized`, `harnessVersion`, `children`). Unnamed fields pass through unchanged (Rust `rest`). A field becomes
typed when the schema names it; a reader cannot read an unnamed one in TypeScript.

## 3. Types only, no runtime validator in this stage

The coordinator keeps its existing hand checks at the three routes (they are the trust-boundary validation). A generated
validator that rejects bodies not matching the schema would change what hosts may send today (delegation and account
receipts, native fields) and is a product-behaviour change; it is left for a later stage.

## 4. Command payloads stay open

Poll reply `Command` types the envelope (`id`, `kind`, `executionId`, ...); `payload` differs by kind and stays an open
object. The executor still handles commands as JSON values after the typed reply is parsed. Each kind gets a schema when
it is next changed. Delegation and account receipts (written by the Python MCP servers) are described in L4.

## 5. One status table for the outbox and store answers

`outbox::class(status, host_token)` is the one table: a redirect is an edge condition; 401 is an edge condition only
for the coordinator (the host token), and a rejection for a store; 429 and 5xx are retried; other 4xx are rejected.
`outbox::classify` and `upload::store_answer` both use it.

## Note after merging main (Conversations Stage 2)

The app no longer reads raw events (`app/src/transcript.ts` was deleted; the Conversations screen reads thread blocks, another route, out of this stage), and `fleetNormalized.messages` was removed from the executor, so `NormalizedMessage` left the schema. Event payloads are now read by the coordinator (ingest) and written by the executor.
