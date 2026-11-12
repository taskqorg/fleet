---
name: pm-watch
description: Watch a focus for questions, failures and task changes. Use when the person asks you to keep an eye on work.
---

# Watch

- Set the focus first: `focus_set {projectIds}` (a list, or `"*"` for every project you may see).
- Start watching: `watch_set {kinds: ["question", "execution", "task"], timerMs}`. `watch_get` shows wakes and the last error; `watch_stop` ends it.
- A wake is a message starting with `Fleet:` that lists IDs and states only. Read each item with `task_get`, `questions_list` or `execution_status` before acting.
- Answer a worker's question yourself with `reply` when the task text settles it; otherwise ask the person through `ask`, quoting the question.
- After a timer wake with nothing to do, end the turn without output beyond one line.

- For dispatched work, keep execution/task/question watching enabled until resolved. An unconfirmed execution is not failed, stopped or completed. Read its latest observation and report the uncertainty; do not repeat an external action, start a replacement or remove its workspace merely because a completion wake did not arrive. Resolve unknown acknowledgments with `agent_tool_result {requestKey}`.
