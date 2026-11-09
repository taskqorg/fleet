# Goldfish Review Report: Stage 8 Intermediate Zoom Out

**Target:** `/Users/kirs/orca/workspaces/fleet/shad/docs/designs/fleet-data-foundation.md` (L1-1126)

**Context:** zoom_out_report.md, comprehension_report.md, critic_report.md, fleet-data-foundation-brief.md, fleet-data-foundation-goldfish-review.md, ~/Projects/tight/tight.md

## Reviewer Status

| Reviewer | Status |
| --- | --- |
| `egm-zoom-out` | completed |

**Severity:** 0 🔴, 6 🟡, 4 🟢

**Verdict:** Most of the Critic's fixes add mechanism to parts that could be deferred or cut. Three cuts remove the reason for about six Critic Majors: no ClickHouse yet, artifact lifecycle deferred, and isolation moved to its own design. The M8 fixes make the model smaller and should be adopted.

## M8 recheck against the task model

- `delivery` holds task intent plus orchestration, not a run. `packages/coordinator/delivery.ts:22` stores task, methodId, profileId, account, questionId and state. Its `steps[]` already point at real `execution` records (`delivery.ts:36,47,56`).
- A `delivery` execution kind (L246, L366, item 19) adds a parent layer over executions that already exist. The task fields would end up in `execution.data` jsonb.
- The two question systems are one concept with two link points. `project-question` gates a task (`delivery.ts:21,25`). `question` is raised by an execution and delivered back to its session (`agent-tools.ts:131-136`).
- Conclusion: make delivery a task, and merge the two question systems into one table (findings 1 and 2).

## Prioritized Findings

### 1. Drop the `delivery` execution kind; delivery is a task - Severity: 🟡 Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** L246, L366, item 19, L785-788
* **Discrepancy Description**: The task table already covers what a delivery holds (title, expected_outcome, result_destination, completion_requirements). Its steps are already executions.
* **Rationale & Recommendation**: Change the kinds to `agent_run | pipeline_run | pipeline_step | child_agent`. "Start delivery" becomes: create a task, then launch a linked `pipeline_run`, with method and profile in `config_ref`. Migrate each delivery to one task row and re-parent its step executions. This also resolves prior Nit 9 ("delivery" meaning two things).

### 2. One typed `question` table instead of a third link point - Severity: 🟡 Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** L267, item 18 (L778), item 20
* **Discrepancy Description**: CUJ 1's waiting reason and item 20's decision blocks depend on questions, but the `task_id` link is buried in jsonb. Adding decisions on top of both legacy kinds gives three mechanisms.
* **Rationale & Recommendation**: Add one Phase 2 table with `project_id`, optional `task_id` and `execution_id`, `kind` (question or decision), `options`, `state`, `answer` and `answered_by`. Migrate both legacy kinds into it. Record acceptance as a decision against an `execution_id` or `artifact_id`, which covers prior Major 4 without a separate table. Derive task state in the view rather than with the trigger cache from prior Major 2. Add a cache only if the 200 ms target (L573) fails.

### 3. Defer ClickHouse instead of patching it - Severity: 🟡 Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** D3, §4, A10; Critic M4, M5
* **Discrepancy Description**: Critic M4 (ReplacingMergeTree dedup, FINAL, seq scope) and M5 (totals recompute, pending-ingest state) add work.
* **Rationale & Recommendation**: In Postgres both problems go away. A unique `(execution_id, seq)` with `ON CONFLICT DO NOTHING` deduplicates exactly at write time. Totals come from an aggregate view or an ingest upsert. Do not reuse Langfuse's ClickHouse: under tight D7 it belongs to the `langfuse` user, and D6 forbids shared roles. Phase 4 should use monthly-partitioned Postgres tables for `event`, `span` and `measurement`. Keep ClickHouse in Alternatives, to adopt when a measured query misses its target.

