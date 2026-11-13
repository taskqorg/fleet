# Coordinator

## Overview

`coordinator` is Fleet's loopback HTTP service. It applies the PostgreSQL schema, loads the people projection, exposes browser and host operations, serves the SSE change stream, and starts background reconciliation passes. The service owns coordination state; executors and the gateway report through its host-facing API rather than writing the database directly.

The component groups its implementation by domain: projects, tasks, sessions and executions; versioned definitions and pipelines; hosts, accounts, connections and limits; artifacts and S3 storage; Lab evaluation; delivery, telemetry and watch passes. Typed public contracts live in the sibling [`../contract`](../contract) package.

## Interface

- [`main`](./main.go) starts `App` after migration and serves only on `127.0.0.1`; [`App.routes`](./registry.go) is the HTTP entry point. [`ops.go`](./ops.go) registers named operations and HTTP routes, applies request decoding, authorization and transactions.
- [`authorize`](./main.go) distinguishes host-secret calls under `/api/hosts/` from browser calls carrying a browser secret and `X-Fleet-Person`; [`checkOrigin`](./browser_checks.go) applies the browser-origin check.
- [`Store.Tx`](./store.go) is the database boundary. [`schema.sql`](./schema.sql) defines documents, tasks, executions, questions, artifacts, stores, definitions and the row-level security policies they use.
- [`serveStream`](./stream.go) provides the server-sent event stream. [`Broadcast`](./stream.go) listens for PostgreSQL notifications and wakes local stream subscribers.
- [`pipelineStart`](./pipelines.go), [`pipelineAdvance`](./pipelines.go) and [`pipelineCancel`](./pipelines.go) create, progress and cancel pipeline runs; [`labReconcile`](./lab.go) progresses Lab experiments under their scope lock.
- [`artifactReserve`](./artifacts.go), [`artifactCommit`](./artifacts.go), [`artifactVerified`](./artifacts.go) and [`S3`](./s3.go) coordinate artifact upload, verification, replication and object-store access.
- [`hostsRegister`](./hosts.go), [`ingest`](./host_events.go), [`executionStop`](./executions.go) and [`dispatch`](./dispatch.go) connect registered hosts to execution lifecycle and command delivery.

## Invariants

- The service requires `FLEET_PEOPLE_FILE` and `FLEET_DATABASE_URL`; [`setPeople`](./main.go) requires exactly one owner and rejects duplicate people or conflicting service-token common names.
- [`readBody`](./main.go) limits JSON request bodies to 1 MiB and host calls cannot name another `hostId`. Host requests carry no person identity.
- [`Store.Tx`](./store.go) gives reads a repeatable-read snapshot; writes serialize by an in-process mutex plus a PostgreSQL advisory transaction lock, and retry serialization failures and deadlocks.
- [`schema.sql`](./schema.sql) enables row-level security on project-owned state. [`scopeOf`](./main.go) and [`refreshRequestAccess`](./project_access.go) set the transaction's project/person scope; the owner uses the unrestricted scope.
- [`migrate`](./store.go) applies each `-- migration N` schema section once under the migration lock.
- [`definitionState`](./definitions.go) computes definition state from stored revisions and shipped repository content; [`reconcileDefinitions`](./definitions.go) records shipped content but does not move an edited head.
- [`taskTransition`](./tasks.go) enforces declared task transitions. [`artifactGate`](./executions.go) keeps an execution waiting until required artifacts satisfy its requirement, rather than treating missing artifacts as success.
- [`newS3`](./s3.go) refuses redirects for signed object-store requests; [`S3.send`](./s3.go) retries only selected transient statuses, throttling codes and network failures, up to three attempts.

## Side Effects

- Starts a loopback HTTP server, PostgreSQL listener and periodic passes through [`startAll`](./registry.go); writes telemetry through [`startTelemetry`](./telemetry.go).
- Mutates PostgreSQL state, emits `fleet_change` notifications, and may queue host commands. [`onEnd`](./store.go) delays registered filesystem effects until a transaction is known committed.
- Reads people, release/configuration and Lab files; [`connections.go`](./connections.go) can ingest gateway spool files and remove fully imported past-day files only after commit.
- Reads, writes, copies and deletes S3-compatible objects through [`S3`](./s3.go). Artifact deletion and retention routines keep tombstones and avoid deletion while a verified copy is unreachable.
- [`runChecks`](./lab.go) executes approved Lab check commands in a temporary directory; on Unix it kills the check process group after completion or cancellation.

## Verification

- Run: `cd go && go test ./coordinator`
- Build the executable: `cd go && CGO_ENABLED=0 go build -o bin/fleet-coordinator ./coordinator`
- For the repository-selected full gate, use the command in [`../../fleet.md`](../../fleet.md#verification); it requires the project test environment and is not a coordinator-only check.
