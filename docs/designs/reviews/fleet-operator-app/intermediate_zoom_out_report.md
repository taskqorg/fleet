# Stage 8: Intermediate zoom out (revised doc)

Reviewer: egm-zoom-out. Verdict: nearly ready. There are 0 Blockers, 1 Major, 5 Nits and 2 earlier items left open. All of them were fixed afterwards, as listed below.

| Finding | Fix applied |
|---|---|
| Major: every host poll rewrites `host-observation` with only `lastSeenAt` changed, which floods the feed. | The trigger skips updates that change only `lastSeenAt`. `/hosts` and the Overview re-read `hosts_list` on their 5 s timer. A test checks the skip. |
| Open: `list_membership` for hosts is promised but not built or tested. | Hosts are dropped from `list_membership`; `/hosts` re-reads `hosts_list`. Membership tests now cover tasks and sessions. |
| Open: the writer of `host-observation` is not named. | The `/api/hosts/poll` handler is named in the kind map and in CUJ 5. |
| Nit: the log records kinds no client uses. | Only mapped kinds write change rows. |
| Nit: the scope of the prune is unclear. | The prune takes its own lock scope, `change:prune`, not GLOBAL. |
| Nit: `/api/me` conflicts with G5. | It is now the read op `me`. `/api/stream` is the only non-op route, and G5 says so. |
| Nit: `rows_get` is unnecessary. | It is dropped; the existing get ops are used instead. |
| Nit: the heading of item 5 is missing `sessions.ts`. | Added. |
