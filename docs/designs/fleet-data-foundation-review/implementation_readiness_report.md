# Goldfish Review Report: Stage 9, Implementation Readiness

**Target:** `docs/designs/fleet-data-foundation.md` (lines 584-1125)
**Standard:** `resources/document_standards/implementation-readiness-standard.md`
**Context:** `fleet-data-foundation-brief.md`, `fleet-data-foundation-goldfish-review.md`

## Reviewer Status

| Reviewer | Status |
| --- | --- |
| `egm-implementation-readiness` | completed |
| `egm-codebase-details` | completed |
| `egm-style` | completed |

**Verdict:** not ready to implement. M8 is not resolved. The plan doesn't say how a task relates to a delivery or to the two question kinds. Several steps in Phases 2-4 would break code that the plan never lists for change.

**Owner constraints:** no violations found. The plan adds no invented limits on concurrency or admission. Harness quirks are recorded as facts. Existing products are preferred. The one possible conflict with tight is finding 7.

**Counts:** 🔴 7, 🟡 17, 🟢 13 (after deduplication).

## 🔴 Blockers

### 1. M8: the relationship between a task and a delivery is undefined (L756, L776-778, L785-789)
* **Lens:** implementation-readiness, codebase-details
* **Problem:** `delivery` is already a task-like record (`packages/coordinator/delivery.ts:10-60`).
  * It holds `task`, `requestId`, `signature`, `methodId`, `profileId`, `account`, `questionId` and `previousDeliveryId`.
  * It runs its own state machine: `waiting`, `pr-open`, `merged`, `published`, `failed`, `unknown`.
  * It has `steps[]`, and each step points to an `execution`.
  * Item 19 turns it into a single `execution(kind=delivery)`. The result is executions nested inside an execution, with no columns for these fields and no place for the state machine.
  * The plan doesn't say how `reconcile`, `delivery_get` and `delivery_history` (which read `all('delivery')`) are rewritten.
* **Fix:** Choose the model. The likely choice: a delivery becomes a `task` row, and each `steps[i].executionId` becomes an `execution` with that `task_id`. Then add a field-by-field mapping table and a state mapping, and list the functions in `delivery.ts` that change.

### 2. M8: the two question kinds are neither merged nor specified (L267, L778, L797)
* **Lens:** implementation-readiness, codebase-details
* **Problem:** The code has two question kinds:
  * `project-question`: the operator answers it (`projects.ts:65-67`), and a delivery waits on it (`delivery.ts:21,25`).
  * `question`: agents raise it, keyed by execution and session (`agent-tools.ts:128-136`, read at lines 169, 172 and 197).

  Item 18 says "questions and decisions gain `task_id`", but no `decision` kind exists in the code. The plan doesn't say how `task_id` gets set. Storing it inside `document.data` gives it no index and no foreign key. Item 20 doesn't say which question kind it shows, or which answer path it calls (`question_answer` or `reply`).
* **Fix:** Either merge both into one typed `question` table (with nullable, indexed `task_id` and `execution_id`) and add a migration for `project-question`, or keep both explicitly. Either way, define the link, how it is set (at write time or by backfill from `execution.task_id`), and the answer operation the task view calls.

### 3. Item 19 changes the code that writes executions but not the code that reads them (L781-792)
* **Lens:** implementation-readiness, codebase-details
* **Problem:** Many modules read `execution` documents directly:
  * `agent-tools.ts:169,172`;
  * `recovery.ts`, `ui.ts`, `lab-ui.ts`, `account-ui.ts` and the routes in `app.ts`;
  * `delivery.ts`, which reads `all('event')` to get step usage. This breaks in Phase 4 (L911).

  Event ingest is `transportRecovery.ingest` in `recovery.ts` (`app.ts:116-117`), and that file isn't in the Phase 4 list either.
* **Fix:** List every place that reads this data and what each changes to, or keep writing the old documents during the transition and add a step that removes them later.

