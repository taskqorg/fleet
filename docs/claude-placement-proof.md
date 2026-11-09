# Claude desktop-only placement proof

2026-11-09. The new orchestration message repeats an account constraint already implemented and deployed; it does not require another login or paid qualification. Parent subsequently confirmed the dedicated login, and actual Mac qualification is retained in [connected live evidence](connected-live-evidence.md), execution `4474aff0-bf92-4e4a-ad04-20d514339fbd` (MCP → Write → Read, exit0/stdout closed). No new native/model/auth action was performed for this follow-up.

Current boundaries:

- Coordinator account observations reject Claude Linux bindings with403; admission resolves the managed account again for every direct or delegated execution and rejects an ineligible persisted binding.
- Delegated children inherit the parent account and pass through the same enqueue/admission function. Cross-harness substitution is rejected.
- Native executor independently refuses Claude on its ineligible build target before launch. The current executor is Mac; Windows is permitted by product policy but not provisioned, built or qualified here.
- Account management renders the account's login/placement guidance, including “Mac/Windows only”, with observed host platform/readiness. Creating a record does not mark it authenticated. The PDS coordinator does not imply a PDS native launch.

Added one focused deterministic regression in `tests/coordinator/multiharness.test.ts`: reject a Linux account observation; admit a fake parent under a desktop binding; mutate only the isolated fixture's persisted binding to Linux; prove both delegated child and new direct execution receive403. Execution count remains exactly the original fake parent and delegation count0. This tests the shared admission boundary after a stale/migrated binding, without running any native process or touching live state.

`npx tsx --test tests/coordinator/multiharness.test.ts`: 3/3 passed. Runtime code/UI were already present, so no service deployment/restart was needed. The live source/binary hashes remain those in the connected report; this follow-up changes only a deterministic test and this evidence note. No credentials were read, copied, exported or placed on PDS. Windows and Claude resume remain unqualified.
