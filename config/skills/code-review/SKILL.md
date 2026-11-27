---
name: code-review
description: Whole-branch code review for Fleet managed workers and reviewers against origin/<base>...HEAD, producing exact-SHA review evidence for project-publishing record_code_review (and legacy project-forge code_review).
---

# Code review

Perform a thorough whole-branch review of `origin/<base>...HEAD` inside the managed execution worktree and produce auditable exact-SHA review evidence for `project-publishing` `record_code_review` (or compatibility alias `project-forge` `delivery` `action: "code_review"`).

## Rules

- **Never** run nested `codex review` or sub-harness CLIs inside a managed Fleet sandbox (`Operation not permitted` in sandboxed executions and no Fleet artifact provenance).
- Review the **entire branch diff** (`origin/<base>...HEAD`), not only the latest commit.
- Anchor the report to the exact 40-character lowercase `HEAD` commit SHA (`git rev-parse HEAD`). Any later commit invalidates prior review evidence and requires a fresh review and artifact.
- When independent review is required, route it through the project's configured review pipeline or start a separate session using the project's `reviewer` role (`session_start` with the configured reviewer profile); never `message_send` another agent's or bot's session (`403: Session belongs to another agent`), never open an unaddressed operator question via `question_ask`, and never include raw commit SHAs or raw UUIDs in question text (`OR-38`; use entity codes such as `T-...`, `C-...`, `R-...`).
- Follow the project contract (`fleet.md`, `AGENTS.md`, `README.md` contracts along changed paths, and pinned shared rules in `$FLEET_INPUT_DIR/context-sources/` when present).

For Fleet visual work, check normal routes and semantic changes that affect copy, visibility, ordering or actions, including server-derived behavior. Under [Design first](../../../docs/design-system/README.md#design-first), cite the owner selection, exact candidate, affected scopes and matching evidence in the report. Unmapped impact and missing runtime comparisons are open findings. The code-review status attests only to the reviewed head.

## Depth levels

- `quick`: obvious bugs, compile/type issues, diff hygiene (`git diff --check`), and test results.
- `medium` (default): full diff review on both axes below, edge cases, contract adherence, error/retry paths, and targeted test coverage.
- `thorough`: deep cross-module audit of invariants, concurrency, security boundaries, failure/recovery paths, and documentation contracts.

## Workflow

1. **Ensure clean branch state and inspect the diff**
   - Fetch the base branch when Git network access is available: `git fetch origin <base>` (use the repository's default branch, typically `main`).
   - Verify clean status (`git status --short`), exact head (`git rev-parse HEAD`), merge base (`git merge-base origin/<base> HEAD`), whitespace/conflict hygiene (`git diff --check origin/<base>...HEAD`), and commit list (`git log --oneline origin/<base>..HEAD`).
   - Inspect `git diff --stat origin/<base>...HEAD` and read all changed files and relevant callers/contracts in full context.

2. **Evaluate both review axes**
   - **Task fit and acceptance**: all requested behavior and edge cases implemented; no unrequested scope or narrowed owner intent; required tests/docs/contracts updated.
   - **Implementation quality and rules**: correctness, shallow single-purpose functions without duplicated logic, security/secret/path boundaries preserved, error handling and retry safety, UI/design rules when UI changed, and project verification checks run and passing on the exact reviewed commit.

3. **Resolve findings before pushing**
   - Fix any confirmed blocking finding, re-run focused verification checks, and commit the fix.
   - Re-review the updated `origin/<base>...HEAD` diff and record the new exact 40-character `HEAD` SHA and total findings fixed.

4. **Persist exact-SHA review evidence**
   - Write the review report to a gitignored working-paper path inside the working area (for example `.egm/code-review-<shortsha>.md` or `output/code-review-<shortsha>.md`) so `git status --short` stays clean of tracked changes.
   - Include:
     - Exact 40-character lowercase `head_sha`
     - Base ref and merge-base SHA
     - Review level (`quick` | `medium` | `thorough`) and count of findings fixed
     - Verdict, reviewed scope, concrete findings/disposition, and exact checks run
   - Persist the file via `artifact_save` (`path`, `name`, `media_type: "text/markdown"`) and verify via `artifact_list` that its `state` is `"stored"`. Record its `artifactId`.

5. **Publish via `project-publishing` (or compatibility alias `project-forge`, when granted)**
   - `publish_draft` with `requestId`, `title`, `body`, and `expectedHeadSha` to push the reviewed branch and open or update the draft PR/MR.
   - `record_code_review` with `requestId`, `expectedHeadSha` (40-char lowercase SHA), `artifactId` (stored review artifact ID), and single-line `description` (`<= 140` bytes, e.g. `"medium, 0 findings fixed"`).
   - `mark_ready` with `requestId` and `expectedHeadSha` to mark the PR/MR ready for review.
   - `request_merge` with `requestId` and `expectedHeadSha` to enable auto-merge / merge-when-pipeline-succeeds / enter the merge queue (when `integration.autoMerge` is granted).
   - `status` to confirm `isDraft: false`, `code-review` success on `headSha`, and active auto-merge or merge queue state.
