# Navigation and columns

**Code.** `AppShell`, `ListDetail`, `Spotlight`, `SearchField` (`packages/ui`).

**Job.** Define Fleet's single navigation and information architecture across every section ([app shell](app-shell.md), [list and detail](list-detail.md), [owner rules](../owner-rules.md)).

## 1. Three-column roles

Every screen follows one three-column structure (`OR-24`, `OR-41`, `OR-43`):

* **Nav Rail** (`NavRail`): top-level section anchors, pinned sections (`OR-12`), and the signed-in user block (`OR-42`). Toggled from the shell header without moving rows or icons (`OR-23`).
* **Control Column** (`size-ctl`): the section's single filterable catalogue (`VirtualList` or compact hand-built list inside `ListDetail`). Opening an item fills the Work Area while keeping the Control Column mounted: active filters, search query, and scroll position remain intact, and the opened row stays highlighted (`.sel`). Never early-return or replace the `ListDetail` Control Column when an item `id` is selected. Sections with no catalogue (`Runners`) omit the Control Column; `Overview` uses the Needs-you inbox as its Control Column. At narrow widths (`<= 1100 px`), CSS toggles visibility between the Control Column and the opened Work Area without unmounting state.
* **Work Area:** the opened item (`[data-ui="detail"]`) or, when idle, the section's concise summary or reference (`OR-27`).

## 2. Search hierarchy

* **Global Spotlight (`⌘K`, `OR-32`):** one cross-section search icon in the shell header (`OR-43`) that jumps to any entity or section and seeds list queries.
* **Icon-only local filter (`OR-31`):** list headers in the Control Column and detail sections use `SearchField` *without* `always`, staying an icon until focused or non-empty. Never place an always-expanded redundant search box in a section list header where it competes with Spotlight (`always` is reserved solely for dedicated picker subviews whose primary job is typing to choose, such as `LabCreate`'s picker).

## 3. Idle vs open Work Area

* **Idle (no item selected):** shows a concise section summary or reference — operational/resource `Totals`, breakdowns by harness or category, pending bulk sync or rollout actions (`Take repo`, Skills rollout), or a section reference such as Lab's Use-case registry. Never duplicate the Control Column list as a second `DataTable` in the idle Work Area ([list-detail](list-detail.md)).
* **Open (item selected, `/<section>/<id>`):** renders the item's detail view or editor (`DefinitionDetail`, `Experiment`, `TaskDetail`, `HostDetail`) in the `detail` slot of the same `ListDetail`, with an icon Close button (`OR-15`) returning to the section root.

## 4. Closed 8-section IA and column causality

Per the [target information architecture](../../designs/fleet-ia-and-journeys.md#1-closed-8-section-information-architecture), the `NavRail` holds **8 top-level sections** in two groups (`Work`: `Overview`, `Conversations`, `Projects`, `Tasks`, `Spend`; `System`: `Lab`, `Library`, `Infrastructure`):

* **No second shell bar (`OR-46`):** Horizontal `<nav class="sub-nav">` bars above columns are forbidden. One `NavRail` and one `ShellHeader` serve every screen.
* **No mode tabs in the Control Column (`OR-46`, `OR-48`):** Object navigation inside `Lab` (`Use Cases`, `Runs`), `Library` (`Methods`, `Pipelines`, `Agents`, `Skills`), and `Infrastructure` (`Hosts`, `Accounts`, `Connections & API keys`, `Runners`) lives in one unified `CatalogueTree` inside the Control Column (`ListPagePanel` serves `Overview`, `Conversations`, `Projects`, `Tasks`, and `Delivery`; `Spend` and `Runners` omit a catalogue).
* **Work Area tabs never mutate the Control Column (`OR-47`):** Causality flows strictly left to right (`NavRail` → Control Column → Work Area). `TabBar` (`packages/ui`) is allowed only inside the Work Area for facets of a single opened entity (for example `Projects`: `Work & Rules · Methods · Delivery · Results`), and switching tabs never rebuilds or alters the Control Column.
* **Cross-section links:** Links to another section's objects (such as a Project linking to its recommended method in `Library`, or `Lab` promoting a winner to `Library → Methods`) navigate directly to the target section/entity URL instead of embedding a duplicate catalogue or sub-nav bar.
