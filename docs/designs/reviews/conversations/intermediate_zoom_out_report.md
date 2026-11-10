# Stage 8: Intermediate Zoom Out Review

Target: `docs/designs/fleet-conversations.md` (full file), after the Stage 5-7 fixes. Reviewer: `egm-zoom-out`. Date: 2026-11-10.

Verdict: direction holds (reuse, no invented limits, no shims); the fixes added overlap that is now removed.

| # | Severity | Finding | Resolution |
|---|---|---|---|
| Y1 | Major | Two stream-closing mechanisms; coordinator became a second producer of text blocks. | Fixed: `supersedes` dropped; `execution.ended` carries no text; a cut-off stream is shown from its `event-delta` rows at read time. |
| Y2 | Major | Live insert rule contradicted late-row placement. | Fixed: insert fed entries inside `[first, last]` or after `last` when `hasAfter = false`; pill counts only entries past `last`. |
| Y3 | Major | "Size limit" returned in CUJ 6 and plan item 22. | Fixed: `Range` pages. |
| Y4 | Nit | Feed delivers documents, not block keys. | Fixed. |
| Y5 | Nit | First-byte type check is extra code beside `nosniff`, sandbox and CSP. | Fixed: dropped. |
| Y6 | Nit | Hidden-project boundary row not needed; a task's executions are in its project. | Fixed: plain scope filter. |
| Y7 | Nit | Duplicate sentence in plan item 4. | Fixed. |
| Y8 | Nit | Membership query index path. | Fixed: filter by session set through the index, join `execution` for page rows only. |

New accepted trade-off: a reply cut off before its final block is not found by search (Risks table).
