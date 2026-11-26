# Navigation and columns

**Code.** `AppShell`, `SidePanel`, `ListDetail`, `Spotlight`, `SearchField` (`packages/ui`).

**Job.** Define Fleet's single navigation and information architecture across every section ([app shell](app-shell.md), [list and detail](list-detail.md), [owner rules](../owner-rules.md)).

## 1. Three-column roles

Catalogue sections follow one three-column structure (`OR-24`, `OR-41`, `OR-43`); Home and Live are full-width pages, and an open session is the conversation with an optional context panel (`OR-50`, `OR-51`):

* **Nav Rail** (`NavRail`): top-level section anchors, pinned sections (`OR-12`), and the signed-in user block with the version and the app update (`OR-42`, [board](../board/screens/shell.bl)). Toggled from the shell header without moving rows or icons (`OR-23`).
* **Control Column** (`size-ctl`): the section's single filterable catalogue (`VirtualList` or compact hand-built list inside `ListDetail`). Opening an item fills the Work Area while keeping the Control Column mounted: active filters, search query, and scroll position remain intact, and the opened row stays highlighted (`.sel`). Never early-return or replace the `ListDetail` Control Column when an item `id` is selected. Sections with no catalogue (`Runners`) omit the Control Column; `Overview` uses the Needs-you inbox as its Control Column. At narrow widths (`<= 1100 px`), CSS toggles visibility between the Control Column and the opened Work Area without unmounting state.
* **Work Area:** the opened item (`[data-ui="detail"]`) or, when idle, the section's concise summary or reference (`OR-27`).

<a id="side-panels"></a>

**Side panels (`OR-41`).** Every column that opens and closes beside the page is a [side panel](../controls/side-panel.md) (`SidePanel`): the Nav Rail on the left and the session's context panel on the right, and any later one. One implementation gives each the same behaviour:

* its toggle sits in the shell header at a fixed place: the left one first, right ones after search (`OR-42`, `OR-43`);
* a divider on its inner edge resizes it (drag, Left and Right, double-click resets);
* open or closed and the width are person settings on the server by panel key (`panels.<key>`, `widths.<key>`; the rail is `navCollapsed` and `widths.nav`), so a panel opens in the same place at the same width after a reload, in another session and on another page;
* opening or closing never moves the toggle, search or the rows being read (`OR-6`, `OR-23`);
* at phone width a right panel opens as a sheet from the same toggle and stores nothing.

A panel never gets its own toggle in a page head, its own resize or state kept in the browser or in the page.

## 2. Search hierarchy

* **Global Spotlight (`⌘K`, `OR-32`):** one cross-section search icon in the shell header (`OR-43`) that jumps to any entity or section and seeds list queries.
* **Icon-only local filter (`OR-31`):** list headers in the Control Column and detail sections use `SearchField` *without* `always`, staying an icon until focused or non-empty. Never place an always-expanded redundant search box in a section list header where it competes with Spotlight (`always` is reserved solely for dedicated picker subviews whose primary job is typing to choose, such as `LabCreate`'s picker).

## 3. Idle vs open Work Area

* **Idle (no item selected):** shows a concise section summary or reference — operational/resource `Totals`, breakdowns by harness or category, pending bulk sync or rollout actions (`Take repo`, Skills rollout), or a section reference such as Lab's Use-case registry. Never duplicate the Control Column list as a second `DataTable` in the idle Work Area ([list-detail](list-detail.md)).
* **Open (item selected, `/<section>/<id>`):** renders the item's detail view or editor (`DefinitionDetail`, `Experiment`, `TaskDetail`, `HostDetail`) in the `detail` slot of the same `ListDetail`, with an icon Close button (`OR-15`) returning to the section root.

<a id="4-closed-8-section-ia-and-column-causality"></a>

## 4. Navigation and column causality

The `NavRail` lists the seven sections of the [information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-information-architecture); until that rail is built it keeps the Work and System groups.

* **No second shell bar (`OR-46`):** Horizontal `<nav class="sub-nav">` bars above columns are forbidden. One `NavRail` and one `ShellHeader` serve every screen.
* **No mode tabs in the Control Column (`OR-46`, `OR-48`):** Object navigation inside `Lab` (`Use Cases`, `Runs`), `Library` (`Methods`, `Pipelines`, `Agents`, `Skills`), and `Infrastructure` (`Hosts`, `Accounts`, `Connections & API keys`, `Runners`) lives in one unified `CatalogueTree` inside the Control Column (`ListPagePanel` serves `Overview`, `Conversations`, `Projects`, `Tasks`, and `Delivery`; `Spend` and `Runners` omit a catalogue).
* **Work Area tabs never mutate the Control Column (`OR-47`):** Causality flows strictly left to right (`NavRail` → Control Column → Work Area). `TabBar` (`packages/ui`) is allowed only inside the Work Area for facets of a single opened entity (for example `Projects`: `Work & Rules · Methods · Delivery · Results`), and switching tabs never rebuilds or alters the Control Column.
* **Cross-section links:** Links to another section's objects (such as a Project linking to its recommended method in `Library`, or `Lab` promoting a winner to `Library → Methods`) navigate directly to the target section/entity URL instead of embedding a duplicate catalogue or sub-nav bar.

## Rules kept here

This spec is the one source of these rules (moved here on 2026-11-26; ids are kept, [owner rules](../owner-rules.md#rules-kept-by-a-component-or-a-check) and [principles](../principles.md#rules-kept-elsewhere) list where every rule lives). An `OR` rule keeps the owner's words.

* **OR-46 One nav level, no second shell bar.** One global navigation level (`NavRail`) and one `ShellHeader`. The rail has eight sections: Home, Live, History, Conversations, Projects, Lab, Library, System ([information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-8-section-information-architecture)). Horizontal `<nav class="sub-nav">` above columns is forbidden. Section object navigation lives in the Control Column. `TabBar` is allowed **only** inside the Work Area for facets of a single opened entity (for example a project's `Sessions · Tasks · Delivery · Results`).
* **OR-47 One-way causality & URL truth.** Strict left-to-right flow: `NavRail` → Control Column → Work Area. Work Area tabs never rebuild the Control Column. Every opened item (including Lab use cases, entity creation, `Limits`) has a URL and renders in the `detail` slot with an icon `Close` button in `.detail-h` (OR-15). `ListDetail` stays mounted while loading (skeleton inside the column, never an early return replacing `ListDetail`).
* **OR-48 Two catalogue engines only.** The Control Column uses strictly `ListPagePanel`, `CatalogueTree`, or none (`Spend`, `Runners`). Raw `<div class="vl-wrap">` catalogues in the Control Column are forbidden. Section actions (`New …`) sit in `<ShellActions>` (OR-43), not in a Control Column `<header class="page-h">`.
