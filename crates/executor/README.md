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
| `src/delegation.py`, `design_rules.py` | Small MCP servers the executor gives to agents. |

## Connections

Talks only to the coordinator's `/api/hosts/*` routes (`packages/coordinator/app.ts`). Protocol tests: `tests/executor`. Host service files: `deploy/hosts`.
