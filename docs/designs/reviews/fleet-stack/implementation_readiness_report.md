# Stage 9: Implementation Readiness (fleet-stack.md, Detailed Implementation)

Date: 2026-11-10. Reviewers: `egm-implementation-readiness`, `egm-codebase-details` (standard `implementation-readiness-standard.md`), `egm-style`. Fresh subagents. One re-audit run.

## Scope decision

Owner (via coordinator, 2026-11-10): this document is the phase-level plan. Each of L3, L2, L4 and L1 gets a short phase design for choices with more than one defensible answer; mechanical choices go to the implementer under specq `engineering/ponytail.md`. Step 0 is implemented in draft PR taskqorg/fleet#75. Findings that need phase detail are listed in the design under "Carried to phase designs".

## First audit

| # | Reviewer | Sev | Finding | Resolution |
| --- | --- | --- | --- | --- |
| 1 | readiness | Blocker | Trial prep steps lack test language, path, bug list | Carried to phase designs; step 6 marked blocked on open question 2 |
| 2 | readiness | Blocker | L3 lacks schema files, generators, reader list | Carried to phase designs; blocked on open question 10 (generators) |
| 3 | readiness | Blocker | L4 has no behaviour spec | Fixed: current Python server behaviour, captured by outside tests, is the spec |
| 4 | readiness | Blocker | L4 conflicts with `fleet-connections.md` C1 on `connection_call.py` | Owner: L4 ports all three servers; C1 later changes the Rust subcommand; follow-up to update C1 |
| 5 | readiness | Blocker | L2 lacks triples, job layout, darwin rule | Carried to phase designs; lasting publication tied to open question 7 |
| 6 | codebase | Blocker | Removing Python files breaks `tests/coordinator/artifact-tools.test.ts` and others | Fixed: readers listed in step 15 |
| 7 | readiness | Major | Step 0 contents, pin, command, budget | Fixed: PR #75, pin `d178c809`, budget 180 s on owner's Mac (owner) |
| 8 | readiness | Major | Phase order and per-phase green gate | Fixed |
| 9 | codebase | Major | `connection_call.py` contradiction | Fixed with owner decision |
| 10 | codebase | Major | "41" old tests wrong | Fixed: 33 of 37, defined by rule |
| 11 | codebase | Major | Step 13 vague | Fixed; exact change carried to L2 design |
| 12 | style | Major | Undefined references, vague bug count, mixed item 16 | Fixed |
| 13 | all | Nit | Artifact expiry, citations, wording | Fixed |

## Re-audit

| # | Reviewer | Sev | Finding | Resolution |
| --- | --- | --- | --- | --- |
| 1 | both | Major | `profile.rs:227` (`include_bytes!` hash) and `profile.rs:262` (test asserts `python3`) missing | Fixed: listed; hash moves to executor binary (author decision) |
| 2 | codebase | Major | Row E cited `fleet-toolchain.md` Node SEA rejection, which covers only the coordinator | Fixed |
| 3 | codebase | Major | L3/L2 file boundaries not exact | Carried to phase designs (owner scope decision) |
| 4 | readiness | Nit | Overview vs detailed run order of trial prep | Fixed |
| 5 | readiness | Nit | How the test finds the binary | Fixed |
| 6 | codebase | Nit | Green gate omits Postgres roles job | Fixed |
| 7 | codebase | Nit | TypeSpec in handoff list; `store.ts:1`; Python on coordinator host | Fixed |

## Verdict

No open blocking finding. Open owner questions remain (see design, Open Questions 1-10).