### 4. Cut artifact lifecycle from the slice, which removes Critic B1 - Severity: 🟡 Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** §3 L289-297, items 25 and 28b; Critic B1
* **Discrepancy Description**: B1 (a shared object deleted while still in use) only happens through retention rules and location removal. Both are empty or rare by default.
* **Rationale & Recommendation**: Keep `store`, `artifact`, `artifact_copy` and a placement worker that only adds copies (CUJ 5). Defer `runAudit`, retention with preview and confirm, location-removal deletes and ffmpeg previews (previews are already deferred in the brief, L117). Secret purge does not need reference counts: tombstone all rows with the given `(project_id, content_hash)` and delete that key in each store.

### 5. Settle store reachability with the network, not new code - Severity: 🟡 Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** item 6 (L660), L194, L276, L298; Critic M8, Comprehension
* **Discrepancy Description**: Stores bind to loopback, yet the design promises cross-host presigned links. `CopyObject` does not work between independent Garage nodes.
* **Rationale & Recommendation**: The owner controls all hosts, so bind each store on the private host network and use plain presigned URLs. Do every cross-host copy as "the target executor GETs from the source and PUTs locally", using the existing `upload.rs` path (item 27). Remove the server-side-copy branch and do not build a proxy.

### 6. Move isolation to its own design; it causes Critic M9 - Severity: 🟡 Major
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** §9, Phase 5, D5; Critic M9
* **Discrepancy Description**: `systemd-run --uid` and the macOS sudoers rule (item 38) need privilege, but tight D4 (L50) says no service user has sudo. Fixing M9 here pulls root-setup decisions into a data design.
* **Rationale & Recommendation**: Keep only the two interfaces the data layer uses: the measurement `basis` and the per-execution secret set for redaction. Move the rest, with the tight change it needs, to `layered-isolation-model.md`. Prior zoom-out Major 7 recommended the same.

### 7. Take the one-line Critic fixes as they are - Severity: 🟢 Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** Critic M6, M7, M10
* **Discrepancy Description**: Each of these has a fix that needs no new subsystem.
* **Rationale & Recommendation**: M6: set `FORCE ROW LEVEL SECURITY`, or drop RLS as the second guard. M7: mask pattern matches as well as logging them, and narrow the claim to "known credentials". Do not add a token-discovery subsystem; harness login tokens in the account `HOME` stay a recorded fact. M10: go forward-only after `verify` and delete the `reverse` tool.

### 8. Keep the risk section short - Severity: 🟢 Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** Critic M11
* **Discrepancy Description**: Once findings 3, 4 and 6 are adopted, about half of the listed risks no longer apply.
* **Rationale & Recommendation**: Keep only PDS18 as a single point of failure, outbox backlog, single-copy projects and Q2.

### 9. Name the reuse choices on PDS18 - Severity: 🟢 Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** D2, A8d; tight D7 (L80-81), D11 (L133)
* **Discrepancy Description**: Garage would sit next to Langfuse's MinIO while tight D11 still leaves "keep or replace MinIO" open. The A8d rejection does not state the real gap.
* **Rationale & Recommendation**: Note that Garage could later answer D11, but do not couple the two decisions. Restate A8d's real gap: AssetQ uses a local filesystem store, not S3, so it cannot pin a project's files to Windows.

### 10. Drop `tool_run` - Severity: 🟢 Nit
* **Reviewer Lens:** `egm-zoom-out`
* **Affected Scope:** L246
* **Discrepancy Description**: `tool_run` has no consumer until Q9 (prior Nit 8, still open).
* **Rationale & Recommendation**: Connection cost is already a `measurement` row (L110). Remove the kind.

## Net effect

- **Removed:** ClickHouse, ReplacingMergeTree, the totals recompute, audit, retention, previews, server-side copy, the reverse tool, `tool_run`, the `delivery` kind, one question kind and the isolation phase.
- **Added:** one typed `question` table.
- **Owner constraints:** none violated.
