# Human entity codes

* **Author:** Alex Kirs
* **Date:** 2026-11-10

## Overview

Fleet entities are keyed by UUIDs and shown shortened (`9ad781be`). Design examples use bare numbers with no agreed scheme. The owner wants one short code that tells the operator, at a glance, what kind of object it is, which project it belongs to, and which one it is. The code is a naming convention for reading and recognition only. UUIDs stay the storage key. A child object (for example an execution of a task) has its own code and does not embed the parent's number; the interface shows the relation. The code must copy cleanly into messengers, URLs, agent prompts, branch names and commit messages.

Owner's starting shape: `T-PRJ-N-999` (type, project, optional section letter, number).

## Analysis

Seven Goldfish lenses reviewed the concept (design, codebase details, zoom-out, comprehension, implementation readiness, structure, inventive catalyst). Main findings, ranked:

1. **The optional section letter is the weak point** (6 of 7 lenses). `T-FLT-42` next to `T-FLT-A-42` forces parsers to count segments. A single letter does not explain itself ("A for what?"). Section letters collide with type letters (`S-FLT-S-5`). If numbering is per section, moving an item to another area renumbers it. Recommendation: drop the section from the code and show the area as a separate chip next to the code. If kept, it must be mandatory and must not drive the number.
2. **Only the coordinator issues codes.** Offline hosts allocating numbers would create duplicates. A `code_counter(project_id, type, next)` row is advanced with `UPDATE … RETURNING` in the same transaction as the insert. Gaps are allowed; reuse is not. Until a code exists, the interface shows the short UUID. Agents already create tasks through coordinator operations (`fleet-data-foundation.md`), so host-leased number blocks are not needed.
3. **Type letters already in use.** `D1…D15` (design-system decisions), `D1…D5`, `R`, `G` in `fleet-operator-app.md`, `OR-N` owner rules, specq `P1…P16`. Fix the type letter list now, avoid D, R, G, P, O, and make the project segment mandatory so `D3` is never read as an entity code.
4. **Codes never change; this answers the move question.** Moving between projects is not built as a feature. If an item moves, a project code is renamed, or an item changes type, it gets a new code and the old one stays as an alias: `entity_code(code PK, entity_id, type, issued_at, superseded_by)`. Codes are never reused.
5. **Codes only for things people type or paste.** Code: tasks, spec rules, experiments, questions. No code: executions and artifacts (many, machine-made, reached through a parent; show parent code plus short UUID), hosts and projects (already have short names such as `mac`, `dashq`).
6. **One visible ID (OR-17, OR-22).** The code is the only ID shown next to the title. The UUID is in a tooltip or a "copy UUID" action. Search, CLI and API accept either. One shared formatter in `packages/ui`; `packages/ui/book/open-questions.tsx:120` renders `T-{t.id}` today and must switch to it.
7. **Storage work.** Keys are `randomUUID()` (`packages/coordinator/tasks.ts`); experiments and artifacts are still JSONB in `document`. Writes are SERIALIZABLE, so counters are per (project, type) to limit retries. Row-level security: per-project counters keep gaps from revealing hidden rows; rows without a project use reserved scope `SYS`. Unknown and forbidden codes both return `not_found`. Backfill existing rows in `created_at` order.
8. **Copy safety.** Start with a letter and always use hyphens: git short SHAs never contain `-`, GitHub numbers start with `#`. Avoid `.`, `/`, `#` (messengers link them). Reject all-hex project codes (`FAB`, `ADD`). No zero padding. Lookup is case-insensitive; output is uppercase. Avoid I, O, S, Z, B in project codes (confusable with digits). Sort by (type, project, integer number).

**Inventive tension.** The concept wants both "see the area" and "never change". The resolution is to separate them: the code carries only stable facts (type, project, number); mutable facts (area, state) live in chips beside it.

## Goals vs. Non-Goals

* **In-Scope Goals:** one code grammar; type letter list; coordinator allocation; alias on move, rename or type change; display rule (code next to title, UUID hidden); agent lookup `entity.resolve(code)` and `entity.resolve_many(text)`.
* **Out-of-Scope Non-Goals:** codes as storage keys; parent number inside child codes; codes for executions, artifacts, hosts, projects; a move feature; host-side allocation; meaning beyond naming.

## Alternatives Weighed

* `T-FLT-A-42` (owner's shape with section): rejected by reviewers: optional segment, undecodable letter, renumbering when area changes.
* `FLT-42` (Jira/Linear, one counter per project, no type letter): strongest runner-up. Shorter; the in-app icon shows type. Loses the type outside the app (messenger, branch). A type letter plus a shared counter is redundant.
* `T-4217` (no project): rejected: project not visible.
* `T-FLT-K7Q2` (host plus base32): rejected: unpronounceable, unordered; coordinator already allocates.
* `.` or no separator: rejected: messengers auto-link dots; no separator is unreadable and SHA-like.
* `K` as the spec rule letter: rejected by owner: not intuitive; `S` chosen.
* Host-leased number blocks: rejected: lease management for no need.

## Key Decisions & Staging for Next Steps

Decided by the owner on 2026-11-10:

1. **Format.** `T-FLT-42`: type letter, project code, number, joined by hyphens. Uppercase output, case-insensitive lookup, no leading zeros.
2. **Section only for spec rules, and there mandatory.** `S-FLT-D3`: type `S`, scope, section letter, number. The number counts within (scope, section). Tasks, questions and experiments carry no section; their area is a chip next to the code.
3. **Type letters.** `T` task, `Q` question, `E` experiment, `S` spec rule. D, R, G, P, O are avoided. A section letter may never equal a type letter, so T, Q, E, S are forbidden as section letters. `S` is safe in the type position: it is always first, followed by a hyphen, and IDs render in monospace. The confusable-letter ban (I, O, S, Z, B) applies only inside project codes. Existing doc labels such as `T1…T16` (test cases) and `Q2`, `Q8` have no project segment, so they cannot be read as entity codes.
4. **System-wide scope.** Proposed code `ALL` for general rules and rows without a project (for example `S-ALL-C4`). It is not hex-only and avoids the confusable letters I, O, S, Z, B, unlike `SYS`. Reserved: no project may take it.
5. **Allocation.** Only the coordinator issues codes, from a counter per (project, type), and per (scope, section) for `S`. Numbers are never reused; gaps are allowed.
6. **Stability.** A code never changes. A moved entity, a renamed project or a changed type gets a new code; the old code stays as an alias. No move feature is built.

Regex:

```
\b(?:([TQE])-([A-Z][A-Z0-9]{1,4})-([1-9][0-9]*)|S-([A-Z][A-Z0-9]{1,4})-([A-DF-PRU-Z])([1-9][0-9]*))\b
```

Open for `/egm-draft`: the section letter list per scope (section letters follow the same confusable-letter rule); project code list; display formatter in `packages/ui`; `entity_code` table and backfill.

Where it lands: grammar and allocation in a new "Human entity codes" section of `fleet-data-foundation.md`; display rule in `owner-rules.md`; dated entry in `docs/design-system/decisions.md`; agent guide `config/skills/fleet/guide.md`.
