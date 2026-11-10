# Design critic review: fleet-lab.md (2026-11-10)

Reviewer: `egm-design`. 0 blockers, 7 major, 3 nits.

1. Major: idempotency lacks `UNIQUE(request_id)` on `execution`. Fix: column + unique index + insert-on-conflict, or per-experiment lock.
2. Major: `attempt` undefined. Fix: owner "retry cell" action sets `attempt = max + 1`; no automatic retry.
3. Major: one stuck trial freezes the loop; no per-trial cancel. Fix: cancel action, cancelled counts as finished, excluded.
4. Major: budget scope ignores judge, strategy, drafting runs. Fix: all Lab executions carry `experiment_id` and count.
5. Major: agent-side Pipe cost arrives late. Fix: pull at step completion; unpulled shows "pending", never zero.
6. Major: "no gain" stop rule and S4 exit undefined. Fix: explicit params, one primary evaluator for loops.
7. Major: Promote writes repo-tracked `config/catalogue.json` at runtime. Fix: revision schema, atomic write, or DB.
8. Nit: assetq dataset conversion must precede Pipe removal.
9. Nit: schema details (split columns, enum ADD VALUE transaction rule, `priority` single value).
10. Nit: rejection reasons cite round 3 without trade-offs.

Also: Pipe token file is readable by the agent on the same host; acceptable for prototype, note in NFR.
