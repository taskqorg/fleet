---
name: pm-watch
description: Watch a focus for questions, failures and task changes. Use when the person asks you to keep an eye on work.
---

# Watch

- Set the focus first: `focus_set {projectIds}` (a list, or `"*"` for every project you may see).
- In a bot conversation about one subject, a focus watch sees changes only for tasks whose managed bot link names this conversation. Fleet records that link when you create a task in the bot's project or use `bot_task_link` from this bot run for an existing task. For a problem conversation (`problem:<id>`), the bot's problem trigger already wakes you. Use `problem_update {id, work}` to record the fix in the problem. Bot conversations cannot attach to a task.
- Start watching with a trigger: `trigger_list {sessionId}` shows your triggers and the presets; `trigger_save {id: <new UUID>, baseRevision: 0, sessionId, presetId: "focus-changes"}` wakes you within one pass of a change in your focus, and `presetId: "conversation-check"` checks every 5 minutes. Change the kinds with `settings.ifChanged` (task, question, execution, host, limits) and the interval with `settings.everyMs`; pause with `settings.enabled: false`; `trigger_delete` ends it. A trigger with `ifChanged` sends nothing while nothing changed, so it costs no turn.
- A wake is a message starting with `Fleet:` that lists IDs and states only. Read each item with `task_get`, `questions_list` or `execution_status` before acting.
- Answer a worker only when the task text settles the question or your directing person delegated that decision, and the question's addressee and decision rules let you answer. Apply this rule to both tools below. Visibility and tool access alone give no authority to decide. Otherwise leave the decision with the authorized addressee. When your directing person needs to decide, use `ask` and quote the worker's question. If you link the ask to a task managed by another bot, Fleet routes it to that bot.
- For an agent question, read `questions {sessionId, state: "open"}` and use its `id` in `reply {questionId, answer}`. The question must be in your session or one you started. A bot can also reply in its own bot sessions and sessions attached to tasks it created or manages.
- For a task or project question, including an operator decision, read `questions_list` or `question_history` as applicable. Use the visible question's `id` in `questions_answer {questionId, answer}` if the tool is available and you are authorized to answer through that route. The same conditions apply to agent questions outside your `reply` session scope. If the tool is unavailable, route the question to the authorized addressee.
- After a timer wake with nothing to do, end the turn without output beyond one line.

- For dispatched work, keep execution/task/question watching enabled until resolved. An unconfirmed execution is not failed, stopped or completed. Read its latest observation and report the uncertainty; do not repeat an external action, start a replacement or remove its workspace merely because a completion wake did not arrive. Resolve unknown acknowledgments with `agent_tool_result {requestKey}`.
