# Codex resume developer-context repair

> Historical record. It was written against the TypeScript coordinator and the Rust and Python executor, which the Go port deleted; file names in it are not current code. Current code: `go/coordinator`, `go/executor`, `go/gateway` ([stack](designs/fleet-stack.md)).

2026-11-09. Zero native/model turns or native thread operations were made in this dispatch. Existing saved native histories and the failed A/B evidence remain unchanged. The repair is source-backed and deterministically exercised through the actual executor; native B acceptance is still pending a separately authorized budget.

## What failed and why

The previous live candidate completed both turns but retained A in model-visible developer history. Its outgoing wire was not retained, so the historical run cannot retrospectively prove the exact emitted bytes. That limitation remains explicit.

The Fleet path resolves the trusted profile, maps profile instructions into CLI configuration and Session.profile, constructs developerInstructions on both opens and keeps task text in turn/start. Current actual-pipe fake checks verify the emitted A/B bytes through that entire executor path, rather than only checking Session helper construction. The live failure is consistent with the following native reconstruction behavior, rather than evidence that supplying the schema field updates model-visible context automatically.

Version-matched primary source, OpenAI rust-v0.167.0 (saved tree commit 086ccc833bc9cf01024ad2dd6af7dbd421b6bf9a):

- [thread_processor.rs](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/app-server/src/request_processors/thread_processor.rs#L4205): resume passes developer_instructions to build_thread_config_overrides; lines 1781–1810 preserve that override. Configuration acceptance is real.
- Config::load precedence in the already retained config.rs is developer_instructions.or(cfg.developer_instructions); supplied typed configuration wins over file configuration. This does not rewrite historical messages.
- [session/mod.rs](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/core/src/session/mod.rs#L1710): resumed initialization calls apply_rollout_reconstruction and restores recorded history/reference state.
- [initial context](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/core/src/session/mod.rs#L4803): the configured supplied text enters the developer bundle when building full initial context.
- [normal context updates](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/core/src/session/mod.rs#L5195): full initial context is built only when no reference snapshot exists; otherwise world-state diffs and turn-context contributors are emitted. build_turn_context_contribution_items at 4749 does not reissue this configured supplied developer text. The saved A/B turn_context records likewise contain no supplied developer-text field.

These source files were fetched into /tmp/fleet-codex-0167-source; the tag thread_processor.rs and session/mod.rs were byte-compared with the saved-tree commit versions. An initial request for the obsolete core/src/codex.rs path returned 404; that missing path is not evidence that the tag is unavailable. No external narrative source or source-code copy is used.

AgentQ d0f6bfa registry/codex/app.rs open forwards caller params unchanged to start/resume, and adapter.rs supplies explicit developerInstructions. Those intended mappings do not contain a developer history-update operation and are not independent native qualification of changing that text on resume. Fleet keeps its authority/journal rather than importing AgentQ policy or state.

## Supported narrow repair

The installed generated ClientRequest schema declares **thread/inject_items**, with the documented semantics of appending raw Responses items without starting a user turn. ThreadInjectItemsParams requires threadId/items. Its method uses an underscore, not an assumed camel-case spelling.

[turn_processor.rs:1027](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/app-server/src/request_processors/turn_processor.rs#L1027) loads the existing thread, checks direct-input authority, deserializes typed ResponseItems and calls inject_response_items. It does not submit a turn. The upstream [thread_inject_items test](https://github.com/openai/codex/blob/rust-v0.167.0/codex-rs/app-server/tests/suite/v2/thread_inject_items.rs#L103) explicitly injects role=developer/input_text and checks there are no model requests before a separate turn/start, then checks retained history/role behavior.

Fleet now performs initialize → thread/resume → thread/inject_items(exact current supplied developer text) → acknowledged success → turn/start(actual task text). Fresh execution retains its native initial developer context and does not inject an extra copy. Empty supplied text does not imply removal of old history; it does not inject an empty replacement. Existing context and native identity are preserved. There is no user-message substitute, rollout edit, context reset, replacement thread or frozen-profile rule.

Injection is an explicit additive developer history update, not deletion of earlier developer instructions. Later supplied text can supersede conflicting earlier text at the same role. Clearing or erasing retained instructions/history is not claimed. If injection is rejected or its acknowledgement is lost/unknown, Fleet does not submit/retry the user turn or inject again automatically; existing unknown/reconciliation policy applies. Native effective behavior after this repair still requires qualification.

## Private wire evidence

Each Codex execution creates `<execution>.protocol-out.jsonl` in its session directory with create_new and mode 0600 before spawning. Each serialized NDJSON frame is written and sync_data completed before the same bytes enter ChildStdin. This records outbound intent even when delivery is uncertain. The file can contain sensitive user/profile content, is host-private, is outside output artifact collection and must not be published as a normal result. Actual credentials are never added to these protocol requests; auth remains native-home owned.

Only protocol.write metadata enters common retained history: fixed method name or response, numeric request ID when available, bytes, SHA256, and explicit “written to local transport; native acceptance unknown.” Bodies, paths, instructions, tool arguments and credential data are not copied into that metadata. Native acknowledgements remain separately retained in incoming raw JSONL. A successful local pipe write is not represented as native acceptance.

## Deterministic proof and next qualification

`cargo test codex::tests` passes fresh/resume instruction mapping, acknowledged injection ordering, foreign-turn refusal, pending duplicate suppression and inbound approval refusal. `tests/executor/codex_wire.py` runs the actual built executor against an external fake executable and local HTTP coordinator: fresh A, same-session resume B, then fake injection refusal C. It asserts exact received NDJSON equals private persisted outbound bytes; A/B open text and B developer item are exact; model/effort and separate task input survive; B inject precedes its sole turn; refused C has no turn/start and settles unknown; metadata contains no instruction bodies. All fixture processes are fake; no installed Codex process, native handshake/read/resume or paid/model request is launched by this test.

The existing failed native qualification thread remains read-only in this dispatch. Parent may separately authorize one bounded repaired resume on that qualification thread/workspace with a new execution ID and current marker B, using the existing temporary registry/homes. Collect outbound resume and injection bytes/ack, developer-role B in native rollout, actual B file readback, same native identity, model/effort, completion and stdout/process closure. Do not retry a failed/unknown injection or turn. Alternatively authorize a new explicit two-turn fresh/resume pair; do not silently replace the saved thread. Account provisioning gaps are independent and unchanged.

Fixed local debug executor SHA256: `5eece6b61e3d9421ea4cbc31863a1dc5dd68afe7cac2e80e0a6c398f14e89d5b`. Focused checks: 2 Codex unit checks passed; actual-executor fake-wire A/B/refusal check passed with retained fixture root `/var/folders/sw/55r1dvwn2g30l77zs5nwp3_80000gn/T/fleet-codex-wire-6c8rc_dp`; cargo build and git diff --check passed. No UI code changed in this dispatch, so no new screenshots were needed. No commit, push, deployment, service/auth/global configuration change or native history mutation occurred.

Subsequent separately authorized [single repaired native resume](multiharness-repaired-native-evidence.md) passed B acceptance on candidate 5eece6b; it preserves the earlier failure/source investigation as historical evidence.