### 4. Rollback is impossible after item 19 (L582 vs L738)
* **Lens:** implementation-readiness
* **Problem:** The plan promises rollback until the end of Phase 2. But `reverse` only exports `document` rows, so new `task` and `execution` rows can't be turned back into legacy documents.
* **Fix:** Specify a reverse mapping from the typed tables to legacy documents, or move the rollback boundary to the end of Phase 1.

### 5. Phase 2 migration has no mapping per legacy kind (L739, L366, L249)
* **Lens:** implementation-readiness
* **Problem:** "Typed mapping" has no mapping for `delegation`, `pipeline-run`, `delivery`, `session-fork`, `handoff` or lab trials. The state mapping covers only `completed`, `stopped` and `unknown`. The plan also doesn't say whether tasks are backfilled from legacy data.
* **Fix:** For each legacy kind, add a table giving the target table, the column for each field, the state mapping and how links are derived. Add a rule for task backfill.

### 6. Phase 3 removes base64 artifacts that `delivery.ts` still reads (items 26 and 28)
* **Lens:** codebase-details
* **Problem:** `reconcile` (`delivery.ts:11`) decodes `contentBase64` to read `delivery-result.json`, so the system doesn't keep working after Phase 3 (L586).
* **Fix:** Add `delivery.ts` (Modify) to Phase 3 and read the result through `artifacts.get`.

### 7. Artifact state is wrong in commit (L829)
* **Lens:** style
* **Problem:** L829 says `commit` marks the artifact `verified`. An artifact's states are `pending`, `stored`, `failed` and `deleted` (L305, L313); `verified` is a state of a copy. Code that follows L829 would fail the check at L831.
* **Fix:** "marks the artifact `stored` and its first `artifact_copy` `verified`".

## 🟡 Major

8. **Task `state` is stored, but nothing defines its transitions (L262, L267, L776).** This conflicts with G2 ("derived state is not stored twice"). Either derive `queued`, `in_progress` and `waiting` in a view, or add a table: trigger, new state, module that writes it. *(implementation-readiness)*
9. **Acceptance and results without a task are undefined (L262, item 20).** `completed` is not the same as accepted. M8's question "can a result with no task be accepted?" is still open. *(implementation-readiness)*
10. **Agents can't reach tasks (L776, L1027).** G6 and brief requirement 5 say agents use the same operations as people, but item 44 adds no `task_*` MCP tools and no permission rule for them. *(implementation-readiness)*
11. **Tests don't cover M8 (L1083-1089).** Add tests for:
    * migrating a delivery to a task or execution;
    * `task_id` on both question kinds;
    * task state transitions;
    * a reverse round trip of the typed tables;
    * legacy readers still working after item 19.

    *(implementation-readiness)*
12. **Row-level security doesn't apply to the table owner (L633, L759).** `fleet` owns the tables, so the policies are ignored for it. Specify `FORCE ROW LEVEL SECURITY`, the policy SQL, and what happens when `fleet.project_ids` is unset. *(implementation-readiness)*
13. **`systemd-run --uid --scope` needs root or polkit (L213, L972, L999).** This may conflict with tight's "no sudo" rule. Name the polkit rule or system unit template that grants it, and add it to tight (item 8). *(implementation-readiness)*
14. **Two things are referenced but never defined (L755, L267, L777).** The columns of `execution_totals` aren't listed, and the task view has no SQL. *(implementation-readiness)*
15. **Line references are wrong.**
    * `/api/hosts/poll` is at `app.ts:114`, not line 106 (L713).
    * `resources()` is at `main.rs:935`, not 925 (L960).
    * The stub `inputSchema` is at `agent-tools.ts:214`, not 191 (L1026).

    *(codebase-details)*
16. **Item 44 (L1026) misses other stub schemas.** They are in `recovery.ts:12`, `projects.ts:77`, `app.ts:132`, `crates/executor/src/delegation.py:26-33` and `design_rules.py:19`. Item 53 requires all of them to have a schema. *(codebase-details)*
17. **Item 5 (L651) edits the wrong file.**
    * `deploy/infrastructure/fleet.service.candidate` contains no environment names; it loads `coordinator.env`.
    * A second unit, `deploy/fleet.service.candidate`, also exists. Say which unit is the real one.

    *(codebase-details)*
