---
name: pm-fleet
description: Resolve project context and manage granted Fleet settings, profiles, pipelines, Lab, projects and people. Use for project selection, missing focus or checkout, and Fleet configuration.
---

# Fleet management

- You act with your person's rights; `tools/list` shows available operations, not additional project authority. Never bypass denied grants, unavailable credentials or secret boundaries.
- Resolve the user's named or otherwise unambiguous target with `project_list` within existing grants. If the intended project is ambiguous, ask; do not silently pick the first project. If the target is granted but the session focus is missing or excludes it, call `focus_set {projectIds: ["<selected project id>"]}` yourself, then retry the intended context read. An out-of-focus context refusal for a verified granted target is recoverable configuration; it is not a grant denial.
- Focus is an observation/default filter, not a checkout. For repository work inspect the actual cwd; if it lacks the intended checkout, call `session_transition {projectId: "<selected project id>"}` in this same session. A queued transition applies after the current execution: end this turn, then continue the original request in the automatic next execution. Do not ask the person to perform setup you can complete under existing authority, and do not require a separate worker just to access the repository.
- Inspect actual project files for repository knowledge. An empty task search does not prove the files contain no answer. Distinguish accepted specifications from implementation and checked results.
- Diagnose refusals by their stated reason. Correct missing focus or checkout through the supplied operations within existing grants; report genuine unavailable authority without seeking a bypass. Before retrying a mutation with an unknown outcome, inspect its original result; do not repeat uncertain external effects.
- Settings: hosts (`hosts_list`, `hosts_update`), budgets (`budgets_list`, `budget_set`), subscriptions (`account_list`, `account_update`), connections (`connections_*`). People: `people_list`, then `people_set` with the whole list (owner only).
- Before a change, read the current value; after it, read it back and tell the person the previous and new value. Every change is audited (`setting_changes`).
- Secrets never pass through you: host registration, secret rotation, gateway tokens and take are the person's.
