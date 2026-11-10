# crates/executor

The Fleet host executor (Rust binary `fleet-executor`). It runs on each host, polls the coordinator for commands, starts harness sessions (Codex, Claude, Antigravity) in worktrees, and reports receipts, events and artifacts back.

## Key files

| File | Purpose |
|---|---|
| `src/main.rs` | Poll loop, command handling, configuration from `FLEET_*` variables. |
| `src/outbox.rs` | Local SQLite outbox; receipts are flushed on their own thread with retries and backoff. |
| `src/harness.rs`, `codex.rs`, `antigravity.rs`, `native.rs` | Harness adapters and native session start, resume and fork. |
| `src/worktree.rs`, `git_access.rs`, `checkout_sync.rs` | Worktree lifecycle, per-execution git identity and keys, fast-forward of clean checkouts. |
| `src/store.rs`, `upload.rs`, `preview.rs` | This host's S3 store registration, artifact transfers through presigned URLs, ffmpeg previews. |
| `src/readiness.rs`, `lab.rs` | Project readiness checks and laboratory runs. |
| `src/mcp.rs`, `mcp-tools.json`, `mcp-mime.json` | Delegation and design-rules stdio MCP subcommands and captured tool/MIME metadata. |

## Connections

Talks only to the coordinator's `/api/hosts/*` routes (`packages/coordinator/app.ts`). Protocol tests: `tests/executor`. Host service files: `deploy/hosts`.

## Remaining Python

The repository retains 19 Python files. L4 removes `src/delegation.py` and `src/design_rules.py`. Connection MCP still
requires Python on its execution host; no live host was inspected or changed.

| File | Purpose and removal condition |
|---|---|
| `packages/coordinator/connection_call.py` | CLI and MCP client of `fleet-gateway` (docs/designs/fleet-connections.md §5); kept while connection MCP runs in Python. |
| `packages/coordinator/delivery_worker.py` | Program delivery and account inspection on worker hosts; remains Python pending its C1 replacement. L4 does not claim Python-free hosts. |
| `deploy/infrastructure/origin_guard.py` | Origin admission service; kept independently of C1 and MCP. |
| `deploy/infrastructure/test_origin_guard.py` | Origin guard regression checks; kept while that service exists. |
| `tests/coordinator/fixtures/fake_connection.py` | Test stand-in for the connection client and the gateway spool; development and CI only. |
| `tests/executor/account_status.py` | Program worker account observations; retained while the worker exists. |
| `tests/executor/antigravity_acp.py`, `claude_mcp.py`, `codex_wire.py`, `fake_integration.py` | Fake native harness/transport checks; development and CI only. |
| `tests/executor/delegation_approval.py`, `delegation_protocol.py`, `project_protocol.py`, `lab_protocol.py` | MCP grants, durable transport and read-only wire checks; development and CI only. The approval check validates Codex config for the Rust subcommand without starting a server. |
| `tests/executor/executor_binary.py` | Protocol-check binary locator/copy and synthetic secret fixture helper; development and CI only. |
| `tests/executor/program_recovery.py` | Program worker restart/recovery check; retained while the worker exists. |
| `tests/executor/lab_provenance.py`, `native_probe.py` | Opt-in retained-run inspection and real native probe; outside the merge gate. |

Python snippets in shell/TypeScript checks and deployment scripts also remain for
fixtures, SQLite setup and administration. `config/pipelines/draft-examples.json` embeds a Python example program; `src/harness.rs` embeds a Python fake harness in its tests. These are not executor MCP servers.
C1 must replace the connection and worker paths before their hosts can drop Python.

The MCP replay compares JSON values, parses JSON text content and normalizes fixture
paths and generated UUIDs; JSON whitespace, object order and Unicode escaping are
not a byte-for-byte contract. The MIME table captures Python 3.12 defaults on the
owner's Mac; unknown extensions retain the octet-stream fallback.
