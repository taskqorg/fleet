# contracts

JSON Schema contracts shared by the coordinator, the app, the executor and the gateway.

* `host-protocol.schema.json`: the host protocol (`/api/hosts/poll` request and reply, `/api/hosts/receipts` status
  receipts, one route per other receipt envelope: `/api/hosts/project-operations` `ProjectReceipt`,
  `/api/hosts/delegations` `ChildReceipt`, `/api/hosts/accounts/report` `AccountReceipt`; `/api/hosts/events` batches; the artifact
  transfer requests `Artifact*Request` and `HostArtifact*Request` of `/api/hosts/artifacts/*`, `StoresRequest`, `AgentToolsRequest`) and
  event payloads. It is the source of the types on all three sides
  (docs/designs/fleet-contracts.md); `go/coordinator/schema.sql` stays the source of table rows only. Its `routes` map
  names every host route: `request` and `reply` (`$defs` names; no reply: not described, such as an operation's record
  or a byte stream), `long` (no surrounding transaction) and `decode` (`checked`: required keys and `Validate`, unknown
  keys refused when the request has `unevaluatedProperties: false`; `plain`: decoded only, for bodies older hosts send
  without required fields). Go gets one `contract.Route<Name>` (`HostRoute[Req, Rep]`) per route, registered by the
  coordinator's `hostRoute` and sent by the executor's `queueTo` and `transferReply`; TypeScript gets `HostRoutes`.
  `Command` is the union of one `<Kind>Command` per command kind (same envelope, the kind's payload); `Capability` lists
  the fixed capability names a host reports, and `contract.MissingCapability` is the one gate dispatch and poll apply.
* `lab-registry.schema.json`: Lab use-case registry entries (`config/lab/use-cases/*.json`, `UseCase`); the coordinator's
  `useCases()` validates every entry and its `maps_to` targets (docs/designs/fleet-asset-pipelines.md §3).
* `gateway.schema.json`: the fleet-gateway seam (docs/designs/fleet-connections.md section 3): the snapshot the
  coordinator writes and the gateway reads, and the call line the gateway spools and the coordinator ingests.
* `ops-<domain>.schema.json` (accounts, analytics, artifacts, definitions, executions, lab, people, projects, sessions, tasks, telemetry) and `ops.schema.json`: the coordinator operations (`POST /api/ops/<name>`, and the agents' MCP tools of the same names), split by domain so lanes that change different domains edit different files. A type that only one domain's operations reach lives in that domain's file; `OpError`, `ChangeKind`, types two domains share and types no operation reaches live in `ops.schema.json`; a reference across files is `<file>.schema.json#/$defs/<Name>`. A name is in one file only (built or planned); `gen-ts.mjs` fails otherwise. A new operation goes into the file of its name's first word:

  | File | First words of operation names |
  | --- | --- |
  | `ops-accounts` | `account`, `subscription`, `connections`, `transport`, `budget`, `budgets`, `price`, `prices` |
  | `ops-analytics` | `analytics` |
  | `ops-artifacts` | `artifact`, `store`, `stores` |
  | `ops-definitions` | `definition`, `agent`, `harness`, `profiles`, `catalogue`, `setting`, `reconcile` |
  | `ops-executions` | `execution`, `executions`, `pipeline`, `queue`, `route`, `hosts`, `system`, `object` |
  | `ops-lab` | `lab` |
  | `ops-people` | `me`, `people`, `person`, `focus`, `view`, `list`, `search`, `nav` |
  | `ops-sessions` | `session`, `sessions`, `thread`, `message`, `reply`, `ask`, `direct`, `watch`, `worker`, `changes`, `recommendation` |
  | `ops-projects` | `project_create`, `project_update`, `project_initial_access_revoke` (shared versioned writes) |
  | `ops-tasks` | `task`, `question`, `questions`, remaining `project` reads |
  | `ops-telemetry` | `telemetry` |

  A new first word joins the closest row here. A new domain file also needs its `go:generate` line in `go/contract/contract.go`.
  `operations` lists every operation (`read`, `long`, `errors`: the statuses its handler returns besides 401, 403 and 500;
  `auth`: who may call it, `person`, `project:<path>` (a grant of the project id at that request path; not visible is 404),
  `holder:<kind>` (the holder of the named object, or the owner), `architect`, `owner` or `system`; `agent`: how a host agent
  calls it, `no`, `tool`, or `bound:<binder>` when the coordinator binds its arguments to the calling `session`, `project` or
  `focus`; `description`: the tool description every MCP server publishes). The generator refuses an entry without them;
  each name `<name>` has `$defs` `<Name>Request` and `<Name>Reply`, and a failed call answers `OpError`. `planned` lists operations specified contract first and not built yet
  (their `$defs` exist; building one moves its name to `operations`). TypeScript gets
  `Ops` (name to request and reply, used by `op()` in `app/src/api.ts`; `generated/ops.ts` re-exports every domain file and joins their `Ops<Domain>`); Go gets `contract.Operations`, filled by the `init` of each domain's generated file, which
  `go/coordinator/ops_contract_test.go` compares with the registry. A handler registered with `op("<name>", handler)` (`go/coordinator/registry.go`)
  decodes the body into `<Name>Request` and returns `<Name>Reply`; `runOp` applies its declared `auth`. Operations move to it domain by domain.
* `ops-tasks.schema.json` also owns the internal `ResourceContributionV1`, `ResourceMethodV1` and `PreparedHeaderV1` prepared-resource shapes. The same domain registers `project_detail`, `project_resources` and `project_resource_items` (read, not long; 400/404/409/503), with named ready/unchanged/preparing replies, typed progress, group/item/sort-key/page shapes and decimal-string versions. Large tool tuples use the optional tool-item `position` (`ResourcePackedToolKey`, `tool-position`) for captured around/resolved anchors, and `r2.` zlib+base64url cursors; small cursor schema1 and ordinary tuples remain compatible. Exact encoding/decompression/body bounds live in [prepared project resources](../docs/designs/fleet-analytics.md#prepared-read-operations). These reads are unadvertised and return 503 while the startup reads flag is off. `ops.schema.json` adds `resource_summary` to ChangeKind for committed project-scoped publication. Metric keys use `ResourceMetricNameV1`; nullable decimal strings preserve exact money/credit values and measured zero. The coordinator validates its fixed metric key set and stores all normalized tool/cost/price support; legacy wire summaries keep their existing numeric fields.
* Each read operation may list `changes`: the change kinds (`<ChangeKind>` or `<ChangeKind>:<id prefix>`) that can alter its reply. The coordinator sends with every change the operations it touches (`Change.touches`, `contract.Touches`), and the app rereads a mounted read when its operation is touched (`app/src/store.ts:touchedBy`); `gen-ts.mjs` refuses a kind that is not a `ChangeKind`.
* `vocabulary.json` (data, not a schema): the words the app and the coordinator share, harnesses, the view grammar (views, list keys, states, groups, sorts) and the system presets with the counter defaults. The app imports it directly; `go generate` copies it into `go/contract/vocabulary_gen.go` and `go/contract/vocabulary.go` decodes it as `contract.Vocabulary`.
* `profile.schema.json`: the agent profile (`config/agents/*.json`, the start command's `agentConfig`). Its validator
  `contract.ParseProfile` (`go/contract/profile.go`) is the one the coordinator and the executor run; a Go test checks every shipped profile.

* `lab-export.schema.json`: a project's export to Lab, version 1 (docs/designs/fleet-lab.md, Project data in examples);
  read by `scripts/lab-export.ts`; no Go code reads it, so it has no `go generate` line (TypeScript types only).

`ops-sessions.schema.json:Change` keeps its required envelope and adds optional nullable `settingTarget` for `setting-change`: the exact server-derived audit target captured in the source transaction. `person:<email>` identifies the preference owner, independently of actor. Missing/null metadata is a legacy/unknown target and consumers keep their conservative reread/resync behavior. Definition change IDs retain the producer's `kind/id` scope; consumers may skip a known other project but must not treat an unknown/legacy ID as proof of irrelevance. Row visibility continues to come from the existing authorized feed and RLS.

## Generated types

* TypeScript: `contracts/gen-ts.mjs` writes `contracts/generated/<name>.ts`. `pnpm run typecheck` and `pnpm run build`
  run it first; `pnpm run contracts` runs it alone (for an editor). The output is not committed.
* Go: `go/gen` writes the same types into the shared package `go/contract` on `go generate` (`go/contract/<name>_gen.go`).
  The files are committed; CI fails when `go generate` changes them. The executor uses them for the poll reply, status receipts, event batches and the data of its own event types (`FleetEventType`); native harness records stay raw JSON (`HostEvent.data`).

Every `*.schema.json` file here is an input to generation; each `$defs` entry becomes one type with the same name.

## Supported subset

Both generators accept only these keywords and fail on any other, so a schema never yields a silently weaker type:
`type` (one type, optionally with `"null"`), `properties`, `required`, `additionalProperties`, `unevaluatedProperties` (only `false`), `items`, `enum` (strings),
`$ref` (`#/$defs/<Name>`, or `<file>.schema.json#/$defs/<Name>` for a type of another contract file: an import in TypeScript, the same package in Go), `minimum`, `$comment`, `const` (a string: a literal type in TypeScript, `string` in Go) and
`oneOf` (only as a `$defs` entry whose members are all `$ref`: a union in TypeScript, `json.RawMessage` in Go, where
each member is its own struct; give each member a `const` discriminator such as `kind`. A schema generated with
`go/gen -unions` (the host protocol) gets a Go struct with one pointer per member, `Other` for a discriminator value no
member has, `Value`, `Validate` and a getter for every envelope property the members share).
An object without properties whose `additionalProperties` is a schema is a map (`map[string]T` in Go, `Record<string, T>` in TypeScript).

* An object with properties, and an enum, must be a `$defs` entry (Go needs its name).
* `{}` is any JSON value: `unknown` in TypeScript, `json.RawMessage` in Go. Use it only for native records Fleet
  passes on without reading.
* `"additionalProperties": true` marks an open object: TypeScript names only the listed fields, so code reads a
  field only after the schema names it; Go keeps the other keys in `Rest` through decode and encode.
* `"unevaluatedProperties": false` on an operation request object, or one nested in it, makes the coordinator's
  strict decoder refuse a key the schema does not name (Go `Closed()`). `"additionalProperties": false` alone types
  only: callers have long sent requests with keys it does not list.
* A property not in `required` is optional (`?` in TypeScript, a pointer with `omitempty` in Go). A required
  property with `"null"` in its type is always written, as `null` when absent.

Catalogue method reads add scoped assessment and recommendation evidence without wrapping existing method fields. `lab_compare_methods` creates an unlaunched Lab experiment with per-variant pipeline snapshots and separate catalogue linkage; `config_ref` remains the actual pipeline/overrides digest. Missing numeric assessment values are explicit nulls; partial observed means retain populations and evaluator/version basis. See [definition editors](../docs/designs/fleet-definition-editors.md) for authority, selection and recovery rules.

## Interface

[gen-ts.mjs](gen-ts.mjs) reads the schema `$defs`, `operations` and `planned` maps, emits domain types, and joins built operations into `generated/ops.ts`. [Go contracts](../go/contract/README.md) consume the same schemas through [go/gen](../go/gen/README.md).

## Invariants

`gen-ts.mjs:type` rejects unsupported keywords and invalid nested enum/object/union forms. The top-level `seen` map rejects an operation name shared by built or planned entries across files. Planned operations need request/reply types but are excluded from the callable `Ops` map. Required nullable values remain distinct from optional fields.

## Side Effects

Generation creates or overwrites TypeScript files under `generated/`. It reads no credentials and makes no network requests. Go generation writes committed files in `go/contract/`; do not hand-edit generated output.

## Verification

From the repository root: `pnpm run contracts` checks TypeScript generation; `pnpm run typecheck` also checks its consumers. In CI, `cd go && go generate ./...` must leave the committed generated Go types unchanged. For README-only changes, use `git diff --check` and link validation.
