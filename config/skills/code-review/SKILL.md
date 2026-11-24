---
name: code-review
description: Whole-branch code review for Fleet managed workers and reviewers against origin/<base>...HEAD, producing exact-SHA review evidence for project-forge code_review.
---

# Code review

Perform a thorough whole-branch review of `origin/<base>...HEAD` inside the managed execution worktree and produce auditable exact-SHA review evidence for `project-forge` `delivery` (`action: "code_review"`).

## Rules

- **Never** run nested `codex review` or sub-harness CLIs inside a managed Fleet sandbox (`Operation not permitted` in sandboxed executions and no Fleet artifact provenance).
- Review the **entire branch diff** (`origin/<base>...HEAD`), not only the latest commit.
- Anchor the report to the exact 40-character lowercase `HEAD` commit SHA (`git rev-parse HEAD`). Any later commit invalidates prior review evidence and requires a fresh review and artifact.
- Follow the project contract (`fleet.md`, `AGENTS.md`, `README.md` contracts along changed paths, and pinned shared rules in `$FLEET_INPUT_DIR/context-sources/` when present).

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

5. **Publish via `project-forge` (when granted)**
   - `publish_draft` with PR `title` and `body` to push the reviewed branch and open or update the draft PR.
   - `delivery` with `action: "code_review"`, `head_sha` (40-char lowercase SHA), `artifact_id` (stored review artifact ID), and single-line `description` (`<= 140` bytes, e.g. `"medium, 0 findings fixed"`).
   - `delivery` with `action: "ready"` to mark the PR ready for review.
   - `delivery` with `action: "auto_merge"` to enable auto-merge / enter the GitHub merge queue (when `integration.autoMerge` is granted).
   - `delivery` with `action: "status"` to confirm `isDraft: false`, `code-review` success on `headRefOid`, and active `autoMergeRequest` or `mergeQueueEntry`.
