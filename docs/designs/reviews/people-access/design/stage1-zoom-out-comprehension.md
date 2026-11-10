# Stage 1 review: zoom out, inventive catalyst, comprehension

Targets: `docs/designs/fleet-people-access.md`, `docs/designs/fleet-connections.md` (first draft, 2026-11-10).
Reviewers (Goldfish, fresh context): `egm-zoom-out`, `egm-inventive-catalyst`, `egm-comprehension`.
EGM stages 5 and 6 were run together in one batch on both companion docs; stage 8 (intermediate zoom out) was folded into stage 2 of this run.

## Findings and disposition

| # | Sev | Finding (reviewers) | Disposition |
| --- | --- | --- | --- |
| 1 | Blocker | Held-host trust filter lives in `dispatch` (slice 6) but non-owner hosts join after slice 4 (comprehension) | Fixed: trust filter moves to slice 2 at command, link and key delivery; dispatch reuses it |
| 2 | Blocker | `dispatch(hostId)` sees one host but ranks across hosts; first poller wins, take becomes a race (comprehension, inventive) | Fixed: global ranking on each poll, polling host claims only executions where it is the best runnable candidate; "best candidate" defined |
| 3 | Major | Take rule: two of three mechanisms are the same field; `take.people` unused; take can bypass grants on a held host (zoom out, inventive) | Fixed: two mechanisms (per task, `take.projects`); take needs a grant; `take.people` removed; trust clause "or that the holder directs" removed |
| 4 | Major | Stale reading over `switchPct` blocks a subscription forever (inventive) | Fixed: a reading past its `resetsAt` counts as unknown |
| 5 | Major | `switchPct` cannot fire for Claude; who sets it unclear (comprehension, inventive) | Fixed: percent readings only, shown on screen; holder or owner sets it |
| 6 | Major | `problem` table does not exist; two alert stores (zoom out) | Fixed: stall alert is one `question` with a unique cause key |
| 7 | Major | Host settings in two homes, two intake mechanisms (zoom out) | Fixed: intake and harness placement are fields on `host` and `managed-account`; `host_setting` stays for executor-applied settings only; one audit kind `setting-change` |
| 8 | Major | New per-run token unusable until the gateway re-reads the snapshot (inventive, zoom out) | Fixed: gateway re-reads the snapshot once on an unknown token before refusing. Signed tokens rejected: departs from the copied lane |
| 9 | Major | C5 model APIs as harness endpoints is speculative (zoom out) | Fixed: moved to Future Improvements |
| 10 | Major | Decision numbers and project names undefined (comprehension) | Fixed: decisions table and glossary entries |
| 11 | Major | Slices and stages not listed (comprehension) | Fixed: slice and stage tables |
| 12 | Major | 401 vs 403 for missing JWT (comprehension, zoom out) | Fixed: guard 403, coordinator 401 without the header |
| 13 | Major | Cost basis values differ between docs (comprehension) | Fixed: `measured`, `estimated`, `unavailable` in both |
| 14 | Major | Budgets enforced in two places with no rule (comprehension) | Fixed: dispatch for agent runs, gateway for calls, both from one coordinator flag |
| 15 | Nit | Files-first host order is invented (zoom out) | Fixed: tie-break only between equal list positions |
| 16 | Nit | `queueBound`, job mode, reconcile built ahead of need (zoom out, inventive) | Fixed: `queueBound` removed; job mode is C3, reconcile is C2 |
| 17 | Nit | `person.grants jsonb` differs from data foundation `person_grant` (zoom out) | Fixed: reuse `person_grant` |
| 18 | Nit | Stall alert recipient (inventive) | Fixed: director of the waiting work; holder of the cause named as contact |
| 19 | Nit | Tokens of runs that end while the coordinator is down (comprehension) | Fixed: removed in the first snapshot after recovery settles the execution |
| 20 | Nit | Generic `acp` kind has one user (zoom out) | Kept as a binding only, no extra UI (owner asked for a generic ACP path) |
