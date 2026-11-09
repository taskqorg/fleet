# Supported transport recovery milestone

2026-11-09. [Next-stage plan](next-stage-plan.md) is linked from the documentation index. Only the new link was staged; unrelated index/root/private research modifications remain untouched. Canonical shared-core/authority/observation requirements are adopted; specific recovery, onboarding and qualification slices remain candidate implementation choices.

## Implemented boundary

The closure incident `fa34505e-c55f-445f-afc5-2ae6b52705f6` had an already-ended native process and retained outbox blocked by deterministic409: closure reused resource sequence61. Existing automatic resend could not fix that conflict. Its prior manual SQL reconciliation and original private backup remain unchanged; this release does not fabricate a retrospective recovery record.

`transport_inspect` and `transport_authorize` now share functions across `/api/recovery/:operation`, authenticated MCP `/api/recovery-tools` and [web recovery](https://fleet.ufobe.com/recovery). The ordinary session links to recovery. Inspect returns known execution/error/commands/events/result metadata, conflict identity/digest, audit and explicit unknown host-journal/native reinspection status. It does not claim coordinator history is a host process probe.

The supported action is deliberately narrow: a distinct process.closed frame colliding with an existing resource observation. Host/execution ownership is authenticated before retaining a conflict. Operator authorization requires exact execution, conflict, frame SHA256 and idempotent request ID. It reserves a fresh sequence without editing existing events. The owning executor's existing automatic outbox retry sends identical original bytes; ingestion appends the unchanged native payload with original identity/sequence/digest linkage and records delivery. Original rejected frame stays retained internally; inspection exposes metadata, not raw native body. All other conflict types refuse repair. A reserved-sequence race is rejected, not silently renumbered.

Approval and delivery are transactionally audited. Repeated approval/resend is idempotent. They create no native/program command, never edit a native rollout or host journal, and never promote unknown into success. Ordinary validated receipts remain execution-state authority. Shared tools use existing operator bearer/Origin rules; machine credentials cannot authorize operator operations, and no native profile receives a new grant automatically. Existing program-worker unknown effects are inspectable but have no unsafe rerun/force-success button.

## Proof

- Build and all17 coordinator tests passed; focused final recovery/auth tests2/2 passed after display correction. They cover missing/wrong bearer, wrong Origin, wrong host, altered digest, unsupported frame, original event preservation, durable audit/idempotency, reload and unknown remaining unknown.
- `npx tsx tests/coordinator/recovery-browser.ts` runs the actual compiled executor against a local real Fleet coordinator and fake Codex executable, with one injected resource/closure transport conflict. Browser authorization/reload unblocks the unchanged retained outbox; exactly two fake native launches for two intended sessions, no replay, outbox0/unfinished0. Original collision/resource/result evidence remains retained. [Proof](../evidence/recovery-local-proof.json), [inspected screenshot](../evidence/recovery-desktop.png).
- The same fixture has two distinct provisioned fixture account homes and contexts. A native barrier requires both processes to start before either completes: actual overlap, not a queue mistaken for concurrency. Result bodies and native message events match only their own session/account. This proves native runtime overlap on this host, not paid model behavior, resource containment or a production load target.
- Existing same-session active-execution refusal and lab sequential trials remain unchanged and tested. Program workers remain serial per process. A finite general admission/concurrency limit remains a next design choice; this change adds no balancer.
- `python3 tests/executor/program_recovery.py` SIGKILLs only an isolated actual program worker at a controlled pre-effect fake transport boundary. Accepted/started/unknown remain, effectReplayCount0, working area absent, prior result survives, no live service disruption. [Proof](../evidence/recovery-program-proof.json).
- Failed local fixture setup attempts remain in their temporary roots; they initially failed to inject the intended conflict because of fixture column order. Native effects were fake. Browser checks also exposed missing UTF-8 metadata and the author CSS overriding hidden; both UI defects were corrected. The [pre-hidden-fix live screenshot](../evidence/recovery-live-before-hidden-fix.png) remains as evidence; it contains no entered key.

No new paid/model/provider call, login, credential change or live crash was used. The previous four website turns, successful Claude/native qualifications and account login were not repeated. Passing-fixture fake launch counts do not claim an aggregate count for earlier failed development fixtures.

## Live readback

Source `9bd1b8a14c024babe4b2bfe62a24e9bd8d2a137d`, release `candidate-683f0e88c04c`, archive SHA256 `683f0e88c04c39c8c84ae7e1d8426b1b635fc1ad60cefde3551fb45d5059fe8d`. Native executor unchanged at `21aa1379f58ff7ad80f0c11638cc2283d78d6ee48577947789aa2a072a0834fb`; tested local debug executor hash is in the isolated proof. Earlier releases `candidate-236931ff00c2` and `candidate-617a6e9687b4`, SQLite pre-release backup and all prior results remain retained. Git branch is `alexkirs/chore-org-repo-inventory`, not merged main. A later evidence-only commit changes no runtime bytes.

[Service/hash/source readback](../evidence/recovery-live-readback.json): coordinator PID1748759 active/NRestarts0; PDS program worker PID1748926 active/NRestarts1 (retained historical counter); Mac executor PID38850 still running/runs1; Mac program PID56554 running/runs1. Nonterminal coordinator executions0, Mac unfinished0/outbox0. Both test repository heads and site release directories are unchanged.

Actual authenticated public-web inspection of historical C returns completed,63 events and two retained artifacts, with zero new recovery conflicts/actions. That is truthful: the old incident had already been manually repaired before this feature. No live event or task was changed. [Browser metadata](../evidence/recovery-live-browser.json), [visually inspected live screenshot](../evidence/recovery-live.png). New recovery mutation is proved only in the isolated actual-executor fixture, not claimed as a live crash/recovery test. The proxy hides the unnecessary key field; direct local mode requires the operator key.

Staged files and coordinator archive were checked against old/current Fleet machine bearer bytes without printing values; no private raw evidence or credential file is published. Unrelated dirty/untracked work is preserved.

## Concrete next choices

1. Build the planned host-binding/onboarding UX over existing managed metadata, with observed readiness and native behavior/config roots. Secrets remain local; Claude Mac/Windows-only applies to children; Windows is not provisioned.
2. Decide a finite independent-session concurrency policy before load qualification. Preserve current same-session exclusion, lab sequence and serial program workers until an explicit change is required.
3. Add narrowly scoped host evidence inspection for other unknown effects only with a native supported source. Do not generalize this collision authorization into arbitrary event edits, execution success override or effect retry. Remaining resume/remote native/Windows gaps stay as listed in architecture coverage.
