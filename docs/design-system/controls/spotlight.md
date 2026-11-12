# Spotlight palette

**Code.** `Spotlight`, `sectionProvider` and the `SpotlightProvider` type in `packages/ui`; the app's providers in `app/src/main.tsx`; the server operation `search` (`go/coordinator/search.go`, `contracts/ops-people.schema.json`).

**Purpose.** Find and open anything from anywhere ([OR-32](../owner-rules.md)). It is the only floating working tool, an exception to OR-14.

**Anatomy.** A floating panel near the top of the window (OR-16 surface): a search field, then results grouped by provider (Sections, Tasks, Questions, Projects, Hosts). A result is the kind's registry icon, the title, the entity code when the kind has one ([D17](../decisions.md#2026-11-10-d17-human-entity-codes)), and a muted context (a task's project).

**Providers.** Each kind of thing found is one provider: `{ id, label, find(query, signal) }`. Sections are found in the browser from the navigation items. Codes, tasks, questions, projects and hosts come from one `search` call, which agents call as the MCP tool of the same name (fleet.md R2, R7). A new kind is one more provider in `searchProviders` on the server, or in the app's list when it needs no server.

**Query.** Free text plus `field:value` terms: `agent`, `host`, `state`, `project` (`agent:claude host:mac flaky`). This is the list search syntax (OR-36): `searchWhere` in `go/coordinator/search.go` applies it to `task_page`, `sessions_list` and `artifact_list`, and `matchSearch` in `packages/ui/src/search.ts` to lists filtered in the browser. A term for a field a list lacks matches nothing. A provider that cannot apply a term finds nothing, so `agent:claude` lists tasks only. Every D17 code in the query (`T-FLT-42`, `Q-FLT-3`) is resolved first, old codes of a renamed project too, and shows the current code. Then tasks whose id or code starts with the text, then recently changed. A question opens its task.

**Behaviour.** Cmd+K (Ctrl+K elsewhere) or the Search item in the navigation opens it; Cmd+K again closes it. Results follow typing after 150 ms. Up and Down move, Enter opens the item in the work area (OR-27) and closes the palette, Escape or a click outside closes it and returns focus.

**Accessibility.** `role="dialog"` named "Search"; the field is a `combobox` controlling a `listbox` of `option`s with `aria-activedescendant`.
