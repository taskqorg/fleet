# Runtime boundary review

Source review, 2026-11-09. Accepted ownership is defined in [runtime integration](runtime-integration.md). Existing code is evidence, not a template or runtime qualification.

## Evidence

Read the local AgentQ contract and selected runtime sources at `37498efab19dcf91b5014952d28b0b389a392cea`. The checkout has an unrelated `taskq.json` modification; no AgentQ files were changed or native sessions launched.

- [Product contract](https://github.com/taskqorg/agentq/blob/37498efab19dcf91b5014952d28b0b389a392cea/agentq.md): native-agent preparation, execution and history; consuming products own scheduling and UI.
- [Runtime source](https://github.com/taskqorg/agentq/blob/37498efab19dcf91b5014952d28b0b389a392cea/runtime/src/agent/adapter.rs): launch inputs include working directory, model, account context, instructions, MCP and ordered input parts. `Runtime` exposes history, launch, progress, reports and controls; several optional operations default to explicit unsupported results.
- [Runtime contract](https://github.com/taskqorg/agentq/blob/37498efab19dcf91b5014952d28b0b389a392cea/docs/spec/RUNTIME.md): native protocol mappings and capability-dependent behavior. This is stated behavior, not fresh native proof.

## Source coverage

The same revision's `runtime/src/agent/events.rs` distinguishes question and approval requests, choice and free-text answers, native image evidence and progress events. `Control` in `adapter.rs` includes replies and structured answers. Codex and Claude registry entries declare different capability sets; a declaration is not proof of every runtime mode.

These sources support investigating the three scenarios below without inventing a separate executor for each. They do not prove complete native event delivery, child usage attribution or behavior after reconnection.

## Next qualification package

For each selected harness and mode, inspect supported input, streamed history, images, questions, permission responses, continuation, interruption, completion and usage reporting. Then exercise three bounded scenarios: interactive refinement, autonomous assignment with a routed question, and budgeted improvement with a child execution.

Specify unsupported behavior and available continuation paths from observed evidence. No live execution, account change or paid experiment was performed by this review.
