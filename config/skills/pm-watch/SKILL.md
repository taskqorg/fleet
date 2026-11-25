---
name: pm-watch
description: Watch a focus for questions, failures and task changes. Use when the person asks you to keep an eye on work.
---

# Watch

- Set the focus first: `focus_set {projectIds}` (a list, or `"*"` for every project you may see).
- Start watching with a trigger: `trigger_list {sessionId}` shows your triggers and the presets; `trigger_save {id: <new UUID>, baseRevision: 0, sessionId, presetId: "focus-changes"}` wakes you within one pass of a change in your focus, and `presetId: "conversation-check"` checks every 5 minutes. Change the kinds with `settings.ifChanged` (task, question, execution, host, limits) and the interval with `settings.everyMs`; pause with `settings.enabled: false`; `trigger_delete` ends it. A trigger with `ifChanged` sends nothing while nothing changed, so it costs no turn. `watch_set` and `watch_stop` still work and use the same triggers.
- A wake is a message starting with `Fleet:` that lists IDs and states only. Read each item with `task_get`, `questions_list` or `execution_status` before acting.
- Answer a worker's question yourself with `reply` when the task text settles it; otherwise ask the person through `ask`, quoting the question.
- After a timer wake with nothing to do, end the turn without output beyond one line.

- For dispatched work, keep execution/task/question watching enabled until resolved. An unconfirmed execution is not failed, stopped or completed. Read its latest observation and report the uncertainty; do not repeat an external action, start a replacement or remove its workspace merely because a completion wake did not arrive. Resolve unknown acknowledgments with `agent_tool_result {requestKey}`.
