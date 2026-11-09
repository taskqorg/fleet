# Fleet session handoff

2026-11-09, end of the Claude Opus 5.5 coordinator session. The next session continues from here. Speak concise Russian; write docs in English. Read memory notes `no-invented-limits` and `architecture-then-engineering` first.

## Principles (owner)
- Fleet is a simple, flexible system that helps agents. Never propose invented limits, caps or gates. Balancing and settings are configured by the owner.
- The architecture phase captures requirements. Entities and storage are decided later, in a separate engineering session (Occam's razor, no extra entities). "Attention item" is NOT decided; the requirement is only that every failure is visible and actionable.
- Copy proven logic from ~/Projects/orca and ~/Projects/t3code instead of inventing it.
- Claude (personal subscription) runs only on Mac/Windows, never on PDS, Linux or WSL.

## Live state
- Coordinator release `candidate-b02670f3cddf` on PDS18. Rollback target: `candidate-28517364d2bd`, plus the `.pre-b02670f3cddf` backups.
- Executors:
  - `owner-mac` (launchd `com.ufobe.fleet.executor`): Codex x2, Claude, Antigravity.
  - `pds18-native` (`fleet-executor` user unit): Codex `fleet-codex-pds` and Antigravity `fleet-antigravity-pds`; the owner did those logins on 2026-11-09.
- The machine host token was rotated on 2026-11-09 after a leak: the old token now gets 401. Revoked values remain in the `*.pre-rotate-20261109` backups; delete them if they are not needed.
- Fleet Cloudflare Access app session is 730h (same as Dash). The bypass app `Fleet machine agent-tools` covers `/api/hosts/agent-tools`.
- GitHub org taskqorg: deploy keys are enabled. Write deploy keys exist for fleet-hello-world (169267266) and fleet-hello-world-alt (169267270); they sit under `~/.config/fleet/connections` on both hosts. Fleet merges PRs itself after checks, because GitHub auto-merge is unavailable on the free plan.
- Stuck execution `6b00f0fb` is queued for the non-existent host `pds18`; its stop came back unknown and nobody forced it. This is itself a finding: an execution bound to a host that does not exist hangs forever.

## Done this session (all merged to main)
- Account onboarding; agent session tools (handoff across all three harnesses, fork, resume, ask/reply, queue/steer, reconcile); Codex steer and native fork; Claude fork.
- PDS as the second environment.
- Isolation fixes; loads of 5 and 10 sessions with no 429 (the bottleneck is the executor's start loop).
- One op surface (`/api/ops`, MCP `/api/tools`); project settings (`config/projects/*`); worktree lifecycle; PR/direct integration; per-execution git identity `fleet-bot`; project_readiness.
- Live tests that passed: T1 (PM -> Claude worker -> Codex reviewer, no nudges), T9, T10, T12, T13, T14 (PR merged by Fleet), T16. Partial: T2, T11. Results: `docs/test-results-T1-T16.md`.

## Key docs
`architecture-breadth-plan.md` (T1-T16), `prototype-implementation-plan.md`, `test-results-T1-T16.md`, `project-settings-matrix.md`, `project-settings-decisions.md`, `real-projects-survey.md`, `project-setup-and-access.md`, `agent-tools-inventory.md`, `layered-isolation-model.md`, `multi-account-same-env.md`, `resources-and-auth-model.md`, `pds-codex-environment.md`.

## Next
1. Finish the remaining tests:
   - T15 checkout readback;
   - setup:skip;
   - T2 on alt;
   - a negative readiness run;
   - T3-T8: load at most 10, lab, real crash, resume for Claude/agy, account loss, leak markers.
   - Then resolve 6b00f0fb.
2. Architecture findings to carry into the freeze:
   - credential isolation is env-only (one OS user can read every project key);
   - gitNetwork gives a writable shared .git and full network;
   - session_deliver does not wake the PM;
   - Antigravity is one account per OS user;
   - computer use is one GUI per OS user;
   - Codex built-in child agents.
3. Freeze the architecture, then hold the engineering session (storage, entities, tool sets), then the infra decisions, then plan V1.
4. Skipped so far: cs on GitLab, a second Claude account, Windows.
