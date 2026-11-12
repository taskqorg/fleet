# Spotlight palette

**Code.** `Spotlight`, `sectionProvider` and the `SpotlightProvider` type in `packages/ui`; the app's providers in `app/src/main.tsx`; the server operation `search` (`go/coordinator/search.go`, `contracts/ops-people.schema.json`).

**Purpose.** Find and open anything from anywhere ([OR-32](../owner-rules.md)). It is the only floating working tool, an exception to OR-14.

**Anatomy.** A floating panel near the top of the window (OR-16 surface): a search field, then results grouped by provider (Sections, Tasks, Projects, Hosts). A result is the kind's registry icon, the title, the entity code when the kind has one ([D17](../decisions.md#2026-11-10-d17-human-entity-codes)), and a muted context (a task's project).

**Providers.** Each kind of thing found is one provider: `{ id, label, find(query, signal) }`. Sections are found in the browser from the navigation items. Tasks, projects and hosts come from one `search` call, which agents call as the MCP tool of the same name (fleet.md R2, R7). A new kind is one more provider in `searchProviders` on the server, or in the app's list when it needs no server.

**Query.** Free text plus `field:value` terms: `agent`, `host`, `state`, `project` (`agent:claude host:mac flaky`). This is the list search syntax (OR-36). A provider that cannot apply a term finds nothing, so `agent:claude` lists tasks only. A task whose id starts with the text comes first (jump by code); then tasks recently changed first.

**Behaviour.** Cmd+K (Ctrl+K elsewhere) or the Search item in the navigation opens it; Cmd+K again closes it. Results follow typing after 150 ms. Up and Down move, Enter opens the item in the work area (OR-27) and closes the palette, Escape or a click outside closes it and returns focus.

**Accessibility.** `role="dialog"` named "Search"; the field is a `combobox` controlling a `listbox` of `option`s with `aria-activedescendant`.

**Not yet.** Task codes (`T-FLT-42`) resolve once tasks carry codes (OR-38); until then a task is found by title or id prefix and no code is shown.
