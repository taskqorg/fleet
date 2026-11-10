# Zoom-out review: fleet-lab.md (2026-11-10)

Reviewer: `egm-zoom-out`. Verdict before fixes: major revisions.

1. Major: `execution` has no `request_id` column (`schema.sql:72-100`). Fix: migration 11 adds it with partial unique index; launch = insert on conflict do nothing.
2. Major: scope is ~5 projects. Fix: A/B + rating + promote first; loop and WSL can split out.
3. Major: Promote invents catalogue revisions; `recommendation_set` / `project-recommendation` path exists (`projects.ts:70`). Fix: reuse it.
4. Major: agent-side cost pull-back is a second accounting path, against G7. Fix: `mcp.py serve` writes the same `tool_run` + cost record.
5. Major: budget model too wide. Fix: `trials`, `pipe_credits`, `wall_ms` at experiment scope first.
6. Major: automatic rating triggers speculative. Fix: owner opens queue; sampling later.
7. Major: coordinator writes approved examples into git-tracked config. Fix: one source of truth.
8. Nit: infra failure belongs on `execution.problem`, not `score`.
9. Nit: `priority`, `iteration`, `strategy` columns premature.
10. Nit: line-level detail inside Technical Plan.
