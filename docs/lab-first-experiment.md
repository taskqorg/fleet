# First configuration experiment

Implements [experiments](experiments.md) using the existing execution path. Owner request: «Хорошо, давай составь план и оркестрируй выполнение вот этого плана. Протестирую, что вот это все работает.» The accepted scenario compares the same website task with and without an MCP design-rules tool.

## Scope

Approved clarification: «Наверное, инструменты и инструкции пользоваться этими инструментами ... неотделимой части». This experiment compares a baseline against tools plus their usage instructions as one configuration change; no third variant is required. Serena and other repository-aware tools are deferred beyond this version.

Two repository-owned agent profiles select the same Codex model, gpt-6.1-sol, low, subscription, existing isolation and host. Baseline has no MCP; the other grants a read-only design-rules MCP. Both run the same task in fresh sessions/workspaces. Profile content is retained with each execution; edits affect new launches, not historical records. Preserve ordinary session follow-up behavior.

The task requests output/index.html and output/style.css and asks the agent to consult project design rules when available. Exact rule values are delivered by the tool, not copied into the task or profile instructions. A distinct heading, palette and layout permit deterministic result checks. Rules are experiment data, not built-in Fleet policy.

## Work plan

1. Runtime: first prove independence from personal harness instructions, memory, MCP, skills and hooks while preserving authorized subscription use; do not change personal configuration. Remove the unjustified blanket session-profile freeze, retaining per-execution settings and specific native limitations. Then define a small JSON profile schema and command payload contract; apply instructions and MCP through the native adapter; retain requested/effective configuration and native call/result evidence. Add a minimal read-only stdio MCP and repository profiles/data. Reuse installed/native mechanisms before dependencies.
2. Coordinator and web: load repository profiles, create an experiment with identical task text and fresh trial sessions, dispatch through existing commands, and persist comparison and operator notes. Expose profile differences, links to complete histories, previews, elapsed time, raw native usage with scope, and available resource summaries with coverage.
3. Integration: deploy only Fleet candidates, preserving existing sessions and rollback artifacts. Run baseline and configured trials through the public web UI on the Mac. Inspect both retained results, MCP call/result and effective settings. Close/reopen the comparison and verify persistence.

## Shared boundary

Command payload gains `agentConfig`: `{id, model, effort, instructions, mcpServers}`. `mcpServers` initially holds logical grants such as `{id:"design-rules", data:{...}}`; host-owned adapter mapping supplies the executable. Browser input cannot supply arbitrary executable paths, environment secrets or native CLI flags. Coordinator resolves repository profiles and retains their full nonsecret content and digest. Executor validates supported fields, applies them, and emits effective preparation evidence; do not claim unsupported settings applied.

Existing payloads without agentConfig keep current behavior during deployment overlap. Profile selection is per fresh experiment trial; no native identity or working directory is shared across variants. Baseline and configured task text must match. MCP tool output is retained through native history or explicit invocation evidence, not inferred from an assistant statement. Runtime worker owns the final exact schema in `contracts/lab-config.json` for coordinator adoption; report any required revision before integration.

## Acceptance

| Metric | Evidence | Decision |
|---|---|---|
| Configuration applied | Resolved profile, native launch mapping, effective preparation | Does profile selection change the agent? |
| MCP called | Named tool call, arguments, response, execution linkage | Did the agent use its configured capability? |
| Rules applied | Retained HTML/CSS checks plus actual browser preview | Did tool information affect the result? |
| Duration and usage | Execution timestamps and native counters with scope | What measurable work differed? |
| Resources | Existing CPU/RSS/disk observations with coverage | What is observed versus missing? |
| Human assessment | Persistent comparison note | What should we change next? |

Focused local checks cover profile validation/application, MCP protocol and call evidence, independent trial identity, durable history/comparison, and existing auth/preview behavior affected by the change. Parent owns real native/deployed acceptance. Record failure as failure; a retried trial keeps its original history.

No statistical superiority claim follows from one pair. Cost, child-tree resource attribution and missing measurements remain unknown. No optimizer, general workflow editor, new telemetry backend or unrequested provider is introduced.
