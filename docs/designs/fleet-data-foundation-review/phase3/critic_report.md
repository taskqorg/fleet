# Stage 7: Critic Review (Phase 3 artifact scope, FeatureRange)

Reviewers: egm-design, egm-structural, egm-style (feature-spec standard). Ranges: §3 (368-418), §8 (492-518).

## egm-design (does not pass yet; 2 Blockers, 8 Majors, 3 Nits; confirms Stage 5/6 findings)

1. Blocker: uploads go straight to the final hash key; CompleteMultipartUpload on an existing key replaces it before verification, so a bad or racing upload overwrites a verified copy. Fix: upload to a staging key, verify, copy to the hash key only if absent, delete staging; dedupe at reserve when a verified copy exists; test.
2. Blocker: no failure model between reserve and commit (crash, expired URLs, abandoned discard): open multipart uploads leak space, rows stay pending. Fix: reservation lease, worker aborts expired uploads (rows failed), periodic ListMultipartUploads sweep, discard only pending and always aborts; state machine `pending -> stored | failed`.
3. Major: object key has scope (lab|prod) but `artifact` has no scope column; delete reference check keyed wrongly. Fix: `artifact.scope`, reference check by (project, scope, hash).
4. Major: delete safety uses stale `verified_at`; HEAD surviving copies before deleting, require one reachable surviving copy, never delete a last copy outside purge.
5. Major: a purged hash can come back via re-upload or an offline store. Fix: purged-hash deny list checked at reserve and placement; `purge_pending` per copy reconciled on reconnect.
6. Major: presigned URLs issued once at reserve expire during long or resumed uploads (about 4 MiB/s via tunnel). Fix: presign parts on demand; journal stores upload id and part ETags/checksums only.
7. Major: "stays running" up to 24 h after the agent exits holds slots and breaks orphan detection and totals. Fix: distinct finalizing state or wait reason; outbox owns retries.
8. Major: collecting files the agent is still writing has no stability rule. Fix: collect only files unchanged for N seconds, skip temp names, final snapshot at run end, transcript chunks as separate artifacts.
9. Major: ffmpeg on untrusted media under the executor service user breaks the isolation boundary. Fix: run as the account user or a low-privilege user, no network, time and memory limits.
10. Major: agent tools need execution-level permission (only own execution's artifacts); `required` decided by task/completion requirements, not by the agent.
11. Nit: coordinator rw key for every store is a single point of compromise; state it, executors hold no S3 keys, rotation per store.
12. Nit: "first reachable store" ignores full and free space; choose reachable, not full, most free capacity.
13. Nit: `artifact_get` path must be inside the agent's working area, readable by the account user.

## egm-structural (changes required; 8 Majors, 7 Nits)

1. Major: §3 order (entities after the rules that use them). Proposed order: artifact, object keys, store, artifact_copy and states, connectivity, transfers, write protocol, success rule, reading, locations/local first/placement, shared/purge/retention, previews, transcripts, no size limit.
2. Major: Q2 open and answered at once (lines 152, 190, 262); move to answered, fix FD2.
3. Major: live proof rules missing from §3 (Access headers after signing; loopback part URLs; control plane via tunnel; credentials from the store's host via registration; WHEN_REQUIRED). Add "Store protocol rules".
4. Major: Windows availability (logged out) and capacity (C: free space) not modelled; Risks entry.
5. Major: read path defined three times differently (§3, §6, §8); one rule in §3.
6. Major: store selection contradictory (Local first vs write protocol); the coordinator chooses.
7. Major: no place declares required artifacts; define in task completion requirements or the launch spec.
8. Major: §8 tools have no matching §6 operations (prepare, commit, discard, list, purge); add a tool-to-operation table.
9-15. Nits: collection timing stated three ways; `store_id` meaning; "per project and scope"; settings scattered between §3 and §11 (`artifact_retry_window`, capacity, `full`); `artifact_discard` result undefined; Terms lacks artifact vocabulary; Risks/NFR lack the tool byte budget and provenance pins. Outside range: §2 delivery table rows 361-362 mis-indented.

## egm-style (7 Majors, 7 Nits)

Majors (calibrate claims to the proof): presigned URLs always on the tunnel (373, 399, 412); "pass Access with a service token" (373); where credentials live (370); `placed` not in `artifact.state` (372, 406); Windows offline cases (376); "never leave it" holds only for a one-host list (381); "limits are the store's disk" (417); §8 "local path" (506).
Nits: name the Cloudflare body limit (374); "does not slow agents" (387); unnamed lab/prod setting and selection rule (371, 411); "files larger than a single request" (502); "long runs"/"chunks" undefined (498, 416); repeated "real", unquantified "short"/"byte budget" (513-517).
