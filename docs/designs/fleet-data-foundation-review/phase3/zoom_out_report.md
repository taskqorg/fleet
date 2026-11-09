# Stage 5: Initial Zoom Out (Phase 3 artifact scope)

Scope: §3 Artifact store (lines 368-418), §8 artifact tools (492-518), Phase 3 items 21-29 (1039-1140) of `docs/designs/fleet-data-foundation.md` at main cb36446. Reviewers: egm-zoom-out, egm-inventive-catalyst. Context: `live-findings.md`, `docs/infrastructure-build.md`.

## egm-zoom-out (verdict: does not pass yet)

1. Blocker: local writes and local copies are routed through Cloudflare (presigning for the tunnel hostname), so CUJ 5's "never leaves it" is false and the tunnel cost (19.4 s vs 0.5 s for 80 MiB) is paid on the common path. Fix: `store.endpoint` (tunnel) plus `local_endpoint` (loopback); presign for loopback when the user of the URL is the store's host; control plane and cross-host reads through the tunnel.
2. Blocker: agent tools hand presigned tunnel URLs to agents, which cannot pass Cloudflare Access without the service token (a credential, forbidden by G3/G8). Fix: one `artifact_save` tool, executor uploads with its own token; `artifact_get` returns a local path, fetched by the executor when needed.
3. Major: post-signing Access headers missing; coordinator side not covered. Fix: SDK middleware after signing, tunnel endpoints only; test that `cf-access-client-id` is never in SignedHeaders.
4. Major: whole-file hash verification for multipart objects unproven (composite checksums). Fix: per-part `x-amz-checksum-sha256`, compare composite; or verify by streamed re-hash; prove against Garage 2.5.0 first.
5. Major: static `FLEET_STORES_FILE` contradicts finding 3 and G11. Fix: executor `store_register` outbox row carrying endpoints, zone, capacity and the coordinator key; coordinator keeps the key in its secret directory.
6. Major: Windows availability (logon-bound) and real capacity (free space on C:) not modelled. Fix: effective capacity = min(Garage capacity, backing disk free); readiness "store requires logon"; CUJ 5 states availability.
7. Major (YAGNI): retention rules engine has no CUJ. Fix: keep tombstones and copy-then-delete; defer `artifact_rules`, preview/confirm, runRules.
8. Major: §3 commit via outbox vs item 26 synchronous route. Fix: pick one.
9. Nit: `artifact.store_id`/`object_key` duplicate `artifact_copy`; derive.
10. Nit (YAGNI): lab/production overrides on `artifact_locations`; defer.
11. Nit: daily `runAudit` re-hash: off by default until needed.
12. Nit: item 26b has two Why lines; item 6 and the Phase 3 test plan still treat Q2 as open.

## egm-inventive-catalyst

1. Major: one endpoint for every caller (tunnel cost). Segment data plane (loopback for the store's own host) from control plane (tunnel). Liability: URL bound to its host; test.
2. Major: credentials copied into two places, registry hand-written. Inversion: executor registers its store through the outbox; executor needs no S3 keys (presigned URLs only). Liability: credential-bearing registration needs host authentication, overwrite rules, rotation.
3. Major: Access headers vs signing. Extraction: one transport layer per side (SDK `finalizeRequest` middleware; `ureq` wrapper), tunnel only; pinned-version test.
4. Major: verified hash vs transfer cost. Per-part checksums, composite compare; audit re-hash by the executor on the store's host over loopback; prove Garage behaviour first (Q1).
5. Major: strict placement vs a store offline when the user logs out. Option: a `staging` copy (artifact `stored`, not `placed`) unless the project is strict; dynamic capacity from host metrics with hysteresis.
6. Nit: `artifact_get` "local path" has no meaning for Garage chunks; executor read cache, or a loopback URL.
7. Nit: purge "from every store at once" cannot hold with an offline store; make purge a persistent `purge_pending` per copy reconciled on reconnect.

Works as is: content-addressed immutable keys, copy-then-delete, 32 MiB multipart with a resume journal, coordinator-driven placement loop.
