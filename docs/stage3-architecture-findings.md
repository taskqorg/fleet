# Stage3 architecture findings

2026-11-09. Analysis only of existing code and evidence. No native/model call, deploy or Cloudflare action. Live baseline: release `candidate-98cb004796a3`, executor SHA256 `21aa1379…` ([onboarding handoff](onboarding-handoff.md)).

## Observations

### Admission / concurrency

| # | Observation | Evidence |
|---|---|---|
| A1 | Same-session exclusion: a new message or profile change returns `409 Execution active` while any execution of the session is not `completed/failed/stopped`. `unknown` counts as active. | `packages/coordinator/app.ts` (`/api/sessions/:id/messages`, `/profile`) |
| A2 | Admission (`enqueue`) re-resolves the managed account per execution, including delegated children; disabled, unready, harness-mismatched or Claude-on-Linux bindings are refused. Admission has no count, rate or capacity check. | `app.ts` `enqueue`, `accounts.ts` `resolve`, [claude-placement-proof](claude-placement-proof.md) |
| A3 | Every execution is bound to `config.hostId`; there is no placement choice between hosts at enqueue. | `app.ts` `enqueue` |
| A4 | `/api/hosts/poll` returns all pending commands for the host. Only gates: delegation readiness and lab trial order. | `app.ts` `/api/hosts/poll` |
| A5 | The executor launches every command it is given and holds them in `live`; there is no local limit. | `crates/executor/src/main.rs` poll loop (~l.699) |
| A6 | Two independent sessions on distinct accounts actually overlapped (native barrier) with separate context/events/results; fake native only. | [recovery-milestone](recovery-milestone.md), `evidence/recovery-local-proof.json` |
| A7 | Program workers are serial per process; the lab runs trials in sequence. | [recovery-milestone](recovery-milestone.md) |

Effective policy today: **unbounded per host and per account; serial per session.** Supported scale was proven only for 2 parallel fake sessions.

### Resume

| Harness | Implemented | Native proof | Ref |
|---|---|---|---|
| Codex | exec for new sessions; `codex app-server` thread/resume + acknowledged developer `inject_items` for retained threads | First A/B **failed** (stale developer context); repaired B **passed** with one turn on candidate `5eece6b`. Same-thread continuation and delegation passed live. | [multiharness-native-evidence](multiharness-native-evidence.md), [multiharness-repaired-native-evidence](multiharness-repaired-native-evidence.md), [continuation-live-evidence](continuation-live-evidence.md) |
| Claude | `--resume <nativeSessionId>` in `crates/executor/src/harness.rs` (~l.248) | **None.** Only one fresh MCP→Write→Read. Whether a changed system prompt/tools apply on resume is unknown. | [claude-placement-proof](claude-placement-proof.md) |
| Antigravity | ACP `session/resume`, or `session/load` only if advertised; no substitution | One fresh native turn on agy 1.5.3; resume not tested, not requalified on the final binary | [antigravity-acp-implementation](antigravity-acp-implementation.md) |

Coordinator invariants: harness and account cannot change on resume (`409`), the native session ID cannot change (`Native identity conflict`).

### Platforms

- Mac: only provisioned native host (Codex ×2, Claude, Antigravity-current ready).
- PDS18/Linux: coordinator plus program publication only; no Codex/agy under `fleet`; Claude forbidden (coordinator `403` on observation and admission; executor refuses at build target).
- Windows: permitted for Claude by policy; no host entry, executor or proof.

## Shortcomings

1. Many sessions launch many native processes on one Mac against the same accounts; real per-account quota is unknown ([architecture-coverage](architecture-coverage.md)). Balancing is an owner setting, not a cap.
2. `unknown` executions block their session with no supported resolution except the narrow closure-collision recovery. A live crash can leave the session stuck.
3. Resume semantics differ per harness. Only Codex has proof for changed instructions. The Claude and Antigravity paths take the same branch with no qualification.
4. Placement is the host from config, not a choice. When a second native host exists, there is no point where host plus account is decided.

## Next design decisions

### D1–D2. Concurrency and `unknown`-blocked sessions
- Withdrawn: no concurrency cap and no invented blocking/resolution rule. Fleet helps agents; balancing is an owner setting. Capabilities to copy (queue/steer, fork, handoff, reconcile) are in the [agent tools inventory](agent-tools-inventory.md).

### D3. Claude resume
- **Recommended:** until qualified, treat Claude follow-ups as new native sessions with explicit carried context (history summary/result). Do not use `--resume`. Then run one bounded native A/B on Mac, mirroring the Codex repair test.
- **Changes it:** an A/B pass where the changed system prompt and tools are applied on resume. Then enable `--resume`. If it fails, keep fresh-session continuation for good.

### D4. Antigravity resume
- **Recommended:** allow ACP resume/load only when advertised. Mark it "unqualified" in session state, and skip one native resume turn until the binary is requalified.
- **Changes it:** one native resume turn on the final binary showing the context and model retained.

### D5. Placement when a second native host appears
- **Recommended:** the operator picks host+account explicitly at session creation (an extension of the existing onboarding host selection). The session stays pinned to that host for resume. No automatic balancing.
- **Changes it:** provisioning of a Windows (Claude-eligible) or Linux (Codex/agy only) native host, and evidence that native session state can move between hosts (currently assumed impossible).

### D6. Platform provisioning order
- **Recommended:** Linux Codex on PDS first (cheap, no Claude, reuses dedicated CODEX_HOME). Windows only when Claude capacity beyond one Mac is needed.
- **Changes it:** owner priority for Claude capacity, or PDS resource limits.

## Code change

None. No admission defect was clear enough to fix safely. A1–A5 match the documented intent (serial per session, unbounded otherwise). See the [agent tools inventory](agent-tools-inventory.md).
