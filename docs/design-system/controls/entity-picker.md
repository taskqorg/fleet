# Entity picker

**Code.** `EntityPicker` in `packages/ui` (planned; see [target IA & journeys](../../designs/fleet-ia-and-journeys.md)). Composes a compact token/chip field with a searchable [`CatalogueTree`](catalogue-tree.md).

**Purpose.** Replace ad-hoc flat `<Choice>` strips of 30+ buttons, raw `<select>` controls (`OR-9`), and manual JSON variant inputs with one uniform single- or multi-select selector for projects, agents, models, pipelines, use cases, and Lab examples.

**Anatomy.**
* **Field trigger:** compact labelled input box showing currently selected entities as removable chips (for single-select: entity icon + label + code; for multi-select: ordered chips e.g. `codex (gpt-6.2-sol) ×`, `claude-worker ×`, capped cleanly with `+N more` before wrapping into an unruly wall) and an inline `+ Select` / chevron trigger.
* **Selection surface (`OR-14` compliant):** never floats a complex form in a modal dialog. Either:
  * **Inline expansion** directly beneath the field inside the Work Area (`LabCreate`, `Catalogue` comparison, assignment forms), with an `always` auto-focused `SearchField` inside the opened picker surface; or
  * **Control-column picker** reusing the section's Control Column (`ListDetail`) while keeping focus restoration to the invoking trigger on close (`Escape` or `Done`).
* **Picker body:** searchable `CatalogueTree` (checkboxes in multi-select mode, single click-to-pick in single-select mode) with collapsed obsolete/variant folders by default and `OR-45` delta summary shown next to agents/variants so differences are readable while picking.

**States.** Closed (rest/hover/focus, disabled with `disabledReason`), Open (searching/filtered, empty match, loading, error with retry), Read-only (when access/evidence is unconfirmed, e.g. stale `Focus`).

**Behaviour.**
* Supports `mode="single" | "multiple"` and optional preset shortcuts at the top of the tree (for example in Lab: *"Codex vs Claude"*, *"1-step vs 3-step reviewed"*, which toggle a set of entities into selection with one click).
* Filtering matches ID, human name, project code, tags and `OR-45` overrides; archived items stay excluded unless explicitly toggled (`Include archived`).
* Restores keyboard focus to the invoking field trigger when closed (`WCAG 2.4.3`).

**Keyboard.** `Enter` or `Space` on the closed field opens the picker and focuses its filter input; `Backspace` on an empty inline filter removes the last chip (multi-select); `↑` / `↓` / `←` / `→` navigate the tree without leaving search ergonomics broken; `Space` toggles selection in multi-select; `Enter` confirms (single-select closes immediately); `Escape` closes and returns focus to the trigger.

**Accessibility.** Trigger exposes `aria-expanded`, `aria-controls`, and accessible count/selection description; chip remove buttons carry explicit `aria-label="Remove <name>"`; picker tree uses `role="tree"` with `aria-multiselectable` in multi-select mode.

**Do.** Use `EntityPicker` whenever a form chooses from > 5 projects, agents, models, pipelines, use cases or examples. Keep labels free of raw UUIDs (`OR-38`).
**Don't.** Render a horizontal wrap of 30+ `Choice` buttons (`Compare methods in Lab`); ask users to type JSON blobs just to pick a different profile/model in A/B runs; open floating modal dialogs with multi-field forms inside (`OR-14`).

**Used in.** `Lab` fast A/B launcher (`LabCreate`), `Library` comparison & skill rollout targets, `Projects` method assignment, `Conversations` start configuration (`StartPicker` model/project choice).