18. **The executor enforces its own `FLEET_MAX_ARTIFACT_BYTES` limit.** `main.rs:792-810` reads it with a 16 MiB default. Item 28 doesn't remove that check, so the "no size limit" goal (L851) and the 400 MB test (L1119) would fail. *(codebase-details)*
19. **Variable removal timing contradicts itself (L651, L728, L589).**
    * Phase 0 removes `FLEET_MAX_ARTIFACT_BYTES`, but the base64 path that uses it remains until Phase 3.
    * `FLEET_DATABASE` is dropped "after Phase 3", but `migrate-sqlite` needs the SQLite path until Phase 7.

    Give one removal phase per variable. *(style, implementation-readiness)*
20. **Insertion order is lost.** `all()` orders by `rowid` (`app.ts:20`), and callers use `.at(-1)` to get the latest record (`delivery.ts:11`, `accounts.ts:15`). The new `document` table (L693) needs an order column, filled from `rowid` by `forward`. *(codebase-details)*
21. **`put` conflict behaviour isn't specified (L720).** Today, writing an id that already exists under a different kind does nothing (`app.ts:22`). Say whether that stays. *(codebase-details)*
22. **The transaction rules ignore async and retried operations.**
    * `delivery_create` is async and calls `question_ask` (`delivery.ts:13-21`).
    * A `SERIALIZABLE` retry runs `fn` again, including `randomUUID()` and file reads.

    List which operations run inside `tx`, and require `fn` to be safe to retry. *(codebase-details)*
23. **Some SQLite users are missing from items 9, 50 and 52.** `tests/coordinator/lab-packaged.ts` sets `FLEET_DATABASE`, and `package.json` lists `@types/better-sqlite3`. *(codebase-details)*
24. **Values that can't be tested.**
    * L768: which measures exist before Phase 4, and which `basis` the missing ones get.
    * L828: "the nearest" host.
    * L838: "a short interval", and how reachability is decided.
    * L839: the audit sample size. Also, L841 should say "detects", not "prevents".
    * L910: what `history_incomplete` is. It is also missing from the `execution` columns at L748-754.
    * L797: "smoothly" and "minimal view".
    * L1115: the coverage rule leaves out item 54 and the preview test files.

    *(style, implementation-readiness)*

## 🟢 Nits

25. Item 49 (L1064) marks the design doc "Create"; it already exists, so it should be "Modify". *(codebase-details, implementation-readiness)*
26. L690: the layout of "numbered migrations in the same file" is ambiguous. Say how the runner applies them.
27. Items 46 and 52 use globs for files that aren't generated. List the exact files.
28. Item 33 (L925): `quota:'unknown'` appears at `accounts.ts:15` and at `accounts.ts:18`; both need the change.
29. Item 13 (L717): mark `delivery_worker.py` and `resume-argv.ts` as unchanged.
30. L874: the number "28b" breaks the item sequence.
31. L597: "one defensible answer" gives no reason.
32. L618, L634, L641, L676: write "`tight.md` decision D5" so it isn't confused with Fleet's own D1-D7.
33. L883 says "preview error" but L1107 says `unavailable`. Use one term.
34. L1035: "a few lines" should refer to the byte budget at L1099.
35. L1061: reword "claims that this design supersedes".
36. L1086: "an `[windows]`" should be "a `[windows]`".
37. L992: say how the git remote's host is resolved for SSH and HTTPS remotes behind a proxy.

Note: findings 25-37 are 13 nits. The count line above should read 🟢 13.

## Claims verified against the code

These claims match the code:
* `records` table: `app.ts:19`.
* Base64 artifact and result routes: `app.ts:118-119`.
* Derived body limit: `app.ts:23-25`.
* Base64 queue: `main.rs:812`; `use base64` at `main.rs:11` is its only user.
* `ureq` and `sha2` in `Cargo.toml`.
* `GIT_SSH_COMMAND`: `git_access.rs:37`.
* Write grant to the whole `.git`: `main.rs:401`.
* `journal.sqlite`: `main.rs:366,831`.
* All the docs named in items 7, 42 and 48 exist.
