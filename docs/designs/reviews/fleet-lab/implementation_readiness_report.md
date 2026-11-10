# Implementation readiness review: fleet-lab.md (2026-11-10)

Reviewer: `egm-implementation-readiness`. Verdict before fixes: not ready.

1. Blocker: no `UNIQUE(request_id)`; concurrent reconciles can double-launch. Fix: DDL + per-experiment lock.
2. Blocker: stop formula "no gain beyond spread", `attempt`, iteration-complete predicate, reconcile triggers undefined.
3. Blocker: pipeline v2 reference grammar (`$example.*`, `$steps.*`), step output schema, job-mode storage and resume undefined.
4. Blocker: agent-side cost pull-back from Pipe has no file, endpoint, trigger, match rule.
5. Major: routes lack schemas; state transitions, catalogue revision, agreement metric, blind order undefined.
6. Major: incomplete DDL (nullability, checks, indexes, DELETE predicate).
7. Major: stage ordering: Stage 0 items depend on Stage 2/3 pieces; S0 exit needs an experiment row.
8. Major: missing files (rating question handling, `claude_placement` storage, WSL login path, strategy scripts, `draft-examples`, assetq conversion, `mcp.py` path); item 13 "delete or rewrite" undecided.
9. Major: missing tests (v2 runner, connection client, script kind, WSL readiness, drift, ratings).
10. Nit: per-harness skills install unspecified; "N 2" typo.
