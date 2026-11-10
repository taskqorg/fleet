# packages/coordinator

The Fleet coordinator: a Fastify server over Postgres that owns tasks, sessions, executions, questions and artifacts, hands commands to host executors, and serves the operator app.

## Key files

| File | Purpose |
|---|---|
| `server.ts` | Process entry; reads `FLEET_*` environment settings and calls `createApp`. |
| `app.ts` | Routes, the Bearer and Origin guard, the read-snapshot and write-transaction wrappers, host poll and receipt handling, and the typed ops behind `/api/ops/:name`. |
| `store.ts` | Postgres (or PGlite in tests) access: migrations, `read`/`tx`/`atomic`, documents, the change log (`changes.since`, `prune`), `listen`, and `READ_OPERATIONS`. |
| `db/schema.sql` | All migrations in order: documents, typed task/execution/question tables, row-level security, the change log and triggers, the artifact store tables. |
| `tasks.ts`, `executions.ts`, `sessions.ts` | Typed task state machine and paging, execution records, session lists. |
| `stream.ts` | `/api/stream` SSE change feed for the app. |
| `artifacts.ts`, `stores.ts`, `placement.ts` | Artifact metadata, per-host S3 stores, presigned transfers and placement rules. |
| `agent-tools.ts`, `delegation.ts`, `delivery.ts` | MCP tools for agents, delegation between executions, delivery pipeline runtime. |
| `projects.ts`, `accounts.ts`, `recovery.ts` | Connected projects and pipelines, harness accounts, recovery tools. |
| `profiles.ts` | Trusted agent profiles from `config/agents`: validation, canonical JSON, digests. Lab profiles ([design](../../docs/designs/fleet-lab.md)). |
| `migrate-sqlite.ts` | One-time move from the old SQLite records to Postgres, and its reverse export. |
| `*.py` | Helpers the coordinator starts: the scoped static-site program worker (`delivery_worker.py`) and the connection client (`connection_call.py`: CLI for pipeline api steps, MCP for agent steps). |

## Connections

Executors in `crates/executor` poll `/api/hosts/*`. The app in `app/` uses `/api/ops/*` and `/api/stream`. Configuration comes from `config/`. Tests live in `tests/coordinator`. Design: [data foundation](../../docs/designs/fleet-data-foundation.md) and [operator app](../../docs/designs/fleet-operator-app.md).
