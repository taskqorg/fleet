# Stage 9: Implementation Readiness (Phase 3 items 21-29, before fixes)

Reviewers: egm-implementation-readiness, egm-codebase-details, egm-style. Verdict: not ready (the items predated Stages 5-8).

Blockers: wrong migration number (3; migrations 1-7 exist, use 8) and missing DDL, RLS, grants, purge list, lease columns; item 22 static store file contradicts live finding 3; item 24 leaves signing, endpoint split, multipart hash, overwrite protection, on-demand presigning, leases, and the success rule hook open, and the hook named (`executions.transition`) is not on the receipt path (the receipt writes `put('execution')`, `executions.ts` `putExecution`); item 26b anchor wrong and many base64 readers unlisted (`app.ts` differences and results, `delegation.ts` sources, `projects.ts` pipe response, `delivery.ts` command payload, `tasks.ts` and `delegation.ts` SQL on artifact documents, `delivery_worker.py` uploads, `server.ts` limit, tests); no backfill item for base64 documents and `migrate-sqlite`; placement copy command schema and verification undefined; agent contract (§8) still exposes presigned URLs.

Majors: route anchors and request bodies; store calls inside the write lock (executor 5 s timeout); Access middleware on the coordinator client; reachability and capacity source (poll body); executor call types (synchronous reserve, outbox commit, separate agent for parts) and journal table; collection rules (extension list, name pattern, upload errors failing runs); project store check input; reuse of the store adapter layer for legacy `all('artifact')` readers and `migrate-sqlite`; acceptance `artifactIds` use legacy ids; missing test plan and live acceptance criteria; ffmpeg capability via poll `capabilities`, not `readiness.rs`.

Style: single vs multipart rule; composite checksum ambiguity; state names; undefined "the list", "writer's host", "live", operator settings; retry and chunk limits; tunnel hostname vs `endpoint`; preview artifact fields.

All of these are addressed in the revised §3, §8 and items 21-30 except the decisions listed in `consolidated_review_report.md`.
