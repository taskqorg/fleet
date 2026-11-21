# Catalogue tree

**Code.** `CatalogueTree` in `packages/ui` (planned; see [target IA & journeys](../../designs/fleet-ia-and-journeys.md)). Built on [VirtualList](data-list.md) sizing and virtual rendering.

**Purpose.** Navigate structured libraries and hierarchical collections (1–3 levels) in the Control Column without flattening combat defaults, variants, suites and archived entries into one noisy list: agents grouped by role, use cases grouped by benchmark suite, Lab runs grouped by series, and unified `Library` sections.

**Anatomy.** Vertical virtualized tree in the Control Column (`size-ctl`, one browser scroller per OR-25), sharing row tokens with [data list](data-list.md):
* **Pinned group (`OR-12`):** pinned leaf items appear in a compact top group before folders regardless of expand/collapse state.
* **Folder / collection row:** disclosure chevron (`chevron-right` / `chevron-down`), collection icon (`OR-3`), label, item count (`tabular-nums`, muted), and optional aggregate badge (for example best score on a benchmark suite). Clicking the chevron toggles expand/collapse; clicking the row body opens the collection's Work Area view (for example, suite leaderboard or folder summary) while keeping the folder selected.
* **Leaf row (`size-row`, indented per level ≤ 3):** leftmost [state badge](state-badge.md) (`OR-22`), primary name + compact ID (`OR-38`), and a single short distinguishing marker on the right (`OR-45` delta badge such as `read-only` or `recommended`, or latest score / run count). Whole row opens the item in the Work Area (`OR-26`).
* **Collapsed archive folder:** stale/obsolete groups (`Lab variants`, `Archived / Obsolete`) render collapsed by default at the bottom of the tree.

**States.**
* Row: rest, hover, selected (`.sel` with accent left bar), focused, problem (`attention-soft` fill).
* Tree: expanded/collapsed folder state persisted per person via `person_settings` (never browser-only, `OR-33`/`OR-41`), empty/loading/error ([status states](status-states.md)).
* When a local `SearchField` filter (`OR-31`) or `Focus` is active, folders containing matching items auto-expand for the query and empty folders hide without mutating the user's persisted collapse preferences.

**Behaviour.**
* Renders the flattened array of currently visible nodes through `VirtualList` (`OR-9` scale-proof past 1,000+ entries).
* Depth never exceeds 3 levels (`Group → Entity → Revision/Example/Variant`); deeper metadata belongs in the Work Area, not extra tree indentation.
* Reuses row menu (`OR-37`) for Pin and secondary actions; no hover action buttons appear inside rows.

**Keyboard.** `↑` / `↓` (or `k` / `j`) move focus across visible rows; `→` expands a collapsed folder or moves to its first child; `←` collapses an expanded folder or jumps from a child to its parent folder; `Enter` / `Space` opens the focused folder summary or leaf item; `Home` / `End` jump to first/last visible node.

**Accessibility.** `role="tree"` container with `role="treeitem"`, `aria-level` (`1..3`), `aria-expanded` on folders, and `aria-selected` on the open item; single roving tab stop (`tabIndex={0}` on active item). Live filter count updates a polite status region.

**Do.** Use `CatalogueTree` for bounded structural catalogues (agents, pipelines, methods, skills, use-case suites, Lab series). Keep node labels short and let the right chip name only what differs from default (`OR-45`).
**Don't.** Nest deeper than 3 levels; embed full tables or descriptions inside tree rows; use `CatalogueTree` for chronological streams (`Tasks`, `Conversations`, transcripts), which belong in `VirtualList`.

**Used in.** `Library` (Methods, Pipelines, Agents & Skills), `Lab` (Use Cases & Runs), `Infrastructure` (`Hosts / Accounts / Connections / Runners`), and inside [`EntityPicker`](entity-picker.md).
