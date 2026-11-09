# Stage 7: Critic review (fleet-operator-app.md, FeatureRange)

Reviewers: egm-design, egm-structural, egm-style. Totals: 2 Blockers, 12 Majors and 15 Nits.

## Blockers

1. **Migration number clash.** `schema.sql` already has migration 7. Number the new one 8 and refer to "migrations 1-7".
2. **No Risks and Mitigations section.** Add a table covering: change loss, notify load, scope, resync, an expired session, SSE buffering, a stale shell after a deploy, and IndexedDB on a shared device.

## Majors

1. **Change loss.** `seq` is not in commit order, and the overlap window shrinks under load. Use an xact-id watermark (`pg_current_xact_id` and `pg_snapshot_xmin`), or weaken the claim. (Same as Stage 5 M1.)
2. **Triggered tables and the trigger sketch.**
   * Make the table list the same in the diagram and in §4; include or exclude `execution_totals`.
   * Encode the `task_dependency` id.
   * Use `RETURNING seq`, `OLD` on DELETE, and a text cast.
3. **Unbounded notify and read load** from event ingest. Coalesce notifies, debounce the stream wake-up, filter kinds per stream, push count deltas, and give figures.
4. **Request scope does not exist yet.** The first slice uses process scope `*`. Name the Phase 6 mechanism (`SET LOCAL` per read).
5. **Resync detection from `min(seq)`.** Use an explicit `pruned_through` mark instead.
6. **Prune blocks GLOBAL writers.** Prune in batches outside GLOBAL.
7. **CUJ 5 has no write when a host goes quiet**, so no change fires. Name the staleness decider: a server sweeper or a client timer. Align the times.
8. **An expired Access session looks like "offline".** Treat a redirect or a 401/403 as signed out, separate from offline, and gate the store.
9. **SSE through the proxy chain is unverified.** Set the headers (`no-cache`, `X-Accel-Buffering: no`, no compression) and add a smoke check against the deployed chain.
10. **Alternatives are thin.** Give each one an H3 with its trade-offs, label rejections by decision (Dn), treat WebSocket as a normal rejected alternative, and add logical decoding.
11. **Goals are not testable.** Define the test setup and name the check file. Say verification "reports against targets" rather than passing on them.
12. **Unsupported claims** ("breaks every owner rule", "native app slow to build", "cursors stay valid"). Support or soften each.

## Nits

1. The screenshot link points at a folder.
2. Link `host-observation` to data foundation §11.
3. Wording of "stores as `unavailable`".
4. The placement of the Decisions section.
5. The guard should be a box in the diagram.
6. Multi-manifest PWA support differs by browser; list the supported browsers (Safari).
7. List the exact paths that skip the Bearer.
8. The at-rest risk of IndexedDB data.
9. The 7-day default is stated twice.
10. "updated 18h ago" breaks OR-2; use "offline · 18h".
11. Tautology: "existing ops where they exist".
12. Link the list-view server note.
13. "real" and "rot" are judgements.
14. "stays live" and "keeps grants in mind" are vague.
15. Header style differs from the sibling doc.
