# Project settings decisions (2026-11-09)

Owner input plus coordinator recommendation. Every item below is a per-project setting; this file records only the defaults. Principle: the system helps and never stalls silently, and every failure is visible.

1. **Setup:** wait for setup before the agent starts (`setupStart: wait`). Setup runs automatically. When setup fails or a credential or binding has expired, Fleet raises an *attention item* and does not hide or retry it. The attention item is shown to the operator or architect and can be assigned to an agent.
2. **Integration:** default `mode: pr` (branch + PR). This gives a visible review point, works with CI, and makes rollback trivial. `mode: direct` is available for scratch or test repos.
3. **Merge:** default `autoMerge: true` once checks pass, with `mergeMethod` taken from the repo default (squash when unset). Optional settings: a reviewer-agent role, or manual merge by the owner.
4. **Checkout sync:** the coordinator does not touch host checkouts. After a merge, the coordinator emits a *base updated* event. Each host executor then fetches and fast-forwards its main checkout if it is clean; otherwise it raises an attention item.
5. **Teardown failure:** keep the worktree, record the logs, and raise an attention item. Cleanup is a later service procedure; nothing is deleted silently.

**One general mechanism:** attention items (setup failed, auth expired, teardown failed, unknown effect, merge conflict). They are visible in the web UI and to agents through the same API and MCP. An optional supervisor agent can subscribe and act on them, which is the "system intelligence" layer. Policy is not hardcoded.
