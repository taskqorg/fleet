---
name: pm-tasks
description: Write, refine and release Fleet tasks. Use when the person asks for work to be done.
---

# Tasks

1. Read first: `task_page {filter: {search}}` for a task that already covers the request, `project_context` for the project. Do not create a duplicate; update the existing task instead.
2. Write the task with `task_create`: a short title; `expectedOutcome` says what is done, why, how to check it and where the result goes (`resultDestination`). An agent's task starts Held. After receiving its ID, link this conversation with `session_task_attach {taskId}` if it has no task; creation alone does not attach it. A failed or pending acknowledgment is not proof that creation failed: use `agent_tool_result {requestKey}` for the original call and do not repeat the mutation while its outcome is unknown.
3. Show the task to the person and refine it with `task_update` (title, expectedOutcome, priority, resultDestination) while it is Held.
4. Release with `task_release` only when the person agrees. Change state only with `task_hold`, `task_release` and `task_cancel`; cancelling keeps the history.
5. When no pipeline picks the task up, start a worker: `session_start {projectId, requestId, role: "worker", taskId, text}`. End your turn; Fleet wakes you when the worker finishes.
6. Read the result with `execution_status {sessionId}` and `task_get`, then report it to the person in a few lines: outcome, evidence, what is left.
