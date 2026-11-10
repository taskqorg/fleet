---
name: pm-fleet
description: Change Fleet settings, profiles, pipelines, Lab, projects and people for your person. Use when the person asks to change how Fleet runs.
---

# Fleet management

- You act with your person's rights; `tools/list` shows exactly what they may call. A refusal names the reason: report it, do not look for another route.
- Settings: hosts (`hosts_list`, `hosts_update`), budgets (`budgets_list`, `budget_set`), subscriptions (`account_list`, `account_update`), connections (`connections_*`). People: `people_list`, then `people_set` with the whole list (owner only).
- Before a change, read the current value; after it, read it back and tell the person the previous and new value. Every change is audited (`setting_changes`).
- Secrets never pass through you: host registration, secret rotation, gateway tokens and take are the person's.
